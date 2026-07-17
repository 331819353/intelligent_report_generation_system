-- 表资产生命周期与源表生命周期解耦：停用和删除只影响 PostgreSQL 资产。
ALTER TABLE platform.metadata_tables
  ADD COLUMN management_status text NOT NULL DEFAULT 'ENABLED'
  CHECK (management_status IN ('ENABLED','DISABLED'));

CREATE INDEX metadata_tables_management_idx
  ON platform.metadata_tables(tenant_id,data_source_id,asset_status,management_status);
