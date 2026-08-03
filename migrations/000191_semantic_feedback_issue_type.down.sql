BEGIN;

DROP INDEX IF EXISTS platform.semantic_query_feedback_issue_idx;
ALTER TABLE platform.semantic_query_feedback
  DROP CONSTRAINT IF EXISTS semantic_query_feedback_issue_shape_check,
  DROP COLUMN IF EXISTS issue_type;

COMMIT;
