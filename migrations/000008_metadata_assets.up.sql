-- 保存表、字段技术元数据的当前态、不可变快照与结构差异。
CREATE TYPE platform.metadata_asset_status AS ENUM ('ACTIVE','INACTIVE');
CREATE TYPE platform.metadata_change_type AS ENUM ('ADDED','CHANGED','REMOVED');

CREATE TABLE platform.metadata_tables(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  catalog_name text NOT NULL DEFAULT '',
  schema_name text NOT NULL,
  table_name text NOT NULL,
  table_type text NOT NULL,
  source_comment text NOT NULL DEFAULT '',
  estimated_row_count bigint,
  primary_key_columns jsonb NOT NULL DEFAULT '[]',
  constraints_json jsonb NOT NULL DEFAULT '[]',
  indexes_json jsonb NOT NULL DEFAULT '[]',
  structure_hash text NOT NULL,
  metadata_version bigint NOT NULL DEFAULT 1 CHECK(metadata_version > 0),
  asset_status platform.metadata_asset_status NOT NULL DEFAULT 'ACTIVE',
  last_sync_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id),
  UNIQUE(tenant_id,data_source_id,catalog_name,schema_name,table_name),
  UNIQUE(id,tenant_id)
);

CREATE TABLE platform.metadata_columns(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  table_id uuid NOT NULL,
  column_name text NOT NULL,
  ordinal_position integer NOT NULL CHECK(ordinal_position > 0),
  source_comment text NOT NULL DEFAULT '',
  native_type text NOT NULL,
  canonical_type text NOT NULL,
  length bigint,
  numeric_precision integer,
  numeric_scale integer,
  nullable boolean NOT NULL,
  default_value text,
  is_primary_key boolean NOT NULL DEFAULT false,
  is_foreign_key boolean NOT NULL DEFAULT false,
  is_unique boolean NOT NULL DEFAULT false,
  structure_hash text NOT NULL,
  asset_status platform.metadata_asset_status NOT NULL DEFAULT 'ACTIVE',
  last_sync_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(table_id,tenant_id) REFERENCES platform.metadata_tables(id,tenant_id),
  UNIQUE(tenant_id,table_id,column_name)
);

CREATE TABLE platform.metadata_snapshots(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  snapshot_hash text NOT NULL,
  snapshot_json jsonb NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id)
);

CREATE TABLE platform.metadata_diffs(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  data_source_id uuid NOT NULL,
  object_type text NOT NULL CHECK(object_type IN ('TABLE','COLUMN')),
  object_key text NOT NULL,
  change_type platform.metadata_change_type NOT NULL,
  before_json jsonb,
  after_json jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(data_source_id,tenant_id) REFERENCES platform.data_sources(id,tenant_id)
);

CREATE INDEX metadata_tables_source_status_idx ON platform.metadata_tables(tenant_id,data_source_id,asset_status);
CREATE INDEX metadata_columns_table_status_idx ON platform.metadata_columns(tenant_id,table_id,asset_status);
CREATE INDEX metadata_snapshots_source_time_idx ON platform.metadata_snapshots(tenant_id,data_source_id,created_at DESC);
CREATE INDEX metadata_diffs_source_time_idx ON platform.metadata_diffs(tenant_id,data_source_id,created_at DESC);
CREATE TRIGGER metadata_tables_set_updated_at BEFORE UPDATE ON platform.metadata_tables FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();
CREATE TRIGGER metadata_columns_set_updated_at BEFORE UPDATE ON platform.metadata_columns FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.metadata_tables ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.metadata_tables FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metadata_columns ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.metadata_columns FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metadata_snapshots ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.metadata_snapshots FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.metadata_diffs ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.metadata_diffs FORCE ROW LEVEL SECURITY;
CREATE POLICY metadata_tables_tenant_isolation ON platform.metadata_tables USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metadata_columns_tenant_isolation ON platform.metadata_columns USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metadata_snapshots_tenant_isolation ON platform.metadata_snapshots USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY metadata_diffs_tenant_isolation ON platform.metadata_diffs USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
