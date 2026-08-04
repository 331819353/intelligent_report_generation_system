ALTER TABLE platform.data_source_connection_test_jobs
  ADD COLUMN stage text NOT NULL DEFAULT 'QUEUED'
  CHECK(stage IN ('QUEUED','ADDRESS','PORT','DATABASE','AUTHENTICATION'));

CREATE OR REPLACE FUNCTION platform.update_data_source_connection_test_stage(
  p_job_id uuid,
  p_lease_token uuid,
  p_stage text
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE
  v_tenant_id uuid := platform.current_tenant_id();
  v_stage text := upper(btrim(COALESCE(p_stage,'')));
  v_updated boolean := false;
BEGIN
  IF v_tenant_id IS NULL
    OR v_stage NOT IN ('ADDRESS','PORT','DATABASE','AUTHENTICATION') THEN
    RETURN false;
  END IF;

  UPDATE platform.data_source_connection_test_jobs
  SET stage=v_stage,updated_at=clock_timestamp()
  WHERE id=p_job_id
    AND tenant_id=v_tenant_id
    AND status='RUNNING'
    AND lease_token IS NOT DISTINCT FROM p_lease_token
    AND lease_expires_at>clock_timestamp();
  v_updated := FOUND;
  RETURN v_updated;
END
$$;

REVOKE ALL ON FUNCTION platform.update_data_source_connection_test_stage(uuid,uuid,text)
  FROM PUBLIC;

COMMENT ON COLUMN platform.data_source_connection_test_jobs.stage IS
  'Current safe connection-test stage exposed to polling clients; never contains target or credential values';
COMMENT ON FUNCTION platform.update_data_source_connection_test_stage(uuid,uuid,text) IS
  'Updates the current stage only for the dedicated worker lease that owns the running test';
