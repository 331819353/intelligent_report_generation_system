BEGIN;

CREATE TABLE platform.semantic_golden_question_sets(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  code text NOT NULL CHECK(code ~ '^[A-Za-z][A-Za-z0-9_]{0,127}$'),
  name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
  business_domain text NOT NULL CHECK(
    length(business_domain) BETWEEN 1 AND 128
    AND business_domain=btrim(business_domain)
  ),
  version bigint NOT NULL CHECK(version>=1),
  correctness_threshold numeric(5,4) NOT NULL DEFAULT 0.9500
    CHECK(correctness_threshold BETWEEN 0 AND 1),
  safety_threshold numeric(5,4) NOT NULL DEFAULT 1.0000
    CHECK(safety_threshold BETWEEN 0 AND 1),
  status text NOT NULL DEFAULT 'DRAFT'
    CHECK(status IN ('DRAFT','ACTIVE','RETIRED')),
  record_version bigint NOT NULL DEFAULT 1 CHECK(record_version>=1),
  created_by uuid NOT NULL,
  updated_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  activated_at timestamptz,
  CONSTRAINT semantic_golden_question_sets_code_version_key
    UNIQUE(tenant_id,code,version),
  CONSTRAINT semantic_golden_question_sets_identity_tenant_key
    UNIQUE(id,tenant_id)
);

CREATE UNIQUE INDEX semantic_golden_question_sets_active_code_idx
  ON platform.semantic_golden_question_sets(tenant_id,code)
  WHERE status='ACTIVE';

ALTER TABLE platform.semantic_golden_questions
  DROP CONSTRAINT semantic_golden_questions_hash_key;
ALTER TABLE platform.semantic_golden_questions ADD COLUMN set_id uuid;
ALTER TABLE platform.semantic_golden_questions
  ADD CONSTRAINT semantic_golden_questions_set_fk
  FOREIGN KEY(set_id,tenant_id)
  REFERENCES platform.semantic_golden_question_sets(id,tenant_id)
  ON DELETE RESTRICT;
ALTER TABLE platform.semantic_golden_questions
  ADD CONSTRAINT semantic_golden_questions_set_hash_key
  UNIQUE(tenant_id,set_id,question_hash);

CREATE TABLE platform.semantic_golden_question_runs(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  golden_question_id uuid NOT NULL,
  graph_generation_id uuid,
  query_plan_id uuid,
  status text NOT NULL CHECK(status IN ('PASSED','FAILED','ERROR')),
  expected_status text NOT NULL
    CHECK(expected_status IN ('READY','AMBIGUOUS','GAP','REJECTED')),
  actual_status text NOT NULL DEFAULT '' CHECK(
    actual_status='' OR actual_status IN (
      'READY','AMBIGUOUS','GAP','REJECTED','EXECUTED','FAILED'
    )
  ),
  expected_path_hash text NOT NULL
    CHECK(expected_path_hash ~ '^[0-9a-f]{64}$'),
  actual_path_hash text NOT NULL DEFAULT ''
    CHECK(actual_path_hash='' OR actual_path_hash ~ '^[0-9a-f]{64}$'),
  failure_stage text NOT NULL DEFAULT '' CHECK(
    failure_stage='' OR failure_stage IN (
      'RECALL','RELATIONSHIP','PLANNING','QUALITY','EXECUTION','EXPRESSION'
    )
  ),
  failure_code text NOT NULL DEFAULT '' CHECK(length(failure_code)<=128),
  executed_by uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT semantic_golden_question_runs_question_fk
    FOREIGN KEY(golden_question_id,tenant_id)
    REFERENCES platform.semantic_golden_questions(id,tenant_id)
    ON DELETE CASCADE,
  CONSTRAINT semantic_golden_question_runs_generation_fk
    FOREIGN KEY(graph_generation_id,tenant_id)
    REFERENCES platform.semantic_graph_generations(id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT semantic_golden_question_runs_plan_fk
    FOREIGN KEY(query_plan_id,tenant_id)
    REFERENCES platform.semantic_query_plans(id,tenant_id)
    ON DELETE RESTRICT,
  CONSTRAINT semantic_golden_question_runs_identity_tenant_key
    UNIQUE(id,tenant_id)
);

CREATE INDEX semantic_golden_question_runs_lookup_idx
  ON platform.semantic_golden_question_runs(
    tenant_id,golden_question_id,created_at DESC
  );

CREATE TRIGGER semantic_golden_question_sets_set_updated_at
BEFORE UPDATE ON platform.semantic_golden_question_sets
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.semantic_golden_question_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_golden_question_sets FORCE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_golden_question_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.semantic_golden_question_runs FORCE ROW LEVEL SECURITY;

CREATE POLICY semantic_golden_question_sets_tenant_isolation
  ON platform.semantic_golden_question_sets
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY semantic_golden_question_runs_tenant_isolation
  ON platform.semantic_golden_question_runs
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.semantic_golden_question_sets IS
  '按业务域和版本发布的黄金问题门禁；新图、Schema 或规划器发布前必须回放';
COMMENT ON TABLE platform.semantic_golden_question_runs IS
  '黄金问题的逐次图 generation 和 Query Plan 审计结果，不保存原始问题或业务结果';

COMMIT;
