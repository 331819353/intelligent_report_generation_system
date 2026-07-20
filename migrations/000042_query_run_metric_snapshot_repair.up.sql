-- 修复已执行旧版 000026 的环境：历史迁移文件后续补充过该约束，但已登记完成的数据库不会重跑。
-- 查询审计必须冻结指标版本实际绑定的数据集版本，防止写入不一致的指标快照。

CREATE OR REPLACE FUNCTION platform.enforce_query_run_metric_snapshot()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF NEW.metric_version_id IS NULL THEN
    RETURN NEW;
  END IF;
  PERFORM 1
  FROM platform.metric_versions
  WHERE id=NEW.metric_version_id
    AND metric_id=NEW.metric_id
    AND dataset_version_id=NEW.dataset_version_id
    AND tenant_id=NEW.tenant_id
    AND status IN ('DRAFT','PUBLISHED')
  FOR SHARE;
  IF NOT FOUND THEN
    RAISE EXCEPTION '查询审计的指标版本、数据集版本或租户不匹配'
      USING ERRCODE='23503',
        CONSTRAINT='query_runs_metric_version_dataset_tenant_snapshot_check';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_query_run_metric_snapshot() FROM PUBLIC;

DROP TRIGGER IF EXISTS query_runs_enforce_metric_snapshot ON platform.query_runs;
CREATE TRIGGER query_runs_enforce_metric_snapshot
BEFORE INSERT
ON platform.query_runs
FOR EACH ROW EXECUTE FUNCTION platform.enforce_query_run_metric_snapshot();
