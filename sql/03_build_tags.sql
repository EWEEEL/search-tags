-- Построение поисковых тегов. Номера в комментариях соответствуют правилам из задания.

-- 1. Нормализация: нижний регистр, повторные пробелы, пробелы по краям.
--    lowerUTF8, а не lower: lower меняет только ASCII и оставил бы «ПЛАТЬЕ» как есть.
--    Логика совпадает с search.Normalize в Go, это проверяет интеграционный тест.
CREATE OR REPLACE FUNCTION normalize_query AS (s) ->
    trimBoth(replaceRegexpAll(lowerUTF8(s), '\\s+', ' '));

CREATE TABLE IF NOT EXISTS search_tags
(
    query String,
    tag   String,
    users UInt64,
    rank  UInt8
)
ENGINE = MergeTree
ORDER BY (query, rank);

-- Собираем во временной таблице и подменяем одним EXCHANGE,
-- чтобы сервис не увидел пустую или наполовину заполненную таблицу.
DROP TABLE IF EXISTS search_tags_new;
CREATE TABLE search_tags_new AS search_tags;

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
        WINDOW w AS (PARTITION BY user_id ORDER BY timestamp ASC
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
    -- 7. Топ-10. tag в сортировке делает порядок стабильным при равном числе пользователей.
    toUInt8(row_number() OVER (PARTITION BY query ORDER BY users DESC, tag ASC)) AS rank
FROM pairs
QUALIFY rank <= 10;

EXCHANGE TABLES search_tags AND search_tags_new;
DROP TABLE search_tags_new;
