# Поисковые теги

[![CI](https://github.com/EWEEEL/search-tags/actions/workflows/ci.yml/badge.svg)](https://github.com/EWEEEL/search-tags/actions/workflows/ci.yml)

Показывает под строкой поиска 10 самых популярных следующих запросов. Теги строятся по логам в ClickHouse, отдаёт их сервис на Go.

## Запуск

Нужен Docker. Положите датасет в `data/logs.parquet` (в репозиторий он не входит) и выполните:

```bash
docker compose up --build
```

Контейнер `db-init` загрузит логи, построит теги и запустит SQL-проверки. Затем открывайте [страницу](http://localhost:8080/) или [API](http://localhost:8080/api/tags?q=платье), который возвращает `[{"tag":"красное платье","users":9264}, ...]`. Настройки сервиса: `CLICKHOUSE_ADDR`, `CLICKHOUSE_DATABASE`, `CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`, `PORT`, `QUERY_TIMEOUT`.

## Почему такая таблица

- **`MergeTree`:** логи загружаются партиями и потом только читаются, поэтому дедупликация и предагрегация не нужны.
- **`ORDER BY (user_id, timestamp)`:** данные уже лежат в порядке окна `PARTITION BY user_id ORDER BY timestamp`. Таблица занимает на 13% меньше, чем с `(timestamp, user_id)`.
- **Типы:** `LowCardinality(String)` для запроса, потому что разных значений всего 11 тыс. на 992 тыс. строк, а таблица меньше почти вдвое. `DateTime64(3, 'UTC')` и `UInt64` совпадают с Parquet.
- **Таблица тегов:** `ORDER BY (query, rank)` сразу даёт нужный порядок, а `EXCHANGE TABLES` обновляет её атомарно.

## Допущения

- Ровно 30 минут входят в интервал. В последовательности `A, A, B` учитывается пара `A → B`.
- При равном числе пользователей порядок задаёт `tag` по алфавиту.
- Ботов отдельно не фильтруем: порог по пользователям отсекает их, с ними и без них результат одинаковый.
- `ё` и `е` считаются разными буквами.

Находки в данных и замеры: [`docs/data_notes.md`](docs/data_notes.md). Ежедневный пересчёт: [`docs/daily_recalculation.md`](docs/daily_recalculation.md).

## Проверки

Результаты последнего запуска лежат в [`results/`](results/). Интеграционным тестам нужен запущенный ClickHouse.

```bash
go test ./...
```

```bash
go test -tags=integration ./tests/integration/
```

```bash
docker compose exec clickhouse clickhouse-client --user app --password clickhouse --database search --queries-file /sql/04_checks.sql
```
