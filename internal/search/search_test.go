package search

import "testing"

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
	for _, input := range []string{"  КРАСНОЕ   Платье ", "платье", ""} {
		once := Normalize(input)
		if twice := Normalize(once); twice != once {
			t.Errorf("Normalize(%q) изменился при повторе: %q -> %q", input, once, twice)
		}
	}
}
