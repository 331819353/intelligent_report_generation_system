-- 逐 ODS 并行分类完成后增加领域级合并审校检查点。该检查点保存后续
-- DIM/DWD 唯一使用的最终分类，使合并 LLM 失败时只重试合并，不重复调用
-- 已经成功的逐表识别。
ALTER TABLE platform.dwd_modeling_checkpoints
  DROP CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check,
  ADD CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check
    CHECK(checkpoint_kind IN (
      'CLASSIFICATION',
      'CLASSIFICATION_MERGE',
      'DIM_DESIGN',
      'FACT_DESIGN'
    ));

COMMENT ON TABLE platform.dwd_modeling_checkpoints IS
  '通过本地合同校验的逐 ODS 分类、领域唯一性合并审校、逐 DIM 说明与标准化设计、逐 FACT DWD 设计检查点';
