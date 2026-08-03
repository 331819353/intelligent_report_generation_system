BEGIN;

DROP TRIGGER IF EXISTS semantic_query_feedback_set_updated_at
  ON platform.semantic_query_feedback;
DROP TABLE IF EXISTS platform.semantic_query_feedback;

COMMIT;
