BEGIN;

CREATE TABLE platform.semantic_question_runs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  actor_id uuid NOT NULL,
  conversation_id uuid,
  parent_question_id uuid,
  question_hash text NOT NULL CHECK(question_hash ~ '^[0-9a-f]{64}$'),
  current_state text NOT NULL CHECK(current_state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','VALIDATING','PLAN_READY',
    'CLARIFICATION_REQUIRED','COST_APPROVED','EXECUTING',
    'RESULT_VERIFIED','ANSWERED','BLOCKED'
  )),
  route text CHECK(route IS NULL OR route IN (
    'SEMANTIC_IR','GOVERNED_TEXT_TO_SQL','CLARIFY_OR_REFUSE'
  )),
  decision text NOT NULL DEFAULT '',
  semantic_version text NOT NULL DEFAULT '',
  intent_hash text NOT NULL DEFAULT ''
    CHECK(intent_hash='' OR intent_hash ~ '^[0-9a-f]{64}$'),
  binding_bundle_hash text NOT NULL DEFAULT ''
    CHECK(binding_bundle_hash='' OR binding_bundle_hash ~ '^[0-9a-f]{64}$'),
  query_plan_hash text NOT NULL DEFAULT ''
    CHECK(query_plan_hash='' OR query_plan_hash ~ '^[0-9a-f]{64}$'),
  result_hash text NOT NULL DEFAULT ''
    CHECK(result_hash='' OR result_hash ~ '^[0-9a-f]{64}$'),
  query_plan_ids uuid[] NOT NULL DEFAULT '{}',
  failure_code text NOT NULL DEFAULT '' CHECK(length(failure_code)<=128),
  execution_budget jsonb NOT NULL DEFAULT '{}'
    CHECK(jsonb_typeof(execution_budget)='object'),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  CONSTRAINT semantic_question_runs_actor_fk
    FOREIGN KEY(actor_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_question_runs_identity_tenant_key UNIQUE(id,tenant_id),
  CONSTRAINT semantic_question_runs_parent_fk
    FOREIGN KEY(parent_question_id,tenant_id)
    REFERENCES platform.semantic_question_runs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_question_runs_completion_shape_check CHECK(
    (current_state IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED')
      AND completed_at IS NOT NULL)
    OR
    (current_state NOT IN ('CLARIFICATION_REQUIRED','ANSWERED','BLOCKED')
      AND completed_at IS NULL)
  )
);

CREATE TABLE platform.semantic_question_run_events(
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  question_run_id uuid NOT NULL,
  event_index integer NOT NULL CHECK(event_index>0),
  state text NOT NULL CHECK(state IN (
    'RECEIVED','AUTHORIZED','CONTEXT_READY','VALIDATING','PLAN_READY',
    'CLARIFICATION_REQUIRED','COST_APPROVED','EXECUTING',
    'RESULT_VERIFIED','ANSWERED','BLOCKED'
  )),
  stage text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT '',
  code text NOT NULL DEFAULT '' CHECK(length(code)<=128),
  duration_ms bigint CHECK(duration_ms IS NULL OR duration_ms>=0),
  summary jsonb NOT NULL DEFAULT '{}'
    CHECK(jsonb_typeof(summary)='object'),
  occurred_at timestamptz NOT NULL,
  PRIMARY KEY(tenant_id,question_run_id,event_index),
  CONSTRAINT semantic_question_run_events_run_fk
    FOREIGN KEY(question_run_id,tenant_id)
    REFERENCES platform.semantic_question_runs(id,tenant_id) ON DELETE CASCADE
);

CREATE INDEX semantic_question_runs_recent_idx
  ON platform.semantic_question_runs(tenant_id,created_at DESC,id);
CREATE INDEX semantic_question_runs_conversation_idx
  ON platform.semantic_question_runs(
    tenant_id,conversation_id,created_at DESC,id
  ) WHERE conversation_id IS NOT NULL;

ALTER TABLE platform.semantic_question_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_question_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_question_runs_tenant_isolation
  ON platform.semantic_question_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

ALTER TABLE platform.semantic_question_run_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_question_run_events FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_question_run_events_tenant_isolation
  ON platform.semantic_question_run_events
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.semantic_question_runs IS
  '统一 Question Orchestrator 运行状态；保存版本、计划和结果摘要，不保存原始问句、提示词、SQL 或结果行';
COMMENT ON TABLE platform.semantic_question_run_events IS
  '智能问答状态迁移账本；event_index 与运行 record_version 一致';

COMMIT;
