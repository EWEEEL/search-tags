package web

import (
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
	tags    map[string][]search.Tag
	err     error
	block   bool
	queries []string
}

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

	var response tagsResponse
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("invalid JSON %s: %v", body, err)
	}
	want := []search.Tag{{Tag: "красное платье", Users: 100}, {Tag: "вечернее платье", Users: 50}}
	if len(response.Tags) != len(want) {
		t.Fatalf("tags = %v, want %v", response.Tags, want)
	}
	for i := range want {
		if response.Tags[i] != want[i] {
			t.Errorf("tags[%d] = %v, want %v", i, response.Tags[i], want[i])
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
	if !strings.Contains(body, `"tags":[]`) {
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
