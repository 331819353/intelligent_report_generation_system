DROP TABLE IF EXISTS platform.semantic_question_artifacts;
DROP INDEX IF EXISTS platform.semantic_question_runs_release_idx;
ALTER TABLE platform.semantic_question_runs
  DROP CONSTRAINT IF EXISTS semantic_question_runs_release_shape_check,
  DROP CONSTRAINT IF EXISTS semantic_question_runs_release_fk,
  DROP COLUMN IF EXISTS graph_plan_hash,
  DROP COLUMN IF EXISTS understanding_hash,
  DROP COLUMN IF EXISTS semantic_content_hash,
  DROP COLUMN IF EXISTS semantic_release_id;
