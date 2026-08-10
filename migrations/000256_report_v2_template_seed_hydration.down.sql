-- Hydrated manifests remain valid when rolling back. Restore the strict
-- immutable-content state machine used before one-time placeholder hydration.
CREATE OR REPLACE FUNCTION platform.enforce_component_template_state()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,platform AS $$
BEGIN
  IF OLD.status='ACTIVE' AND NEW.status NOT IN ('ACTIVE','DEPRECATED') THEN
    RAISE EXCEPTION 'invalid component template state transition' USING ERRCODE='23514';
  ELSIF OLD.status='DEPRECATED' AND NEW.status NOT IN ('DEPRECATED','RETAINED') THEN
    RAISE EXCEPTION 'invalid component template state transition' USING ERRCODE='23514';
  ELSIF OLD.status='RETAINED' AND NEW.status<>'RETAINED' THEN
    RAISE EXCEPTION 'retained component template is immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.manifest_json IS DISTINCT FROM NEW.manifest_json
    OR OLD.content_hash IS DISTINCT FROM NEW.content_hash
    OR OLD.id<>NEW.id
    OR OLD.component_template_id<>NEW.component_template_id
    OR OLD.version IS DISTINCT FROM NEW.version
    OR OLD.migrator_id IS DISTINCT FROM NEW.migrator_id THEN
    RAISE EXCEPTION 'component template version content is immutable' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION platform.enforce_component_template_state() FROM PUBLIC;
