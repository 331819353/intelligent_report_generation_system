BEGIN;

ALTER TABLE platform.semantic_query_plans
  ADD COLUMN execution_duration_ms bigint
    CHECK(execution_duration_ms IS NULL OR execution_duration_ms>=0),
  ADD COLUMN execution_row_count integer
    CHECK(execution_row_count IS NULL OR execution_row_count>=0);

-- 91–94 期间已完成的执行没有时延/行数列；用 0 作为“历史未知”哨兵，
-- 保证升级不丢弃审计记录。新执行始终写入运行时的真实值。
UPDATE platform.semantic_query_plans
SET execution_duration_ms=0,execution_row_count=0
WHERE status='EXECUTED';

ALTER TABLE platform.semantic_query_plans
  ADD CONSTRAINT semantic_query_plans_execution_result_shape_check CHECK(
    (status='EXECUTED'
      AND executed_query_id<>''
      AND execution_error_code=''
      AND execution_duration_ms IS NOT NULL
      AND execution_row_count IS NOT NULL)
    OR
    (status<>'EXECUTED'
      AND execution_duration_ms IS NULL
      AND execution_row_count IS NULL)
  ) NOT VALID;

ALTER TABLE platform.semantic_query_plans
  VALIDATE CONSTRAINT semantic_query_plans_execution_result_shape_check;

COMMENT ON COLUMN platform.semantic_query_plans.execution_duration_ms IS
  '受控指标运行时返回的总执行毫秒数；只保存运行元数据，不保存结果行';
COMMENT ON COLUMN platform.semantic_query_plans.execution_row_count IS
  '受控指标运行时返回的行数；用于质量回放和物化建议';

COMMIT;
