-- Semantic execution Tool Host audit. The online API may append sanitized
-- call facts, but nobody may rewrite a completed call.
CREATE TABLE platform.semantic_tool_calls(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  question_run_id uuid NOT NULL,
  actor_user_id uuid NOT NULL,
  semantic_release_id uuid NOT NULL,
  semantic_version text NOT NULL CHECK(length(semantic_version) BETWEEN 1 AND 128),
  semantic_content_hash text NOT NULL CHECK(semantic_content_hash ~ '^[0-9a-f]{64}$'),
  tool_call_id text NOT NULL CHECK(
    length(tool_call_id) BETWEEN 1 AND 128
    AND tool_call_id=btrim(tool_call_id)
    AND tool_call_id !~ '[[:cntrl:]]'
  ),
  tool_name text NOT NULL CHECK(tool_name IN (
    'search_semantic_objects','get_semantic_contracts',
    'lookup_dimension_values','get_certified_examples',
    'validate_semantic_bundle','get_data_quality_status',
    'compile_semantic_query','validate_query_plan','explain_query_plan',
    'probe_join_cardinality','execute_query_plan',
    'execute_validation_query','compare_candidate_results',
    'request_clarification'
  )),
  state text NOT NULL CHECK(length(state) BETWEEN 1 AND 64),
  status text NOT NULL CHECK(status IN ('SUCCEEDED','BLOCKED','FAILED')),
  request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
  policy_scope_hash text NOT NULL CHECK(policy_scope_hash ~ '^[0-9a-f]{64}$'),
  result_hash text NOT NULL CHECK(result_hash ~ '^[0-9a-f]{64}$'),
  evidence_ids text[] NOT NULL DEFAULT '{}',
  budget_json jsonb NOT NULL CHECK(
    jsonb_typeof(budget_json)='object'
    AND pg_column_size(budget_json)<=16384
    AND platform.materialization_json_is_safe(budget_json)
  ),
  duration_ms bigint NOT NULL CHECK(duration_ms>=0),
  error_code text NOT NULL DEFAULT '' CHECK(
    length(error_code)<=128 AND error_code !~ '[[:cntrl:]]'
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_tool_calls_release_fk
    FOREIGN KEY(semantic_release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_tool_calls_actor_fk
    FOREIGN KEY(actor_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT semantic_tool_calls_question_fk
    FOREIGN KEY(question_run_id,tenant_id)
    REFERENCES platform.semantic_question_runs(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_tool_calls_tenant_identity_key UNIQUE(tenant_id,id),
  CONSTRAINT semantic_tool_calls_question_call_key UNIQUE(
    tenant_id,question_run_id,tool_call_id
  ),
  CONSTRAINT semantic_tool_calls_evidence_limit CHECK(
    cardinality(evidence_ids)<=256
  )
);

CREATE INDEX semantic_tool_calls_question_time_idx
  ON platform.semantic_tool_calls(tenant_id,question_run_id,created_at,id);
CREATE INDEX semantic_tool_calls_release_tool_idx
  ON platform.semantic_tool_calls(
    tenant_id,semantic_release_id,tool_name,status,created_at DESC
  );

ALTER TABLE platform.semantic_tool_calls ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_tool_calls FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_tool_calls_tenant_isolation
  ON platform.semantic_tool_calls
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

CREATE OR REPLACE FUNCTION platform.reject_semantic_tool_call_mutation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION '语义 Tool Host 调用事实不可修改或删除';
END
$$;
REVOKE ALL ON FUNCTION platform.reject_semantic_tool_call_mutation() FROM PUBLIC;

CREATE TRIGGER semantic_tool_calls_immutable
BEFORE UPDATE OR DELETE ON platform.semantic_tool_calls
FOR EACH ROW EXECUTE FUNCTION platform.reject_semantic_tool_call_mutation();

COMMENT ON TABLE platform.semantic_tool_calls IS
  'Go Tool Host 追加式脱敏审计；仅保存参数/结果哈希、证据、版本、策略范围和预算，不保存 SQL、参数明文、结果行或模型思维链';
