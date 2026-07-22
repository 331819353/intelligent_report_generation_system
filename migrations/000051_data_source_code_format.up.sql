-- 数据源编码统一为可移植的 ASCII 标识；旧的中文文件编码按原编码 MD5 平滑迁移。
UPDATE platform.data_sources
SET code = CASE WHEN source_type = 'EXCEL' THEN 'file_' ELSE 'source_' END || md5(code::text)
WHERE code::text !~ '^[A-Za-z][A-Za-z0-9_]{0,127}$';

ALTER TABLE platform.data_sources
  ADD CONSTRAINT data_source_code_format
  CHECK(code::text ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$');

COMMENT ON CONSTRAINT data_source_code_format ON platform.data_sources
  IS 'Portable data source identifier: starts with an ASCII letter and contains only ASCII letters, digits, or underscores';
