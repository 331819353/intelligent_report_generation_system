-- 跨源节点指标由可信网关采集，用于运行追溯、慢源定位和后续代价模型。
ALTER TABLE platform.query_run_sources
  ADD COLUMN status text NOT NULL DEFAULT 'RUNNING'
    CHECK(status IN ('RUNNING','SUCCEEDED','FAILED','TIMEOUT','CANCELLED')),
  ADD COLUMN row_count integer NOT NULL DEFAULT 0 CHECK(row_count>=0),
  ADD COLUMN duration_ms bigint NOT NULL DEFAULT 0 CHECK(duration_ms>=0);

COMMENT ON COLUMN platform.query_run_sources.status IS '跨源节点执行状态';
COMMENT ON COLUMN platform.query_run_sources.row_count IS '节点通过网关校验后的实际输入行数';
COMMENT ON COLUMN platform.query_run_sources.duration_ms IS '节点从读取到规范化完成的网关侧耗时';
