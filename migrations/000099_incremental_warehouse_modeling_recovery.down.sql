-- Recovery only changes durable job state and cannot be safely reversed after
-- workers consume the jobs. Keep the corrected table description on rollback.
COMMENT ON TABLE platform.dwd_modeling_outputs IS
  '事实 ODS 与后台生成 DWD 的稳定映射及人工修改保护栅栏';
