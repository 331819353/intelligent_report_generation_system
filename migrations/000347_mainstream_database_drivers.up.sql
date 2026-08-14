-- Expand the data-source contract from two database engines to the shared
-- mainstream driver catalog used by the API, workers and connector service.
ALTER TYPE platform.data_source_type ADD VALUE IF NOT EXISTS 'MARIADB' AFTER 'MYSQL';
ALTER TYPE platform.data_source_type ADD VALUE IF NOT EXISTS 'POSTGRESQL' AFTER 'MARIADB';
ALTER TYPE platform.data_source_type ADD VALUE IF NOT EXISTS 'SQLSERVER' AFTER 'ORACLE';
ALTER TYPE platform.data_source_type ADD VALUE IF NOT EXISTS 'CLICKHOUSE' AFTER 'SQLSERVER';

COMMENT ON TYPE platform.data_source_type IS
  'Supported sources: MySQL, MariaDB, PostgreSQL, Oracle, SQL Server, ClickHouse and Excel/CSV';
