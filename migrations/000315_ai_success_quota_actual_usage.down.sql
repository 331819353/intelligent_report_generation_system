BEGIN;

ALTER TABLE platform.ai_requests
  DROP COLUMN accounted_tokens,
  DROP COLUMN accounted_cost_micros;

ALTER TABLE platform.ai_requests
  ADD COLUMN accounted_tokens bigint GENERATED ALWAYS AS (
    CASE WHEN status='SUCCEEDED' THEN GREATEST(total_tokens::bigint,reserved_tokens) ELSE reserved_tokens END
  ) STORED,
  ADD COLUMN accounted_cost_micros bigint GENERATED ALWAYS AS (
    CASE WHEN status='SUCCEEDED' THEN GREATEST(cost_micros,reserved_cost_micros) ELSE reserved_cost_micros END
  ) STORED;

COMMENT ON COLUMN platform.ai_requests.accounted_tokens IS
  '终态至少保留预留量，可信实耗更高时采用实耗的失败关闭 Token 计量';
COMMENT ON COLUMN platform.ai_requests.accounted_cost_micros IS
  '终态至少保留预留量，可信实耗更高时采用实耗的失败关闭成本计量';

COMMIT;
