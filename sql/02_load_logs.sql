-- Загрузка датасета. Выполняется в clickhouse-client: FROM INFILE читает файл на стороне клиента.
-- TRUNCATE делает повторный запуск безопасным: данные не задвоятся.
TRUNCATE TABLE search_logs;

INSERT INTO search_logs (timestamp, user_id, search_query)
FROM INFILE '/data/logs.parquet'
FORMAT Parquet;
