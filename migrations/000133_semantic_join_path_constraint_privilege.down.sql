DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    REVOKE EXECUTE ON FUNCTION platform.semantic_join_path_is_valid(
      jsonb,text,text
    ) FROM report_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    REVOKE EXECUTE ON FUNCTION platform.semantic_join_path_is_valid(
      jsonb,text,text
    ) FROM report_worker;
  END IF;
END
$$;

COMMENT ON FUNCTION platform.semantic_join_path_is_valid(jsonb,text,text) IS
  '校验有界语义 Join 路径；PUBLIC 无执行权';
