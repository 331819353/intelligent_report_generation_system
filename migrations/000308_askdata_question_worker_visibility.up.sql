-- Worker roles intentionally have no tenant actor session, so forced RLS makes
-- a direct tenant enumeration over question_runs return zero rows. Expose only
-- the distinct tenant IDs that currently have non-terminal runs; claiming the
-- actual run remains guarded by the separate lease function.

BEGIN;

CREATE OR REPLACE FUNCTION askdata.list_question_run_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
  SELECT DISTINCT run.tenant_id
  FROM askdata.question_runs AS run
  WHERE run.current_state NOT IN (
    'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED',
    'OUT_OF_SCOPE','ANSWERED','BLOCKED'
  )
  ORDER BY run.tenant_id
$$;

REVOKE ALL ON FUNCTION askdata.list_question_run_tenants() FROM PUBLIC;

COMMENT ON FUNCTION askdata.list_question_run_tenants() IS
  'RLS-safe tenant enumeration for the question-run worker; returns no actor or question data';

COMMIT;
