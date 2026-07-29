DELETE FROM platform.dwd_modeling_checkpoints
WHERE checkpoint_kind IN (
  'DIM_NAME_VALIDATION',
  'DIM_DUPLICATE_VALIDATION'
);

ALTER TABLE platform.dwd_modeling_checkpoints
  DROP CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check,
  ADD CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check
    CHECK(checkpoint_kind IN (
      'CLASSIFICATION',
      'CLASSIFICATION_MERGE',
      'DIM_DESIGN',
      'FACT_DESIGN'
    ));

DROP TABLE IF EXISTS platform.dim_modeling_suppressions;

COMMENT ON TABLE platform.dwd_modeling_checkpoints IS
  '通过本地合同校验的逐 ODS 分类、领域唯一性合并审校、逐 DIM 说明与标准化设计、逐 FACT DWD 设计检查点';

COMMENT ON TABLE platform.dim_modeling_outputs IS
  'ODS 分类为 DIMENSION/MASTER 后生成的 DIM 草稿所有权与幂等映射';
