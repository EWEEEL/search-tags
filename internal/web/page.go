package web

import (
	"bytes"
	"embed"
	"html/template"
	"net/http"

	"github.com/EWEEEL/search-tags/internal/search"
)

//go:embed templates/index.html
var templateFiles embed.FS

var pageTemplate = template.Must(template.ParseFS(templateFiles, "templates/index.html"))

type pageData struct {
	Query    string // текст в поле поиска, как его ввёл пользователь
	Searched bool   // был ли непустой запрос
	Tags     []search.Tag
	Failed   bool // не удалось получить теги
}

// handlePage обрабатывает GET /: форма поиска и теги для параметра q.
func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw := r.URL.Query().Get("q")
	data := pageData{Query: raw}
	status := http.StatusOK

	if query := search.Normalize(raw); query != "" {
		data.Searched = true
		tags, err := s.lookup(ctx, query)
		if err != nil {
			s.logLookupError(ctx, err)
			data.Failed = true
			status = errorStatus(err)
		} else {
			noteSearch(ctx, query, len(tags))
		}
		data.Tags = tags
	}

	// Рендерим в буфер: при ошибке шаблона клиент не получит половину страницы.
	var page bytes.Buffer
	if err := pageTemplate.Execute(&page, data); err != nil {
		s.requestLogger(ctx).Error("render page", "error", err)
		setCacheHeaders(w, http.StatusInternalServerError)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	setCacheHeaders(w, status)
	w.WriteHeader(status)
	if _, err := page.WriteTo(w); err != nil {
		s.requestLogger(ctx).Error("write response", "error", err)
	}
}
