-- 产品默认用最多十行格式脱敏样本辅助 LLM 识别；原始值仍必须逐任务明确选择 RAW。
-- MASK 只把业务值转换为格式保持占位值后发送，并继续受任务授权、策略版本冻结和撤权围栏约束。

ALTER TABLE platform.ai_tenant_policies
  ALTER COLUMN metadata_sample_mode SET DEFAULT 'MASK';

UPDATE platform.ai_tenant_policies
SET metadata_sample_mode='MASK'
WHERE metadata_sample_mode='DENY';

COMMENT ON COLUMN platform.ai_tenant_policies.metadata_sample_mode IS
  '元数据补全样本上限：默认 MASK（最多十行格式脱敏样本）；DENY 不采样；RAW 允许逐任务明确同意的原值';
