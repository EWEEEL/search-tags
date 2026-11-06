package config

import (
	"testing"
	"time"
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8080" || cfg.ClickHouseAddr != "localhost:9000" || cfg.QueryTimeout != 3*time.Second {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadFromEnvironment(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"PORT":                "9090",
		"CLICKHOUSE_ADDR":     "clickhouse:9000",
		"CLICKHOUSE_DATABASE": "tags",
		"CLICKHOUSE_USER":     "app",
		"CLICKHOUSE_PASSWORD": "secret",
		"QUERY_TIMEOUT":       "500ms",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Port:               "9090",
		ClickHouseAddr:     "clickhouse:9000",
		ClickHouseDatabase: "tags",
		ClickHouseUser:     "app",
		ClickHousePassword: "secret",
		QueryTimeout:       500 * time.Millisecond,
	}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := map[string]map[string]string{
		"порт не число":      {"PORT": "abc"},
		"порт вне диапазона": {"PORT": "70000"},
		"нулевой порт":       {"PORT": "0"},
		"таймаут не время":   {"QUERY_TIMEOUT": "soon"},
		"нулевой таймаут":    {"QUERY_TIMEOUT": "0s"},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(env(values)); err == nil {
				t.Error("ожидалась ошибка")
			}
		})
	}
}
