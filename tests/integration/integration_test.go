//go:build integration

// Интеграционные тесты выполняют настоящие SQL-файлы проекта в отдельной базе search_test
// на небольшом наборе данных, где каждое правило из задания проверяется своим сценарием.
//
// Запуск: go test -tags=integration ./tests/integration/
// Адрес и пароль берутся из CLICKHOUSE_ADDR, CLICKHOUSE_USER и CLICKHOUSE_PASSWORD.
package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/EWEEEL/search-tags/internal/search"
	"github.com/EWEEEL/search-tags/internal/storage"
	"github.com/EWEEEL/search-tags/internal/web"
)

const testDatabase = "search_test"

var baseTime = time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

type logRow struct {
	ts     time.Time
	userID uint64
	query  string
}

// fixture накапливает строки логов. Каждый сценарий получает своих пользователей,
// поэтому сценарии не влияют друг на друга.
type fixture struct {
	rows   []logRow
	nextID uint64
}

// session добавляет пользователя, который искал запросы с указанными паузами перед каждым следующим.
func (f *fixture) session(gaps []time.Duration, queries ...string) {
	f.nextID++
	ts := baseTime
	for i, query := range queries {
		if i > 0 {
			ts = ts.Add(gaps[i-1])
		}
		f.rows = append(f.rows, logRow{ts: ts, userID: f.nextID, query: query})
	}
}

// users добавляет count пользователей, которые искали first, а через минуту second.
func (f *fixture) users(count int, first, second string) {
	f.usersWithGap(count, time.Minute, first, second)
}

func (f *fixture) usersWithGap(count int, gap time.Duration, first, second string) {
	for range count {
		f.session([]time.Duration{gap}, first, second)
	}
}

func buildFixture() *fixture {
	f := &fixture{}

	// Правило 6: порог в 5 пользователей.
	f.users(5, "порог", "ровно пять")
	f.users(4, "порог", "всего четыре")

	// Правило 3: не больше 30 минут включительно.
	f.usersWithGap(5, 30*time.Minute, "интервал", "ровно полчаса")
	f.usersWithGap(5, 30*time.Minute+time.Second, "интервал", "на секунду дольше")
	f.usersWithGap(5, 30*time.Minute+time.Millisecond, "интервал", "на миллисекунду дольше")

	// Правило 2: пара только из соседних запросов.
	for range 5 {
		f.session([]time.Duration{time.Minute, time.Minute}, "первый", "второй", "третий")
	}

	// Правило 4: после нормализации это один запрос, поэтому пары «повтор → повтор» нет.
	for range 5 {
		f.session([]time.Duration{time.Minute, time.Minute}, "Повтор", "повтор ", "после повтора")
	}

	// Правило 5: бот с сотней повторов считается одним пользователем.
	bot := make([]string, 0, 200)
	botGaps := make([]time.Duration, 0, 199)
	for range 100 {
		bot = append(bot, "бот", "спам")
		botGaps = append(botGaps, time.Minute, time.Minute)
	}
	f.session(botGaps[:len(bot)-1], bot...)
	f.users(3, "бот", "спам")

	// Правило 1: разные написания сливаются в один запрос и один тег.
	spellings := [][2]string{
		{"ПЛАТЬЕ", "КРАСНОЕ ПЛАТЬЕ"},
		{"Платье ", "Красное Платье"},
		{" платье", "красное  платье"},
		{"  ПлАтЬе  ", " красное   платье "},
		{"платье", "красное платье"},
	}
	for _, s := range spellings {
		f.users(1, s[0], s[1])
	}

	// Правило 7: 12 кандидатов, в топ попадают 10, при равенстве порядок задаёт tag.
	for i, count := range []int{6, 5, 8, 5, 5, 7, 5, 5, 5, 5, 5, 5} {
		f.users(count, "топ", fmt.Sprintf("тег %02d", i+1))
	}

	// Порядок определяется временем запроса, а не порядком вставки.
	for range 5 {
		f.nextID++
		f.rows = append(f.rows,
			logRow{ts: baseTime.Add(time.Minute), userID: f.nextID, query: "позже"},
			logRow{ts: baseTime, userID: f.nextID, query: "раньше"},
		)
	}

	// Запросы разных пользователей пару не образуют.
	for i := range 5 {
		f.nextID++
		f.rows = append(f.rows, logRow{ts: baseTime.Add(time.Duration(i) * time.Second), userID: f.nextID, query: "чужой"})
		f.nextID++
		f.rows = append(f.rows, logRow{ts: baseTime.Add(time.Duration(i)*time.Second + time.Millisecond), userID: f.nextID, query: "сосед по времени"})
	}

	// Спецсимволы в значениях: такие запросы должны находить свои теги через сервис.
	f.users(5, `обратный\слеш`, `тег со\слешем`)
	f.users(5, `кавычка'`, `тег с 'кавычкой'`)

	// Запрос из одних пробелов не считается запросом и не разрывает пару.
	for range 5 {
		f.session([]time.Duration{time.Minute, time.Minute}, "до пробелов", "   ", "после пробелов")
	}

	return f
}

type tagRow struct {
	Query string
	Tag   string
	Users uint64
	Rank  uint8
}

type env struct {
	conn  driver.Conn
	store *storage.ClickHouse
	opts  storage.Options
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()

	getenv := func(key, fallback string) string {
		if value := os.Getenv(key); value != "" {
			return value
		}
		return fallback
	}
	opts := storage.Options{
		Addr:     getenv("CLICKHOUSE_ADDR", "localhost:9000"),
		Database: testDatabase,
		Username: getenv("CLICKHOUSE_USER", "app"),
		Password: getenv("CLICKHOUSE_PASSWORD", "clickhouse"),
	}

	admin, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{opts.Addr},
		Auth: clickhouse.Auth{Username: opts.Username, Password: opts.Password},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, statement := range []string{
		"DROP DATABASE IF EXISTS " + testDatabase,
		"CREATE DATABASE " + testDatabase,
	} {
		if err := admin.Exec(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}

	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{opts.Addr},
		Auth: clickhouse.Auth{Database: opts.Database, Username: opts.Username, Password: opts.Password},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+testDatabase); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		conn.Close()
	})

	runSQLFile(t, conn, "01_create_logs.sql")

	batch, err := conn.PrepareBatch(ctx, "INSERT INTO search_logs (timestamp, user_id, search_query)")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range buildFixture().rows {
		if err := batch.Append(row.ts, row.userID, row.query); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	runSQLFile(t, conn, "03_build_tags.sql")

	store, err := storage.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	return &env{conn: conn, store: store, opts: opts}
}

// runSQLFile выполняет файл из папки sql по одному выражению.
func runSQLFile(t *testing.T, conn driver.Conn, name string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "sql", name))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), ";\n") {
		if !hasSQL(statement) {
			continue
		}
		if err := conn.Exec(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v\n%s", name, err, statement)
		}
	}
}

func hasSQL(statement string) bool {
	for _, line := range strings.Split(statement, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "--") {
			return true
		}
	}
	return false
}

func (e *env) tags(t *testing.T, query string) []tagRow {
	t.Helper()
	ctx := clickhouse.Context(context.Background(), clickhouse.WithParameters(clickhouse.Parameters{"query": storage.EscapeParameter(query)}))
	rows, err := e.conn.Query(ctx, "SELECT query, tag, users, rank FROM search_tags WHERE query = {query:String} ORDER BY rank")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	result := []tagRow{}
	for rows.Next() {
		var row tagRow
		if err := rows.Scan(&row.Query, &row.Tag, &row.Users, &row.Rank); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func (e *env) tagNames(t *testing.T, query string) []string {
	t.Helper()
	names := []string{}
	for _, row := range e.tags(t, query) {
		names = append(names, row.Tag)
	}
	return names
}

func assertTags(t *testing.T, e *env, query string, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := e.tagNames(t, query); !reflect.DeepEqual(got, want) {
		t.Errorf("теги для %q = %v, want %v", query, got, want)
	}
}

func TestBuildRules(t *testing.T) {
	e := setup(t)

	t.Run("порог в 5 пользователей", func(t *testing.T) {
		assertTags(t, e, "порог", "ровно пять")
	})

	t.Run("ровно 30 минут входят, больше нет", func(t *testing.T) {
		assertTags(t, e, "интервал", "ровно полчаса")
	})

	t.Run("только соседние запросы", func(t *testing.T) {
		assertTags(t, e, "первый", "второй")
		assertTags(t, e, "второй", "третий")
		assertTags(t, e, "третий")
	})

	t.Run("повтор после нормализации не пара", func(t *testing.T) {
		assertTags(t, e, "повтор", "после повтора")
	})

	t.Run("считаются пользователи, а не события", func(t *testing.T) {
		assertTags(t, e, "бот")
		assertTags(t, e, "спам")
	})

	t.Run("написания сливаются", func(t *testing.T) {
		got := e.tags(t, "платье")
		want := []tagRow{{Query: "платье", Tag: "красное платье", Users: 5, Rank: 1}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("топ-10 со стабильным порядком при равенстве", func(t *testing.T) {
		got := e.tags(t, "топ")
		want := []tagRow{
			{"топ", "тег 03", 8, 1},
			{"топ", "тег 06", 7, 2},
			{"топ", "тег 01", 6, 3},
			{"топ", "тег 02", 5, 4},
			{"топ", "тег 04", 5, 5},
			{"топ", "тег 05", 5, 6},
			{"топ", "тег 07", 5, 7},
			{"топ", "тег 08", 5, 8},
			{"топ", "тег 09", 5, 9},
			{"топ", "тег 10", 5, 10},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v\nwant %+v", got, want)
		}
	})

	t.Run("порядок по времени, а не по вставке", func(t *testing.T) {
		assertTags(t, e, "раньше", "позже")
		assertTags(t, e, "позже")
	})

	t.Run("разные пользователи пару не образуют", func(t *testing.T) {
		assertTags(t, e, "чужой")
	})

	t.Run("запрос из пробелов пропускается", func(t *testing.T) {
		assertTags(t, e, "до пробелов", "после пробелов")
		assertTags(t, e, "")
	})

	t.Run("повторная сборка даёт тот же результат", func(t *testing.T) {
		before := e.tags(t, "топ")
		runSQLFile(t, e.conn, "03_build_tags.sql")
		if after := e.tags(t, "топ"); !reflect.DeepEqual(before, after) {
			t.Errorf("before %+v, after %+v", before, after)
		}
	})

	t.Run("инварианты результата", func(t *testing.T) {
		checks := map[string]string{
			"query = tag":     "SELECT count() FROM search_tags WHERE query = tag",
			"users < 5":       "SELECT count() FROM search_tags WHERE users < 5",
			"больше 10 тегов": "SELECT count() FROM (SELECT query FROM search_tags GROUP BY query HAVING count() > 10)",
		}
		for name, query := range checks {
			var violations uint64
			if err := e.conn.QueryRow(context.Background(), query).Scan(&violations); err != nil {
				t.Fatal(err)
			}
			if violations != 0 {
				t.Errorf("%s: нарушений %d", name, violations)
			}
		}
	})
}

func TestStoreAndAPI(t *testing.T) {
	e := setup(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := httptest.NewServer(web.NewServer(e.store, 3*time.Second, logger).Handler())
	defer api.Close()

	fetch := func(t *testing.T, q string) (int, string) {
		t.Helper()
		response, err := http.Get(api.URL + "/api/tags?q=" + url.QueryEscape(q))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(body)
	}

	t.Run("«Платье » и «платье» дают один ответ", func(t *testing.T) {
		_, a := fetch(t, "Платье ")
		_, b := fetch(t, "платье")
		if a != b {
			t.Errorf("%s != %s", a, b)
		}
		var response struct {
			Tags []search.Tag `json:"tags"`
		}
		if err := json.Unmarshal([]byte(a), &response); err != nil {
			t.Fatal(err)
		}
		want := []search.Tag{{Tag: "красное платье", Users: 5}}
		if !reflect.DeepEqual(response.Tags, want) {
			t.Errorf("tags = %+v, want %+v", response.Tags, want)
		}
	})

	t.Run("теги идут в порядке rank", func(t *testing.T) {
		tags, err := e.store.Tags(context.Background(), "топ")
		if err != nil {
			t.Fatal(err)
		}
		if len(tags) != 10 || tags[0].Tag != "тег 03" || tags[9].Tag != "тег 10" {
			t.Errorf("tags = %+v", tags)
		}
	})

	t.Run("без тегов пустой список и 200", func(t *testing.T) {
		status, body := fetch(t, "такого запроса нет")
		if status != http.StatusOK || !strings.Contains(body, `"tags":[]`) {
			t.Errorf("status %d, body %s", status, body)
		}
	})

	t.Run("пустой q даёт 400", func(t *testing.T) {
		if status, _ := fetch(t, "   "); status != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
	})

	t.Run("спецсимволы передаются как значение, а не как SQL", func(t *testing.T) {
		for _, q := range []string{
			`' OR 1=1 --`,
			`платье' OR '1'='1`,
			`'; DROP TABLE search_tags; --`,
			`\`,
			`a\b`,
			`a\'b`,
			`{query:String}`,
			`%s`,
			"строка\x00с нулём",
		} {
			status, body := fetch(t, q)
			if status != http.StatusOK || !strings.Contains(body, `"tags":[]`) {
				t.Errorf("q=%q: status %d, body %s", q, status, body)
			}
		}
		if got := e.tags(t, "топ"); len(got) != 10 {
			t.Errorf("таблица изменилась: %d строк", len(got))
		}
	})

	t.Run("запросы со спецсимволами находят свои теги", func(t *testing.T) {
		tests := map[string]search.Tag{
			`Обратный\Слеш `: {Tag: `тег со\слешем`, Users: 5},
			`КАВЫЧКА'`:       {Tag: `тег с 'кавычкой'`, Users: 5},
		}
		for query, want := range tests {
			tags, err := e.store.Tags(context.Background(), search.Normalize(query))
			if err != nil {
				t.Fatalf("%q: %v", query, err)
			}
			if !reflect.DeepEqual(tags, []search.Tag{want}) {
				t.Errorf("%q: tags = %+v, want %+v", query, tags, want)
			}
		}
	})

	t.Run("истекший таймаут возвращает ошибку контекста", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
		defer cancel()
		time.Sleep(time.Millisecond)
		if _, err := e.store.Tags(ctx, "топ"); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want context.DeadlineExceeded", err)
		}
	})
}

// TestParameterRoundTrip проверяет, что значение доходит до сервера без изменений.
func TestParameterRoundTrip(t *testing.T) {
	e := setup(t)
	for _, input := range []string{
		`\`, `a\b`, `a\nb`, `\\`, "таб\tтаб", "строка\nновая", "возврат\rкаретки", `кавычка'`, `"`, "платье",
	} {
		var got string
		ctx := clickhouse.Context(context.Background(), clickhouse.WithParameters(clickhouse.Parameters{"s": storage.EscapeParameter(input)}))
		if err := e.conn.QueryRow(ctx, "SELECT {s:String}").Scan(&got); err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if got != input {
			t.Errorf("сервер получил %q, want %q", got, input)
		}
	}
}

// TestNormalizeParity проверяет, что Go и ClickHouse нормализуют текст одинаково.
func TestNormalizeParity(t *testing.T) {
	e := setup(t)

	inputs := []string{
		"ПЛАТЬЕ", "Платье ", "  Красное   Платье  ", "ЧЁРНОЕ ПЛАТЬЕ", "Ёлка", "кроссовки NIKE",
		"Iphone 15", "таб\tи\nперевод\r\nстроки", "   ", "", "ЭЮЯ абв XYZ 123",
	}
	for _, input := range inputs {
		var got string
		paramCtx := clickhouse.Context(context.Background(), clickhouse.WithParameters(clickhouse.Parameters{"s": storage.EscapeParameter(input)}))
		if err := e.conn.QueryRow(paramCtx, "SELECT normalize_query({s:String})").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if want := search.Normalize(input); got != want {
			t.Errorf("%q: ClickHouse = %q, Go = %q", input, got, want)
		}
	}
}

// TestNormalizeParityOnRealLogs сверяет нормализацию на всех запросах загруженного датасета.
func TestNormalizeParityOnRealLogs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	var total uint64
	err := e.conn.QueryRow(ctx, "SELECT count() FROM system.tables WHERE database = 'search' AND name = 'search_logs'").Scan(&total)
	if err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Skip("база search не загружена")
	}

	rows, err := e.conn.Query(ctx, "SELECT DISTINCT search_query, normalize_query(search_query) FROM search.search_logs")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var raw, normalized string
		if err := rows.Scan(&raw, &normalized); err != nil {
			t.Fatal(err)
		}
		checked++
		if want := search.Normalize(raw); normalized != want {
			t.Errorf("%q: ClickHouse = %q, Go = %q", raw, normalized, want)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	t.Logf("проверено запросов: %d", checked)
}
