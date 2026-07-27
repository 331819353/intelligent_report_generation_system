DELETE FROM platform.dwd_modeling_checkpoints
WHERE checkpoint_kind='DIM_DESIGN';

ALTER TABLE platform.dwd_modeling_checkpoints
  DROP CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check,
  ADD CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check
    CHECK(checkpoint_kind IN ('CLASSIFICATION','FACT_DESIGN'));

COMMENT ON TABLE platform.dwd_modeling_checkpoints IS
  '通过本地合同校验的 ODS 分类和逐 FACT DWD 设计检查点；支持进程中断后的精确续跑';
