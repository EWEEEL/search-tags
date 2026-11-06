// Package search содержит доменные типы и нормализацию поисковых запросов.
package search

import (
	"regexp"
	"strings"
)

// Tag — следующий запрос, который показывается под строкой поиска.
type Tag struct {
	Tag   string `json:"tag"`
	Users uint64 `json:"users"`
}

// whitespace совпадает с выражением в SQL-функции normalize_query:
// и Go, и ClickHouse используют синтаксис RE2, где \s означает [\t\n\f\r ].
var whitespace = regexp.MustCompile(`\s+`)

// Normalize приводит запрос к тому же виду, что и sql/03_build_tags.sql:
// нижний регистр, повторные пробелы схлопнуты, пробелы по краям убраны.
func Normalize(query string) string {
	query = strings.ToLower(query)
	query = whitespace.ReplaceAllString(query, " ")
	return strings.Trim(query, " ")
}
