-- Recovery can be consumed immediately and therefore cannot be reversed safely.
COMMENT ON TABLE platform.dwd_modeling_jobs IS
  'ODS 发布后的可恢复仓库建模任务；支持租约、检查点、并发 LLM 与稳定结果码';
