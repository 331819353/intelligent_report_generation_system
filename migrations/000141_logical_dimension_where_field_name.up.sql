BEGIN;

-- Decision graph rows are a semantic-layer audit contract. Display the
-- governed dimension code in the reader-facing WHERE while retaining the
-- immutable dataset field_id in compiled_condition for physical execution.
-- This prevents a legacy physical column name (for example business_track)
-- from masquerading as the governed birth_cohort dimension in the Q&A trace.
UPDATE platform.dimension_where_decisions AS decision
SET dimension_field_name=dimension.code::text,
    where_condition=regexp_replace(
      decision.where_condition,
      '^[^[:space:]]+',
      dimension.code::text
    ),
    compiled_condition=regexp_replace(
      decision.compiled_condition,
      '^[^[:space:]]+',
      dimension.field_id
    )
FROM platform.semantic_dimensions AS dimension
WHERE dimension.tenant_id=decision.tenant_id
  AND dimension.id=decision.dimension_id;

COMMENT ON COLUMN platform.dimension_where_decisions.dimension_field_name IS
  '读者可见的正式语义维度编码；物理执行字段使用 dimension_field_id 并在运行时重新解析';

COMMIT;
