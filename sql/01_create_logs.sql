-- Таблица с сырыми логами поиска. Почему такие типы, движок и ORDER BY, см. README.
-- Текст запроса храним как есть: нормализация относится к расчёту, а исходник нужен для аудита.
CREATE TABLE IF NOT EXISTS search_logs
(
    timestamp    DateTime64(3, 'UTC'),
    user_id      UInt64,
    search_query LowCardinality(String)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(timestamp)
ORDER BY (user_id, timestamp);
