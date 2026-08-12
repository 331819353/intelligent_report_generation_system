-- Append-only Release approval recovery: withdrawal, rejection reset, resubmission and SLA escalation.
BEGIN;

ALTER TABLE askdata.release_approvals
  DROP CONSTRAINT askdata_release_approvals_slot_key,
  DROP CONSTRAINT askdata_release_approvals_reviewer_key;

CREATE INDEX askdata_release_approvals_slot_idx ON askdata.release_approvals(
  tenant_id,release_id,gate_receipt_hash,review_slot,approved_at DESC,id
);
CREATE INDEX askdata_release_approvals_reviewer_idx ON askdata.release_approvals(
  tenant_id,release_id,gate_receipt_hash,reviewer_id,approved_at DESC,id
);

CREATE TABLE askdata.release_approval_withdrawals(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  gate_receipt_hash text NOT NULL CHECK(gate_receipt_hash ~ '^[0-9a-f]{64}$'),
  approval_id uuid NOT NULL,
  withdrawn_by uuid NOT NULL,
  reason_hash text NOT NULL CHECK(reason_hash ~ '^[0-9a-f]{64}$'),
  withdrawal_kind text NOT NULL CHECK(withdrawal_kind IN ('SELF_WITHDRAWN','REJECTION_RESET')),
  withdrawn_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_approval_withdrawals_approval_key UNIQUE(tenant_id,approval_id),
  CONSTRAINT askdata_release_approval_withdrawals_approval_fk FOREIGN KEY(approval_id,tenant_id)
    REFERENCES askdata.release_approvals(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_approval_withdrawals_actor_fk FOREIGN KEY(withdrawn_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TABLE askdata.release_approval_escalations(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  gate_receipt_hash text NOT NULL CHECK(gate_receipt_hash ~ '^[0-9a-f]{64}$'),
  escalation_level smallint NOT NULL CHECK(escalation_level BETWEEN 1 AND 3),
  escalated_by uuid NOT NULL,
  reason_hash text NOT NULL CHECK(reason_hash ~ '^[0-9a-f]{64}$'),
  escalated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_approval_escalations_level_key UNIQUE(
    tenant_id,release_id,gate_receipt_hash,escalation_level
  ),
  CONSTRAINT askdata_release_approval_escalations_actor_fk FOREIGN KEY(escalated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE TRIGGER askdata_release_approval_withdrawals_immutable
BEFORE UPDATE OR DELETE ON askdata.release_approval_withdrawals
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();
CREATE TRIGGER askdata_release_approval_escalations_immutable
BEFORE UPDATE OR DELETE ON askdata.release_approval_escalations
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

ALTER TABLE askdata.release_approval_withdrawals ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_approval_withdrawals FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_approval_withdrawals_management_isolation
  ON askdata.release_approval_withdrawals
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));
ALTER TABLE askdata.release_approval_escalations ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_approval_escalations FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_approval_escalations_management_isolation
  ON askdata.release_approval_escalations
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));

CREATE OR REPLACE FUNCTION askdata.submit_release_approval_v2(
  selected_release_id uuid, selected_evaluation_set_id uuid,
  selected_evaluation_batch_id uuid, selected_gate_receipt_hash text,
  selected_review_role text, selected_decision text, selected_comment_hash text,
  selected_actor_id uuid, selected_submission_id uuid
)
RETURNS text
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_gate askdata.release_evaluation_gate_receipts%ROWTYPE;
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE selected_slot smallint;
DECLARE selected_approval_hash text;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_submission_id IS NULL
    OR selected_review_role NOT IN ('SEMANTIC_OWNER','DATA_OWNER')
    OR selected_decision NOT IN ('APPROVED','REJECTED')
    OR selected_gate_receipt_hash !~ '^[0-9a-f]{64}$'
    OR selected_comment_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_INVALID' USING ERRCODE='22023';
  END IF;
  selected_slot := CASE selected_review_role WHEN 'SEMANTIC_OWNER' THEN 1 ELSE 2 END;
  SELECT * INTO selected_gate FROM askdata.release_evaluation_gate_receipts
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND release_id=selected_release_id AND evaluation_set_id=selected_evaluation_set_id
    AND evaluation_batch_id=selected_evaluation_batch_id
    AND receipt_hash=selected_gate_receipt_hash FOR SHARE;
  SELECT * INTO selected_release FROM askdata.releases
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND id=selected_release_id FOR SHARE;
  IF selected_gate.id IS NULL OR selected_release.id IS NULL OR NOT selected_gate.passed
    OR selected_release.status NOT IN ('READY','SUPERSEDED')
    OR selected_release.content_hash<>selected_gate.release_content_hash
    OR askdata.evaluation_control_can_access(selected_gate.tenant_id,selected_gate.domain_id) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_GATE_NOT_PASSED' USING ERRCODE='55000';
  END IF;
  IF NOT EXISTS(SELECT 1 FROM askdata.release_review_reports AS report
    WHERE report.tenant_id=selected_gate.tenant_id AND report.release_id=selected_gate.release_id
      AND report.gate_receipt_hash=selected_gate.receipt_hash
      AND report.generated_at>selected_release.updated_at) THEN
    RAISE EXCEPTION 'RELEASE_REVIEW_REQUIRED' USING ERRCODE='55000';
  END IF;
  IF EXISTS(SELECT 1 FROM askdata.release_approvals AS approval
    LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
      ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
    WHERE approval.tenant_id=selected_gate.tenant_id
      AND approval.release_id=selected_gate.release_id
      AND approval.gate_receipt_hash=selected_gate.receipt_hash
      AND withdrawal.id IS NULL AND approval.decision='REJECTED') THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_REJECTION_REQUIRES_RESET' USING ERRCODE='55000';
  END IF;
  IF EXISTS(SELECT 1 FROM askdata.release_approvals AS approval
    LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
      ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
    WHERE approval.tenant_id=selected_gate.tenant_id
      AND approval.release_id=selected_gate.release_id
      AND approval.gate_receipt_hash=selected_gate.receipt_hash
      AND withdrawal.id IS NULL
      AND (approval.review_slot=selected_slot OR approval.reviewer_id=selected_actor_id)) THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_DUTY_SEPARATION' USING ERRCODE='23505';
  END IF;
  selected_approval_hash := encode(public.digest(
    selected_gate.tenant_id::text||':'||selected_release.id::text||':'||selected_gate.receipt_hash
    ||':'||selected_slot::text||':'||selected_review_role||':'||selected_actor_id::text
    ||':'||selected_decision||':'||selected_comment_hash||':'||selected_submission_id::text,'sha256'),'hex');
  INSERT INTO askdata.release_approvals(id,tenant_id,domain_id,release_id,release_content_hash,
    evaluation_set_id,evaluation_set_content_hash,evaluation_batch_id,gate_receipt_hash,
    review_slot,review_role,reviewer_id,decision,comment_hash,approval_hash,approved_at)
  VALUES(selected_submission_id,selected_gate.tenant_id,selected_gate.domain_id,
    selected_gate.release_id,selected_gate.release_content_hash,selected_gate.evaluation_set_id,
    selected_gate.evaluation_set_content_hash,selected_gate.evaluation_batch_id,
    selected_gate.receipt_hash,selected_slot,selected_review_role,selected_actor_id,
    selected_decision,selected_comment_hash,selected_approval_hash,clock_timestamp());
  RETURN selected_approval_hash;
END $$;

CREATE OR REPLACE FUNCTION askdata.withdraw_release_approval(
  selected_release_id uuid, selected_gate_receipt_hash text,
  selected_review_role text, selected_reason_hash text, selected_actor_id uuid
)
RETURNS uuid
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_approval askdata.release_approvals%ROWTYPE;
DECLARE selected_withdrawal_id uuid := gen_random_uuid();
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_review_role NOT IN ('SEMANTIC_OWNER','DATA_OWNER')
    OR selected_gate_receipt_hash !~ '^[0-9a-f]{64}$'
    OR selected_reason_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_WITHDRAW_INVALID' USING ERRCODE='22023';
  END IF;
  SELECT approval.* INTO selected_approval FROM askdata.release_approvals AS approval
  LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
    ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
  WHERE approval.tenant_id=askdata.current_tenant_id()
    AND approval.domain_id=askdata.current_domain_id()
    AND approval.release_id=selected_release_id
    AND approval.gate_receipt_hash=selected_gate_receipt_hash
    AND approval.review_role=selected_review_role AND approval.reviewer_id=selected_actor_id
    AND withdrawal.id IS NULL ORDER BY approval.approved_at DESC LIMIT 1 FOR UPDATE OF approval;
  IF selected_approval.id IS NULL THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_NOT_WITHDRAWABLE' USING ERRCODE='55000';
  END IF;
  IF (SELECT count(*) FROM askdata.release_approvals AS approval
      LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
        ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
      WHERE approval.tenant_id=selected_approval.tenant_id
        AND approval.release_id=selected_release_id
        AND approval.gate_receipt_hash=selected_gate_receipt_hash
        AND withdrawal.id IS NULL AND approval.decision='APPROVED') >= 2 THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_ALREADY_COMPLETE' USING ERRCODE='55000';
  END IF;
  INSERT INTO askdata.release_approval_withdrawals(id,tenant_id,domain_id,release_id,
    gate_receipt_hash,approval_id,withdrawn_by,reason_hash,withdrawal_kind)
  VALUES(selected_withdrawal_id,selected_approval.tenant_id,selected_approval.domain_id,
    selected_release_id,selected_gate_receipt_hash,selected_approval.id,selected_actor_id,
    selected_reason_hash,'SELF_WITHDRAWN');
  RETURN selected_withdrawal_id;
END $$;

CREATE OR REPLACE FUNCTION askdata.reset_rejected_release_approvals(
  selected_release_id uuid, selected_gate_receipt_hash text,
  selected_reason_hash text, selected_actor_id uuid
)
RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE reset_count integer;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_gate_receipt_hash !~ '^[0-9a-f]{64}$'
    OR selected_reason_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_RESET_INVALID' USING ERRCODE='22023';
  END IF;
  IF NOT EXISTS(SELECT 1 FROM askdata.release_approvals AS approval
    LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
      ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
    WHERE approval.tenant_id=askdata.current_tenant_id()
      AND approval.domain_id=askdata.current_domain_id()
      AND approval.release_id=selected_release_id
      AND approval.gate_receipt_hash=selected_gate_receipt_hash
      AND approval.decision='REJECTED' AND withdrawal.id IS NULL) THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_REJECTION_MISSING' USING ERRCODE='55000';
  END IF;
  INSERT INTO askdata.release_approval_withdrawals(tenant_id,domain_id,release_id,
    gate_receipt_hash,approval_id,withdrawn_by,reason_hash,withdrawal_kind)
  SELECT approval.tenant_id,approval.domain_id,approval.release_id,approval.gate_receipt_hash,
    approval.id,selected_actor_id,selected_reason_hash,'REJECTION_RESET'
  FROM askdata.release_approvals AS approval
  LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
    ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
  WHERE approval.tenant_id=askdata.current_tenant_id()
    AND approval.domain_id=askdata.current_domain_id()
    AND approval.release_id=selected_release_id
    AND approval.gate_receipt_hash=selected_gate_receipt_hash AND withdrawal.id IS NULL;
  GET DIAGNOSTICS reset_count = ROW_COUNT;
  RETURN reset_count;
END $$;

CREATE OR REPLACE FUNCTION askdata.escalate_release_approval(
  selected_release_id uuid, selected_gate_receipt_hash text,
  selected_reason_hash text, selected_actor_id uuid
)
RETURNS smallint
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_gate askdata.release_evaluation_gate_receipts%ROWTYPE;
DECLARE selected_level smallint;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_gate_receipt_hash !~ '^[0-9a-f]{64}$'
    OR selected_reason_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_ESCALATION_INVALID' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_gate FROM askdata.release_evaluation_gate_receipts
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND release_id=selected_release_id AND receipt_hash=selected_gate_receipt_hash FOR SHARE;
  IF selected_gate.id IS NULL OR NOT selected_gate.passed
    OR selected_gate.recomputed_at + interval '24 hours' > clock_timestamp() THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_SLA_NOT_DUE' USING ERRCODE='55000';
  END IF;
  SELECT (count(*)+1)::smallint INTO selected_level
  FROM askdata.release_approval_escalations
  WHERE tenant_id=selected_gate.tenant_id AND release_id=selected_release_id
    AND gate_receipt_hash=selected_gate_receipt_hash;
  IF selected_level>3 THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_ESCALATION_MAX' USING ERRCODE='55000';
  END IF;
  INSERT INTO askdata.release_approval_escalations(tenant_id,domain_id,release_id,
    gate_receipt_hash,escalation_level,escalated_by,reason_hash)
  VALUES(selected_gate.tenant_id,selected_gate.domain_id,selected_release_id,
    selected_gate_receipt_hash,selected_level,selected_actor_id,selected_reason_hash);
  RETURN selected_level;
END $$;

REVOKE ALL ON FUNCTION
  askdata.submit_release_approval_v2(uuid,uuid,uuid,text,text,text,text,uuid,uuid),
  askdata.withdraw_release_approval(uuid,text,text,text,uuid),
  askdata.reset_rejected_release_approvals(uuid,text,text,uuid),
  askdata.escalate_release_approval(uuid,text,text,uuid)
FROM PUBLIC;
REVOKE ALL ON TABLE askdata.release_approval_withdrawals,askdata.release_approval_escalations FROM PUBLIC;

COMMIT;
