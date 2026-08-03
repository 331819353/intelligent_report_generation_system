BEGIN;

DROP INDEX IF EXISTS platform.semantic_golden_question_runs_gate_idx;

ALTER TABLE platform.semantic_golden_question_runs
  DROP CONSTRAINT semantic_golden_question_runs_failure_stage_check;
ALTER TABLE platform.semantic_golden_question_runs
  ADD CONSTRAINT semantic_golden_question_runs_failure_stage_check CHECK(
    failure_stage='' OR failure_stage IN (
      'RECALL','RELATIONSHIP','PLANNING','QUALITY','EXECUTION','EXPRESSION'
    )
  );

ALTER TABLE platform.semantic_golden_question_runs
  DROP COLUMN IF EXISTS sensitive_leak_detected,
  DROP COLUMN IF EXISTS unauthorized_blocked,
  DROP COLUMN IF EXISTS refusal,
  DROP COLUMN IF EXISTS direct_answer,
  DROP COLUMN IF EXISTS actual_result_hash,
  DROP COLUMN IF EXISTS expected_result_hash,
  DROP COLUMN IF EXISTS semantic_content_hash,
  DROP COLUMN IF EXISTS semantic_version,
  DROP COLUMN IF EXISTS evaluation_mode;

ALTER TABLE platform.semantic_golden_questions
  DROP CONSTRAINT IF EXISTS semantic_golden_questions_result_hash_check,
  DROP COLUMN IF EXISTS independent_review_count,
  DROP COLUMN IF EXISTS security_expectation,
  DROP COLUMN IF EXISTS answerable,
  DROP COLUMN IF EXISTS priority,
  DROP COLUMN IF EXISTS approved_question;

ALTER TABLE platform.semantic_golden_question_sets
  DROP CONSTRAINT IF EXISTS semantic_golden_question_sets_sealed_shape_check,
  DROP COLUMN IF EXISTS sealed_at,
  DROP COLUMN IF EXISTS sealed_content_hash,
  DROP COLUMN IF EXISTS evaluation_mode,
  DROP COLUMN IF EXISTS dataset_split;

COMMIT;
