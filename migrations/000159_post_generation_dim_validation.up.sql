-- DIM 先全部生成未发布草稿，再通过两阶段校验检查表名和重名组字段。
ALTER TABLE platform.dwd_modeling_checkpoints
  DROP CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check,
  ADD CONSTRAINT dwd_modeling_checkpoints_checkpoint_kind_check
    CHECK(checkpoint_kind IN (
      'CLASSIFICATION',
      'CLASSIFICATION_MERGE',
      'DIM_DESIGN',
      'DIM_NAME_VALIDATION',
      'DIM_DUPLICATE_VALIDATION',
      'FACT_DESIGN'
    ));

-- DIM 与 ODS 始终保持一对一。字段校验选择权威候选后，单独记录被抑制来源，
-- 供后续事实规划复用权威 DIM；该记录不改变 DIM 的物理血缘。
CREATE TABLE platform.dim_modeling_suppressions(
  id uuid PRIMARY KEY DEFAULT public.gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  suppressed_source_dataset_id uuid NOT NULL,
  canonical_source_dataset_id uuid NOT NULL,
  canonical_dim_dataset_id uuid NOT NULL,
  last_job_id uuid NOT NULL,
  last_input_hash text NOT NULL CHECK(last_input_hash ~ '^[0-9a-f]{64}$'),
  rationale text NOT NULL DEFAULT '' CHECK(length(rationale)<=2048),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT dim_modeling_suppressions_sources_differ_check
    CHECK(suppressed_source_dataset_id<>canonical_source_dataset_id),
  CONSTRAINT dim_modeling_suppressions_source_fk
    FOREIGN KEY(suppressed_source_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dim_modeling_suppressions_canonical_source_fk
    FOREIGN KEY(canonical_source_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dim_modeling_suppressions_dim_fk
    FOREIGN KEY(canonical_dim_dataset_id,tenant_id)
    REFERENCES platform.datasets(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dim_modeling_suppressions_job_fk
    FOREIGN KEY(last_job_id,tenant_id)
    REFERENCES platform.dwd_modeling_jobs(id,tenant_id) ON DELETE RESTRICT,
  CONSTRAINT dim_modeling_suppressions_source_key
    UNIQUE(tenant_id,suppressed_source_dataset_id),
  CONSTRAINT dim_modeling_suppressions_identity_tenant_key
    UNIQUE(id,tenant_id)
);

CREATE INDEX dim_modeling_suppressions_canonical_idx
  ON platform.dim_modeling_suppressions(
    tenant_id,canonical_source_dataset_id,canonical_dim_dataset_id
  );

CREATE TRIGGER dim_modeling_suppressions_set_updated_at
BEFORE UPDATE ON platform.dim_modeling_suppressions
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

ALTER TABLE platform.dim_modeling_suppressions ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.dim_modeling_suppressions FORCE ROW LEVEL SECURITY;

CREATE POLICY dim_modeling_suppressions_tenant_isolation
  ON platform.dim_modeling_suppressions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

COMMENT ON TABLE platform.dwd_modeling_checkpoints IS
  '通过本地合同校验的逐 ODS 分类、领域分类审校、逐 DIM 设计、DIM 全量表名校验、重名 DIM 字段裁决及逐 FACT DWD 设计检查点';

COMMENT ON TABLE platform.dim_modeling_outputs IS
  'ODS 到系统生成 DIM 的一对一所有权与幂等映射';

COMMENT ON TABLE platform.dim_modeling_suppressions IS
  '重名 DIM 字段校验后，被抑制 ODS 候选到权威单 ODS DIM 的非物理复用记录';
