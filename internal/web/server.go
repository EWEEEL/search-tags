// Package web содержит HTTP-обработчики: JSON API и страницу поиска.
package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/EWEEEL/search-tags/internal/search"
)

// TagStore — источник тегов. Интерфейс позволяет тестировать обработчики без ClickHouse.
type TagStore interface {
	Tags(ctx context.Context, query string) ([]search.Tag, error)
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

// Handler возвращает маршруты сервиса с логированием запросов.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tags", s.handleAPITags)
	mux.HandleFunc("GET /{$}", s.handlePage)
	return s.logRequests(mux)
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
func (s *Server) logLookupError(err error) {
	if errors.Is(err, context.Canceled) {
		s.logger.Info("client disconnected", "error", err)
		return
	}
	s.logger.Error("load tags", "error", err)
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
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.logger.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(start),
		)
	})
}
