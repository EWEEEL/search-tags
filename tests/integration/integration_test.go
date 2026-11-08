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
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

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

	// Больше 255 кандидатов: номер строки не должен переполниться при приведении к UInt8.
	for i := range 300 {
		f.users(5, "много кандидатов", fmt.Sprintf("кандидат %03d", i))
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

	buildAndPublish(t, conn, opts)

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
	if err := execSQLFile(context.Background(), conn, name); err != nil {
		t.Fatal(err)
	}
}

func execSQLFile(ctx context.Context, conn driver.Conn, name string) error {
	content, err := os.ReadFile(filepath.Join("..", "..", "sql", name))
	if err != nil {
		return err
	}
	for _, statement := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), ";\n") {
		if !hasSQL(statement) {
			continue
		}
		if err := conn.Exec(ctx, statement); err != nil {
			return fmt.Errorf("%s: %w\n%s", name, err, statement)
		}
	}
	return nil
}

// runChecks выполняет sql/04_checks.sql для таблицы table так же, как scripts/init-db.sh.
// Нужно отдельное соединение с одним подключением в пуле: временная таблица check_results
// живёт в сессии, а пул мог бы отправить следующее выражение в другое подключение.
func runChecks(t *testing.T, opts storage.Options, table string) error {
	t.Helper()
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:         []string{opts.Addr},
		Auth:         clickhouse.Auth{Database: opts.Database, Username: opts.Username, Password: opts.Password},
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := clickhouse.Context(context.Background(), clickhouse.WithParameters(clickhouse.Parameters{"table": table}))
	return execSQLFile(ctx, conn, "04_checks.sql")
}

// buildAndPublish повторяет шаги после загрузки логов: сборка, проверки новой сборки, публикация.
func buildAndPublish(t *testing.T, conn driver.Conn, opts storage.Options) {
	t.Helper()
	runSQLFile(t, conn, "03_build_tags.sql")
	if err := runChecks(t, opts, "search_tags_new"); err != nil {
		t.Fatalf("проверки не прошли на корректной сборке: %v", err)
	}
	runSQLFile(t, conn, "05_publish.sql")
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

	t.Run("больше 255 кандидатов не ломают топ-10", func(t *testing.T) {
		got := e.tags(t, "много кандидатов")
		if len(got) != 10 {
			t.Fatalf("строк %d, want 10: %+v", len(got), got)
		}
		for i, row := range got {
			wantTag := fmt.Sprintf("кандидат %03d", i)
			if row.Tag != wantTag || row.Rank != uint8(i+1) {
				t.Errorf("строка %d = %+v, want tag %q rank %d", i, row, wantTag, i+1)
			}
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
		buildAndPublish(t, e.conn, e.opts)
		if after := e.tags(t, "топ"); !reflect.DeepEqual(before, after) {
			t.Errorf("before %+v, after %+v", before, after)
		}
	})

	t.Run("опубликованная таблица проходит все SQL-проверки", func(t *testing.T) {
		if err := runChecks(t, e.opts, "search_tags"); err != nil {
			t.Fatal(err)
		}
	})
}

// TestChecksBlockBrokenBuild ломает новую сборку разными способами и проверяет, что 04_checks.sql
// каждый раз её отклоняет, а опубликованная таблица остаётся прежней.
func TestChecksBlockBrokenBuild(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	published := e.tags(t, "топ")

	breakages := []struct{ name, sql string }{
		{"тег совпадает с запросом", "INSERT INTO search_tags_new VALUES ('куртка', 'куртка', 9, 1)"},
		{"меньше 5 пользователей", "INSERT INTO search_tags_new VALUES ('куртка', 'пуховик', 4, 1)"},
		{"больше 10 тегов", "INSERT INTO search_tags_new SELECT 'куртка', concat('тег ', toString(number)), 5, number + 1 FROM numbers(11)"},
		{"ненормализованный запрос", "INSERT INTO search_tags_new VALUES ('Куртка ', 'пуховик', 9, 1)"},
		{"пропуск в rank", "INSERT INTO search_tags_new VALUES ('куртка', 'пуховик', 9, 2)"},
		{"users растёт вместе с rank", "INSERT INTO search_tags_new VALUES ('куртка', 'пуховик', 5, 1), ('куртка', 'парка', 9, 2)"},
		{"тег бота", "INSERT INTO search_tags_new VALUES ('платье', 'странный запрос', 7, 2)"},
		{"пустая сборка", "TRUNCATE TABLE search_tags_new"},
		{"неполная сборка", "DELETE FROM search_tags_new WHERE query != 'платье'"},
	}
	for _, b := range breakages {
		t.Run(b.name, func(t *testing.T) {
			runSQLFile(t, e.conn, "03_build_tags.sql")
			if err := e.conn.Exec(ctx, b.sql); err != nil {
				t.Fatal(err)
			}
			err := runChecks(t, e.opts, "search_tags_new")
			if err == nil || !strings.Contains(err.Error(), "SQL-проверки не пройдены") {
				t.Fatalf("сломанная сборка прошла проверки: %v", err)
			}
			if got := e.tags(t, "топ"); !reflect.DeepEqual(got, published) {
				t.Errorf("опубликованные теги изменились: %+v", got)
			}
		})
	}
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
		var tags []search.Tag
		if err := json.Unmarshal([]byte(a), &tags); err != nil {
			t.Fatal(err)
		}
		want := []search.Tag{{Tag: "красное платье", Users: 5}}
		if !reflect.DeepEqual(tags, want) {
			t.Errorf("tags = %+v, want %+v", tags, want)
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
		if status != http.StatusOK || strings.TrimSpace(body) != "[]" {
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
			if status != http.StatusOK || strings.TrimSpace(body) != "[]" {
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

	t.Run("готовность: база доступна и таблица тегов есть", func(t *testing.T) {
		if err := e.store.Ready(context.Background()); err != nil {
			t.Errorf("Ready() = %v, want nil", err)
		}
		status, body := func() (int, string) {
			response, err := http.Get(api.URL + "/readyz")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			b, _ := io.ReadAll(response.Body)
			return response.StatusCode, string(b)
		}()
		if status != http.StatusOK {
			t.Errorf("/readyz: %d %s", status, body)
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

// TestNotReadyWithoutTagsTable: до первой публикации таблицы тегов нет, и сервис должен
// честно отвечать 503 на /readyz, а не считаться готовым.
func TestNotReadyWithoutTagsTable(t *testing.T) {
	e := setup(t)
	if err := e.conn.Exec(context.Background(), "DROP TABLE search_tags"); err != nil {
		t.Fatal(err)
	}
	if err := e.store.Ready(context.Background()); err == nil {
		t.Error("Ready() = nil без таблицы search_tags")
	}
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

// compareWithClickHouse загружает пары (вход, результат Go) в таблицу и считает в ClickHouse,
// где normalize_query даёт другое. Возвращает число расхождений и первые примеры.
func compareWithClickHouse(t *testing.T, e *env, inputs []string) (uint64, []string) {
	t.Helper()
	ctx := context.Background()
	if err := e.conn.Exec(ctx, "CREATE OR REPLACE TABLE normalize_parity (input String, from_go String) ENGINE = Memory"); err != nil {
		t.Fatal(err)
	}
	batch, err := e.conn.PrepareBatch(ctx, "INSERT INTO normalize_parity")
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range inputs {
		if err := batch.Append(input, search.Normalize(input)); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Send(); err != nil {
		t.Fatal(err)
	}

	var mismatches uint64
	if err := e.conn.QueryRow(ctx, "SELECT count() FROM normalize_parity WHERE normalize_query(input) != from_go").Scan(&mismatches); err != nil {
		t.Fatal(err)
	}
	rows, err := e.conn.Query(ctx, "SELECT input, from_go, normalize_query(input) FROM normalize_parity WHERE normalize_query(input) != from_go LIMIT 10")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var examples []string
	for rows.Next() {
		var input, fromGo, fromSQL string
		if err := rows.Scan(&input, &fromGo, &fromSQL); err != nil {
			t.Fatal(err)
		}
		examples = append(examples, fmt.Sprintf("%+q: Go %+q, ClickHouse %+q", input, fromGo, fromSQL))
	}
	return mismatches, examples
}

// TestNormalizeParityAllCodePoints сверяет Go и ClickHouse на каждой кодовой точке Unicode в трёх
// контекстах: символ отдельно, после «ΑΣ» и между «Α» и «Σ». Два последних проверяют, как символ
// влияет на правило конечной сигмы: считается ли он буквой с регистром или пропускается.
func TestNormalizeParityAllCodePoints(t *testing.T) {
	e := setup(t)
	inputs := make([]string, 0, 3*(unicode.MaxRune+1))
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue // суррогаты не кодируются в UTF-8
		}
		c := string(r)
		inputs = append(inputs, c, "ΑΣ"+c, "Α"+c+"Σ")
	}
	mismatches, examples := compareWithClickHouse(t, e, inputs)
	if mismatches > 0 {
		t.Errorf("расхождений: %d из %d\n%s", mismatches, len(inputs), strings.Join(examples, "\n"))
	}
	t.Logf("проверено строк: %d", len(inputs))
}

// TestNormalizeParityRandom сверяет Go и ClickHouse на случайных строках из символов,
// на которых нормализация чаще всего ошибается. seed фиксирован, поэтому прогон воспроизводим.
func TestNormalizeParityRandom(t *testing.T) {
	e := setup(t)
	alphabet := []string{
		"Σ", "σ", "ς", "Α", "β", "Ω", "ά", "́", "'", ".", ":", "·", "’", "-", "ʰ", "­",
		"İ", "I", "i", "К", "к", "Ё", "ё", "A", "z", "5", " ", "  ", "\t", "\n", "\r", "\f", "\v",
		" ", "\xff", "\xd0", "\xc0\xaf", "Ꮳ", "ᲀ", "ǅ", "ẞ", "K", "Ⅻ", "Ⓐ", "Ⴀ",
	}
	rng := rand.New(rand.NewPCG(2026, 11))
	inputs := make([]string, 20000)
	for i := range inputs {
		var b strings.Builder
		for range rng.IntN(12) {
			b.WriteString(alphabet[rng.IntN(len(alphabet))])
		}
		inputs[i] = b.String()
	}
	mismatches, examples := compareWithClickHouse(t, e, inputs)
	if mismatches > 0 {
		t.Errorf("расхождений: %d из %d\n%s", mismatches, len(inputs), strings.Join(examples, "\n"))
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
