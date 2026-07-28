BEGIN;

UPDATE platform.dimension_where_decisions AS decision
SET dimension_field_name=field.field_code::text,
    where_condition=regexp_replace(
      decision.where_condition,
      '^[^[:space:]]+',
      field.field_code::text
    ),
    compiled_condition=regexp_replace(
      decision.compiled_condition,
      '^[^[:space:]]+',
      field.field_code::text
    )
FROM platform.semantic_dimensions AS dimension
JOIN platform.dataset_fields AS field
  ON field.tenant_id=dimension.tenant_id
 AND field.dataset_version_id=dimension.dataset_version_id
 AND field.field_id=dimension.field_id
WHERE dimension.tenant_id=decision.tenant_id
  AND dimension.id=decision.dimension_id;

COMMENT ON COLUMN platform.dimension_where_decisions.dimension_field_name IS
  NULL;

COMMIT;
