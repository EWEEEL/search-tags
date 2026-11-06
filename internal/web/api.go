package web

import (
	"encoding/json"
	"net/http"

	"github.com/EWEEEL/search-tags/internal/search"
)

type errorResponse struct {
	Error string `json:"error"`
}

// handleAPITags обрабатывает GET /api/tags?q=<текст>.
func (s *Server) handleAPITags(w http.ResponseWriter, r *http.Request) {
	query := search.Normalize(r.URL.Query().Get("q"))
	if query == "" {
		s.writeJSON(w, http.StatusBadRequest, errorResponse{Error: "query parameter q is required"})
		return
	}

	tags, err := s.lookup(r.Context(), query)
	if err != nil {
		// Подробности только в логе: клиенту не нужны детали устройства базы.
		s.logLookupError(err)
		status := errorStatus(err)
		s.writeJSON(w, status, errorResponse{Error: http.StatusText(status)})
		return
	}
	if tags == nil {
		tags = []search.Tag{} // в JSON должен быть [], а не null
	}

	s.writeJSON(w, http.StatusOK, tags)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.logger.Error("write response", "error", err)
	}
}
