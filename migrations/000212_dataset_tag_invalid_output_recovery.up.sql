-- MiniMax-compatible endpoints can occasionally return a response that does
-- not satisfy the strict tag JSON schema. Prompt v7 adds bounded in-process
-- repair and job-level retry. Terminal v6 audit rows remain immutable; current
-- versions with an unsuccessful v6 job receive one new v7 outbox identity.
UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',
    error_code='PROMPT_SUPERSEDED',
    error_message='标签结构化输出已切换到可纠错版本',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE prompt_version='dataset-tag-suggestion-v6'
  AND status IN ('PENDING','RUNNING');

DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enqueue_dataset_tag_suggestion()'::regprocedure
  ) INTO definition;
  IF position('dataset-tag-suggestion-v6' IN definition)=0 THEN
    RAISE EXCEPTION 'dataset tag suggestion enqueue prompt is not v6';
  END IF;
  definition := replace(
    definition,
    'dataset-tag-suggestion-v6',
    'dataset-tag-suggestion-v7'
  );
  EXECUTE definition;
END
$migration$;

INSERT INTO platform.dataset_tag_suggestion_jobs(
  tenant_id,dataset_id,dataset_version_id,schema_hash,
  source_version_snapshot,source_version_snapshot_hash,layer,
  prompt_version,requested_by
)
SELECT
  version.tenant_id,version.dataset_id,version.id,version.schema_hash,
  source_facts.snapshot,
  encode(public.digest(source_facts.snapshot::text,'sha256'),'hex'),
  version.layer,'dataset-tag-suggestion-v7',
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
      COALESCE(source.current_published_version_id::text,'')
        AS data_source_version_id
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
  AND EXISTS(
    SELECT 1
    FROM platform.dataset_tag_suggestion_jobs AS previous
    WHERE previous.tenant_id=version.tenant_id
      AND previous.dataset_version_id=version.id
      AND previous.schema_hash=version.schema_hash
      AND previous.prompt_version='dataset-tag-suggestion-v6'
      AND previous.status IN ('FAILED','SKIPPED')
  )
ON CONFLICT(
  tenant_id,dataset_version_id,prompt_version,schema_hash
) DO NOTHING;
