// Package config читает настройки сервиса из переменных окружения.
package config

import (
	"fmt"
	"strconv"
	"time"
)

// Config содержит настройки сервиса.
type Config struct {
	Port               string
	ClickHouseAddr     string
	ClickHouseDatabase string
	ClickHouseUser     string
	ClickHousePassword string
	QueryTimeout       time.Duration
}

// Load читает настройки. getenv передаётся параметром, чтобы тесты не меняли окружение процесса.
func Load(getenv func(string) string) (Config, error) {
	withDefault := func(key, fallback string) string {
		if value := getenv(key); value != "" {
			return value
		}
		return fallback
	}

	cfg := Config{
		Port:               withDefault("PORT", "8080"),
		ClickHouseAddr:     withDefault("CLICKHOUSE_ADDR", "localhost:9000"),
		ClickHouseDatabase: withDefault("CLICKHOUSE_DATABASE", "search"),
		ClickHouseUser:     withDefault("CLICKHOUSE_USER", "default"),
		ClickHousePassword: getenv("CLICKHOUSE_PASSWORD"),
	}

	if port, err := strconv.ParseUint(cfg.Port, 10, 16); err != nil || port == 0 {
		return Config{}, fmt.Errorf("PORT: invalid port %q", cfg.Port)
	}

	timeout, err := time.ParseDuration(withDefault("QUERY_TIMEOUT", "3s"))
	if err != nil {
		return Config{}, fmt.Errorf("QUERY_TIMEOUT: %w", err)
	}
	if timeout <= 0 {
		return Config{}, fmt.Errorf("QUERY_TIMEOUT: must be positive, got %s", timeout)
	}
	cfg.QueryTimeout = timeout

	return cfg, nil
}
