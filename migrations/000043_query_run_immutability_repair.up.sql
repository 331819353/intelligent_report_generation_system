-- 修复已执行旧版 000026 的环境：补齐查询审计身份、计划和终态的不可变约束。

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
    OR NEW.metric_id IS DISTINCT FROM OLD.metric_id
    OR NEW.metric_version_id IS DISTINCT FROM OLD.metric_version_id
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

DROP TRIGGER IF EXISTS query_runs_reject_identity_mutation ON platform.query_runs;
CREATE TRIGGER query_runs_reject_identity_mutation
BEFORE UPDATE OR DELETE
ON platform.query_runs
FOR EACH ROW EXECUTE FUNCTION platform.reject_query_run_identity_mutation();
