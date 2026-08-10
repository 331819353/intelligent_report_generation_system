-- FUSE-001 repair: tenant discovery must cross FORCE RLS only through a
-- bounded SECURITY DEFINER function. Intent and outbox identity/payload facts
-- are immutable after insertion; only their explicit state machines advance.
DROP INDEX IF EXISTS askdata.add_to_report_outbox_claim_idx;
CREATE INDEX add_to_report_outbox_claim_idx
  ON askdata.add_to_report_outbox(
    tenant_id,state,next_attempt_at,lease_expires_at,created_at,id
  ) WHERE state IN ('PENDING','RUNNING','FAILED');

ALTER TABLE askdata.add_to_report_outbox
  ADD CONSTRAINT add_to_report_outbox_lease_shape_check CHECK(
    (state='RUNNING')=(lease_token IS NOT NULL AND lease_expires_at IS NOT NULL)
  );

CREATE OR REPLACE FUNCTION askdata.list_add_to_report_tenants()
RETURNS TABLE(tenant_id uuid)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
  SELECT DISTINCT outbox.tenant_id
  FROM askdata.add_to_report_outbox AS outbox
  JOIN askdata.add_to_report_intents AS intent
    ON intent.id=outbox.intent_id AND intent.tenant_id=outbox.tenant_id
  WHERE intent.state='PENDING' AND intent.expires_at>now()
    AND outbox.attempt<10
    AND (
      (outbox.state IN ('PENDING','FAILED') AND outbox.next_attempt_at<=now())
      OR (outbox.state='RUNNING' AND outbox.lease_expires_at<=now())
    )
  ORDER BY outbox.tenant_id
$$;

REVOKE ALL ON FUNCTION askdata.list_add_to_report_tenants() FROM PUBLIC;

CREATE OR REPLACE FUNCTION askdata.guard_add_to_report_intent()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'add-to-report intent cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF ROW(OLD.id,OLD.tenant_id,OLD.question_run_id,OLD.actor_user_id,
         OLD.idempotency_key,OLD.target_report_id,OLD.target_page_id,
         OLD.target_section_id,OLD.target_block_id,OLD.operation_bundle_json,
         OLD.preview_hash,OLD.created_at,OLD.expires_at)
     IS DISTINCT FROM
     ROW(NEW.id,NEW.tenant_id,NEW.question_run_id,NEW.actor_user_id,
         NEW.idempotency_key,NEW.target_report_id,NEW.target_page_id,
         NEW.target_section_id,NEW.target_block_id,NEW.operation_bundle_json,
         NEW.preview_hash,NEW.created_at,NEW.expires_at) THEN
    RAISE EXCEPTION 'add-to-report intent facts are immutable' USING ERRCODE='55000';
  END IF;
  IF OLD.state IN ('APPLIED','REJECTED','EXPIRED')
    OR (OLD.state='PENDING_CONFIRMATION' AND NEW.state NOT IN ('PENDING_CONFIRMATION','PENDING','EXPIRED'))
    OR (OLD.state='PENDING' AND NEW.state NOT IN ('PENDING','APPLIED','REJECTED','EXPIRED')) THEN
    RAISE EXCEPTION 'invalid add-to-report intent transition' USING ERRCODE='23514';
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.guard_add_to_report_outbox()
RETURNS trigger
LANGUAGE plpgsql
SET search_path=pg_catalog,askdata
AS $$
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'add-to-report outbox cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF ROW(OLD.id,OLD.tenant_id,OLD.intent_id,OLD.created_at)
     IS DISTINCT FROM ROW(NEW.id,NEW.tenant_id,NEW.intent_id,NEW.created_at)
    OR NEW.attempt<OLD.attempt OR NEW.attempt>OLD.attempt+1
    OR (OLD.state='PENDING' AND NEW.state NOT IN ('PENDING','RUNNING','DONE'))
    OR (OLD.state='FAILED' AND NEW.state NOT IN ('FAILED','RUNNING','DONE'))
    OR (OLD.state='RUNNING' AND NEW.state NOT IN ('RUNNING','FAILED','DONE'))
    OR OLD.state='DONE' THEN
    RAISE EXCEPTION 'invalid add-to-report outbox transition' USING ERRCODE='23514';
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE TRIGGER add_to_report_intents_guard
BEFORE UPDATE OR DELETE ON askdata.add_to_report_intents
FOR EACH ROW EXECUTE FUNCTION askdata.guard_add_to_report_intent();

CREATE TRIGGER add_to_report_outbox_guard
BEFORE UPDATE OR DELETE ON askdata.add_to_report_outbox
FOR EACH ROW EXECUTE FUNCTION askdata.guard_add_to_report_outbox();

REVOKE ALL ON FUNCTION askdata.guard_add_to_report_intent() FROM PUBLIC;
REVOKE ALL ON FUNCTION askdata.guard_add_to_report_outbox() FROM PUBLIC;
