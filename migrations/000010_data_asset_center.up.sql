-- 在技术元数据之上增加业务语义、敏感级别、可见性与依赖血缘。
CREATE TYPE platform.asset_sensitivity AS ENUM ('PUBLIC','INTERNAL','CONFIDENTIAL','RESTRICTED');
CREATE TYPE platform.asset_visibility AS ENUM ('PRIVATE','TENANT_PUBLIC');

ALTER TABLE platform.metadata_tables
  ADD COLUMN business_name text NOT NULL DEFAULT '',
  ADD COLUMN business_description text NOT NULL DEFAULT '',
  ADD COLUMN tags text[] NOT NULL DEFAULT '{}',
  ADD COLUMN sensitivity_level platform.asset_sensitivity NOT NULL DEFAULT 'INTERNAL',
  ADD COLUMN visibility platform.asset_visibility NOT NULL DEFAULT 'PRIVATE',
  ADD COLUMN manual_locked boolean NOT NULL DEFAULT false,
  ADD COLUMN business_version bigint NOT NULL DEFAULT 1 CHECK(business_version>0);

ALTER TABLE platform.metadata_columns
  ADD COLUMN business_name text NOT NULL DEFAULT '',
  ADD COLUMN business_description text NOT NULL DEFAULT '',
  ADD COLUMN tags text[] NOT NULL DEFAULT '{}',
  ADD COLUMN sensitivity_level platform.asset_sensitivity NOT NULL DEFAULT 'INTERNAL',
  ADD COLUMN semantic_type text NOT NULL DEFAULT '',
  ADD COLUMN manual_locked boolean NOT NULL DEFAULT false,
  ADD COLUMN business_version bigint NOT NULL DEFAULT 1 CHECK(business_version>0);

CREATE TABLE platform.asset_dependencies(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  upstream_type text NOT NULL CHECK(upstream_type IN ('TABLE','COLUMN')),
  upstream_id uuid NOT NULL,
  downstream_type text NOT NULL CHECK(btrim(downstream_type)<>''),
  downstream_id uuid NOT NULL,
  downstream_name text NOT NULL DEFAULT '',
  dependency_kind text NOT NULL DEFAULT 'USES',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(tenant_id,upstream_type,upstream_id,downstream_type,downstream_id,dependency_kind)
);

CREATE INDEX metadata_tables_search_idx ON platform.metadata_tables(tenant_id,asset_status,sensitivity_level,visibility);
CREATE INDEX metadata_tables_tags_idx ON platform.metadata_tables USING gin(tags);
CREATE INDEX metadata_columns_tags_idx ON platform.metadata_columns USING gin(tags);
CREATE INDEX asset_dependencies_upstream_idx ON platform.asset_dependencies(tenant_id,upstream_type,upstream_id);

ALTER TABLE platform.asset_dependencies ENABLE ROW LEVEL SECURITY; ALTER TABLE platform.asset_dependencies FORCE ROW LEVEL SECURITY;
CREATE POLICY asset_dependencies_tenant_isolation ON platform.asset_dependencies USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
