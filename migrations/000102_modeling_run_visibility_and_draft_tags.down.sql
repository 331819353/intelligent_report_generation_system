DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion
  ON platform.dataset_versions;

CREATE OR REPLACE FUNCTION platform.enqueue_dataset_tag_suggestion()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  source_snapshot jsonb;
BEGIN
  IF NEW.layer IN ('DIM','DWD','DWS','ADS')
     AND NEW.status='PUBLISHED'
     AND OLD.status IS DISTINCT FROM 'PUBLISHED' THEN
    SELECT COALESCE(
      jsonb_agg(
        jsonb_build_object(
          'dataSourceId',source_fact.data_source_id,
          'dataSourceVersionId',source_fact.data_source_version_id
        )
        ORDER BY source_fact.data_source_id
      ),
      '[]'::jsonb
    )
    INTO source_snapshot
    FROM (
      SELECT DISTINCT
        source.id::text AS data_source_id,
        COALESCE(source.current_published_version_id::text,'') AS data_source_version_id
      FROM platform.dataset_dependencies AS dependency
      JOIN platform.metadata_tables AS source_table
        ON dependency.source_type='TABLE'
       AND source_table.id::text=dependency.source_id
       AND source_table.tenant_id=dependency.tenant_id
      JOIN platform.data_sources AS source
        ON source.id=source_table.data_source_id
       AND source.tenant_id=source_table.tenant_id
      WHERE dependency.tenant_id=NEW.tenant_id
        AND dependency.dataset_version_id=NEW.id
    ) AS source_fact;

    INSERT INTO platform.dataset_tag_suggestion_jobs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,
      source_version_snapshot,source_version_snapshot_hash,layer,
      prompt_version,requested_by
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      source_snapshot,encode(public.digest(source_snapshot::text,'sha256'),'hex'),NEW.layer,
      'dataset-tag-suggestion-v1',NEW.published_by
    )
    ON CONFLICT(tenant_id,dataset_version_id,prompt_version) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_tag_suggestion() FROM PUBLIC;

CREATE TRIGGER dataset_versions_enqueue_tag_suggestion
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion();

-- 已被标签绑定引用的系统词表保留以避免破坏治理审计；只清理从未使用的条目。
DELETE FROM platform.semantic_tags AS tag
WHERE tag.code::text LIKE 'system.%'
  AND NOT EXISTS(
    SELECT 1 FROM platform.asset_tag_bindings AS binding
    WHERE binding.tenant_id=tag.tenant_id AND binding.tag_id=tag.id
  )
  AND NOT EXISTS(
    SELECT 1 FROM platform.semantic_tag_aliases AS alias
    WHERE alias.tenant_id=tag.tenant_id AND alias.tag_id=tag.id
  );

ALTER TABLE platform.dws_modeling_jobs DROP COLUMN requested_at;
ALTER TABLE platform.dwd_modeling_jobs DROP COLUMN requested_at;

ALTER TABLE platform.dataset_tag_suggestion_jobs
  DROP CONSTRAINT dataset_tag_suggestion_jobs_idempotency_key;
ALTER TABLE platform.dataset_tag_suggestion_jobs
  ADD CONSTRAINT dataset_tag_suggestion_jobs_idempotency_key
  UNIQUE(tenant_id,dataset_version_id,prompt_version);
