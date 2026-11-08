package web

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/EWEEEL/search-tags/internal/search"
)

type errorResponse struct {
	Error string `json:"error"`
}

// handleAPITags обрабатывает GET /api/tags?q=<текст>.
func (s *Server) handleAPITags(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := search.Normalize(r.URL.Query().Get("q"))
	if query == "" {
		setCacheHeaders(w, http.StatusBadRequest)
		s.writeJSON(ctx, w, http.StatusBadRequest, errorResponse{Error: "query parameter q is required"})
		return
	}

	tags, err := s.lookup(ctx, query)
	if err != nil {
		// Подробности только в логе: клиенту не нужны детали устройства базы.
		s.logLookupError(ctx, err)
		status := errorStatus(err)
		setCacheHeaders(w, status)
		s.writeJSON(ctx, w, status, errorResponse{Error: http.StatusText(status)})
		return
	}
	if tags == nil {
		tags = []search.Tag{} // в JSON должен быть [], а не null
	}
	noteSearch(ctx, query, len(tags))

	setCacheHeaders(w, http.StatusOK)
	s.writeJSON(ctx, w, http.StatusOK, tags)
}

func (s *Server) writeJSON(ctx context.Context, w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.requestLogger(ctx).Error("write response", "error", err)
	}
}
