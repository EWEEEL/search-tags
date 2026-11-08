// Package search содержит доменные типы и нормализацию поисковых запросов.
package search

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Tag — следующий запрос, который показывается под строкой поиска.
type Tag struct {
	Tag   string `json:"tag"`
	Users uint64 `json:"users"`
}

// whitespace совпадает с выражением в SQL-функции normalize_query:
// и Go, и ClickHouse используют синтаксис RE2, где \s означает [\t\n\f\r ].
var whitespace = regexp.MustCompile(`\s+`)

// Normalize выполняет те же шаги, что и normalize_query в sql/03_build_tags.sql: нижний регистр,
// повторные пробелы схлопнуты, пробелы по краям убраны. Результат совпадает с SQL символ в символ:
// интеграционные тесты сверяют его на всех кодовых точках Unicode и на случайных строках.
func Normalize(query string) string {
	query = lowerUTF8(query)
	query = whitespace.ReplaceAllString(query, " ")
	return strings.Trim(query, " ")
}

// lowerUTF8 повторяет lowerUTF8 из ClickHouse 26.8. Он отличается от strings.ToLower в трёх местах:
//   - İ (U+0130) становится «i» с точкой сверху (U+0069 U+0307), как в полном приведении Unicode;
//   - Σ в конце слова становится ς (правило Final_Sigma), в остальных местах — σ;
//   - байты, которые не складываются в корректный UTF-8, остаются как есть, а не заменяются на U+FFFD.
func lowerUTF8(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if 'A' <= c && c <= 'Z' {
				c += 'a' - 'A'
			}
			b.WriteByte(c)
			i++
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			b.WriteByte(c)
		case r == 'İ':
			b.WriteString("i̇")
		case r == 'Σ' && isFinalSigma(s, i, i+size):
			b.WriteRune('ς')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
		i += size
	}
	return b.String()
}

// isFinalSigma реализует условие Final_Sigma из Unicode (SpecialCasing.txt) так, как его проверяет ICU:
// перед сигмой, если пропустить case-ignorable символы, стоит буква с регистром,
// а после неё, если пропустить такие же символы, буквы с регистром нет.
func isFinalSigma(s string, start, end int) bool {
	before := false
	for j := start; j > 0; {
		r, size := utf8.DecodeLastRuneInString(s[:j])
		j -= size
		if isCaseIgnorable(r) {
			continue
		}
		before = isCased(r)
		break
	}
	if !before {
		return false
	}
	for j := end; j < len(s); {
		r, size := utf8.DecodeRuneInString(s[j:])
		j += size
		if isCaseIgnorable(r) {
			continue
		}
		return !isCased(r)
	}
	return true
}

// isCased — свойство Cased из Unicode: буквы верхнего, нижнего и заглавного регистра.
func isCased(r rune) bool {
	return unicode.IsUpper(r) || unicode.IsLower(r) || unicode.IsTitle(r) ||
		unicode.Is(unicode.Other_Lowercase, r) || unicode.Is(unicode.Other_Uppercase, r)
}

// wordBreakMid — символы со свойством Word_Break = MidLetter, MidNumLet или Single_Quote:
// апостроф, точка, двоеточие и их варианты. Они не разрывают слово для правила Final_Sigma.
var wordBreakMid = &unicode.RangeTable{
	R16: []unicode.Range16{
		{Lo: 0x0027, Hi: 0x0027, Stride: 1}, // '
		{Lo: 0x002E, Hi: 0x002E, Stride: 1}, // .
		{Lo: 0x003A, Hi: 0x003A, Stride: 1}, // :
		{Lo: 0x00B7, Hi: 0x00B7, Stride: 1}, // ·
		{Lo: 0x0387, Hi: 0x0387, Stride: 1}, // греческая ано телия
		{Lo: 0x055F, Hi: 0x055F, Stride: 1},
		{Lo: 0x05F4, Hi: 0x05F4, Stride: 1},
		{Lo: 0x2018, Hi: 0x2019, Stride: 1}, // ‘ ’
		{Lo: 0x2024, Hi: 0x2024, Stride: 1},
		{Lo: 0x2027, Hi: 0x2027, Stride: 1},
		{Lo: 0xFE13, Hi: 0xFE13, Stride: 1},
		{Lo: 0xFE52, Hi: 0xFE52, Stride: 1},
		{Lo: 0xFE55, Hi: 0xFE55, Stride: 1},
		{Lo: 0xFF07, Hi: 0xFF07, Stride: 1},
		{Lo: 0xFF0E, Hi: 0xFF0E, Stride: 1},
		{Lo: 0xFF1A, Hi: 0xFF1A, Stride: 1},
	},
	LatinOffset: 4,
}

// isCaseIgnorable — свойство Case_Ignorable: диакритика, форматирующие символы, модификаторы
// и разделители внутри слова.
func isCaseIgnorable(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf, unicode.Lm, unicode.Sk) ||
		unicode.Is(wordBreakMid, r)
}
