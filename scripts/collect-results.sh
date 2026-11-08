#!/usr/bin/env bash
# Заново собирает папку results/ на запущенном стеке: тесты, SQL-проверки, профиль данных и запросы к API.
#
#   docker compose up --build -d
#   bash scripts/collect-results.sh
#
# Работает в Linux, macOS и Git Bash на Windows.
set -euo pipefail

cd "$(dirname "$0")/.."
# Git Bash иначе превращает пути внутри контейнера (/sql/...) в пути Windows.
export MSYS_NO_PATHCONV=1

PASSWORD="${CLICKHOUSE_PASSWORD:-clickhouse}"
PORT="${HTTP_PORT:-8080}"
BASE="http://localhost:$PORT"

ch() {
  docker compose exec -T clickhouse clickhouse-client \
    --user app --password "$PASSWORD" --database search "$@"
}

echo "==> results/go_tests.txt"
{
  echo "\$ go version"
  go version
  echo
  echo "\$ go test -count=1 -v ./..."
  go test -count=1 -v ./...
  echo
  echo "\$ CLICKHOUSE_ADDR=localhost:9000 CLICKHOUSE_USER=app CLICKHOUSE_PASSWORD=*** go test -count=1 -v -tags=integration ./tests/integration/"
  CLICKHOUSE_ADDR=localhost:9000 CLICKHOUSE_USER=app CLICKHOUSE_PASSWORD="$PASSWORD" \
    go test -count=1 -v -tags=integration ./tests/integration/
} > results/go_tests.txt 2>&1

echo "==> results/sql_checks.txt"
{
  echo "\$ clickhouse-client --param_table=search_tags --queries-file /sql/04_checks.sql"
  ch --multiquery --param_table=search_tags --queries-file /sql/04_checks.sql
} > results/sql_checks.txt 2>&1

echo "==> results/data_quality.txt"
ch --multiquery --queries-file /sql/data_quality.sql > results/data_quality.txt 2>&1

echo "==> results/api_smoke.txt"
request() {
  local note="$1" path="$2"
  echo "\$ curl -i '$BASE$path'   # $note"
  curl -s -i "$BASE$path" | tr -d '\r' | grep -iE '^(HTTP/|content-type|cache-control)'
  echo
  curl -s "$BASE$path"
  echo
}
{
  request "платье" "/api/tags?q=%D0%BF%D0%BB%D0%B0%D1%82%D1%8C%D0%B5"
  request "«Платье » с пробелом: тот же ответ" "/api/tags?q=%D0%9F%D0%BB%D0%B0%D1%82%D1%8C%D0%B5%20"
  request "тегов нет: пустой список" "/api/tags?q=%D0%BA%D0%BE%D0%BD%D1%8C%20%D0%B2%20%D0%BF%D0%B0%D0%BB%D1%8C%D1%82%D0%BE"
  request "пустой q: 400" "/api/tags?q=%20%20"
  request "попытка SQL-инъекции" "/api/tags?q=%27%20OR%201%3D1%20--"
  request "жив ли процесс" "/healthz"
  request "готов ли сервис" "/readyz"
} > results/api_smoke.txt

echo "Готово: results/ обновлена."
