BEGIN;

CREATE OR REPLACE FUNCTION askdata.reject_shadow_run_side_effect()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_run_id uuid;
BEGIN
  selected_run_id:=CASE TG_TABLE_NAME
    WHEN 'saved_questions' THEN NEW.source_question_run_id
    ELSE NEW.question_run_id END;
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
