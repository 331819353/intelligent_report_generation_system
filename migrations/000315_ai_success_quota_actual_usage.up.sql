-- Successful AI requests have trusted provider usage and must consume that
-- actual usage. Reserving worst-case output remains the admission-control
-- mechanism while a request is running and the fail-closed charge for failed
-- or canceled requests, but carrying the reservation into successful monthly
-- accounting makes normal multi-step workflows exhaust quota prematurely.
BEGIN;

ALTER TABLE platform.ai_requests
  DROP COLUMN accounted_tokens,
  DROP COLUMN accounted_cost_micros;

ALTER TABLE platform.ai_requests
  ADD COLUMN accounted_tokens bigint GENERATED ALWAYS AS (
    CASE WHEN status='SUCCEEDED' THEN total_tokens::bigint ELSE reserved_tokens END
  ) STORED,
  ADD COLUMN accounted_cost_micros bigint GENERATED ALWAYS AS (
    CASE WHEN status='SUCCEEDED' THEN cost_micros ELSE reserved_cost_micros END
  ) STORED;

COMMENT ON COLUMN platform.ai_requests.accounted_tokens IS
  '成功请求按可信 Provider 实耗计量；运行中、失败和取消请求按保守预留量计量';
COMMENT ON COLUMN platform.ai_requests.accounted_cost_micros IS
  '成功请求按可信 Provider 实际成本计量；运行中、失败和取消请求按保守预留成本计量';

COMMIT;
