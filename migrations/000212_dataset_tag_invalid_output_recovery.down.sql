UPDATE platform.dataset_tag_suggestion_jobs
SET status='SKIPPED',
    error_code='PROMPT_SUPERSEDED',
    error_message='标签结构化输出纠错版本已回滚',
    lease_owner='',lease_token=NULL,lease_expires_at=NULL,
    completed_at=now(),updated_at=now()
WHERE prompt_version='dataset-tag-suggestion-v7'
  AND status IN ('PENDING','RUNNING');

DO $migration$
DECLARE
  definition text;
BEGIN
  SELECT pg_get_functiondef(
    'platform.enqueue_dataset_tag_suggestion()'::regprocedure
  ) INTO definition;
  IF position('dataset-tag-suggestion-v7' IN definition)=0 THEN
    RAISE EXCEPTION 'dataset tag suggestion enqueue prompt is not v7';
  END IF;
  definition := replace(
    definition,
    'dataset-tag-suggestion-v7',
    'dataset-tag-suggestion-v6'
  );
  EXECUTE definition;
END
$migration$;
