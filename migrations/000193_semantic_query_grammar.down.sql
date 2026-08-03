DELETE FROM platform.semantic_parsing_rules
WHERE tenant_id IS NULL
  AND rule_type='QUERY_RESIDUAL_TERM'
  AND lower(pattern)=ANY(ARRAY[
    '按','按照','根据','基于','依据','每个','各'
  ]::text[]);
