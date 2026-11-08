-- Таблица с сырыми логами поиска. Почему такие типы, движок и ORDER BY, см. README.
-- Текст запроса храним как есть: нормализация относится к расчёту, а исходник нужен для аудита.
CREATE TABLE IF NOT EXISTS search_logs
(
    -- Внутри пользователя время растёт, поэтому Delta превращает его в маленькие приращения:
    -- колонка становится на 39% меньше, вся таблица на 27% (замер в docs/data_notes.md).
    timestamp    DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1)),
    user_id      UInt64,
    search_query LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(timestamp)
ORDER BY (user_id, timestamp);

-- CREATE IF NOT EXISTS не меняет таблицу, созданную прежней версией: кодек догоняем отдельно.
-- Дальше 02 всё равно перезаливает логи, так что это дёшево.
ALTER TABLE search_logs MODIFY COLUMN timestamp DateTime64(3, 'UTC') CODEC(Delta, ZSTD(1));
