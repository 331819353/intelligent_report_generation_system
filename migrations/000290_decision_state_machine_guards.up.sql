-- Enforce the decision and action state machines below the service layer.
CREATE OR REPLACE FUNCTION decision.status_transition_allowed(previous_status text,next_status text)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT AS $$
  SELECT previous_status<>next_status AND CASE previous_status
    WHEN 'DRAFT' THEN next_status IN ('IN_REVIEW','CANCELED')
    WHEN 'IN_REVIEW' THEN next_status IN ('APPROVED','REJECTED','CANCELED')
    WHEN 'APPROVED' THEN next_status IN ('IN_EXECUTION','CANCELED')
    WHEN 'IN_EXECUTION' THEN next_status IN ('REVIEW_DUE','CANCELED')
    WHEN 'REOPENED' THEN next_status IN ('REVIEW_DUE','CANCELED')
    WHEN 'REVIEW_DUE' THEN next_status IN ('CLOSED','REOPENED','CANCELED')
    WHEN 'REJECTED' THEN next_status='REOPENED'
    WHEN 'CLOSED' THEN next_status='REOPENED'
    ELSE false
  END
$$;

CREATE OR REPLACE FUNCTION decision.action_transition_allowed(previous_status text,next_status text)
RETURNS boolean LANGUAGE sql IMMUTABLE STRICT AS $$
  SELECT previous_status<>next_status AND CASE previous_status
    WHEN 'TODO' THEN next_status IN ('DOING','CANCELED')
    WHEN 'DOING' THEN next_status IN ('BLOCKED','DONE','CANCELED')
    WHEN 'BLOCKED' THEN next_status IN ('DOING','CANCELED')
    WHEN 'DONE' THEN next_status='DOING'
    WHEN 'CANCELED' THEN next_status='DOING'
    ELSE false
  END
$$;

CREATE OR REPLACE FUNCTION decision.guard_decision_mutation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,decision AS $$
DECLARE actor_is_owner boolean;
DECLARE actor_is_final_approver boolean;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'decision facts cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    IF NOT decision.system_access() AND
      (NEW.domain_id<>decision.current_domain_id() OR
       (NEW.owner_user_id<>decision.current_actor_id() AND NEW.created_by<>decision.current_actor_id())) THEN
      RAISE EXCEPTION 'decision creation actor is invalid' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.created_by IS DISTINCT FROM OLD.created_by
    OR NEW.evidence_mode IS DISTINCT FROM OLD.evidence_mode
    OR NEW.approval_policy_id IS DISTINCT FROM OLD.approval_policy_id
    OR NEW.required_approvals IS DISTINCT FROM OLD.required_approvals
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'decision identity and governance pin are immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'decision record version is stale' USING ERRCODE='40001';
  END IF;
  IF NEW.owner_user_id IS DISTINCT FROM OLD.owner_user_id AND NOT decision.system_access() THEN
    RAISE EXCEPTION 'only lifecycle control may transfer a decision owner' USING ERRCODE='42501';
  END IF;
  IF decision.system_access() THEN
    IF NEW.status IS DISTINCT FROM OLD.status AND
      NOT decision.status_transition_allowed(OLD.status,NEW.status) THEN
      RAISE EXCEPTION 'illegal decision state transition' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
  END IF;

  actor_is_owner := OLD.owner_user_id=decision.current_actor_id();
  SELECT EXISTS(
    SELECT 1 FROM decision.decision_approvals approval
    JOIN decision.decision_approval_events event
      ON event.tenant_id=approval.tenant_id AND event.approval_id=approval.id
    WHERE approval.tenant_id=OLD.tenant_id AND approval.decision_id=OLD.id
      AND approval.approver_user_id=decision.current_actor_id()
      AND event.actor_user_id=decision.current_actor_id()
      AND event.status=NEW.status
  ) INTO actor_is_final_approver;

  IF NEW.status IS NOT DISTINCT FROM OLD.status THEN
    IF NOT actor_is_owner OR OLD.status NOT IN ('DRAFT','REOPENED') THEN
      RAISE EXCEPTION 'decision content is not editable' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
  END IF;
  IF NOT decision.status_transition_allowed(OLD.status,NEW.status) THEN
    RAISE EXCEPTION 'illegal decision state transition' USING ERRCODE='23514';
  END IF;
  IF OLD.status='IN_REVIEW' AND NEW.status IN ('APPROVED','REJECTED') THEN
    IF NOT actor_is_final_approver THEN
      RAISE EXCEPTION 'decision approval event is missing' USING ERRCODE='42501';
    END IF;
  ELSIF NOT actor_is_owner THEN
    RAISE EXCEPTION 'decision owner is required for this transition' USING ERRCODE='42501';
  END IF;
  IF NEW.title IS DISTINCT FROM OLD.title OR NEW.question IS DISTINCT FROM OLD.question
    OR NEW.decision_text IS DISTINCT FROM OLD.decision_text
    OR NEW.expected_effect IS DISTINCT FROM OLD.expected_effect
    OR NEW.risks_json IS DISTINCT FROM OLD.risks_json
    OR NEW.review_at IS DISTINCT FROM OLD.review_at THEN
    RAISE EXCEPTION 'state transition cannot rewrite decision content' USING ERRCODE='55000';
  END IF;
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION decision.guard_action_mutation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,decision AS $$
DECLARE parent_status text;
DECLARE parent_owner uuid;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'decision actions cannot be deleted' USING ERRCODE='55000';
  END IF;
  SELECT status,owner_user_id INTO parent_status,parent_owner
  FROM decision.decisions WHERE tenant_id=NEW.tenant_id AND id=NEW.decision_id;
  IF TG_OP='INSERT' THEN
    IF parent_status NOT IN ('APPROVED','IN_EXECUTION','REOPENED') THEN
      RAISE EXCEPTION 'approved decision is required for an action' USING ERRCODE='23514';
    END IF;
    IF NOT decision.system_access() AND
      (NEW.domain_id<>decision.current_domain_id() OR NEW.created_by<>decision.current_actor_id()
       OR parent_owner<>decision.current_actor_id()) THEN
      RAISE EXCEPTION 'decision owner is required to create an action' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.decision_id IS DISTINCT FROM OLD.decision_id
    OR NEW.title IS DISTINCT FROM OLD.title OR NEW.description IS DISTINCT FROM OLD.description
    OR NEW.due_at IS DISTINCT FROM OLD.due_at OR NEW.deliverable_refs IS DISTINCT FROM OLD.deliverable_refs
    OR NEW.created_by IS DISTINCT FROM OLD.created_by OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'action identity and plan are immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 OR NEW.updated_at<OLD.updated_at THEN
    RAISE EXCEPTION 'action record version is stale' USING ERRCODE='40001';
  END IF;
  IF NEW.assignee_user_id IS DISTINCT FROM OLD.assignee_user_id THEN
    IF NOT decision.system_access() OR NEW.status IS DISTINCT FROM OLD.status THEN
      RAISE EXCEPTION 'only lifecycle control may transfer an action owner' USING ERRCODE='42501';
    END IF;
    RETURN NEW;
  END IF;
  IF NOT decision.action_transition_allowed(OLD.status,NEW.status) THEN
    RAISE EXCEPTION 'illegal action state transition' USING ERRCODE='23514';
  END IF;
  IF NOT decision.system_access() AND decision.current_actor_id() NOT IN (OLD.assignee_user_id,parent_owner) THEN
    RAISE EXCEPTION 'action assignee or decision owner is required' USING ERRCODE='42501';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER decision_mutation_guard
BEFORE INSERT OR UPDATE OR DELETE ON decision.decisions
FOR EACH ROW EXECUTE FUNCTION decision.guard_decision_mutation();
CREATE TRIGGER decision_action_mutation_guard
BEFORE INSERT OR UPDATE OR DELETE ON decision.action_items
FOR EACH ROW EXECUTE FUNCTION decision.guard_action_mutation();

DROP POLICY decisions_access ON decision.decisions;
CREATE POLICY decisions_access ON decision.decisions
  USING(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR decision.can_access(id)))
  WITH CHECK(tenant_id=decision.current_tenant_id()
    AND (decision.system_access() OR
      (domain_id=decision.current_domain_id() AND
        (owner_user_id=decision.current_actor_id() OR created_by=decision.current_actor_id()
          OR decision.can_access(id)))));

REVOKE ALL ON FUNCTION decision.status_transition_allowed(text,text),
  decision.action_transition_allowed(text,text),decision.guard_decision_mutation(),
  decision.guard_action_mutation() FROM PUBLIC;
