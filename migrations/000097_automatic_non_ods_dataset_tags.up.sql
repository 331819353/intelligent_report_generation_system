-- DIM、DWD、DWS、ADS 发布后自动登记 LLM 标签建议。分层建模继续保持
-- 人工触发；标签建议只进入 SUGGESTED 治理态，不自动批准。
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

DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion
  ON platform.dataset_versions;
CREATE TRIGGER dataset_versions_enqueue_tag_suggestion
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion();

-- 为当前发布的非 ODS 版本补齐尚未登记的任务，避免切换期间遗漏资产。
INSERT INTO platform.dataset_tag_suggestion_jobs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,
  source_version_snapshot,source_version_snapshot_hash,layer,
  prompt_version,requested_by
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  source_facts.snapshot,
  encode(public.digest(source_facts.snapshot::text,'sha256'),'hex'),
  version.layer,
  'dataset-tag-suggestion-v1',version.published_by
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
 ON dataset.id=version.dataset_id
 AND dataset.tenant_id=version.tenant_id
 AND dataset.current_published_version_id=version.id
CROSS JOIN LATERAL (
  SELECT COALESCE(
    jsonb_agg(
      jsonb_build_object(
        'dataSourceId',source_fact.data_source_id,
        'dataSourceVersionId',source_fact.data_source_version_id
      )
      ORDER BY source_fact.data_source_id
    ),
    '[]'::jsonb
  ) AS snapshot
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
    WHERE dependency.tenant_id=version.tenant_id
      AND dependency.dataset_version_id=version.id
  ) AS source_fact
) AS source_facts
WHERE version.status='PUBLISHED'
  AND version.layer IN ('DIM','DWD','DWS','ADS')
  AND dataset.status='PUBLISHED'
  AND dataset.deleted_at IS NULL
ON CONFLICT(tenant_id,dataset_version_id,prompt_version) DO NOTHING;

COMMENT ON TABLE platform.dataset_tag_suggestion_jobs IS
  '非 ODS 数据集发布时自动登记的精确版本标签建议 outbox；带租约、fencing 和有界重试';
