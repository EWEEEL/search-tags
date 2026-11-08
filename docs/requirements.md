# Пункты задания и где они закрыты

Ссылки ведут на код, а колонка «Чем проверено» — на тест или проверку, которая упадёт, если пункт сломать.

## Часть 1. ClickHouse

| Требование | Где | Чем проверено |
|---|---|---|
| Загрузить логи `timestamp, user_id, search_query` | [`sql/01_create_logs.sql`](../sql/01_create_logs.sql), [`sql/02_load_logs.sql`](../sql/02_load_logs.sql) | `db-init` при `docker compose up`, [`results/data_quality.txt`](../results/data_quality.txt) |
| Нормализация: нижний регистр, пробелы по краям, повторные пробелы | [`sql/03_build_tags.sql:8`](../sql/03_build_tags.sql#L8) | `TestBuildRules` / «написания сливаются», проверка 6 в `04_checks.sql` |
| Пара — соседние запросы одного пользователя | [`sql/03_build_tags.sql:45`](../sql/03_build_tags.sql#L45) | `TestBuildRules` / «только соседние запросы», «разные пользователи пару не образуют» |
| Пауза не больше 30 минут | [`sql/03_build_tags.sql:64`](../sql/03_build_tags.sql#L64) | `TestBuildRules` / «ровно 30 минут входят, больше нет» (до миллисекунды) |
| Второй запрос не равен первому | [`sql/03_build_tags.sql:65`](../sql/03_build_tags.sql#L65) | `TestBuildRules` / «повтор после нормализации не пара», проверка 1 |
| Уникальные пользователи, порог ≥ 5 | [`sql/03_build_tags.sql:60`](../sql/03_build_tags.sql#L60), [`:67`](../sql/03_build_tags.sql#L67) | «порог в 5 пользователей», «считаются пользователи, а не события», проверки 2 и 9 |
| Топ-10 в таблицу `query, tag, users, rank` | [`sql/03_build_tags.sql:15`](../sql/03_build_tags.sql#L15), [`:78`](../sql/03_build_tags.sql#L78) | «топ-10 со стабильным порядком», «больше 255 кандидатов», проверки 3, 7, 8 |
| Типы, движок, `ORDER BY` с обоснованием | [README](../README.md#почему-так), замеры в [`data_notes.md`](data_notes.md) | цифры получены на этих логах |
| Проверить данные на грязь | [`sql/data_quality.sql`](../sql/data_quality.sql) | [`results/data_quality.txt`](../results/data_quality.txt), выводы в [`data_notes.md`](data_notes.md) |

## Часть 2. Сервис на Go

| Требование | Где | Чем проверено |
|---|---|---|
| `GET /api/tags?q=` → JSON-список `tag`, `users` по rank | [`internal/web/server.go:41`](../internal/web/server.go#L41), [`api.go`](../internal/web/api.go) | `TestAPIReturnsTagsInStoreOrder`, интеграционный `TestStoreAndAPI` |
| Нормализация как в SQL | [`internal/search/search.go:24`](../internal/search/search.go#L24) | `TestNormalize`, `FuzzNormalize`, `TestNormalizeParityAllCodePoints` (3,3 млн строк против ClickHouse) |
| Нет тегов → 200 и `[]` | [`api.go`](../internal/web/api.go) | `TestAPINoTagsReturnsEmptyList` |
| Пустой `q` → 400 | [`api.go`](../internal/web/api.go) | `TestAPIEmptyQueryReturns400` |
| SQL только с параметрами | [`internal/storage/clickhouse.go:18`](../internal/storage/clickhouse.go#L18) | «спецсимволы передаются как значение, а не как SQL», `TestParameterRoundTrip` |
| Адрес ClickHouse и порт из env | [`internal/config/config.go:30`](../internal/config/config.go#L30) | `TestLoadFromEnvironment` |
| Таймаут | [`internal/web/server.go:65`](../internal/web/server.go#L65) | `TestAPITimeoutReturns504`, ручная проверка с `docker pause` |
| Обработка ошибок | [`internal/web/server.go:84`](../internal/web/server.go#L84) | `TestAPIStoreErrorDoesNotLeakDetails`, `TestClientDisconnectIsNotServerError` |
| Логи | [`internal/web/server.go:181`](../internal/web/server.go#L181) | `TestRequestIDIsGeneratedAndLogged`, `TestLookupErrorIsLoggedWithRequestID` |
| `net/http` и `clickhouse-go` | [`go.mod`](../go.mod) | — |

## Часть 3. Страница

| Требование | Где | Чем проверено |
|---|---|---|
| `html/template`, поле и кнопка «Найти» | [`templates/index.html:29`](../internal/web/templates/index.html#L29) | `TestPageWithoutQueryShowsOnlyForm` |
| До 10 кликабельных тегов, клик подставляет тег в поле | [`templates/index.html:36`](../internal/web/templates/index.html#L36) | `TestPageShowsClickableTags` |
| «Для этого запроса пока нет тегов» | [`templates/index.html:40`](../internal/web/templates/index.html#L40) | `TestPageNoTagsMessage` |
| Экранирование не отключено | [`page.go`](../internal/web/page.go) | `TestPageEscapesUserInput`, `TestPageEscapesTags` |

## Часть 4. Проверки

| Требование | Где | Чем проверено |
|---|---|---|
| Нет `query = tag`, нет `users < 5`, не больше 10 тегов | [`sql/04_checks.sql:16`](../sql/04_checks.sql#L16) | проверки 1–3; `TestChecksBlockBrokenBuild` доказывает, что они ловят нарушения |
| «Платье» и «платье » дают один результат | [`sql/04_checks.sql:28`](../sql/04_checks.sql#L28) | проверка 4, `TestStoreAndAPI` |
| Go unit-тест нормализации | [`internal/search/search_test.go:10`](../internal/search/search_test.go#L10) | — |
| httptest: пустой `q` → 400 | [`internal/web/web_test.go:73`](../internal/web/web_test.go#L73) | — |
| Результаты запуска | [`results/`](../results/) | пересобираются `bash scripts/collect-results.sh` |

## Бонус

Ежедневный пересчёт описан в [`daily_recalculation.md`](daily_recalculation.md).
