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
	raw := r.URL.Query().Get("q")
	data := pageData{Query: raw}
	status := http.StatusOK

	if query := search.Normalize(raw); query != "" {
		data.Searched = true
		tags, err := s.lookup(r.Context(), query)
		if err != nil {
			s.logger.Error("load tags", "error", err)
			data.Failed = true
			status = errorStatus(err)
		}
		data.Tags = tags
	}

	// Рендерим в буфер: при ошибке шаблона клиент не получит половину страницы.
	var page bytes.Buffer
	if err := pageTemplate.Execute(&page, data); err != nil {
		s.logger.Error("render page", "error", err)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := page.WriteTo(w); err != nil {
		s.logger.Error("write response", "error", err)
	}
}
