BEGIN;

ALTER TABLE platform.semantic_query_plans
  DROP CONSTRAINT IF EXISTS semantic_query_plans_execution_result_shape_check,
  DROP COLUMN IF EXISTS execution_row_count,
  DROP COLUMN IF EXISTS execution_duration_ms;

COMMIT;
