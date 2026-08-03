-- Question Orchestrator pins every governed run to the active release and
-- persists only sanitized, hash-addressed intermediate contracts for replay.
ALTER TABLE platform.semantic_question_runs
  ADD COLUMN semantic_release_id uuid,
  ADD COLUMN semantic_content_hash text NOT NULL DEFAULT '' CHECK(
    semantic_content_hash='' OR semantic_content_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD COLUMN understanding_hash text NOT NULL DEFAULT '' CHECK(
    understanding_hash='' OR understanding_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD COLUMN graph_plan_hash text NOT NULL DEFAULT '' CHECK(
    graph_plan_hash='' OR graph_plan_hash ~ '^[0-9a-f]{64}$'
  ),
  ADD CONSTRAINT semantic_question_runs_release_fk
    FOREIGN KEY(semantic_release_id,tenant_id)
    REFERENCES platform.semantic_releases(id,tenant_id) ON DELETE RESTRICT,
  ADD CONSTRAINT semantic_question_runs_release_shape_check CHECK(
    (semantic_release_id IS NULL AND semantic_content_hash='')
    OR (semantic_release_id IS NOT NULL AND semantic_version<>''
      AND semantic_content_hash<>'')
  );

CREATE INDEX semantic_question_runs_release_idx
  ON platform.semantic_question_runs(
    tenant_id,semantic_release_id,semantic_version,created_at DESC,id
  ) WHERE semantic_release_id IS NOT NULL;

CREATE TABLE platform.semantic_question_artifacts(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id) ON DELETE CASCADE,
  question_run_id uuid NOT NULL,
  artifact_type text NOT NULL CHECK(artifact_type IN (
    'UNDERSTANDING','GRAPH_PLAN','SEMANTIC_IR'
  )),
  artifact_hash text NOT NULL CHECK(artifact_hash ~ '^[0-9a-f]{64}$'),
  payload jsonb NOT NULL CHECK(
    jsonb_typeof(payload)='object'
    AND pg_column_size(payload)<=262144
    AND platform.materialization_json_is_safe(payload)
  ),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_question_artifacts_run_fk
    FOREIGN KEY(question_run_id,tenant_id)
    REFERENCES platform.semantic_question_runs(id,tenant_id) ON DELETE CASCADE,
  CONSTRAINT semantic_question_artifacts_type_key
    UNIQUE(tenant_id,question_run_id,artifact_type)
);

CREATE INDEX semantic_question_artifacts_hash_idx
  ON platform.semantic_question_artifacts(tenant_id,artifact_hash);
CREATE TRIGGER semantic_question_artifacts_set_updated_at
BEFORE UPDATE ON platform.semantic_question_artifacts
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.semantic_question_artifacts ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_question_artifacts FORCE ROW LEVEL SECURITY;
CREATE POLICY semantic_question_artifacts_tenant_isolation
  ON platform.semantic_question_artifacts
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.semantic_question_artifacts IS
  '问答可重放中间合同；理解产物删除原始问句和命中文本，只保留对齐、稳定对象 ID、图计划及 Semantic IR';
