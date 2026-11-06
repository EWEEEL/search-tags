-- Проверки результата. Каждая строка показывает число нарушений, норма равна 0.
-- Последний запрос завершается ошибкой, если нарушение есть, поэтому файл подходит для CI.
CREATE TEMPORARY TABLE check_results ENGINE = Memory AS
SELECT check_name, violations
FROM
(
    -- Требования из задания
    SELECT '1. нет строк, где query = tag' AS check_name, count() AS violations
    FROM search_tags WHERE query = tag

    UNION ALL
    SELECT '2. нет строк, где users < 5', count()
    FROM search_tags WHERE users < 5

    UNION ALL
    SELECT '3. на каждый query не больше 10 тегов', count()
    FROM (SELECT query FROM search_tags GROUP BY query HAVING count() > 10)

    UNION ALL
    SELECT '4. «Платье» и «платье » дают один результат и он не пустой',
        toUInt64(NOT (a = b AND notEmpty(a)))
    FROM
    (
        SELECT
            (SELECT arraySort(x -> x.3, groupArray((tag, users, rank)))
             FROM search_tags WHERE query = normalize_query('Платье')) AS a,
            (SELECT arraySort(x -> x.3, groupArray((tag, users, rank)))
             FROM search_tags WHERE query = normalize_query('платье ')) AS b
    )

    -- Дополнительные инварианты
    UNION ALL
    SELECT '5. таблица тегов не пустая', toUInt64(count() = 0)
    FROM search_tags

    UNION ALL
    SELECT '6. query и tag нормализованы', count()
    FROM search_tags
    WHERE query != normalize_query(query) OR tag != normalize_query(tag)

    UNION ALL
    SELECT '7. rank идёт подряд с 1 без пропусков', count()
    FROM
    (
        SELECT query
        FROM search_tags
        GROUP BY query
        HAVING min(rank) != 1 OR max(rank) != count() OR uniqExact(rank) != count()
    )

    UNION ALL
    SELECT '8. чем выше rank, тем не меньше users', count()
    FROM
    (
        SELECT query
        FROM search_tags
        GROUP BY query
        HAVING NOT arrayAll((u, n) -> u >= n,
            arrayPopBack(arrayMap(x -> x.2, arraySort(groupArray((rank, users))))),
            arrayPopFront(arrayMap(x -> x.2, arraySort(groupArray((rank, users))))))
    )

    UNION ALL
    SELECT '9. пары, созданные только ботами, не попали в теги', count()
    FROM search_tags
    WHERE tag IN ('странный запрос', 'кроссовки nike акция')
);

SELECT
    check_name,
    violations,
    if(violations = 0, 'OK', 'FAIL') AS status
FROM check_results
ORDER BY check_name
FORMAT PrettyCompactMonoBlock;

SELECT throwIf(sum(violations) > 0, 'SQL-проверки не пройдены')
FROM check_results
FORMAT Null;
