-- 000195 removed metric_id and metric_version_id from query_runs, but the
-- PL/pgSQL immutability trigger still referenced both fields at execution
-- time. Every attempt to close a preview therefore rolled back and left the
-- audit row RUNNING, which also made dataset deletion report DATASET_IN_USE.
CREATE OR REPLACE FUNCTION platform.reject_query_run_identity_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION '查询审计不可删除' USING ERRCODE='23514';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id
    OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.dataset_id IS DISTINCT FROM OLD.dataset_id
    OR NEW.dataset_version_id IS DISTINCT FROM OLD.dataset_version_id
    OR NEW.actor_user_id IS DISTINCT FROM OLD.actor_user_id
    OR NEW.data_source_id IS DISTINCT FROM OLD.data_source_id
    OR NEW.run_type IS DISTINCT FROM OLD.run_type
    OR NEW.plan_hash IS DISTINCT FROM OLD.plan_hash
    OR NEW.parameter_hash IS DISTINCT FROM OLD.parameter_hash
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION '查询审计的执行身份与计划摘要不可修改' USING ERRCODE='23514';
  END IF;
  IF OLD.status<>'RUNNING' OR NEW.status='RUNNING'
    OR NEW.completed_at IS NULL OR NEW.completed_at<OLD.created_at THEN
    RAISE EXCEPTION '查询审计只能从 RUNNING 一次性收口到终态' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.reject_query_run_identity_mutation() FROM PUBLIC;

-- A preview has a hard 25-second execution limit. Rows older than one minute
-- cannot still be legitimate live previews and were stranded by the broken
-- trigger above. Close them before they can block lifecycle operations.
UPDATE platform.query_runs
SET status='FAILED',
    duration_ms=GREATEST(
      duration_ms,
      floor(extract(epoch FROM (now()-created_at))*1000)::bigint
    ),
    error_code='QUERY_AUDIT_RECOVERED',
    completed_at=now()
WHERE status='RUNNING'
  AND created_at<now()-interval '1 minute';

UPDATE platform.query_run_sources AS source
SET status='FAILED'
FROM platform.query_runs AS run
WHERE source.query_run_id=run.id
  AND source.tenant_id=run.tenant_id
  AND source.status='RUNNING'
  AND run.status='FAILED'
  AND run.error_code='QUERY_AUDIT_RECOVERED';
