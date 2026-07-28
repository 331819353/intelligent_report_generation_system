DROP TRIGGER IF EXISTS tenants_create_default_data_source_quota
  ON platform.tenants;
DROP FUNCTION IF EXISTS platform.create_default_tenant_data_source_quota();

UPDATE platform.tenant_data_source_quotas
SET max_excel_file_bytes=52428800,
    updated_at=clock_timestamp()
WHERE max_excel_file_bytes=268435456;

ALTER TABLE platform.tenant_data_source_quotas
  ALTER COLUMN max_excel_file_bytes SET DEFAULT 52428800;

COMMENT ON COLUMN platform.tenant_data_source_quotas.max_excel_file_bytes IS NULL;
