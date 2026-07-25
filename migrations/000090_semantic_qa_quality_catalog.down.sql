BEGIN;

DROP TABLE IF EXISTS platform.semantic_golden_question_runs;
ALTER TABLE platform.semantic_golden_questions
  DROP CONSTRAINT IF EXISTS semantic_golden_questions_set_fk;
ALTER TABLE platform.semantic_golden_questions
  DROP CONSTRAINT IF EXISTS semantic_golden_questions_set_hash_key;
ALTER TABLE platform.semantic_golden_questions
  DROP COLUMN IF EXISTS set_id;
ALTER TABLE platform.semantic_golden_questions
  ADD CONSTRAINT semantic_golden_questions_hash_key
  UNIQUE(tenant_id,question_hash);
DROP TABLE IF EXISTS platform.semantic_golden_question_sets;

COMMIT;
