ALTER TABLE platform.data_sources
  DROP CONSTRAINT data_source_secret_or_file,
  ADD CONSTRAINT data_source_secret_or_file CHECK(
    (source_type='EXCEL' AND file_asset_id IS NOT NULL AND secret_ref IS NULL)
    OR
    (source_type<>'EXCEL' AND secret_ref IS NOT NULL AND file_asset_id IS NULL)
  );

ALTER TABLE platform.data_source_versions
  DROP CONSTRAINT data_source_version_secret_or_file,
  ADD CONSTRAINT data_source_version_secret_or_file CHECK(
    (source_type='EXCEL' AND file_asset_id IS NOT NULL AND file_version_id IS NOT NULL AND secret_ref IS NULL)
    OR
    (source_type<>'EXCEL' AND secret_ref IS NOT NULL AND file_asset_id IS NULL AND file_version_id IS NULL)
  );
