BEGIN;

ALTER TABLE platform.dimension_where_design_policies
  DROP CONSTRAINT dimension_where_design_policies_attempt_check;

ALTER TABLE platform.dimension_where_design_policies
  ADD CONSTRAINT dimension_where_design_policies_attempt_check
  CHECK(attempt BETWEEN 0 AND 10);

UPDATE platform.dimension_where_design_policies
SET status='PENDING',attempt=0,next_attempt_at=now(),
    error_code='',completed_at=NULL,updated_at=now()
WHERE status='FAILED'
  AND error_code IN (
    'LLM_PROVIDER_FAILED','LLM_TIMEOUT','LLM_QUOTA_EXCEEDED'
  );

COMMENT ON COLUMN platform.dimension_where_design_policies.attempt IS
  '受约束LLM策略最多重试10次；短时Provider波动不能让一个DWS维度永久缺失';

COMMIT;
