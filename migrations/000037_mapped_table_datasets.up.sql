-- 每张经 LLM 完整映射的元数据表默认拥有且仅拥有一个单表数据集。
ALTER TABLE platform.datasets
  ADD COLUMN origin_table_id uuid;

ALTER TABLE platform.datasets
  ADD CONSTRAINT datasets_origin_table_fk
  FOREIGN KEY(origin_table_id,tenant_id)
  REFERENCES platform.metadata_tables(id,tenant_id);

-- 软删除的数据集仍占用来源表身份，避免同一映射表产生多个历史主对象。
CREATE UNIQUE INDEX datasets_tenant_origin_table_uidx
  ON platform.datasets(tenant_id,origin_table_id)
  WHERE origin_table_id IS NOT NULL;

COMMENT ON COLUMN platform.datasets.origin_table_id IS
  '由 LLM 完整映射的元数据表创建的数据集来源；为空表示人工创建的数据集';
