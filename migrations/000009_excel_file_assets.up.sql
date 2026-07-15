-- 为 Excel/CSV 数据源建立对象存储定位和不可变文件版本历史。
CREATE TABLE platform.file_assets(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  filename text NOT NULL,
  mime_type text NOT NULL,
  current_version integer NOT NULL DEFAULT 1 CHECK(current_version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  UNIQUE(id,tenant_id)
);

CREATE TABLE platform.file_asset_versions(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  file_asset_id uuid NOT NULL,
  version integer NOT NULL CHECK(version > 0),
  filename text NOT NULL,
  mime_type text NOT NULL,
  size_bytes bigint NOT NULL CHECK(size_bytes > 0),
  sha256 text NOT NULL CHECK(length(sha256)=64),
  storage_bucket text NOT NULL,
  storage_key text NOT NULL,
  parse_config jsonb NOT NULL DEFAULT '{}',
  workbook_summary jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(file_asset_id,tenant_id) REFERENCES platform.file_assets(id,tenant_id),
  UNIQUE(tenant_id,file_asset_id,version),
  UNIQUE(storage_bucket,storage_key),
  UNIQUE(id,tenant_id)
);

ALTER TABLE platform.data_sources DROP CONSTRAINT data_source_secret_or_file;
ALTER TABLE platform.data_sources ADD CONSTRAINT data_source_secret_or_file CHECK(
  (source_type='EXCEL' AND file_asset_id IS NOT NULL AND secret_ref IS NULL) OR
  (source_type IN ('MYSQL','ORACLE') AND secret_ref IS NOT NULL AND file_asset_id IS NULL)
);
ALTER TABLE platform.data_sources ADD CONSTRAINT data_sources_file_asset_fk
  FOREIGN KEY(file_asset_id,tenant_id) REFERENCES platform.file_assets(id,tenant_id);

CREATE INDEX file_assets_tenant_time_idx ON platform.file_assets(tenant_id,created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX file_asset_versions_asset_idx ON platform.file_asset_versions(tenant_id,file_asset_id,version DESC);
CREATE TRIGGER file_assets_set_updated_at BEFORE UPDATE ON platform.file_assets FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.file_assets ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.file_assets FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.file_asset_versions ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.file_asset_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY file_assets_tenant_isolation ON platform.file_assets USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY file_asset_versions_tenant_isolation ON platform.file_asset_versions USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.file_asset_versions IS 'Immutable file versions; published datasets reference a fixed version id';
