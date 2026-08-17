-- A zero AI policy limit is the explicit unlimited sentinel. Keep usage and
-- cost accounting for observability, but do not reject model calls by default.
BEGIN;

ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT IF EXISTS ai_tenant_policies_max_requests_per_day_check,
  DROP CONSTRAINT IF EXISTS ai_tenant_policies_max_tokens_per_month_check,
  DROP CONSTRAINT IF EXISTS ai_tenant_policies_max_cost_micros_per_month_check;

ALTER TABLE platform.ai_tenant_policies
  ALTER COLUMN max_requests_per_day SET DEFAULT 0,
  ALTER COLUMN max_tokens_per_month SET DEFAULT 0,
  ALTER COLUMN max_cost_micros_per_month SET DEFAULT 0;

UPDATE platform.ai_tenant_policies
SET max_requests_per_day=0,
    max_tokens_per_month=0,
    max_cost_micros_per_month=0
WHERE max_requests_per_day<>0
   OR max_tokens_per_month<>0
   OR max_cost_micros_per_month<>0;

ALTER TABLE platform.ai_tenant_policies
  ADD CONSTRAINT ai_tenant_policies_max_requests_per_day_check
    CHECK(max_requests_per_day>=0),
  ADD CONSTRAINT ai_tenant_policies_max_tokens_per_month_check
    CHECK(max_tokens_per_month>=0),
  ADD CONSTRAINT ai_tenant_policies_max_cost_micros_per_month_check
    CHECK(max_cost_micros_per_month>=0);

COMMENT ON COLUMN platform.ai_tenant_policies.max_requests_per_day IS
  '每日 AI 请求上限；0 表示不限额';
COMMENT ON COLUMN platform.ai_tenant_policies.max_tokens_per_month IS
  '每月 AI Token 上限；0 表示不限额';
COMMENT ON COLUMN platform.ai_tenant_policies.max_cost_micros_per_month IS
  '每月 AI 成本微单位上限；0 表示不限额';

COMMIT;
