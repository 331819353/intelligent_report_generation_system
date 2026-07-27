DROP TRIGGER IF EXISTS datasets_resume_fact_modeling_after_dim_publication
  ON platform.datasets;
DROP FUNCTION IF EXISTS
  platform.resume_fact_modeling_after_dim_publication();

-- 回滚只恢复 v4 任务登记身份；已有不可变 v5 任务和建议保留审计。
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
          'dataSourceId',source.id::text,
          'dataSourceVersionId',
          COALESCE(source.current_published_version_id::text,'')
        )
        ORDER BY source.id::text
      ),
      '[]'::jsonb
    )
    INTO source_snapshot
    FROM platform.dataset_dependencies AS dependency
    JOIN platform.metadata_tables AS source_table
      ON dependency.source_type='TABLE'
     AND source_table.id::text=dependency.source_id
     AND source_table.tenant_id=dependency.tenant_id
    JOIN platform.data_sources AS source
      ON source.id=source_table.data_source_id
     AND source.tenant_id=source_table.tenant_id
    WHERE dependency.tenant_id=NEW.tenant_id
      AND dependency.dataset_version_id=NEW.id;

    request_actor := COALESCE(NEW.published_by,NEW.created_by);
    INSERT INTO platform.dataset_tag_suggestion_jobs(
      tenant_id,dataset_id,dataset_version_id,schema_hash,
      source_version_snapshot,source_version_snapshot_hash,layer,
      prompt_version,requested_by
    ) VALUES(
      NEW.tenant_id,NEW.dataset_id,NEW.id,NEW.schema_hash,
      source_snapshot,
      encode(public.digest(source_snapshot::text,'sha256'),'hex'),
      NEW.layer,'dataset-tag-suggestion-v4',request_actor
    )
    ON CONFLICT(
      tenant_id,dataset_version_id,prompt_version,schema_hash
    ) DO NOTHING;
  END IF;
  RETURN NEW;
END
$$;
