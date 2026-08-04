-- Dataset tags remain part of dataset configuration. Recreate their narrow
-- access helpers after semantic governance functions are retired.
DELETE FROM platform.asset_tag_bindings
WHERE asset_type NOT IN ('DATASET_VERSION','DATASET_FIELD');

ALTER TABLE platform.asset_tag_bindings
  DROP CONSTRAINT IF EXISTS asset_tag_bindings_subject_shape_check,
  DROP CONSTRAINT IF EXISTS asset_tag_bindings_asset_type_check,
  DROP COLUMN IF EXISTS dimension_id,
  DROP COLUMN IF EXISTS dimension_member_id,
  DROP COLUMN IF EXISTS metric_id,
  DROP COLUMN IF EXISTS metric_version_id,
  DROP COLUMN IF EXISTS metric_dataset_version_id,
  ADD CONSTRAINT asset_tag_bindings_asset_type_check
    CHECK(asset_type IN ('DATASET_VERSION','DATASET_FIELD')),
  ADD CONSTRAINT asset_tag_bindings_subject_shape_check CHECK(
    dataset_id IS NOT NULL AND dataset_version_id IS NOT NULL
    AND (
      (asset_type='DATASET_VERSION' AND dataset_field_id IS NULL)
      OR (asset_type='DATASET_FIELD' AND dataset_field_id IS NOT NULL)
    )
  );

CREATE OR REPLACE FUNCTION platform.semantic_tag_can_read(asset_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.semantic_tags AS asset
    WHERE asset.id=asset_id
      AND asset.tenant_id=platform.current_tenant_id()
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
      AND asset.tenant_id=platform.current_tenant_id()
      AND platform.asset_can_write(asset.domain_id,asset.created_by)
  )
$$;

REVOKE ALL ON FUNCTION platform.semantic_tag_can_read(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.semantic_tag_can_write(uuid) FROM PUBLIC;

DROP POLICY IF EXISTS semantic_tag_aliases_read_scope
  ON platform.semantic_tag_aliases;
DROP POLICY IF EXISTS semantic_tag_aliases_write_scope
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
