-- LLM 可以在元数据证据不足时把 ODS 判为 OTHER。该角色同样需要进入
-- 触发任务结果和领域级合并摘要，不能在最终持久化时被列约束拒绝。
ALTER TABLE platform.dwd_modeling_jobs
  DROP CONSTRAINT dwd_modeling_jobs_trigger_role_check;

ALTER TABLE platform.dwd_modeling_jobs
  ADD CONSTRAINT dwd_modeling_jobs_trigger_role_check
  CHECK(trigger_role IN ('','FACT','DIMENSION','MASTER','OTHER'));
