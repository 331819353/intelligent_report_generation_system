-- NLU-008: durable conversation release pins and terminating clarification
-- waits. PostgreSQL remains authoritative for pin changes, inherited budgets,
-- timeout state, actor/domain isolation and append-only replay.

CREATE TABLE askdata.conversations(
  id uuid NOT NULL,
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE RESTRICT,
  domain_id uuid NOT NULL,
  actor_id uuid NOT NULL,
  pinned_release_id uuid,
  pinned_at timestamptz,
  pin_drift_acknowledged boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(tenant_id,id),
  CONSTRAINT askdata_conversations_identity_domain_key UNIQUE(id,domain_id,tenant_id),
  CONSTRAINT askdata_conversations_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES askdata.domains(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_conversations_actor_fk FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_conversations_release_fk FOREIGN KEY(pinned_release_id,domain_id,tenant_id)
    REFERENCES askdata.releases(id,domain_id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT askdata_conversations_pin_shape_check CHECK(
    (pinned_release_id IS NULL AND pinned_at IS NULL AND NOT pin_drift_acknowledged)
    OR (pinned_release_id IS NOT NULL AND pinned_at IS NOT NULL)
  )
);

CREATE INDEX askdata_conversations_actor_recent_idx
  ON askdata.conversations(tenant_id,domain_id,actor_id,updated_at DESC,id);
CREATE INDEX askdata_conversations_release_idx
  ON askdata.conversations(tenant_id,domain_id,pinned_release_id)
  WHERE pinned_release_id IS NOT NULL;

INSERT INTO askdata.conversations(id,tenant_id,domain_id,actor_id,created_at,updated_at)
SELECT run.conversation_id,run.tenant_id,run.domain_id,run.actor_id,
       min(run.created_at),max(run.updated_at)
FROM askdata.question_runs AS run
WHERE run.conversation_id IS NOT NULL
GROUP BY run.conversation_id,run.tenant_id,run.domain_id,run.actor_id
ON CONFLICT(tenant_id,id) DO NOTHING;

WITH latest_bound AS (
  SELECT DISTINCT ON (tenant_id,conversation_id)
    tenant_id,conversation_id,release_id,updated_at
  FROM askdata.question_runs
  WHERE conversation_id IS NOT NULL AND binding_bundle_hash IS NOT NULL
  ORDER BY tenant_id,conversation_id,created_at DESC,id DESC
)
UPDATE askdata.conversations AS conversation SET
  pinned_release_id=latest_bound.release_id,
  pinned_at=latest_bound.updated_at,
  updated_at=GREATEST(conversation.updated_at,latest_bound.updated_at)
FROM latest_bound
WHERE conversation.tenant_id=latest_bound.tenant_id
  AND conversation.id=latest_bound.conversation_id;

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
      WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
        AND id=NEW.pinned_release_id FOR SHARE;
      IF new_status<>'ACTIVE' OR NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'initial conversation pin requires the current ACTIVE release'
          USING ERRCODE='23514';
      END IF;
      NEW.pinned_at=COALESCE(NEW.pinned_at,clock_timestamp());
    END IF;
    NEW.created_at=clock_timestamp();
    NEW.updated_at=NEW.created_at;
    RETURN NEW;
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'conversation identity is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.pinned_release_id IS DISTINCT FROM OLD.pinned_release_id THEN
    IF NEW.pinned_release_id IS NULL THEN
      RAISE EXCEPTION 'conversation release pin cannot be cleared' USING ERRCODE='55000';
    END IF;
    SELECT status INTO new_status FROM askdata.releases
    WHERE tenant_id=NEW.tenant_id AND domain_id=NEW.domain_id
      AND id=NEW.pinned_release_id FOR SHARE;
    IF new_status<>'ACTIVE' THEN
      RAISE EXCEPTION 'conversation can only pin the current ACTIVE release'
        USING ERRCODE='23514';
    END IF;
    IF OLD.pinned_release_id IS NULL THEN
      IF NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'first successful binding is not a drift acknowledgement'
          USING ERRCODE='23514';
      END IF;
    ELSE
      SELECT status INTO old_status FROM askdata.releases
      WHERE tenant_id=OLD.tenant_id AND domain_id=OLD.domain_id
        AND id=OLD.pinned_release_id FOR SHARE;
      IF old_status NOT IN ('SUPERSEDED','RETAINED','RETIRED')
        OR NOT NEW.pin_drift_acknowledged THEN
        RAISE EXCEPTION 'release drift switch requires an acknowledged stale pin'
          USING ERRCODE='23514';
      END IF;
    END IF;
    NEW.pinned_at=clock_timestamp();
  ELSIF NEW.pin_drift_acknowledged IS DISTINCT FROM OLD.pin_drift_acknowledged THEN
    RAISE EXCEPTION 'drift acknowledgement can only change with the release pin'
      USING ERRCODE='55000';
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION askdata.enforce_conversation_release_pin() FROM PUBLIC;
CREATE TRIGGER askdata_conversations_release_pin
BEFORE INSERT OR UPDATE OR DELETE ON askdata.conversations
FOR EACH ROW EXECUTE FUNCTION askdata.enforce_conversation_release_pin();

ALTER TABLE askdata.conversations ENABLE ROW LEVEL SECURITY;
ALTER TABLE askdata.conversations FORCE ROW LEVEL SECURITY;
CREATE POLICY askdata_conversations_actor_domain_isolation
ON askdata.conversations
USING(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id))
WITH CHECK(askdata.question_runtime_can_access(tenant_id,domain_id,actor_id));

ALTER TABLE askdata.question_runs
  ADD COLUMN clarification_deadline timestamptz,
  ADD COLUMN budget_frozen_at timestamptz,
  ADD COLUMN budget_consumed_json jsonb;

UPDATE askdata.question_runs SET
  budget_frozen_at=completed_at,
  clarification_deadline=completed_at+interval '30 minutes',
  budget_consumed_json=jsonb_build_object(
    'stepCount',step_count,
    'llmCallsUsed',llm_calls_used,
    'toolCallsUsed',tool_calls_used,
    'formalQueriesUsed',formal_queries_used,
    'validationQueriesUsed',validation_queries_used,
    'elapsedMs',elapsed_ms,
    'exhausted',budget_exhausted
  )
WHERE current_state='CLARIFICATION_REQUIRED';

ALTER TABLE askdata.question_runs
  DROP CONSTRAINT question_runs_current_state_check,
  DROP CONSTRAINT askdata_question_runs_budget_terminal_check,
  DROP CONSTRAINT askdata_question_runs_completion_shape_check,
  ADD CONSTRAINT askdata_question_runs_state_check CHECK(current_state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED'
  )),
  ADD CONSTRAINT askdata_question_runs_budget_terminal_check CHECK(
    NOT budget_exhausted OR current_state IN (
      'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED'
    )
  ),
  ADD CONSTRAINT askdata_question_runs_clarification_budget_check CHECK(
    (
      current_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND clarification_deadline IS NOT NULL AND budget_frozen_at IS NOT NULL
      AND clarification_deadline>budget_frozen_at
      AND jsonb_typeof(budget_consumed_json)='object'
      AND pg_column_size(budget_consumed_json)<=16384
      AND askdata.question_audit_json_is_safe(budget_consumed_json)
      AND budget_consumed_json=jsonb_build_object(
        'stepCount',step_count,
        'llmCallsUsed',llm_calls_used,
        'toolCallsUsed',tool_calls_used,
        'formalQueriesUsed',formal_queries_used,
        'validationQueriesUsed',validation_queries_used,
        'elapsedMs',elapsed_ms,
        'exhausted',budget_exhausted
      )
    ) OR (
      current_state NOT IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND clarification_deadline IS NULL AND budget_frozen_at IS NULL
      AND budget_consumed_json IS NULL
    )
  ),
  ADD CONSTRAINT askdata_question_runs_completion_shape_check CHECK(
    (
      current_state NOT IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED')
      AND disposition='PENDING' AND completion_code=''
      AND completion_artifact_hash IS NULL AND completed_at IS NULL
    ) OR (
      current_state='ANSWERED' AND disposition='DIRECT'
      AND completion_code<>'' AND completion_artifact_hash IS NOT NULL
      AND completed_at IS NOT NULL AND understanding_hash IS NOT NULL
      AND binding_bundle_hash IS NOT NULL AND graph_plan_hash IS NOT NULL
      AND semantic_ir_hash IS NOT NULL AND query_plan_hash IS NOT NULL
      AND result_hash IS NOT NULL
    ) OR (
      current_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED')
      AND disposition='CLARIFY' AND completion_code<>''
      AND completion_artifact_hash IS NOT NULL AND completed_at IS NOT NULL
    ) OR (
      current_state='BLOCKED' AND disposition='REFUSE'
      AND completion_code<>'' AND completion_artifact_hash IS NOT NULL
      AND completed_at IS NOT NULL
    )
  );

ALTER TABLE askdata.question_run_events
  DROP CONSTRAINT question_run_events_state_check,
  ADD CONSTRAINT askdata_question_run_events_state_check CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED'
  ));

ALTER TABLE askdata.question_run_events
  DROP CONSTRAINT askdata_question_run_events_type_shape_check,
  ADD CONSTRAINT askdata_question_run_events_type_shape_check CHECK(
    (
      event_type='STATE_TRANSITION' AND tool_call_id=''
      AND ((ai_request_id IS NULL AND action_hash IS NULL)
        OR (ai_request_id IS NOT NULL AND action_hash IS NOT NULL))
      AND (artifact_hash IS NULL OR state IN (
        'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED'
      ))
    ) OR (
      event_type='LLM_DECISION' AND tool_call_id=''
      AND ai_request_id IS NOT NULL AND action_hash IS NOT NULL AND artifact_hash IS NULL
    ) OR (
      event_type='TOOL_RESULT' AND tool_call_id<>''
      AND ai_request_id IS NULL AND action_hash IS NULL AND artifact_hash IS NULL
    ) OR (
      event_type='ARTIFACT_RECORDED' AND tool_call_id=''
      AND ai_request_id IS NULL AND action_hash IS NULL AND artifact_hash IS NOT NULL
    ) OR (
      event_type IN ('BUDGET_UPDATED','CORRECTION','ERROR','PROGRESS')
      AND tool_call_id='' AND artifact_hash IS NULL
    )
  );

ALTER TABLE askdata.tool_calls
  DROP CONSTRAINT tool_calls_state_check,
  ADD CONSTRAINT askdata_tool_calls_state_check CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','UNDERSTANDING','RETRIEVING',
    'BINDING','GRAPH_VALIDATING','IR_READY','PLAN_VALIDATING','EXECUTING',
    'RESULT_VERIFYING','CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED'
  ));

CREATE OR REPLACE FUNCTION askdata.valid_question_run_transition(previous_state text,next_state text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
STRICT
AS $$
  SELECT (previous_state='CLARIFICATION_REQUIRED' AND next_state='CLARIFICATION_EXPIRED')
    OR (previous_state NOT IN (
      'CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED'
    ) AND (previous_state=next_state OR CASE previous_state
      WHEN 'RECEIVED' THEN next_state IN ('AUTHORIZED','BLOCKED')
      WHEN 'AUTHORIZED' THEN next_state IN ('CONTEXT_READY','BLOCKED')
      WHEN 'CONTEXT_READY' THEN next_state IN ('UNDERSTANDING','BLOCKED')
      WHEN 'UNDERSTANDING' THEN next_state IN ('RETRIEVING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'RETRIEVING' THEN next_state IN ('BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'BINDING' THEN next_state IN ('GRAPH_VALIDATING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'GRAPH_VALIDATING' THEN next_state IN ('IR_READY','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'IR_READY' THEN next_state IN ('PLAN_VALIDATING','BLOCKED')
      WHEN 'PLAN_VALIDATING' THEN next_state IN ('EXECUTING','BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      WHEN 'EXECUTING' THEN next_state IN ('RESULT_VERIFYING','BLOCKED')
      WHEN 'RESULT_VERIFYING' THEN next_state IN ('ANSWERED','BINDING','CLARIFICATION_REQUIRED','BLOCKED')
      ELSE false
    END))
$$;

CREATE OR REPLACE FUNCTION askdata.enforce_question_run_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE release_valid boolean := false;
DECLARE expected_artifact_type text;
DECLARE actual_artifact_type text;
DECLARE semantic_correction boolean := false;
DECLARE parent_run askdata.question_runs%ROWTYPE;
BEGIN
  IF TG_OP='DELETE' THEN
    RAISE EXCEPTION 'question run audit cannot be deleted' USING ERRCODE='55000';
  END IF;
  IF TG_OP='INSERT' THEN
    release_valid := askdata.lock_active_question_release(
      NEW.tenant_id,NEW.domain_id,NEW.release_id,NEW.release_content_hash
    );
    IF NOT release_valid THEN
      RAISE EXCEPTION 'question run requires the matching ACTIVE release' USING ERRCODE='23514';
    END IF;
    IF NEW.current_state<>'RECEIVED' OR NEW.record_version<>1 OR NEW.budget_exhausted
      OR NEW.understanding_hash IS NOT NULL OR NEW.binding_bundle_hash IS NOT NULL
      OR NEW.graph_plan_hash IS NOT NULL OR NEW.semantic_ir_hash IS NOT NULL
      OR NEW.query_plan_hash IS NOT NULL OR NEW.result_hash IS NOT NULL
      OR NEW.clarification_deadline IS NOT NULL OR NEW.budget_frozen_at IS NOT NULL
      OR NEW.budget_consumed_json IS NOT NULL THEN
      RAISE EXCEPTION 'question run initial shape is invalid' USING ERRCODE='23514';
    END IF;
    IF NEW.parent_run_id IS NULL THEN
      IF NEW.step_count<>0 OR NEW.llm_calls_used<>0 OR NEW.tool_calls_used<>0
        OR NEW.formal_queries_used<>0 OR NEW.validation_queries_used<>0 OR NEW.elapsed_ms<>0 THEN
        RAISE EXCEPTION 'new conversation run must start with an empty budget' USING ERRCODE='23514';
      END IF;
    ELSE
      SELECT * INTO parent_run FROM askdata.question_runs
      WHERE id=NEW.parent_run_id AND tenant_id=NEW.tenant_id
        AND domain_id=NEW.domain_id AND actor_id=NEW.actor_id FOR SHARE;
      IF parent_run.id IS NULL OR parent_run.current_state<>'CLARIFICATION_REQUIRED'
        OR parent_run.conversation_id IS DISTINCT FROM NEW.conversation_id
        OR parent_run.budget_consumed_json IS NULL
        OR parent_run.budget_consumed_json<>jsonb_build_object(
          'stepCount',NEW.step_count,'llmCallsUsed',NEW.llm_calls_used,
          'toolCallsUsed',NEW.tool_calls_used,'formalQueriesUsed',NEW.formal_queries_used,
          'validationQueriesUsed',NEW.validation_queries_used,'elapsedMs',NEW.elapsed_ms,
          'exhausted',false
        ) THEN
        RAISE EXCEPTION 'clarification child must resume the frozen parent budget'
          USING ERRCODE='23514';
      END IF;
    END IF;
    NEW.created_at=clock_timestamp();
    NEW.updated_at=NEW.created_at;
    RETURN NEW;
  END IF;
  IF OLD.current_state IN ('CLARIFICATION_EXPIRED','ANSWERED','BLOCKED')
    OR (OLD.current_state='CLARIFICATION_REQUIRED' AND NEW.current_state<>'CLARIFICATION_EXPIRED') THEN
    RAISE EXCEPTION 'question run terminal state is immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
    OR NEW.domain_id IS DISTINCT FROM OLD.domain_id OR NEW.actor_id IS DISTINCT FROM OLD.actor_id
    OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
    OR NEW.parent_run_id IS DISTINCT FROM OLD.parent_run_id OR NEW.trace_id IS DISTINCT FROM OLD.trace_id
    OR NEW.idempotency_key_hash IS DISTINCT FROM OLD.idempotency_key_hash
    OR NEW.question_hash IS DISTINCT FROM OLD.question_hash
    OR NEW.policy_scope_hash IS DISTINCT FROM OLD.policy_scope_hash
    OR NEW.release_id IS DISTINCT FROM OLD.release_id
    OR NEW.release_content_hash IS DISTINCT FROM OLD.release_content_hash
    OR NEW.max_steps IS DISTINCT FROM OLD.max_steps OR NEW.max_llm_calls IS DISTINCT FROM OLD.max_llm_calls
    OR NEW.max_tool_calls IS DISTINCT FROM OLD.max_tool_calls
    OR NEW.max_formal_queries IS DISTINCT FROM OLD.max_formal_queries
    OR NEW.max_validation_queries IS DISTINCT FROM OLD.max_validation_queries
    OR NEW.max_duration_ms IS DISTINCT FROM OLD.max_duration_ms
    OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'question run identity, release pin and budget are immutable' USING ERRCODE='55000';
  END IF;
  IF NEW.record_version<>OLD.record_version+1 THEN
    RAISE EXCEPTION 'question run record_version must increase by exactly one' USING ERRCODE='40001';
  END IF;
  IF NOT askdata.valid_question_run_transition(OLD.current_state,NEW.current_state) THEN
    RAISE EXCEPTION 'illegal question run state transition' USING ERRCODE='23514';
  END IF;
  IF NEW.step_count<OLD.step_count OR NEW.llm_calls_used<OLD.llm_calls_used
    OR NEW.tool_calls_used<OLD.tool_calls_used OR NEW.formal_queries_used<OLD.formal_queries_used
    OR NEW.validation_queries_used<OLD.validation_queries_used OR NEW.elapsed_ms<OLD.elapsed_ms
    OR (OLD.budget_exhausted AND NOT NEW.budget_exhausted) THEN
    RAISE EXCEPTION 'question run budget usage cannot decrease' USING ERRCODE='23514';
  END IF;
  semantic_correction := NEW.current_state='BINDING'
    AND OLD.current_state IN ('PLAN_VALIDATING','RESULT_VERIFYING');
  IF OLD.understanding_hash IS NOT NULL AND NEW.understanding_hash IS DISTINCT FROM OLD.understanding_hash THEN
    RAISE EXCEPTION 'understanding hash cannot be cleared or overwritten' USING ERRCODE='23514';
  END IF;
  IF NOT semantic_correction AND (
    (OLD.binding_bundle_hash IS NOT NULL AND NEW.binding_bundle_hash IS DISTINCT FROM OLD.binding_bundle_hash)
    OR (OLD.graph_plan_hash IS NOT NULL AND NEW.graph_plan_hash IS DISTINCT FROM OLD.graph_plan_hash)
    OR (OLD.semantic_ir_hash IS NOT NULL AND NEW.semantic_ir_hash IS DISTINCT FROM OLD.semantic_ir_hash)
    OR (OLD.query_plan_hash IS NOT NULL AND NEW.query_plan_hash IS DISTINCT FROM OLD.query_plan_hash)
    OR (OLD.result_hash IS NOT NULL AND NEW.result_hash IS DISTINCT FROM OLD.result_hash)
  ) THEN
    RAISE EXCEPTION 'governed run hashes cannot be cleared or overwritten' USING ERRCODE='23514';
  END IF;
  IF semantic_correction AND (
    NEW.binding_bundle_hash IS NOT NULL OR NEW.graph_plan_hash IS NOT NULL
    OR NEW.semantic_ir_hash IS NOT NULL OR NEW.query_plan_hash IS NOT NULL
    OR NEW.result_hash IS NOT NULL
  ) THEN
    RAISE EXCEPTION 'semantic correction must clear stale downstream hashes' USING ERRCODE='23514';
  END IF;
  IF OLD.understanding_hash IS NULL AND NEW.understanding_hash IS NOT NULL
    AND OLD.current_state<>'UNDERSTANDING' AND NEW.current_state<>'UNDERSTANDING' THEN
    RAISE EXCEPTION 'understanding hash appeared outside UNDERSTANDING' USING ERRCODE='23514';
  END IF;
  IF OLD.binding_bundle_hash IS NULL AND NEW.binding_bundle_hash IS NOT NULL
    AND OLD.current_state<>'BINDING' AND NEW.current_state<>'BINDING' THEN
    RAISE EXCEPTION 'binding hash appeared outside BINDING' USING ERRCODE='23514';
  END IF;
  IF OLD.graph_plan_hash IS NULL AND NEW.graph_plan_hash IS NOT NULL
    AND OLD.current_state<>'GRAPH_VALIDATING' AND NEW.current_state<>'GRAPH_VALIDATING' THEN
    RAISE EXCEPTION 'graph plan hash appeared outside GRAPH_VALIDATING' USING ERRCODE='23514';
  END IF;
  IF OLD.semantic_ir_hash IS NULL AND NEW.semantic_ir_hash IS NOT NULL
    AND OLD.current_state<>'IR_READY' AND NEW.current_state<>'IR_READY' THEN
    RAISE EXCEPTION 'semantic IR hash appeared outside IR_READY' USING ERRCODE='23514';
  END IF;
  IF OLD.query_plan_hash IS NULL AND NEW.query_plan_hash IS NOT NULL
    AND OLD.current_state<>'PLAN_VALIDATING' AND NEW.current_state<>'PLAN_VALIDATING' THEN
    RAISE EXCEPTION 'query plan hash appeared outside PLAN_VALIDATING' USING ERRCODE='23514';
  END IF;
  IF OLD.result_hash IS NULL AND NEW.result_hash IS NOT NULL
    AND OLD.current_state<>'RESULT_VERIFYING' AND NEW.current_state<>'RESULT_VERIFYING' THEN
    RAISE EXCEPTION 'result hash appeared outside RESULT_VERIFYING' USING ERRCODE='23514';
  END IF;
  IF (NEW.binding_bundle_hash IS NOT NULL AND NEW.understanding_hash IS NULL)
    OR (NEW.graph_plan_hash IS NOT NULL AND NEW.binding_bundle_hash IS NULL)
    OR (NEW.semantic_ir_hash IS NOT NULL AND NEW.graph_plan_hash IS NULL)
    OR (NEW.query_plan_hash IS NOT NULL AND NEW.semantic_ir_hash IS NULL)
    OR (NEW.result_hash IS NOT NULL AND NEW.query_plan_hash IS NULL) THEN
    RAISE EXCEPTION 'run hashes must form a contiguous governed chain' USING ERRCODE='23514';
  END IF;
  IF NEW.current_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED') THEN
    expected_artifact_type := CASE
      WHEN NEW.current_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED') THEN 'CLARIFICATION'
      WHEN NEW.current_state='ANSWERED' THEN 'ANSWER' ELSE 'BLOCK' END;
    SELECT artifact_type INTO actual_artifact_type FROM askdata.question_artifacts
    WHERE tenant_id=NEW.tenant_id AND question_run_id=NEW.id
      AND artifact_hash=NEW.completion_artifact_hash;
    IF actual_artifact_type IS DISTINCT FROM expected_artifact_type THEN
      RAISE EXCEPTION 'terminal state requires a matching completion artifact' USING ERRCODE='23514';
    END IF;
    IF NEW.current_state='CLARIFICATION_EXPIRED' THEN
      IF NEW.completion_code<>'CLARIFICATION_EXPIRED'
        OR NEW.clarification_deadline IS DISTINCT FROM OLD.clarification_deadline
        OR NEW.budget_frozen_at IS DISTINCT FROM OLD.budget_frozen_at
        OR NEW.budget_consumed_json IS DISTINCT FROM OLD.budget_consumed_json THEN
        RAISE EXCEPTION 'expired clarification must preserve its frozen contract' USING ERRCODE='23514';
      END IF;
      NEW.completed_at=OLD.completed_at;
    ELSE
      NEW.completed_at=clock_timestamp();
    END IF;
  ELSE
    NEW.completed_at=NULL;
  END IF;
  NEW.updated_at=clock_timestamp();
  RETURN NEW;
END
$$;

CREATE OR REPLACE FUNCTION askdata.stamp_question_runtime_fact()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,askdata
AS $$
DECLARE current_run_version bigint;
DECLARE current_run_state text;
DECLARE current_completion_artifact_hash text;
DECLARE current_completion_code text;
BEGIN
  SELECT record_version,current_state,completion_artifact_hash,completion_code
    INTO current_run_version,current_run_state,current_completion_artifact_hash,current_completion_code
  FROM askdata.question_runs
  WHERE id=NEW.question_run_id AND actor_id=NEW.actor_id
    AND release_id=NEW.release_id AND release_content_hash=NEW.release_content_hash
    AND policy_scope_hash=NEW.policy_scope_hash AND domain_id=NEW.domain_id
    AND tenant_id=NEW.tenant_id FOR SHARE;
  IF current_run_version IS NULL OR NEW.run_version<>current_run_version THEN
    RAISE EXCEPTION 'question runtime fact must bind the current run version' USING ERRCODE='23514';
  END IF;
  IF TG_TABLE_NAME IN ('question_run_events','tool_calls') THEN
    IF NEW.state<>current_run_state THEN
      RAISE EXCEPTION 'question runtime fact state must match the current run state' USING ERRCODE='23514';
    END IF;
  END IF;
  IF current_run_state IN ('CLARIFICATION_REQUIRED','CLARIFICATION_EXPIRED','ANSWERED','BLOCKED') THEN
    IF TG_TABLE_NAME<>'question_run_events' THEN
      RAISE EXCEPTION 'terminal question run accepts no additional facts' USING ERRCODE='55000';
    END IF;
    IF NEW.event_type<>'STATE_TRANSITION' OR NEW.stage<>current_run_state
      OR NEW.code<>current_completion_code
      OR NEW.artifact_hash IS DISTINCT FROM current_completion_artifact_hash
      OR NEW.status<>(CASE WHEN current_run_state='ANSWERED' THEN 'SUCCEEDED' ELSE 'BLOCKED' END)
      OR EXISTS(SELECT 1 FROM askdata.question_run_events
        WHERE tenant_id=NEW.tenant_id AND question_run_id=NEW.question_run_id
          AND run_version=current_run_version) THEN
      RAISE EXCEPTION 'terminal question run accepts only its unique completion event' USING ERRCODE='55000';
    END IF;
  END IF;
  IF TG_TABLE_NAME='question_run_events' THEN
    IF NEW.event_type='TOOL_RESULT' AND NOT EXISTS(
      SELECT 1 FROM askdata.tool_calls AS call
      WHERE call.tenant_id=NEW.tenant_id AND call.question_run_id=NEW.question_run_id
        AND call.tool_call_id=NEW.tool_call_id AND call.run_version=NEW.run_version
        AND call.state=NEW.state AND call.status=NEW.status AND call.actor_id=NEW.actor_id
        AND call.release_id=NEW.release_id AND call.release_content_hash=NEW.release_content_hash
        AND call.policy_scope_hash=NEW.policy_scope_hash AND call.domain_id=NEW.domain_id
    ) THEN
      RAISE EXCEPTION 'tool result event requires its exact tool outcome' USING ERRCODE='23514';
    END IF;
    IF NEW.event_type='ARTIFACT_RECORDED' AND NOT EXISTS(
      SELECT 1 FROM askdata.question_artifacts AS artifact
      WHERE artifact.tenant_id=NEW.tenant_id AND artifact.question_run_id=NEW.question_run_id
        AND artifact.artifact_hash=NEW.artifact_hash AND artifact.run_version=NEW.run_version
        AND artifact.actor_id=NEW.actor_id AND artifact.release_id=NEW.release_id
        AND artifact.release_content_hash=NEW.release_content_hash
        AND artifact.policy_scope_hash=NEW.policy_scope_hash AND artifact.domain_id=NEW.domain_id
    ) THEN
      RAISE EXCEPTION 'artifact event requires its exact persisted artifact' USING ERRCODE='23514';
    END IF;
    IF NEW.ai_request_id IS NOT NULL AND NOT EXISTS(
      SELECT 1 FROM platform.ai_requests AS request
      WHERE request.id=NEW.ai_request_id AND request.tenant_id=NEW.tenant_id
        AND request.actor_user_id=NEW.actor_id AND request.purpose='SEMANTIC_QUESTION'
    ) THEN
      RAISE EXCEPTION 'question event AI request must match actor and purpose' USING ERRCODE='23514';
    END IF;
  END IF;
  NEW.created_at=clock_timestamp();
  RETURN NEW;
END
$$;

REVOKE ALL ON FUNCTION
  askdata.valid_question_run_transition(text,text),
  askdata.enforce_question_run_lifecycle(),
  askdata.stamp_question_runtime_fact()
FROM PUBLIC;

COMMENT ON TABLE askdata.conversations IS
  'Actor/domain-scoped AskData conversation with a release pin written only after successful binding';
COMMENT ON COLUMN askdata.question_runs.clarification_deadline IS
  'Hard user-response deadline; runtime reads and the worker both terminate overdue clarification waits';
COMMENT ON COLUMN askdata.question_runs.budget_consumed_json IS
  'Exact governed usage snapshot resumed by the clarification child without charging waiting time';
