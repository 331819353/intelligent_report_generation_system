DROP TABLE IF EXISTS platform.dwd_modeling_checkpoints;

ALTER TABLE platform.dwd_modeling_jobs
  DROP CONSTRAINT IF EXISTS dwd_modeling_jobs_checkpoint_claim_check,
  DROP COLUMN IF EXISTS claimed_checkpoint_version,
  DROP COLUMN IF EXISTS checkpoint_version;
