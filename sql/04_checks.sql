-- Проверки таблицы тегов. Каждая строка показывает число нарушений, норма равна 0.
-- Последний запрос завершается ошибкой, если нарушение есть: db-init тогда останавливается
-- до публикации, и сервис продолжает отдавать прежние теги.
--
-- Какую таблицу проверять, задаёт параметр table:
--   новая сборка перед публикацией (так делает scripts/init-db.sh): --param_table=search_tags_new
--   то, что сервис отдаёт сейчас:                                    --param_table=search_tags

-- Временная таблица живёт в сессии: при повторном запуске начинаем с чистой.
DROP TEMPORARY TABLE IF EXISTS check_results;
CREATE TEMPORARY TABLE check_results ENGINE = Memory AS
SELECT check_name, violations
FROM
(
    -- Требования из задания
    SELECT '1. нет строк, где query = tag' AS check_name, count() AS violations
    FROM {table:Identifier} WHERE query = tag

    UNION ALL
    SELECT '2. нет строк, где users < 5', count()
    FROM {table:Identifier} WHERE users < 5

    UNION ALL
    SELECT '3. на каждый query не больше 10 тегов', count()
    FROM (SELECT query FROM {table:Identifier} GROUP BY query HAVING count() > 10)

    UNION ALL
    SELECT '4. «Платье» и «платье » дают один результат и он не пустой',
        toUInt64(NOT (a = b AND notEmpty(a)))
    FROM
    (
        SELECT
            (SELECT arraySort(x -> x.3, groupArray((tag, users, rank)))
             FROM {table:Identifier} WHERE query = normalize_query('Платье')) AS a,
            (SELECT arraySort(x -> x.3, groupArray((tag, users, rank)))
             FROM {table:Identifier} WHERE query = normalize_query('платье ')) AS b
    )

    -- Дополнительные инварианты
    UNION ALL
    SELECT '5. таблица тегов не пустая', toUInt64(count() = 0)
    FROM {table:Identifier}

    UNION ALL
    SELECT '6. query и tag нормализованы', count()
    FROM {table:Identifier}
    WHERE query != normalize_query(query) OR tag != normalize_query(tag)

    UNION ALL
    SELECT '7. rank идёт подряд с 1 без пропусков', count()
    FROM
    (
        SELECT query
        FROM {table:Identifier}
        GROUP BY query
        HAVING min(rank) != 1 OR max(rank) != count() OR uniqExact(rank) != count()
    )

    UNION ALL
    SELECT '8. чем выше rank, тем не меньше users', count()
    FROM
    (
        SELECT query
        FROM {table:Identifier}
        GROUP BY query
        HAVING NOT arrayAll((u, n) -> u >= n,
            arrayPopBack(arrayMap(x -> x.2, arraySort(groupArray((rank, users))))),
            arrayPopFront(arrayMap(x -> x.2, arraySort(groupArray((rank, users))))))
    )

    UNION ALL
    SELECT '9. пары, созданные только ботами, не попали в теги', count()
    FROM {table:Identifier}
    WHERE tag IN ('странный запрос', 'кроссовки nike акция')

    -- Защита от неполной загрузки: абсолютные проверки пройдёт и сборка по 5% логов.
    UNION ALL
    SELECT '10. тегов не меньше половины от опубликованных сейчас',
        toUInt64(published > 0 AND candidate < published / 2)
    FROM
    (
        SELECT
            (SELECT count() FROM {table:Identifier}) AS candidate,
            (SELECT count() FROM search_tags) AS published
    )
);

SELECT
    check_name,
    violations,
    if(violations = 0, 'OK', 'FAIL') AS status
FROM check_results
ORDER BY toUInt32OrZero(extract(check_name, '^(\\d+)'))
FORMAT PrettyCompactMonoBlock;

SELECT throwIf(sum(violations) > 0, 'SQL-проверки не пройдены')
FROM check_results
FORMAT Null;
