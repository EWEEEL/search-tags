-- Проверка качества исходных логов. Результат последнего запуска: results/data_quality.txt.

-- Общий объём и диапазон дат
SELECT count() AS rows, uniqExact(user_id) AS users, min(timestamp) AS first_ts, max(timestamp) AS last_ts
FROM search_logs
FORMAT Vertical;

-- Форматный мусор в запросах: всё это убирает нормализация
SELECT
    countIf(search_query = '')                             AS empty,
    countIf(normalize_query(search_query) = '')            AS blank_after_normalization,
    countIf(search_query != lowerUTF8(search_query))       AS has_upper_case,
    countIf(search_query != trimBoth(search_query))        AS has_edge_spaces,
    countIf(search_query LIKE '%  %')                      AS has_repeated_spaces,
    countIf(match(search_query, '[\\t\\n\\r]'))            AS has_tabs_or_newlines,
    uniqExact(search_query)                                AS distinct_raw,
    uniqExact(normalize_query(search_query))               AS distinct_normalized
FROM search_logs
FORMAT Vertical;

-- Варианты написания одного запроса
SELECT search_query, count() AS rows
FROM search_logs
WHERE normalize_query(search_query) = 'платье'
GROUP BY search_query
ORDER BY rows DESC
FORMAT PrettyCompactMonoBlock;

-- Полные дубли строк и одинаковое время у одного пользователя: от этого зависит порядок соседей
SELECT
    (SELECT count() FROM search_logs) - (SELECT count() FROM (SELECT DISTINCT * FROM search_logs)) AS full_duplicates,
    (SELECT count() FROM (SELECT 1 FROM search_logs GROUP BY user_id, timestamp HAVING count() > 1)) AS same_user_same_timestamp
FORMAT Vertical;

-- Самые активные пользователи: первые двое повторяют пару запросов как боты
SELECT
    user_id,
    count() AS rows,
    uniqExact(normalize_query(search_query)) AS distinct_queries,
    round(rows / distinct_queries, 1) AS repeats_per_query
FROM search_logs
GROUP BY user_id
ORDER BY rows DESC
LIMIT 5
FORMAT PrettyCompactMonoBlock;

-- Зачем считать пользователей, а не события: у пары бота много событий, но всего один пользователь
SELECT first_query, second_query, count() AS events, uniqExact(user_id) AS users
FROM
(
    SELECT
        user_id,
        lagInFrame(normalize_query(search_query)) OVER w AS first_query,
        normalize_query(search_query) AS second_query
    FROM search_logs
    WINDOW w AS (PARTITION BY user_id ORDER BY timestamp ASC ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW)
)
WHERE (first_query, second_query) IN (('платье', 'странный запрос'), ('кроссовки', 'кроссовки nike акция'))
GROUP BY first_query, second_query
FORMAT PrettyCompactMonoBlock;
