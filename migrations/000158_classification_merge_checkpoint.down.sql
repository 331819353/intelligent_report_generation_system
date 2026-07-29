DELETE FROM platform.dwd_modeling_checkpoints
WHERE checkpoint_kind='CLASSIFICATION_MERGE';

ALTER TABLE platform.dwd_modeling_checkpoints
  DROP CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check,
  ADD CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check
    CHECK(checkpoint_kind IN (
      'CLASSIFICATION',
      'DIM_DESIGN',
      'FACT_DESIGN'
    ));

COMMENT ON TABLE platform.dwd_modeling_checkpoints IS
  '通过本地合同校验的领域 ODS 分类、逐 DIM 说明与标准化设计、逐 FACT DWD 设计检查点';
