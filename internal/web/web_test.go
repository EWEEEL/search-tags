package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/EWEEEL/search-tags/internal/search"
)

// fakeStore подменяет ClickHouse и запоминает, с каким запросом его вызвали.
type fakeStore struct {
	tags     map[string][]search.Tag
	err      error
	block    bool
	readyErr error
	queries  []string
}

func (f *fakeStore) Ready(context.Context) error { return f.readyErr }

func (f *fakeStore) Tags(ctx context.Context, query string) ([]search.Tag, error) {
	f.queries = append(f.queries, query)
	if f.block {
		<-ctx.Done()
		return nil, errors.New("driver: connection read failed")
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.tags[query], nil
}

func newTestStore() *fakeStore {
	return &fakeStore{tags: map[string][]search.Tag{
		"платье": {
			{Tag: "красное платье", Users: 100},
			{Tag: "вечернее платье", Users: 50},
		},
		"xss": {
			{Tag: `<script>alert("tag")</script>`, Users: 7},
			{Tag: "a&b=c d", Users: 6},
		},
	}}
}

func get(t *testing.T, store TagStore, target string) (int, string, http.Header) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(store, 20*time.Millisecond, logger)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.String(), rec.Header()
}

func apiURL(q string) string {
	return "/api/tags?q=" + url.QueryEscape(q)
}

func pageURL(q string) string {
	return "/?q=" + url.QueryEscape(q)
}

func TestAPIEmptyQueryReturns400(t *testing.T) {
	for _, target := range []string{"/api/tags", "/api/tags?q=", apiURL("   "), apiURL("\t\n")} {
		t.Run(target, func(t *testing.T) {
			store := newTestStore()
			status, body, _ := get(t, store, target)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", status)
			}
			if !strings.Contains(body, `"error"`) {
				t.Errorf("body = %s, want JSON error", body)
			}
			if len(store.queries) != 0 {
				t.Errorf("база вызвана с %v, хотя запрос пустой", store.queries)
			}
		})
	}
}

func TestAPIReturnsTagsInStoreOrder(t *testing.T) {
	status, body, header := get(t, newTestStore(), apiURL("платье"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if got := header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q", got)
	}

	var tags []search.Tag
	if err := json.Unmarshal([]byte(body), &tags); err != nil {
		t.Fatalf("ответ не является JSON-списком %s: %v", body, err)
	}
	want := []search.Tag{{Tag: "красное платье", Users: 100}, {Tag: "вечернее платье", Users: 50}}
	if len(tags) != len(want) {
		t.Fatalf("tags = %v, want %v", tags, want)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Errorf("tags[%d] = %v, want %v", i, tags[i], want[i])
		}
	}
}

func TestAPINormalizesQuery(t *testing.T) {
	_, reference, _ := get(t, newTestStore(), apiURL("платье"))
	for _, q := range []string{"Платье ", " ПЛАТЬЕ", "  плАтье  "} {
		t.Run(q, func(t *testing.T) {
			store := newTestStore()
			status, body, _ := get(t, store, apiURL(q))
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if body != reference {
				t.Errorf("body = %s, want %s", body, reference)
			}
			if len(store.queries) != 1 || store.queries[0] != "платье" {
				t.Errorf("база вызвана с %v, want [платье]", store.queries)
			}
		})
	}
}

func TestAPINoTagsReturnsEmptyList(t *testing.T) {
	status, body, _ := get(t, newTestStore(), apiURL("неизвестный запрос"))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.TrimSpace(body) != "[]" {
		t.Errorf("body = %s, want an empty list, not null", body)
	}
}

func TestAPIStoreErrorDoesNotLeakDetails(t *testing.T) {
	store := &fakeStore{err: errors.New("secret-host:9000 refused")}
	status, body, _ := get(t, store, apiURL("платье"))
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if strings.Contains(body, "secret-host") {
		t.Errorf("детали ошибки попали в ответ: %s", body)
	}
}

func TestAPITimeoutReturns504(t *testing.T) {
	status, _, _ := get(t, &fakeStore{block: true}, apiURL("платье"))
	if status != http.StatusGatewayTimeout {
		t.Errorf("status = %d, want 504", status)
	}
}

func TestClientDisconnectIsNotServerError(t *testing.T) {
	for _, target := range []string{apiURL("платье"), pageURL("платье")} {
		t.Run(target, func(t *testing.T) {
			var logs bytes.Buffer
			server := NewServer(&fakeStore{block: true}, time.Minute, slog.New(slog.NewTextHandler(&logs, nil)))

			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx))

			if rec.Code != statusClientClosedRequest {
				t.Errorf("status = %d, want %d", rec.Code, statusClientClosedRequest)
			}
			if strings.Contains(logs.String(), "level=ERROR") {
				t.Errorf("уход клиента записан как ошибка:\n%s", logs.String())
			}
		})
	}
}

func TestAPIRejectsOtherMethods(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := NewServer(newTestStore(), time.Second, logger)
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tags?q=x", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestPageWithoutQueryShowsOnlyForm(t *testing.T) {
	store := newTestStore()
	for _, target := range []string{"/", pageURL("   ")} {
		status, body, _ := get(t, store, target)
		if status != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", target, status)
		}
		if !strings.Contains(body, "Найти") || !strings.Contains(body, `name="q"`) {
			t.Errorf("%s: нет формы поиска", target)
		}
		if strings.Contains(body, "пока нет тегов") {
			t.Errorf("%s: сообщение показано без поиска", target)
		}
	}
	if len(store.queries) != 0 {
		t.Errorf("база вызвана с %v, хотя запроса нет", store.queries)
	}
}

func TestPageShowsClickableTags(t *testing.T) {
	status, body, _ := get(t, newTestStore(), pageURL("Платье "))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	// Регистр в %XX не важен для URL, а html/template пишет его строчными буквами.
	wantLink := `href="/?q=` + strings.ToLower(url.PathEscape("красное платье")) + `"`
	if !strings.Contains(body, wantLink) {
		t.Errorf("нет ссылки %s в:\n%s", wantLink, body)
	}
	first := strings.Index(body, ">красное платье<")
	second := strings.Index(body, ">вечернее платье<")
	if first < 0 || second < 0 || first > second {
		t.Errorf("теги не показаны в порядке rank: %d, %d", first, second)
	}
	if !strings.Contains(body, `value="Платье "`) {
		t.Errorf("в поле поиска нет введённого текста")
	}
}

func TestPageNoTagsMessage(t *testing.T) {
	_, body, _ := get(t, newTestStore(), pageURL("неизвестный запрос"))
	if !strings.Contains(body, "Для этого запроса пока нет тегов") {
		t.Errorf("нет сообщения об отсутствии тегов:\n%s", body)
	}
}

func TestPageEscapesUserInput(t *testing.T) {
	payload := `"><script>alert(1)</script>`
	_, body, _ := get(t, newTestStore(), pageURL(payload))
	if strings.Contains(body, "<script>alert") {
		t.Errorf("пользовательский текст не экранирован:\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("экранированного текста нет в поле поиска:\n%s", body)
	}
}

func TestPageEscapesTags(t *testing.T) {
	_, body, _ := get(t, newTestStore(), pageURL("xss"))
	if strings.Contains(body, `<script>alert("tag")`) {
		t.Errorf("тег не экранирован:\n%s", body)
	}
	if !strings.Contains(body, `href="/?q=a%26b%3dc%20d"`) {
		t.Errorf("спецсимволы тега не закодированы в ссылке:\n%s", body)
	}
}

func TestPageStoreError(t *testing.T) {
	store := &fakeStore{err: errors.New("secret-host:9000 refused")}
	status, body, _ := get(t, store, pageURL("платье"))
	if status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", status)
	}
	if !strings.Contains(body, "Не удалось загрузить теги") || strings.Contains(body, "secret-host") {
		t.Errorf("неверное сообщение об ошибке:\n%s", body)
	}
	if strings.Contains(body, "пока нет тегов") {
		t.Errorf("ошибка базы показана как отсутствие тегов")
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	status, _, _ := get(t, newTestStore(), "/unknown")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", status)
	}
}

func TestHealthzAndReadyz(t *testing.T) {
	status, body, header := get(t, newTestStore(), "/healthz")
	if status != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Errorf("healthz: %d %s", status, body)
	}
	if header.Get("Cache-Control") != "no-store" {
		t.Errorf("healthz кэшируется: %q", header.Get("Cache-Control"))
	}

	if status, body, _ := get(t, newTestStore(), "/readyz"); status != http.StatusOK || !strings.Contains(body, `"ready"`) {
		t.Errorf("readyz при живой базе: %d %s", status, body)
	}
	down := &fakeStore{readyErr: errors.New("table search_tags does not exist yet")}
	if status, body, _ := get(t, down, "/readyz"); status != http.StatusServiceUnavailable || strings.Contains(body, "search_tags") {
		t.Errorf("readyz без базы: %d %s", status, body)
	}
	// Жив, но не готов: /healthz не зависит от базы.
	if status, _, _ := get(t, down, "/healthz"); status != http.StatusOK {
		t.Errorf("healthz без базы: %d", status)
	}
}

func TestCacheHeaders(t *testing.T) {
	tests := []struct {
		name   string
		store  *fakeStore
		target string
		want   string
	}{
		{"теги кэшируются", newTestStore(), apiURL("платье"), "public, max-age=300"},
		{"пустой ответ тоже", newTestStore(), apiURL("неизвестный запрос"), "public, max-age=300"},
		{"страница кэшируется", newTestStore(), pageURL("платье"), "public, max-age=300"},
		{"400 не кэшируется", newTestStore(), apiURL(" "), "no-store"},
		{"500 не кэшируется", &fakeStore{err: errors.New("down")}, apiURL("платье"), "no-store"},
		{"таймаут не кэшируется", &fakeStore{block: true}, apiURL("платье"), "no-store"},
		{"страница с ошибкой не кэшируется", &fakeStore{err: errors.New("down")}, pageURL("платье"), "no-store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, header := get(t, tt.store, tt.target)
			if got := header.Get("Cache-Control"); got != tt.want {
				t.Errorf("Cache-Control = %q, want %q", got, tt.want)
			}
		})
	}
}

func serveWithLogs(t *testing.T, store TagStore, req *http.Request) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var logs bytes.Buffer
	server := NewServer(store, 20*time.Millisecond, slog.New(slog.NewJSONHandler(&logs, nil)))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("строка лога не JSON: %q", line)
		}
		entries = append(entries, entry)
	}
	return rec, entries
}

func TestRequestIDIsGeneratedAndLogged(t *testing.T) {
	rec, logs := serveWithLogs(t, newTestStore(), httptest.NewRequest(http.MethodGet, apiURL("  Платье "), nil))
	id := rec.Header().Get("X-Request-ID")
	if !validRequestID.MatchString(id) {
		t.Fatalf("X-Request-ID = %q", id)
	}
	last := logs[len(logs)-1]
	if last["request_id"] != id || last["query"] != "платье" || last["tags"] != float64(2) || last["status"] != float64(200) {
		t.Errorf("строка лога: %v", last)
	}
	if _, ok := last["duration_ms"].(float64); !ok {
		t.Errorf("нет duration_ms в миллисекундах: %v", last)
	}
}

func TestRequestIDFromClient(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, apiURL("платье"), nil)
	req.Header.Set("X-Request-ID", "front-42.a_b")
	rec, _ := serveWithLogs(t, newTestStore(), req)
	if got := rec.Header().Get("X-Request-ID"); got != "front-42.a_b" {
		t.Errorf("X-Request-ID = %q, want переданный клиентом", got)
	}

	// Чужой id с переводом строки мог бы подделать строки лога: заменяем его своим.
	req = httptest.NewRequest(http.MethodGet, apiURL("платье"), nil)
	req.Header.Set("X-Request-ID", "evil\nlevel=ERROR")
	rec, _ = serveWithLogs(t, newTestStore(), req)
	if got := rec.Header().Get("X-Request-ID"); strings.Contains(got, "evil") || !validRequestID.MatchString(got) {
		t.Errorf("X-Request-ID = %q", got)
	}
}

func TestLookupErrorIsLoggedWithRequestID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, apiURL("платье"), nil)
	req.Header.Set("X-Request-ID", "req-1")
	_, logs := serveWithLogs(t, &fakeStore{err: errors.New("boom")}, req)
	for _, entry := range logs {
		if entry["level"] == "ERROR" && entry["request_id"] == "req-1" && entry["error"] == "boom" {
			return
		}
	}
	t.Errorf("ошибка не записана с request_id: %v", logs)
}

func TestLoggedQueryIsTruncated(t *testing.T) {
	long := strings.Repeat("я", 300)
	_, logs := serveWithLogs(t, newTestStore(), httptest.NewRequest(http.MethodGet, apiURL(long), nil))
	query, _ := logs[len(logs)-1]["query"].(string)
	if n := len([]rune(query)); n != maxLoggedQuery+1 || !strings.HasSuffix(query, "…") {
		t.Errorf("в лог попало %d символов: %q", n, query)
	}
}
