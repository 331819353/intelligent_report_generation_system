-- Recovery can be consumed immediately and therefore cannot be reversed safely.
COMMENT ON TABLE platform.dwd_modeling_outputs IS
  '事实 ODS 与后台生成 DWD 的稳定映射；Worker 比较历史来源版本后仅更新变化模型，并保护人工修改草稿';
