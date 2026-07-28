BEGIN;

CREATE INDEX dimension_member_aliases_decision_member_idx
  ON platform.dimension_member_aliases(
    tenant_id,dimension_id,dimension_member_id,alias
  )
  WHERE btrim(alias)<>'';

COMMENT ON INDEX platform.dimension_member_aliases_decision_member_idx IS
  '按DWS规范成员批量生成WHERE决策时读取已治理同义表达，避免逐成员扫描整维度';

COMMIT;
