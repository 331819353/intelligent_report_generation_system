BEGIN;

ALTER TABLE platform.ai_tenant_policies
  DROP CONSTRAINT IF EXISTS ai_tenant_policies_max_requests_per_day_check,
  DROP CONSTRAINT IF EXISTS ai_tenant_policies_max_tokens_per_month_check,
  DROP CONSTRAINT IF EXISTS ai_tenant_policies_max_cost_micros_per_month_check;

UPDATE platform.ai_tenant_policies
SET max_requests_per_day=CASE WHEN max_requests_per_day=0 THEN 1000 ELSE max_requests_per_day END,
    max_tokens_per_month=CASE WHEN max_tokens_per_month=0 THEN 10000000 ELSE max_tokens_per_month END,
    max_cost_micros_per_month=CASE WHEN max_cost_micros_per_month=0 THEN 100000000 ELSE max_cost_micros_per_month END;

ALTER TABLE platform.ai_tenant_policies
  ALTER COLUMN max_requests_per_day SET DEFAULT 1000,
  ALTER COLUMN max_tokens_per_month SET DEFAULT 10000000,
  ALTER COLUMN max_cost_micros_per_month SET DEFAULT 100000000,
  ADD CONSTRAINT ai_tenant_policies_max_requests_per_day_check
    CHECK(max_requests_per_day>0),
  ADD CONSTRAINT ai_tenant_policies_max_tokens_per_month_check
    CHECK(max_tokens_per_month>0),
  ADD CONSTRAINT ai_tenant_policies_max_cost_micros_per_month_check
    CHECK(max_cost_micros_per_month>0);

COMMENT ON COLUMN platform.ai_tenant_policies.max_requests_per_day IS NULL;
COMMENT ON COLUMN platform.ai_tenant_policies.max_tokens_per_month IS NULL;
COMMENT ON COLUMN platform.ai_tenant_policies.max_cost_micros_per_month IS NULL;

COMMIT;
