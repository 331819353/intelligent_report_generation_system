DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM platform.dim_modeling_outputs
    WHERE dimension_key <> 'primary'
  ) THEN
    RAISE EXCEPTION
      'cannot roll back multi-DIM mapping while non-primary DIM outputs exist';
  END IF;
END
$$;

DROP INDEX platform.dim_modeling_outputs_domain_idx;

ALTER TABLE platform.dim_modeling_outputs
  DROP CONSTRAINT dim_modeling_outputs_source_dimension_key;

ALTER TABLE platform.dim_modeling_outputs
  DROP CONSTRAINT dim_modeling_outputs_dimension_key_check;

ALTER TABLE platform.dim_modeling_outputs
  DROP COLUMN dimension_key;

ALTER TABLE platform.dim_modeling_outputs
  ADD CONSTRAINT dim_modeling_outputs_source_key
  UNIQUE(tenant_id,source_dataset_id);

CREATE INDEX dim_modeling_outputs_domain_idx
  ON platform.dim_modeling_outputs(tenant_id,domain_key,source_dataset_id);

COMMENT ON TABLE platform.dim_modeling_outputs IS
  'System-generated DIM mapping. Each source ODS dataset has at most one generated DIM dataset.';
