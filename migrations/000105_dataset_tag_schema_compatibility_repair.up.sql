-- deepseek-v3 的严格 JSON Schema 方言拒绝 uniqueItems。应用层已经保留重复
-- tagId 的本地失败关闭校验，因此移除该供应商不兼容关键字不会放宽结果合同。
-- 旧失败任务继续保留审计，新 prompt 身份登记当前资产。
CREATE OR REPLACE FUNCTION platform.enqueue_dataset_tag_suggestion()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  source_snapshot jsonb;
  request_actor uuid;
BEGIN
  IF NEW.layer IN ('DIM','DWD','DWS','ADS')
     AND NEW.status IN ('DRAFT','PUBLISHED')
     AND (
       TG_OP='INSERT'
       OR OLD.status IS DISTINCT FROM NEW.status
       OR OLD.schema_hash IS DISTINCT FROM NEW.schema_hash
     ) THEN
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

    request_actor := COALESCE(NEW.published_by,NEW.created_by);
    INSERT INTO platform.dataset_tag_suggestion_jobs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,
      source_version_snapshot,source_version_snapshot_hash,layer,
      prompt_version,requested_by
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      source_snapshot,encode(public.digest(source_snapshot::text,'sha256'),'hex'),NEW.layer,
      'dataset-tag-suggestion-v4',request_actor
    )
    ON CONFLICT(
      tenant_id,dataset_version_id,prompt_version,schema_hash
    ) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enqueue_dataset_tag_suggestion() FROM PUBLIC;

DROP TRIGGER IF EXISTS dataset_versions_enqueue_tag_suggestion
  ON platform.dataset_versions;
CREATE TRIGGER dataset_versions_enqueue_tag_suggestion
AFTER INSERT OR UPDATE OF status,schema_hash ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dataset_tag_suggestion();

INSERT INTO platform.dataset_tag_suggestion_jobs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,
  source_version_snapshot,source_version_snapshot_hash,layer,
  prompt_version,requested_by
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  source_facts.snapshot,
  encode(public.digest(source_facts.snapshot::text,'sha256'),'hex'),
  version.layer,'dataset-tag-suggestion-v4',
  COALESCE(version.published_by,version.created_by)
FROM platform.dataset_versions AS version
JOIN platform.datasets AS dataset
  ON dataset.id=version.dataset_id
 AND dataset.tenant_id=version.tenant_id
 AND (
   dataset.current_draft_version_id=version.id
   OR dataset.current_published_version_id=version.id
 )
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
WHERE version.layer IN ('DIM','DWD','DWS','ADS')
  AND version.status IN ('DRAFT','PUBLISHED')
  AND dataset.deleted_at IS NULL
ON CONFLICT(
  tenant_id,dataset_version_id,prompt_version,schema_hash
) DO NOTHING;
