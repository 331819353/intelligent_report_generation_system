BEGIN;

ALTER TABLE platform.semantic_golden_question_sets
  ADD COLUMN dataset_split text NOT NULL DEFAULT 'DEVELOPMENT'
    CHECK(dataset_split IN (
      'DEVELOPMENT','VALIDATION','SEALED','PRODUCTION_REGRESSION'
    )),
  ADD COLUMN evaluation_mode text NOT NULL DEFAULT 'FIXTURE_REGRESSION'
    CHECK(evaluation_mode IN (
      'FIXTURE_REGRESSION','END_TO_END_RESULT_EQUIVALENCE'
    )),
  ADD COLUMN sealed_content_hash text NOT NULL DEFAULT ''
    CHECK(
      sealed_content_hash=''
      OR sealed_content_hash ~ '^[0-9a-f]{64}$'
    ),
  ADD COLUMN sealed_at timestamptz;

ALTER TABLE platform.semantic_golden_question_sets
  ADD CONSTRAINT semantic_golden_question_sets_sealed_shape_check CHECK(
    (dataset_split<>'SEALED' AND sealed_content_hash='' AND sealed_at IS NULL)
    OR
    (dataset_split='SEALED' AND (
      status='DRAFT'
      OR (
        evaluation_mode='END_TO_END_RESULT_EQUIVALENCE'
        AND sealed_content_hash ~ '^[0-9a-f]{64}$'
        AND sealed_at IS NOT NULL
      )
    ))
  );

ALTER TABLE platform.semantic_golden_questions
  ADD COLUMN approved_question text NOT NULL DEFAULT ''
    CHECK(length(approved_question)<=4000),
  ADD COLUMN priority text NOT NULL DEFAULT 'P1'
    CHECK(priority IN ('P0','P1','P2')),
  ADD COLUMN answerable boolean NOT NULL DEFAULT true,
  ADD COLUMN security_expectation text NOT NULL DEFAULT 'NONE'
    CHECK(security_expectation IN (
      'NONE','UNAUTHORIZED_BLOCK','PROMPT_INJECTION_BLOCK',
      'SENSITIVE_DATA_BLOCK','CACHE_ISOLATION_BLOCK'
    )),
  ADD COLUMN independent_review_count smallint NOT NULL DEFAULT 0
    CHECK(independent_review_count BETWEEN 0 AND 2);

ALTER TABLE platform.semantic_golden_questions
  ADD CONSTRAINT semantic_golden_questions_result_hash_check CHECK(
    NOT (fixture_json ? 'expectedResultHash')
    OR fixture_json->>'expectedResultHash' ~ '^[0-9a-f]{64}$'
  );

ALTER TABLE platform.semantic_golden_question_runs
  ADD COLUMN evaluation_mode text NOT NULL DEFAULT 'FIXTURE_REGRESSION'
    CHECK(evaluation_mode IN (
      'FIXTURE_REGRESSION','END_TO_END_RESULT_EQUIVALENCE'
    )),
  ADD COLUMN semantic_version text NOT NULL DEFAULT ''
    CHECK(length(semantic_version)<=200),
  ADD COLUMN semantic_content_hash text NOT NULL DEFAULT ''
    CHECK(
      semantic_content_hash=''
      OR semantic_content_hash ~ '^[0-9a-f]{64}$'
    ),
  ADD COLUMN expected_result_hash text NOT NULL DEFAULT ''
    CHECK(
      expected_result_hash=''
      OR expected_result_hash ~ '^[0-9a-f]{64}$'
    ),
  ADD COLUMN actual_result_hash text NOT NULL DEFAULT ''
    CHECK(
      actual_result_hash=''
      OR actual_result_hash ~ '^[0-9a-f]{64}$'
    ),
  ADD COLUMN direct_answer boolean NOT NULL DEFAULT false,
  ADD COLUMN refusal boolean NOT NULL DEFAULT false,
  ADD COLUMN unauthorized_blocked boolean NOT NULL DEFAULT false,
  ADD COLUMN sensitive_leak_detected boolean NOT NULL DEFAULT false;

ALTER TABLE platform.semantic_golden_question_runs
  DROP CONSTRAINT semantic_golden_question_runs_actual_status_check;
ALTER TABLE platform.semantic_golden_question_runs
  ADD CONSTRAINT semantic_golden_question_runs_actual_status_check CHECK(
    actual_status='' OR actual_status IN (
      'READY','AMBIGUOUS','GAP','REJECTED','EXECUTED','FAILED'
    )
  );

ALTER TABLE platform.semantic_golden_question_runs
  DROP CONSTRAINT semantic_golden_question_runs_failure_stage_check;
ALTER TABLE platform.semantic_golden_question_runs
  ADD CONSTRAINT semantic_golden_question_runs_failure_stage_check CHECK(
    failure_stage='' OR failure_stage IN (
      'INTENT','RECALL','BINDING','RELATIONSHIP','GRAPH','PLANNING',
      'AUTHORIZATION','QUALITY','EXECUTION','VALIDATION','EXPRESSION'
    )
  );

CREATE INDEX semantic_golden_question_runs_gate_idx
  ON platform.semantic_golden_question_runs(
    tenant_id,golden_question_id,evaluation_mode,created_at DESC,id DESC
  );

COMMENT ON COLUMN platform.semantic_golden_question_sets.evaluation_mode IS
  'FIXTURE_REGRESSION 只验证规划回归；只有 END_TO_END_RESULT_EQUIVALENCE 可进入正式发布门禁';
COMMENT ON COLUMN platform.semantic_golden_question_sets.sealed_content_hash IS
  '冻结后由全部双人复核问题 hash、期望路径与期望结果生成的不可变集合指纹';
COMMENT ON COLUMN platform.semantic_golden_questions.approved_question IS
  '仅 END_TO_END 集保存经脱敏和双人批准的评测问句；普通回归继续只保存 question hash';
COMMENT ON COLUMN platform.semantic_golden_question_runs.sensitive_leak_detected IS
  '安全评测中检测到受保护问题被直接回答；正式门禁要求始终为 false';

COMMIT;
