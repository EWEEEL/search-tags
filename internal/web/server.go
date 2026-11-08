// Package web содержит HTTP-обработчики: JSON API, страницу поиска и проверки здоровья.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/EWEEEL/search-tags/internal/search"
)

// TagStore — источник тегов. Интерфейс позволяет тестировать обработчики без ClickHouse.
type TagStore interface {
	Tags(ctx context.Context, query string) ([]search.Tag, error)
	// Ready проверяет, что база доступна и таблица тегов на месте.
	Ready(ctx context.Context) error
}

// Server обрабатывает HTTP-запросы сервиса тегов.
type Server struct {
	store        TagStore
	queryTimeout time.Duration
	logger       *slog.Logger
}

// NewServer создаёт сервер. queryTimeout ограничивает каждый запрос к базе.
func NewServer(store TagStore, queryTimeout time.Duration, logger *slog.Logger) *Server {
	return &Server{store: store, queryTimeout: queryTimeout, logger: logger}
}

// Handler возвращает маршруты сервиса с request-id и логированием запросов.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", s.handleAPITags)
	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	return s.logRequests(mux)
}

// cacheTTL: теги пересчитываются раз в сутки, поэтому пять минут устаревания незаметны,
// а повторные запросы популярных подсказок обслужит кэш браузера или CDN, а не ClickHouse.
const cacheTTL = 5 * time.Minute

var cacheableHeader = fmt.Sprintf("public, max-age=%d", int(cacheTTL.Seconds()))

// setCacheHeaders кэширует только успешные ответы: ошибку или таймаут кэшировать нельзя.
func setCacheHeaders(w http.ResponseWriter, status int) {
	if status == http.StatusOK {
		w.Header().Set("Cache-Control", cacheableHeader)
	} else {
		w.Header().Set("Cache-Control", "no-store")
	}
}

// lookup читает теги с таймаутом и отделяет истечение таймаута от остальных ошибок.
func (s *Server) lookup(ctx context.Context, query string) ([]search.Tag, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()

	tags, err := s.store.Tags(ctx, query)
	if err != nil {
		// Драйвер может вернуть сетевую ошибку вместо ошибки контекста,
		// поэтому причину определяем по самому контексту.
		if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) {
			return nil, fmt.Errorf("%w: %v", ctxErr, err)
		}
		return nil, err
	}
	return tags, nil
}

// statusClientClosedRequest означает, что клиент ушёл, не дождавшись ответа (код из nginx).
const statusClientClosedRequest = 499

// errorStatus выбирает HTTP-статус для ошибки чтения тегов.
func errorStatus(err error) int {
	switch {
	case errors.Is(err, context.Canceled):
		return statusClientClosedRequest
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}

// logLookupError пишет ошибку чтения тегов. Уход клиента не считается ошибкой сервера.
func (s *Server) logLookupError(ctx context.Context, err error) {
	logger := s.requestLogger(ctx)
	if errors.Is(err, context.Canceled) {
		logger.Info("client disconnected", "error", err)
		return
	}
	logger.Error("load tags", "error", err)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), s.queryTimeout)
	defer cancel()
	if err := s.store.Ready(ctx); err != nil {
		s.requestLogger(r.Context()).Warn("not ready", "error", err)
		s.writeJSON(r.Context(), w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	s.writeJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ready"})
}

// requestInfo собирает то, что обработчик знает о запросе, для итоговой строки лога.
type requestInfo struct {
	id    string
	query string // нормализованный запрос, обрезанный до maxLoggedQuery символов
	tags  int    // сколько тегов отдали; -1, если поиска не было
}

type requestInfoKey struct{}

func infoFrom(ctx context.Context) *requestInfo {
	if info, ok := ctx.Value(requestInfoKey{}).(*requestInfo); ok {
		return info
	}
	return &requestInfo{tags: -1}
}

// requestLogger добавляет request_id ко всем записям, сделанным во время запроса.
func (s *Server) requestLogger(ctx context.Context) *slog.Logger {
	if info := infoFrom(ctx); info.id != "" {
		return s.logger.With("request_id", info.id)
	}
	return s.logger
}

const maxLoggedQuery = 100

// noteSearch запоминает запрос и число найденных тегов для строки лога.
func noteSearch(ctx context.Context, query string, tags int) {
	info := infoFrom(ctx)
	if utf8.RuneCountInString(query) > maxLoggedQuery {
		query = string([]rune(query)[:maxLoggedQuery]) + "…"
	}
	info.query = query
	info.tags = tags
}

// validRequestID пропускает чужой X-Request-ID, только если он короткий и без управляющих символов:
// иначе через заголовок можно было бы подделать строки лога.
var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}

// statusRecorder запоминает статус ответа для лога.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		id := r.Header.Get("X-Request-ID")
		if !validRequestID.MatchString(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		// По умолчанию не кэшируем: так закрыты и ответы самого mux (404, 405).
		// Успешные ответы обработчики переопределяют через setCacheHeaders.
		w.Header().Set("Cache-Control", "no-store")
		info := &requestInfo{id: id, tags: -1}

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), requestInfoKey{}, info)))

		attrs := []any{
			"request_id", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", float64(time.Since(start).Microseconds()) / 1000,
		}
		if info.tags >= 0 {
			attrs = append(attrs, "query", info.query, "tags", info.tags)
		}
		s.logger.Info("request", attrs...)
	})
}
