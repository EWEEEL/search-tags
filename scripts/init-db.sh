#!/usr/bin/env bash
# Создаёт таблицы, загружает логи, строит теги и запускает проверки.
# Скрипт можно запускать повторно: результат будет тем же.
set -euo pipefail

if [[ ! -f /data/logs.parquet ]]; then
  echo "ОШИБКА: не найден файл data/logs.parquet. Положите датасет в папку data (см. README)." >&2
  exit 1
fi

client() {
  clickhouse-client \
    --host "$CLICKHOUSE_HOST" \
    --user "$CLICKHOUSE_USER" \
    --password "$CLICKHOUSE_PASSWORD" \
    --database "$CLICKHOUSE_DATABASE" \
    --multiquery \
    "$@"
}

for file in 01_create_logs 02_load_logs 03_build_tags; do
  echo "==> sql/$file.sql"
  client --queries-file "/sql/$file.sql"
done

echo "==> sql/04_checks.sql"
client --queries-file /sql/04_checks.sql

echo "Готово: логов $(client --query 'SELECT count() FROM search_logs'), тегов $(client --query 'SELECT count() FROM search_tags')."
