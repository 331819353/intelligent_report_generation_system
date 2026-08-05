-- Dataset tag alias RLS policies invoke these SECURITY DEFINER helpers while
-- requests run as the dedicated API and worker roles. Migration 000196
-- revoked PUBLIC without restoring the narrow runtime grants, which made
-- taxonomy reads fail before tag suggestions could be generated.
DO $$
BEGIN
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_app') THEN
    GRANT EXECUTE ON FUNCTION platform.semantic_tag_can_read(uuid)
      TO report_app;
    GRANT EXECUTE ON FUNCTION platform.semantic_tag_can_write(uuid)
      TO report_app;
  END IF;
  IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='report_worker') THEN
    GRANT EXECUTE ON FUNCTION platform.semantic_tag_can_read(uuid)
      TO report_worker;
    GRANT EXECUTE ON FUNCTION platform.semantic_tag_can_write(uuid)
      TO report_worker;
  END IF;
END
$$;
