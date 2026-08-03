-- 常见中文分组/排序连接词只表达查询结构，不携带业务语义。
-- 将其作为可治理的确定性残余词，避免已锁定指标和维度后再次扫描完整语义语料。
INSERT INTO platform.semantic_parsing_rules(
  rule_type,pattern,match_mode,action,priority
)
SELECT 'QUERY_RESIDUAL_TERM',pattern,'EXACT','ALLOW_DETERMINISTIC',110
FROM unnest(ARRAY[
  '按','按照','根据','基于','依据','每个','各'
]) AS source(pattern)
WHERE NOT EXISTS(
  SELECT 1
  FROM platform.semantic_parsing_rules AS existing
  WHERE existing.tenant_id IS NULL
    AND existing.rule_type='QUERY_RESIDUAL_TERM'
    AND lower(existing.pattern)=lower(source.pattern)
);
