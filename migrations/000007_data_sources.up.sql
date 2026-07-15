-- 建立数据源生命周期、租户配额和审计所需的持久化结构。
CREATE TYPE platform.data_source_type AS ENUM ('MYSQL','ORACLE','EXCEL');
CREATE TYPE platform.data_source_status AS ENUM ('DRAFT','ACTIVE','DISABLED','SYNCING','ERROR','DELETING','DELETED');

CREATE TABLE platform.data_sources(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code citext NOT NULL,
  name text NOT NULL,
  source_type platform.data_source_type NOT NULL,
  status platform.data_source_status NOT NULL DEFAULT 'DRAFT',
  config jsonb NOT NULL DEFAULT '{}',
  secret_ref text,
  file_asset_id uuid,
  last_tested_at timestamptz,
  last_synced_at timestamptz,
  last_error text,
  version bigint NOT NULL DEFAULT 1 CHECK(version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  deleted_at timestamptz,
  CONSTRAINT data_source_code_not_blank CHECK(btrim(code::text)<>''),
  CONSTRAINT data_source_name_not_blank CHECK(btrim(name)<>''),
  CONSTRAINT data_source_secret_or_file CHECK((source_type='EXCEL' AND file_asset_id IS NOT NULL AND secret_ref IS NULL) OR (source_type IN ('MYSQL','ORACLE') AND secret_ref IS NOT NULL AND file_asset_id IS NULL)),
  UNIQUE(tenant_id,code), UNIQUE(id,tenant_id)
);

CREATE TABLE platform.tenant_data_source_quotas(
  tenant_id uuid PRIMARY KEY REFERENCES platform.tenants(id),
  max_data_sources integer NOT NULL DEFAULT 20 CHECK(max_data_sources>0),
  max_connections_per_source integer NOT NULL DEFAULT 5 CHECK(max_connections_per_source>0),
  max_concurrent_queries integer NOT NULL DEFAULT 10 CHECK(max_concurrent_queries>0),
  max_excel_file_bytes bigint NOT NULL DEFAULT 52428800 CHECK(max_excel_file_bytes>0),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX data_sources_tenant_type_status_idx ON platform.data_sources(tenant_id,source_type,status) WHERE deleted_at IS NULL;
CREATE TRIGGER data_sources_set_updated_at BEFORE UPDATE ON platform.data_sources FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
ALTER TABLE platform.data_sources ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.data_sources FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.tenant_data_source_quotas ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.tenant_data_source_quotas FORCE ROW LEVEL SECURITY;
CREATE POLICY data_sources_tenant_isolation ON platform.data_sources USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY tenant_data_source_quotas_isolation ON platform.tenant_data_source_quotas USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
COMMENT ON COLUMN platform.data_sources.secret_ref IS 'Reference to an external secret; plaintext credentials are forbidden';
