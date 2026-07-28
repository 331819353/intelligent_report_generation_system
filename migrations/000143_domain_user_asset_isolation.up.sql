BEGIN;

CREATE TYPE platform.asset_share_scope AS ENUM ('PRIVATE','DOMAIN','PLATFORM');

CREATE TABLE platform.domain_memberships(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  domain_id uuid NOT NULL,
  user_id uuid NOT NULL,
  status platform.role_status NOT NULL DEFAULT 'ACTIVE',
  assigned_by uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,domain_id,user_id),
  FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(assigned_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (assigned_by)
);

CREATE INDEX domain_memberships_user_status_idx
  ON platform.domain_memberships(tenant_id,user_id,status,domain_id);
CREATE TRIGGER domain_memberships_set_updated_at
BEFORE UPDATE ON platform.domain_memberships
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.domain_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.domain_memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY domain_memberships_tenant_isolation
  ON platform.domain_memberships
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- Existing users start in the tenant's default domain. Additional membership
-- must be explicit; creating a domain only grants its creator membership.
INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id)
SELECT domain.tenant_id,domain.id,user_account.id
FROM platform.business_domains AS domain
JOIN platform.users AS user_account
  ON user_account.tenant_id=domain.tenant_id
 AND user_account.deleted_at IS NULL
WHERE domain.is_default
  AND domain.status='ACTIVE'
  AND domain.deleted_at IS NULL
ON CONFLICT DO NOTHING;

-- Tenant/platform administrators can manage every existing domain.
INSERT INTO platform.domain_memberships(
  tenant_id,domain_id,user_id,assigned_by
)
SELECT DISTINCT domain.tenant_id,domain.id,assignment.user_id,assignment.user_id
FROM platform.business_domains AS domain
JOIN platform.user_roles AS assignment
  ON assignment.tenant_id=domain.tenant_id
JOIN platform.roles AS role
  ON role.id=assignment.role_id
 AND role.tenant_id=assignment.tenant_id
WHERE role.code::text IN ('platform_admin','tenant_admin')
  AND role.status='ACTIVE'
  AND role.deleted_at IS NULL
  AND domain.status='ACTIVE'
  AND domain.deleted_at IS NULL
ON CONFLICT DO NOTHING;

CREATE OR REPLACE FUNCTION platform.current_user_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
  SELECT NULLIF(current_setting('app.user_id',true),'')::uuid
$$;

CREATE OR REPLACE FUNCTION platform.current_domain_id()
RETURNS uuid
LANGUAGE sql
STABLE
AS $$
  SELECT NULLIF(current_setting('app.domain_id',true),'')::uuid
$$;

CREATE OR REPLACE FUNCTION platform.is_system_access()
RETURNS boolean
LANGUAGE sql
STABLE
AS $$
  SELECT COALESCE(NULLIF(current_setting('app.access_mode',true),''),'SYSTEM')='SYSTEM'
$$;

CREATE OR REPLACE FUNCTION platform.current_or_default_domain_id()
RETURNS uuid
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT COALESCE(
    platform.current_domain_id(),
    (
      SELECT domain.id
      FROM platform.business_domains AS domain
      WHERE domain.tenant_id=platform.current_tenant_id()
        AND domain.is_default
        AND domain.status='ACTIVE'
        AND domain.deleted_at IS NULL
      LIMIT 1
    )
  )
$$;

CREATE OR REPLACE FUNCTION platform.default_asset_share_scope()
RETURNS platform.asset_share_scope
LANGUAGE sql
STABLE
AS $$
  SELECT 'PRIVATE'::platform.asset_share_scope
$$;

CREATE OR REPLACE FUNCTION platform.user_has_active_domain_membership(
  target_domain_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.domain_memberships AS membership
    JOIN platform.business_domains AS domain
      ON domain.id=membership.domain_id
     AND domain.tenant_id=membership.tenant_id
    WHERE membership.tenant_id=platform.current_tenant_id()
      AND membership.user_id=platform.current_user_id()
      AND membership.domain_id=target_domain_id
      AND membership.status='ACTIVE'
      AND domain.status='ACTIVE'
      AND domain.deleted_at IS NULL
  )
$$;

CREATE OR REPLACE FUNCTION platform.user_is_asset_administrator()
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.user_roles AS assignment
    JOIN platform.roles AS role
      ON role.id=assignment.role_id
     AND role.tenant_id=assignment.tenant_id
    WHERE assignment.tenant_id=platform.current_tenant_id()
      AND assignment.user_id=platform.current_user_id()
      AND role.code::text IN ('platform_admin','tenant_admin')
      AND role.status='ACTIVE'
      AND role.deleted_at IS NULL
  )
$$;

CREATE OR REPLACE FUNCTION platform.asset_can_read(
  asset_domain_id uuid,
  asset_owner_user_id uuid,
  asset_scope platform.asset_share_scope
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      platform.current_user_id() IS NOT NULL
      AND platform.current_domain_id() IS NOT NULL
      AND platform.user_has_active_domain_membership(platform.current_domain_id())
      AND (
        asset_scope='PLATFORM'
        OR (
          asset_domain_id=platform.current_domain_id()
          AND (
            asset_scope='DOMAIN'
            OR asset_owner_user_id=platform.current_user_id()
            OR platform.user_is_asset_administrator()
          )
        )
      )
    )
$$;

CREATE OR REPLACE FUNCTION platform.asset_can_write(
  asset_domain_id uuid,
  asset_owner_user_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      asset_domain_id=platform.current_domain_id()
      AND platform.user_has_active_domain_membership(asset_domain_id)
      AND (
        asset_owner_user_id=platform.current_user_id()
        OR platform.user_is_asset_administrator()
      )
    )
$$;

ALTER TABLE platform.data_sources
  ADD COLUMN domain_id uuid,
  ADD COLUMN sharing_scope platform.asset_share_scope;
ALTER TABLE platform.datasets
  ADD COLUMN domain_id uuid,
  ADD COLUMN sharing_scope platform.asset_share_scope;
ALTER TABLE platform.metrics
  ADD COLUMN domain_id uuid,
  ADD COLUMN sharing_scope platform.asset_share_scope;
ALTER TABLE platform.semantic_tags
  ADD COLUMN domain_id uuid,
  ADD COLUMN sharing_scope platform.asset_share_scope;
ALTER TABLE platform.semantic_dimensions
  ADD COLUMN domain_id uuid,
  ADD COLUMN sharing_scope platform.asset_share_scope;
ALTER TABLE platform.semantic_term_assets
  ADD COLUMN domain_id uuid,
  ADD COLUMN sharing_scope platform.asset_share_scope;
ALTER TABLE platform.dimension_where_decisions
  ADD COLUMN domain_id uuid,
  ADD COLUMN sharing_scope platform.asset_share_scope;

-- Legacy tenant-public data remains tenant-wide after upgrade. Other legacy
-- assets are conservatively assigned to the default domain and their creator.
UPDATE platform.data_sources AS asset
SET domain_id=domain.id,
    sharing_scope=CASE
      WHEN asset.visibility='TENANT_PUBLIC'
        THEN 'PLATFORM'::platform.asset_share_scope
      ELSE 'PRIVATE'::platform.asset_share_scope
    END
FROM platform.business_domains AS domain
WHERE domain.tenant_id=asset.tenant_id AND domain.is_default;

UPDATE platform.datasets AS asset
SET domain_id=domain.id,
    sharing_scope='PRIVATE'
FROM platform.business_domains AS domain
WHERE domain.tenant_id=asset.tenant_id AND domain.is_default;

UPDATE platform.metrics AS asset
SET domain_id=dataset.domain_id,
    sharing_scope='PRIVATE'
FROM platform.datasets AS dataset
WHERE dataset.tenant_id=asset.tenant_id AND dataset.id=asset.dataset_id;

UPDATE platform.semantic_tags AS asset
SET domain_id=domain.id,
    sharing_scope='PRIVATE'
FROM platform.business_domains AS domain
WHERE domain.tenant_id=asset.tenant_id AND domain.is_default;

UPDATE platform.semantic_dimensions AS asset
SET domain_id=dataset.domain_id,
    sharing_scope='PRIVATE'
FROM platform.datasets AS dataset
WHERE dataset.tenant_id=asset.tenant_id AND dataset.id=asset.dataset_id;

UPDATE platform.semantic_term_assets AS asset
SET domain_id=domain.id,
    sharing_scope='PRIVATE'
FROM platform.business_domains AS domain
WHERE domain.tenant_id=asset.tenant_id AND domain.is_default;

UPDATE platform.dimension_where_decisions AS asset
SET domain_id=dimension.domain_id,
    sharing_scope=dimension.sharing_scope
FROM platform.semantic_dimensions AS dimension
WHERE dimension.tenant_id=asset.tenant_id AND dimension.id=asset.dimension_id;

-- Several existing dataset/metric consistency constraints are deferred until
-- commit. Flush them before altering the same tables further.
SET CONSTRAINTS ALL IMMEDIATE;

ALTER TABLE platform.data_sources
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ALTER COLUMN sharing_scope SET DEFAULT platform.default_asset_share_scope(),
  ALTER COLUMN sharing_scope SET NOT NULL,
  ADD CONSTRAINT data_sources_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
ALTER TABLE platform.datasets
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ALTER COLUMN sharing_scope SET DEFAULT platform.default_asset_share_scope(),
  ALTER COLUMN sharing_scope SET NOT NULL,
  ADD CONSTRAINT datasets_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
ALTER TABLE platform.metrics
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ALTER COLUMN sharing_scope SET DEFAULT platform.default_asset_share_scope(),
  ALTER COLUMN sharing_scope SET NOT NULL,
  ADD CONSTRAINT metrics_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
ALTER TABLE platform.semantic_tags
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ALTER COLUMN sharing_scope SET DEFAULT platform.default_asset_share_scope(),
  ALTER COLUMN sharing_scope SET NOT NULL,
  ADD CONSTRAINT semantic_tags_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
ALTER TABLE platform.semantic_dimensions
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ALTER COLUMN sharing_scope SET DEFAULT platform.default_asset_share_scope(),
  ALTER COLUMN sharing_scope SET NOT NULL,
  ADD CONSTRAINT semantic_dimensions_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
ALTER TABLE platform.semantic_term_assets
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ALTER COLUMN sharing_scope SET DEFAULT platform.default_asset_share_scope(),
  ALTER COLUMN sharing_scope SET NOT NULL,
  ADD CONSTRAINT semantic_term_assets_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
ALTER TABLE platform.dimension_where_decisions
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ALTER COLUMN sharing_scope SET DEFAULT platform.default_asset_share_scope(),
  ALTER COLUMN sharing_scope SET NOT NULL,
  ADD CONSTRAINT dimension_where_decisions_domain_fk
    FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);

CREATE INDEX data_sources_domain_scope_idx
  ON platform.data_sources(tenant_id,domain_id,sharing_scope,updated_at DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX datasets_domain_scope_idx
  ON platform.datasets(tenant_id,domain_id,sharing_scope,updated_at DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX metrics_domain_scope_idx
  ON platform.metrics(tenant_id,domain_id,sharing_scope,updated_at DESC)
  WHERE deleted_at IS NULL;
CREATE INDEX semantic_dimensions_domain_scope_idx
  ON platform.semantic_dimensions(tenant_id,domain_id,sharing_scope,updated_at DESC);
CREATE INDEX semantic_term_assets_domain_scope_idx
  ON platform.semantic_term_assets(tenant_id,domain_id,sharing_scope,updated_at DESC);

-- Metrics and dimensions cannot be moved to a different domain from their
-- dataset or shared more broadly than that dataset.
CREATE OR REPLACE FUNCTION platform.enforce_derived_asset_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  parent_domain_id uuid;
  parent_scope platform.asset_share_scope;
  parent_rank integer;
  requested_rank integer;
BEGIN
  SELECT dataset.domain_id,dataset.sharing_scope
  INTO parent_domain_id,parent_scope
  FROM platform.datasets AS dataset
  WHERE dataset.tenant_id=NEW.tenant_id AND dataset.id=NEW.dataset_id;

  IF NOT FOUND THEN
    RAISE EXCEPTION 'parent dataset is not available'
      USING ERRCODE='23503';
  END IF;

  IF TG_OP='INSERT' THEN
    NEW.domain_id=parent_domain_id;
  ELSIF NEW.domain_id<>parent_domain_id THEN
    RAISE EXCEPTION 'derived asset must stay in its dataset domain'
      USING ERRCODE='23514';
  END IF;

  parent_rank=CASE parent_scope WHEN 'PRIVATE' THEN 0 WHEN 'DOMAIN' THEN 1 ELSE 2 END;
  requested_rank=CASE NEW.sharing_scope WHEN 'PRIVATE' THEN 0 WHEN 'DOMAIN' THEN 1 ELSE 2 END;
  IF requested_rank>parent_rank THEN
    RAISE EXCEPTION 'derived asset cannot be shared more broadly than its dataset'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER metrics_enforce_derived_scope
BEFORE INSERT OR UPDATE OF domain_id,sharing_scope,dataset_id
ON platform.metrics
FOR EACH ROW EXECUTE FUNCTION platform.enforce_derived_asset_scope();
CREATE TRIGGER semantic_dimensions_enforce_derived_scope
BEFORE INSERT OR UPDATE OF domain_id,sharing_scope,dataset_id
ON platform.semantic_dimensions
FOR EACH ROW EXECUTE FUNCTION platform.enforce_derived_asset_scope();

CREATE OR REPLACE FUNCTION platform.inherit_dimension_decision_scope()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  SELECT dimension.domain_id,dimension.sharing_scope
  INTO NEW.domain_id,NEW.sharing_scope
  FROM platform.semantic_dimensions AS dimension
  WHERE dimension.tenant_id=NEW.tenant_id AND dimension.id=NEW.dimension_id;
  IF NOT FOUND THEN
    RAISE EXCEPTION 'dimension is not available'
      USING ERRCODE='23503';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER dimension_where_decisions_inherit_scope
BEFORE INSERT OR UPDATE OF dimension_id
ON platform.dimension_where_decisions
FOR EACH ROW EXECUTE FUNCTION platform.inherit_dimension_decision_scope();

-- Root asset policies are the security boundary. PRIVATE is owner-only,
-- DOMAIN is readable by members of the owning domain, and PLATFORM is
-- readable from every domain membership in the tenant.
DROP POLICY data_sources_tenant_isolation ON platform.data_sources;
CREATE POLICY data_sources_read_scope ON platform.data_sources FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_read(domain_id,owner_user_id,sharing_scope)
  );
CREATE POLICY data_sources_insert_scope ON platform.data_sources FOR INSERT
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,owner_user_id)
  );
CREATE POLICY data_sources_update_scope ON platform.data_sources FOR UPDATE
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,owner_user_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,owner_user_id)
  );
CREATE POLICY data_sources_delete_scope ON platform.data_sources FOR DELETE
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,owner_user_id)
  );

DROP POLICY datasets_tenant_isolation ON platform.datasets;
CREATE POLICY datasets_read_scope ON platform.datasets FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_read(domain_id,created_by,sharing_scope)
  );
CREATE POLICY datasets_insert_scope ON platform.datasets FOR INSERT
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  );
CREATE POLICY datasets_update_scope ON platform.datasets FOR UPDATE
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  );
CREATE POLICY datasets_delete_scope ON platform.datasets FOR DELETE
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  );

DROP POLICY metrics_tenant_isolation ON platform.metrics;
CREATE POLICY metrics_read_scope ON platform.metrics FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_read(domain_id,created_by,sharing_scope)
  );
CREATE POLICY metrics_write_scope ON platform.metrics FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  );

DROP POLICY semantic_tags_tenant_isolation ON platform.semantic_tags;
CREATE POLICY semantic_tags_read_scope ON platform.semantic_tags FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_read(domain_id,created_by,sharing_scope)
  );
CREATE POLICY semantic_tags_write_scope ON platform.semantic_tags FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  );

DROP POLICY semantic_dimensions_tenant_isolation ON platform.semantic_dimensions;
CREATE POLICY semantic_dimensions_read_scope ON platform.semantic_dimensions FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_read(domain_id,created_by,sharing_scope)
  );
CREATE POLICY semantic_dimensions_write_scope ON platform.semantic_dimensions FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  );

DROP POLICY semantic_term_assets_tenant_isolation ON platform.semantic_term_assets;
CREATE POLICY semantic_term_assets_read_scope ON platform.semantic_term_assets FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_read(domain_id,created_by,sharing_scope)
  );
CREATE POLICY semantic_term_assets_write_scope ON platform.semantic_term_assets FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.asset_can_write(domain_id,created_by)
  );

DROP POLICY dimension_where_decisions_tenant_isolation
  ON platform.dimension_where_decisions;
CREATE POLICY dimension_where_decisions_read_scope
  ON platform.dimension_where_decisions FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND EXISTS(
      SELECT 1 FROM platform.semantic_dimensions AS dimension
      WHERE dimension.id=dimension_where_decisions.dimension_id
    )
  );
CREATE POLICY dimension_where_decisions_write_scope
  ON platform.dimension_where_decisions FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND EXISTS(
      SELECT 1 FROM platform.semantic_dimensions AS dimension
      WHERE dimension.id=dimension_where_decisions.dimension_id
        AND platform.asset_can_write(dimension.domain_id,dimension.created_by)
    )
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND EXISTS(
      SELECT 1 FROM platform.semantic_dimensions AS dimension
      WHERE dimension.id=dimension_where_decisions.dimension_id
        AND platform.asset_can_write(dimension.domain_id,dimension.created_by)
    )
  );

-- Child records (versions, fields, members, metadata and materializations)
-- inherit the root asset boundary so guessed UUIDs cannot bypass isolation.
CREATE OR REPLACE FUNCTION platform.data_source_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.data_sources AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_read(
        asset.domain_id,asset.owner_user_id,asset.sharing_scope
      )
  )
$$;
CREATE OR REPLACE FUNCTION platform.data_source_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.data_sources AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_write(asset.domain_id,asset.owner_user_id)
  )
$$;
CREATE OR REPLACE FUNCTION platform.metadata_table_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.metadata_tables AS metadata_table
    WHERE metadata_table.id=asset_id
      AND platform.data_source_can_read(metadata_table.data_source_id)
  )
$$;
CREATE OR REPLACE FUNCTION platform.metadata_table_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.metadata_tables AS metadata_table
    WHERE metadata_table.id=asset_id
      AND platform.data_source_can_write(metadata_table.data_source_id)
  )
$$;
CREATE OR REPLACE FUNCTION platform.dataset_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.datasets AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_read(
        asset.domain_id,asset.created_by,asset.sharing_scope
      )
  )
$$;
CREATE OR REPLACE FUNCTION platform.dataset_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.datasets AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_write(asset.domain_id,asset.created_by)
  )
$$;
CREATE OR REPLACE FUNCTION platform.dataset_version_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.dataset_versions AS version
    WHERE version.id=asset_id
      AND platform.dataset_can_read(version.dataset_id)
  )
$$;
CREATE OR REPLACE FUNCTION platform.dataset_version_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.dataset_versions AS version
    WHERE version.id=asset_id
      AND platform.dataset_can_write(version.dataset_id)
  )
$$;
CREATE OR REPLACE FUNCTION platform.metric_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.metrics AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_read(
        asset.domain_id,asset.created_by,asset.sharing_scope
      )
  )
$$;
CREATE OR REPLACE FUNCTION platform.metric_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.metrics AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_write(asset.domain_id,asset.created_by)
  )
$$;
CREATE OR REPLACE FUNCTION platform.metric_version_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.metric_versions AS version
    WHERE version.id=asset_id
      AND platform.metric_can_read(version.metric_id)
  )
$$;
CREATE OR REPLACE FUNCTION platform.metric_version_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.metric_versions AS version
    WHERE version.id=asset_id
      AND platform.metric_can_write(version.metric_id)
  )
$$;
CREATE OR REPLACE FUNCTION platform.dimension_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.semantic_dimensions AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_read(
        asset.domain_id,asset.created_by,asset.sharing_scope
      )
  )
$$;
CREATE OR REPLACE FUNCTION platform.dimension_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.semantic_dimensions AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_write(asset.domain_id,asset.created_by)
  )
$$;
CREATE OR REPLACE FUNCTION platform.semantic_tag_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.semantic_tags AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_read(
        asset.domain_id,asset.created_by,asset.sharing_scope
      )
  )
$$;
CREATE OR REPLACE FUNCTION platform.semantic_tag_can_write(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.semantic_tags AS asset
    WHERE asset.id=asset_id
      AND platform.asset_can_write(asset.domain_id,asset.created_by)
  )
$$;

DROP POLICY IF EXISTS data_source_versions_tenant_isolation
  ON platform.data_source_versions;
CREATE POLICY data_source_versions_read_scope
  ON platform.data_source_versions FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.data_source_can_read(data_source_id)
  );
CREATE POLICY data_source_versions_write_scope
  ON platform.data_source_versions FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.data_source_can_write(data_source_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.data_source_can_write(data_source_id)
  );

DROP POLICY metadata_tables_tenant_isolation ON platform.metadata_tables;
CREATE POLICY metadata_tables_read_scope ON platform.metadata_tables FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.data_source_can_read(data_source_id)
  );
CREATE POLICY metadata_tables_write_scope ON platform.metadata_tables FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.data_source_can_write(data_source_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.data_source_can_write(data_source_id)
  );

DROP POLICY metadata_columns_tenant_isolation ON platform.metadata_columns;
CREATE POLICY metadata_columns_read_scope ON platform.metadata_columns FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metadata_table_can_read(table_id)
  );
CREATE POLICY metadata_columns_write_scope ON platform.metadata_columns FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metadata_table_can_write(table_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.metadata_table_can_write(table_id)
  );

DROP POLICY dataset_versions_tenant_isolation ON platform.dataset_versions;
CREATE POLICY dataset_versions_read_scope ON platform.dataset_versions FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_can_read(dataset_id)
  );
CREATE POLICY dataset_versions_write_scope ON platform.dataset_versions FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_can_write(dataset_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_can_write(dataset_id)
  );

DROP POLICY dataset_fields_tenant_isolation ON platform.dataset_fields;
CREATE POLICY dataset_fields_read_scope ON platform.dataset_fields FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_read(dataset_version_id)
  );
CREATE POLICY dataset_fields_write_scope ON platform.dataset_fields FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_write(dataset_version_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_write(dataset_version_id)
  );

DROP POLICY dataset_parameters_tenant_isolation ON platform.dataset_parameters;
CREATE POLICY dataset_parameters_read_scope ON platform.dataset_parameters FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_read(dataset_version_id)
  );
CREATE POLICY dataset_parameters_write_scope ON platform.dataset_parameters FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_write(dataset_version_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_write(dataset_version_id)
  );

DROP POLICY dataset_dependencies_tenant_isolation ON platform.dataset_dependencies;
CREATE POLICY dataset_dependencies_read_scope ON platform.dataset_dependencies FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_read(dataset_version_id)
  );
CREATE POLICY dataset_dependencies_write_scope ON platform.dataset_dependencies FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_write(dataset_version_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_version_can_write(dataset_version_id)
  );

DROP POLICY IF EXISTS dataset_draft_revisions_tenant_isolation
  ON platform.dataset_draft_revisions;
CREATE POLICY dataset_draft_revisions_read_scope
  ON platform.dataset_draft_revisions FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_can_read(dataset_id)
  );
CREATE POLICY dataset_draft_revisions_write_scope
  ON platform.dataset_draft_revisions FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_can_write(dataset_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dataset_can_write(dataset_id)
  );

DROP POLICY metric_versions_tenant_isolation ON platform.metric_versions;
CREATE POLICY metric_versions_read_scope ON platform.metric_versions FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_can_read(metric_id)
  );
CREATE POLICY metric_versions_write_scope ON platform.metric_versions FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_can_write(metric_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_can_write(metric_id)
  );

DROP POLICY metric_dimensions_tenant_isolation ON platform.metric_dimensions;
CREATE POLICY metric_dimensions_read_scope ON platform.metric_dimensions FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_version_can_read(metric_version_id)
  );
CREATE POLICY metric_dimensions_write_scope ON platform.metric_dimensions FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_version_can_write(metric_version_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_version_can_write(metric_version_id)
  );

DROP POLICY metric_dependencies_tenant_isolation ON platform.metric_dependencies;
CREATE POLICY metric_dependencies_read_scope ON platform.metric_dependencies FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_version_can_read(metric_version_id)
  );
CREATE POLICY metric_dependencies_write_scope ON platform.metric_dependencies FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_version_can_write(metric_version_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.metric_version_can_write(metric_version_id)
  );

DROP POLICY semantic_tag_aliases_tenant_isolation
  ON platform.semantic_tag_aliases;
CREATE POLICY semantic_tag_aliases_read_scope
  ON platform.semantic_tag_aliases FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.semantic_tag_can_read(tag_id)
  );
CREATE POLICY semantic_tag_aliases_write_scope
  ON platform.semantic_tag_aliases FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.semantic_tag_can_write(tag_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.semantic_tag_can_write(tag_id)
  );

DROP POLICY dimension_members_tenant_isolation ON platform.dimension_members;
CREATE POLICY dimension_members_read_scope
  ON platform.dimension_members FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_read(dimension_id)
  );
CREATE POLICY dimension_members_write_scope
  ON platform.dimension_members FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_write(dimension_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_write(dimension_id)
  );

DROP POLICY dimension_member_aliases_tenant_isolation
  ON platform.dimension_member_aliases;
CREATE POLICY dimension_member_aliases_read_scope
  ON platform.dimension_member_aliases FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_read(dimension_id)
  );
CREATE POLICY dimension_member_aliases_write_scope
  ON platform.dimension_member_aliases FOR ALL
  USING(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_write(dimension_id)
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND platform.dimension_can_write(dimension_id)
  );

COMMENT ON TABLE platform.domain_memberships IS
  'Explicit user-to-domain membership; domain switching and all user-scoped RLS depend on this table';
COMMENT ON TYPE platform.asset_share_scope IS
  'PRIVATE=owner only; DOMAIN=owning domain members; PLATFORM=all tenant domain members';
COMMENT ON COLUMN platform.datasets.domain_id IS
  'Owning business domain; cross-domain access requires PLATFORM sharing';

COMMIT;
