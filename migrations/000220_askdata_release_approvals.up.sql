-- Immutable LLM review evidence, two-person approval and atomic activation.
-- LLM output remains advisory: activate_release always recomputes DB-007 and
-- never accepts a caller-provided gate result.

CREATE TABLE askdata.release_review_reports(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_set_id uuid NOT NULL,
  evaluation_set_content_hash text NOT NULL CHECK(evaluation_set_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_batch_id uuid NOT NULL,
  gate_receipt_hash text NOT NULL CHECK(gate_receipt_hash ~ '^[0-9a-f]{64}$'),
  recommendation text NOT NULL CHECK(recommendation IN ('APPROVE','CONDITIONAL','REJECT')),
  report_json jsonb NOT NULL CHECK(
    jsonb_typeof(report_json)='object'
    AND pg_column_size(report_json)<=131072
    AND askdata.json_is_safe(report_json)
  ),
  report_hash text NOT NULL CHECK(report_hash ~ '^[0-9a-f]{64}$'),
  generated_by uuid NOT NULL,
  generated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_review_reports_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_review_reports_hash_key UNIQUE(
    tenant_id,release_id,gate_receipt_hash,report_hash
  ),
  CONSTRAINT askdata_release_review_reports_release_fk FOREIGN KEY(
    release_id,release_content_hash,domain_id,tenant_id
  ) REFERENCES askdata.releases(id,content_hash,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_review_reports_set_fk FOREIGN KEY(
    evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_review_reports_gate_fk FOREIGN KEY(
    tenant_id,gate_receipt_hash
  ) REFERENCES askdata.release_evaluation_gate_receipts(tenant_id,receipt_hash) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_review_reports_actor_fk FOREIGN KEY(generated_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT
);

CREATE INDEX askdata_release_review_reports_subject_idx
  ON askdata.release_review_reports(
    tenant_id,domain_id,release_id,gate_receipt_hash,generated_at DESC,id
  );

CREATE TABLE askdata.release_approvals(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  release_id uuid NOT NULL,
  release_content_hash text NOT NULL CHECK(release_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_set_id uuid NOT NULL,
  evaluation_set_content_hash text NOT NULL CHECK(evaluation_set_content_hash ~ '^[0-9a-f]{64}$'),
  evaluation_batch_id uuid NOT NULL,
  gate_receipt_hash text NOT NULL CHECK(gate_receipt_hash ~ '^[0-9a-f]{64}$'),
  review_slot smallint NOT NULL CHECK(review_slot BETWEEN 1 AND 2),
  review_role text NOT NULL CHECK(review_role IN ('SEMANTIC_OWNER','DATA_OWNER')),
  reviewer_id uuid NOT NULL,
  decision text NOT NULL CHECK(decision IN ('APPROVED','REJECTED')),
  comment_hash text NOT NULL CHECK(comment_hash ~ '^[0-9a-f]{64}$'),
  approval_hash text NOT NULL CHECK(approval_hash ~ '^[0-9a-f]{64}$'),
  approved_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT askdata_release_approvals_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT askdata_release_approvals_slot_key UNIQUE(
    tenant_id,release_id,gate_receipt_hash,review_slot
  ),
  CONSTRAINT askdata_release_approvals_reviewer_key UNIQUE(
    tenant_id,release_id,gate_receipt_hash,reviewer_id
  ),
  CONSTRAINT askdata_release_approvals_hash_key UNIQUE(tenant_id,approval_hash),
  CONSTRAINT askdata_release_approvals_release_fk FOREIGN KEY(
    release_id,release_content_hash,domain_id,tenant_id
  ) REFERENCES askdata.releases(id,content_hash,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_approvals_set_fk FOREIGN KEY(
    evaluation_set_id,domain_id,tenant_id
  ) REFERENCES askdata.evaluation_sets(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_approvals_gate_fk FOREIGN KEY(
    tenant_id,gate_receipt_hash
  ) REFERENCES askdata.release_evaluation_gate_receipts(tenant_id,receipt_hash) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_approvals_reviewer_fk FOREIGN KEY(reviewer_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_release_approvals_role_slot_check CHECK(
    (review_role='SEMANTIC_OWNER' AND review_slot=1)
    OR (review_role='DATA_OWNER' AND review_slot=2)
  )
);

CREATE INDEX askdata_release_approvals_subject_idx
  ON askdata.release_approvals(
    tenant_id,domain_id,release_id,gate_receipt_hash,review_slot,approved_at,id
  );

CREATE OR REPLACE FUNCTION askdata.record_release_review_report(
  selected_release_id uuid,
  selected_evaluation_set_id uuid,
  selected_evaluation_batch_id uuid,
  selected_gate_receipt_hash text,
  selected_recommendation text,
  selected_report jsonb,
  selected_actor_id uuid
)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_gate askdata.release_evaluation_gate_receipts%ROWTYPE;
DECLARE selected_report_hash text;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_recommendation NOT IN ('APPROVE','CONDITIONAL','REJECT')
    OR selected_report IS NULL OR jsonb_typeof(selected_report)<>'object'
    OR pg_column_size(selected_report)>131072 OR NOT askdata.json_is_safe(selected_report)
    OR selected_gate_receipt_hash !~ '^[0-9a-f]{64}$' THEN
    RAISE EXCEPTION 'RELEASE_REVIEW_INVALID' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_gate FROM askdata.release_evaluation_gate_receipts
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND release_id=selected_release_id AND evaluation_set_id=selected_evaluation_set_id
    AND evaluation_batch_id=selected_evaluation_batch_id
    AND receipt_hash=selected_gate_receipt_hash FOR SHARE;
  IF selected_gate.id IS NULL
    OR (NOT selected_gate.passed AND selected_recommendation='APPROVE')
    OR askdata.evaluation_control_can_access(selected_gate.tenant_id,selected_gate.domain_id) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'RELEASE_REVIEW_GATE_CONFLICT' USING ERRCODE='55000';
  END IF;
  selected_report_hash := encode(public.digest(
    selected_report::text||':'||selected_recommendation||':'||selected_gate_receipt_hash,
    'sha256'
  ),'hex');
  INSERT INTO askdata.release_review_reports(
    tenant_id,domain_id,release_id,release_content_hash,evaluation_set_id,
    evaluation_set_content_hash,evaluation_batch_id,gate_receipt_hash,
    recommendation,report_json,report_hash,generated_by,generated_at
  ) VALUES(
    selected_gate.tenant_id,selected_gate.domain_id,selected_gate.release_id,
    selected_gate.release_content_hash,selected_gate.evaluation_set_id,
    selected_gate.evaluation_set_content_hash,selected_gate.evaluation_batch_id,
    selected_gate.receipt_hash,selected_recommendation,selected_report,
    selected_report_hash,selected_actor_id,clock_timestamp()
  ) ON CONFLICT(tenant_id,release_id,gate_receipt_hash,report_hash) DO NOTHING;
  RETURN selected_report_hash;
END
$$;

CREATE OR REPLACE FUNCTION askdata.submit_release_approval(
  selected_release_id uuid,
  selected_evaluation_set_id uuid,
  selected_evaluation_batch_id uuid,
  selected_gate_receipt_hash text,
  selected_review_role text,
  selected_decision text,
  selected_comment_hash text,
  selected_actor_id uuid
)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_gate askdata.release_evaluation_gate_receipts%ROWTYPE;
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE selected_slot smallint;
DECLARE selected_approval_hash text;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
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
  IF NOT EXISTS(
    SELECT 1 FROM askdata.release_review_reports AS report
    WHERE report.tenant_id=selected_gate.tenant_id
      AND report.release_id=selected_gate.release_id
      AND report.gate_receipt_hash=selected_gate.receipt_hash
      AND report.generated_at>selected_release.updated_at
  ) THEN
    RAISE EXCEPTION 'RELEASE_REVIEW_REQUIRED' USING ERRCODE='55000';
  END IF;
  IF EXISTS(
    SELECT 1 FROM askdata.release_approvals AS approval
    WHERE approval.tenant_id=selected_gate.tenant_id
      AND approval.release_id=selected_gate.release_id
      AND approval.gate_receipt_hash=selected_gate.receipt_hash
      AND (approval.review_slot=selected_slot OR approval.reviewer_id=selected_actor_id)
  ) THEN
    RAISE EXCEPTION 'RELEASE_APPROVAL_DUTY_SEPARATION' USING ERRCODE='23505';
  END IF;
  selected_approval_hash := encode(public.digest(
    selected_gate.tenant_id::text||':'||selected_release.id::text||':'
    ||selected_gate.receipt_hash||':'||selected_slot::text||':'||selected_review_role
    ||':'||selected_actor_id::text||':'||selected_decision||':'||selected_comment_hash,
    'sha256'
  ),'hex');
  INSERT INTO askdata.release_approvals(
    tenant_id,domain_id,release_id,release_content_hash,evaluation_set_id,
    evaluation_set_content_hash,evaluation_batch_id,gate_receipt_hash,
    review_slot,review_role,reviewer_id,decision,comment_hash,approval_hash,approved_at
  ) VALUES(
    selected_gate.tenant_id,selected_gate.domain_id,selected_gate.release_id,
    selected_gate.release_content_hash,selected_gate.evaluation_set_id,
    selected_gate.evaluation_set_content_hash,selected_gate.evaluation_batch_id,
    selected_gate.receipt_hash,selected_slot,selected_review_role,selected_actor_id,
    selected_decision,selected_comment_hash,selected_approval_hash,clock_timestamp()
  );
  RETURN selected_approval_hash;
END
$$;

CREATE OR REPLACE FUNCTION askdata.activate_release(
  selected_release_id uuid,
  selected_evaluation_set_id uuid,
  selected_evaluation_batch_id uuid,
  selected_actor_id uuid,
  selected_expected_state_version bigint
)
RETURNS TABLE(
  activation_succeeded boolean,
  active_release_id uuid,
  superseded_release_id uuid,
  release_state_version bigint,
  gate_receipt_hash text,
  failure_codes text[]
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata,platform
AS $$
DECLARE selected_release askdata.releases%ROWTYPE;
DECLARE selected_state askdata.release_state%ROWTYPE;
DECLARE selected_gate_passed boolean;
DECLARE selected_gate_hash text;
DECLARE selected_gate_failures text[];
DECLARE selected_gate_facts jsonb;
DECLARE approval_count integer;
DECLARE approval_reviewer_count integer;
DECLARE approval_role_count integer;
DECLARE prior_release_id uuid;
DECLARE prior_release_status text;
DECLARE new_state_version bigint;
BEGIN
  IF selected_actor_id IS NULL OR selected_actor_id<>askdata.current_actor_id()
    OR selected_expected_state_version IS NULL OR selected_expected_state_version<1 THEN
    RAISE EXCEPTION 'RELEASE_ACTIVATION_INVALID' USING ERRCODE='22023';
  END IF;
  SELECT * INTO selected_release FROM askdata.releases
  WHERE tenant_id=askdata.current_tenant_id() AND domain_id=askdata.current_domain_id()
    AND id=selected_release_id FOR UPDATE;
  IF selected_release.id IS NULL
    OR askdata.evaluation_control_can_access(selected_release.tenant_id,selected_release.domain_id) IS DISTINCT FROM true THEN
    RAISE EXCEPTION 'RELEASE_ACTIVATION_SCOPE' USING ERRCODE='42501';
  END IF;
  SELECT * INTO selected_state FROM askdata.release_state
  WHERE tenant_id=selected_release.tenant_id AND domain_id=selected_release.domain_id FOR UPDATE;
  IF selected_state.version<>selected_expected_state_version THEN
    RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
      NULL::text,ARRAY['RELEASE_STATE_VERSION_CONFLICT']::text[];
    RETURN;
  END IF;
  IF selected_release.status NOT IN ('READY','SUPERSEDED') THEN
    RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
      NULL::text,ARRAY['RELEASE_STATE_INVALID']::text[];
    RETURN;
  END IF;

  SELECT gate.gate_passed,gate.gate_receipt_hash,gate.gate_failure_codes,gate.gate_facts
  INTO selected_gate_passed,selected_gate_hash,selected_gate_failures,selected_gate_facts
  FROM askdata.recompute_release_evaluation_gate(
    selected_release_id,selected_evaluation_set_id,selected_evaluation_batch_id,selected_actor_id
  ) AS gate;
  IF NOT selected_gate_passed THEN
    RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
      selected_gate_hash,selected_gate_failures;
    RETURN;
  END IF;
  IF NOT EXISTS(
    SELECT 1 FROM askdata.release_review_reports AS report
    WHERE report.tenant_id=selected_release.tenant_id AND report.release_id=selected_release.id
      AND report.gate_receipt_hash=selected_gate_hash
      AND report.generated_at>selected_release.updated_at
  ) THEN
    RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
      selected_gate_hash,ARRAY['RELEASE_REVIEW_REQUIRED']::text[];
    RETURN;
  END IF;
  SELECT count(*),count(DISTINCT reviewer_id),count(DISTINCT review_role)
  INTO approval_count,approval_reviewer_count,approval_role_count
  FROM askdata.release_approvals AS approval
  WHERE approval.tenant_id=selected_release.tenant_id
    AND approval.release_id=selected_release.id
    AND approval.gate_receipt_hash=selected_gate_hash
    AND approval.decision='APPROVED'
    AND approval.approved_at>selected_release.updated_at;
  IF approval_count<>2 OR approval_reviewer_count<>2 OR approval_role_count<>2 THEN
    RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
      selected_gate_hash,ARRAY['RELEASE_APPROVALS_REQUIRED']::text[];
    RETURN;
  END IF;

  prior_release_id := selected_state.active_release_id;
  IF prior_release_id IS NOT NULL THEN
    UPDATE askdata.releases SET status='SUPERSEDED',updated_by=selected_actor_id,
      version=version+1
    WHERE tenant_id=selected_release.tenant_id AND domain_id=selected_release.domain_id
      AND id=prior_release_id AND status='ACTIVE'
    RETURNING status INTO prior_release_status;
    IF prior_release_status IS NULL THEN
      RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
        selected_gate_hash,ARRAY['RELEASE_ACTIVE_POINTER_STALE']::text[];
      RETURN;
    END IF;
    INSERT INTO askdata.release_events(
      tenant_id,domain_id,release_id,event_type,actor_id,detail
    ) VALUES(
      selected_release.tenant_id,selected_release.domain_id,prior_release_id,
      prior_release_status,selected_actor_id,
      jsonb_build_object('supersededBy',selected_release.id,'gateReceiptHash',selected_gate_hash)
    );
  END IF;
  UPDATE askdata.releases SET status='ACTIVE',activated_by=selected_actor_id,
    activated_at=clock_timestamp(),updated_by=selected_actor_id,version=version+1
  WHERE tenant_id=selected_release.tenant_id AND id=selected_release.id;
  UPDATE askdata.release_state SET active_release_id=selected_release.id,
    updated_by=selected_actor_id,version=version+1
  WHERE tenant_id=selected_release.tenant_id AND domain_id=selected_release.domain_id
    AND version=selected_expected_state_version
  RETURNING version INTO new_state_version;
  IF new_state_version IS NULL THEN
    RAISE EXCEPTION 'RELEASE_STATE_VERSION_CONFLICT' USING ERRCODE='40001';
  END IF;
  INSERT INTO askdata.release_events(
    tenant_id,domain_id,release_id,event_type,actor_id,detail
  ) VALUES(
    selected_release.tenant_id,selected_release.domain_id,selected_release.id,
    'ACTIVATED',selected_actor_id,
    jsonb_build_object(
      'gateReceiptHash',selected_gate_hash,'evaluationSetId',selected_evaluation_set_id,
      'evaluationBatchId',selected_evaluation_batch_id,'previousReleaseId',prior_release_id,
      'releaseStateVersion',new_state_version
    )
  );
  RETURN QUERY SELECT true,selected_release.id,prior_release_id,new_state_version,
    selected_gate_hash,'{}'::text[];
END
$$;

CREATE TRIGGER askdata_release_review_reports_immutable
BEFORE UPDATE OR DELETE ON askdata.release_review_reports
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();
CREATE TRIGGER askdata_release_approvals_immutable
BEFORE UPDATE OR DELETE ON askdata.release_approvals
FOR EACH ROW EXECUTE FUNCTION askdata.reject_immutable_mutation();

ALTER TABLE askdata.release_review_reports ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_review_reports FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_review_reports_management_isolation
  ON askdata.release_review_reports
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));

ALTER TABLE askdata.release_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.release_approvals FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_release_approvals_management_isolation
  ON askdata.release_approvals
  USING(askdata.evaluation_control_can_access(tenant_id,domain_id))
  WITH CHECK(askdata.evaluation_control_can_access(tenant_id,domain_id));

REVOKE ALL ON FUNCTION
  askdata.record_release_review_report(uuid,uuid,uuid,text,text,jsonb,uuid),
  askdata.submit_release_approval(uuid,uuid,uuid,text,text,text,text,uuid),
  askdata.activate_release(uuid,uuid,uuid,uuid,bigint)
FROM PUBLIC;
REVOKE ALL ON TABLE askdata.release_review_reports,askdata.release_approvals FROM PUBLIC;

COMMENT ON TABLE askdata.release_approvals IS
  'Append-only, hash-bound two-seat release approval audit; one reviewer can never occupy both duties';
COMMENT ON FUNCTION askdata.activate_release(uuid,uuid,uuid,uuid,bigint) IS
  'Atomically recomputes DB-007, checks fresh LLM review and two independent approvals, supersedes the prior ACTIVE release and advances release_state';
