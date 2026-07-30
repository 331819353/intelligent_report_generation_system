DELETE FROM platform.asset_tag_bindings AS binding
USING platform.semantic_tags AS tag
WHERE tag.id=binding.tag_id
  AND tag.code::text IN (
    'system.function.ods_fact',
    'system.function.ods_dimension',
    'system.function.ods_fact_dimension',
    'system.function.ods_other'
  );

DELETE FROM platform.semantic_tags
WHERE code::text IN (
  'system.function.ods_fact',
  'system.function.ods_dimension',
  'system.function.ods_fact_dimension',
  'system.function.ods_other'
);
