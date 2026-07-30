ALTER TABLE platform.dim_modeling_outputs
  ADD COLUMN dimension_key text NOT NULL DEFAULT 'primary';

ALTER TABLE platform.dim_modeling_outputs
  ADD CONSTRAINT dim_modeling_outputs_dimension_key_check
  CHECK (dimension_key ~ '^[a-z][a-z0-9_]{0,63}$');

ALTER TABLE platform.dim_modeling_outputs
  DROP CONSTRAINT dim_modeling_outputs_source_key;

ALTER TABLE platform.dim_modeling_outputs
  ADD CONSTRAINT dim_modeling_outputs_source_dimension_key
  UNIQUE(tenant_id,source_dataset_id,dimension_key);

DROP INDEX platform.dim_modeling_outputs_domain_idx;

CREATE INDEX dim_modeling_outputs_domain_idx
  ON platform.dim_modeling_outputs(
    tenant_id,domain_key,source_dataset_id,dimension_key
  );

COMMENT ON COLUMN platform.dim_modeling_outputs.dimension_key IS
  'Stable LLM projection key within one ODS source. primary preserves the legacy one-source/one-DIM mapping.';

COMMENT ON TABLE platform.dim_modeling_outputs IS
  'System-generated DIM mapping. One ODS source may produce multiple independently governed DIM datasets.';
