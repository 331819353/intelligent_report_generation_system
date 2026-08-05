DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    REVOKE EXECUTE ON FUNCTION platform.semantic_tag_can_read(uuid)
      FROM report_app;
    REVOKE EXECUTE ON FUNCTION platform.semantic_tag_can_write(uuid)
      FROM report_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    REVOKE EXECUTE ON FUNCTION platform.semantic_tag_can_read(uuid)
      FROM report_worker;
    REVOKE EXECUTE ON FUNCTION platform.semantic_tag_can_write(uuid)
      FROM report_worker;
  END IF;
END
$$;
