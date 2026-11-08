-- Публикация проверенной сборки: одним EXCHANGE меняем живую таблицу на новую.
-- Сервис видит либо прежние теги, либо новые; пустой или недостроенной таблицы он не видит.
-- Запускается только после того, как 04_checks.sql прошёл на search_tags_new.
EXCHANGE TABLES search_tags AND search_tags_new;
DROP TABLE search_tags_new;
