// Package storage читает теги из ClickHouse.
package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/EWEEEL/search-tags/internal/search"
)

// tagsQuery использует серверный параметр: значение передаётся отдельно от текста SQL.
const tagsQuery = `SELECT tag, users FROM search_tags WHERE query = {query:String} ORDER BY rank`

// parameterEscaper кодирует символы, особые для формата TSV Escaped.
var parameterEscaper = strings.NewReplacer(`\`, `\\`, "\t", `\t`, "\n", `\n`, "\r", `\r`)

// EscapeParameter готовит значение серверного параметра. ClickHouse разбирает такие значения
// как TSV Escaped, а драйвер передаёт их без изменений. Без этого `\` приводит к ошибке сервера,
// а `a\b` тихо превращается в другую строку. На защиту от SQL-инъекций это не влияет:
// значение и так передаётся отдельно от текста запроса.
func EscapeParameter(value string) string {
	return parameterEscaper.Replace(value)
}

// Options описывает подключение к ClickHouse.
type Options struct {
	Addr     string
	Database string
	Username string
	Password string
	// DialTimeout ограничивает установку соединения: драйвер не учитывает для неё
	// дедлайн контекста, поэтому без этой настройки запрос может превысить свой таймаут.
	DialTimeout time.Duration
}

// ClickHouse реализует чтение тегов из результирующей таблицы.
type ClickHouse struct {
	conn driver.Conn
}

// Open создаёт пул соединений. Само подключение проверяет Ping.
func Open(opts Options) (*ClickHouse, error) {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{opts.Addr},
		Auth: clickhouse.Auth{
			Database: opts.Database,
			Username: opts.Username,
			Password: opts.Password,
		},
		DialTimeout: opts.DialTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("open clickhouse: %w", err)
	}
	return &ClickHouse{conn: conn}, nil
}

// Ping проверяет, что ClickHouse доступен.
func (c *ClickHouse) Ping(ctx context.Context) error {
	if err := c.conn.Ping(ctx); err != nil {
		return fmt.Errorf("ping clickhouse: %w", err)
	}
	return nil
}

// Ready проверяет, что ClickHouse доступен и таблица тегов уже создана: до первого прогона
// db-init её нет, и сервис ответил бы 500 на любой поиск.
func (c *ClickHouse) Ready(ctx context.Context) error {
	if err := c.Ping(ctx); err != nil {
		return err
	}
	var exists uint8
	if err := c.conn.QueryRow(ctx, "EXISTS TABLE search_tags").Scan(&exists); err != nil {
		return fmt.Errorf("check search_tags: %w", err)
	}
	if exists == 0 {
		return errors.New("table search_tags does not exist yet")
	}
	return nil
}

// Close закрывает пул соединений.
func (c *ClickHouse) Close() error {
	return c.conn.Close()
}

// Tags возвращает теги нормализованного запроса в порядке rank.
// Если тегов нет, возвращается пустой срез, а не ошибка.
func (c *ClickHouse) Tags(ctx context.Context, query string) ([]search.Tag, error) {
	queryCtx := clickhouse.Context(ctx, clickhouse.WithParameters(clickhouse.Parameters{
		"query": EscapeParameter(query),
	}))

	rows, err := c.conn.Query(queryCtx, tagsQuery)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	defer rows.Close()

	tags := make([]search.Tag, 0, 10)
	for rows.Next() {
		var tag search.Tag
		if err := rows.Scan(&tag.Tag, &tag.Users); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		tags = append(tags, tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read tags: %w", err)
	}
	return tags, nil
}
