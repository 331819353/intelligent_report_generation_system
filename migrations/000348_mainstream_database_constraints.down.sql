DO $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM platform.data_sources
    WHERE source_type::text IN ('MARIADB','POSTGRESQL','SQLSERVER','CLICKHOUSE')
  ) OR EXISTS(
    SELECT 1 FROM platform.data_source_versions
    WHERE source_type::text IN ('MARIADB','POSTGRESQL','SQLSERVER','CLICKHOUSE')
  ) THEN
    RAISE EXCEPTION 'cannot restore legacy database constraints while rows still use new drivers';
  END IF;
END
$$;

ALTER TABLE platform.data_sources
  DROP CONSTRAINT data_source_secret_or_file,
  ADD CONSTRAINT data_source_secret_or_file CHECK(
    (source_type='EXCEL' AND file_asset_id IS NOT NULL AND secret_ref IS NULL)
    OR
    (source_type IN ('MYSQL','ORACLE') AND secret_ref IS NOT NULL AND file_asset_id IS NULL)
  );

ALTER TABLE platform.data_source_versions
  DROP CONSTRAINT data_source_version_secret_or_file,
  ADD CONSTRAINT data_source_version_secret_or_file CHECK(
    (source_type='EXCEL' AND file_asset_id IS NOT NULL AND file_version_id IS NOT NULL AND secret_ref IS NULL)
    OR
    (source_type IN ('MYSQL','ORACLE') AND secret_ref IS NOT NULL AND file_asset_id IS NULL AND file_version_id IS NULL)
  );
