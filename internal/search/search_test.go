package search

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"уже нормализован", "платье", "платье"},
		{"кириллица в верхнем регистре", "ПЛАТЬЕ", "платье"},
		{"смешанный регистр", "Красное Платье", "красное платье"},
		{"буква ё в верхнем регистре", "ЧЁРНОЕ ПЛАТЬЕ", "чёрное платье"},
		{"латиница", "Кроссовки NIKE", "кроссовки nike"},
		{"пробел справа", "платье ", "платье"},
		{"пробел слева", " платье", "платье"},
		{"пробелы с обеих сторон", "   платье  ", "платье"},
		{"повторные пробелы внутри", "красное   платье", "красное платье"},
		{"всё сразу", "  КРАСНОЕ   Платье ", "красное платье"},
		{"табуляция и перевод строки", "красное\t\nплатье\n", "красное платье"},
		{"ё не заменяется на е", "чёрное платье", "чёрное платье"},
		{"цифры", "Iphone 15", "iphone 15"},
		{"пустая строка", "", ""},
		{"только пробелы", " \t\n ", ""},
		{"неразрывный пробел не схлопывается, как и в SQL", "платье миди", "платье миди"},

		// Дальше — места, где strings.ToLower расходится с lowerUTF8 из ClickHouse. Ведём себя как ClickHouse.
		{"İ становится i с точкой", "İSTANBUL", "i̇stanbul"},
		{"сигма в конце слова", "ΣΟΦΙΑΣ", "σοφιας"},
		{"сигма в начале и середине", "ΣΑΣ ΣΑΣ", "σας σας"},
		{"одиночная сигма", "Σ", "σ"},
		{"сигма перед дефисом — конец слова", "ΑΣ-Β", "ας-β"},
		{"апостроф не разрывает слово", "ΑΣ'Α", "ασ'α"},
		{"точка не разрывает слово", "ΑΣ.Α", "ασ.α"},
		{"диакритика пропускается", "ΑΣ́", "ας́"},
		{"после цифры сигма не конечная", "1Σ", "1σ"},
		{"битые байты сохраняются", "A\xff\xfeB", "a\xff\xfeb"},
		{"обрезанная кириллица сохраняется", "ПЛА\xd1", "пла\xd1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Normalize(tt.input); got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	for _, input := range []string{"  КРАСНОЕ   Платье ", "платье", "", "İSTANBUL", "ΣΑΣ"} {
		once := Normalize(input)
		if twice := Normalize(once); twice != once {
			t.Errorf("Normalize(%q) изменился при повторе: %q -> %q", input, once, twice)
		}
	}
}

var asciiSpaceRun = regexp.MustCompile(`[\t\n\f\r ]{2,}`)

// FuzzNormalize проверяет свойства, которые должны выполняться для любой строки.
// Совпадение с ClickHouse проверяют интеграционные тесты: здесь базы нет.
func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{"Платье ", "  a  b  ", "\t\n", "ΣΑΣ", "İ", "\xff\xfe", "a  b", "ΑΣ'Α"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		out := Normalize(input)
		if Normalize(out) != out {
			t.Fatalf("не идемпотентно: %q -> %q -> %q", input, out, Normalize(out))
		}
		if strings.HasPrefix(out, " ") || strings.HasSuffix(out, " ") {
			t.Fatalf("пробелы по краям: %q", out)
		}
		if asciiSpaceRun.MatchString(out) || strings.ContainsAny(out, "\t\n\f\r") {
			t.Fatalf("пробелы не схлопнуты: %q", out)
		}
		if utf8.ValidString(input) != utf8.ValidString(out) {
			t.Fatalf("корректность UTF-8 изменилась: %q -> %q", input, out)
		}
		if strings.Count(out, "�") > strings.Count(input, "�") {
			t.Fatalf("появились символы замены: %q -> %q", input, out)
		}
	})
}
