DROP TRIGGER IF EXISTS platform_data_requests_guard ON platform.data_requests;

DELETE FROM askdata.active_learning_candidates
WHERE task_type='DATA_REQUEST_CLUSTER' OR candidate_type='SEMANTIC_ASSET';
ALTER TABLE askdata.active_learning_candidates
  DROP CONSTRAINT active_learning_candidates_task_type_check,
  DROP CONSTRAINT active_learning_candidates_candidate_type_check,
  ADD CONSTRAINT active_learning_candidates_task_type_check CHECK(task_type IN(
    'UNRESOLVED_EXPRESSION','FREQUENT_CLARIFICATION','CONFUSABLE_METRIC',
    'CONFUSABLE_MEMBER','RETRIEVAL_MISS','REPORT_METRIC_COMBINATION','FEEDBACK_CLUSTER'
  )),
  ADD CONSTRAINT active_learning_candidates_candidate_type_check CHECK(candidate_type IN(
    'BUSINESS_TERM','NEGATIVE_CONTEXT','CERTIFIED_EXAMPLE','HARD_NEGATIVE',
    'SEARCH_DOCUMENT','KPI_BUNDLE','FIX_PRIORITY'
  ));

CREATE OR REPLACE FUNCTION platform.guard_data_request_mutation()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE actor_id uuid := platform.current_user_id();
DECLARE approver_valid boolean;
BEGIN
  IF platform.is_system_access() THEN
    RETURN NEW;
  END IF;
  IF actor_id IS NULL OR NEW.tenant_id<>platform.current_tenant_id()
    OR NEW.domain_id<>platform.current_domain_id()
    OR NOT platform.user_has_active_domain_membership(NEW.domain_id) THEN
    RAISE EXCEPTION 'data request access context is invalid' USING ERRCODE='42501';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NEW.requester_user_id<>actor_id OR NEW.state<>'DRAFT' OR NEW.record_version<>1 THEN
      RAISE EXCEPTION 'data request creation identity is invalid' USING ERRCODE='42501';
    END IF;
    SELECT bool_and(platform.data_request_actor_is_domain_admin(
      NEW.tenant_id,NEW.domain_id,approver_id
    )) INTO approver_valid
    FROM unnest(NEW.approver_user_ids) AS approver_id;
    IF NOT COALESCE(approver_valid,false) THEN
      RAISE EXCEPTION 'data request approver set is invalid' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.id<>OLD.id OR NEW.tenant_id<>OLD.tenant_id OR NEW.domain_id<>OLD.domain_id
    OR NEW.requester_user_id<>OLD.requester_user_id
    OR NEW.source_question_run_id IS DISTINCT FROM OLD.source_question_run_id
    OR NEW.request_text<>OLD.request_text OR NEW.parsed_context_json<>OLD.parsed_context_json
    OR NEW.business_purpose<>OLD.business_purpose
    OR NEW.required_fields_json<>OLD.required_fields_json
    OR NEW.sensitivity_level<>OLD.sensitivity_level
    OR NEW.approver_user_ids<>OLD.approver_user_ids
    OR NEW.security_cosign_user_id IS DISTINCT FROM OLD.security_cosign_user_id
    OR NEW.sla_due_at<>OLD.sla_due_at OR NEW.created_at<>OLD.created_at
    OR NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'data request immutable facts changed' USING ERRCODE='23514';
  END IF;
  IF NOT (
    (OLD.state='DRAFT' AND NEW.state='SUBMITTED' AND actor_id=OLD.requester_user_id)
    OR (OLD.state='SUBMITTED' AND NEW.state IN ('APPROVED','REJECTED')
      AND actor_id=ANY(OLD.approver_user_ids))
    OR (OLD.state='APPROVED' AND NEW.state='IN_PROGRESS'
      AND actor_id=ANY(OLD.approver_user_ids))
    OR (OLD.state='IN_PROGRESS' AND NEW.state='DELIVERED'
      AND (actor_id=OLD.assignee_user_id OR actor_id=ANY(OLD.approver_user_ids)))
    OR (OLD.state='DELIVERED' AND NEW.state='CLOSED'
      AND (actor_id=OLD.requester_user_id OR actor_id=ANY(OLD.approver_user_ids)))
  ) THEN
    RAISE EXCEPTION 'data request transition is not permitted' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER platform_data_requests_guard
BEFORE INSERT OR UPDATE ON platform.data_requests
FOR EACH ROW EXECUTE FUNCTION platform.guard_data_request_mutation();

REVOKE ALL ON FUNCTION platform.guard_data_request_mutation() FROM PUBLIC;
DROP FUNCTION IF EXISTS platform.derive_data_request_sensitivity(uuid,uuid,uuid,jsonb,jsonb);

DROP POLICY IF EXISTS platform_data_request_export_jobs_access ON platform.data_request_export_jobs;
DROP TABLE IF EXISTS platform.data_request_export_jobs;

DROP TRIGGER IF EXISTS platform_data_request_events_guard ON platform.data_request_events;
DELETE FROM platform.data_request_events WHERE event_type<>'STATE_TRANSITION';
ALTER TABLE platform.data_request_events
  DROP CONSTRAINT IF EXISTS platform_data_request_events_request_audit_key,
  DROP CONSTRAINT IF EXISTS platform_data_request_events_audit_no_check,
  ALTER COLUMN sequence_no SET NOT NULL,
  DROP COLUMN IF EXISTS audit_no,
  DROP COLUMN IF EXISTS event_type;

CREATE OR REPLACE FUNCTION platform.guard_data_request_event()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
DECLARE current_state text;
DECLARE current_version bigint;
DECLARE latest_state text;
DECLARE latest_sequence bigint;
DECLARE requester_id uuid;
BEGIN
  IF TG_OP<>'INSERT' THEN
    RAISE EXCEPTION 'data request events are append only' USING ERRCODE='23514';
  END IF;
  SELECT request.state,request.record_version,request.requester_user_id
  INTO current_state,current_version,requester_id
  FROM platform.data_requests AS request
  WHERE request.id=NEW.data_request_id AND request.tenant_id=NEW.tenant_id
    AND request.domain_id=NEW.domain_id;
  SELECT event.to_state,event.sequence_no INTO latest_state,latest_sequence
  FROM platform.data_request_events AS event
  WHERE event.data_request_id=NEW.data_request_id AND event.tenant_id=NEW.tenant_id
  ORDER BY event.sequence_no DESC LIMIT 1;
  IF NEW.to_state<>current_state OR NEW.sequence_no<>current_version
    OR NEW.actor_user_id IS DISTINCT FROM platform.current_user_id()
    OR (latest_sequence IS NULL AND (NEW.sequence_no<>1 OR NEW.from_state IS NOT NULL
      OR NEW.to_state<>'DRAFT' OR NEW.actor_user_id<>requester_id))
    OR (latest_sequence IS NOT NULL AND (NEW.sequence_no<>latest_sequence+1
      OR NEW.from_state IS NULL OR NEW.from_state<>latest_state
      OR NEW.to_state=NEW.from_state)) THEN
    RAISE EXCEPTION 'data request event chain is invalid' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;
CREATE TRIGGER platform_data_request_events_guard
BEFORE INSERT OR UPDATE OR DELETE ON platform.data_request_events
FOR EACH ROW EXECUTE FUNCTION platform.guard_data_request_event();
REVOKE ALL ON FUNCTION platform.guard_data_request_event() FROM PUBLIC;

ALTER TABLE platform.data_request_events
  DROP CONSTRAINT IF EXISTS platform_data_request_events_details_check,
  DROP COLUMN IF EXISTS details_json;

ALTER TABLE platform.dataset_fields DROP COLUMN IF EXISTS sensitivity_level;
