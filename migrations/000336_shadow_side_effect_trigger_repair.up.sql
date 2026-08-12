BEGIN;

-- Row records expose their source run under different column names. PL/pgSQL
-- resolves every NEW.field reference at runtime, so a CASE expression that
-- mentions both names still fails on relations lacking one of them. Converting
-- NEW to jsonb keeps the shared trigger fail-closed without referencing a
-- relation-specific field directly.
CREATE OR REPLACE FUNCTION askdata.reject_shadow_run_side_effect()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_run_id uuid;
DECLARE new_row jsonb;
BEGIN
  new_row:=to_jsonb(NEW);
  selected_run_id:=NULLIF(
    COALESCE(new_row->>'source_question_run_id',new_row->>'question_run_id'),''
  )::uuid;
  IF selected_run_id IS NOT NULL AND EXISTS(
    SELECT 1 FROM askdata.question_runs
    WHERE tenant_id=NEW.tenant_id AND id=selected_run_id AND execution_mode='SHADOW'
  ) THEN
    RAISE EXCEPTION 'shadow question run cannot create user-visible side effects' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.reject_shadow_run_side_effect() FROM PUBLIC;

COMMIT;
