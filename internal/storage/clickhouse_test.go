package storage

import "testing"

func TestEscapeParameter(t *testing.T) {
	tests := map[string]string{
		"платье":        "платье",
		`\`:             `\\`,
		`a\b`:           `a\\b`,
		"таб\tтаб":      `таб\tтаб`,
		"строка\nновая": `строка\nновая`,
		"возврат\r":     `возврат\r`,
		`кавычка'`:      `кавычка'`,
		`\n`:            `\\n`,
	}
	for input, want := range tests {
		if got := EscapeParameter(input); got != want {
			t.Errorf("EscapeParameter(%q) = %q, want %q", input, got, want)
		}
	}
}
