-- 本迁移只纠正已完成任务的终态。回滚代码时不把已经确认“无事实表”的流程
-- 重新改回无意义的 DIM 发布等待。

COMMENT ON COLUMN platform.dwd_modeling_stage_jobs.stage IS NULL;
