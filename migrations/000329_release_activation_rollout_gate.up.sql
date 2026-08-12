-- Shadow/Canary becomes a first-class activation condition, decided by the
-- atomic activation procedure rather than by the application layer.
--
-- Two defects are fixed here.
--
-- 1. The rollout condition lived only in Go (registry.ActivateRelease), while
--    02 §12.5 and 06 §4.2 make askdata.activate_release the sole arbiter of
--    activation. Any other caller of the procedure could activate a candidate
--    that never carried traffic.
--
-- 2. The Go check required an ACCEPTED rollout unconditionally, but a rollout
--    cannot exist without a control: StartReleaseRollout reads
--    release_state.active_release_id and refuses when it is absent. A business
--    domain's FIRST release therefore could never be activated - it had no
--    control to canary against, so it could never obtain the rollout its own
--    activation demanded. Bootstrapping a domain was impossible.
--
-- The rule is now stated where it is true: a candidate that REPLACES a live
-- ACTIVE release must have carried real traffic against exactly that release;
-- the first release of a domain has no control, no traffic to split and no
-- comparison to make, so the evaluation gate and the two-person approval are
-- its complete set of controls.
BEGIN;

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
DECLARE selected_rollout askdata.release_rollouts%ROWTYPE;
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
  LEFT JOIN askdata.release_approval_withdrawals AS withdrawal
    ON withdrawal.tenant_id=approval.tenant_id AND withdrawal.approval_id=approval.id
  WHERE approval.tenant_id=selected_release.tenant_id
    AND approval.release_id=selected_release.id
    AND approval.gate_receipt_hash=selected_gate_hash
    AND approval.decision='APPROVED'
    AND withdrawal.id IS NULL
    AND approval.approved_at>selected_release.updated_at;
  IF approval_count<>2 OR approval_reviewer_count<>2 OR approval_role_count<>2 THEN
    RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
      selected_gate_hash,ARRAY['RELEASE_APPROVALS_REQUIRED']::text[];
    RETURN;
  END IF;

  -- Shadow/Canary is required only when this candidate would replace a live
  -- ACTIVE release, and the evidence must have been gathered against exactly
  -- that release: a rollout measured against a control that has since been
  -- replaced proves nothing about the comparison being made now.
  IF selected_state.active_release_id IS NOT NULL THEN
    SELECT * INTO selected_rollout FROM askdata.release_rollouts AS rollout
    WHERE rollout.tenant_id=selected_release.tenant_id
      AND rollout.domain_id=selected_release.domain_id
      AND rollout.candidate_release_id=selected_release.id
      AND rollout.control_release_id=selected_state.active_release_id
      AND rollout.stage='ACCEPTED_95'
      AND rollout.state='ACCEPTED'
    ORDER BY rollout.accepted_at DESC,rollout.id DESC LIMIT 1
    FOR UPDATE;
    IF selected_rollout.id IS NULL THEN
      RETURN QUERY SELECT false,selected_state.active_release_id,NULL::uuid,selected_state.version,
        selected_gate_hash,ARRAY['RELEASE_ROLLOUT_REQUIRED']::text[];
      RETURN;
    END IF;
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
      'releaseStateVersion',new_state_version,
      'rolloutId',selected_rollout.id,
      'rolloutRequired',selected_state.active_release_id IS NOT NULL
    )
  );
  -- The rollout is closed inside the same transaction that activates, so a
  -- COMPLETED rollout and an ACTIVE release can never disagree.
  IF selected_rollout.id IS NOT NULL THEN
    UPDATE askdata.release_rollouts SET state='COMPLETED',completed_at=clock_timestamp(),
      updated_at=clock_timestamp(),updated_by=selected_actor_id,version=version+1
    WHERE id=selected_rollout.id AND state='ACCEPTED';
    INSERT INTO askdata.release_rollout_events(
      tenant_id,domain_id,rollout_id,candidate_release_id,event_type,from_stage,to_stage,
      actor_id,reason_hash,detail
    ) VALUES(
      selected_rollout.tenant_id,selected_rollout.domain_id,selected_rollout.id,
      selected_release.id,'ACTIVATED','ACCEPTED_95','ACCEPTED_95',selected_actor_id,
      selected_rollout.reason_hash,
      jsonb_build_object('releaseStateVersion',new_state_version)
    );
  END IF;
  RETURN QUERY SELECT true,selected_release.id,prior_release_id,new_state_version,
    selected_gate_hash,'{}'::text[];
END
$$;

COMMENT ON FUNCTION askdata.activate_release(uuid,uuid,uuid,uuid,bigint) IS
  'Atomically recomputes DB-007, checks fresh LLM review, two independent approvals and - when replacing a live ACTIVE release - an ACCEPTED Shadow/Canary rollout measured against that same release, then supersedes the prior ACTIVE release, advances release_state and completes the rollout';

COMMIT;
