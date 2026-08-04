-- Rolling back keeps the staged codes as generic safe failures. Reinstall the
-- previous function body from migration 000197 when a full historical rollback
-- is required; application deployments only migrate forward.
COMMENT ON FUNCTION platform.fail_data_source_connection_test(uuid,uuid,text,boolean) IS
  'Stores safe connection-test diagnostics';
