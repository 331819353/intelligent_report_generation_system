-- 新批次已经形成不可变任务历史，安全回滚不能重新施加永久唯一约束或合并、
-- 删除既有批次。保留多批次表结构，仅移除说明性历史索引。
DROP INDEX IF EXISTS platform.dwd_modeling_jobs_version_history_idx;
SELECT 1;
