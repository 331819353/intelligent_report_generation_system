-- 不把历史 v4 检查点伪装成 v5；回滚仅移除说明。
COMMENT ON COLUMN platform.dwd_modeling_stage_jobs.prompt_version IS NULL;
