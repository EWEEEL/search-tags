-- Построение поисковых тегов. Номера в комментариях соответствуют правилам из задания.

-- 1. Нормализация: нижний регистр, повторные пробелы, пробелы по краям.
--    lowerUTF8, а не lower: lower меняет только ASCII и оставил бы «ПЛАТЬЕ» как есть.
--    Сервис ищет теги по normalize_query(q) в самом запросе, поэтому ключ всегда совпадает
--    с тем, что здесь записано. search.Normalize в Go повторяет функцию символ в символ,
--    это проверяют интеграционные тесты на всех кодовых точках Unicode.
CREATE OR REPLACE FUNCTION normalize_query AS (s) ->
    trimBoth(replaceRegexpAll(lowerUTF8(s), '\\s+', ' '));

-- Собираем в search_tags_new. Живую search_tags этот файл не трогает: 04_checks.sql проверяет
-- новую сборку, и только потом 05_publish.sql подменяет таблицу одним EXCHANGE.
-- Схема описана здесь целиком, поэтому её изменение доходит и до уже существующего тома.
DROP TABLE IF EXISTS search_tags_new;
CREATE TABLE search_tags_new
(
    query String,
    tag   String,
    users UInt64,
    rank  UInt8
)
ENGINE = MergeTree
ORDER BY (query, rank);

-- При первом запуске подменять ещё нечего: создаём пустую таблицу той же схемы.
CREATE TABLE IF NOT EXISTS search_tags AS search_tags_new;

INSERT INTO search_tags_new (query, tag, users, rank)
WITH
    normalized AS
    (
        SELECT
            user_id,
            timestamp,
            normalize_query(search_query) AS query
        FROM search_logs
        WHERE query != ''
    ),
    -- 2. Соседние запросы одного пользователя в порядке времени.
    --    Нормализуем до поиска соседей, иначе «Платье» и «платье » стали бы парой.
    neighbours AS
    (
        SELECT
            user_id,
            lagInFrame(query) OVER w     AS first_query,
            lagInFrame(timestamp) OVER w AS first_ts,
            query                        AS second_query,
            timestamp                    AS second_ts,
            row_number() OVER w          AS position
        FROM normalized
        -- query во втором ключе: при одинаковом времени порядок соседей не зависит от слияния частей.
        WINDOW w AS (PARTITION BY user_id ORDER BY timestamp ASC, query ASC
                     ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
    ),
    pairs AS
    (
        SELECT
            first_query  AS query,
            second_query AS tag,
            -- 5. uniqExact, а не uniq: uniq приближённый и может сдвинуть места в топе.
            uniqExact(user_id) AS users
        FROM neighbours
        WHERE position > 1                                         -- у первого запроса нет соседа слева
          AND dateDiff('millisecond', first_ts, second_ts) <= 1800000 -- 3. не больше 30 минут включительно
          AND first_query != second_query                          -- 4. повтор того же запроса не пара
        GROUP BY query, tag
        HAVING users >= 5                                          -- 6. минимум 5 пользователей
    )
SELECT
    query,
    tag,
    users,
    toUInt8(row_number() OVER top) AS rank
FROM pairs
-- 7. Топ-10. tag в сортировке делает порядок стабильным при равном числе пользователей.
WINDOW top AS (PARTITION BY query ORDER BY users DESC, tag ASC)
-- Фильтруем по исходному номеру: toUInt8 после 255 начинает считать заново.
QUALIFY row_number() OVER top <= 10;
