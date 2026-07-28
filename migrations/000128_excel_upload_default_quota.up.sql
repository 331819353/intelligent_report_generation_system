-- 将默认文件数据源配额提高到 256 MiB，并为缺少显式配额的现有/新增租户
-- 创建独立记录。租户已经配置的更严格或更宽松限额保持不变。
ALTER TABLE platform.tenant_data_source_quotas
  ALTER COLUMN max_excel_file_bytes SET DEFAULT 268435456;

CREATE OR REPLACE FUNCTION platform.create_default_tenant_data_source_quota()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  INSERT INTO platform.tenant_data_source_quotas(tenant_id)
  VALUES(NEW.id)
  ON CONFLICT (tenant_id) DO NOTHING;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.create_default_tenant_data_source_quota()
  FROM PUBLIC;

DROP TRIGGER IF EXISTS tenants_create_default_data_source_quota
  ON platform.tenants;
CREATE TRIGGER tenants_create_default_data_source_quota
AFTER INSERT ON platform.tenants
FOR EACH ROW
EXECUTE FUNCTION platform.create_default_tenant_data_source_quota();

INSERT INTO platform.tenant_data_source_quotas(tenant_id)
SELECT tenant.id
FROM platform.tenants AS tenant
ON CONFLICT (tenant_id) DO NOTHING;

COMMENT ON COLUMN platform.tenant_data_source_quotas.max_excel_file_bytes IS
  'Excel/CSV 原始文件上传上限；默认 256 MiB，可按租户显式覆盖';
