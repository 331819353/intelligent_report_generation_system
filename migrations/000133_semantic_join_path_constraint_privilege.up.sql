-- dimension_metric_compatibility 的 CHECK 约束会在应用角色更新关系时调用
-- semantic_join_path_is_valid。000086 从 PUBLIC 撤销了函数执行权，却没有把
-- 最小权限补给实际写入角色，导致合法的 verify/reject 操作以 42501 失败。

DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    GRANT EXECUTE ON FUNCTION platform.semantic_join_path_is_valid(
      jsonb,text,text
    ) TO report_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    GRANT EXECUTE ON FUNCTION platform.semantic_join_path_is_valid(
      jsonb,text,text
    ) TO report_worker;
  END IF;
END
$$;

COMMENT ON FUNCTION platform.semantic_join_path_is_valid(jsonb,text,text) IS
  '校验有界语义 Join 路径；仅授予需要写入兼容关系的应用与 Worker 角色执行权';
