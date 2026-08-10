DROP TABLE IF EXISTS askdata.question_seed_contexts;

CREATE OR REPLACE FUNCTION askdata.reject_non_runnable_question_release()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE selected_status text;
BEGIN
  SELECT status INTO selected_status FROM askdata.releases
  WHERE id=NEW.release_id AND tenant_id=NEW.tenant_id
    AND domain_id=NEW.domain_id AND content_hash=NEW.release_content_hash;
  IF selected_status IN ('SUPERSEDED','RETAINED','RETIRED') THEN
    RAISE EXCEPTION 'RELEASE_NOT_RUNNABLE: semantic release cannot create a new question run'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

DROP FUNCTION IF EXISTS askdata.report_seed_release_is_runnable(uuid,uuid,uuid,uuid,uuid,text);
ALTER TABLE askdata.conversations
  DROP CONSTRAINT IF EXISTS askdata_conversations_report_source_shape,
  DROP COLUMN IF EXISTS report_component_id,
  DROP COLUMN IF EXISTS report_version_id,
  DROP COLUMN IF EXISTS pin_source;

CREATE OR REPLACE FUNCTION askdata.lock_active_question_release(
  selected_tenant_id uuid,
  selected_domain_id uuid,
  selected_release_id uuid,
  selected_content_hash text
)
RETURNS boolean
LANGUAGE plpgsql
VOLATILE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE release_valid boolean := false;
BEGIN
  IF NOT askdata.tenant_matches(selected_tenant_id)
    OR NOT askdata.domain_can_access(selected_domain_id)
    OR (NOT askdata.system_access() AND askdata.current_actor_id() IS NULL) THEN
    RETURN false;
  END IF;
  SELECT true INTO release_valid FROM askdata.releases AS release
  WHERE release.id=selected_release_id AND release.domain_id=selected_domain_id
    AND release.tenant_id=selected_tenant_id AND release.content_hash=selected_content_hash
    AND release.status='ACTIVE'
  FOR SHARE OF release;
  RETURN COALESCE(release_valid,false);
END
$$;

REVOKE ALL ON FUNCTION askdata.lock_active_question_release(uuid,uuid,uuid,text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION askdata.enforce_conversation_release_pin()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE old_status text;
DECLARE new_status text;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'question conversation cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.pinned_release_id IS NOT NULL THEN
      SELECT status INTO new_status FROM askdata.releases
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.pinned_release_id FOR SHARE;
      IF new_status<>'ACTIVE' OR NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'initial conversation pin requires the current ACTIVE release' USING ERRCODE='23514';
      END IF;
      NEW.pinned_at=COALESCE(NEW.pinned_at,clock_timestamp());
    END IF;
    NEW.created_at=clock_timestamp(); NEW.updated_at=NEW.created_at; RETURN NEW;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'conversation identity is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.pinned_release_id IS DISTINCT FROM OLD.pinned_release_id THEN
    IF NEW.pinned_release_id IS NULL THEN RAISE EXCEPTION 'conversation release pin cannot be cleared' USING ERRCODE='55000'; END IF;
    SELECT status INTO new_status FROM askdata.releases
    WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id AND id=NEW.pinned_release_id FOR SHARE;
    IF new_status<>'ACTIVE' THEN RAISE EXCEPTION 'conversation can only pin the current ACTIVE release' USING ERRCODE='23514'; END IF;
    IF OLD.pinned_release_id IS NULL THEN
      IF NEW.pin_drift_acknowledged THEN RAISE EXCEPTION 'first successful binding is not a drift acknowledgement' USING ERRCODE='23514'; END IF;
    ELSE
      SELECT status INTO old_status FROM askdata.releases
      WHERE tenant_id=OLD.tenant_id AND domain_id=OLD.domain_id AND id=OLD.pinned_release_id FOR SHARE;
      IF old_status NOT IN ('SUPERSEDED','RETAINED','RETIRED') OR NOT NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'release drift switch requires an acknowledged stale pin' USING ERRCODE='23514';
      END IF;
    END IF;
    NEW.pinned_at=clock_timestamp();
  ELSIF NEW.pin_drift_acknowledged IS DISTINCT FROM OLD.pin_drift_acknowledged THEN
    RAISE EXCEPTION 'drift acknowledgement can only change with the release pin' USING ERRCODE='55000';
  END IF;
  NEW.updated_at=clock_timestamp(); RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.enforce_conversation_release_pin() FROM PUBLIC;
