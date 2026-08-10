DROP FUNCTION IF EXISTS askdata.recompute_release_evaluation_gate(uuid,uuid,uuid,uuid);
DROP FUNCTION IF EXISTS askdata.expose_evaluation_shard(uuid,smallint,uuid);
DROP FUNCTION IF EXISTS askdata.plan_evaluation_batch(uuid,uuid,text,uuid);
DROP FUNCTION IF EXISTS askdata.record_release_error_budget(uuid,uuid,uuid,jsonb,uuid);
DROP FUNCTION IF EXISTS askdata.wilson_lower_bound(bigint,bigint);

DROP TABLE IF EXISTS askdata.release_evaluation_gate_receipts;
DROP TABLE IF EXISTS askdata.release_error_budget_receipts;
DROP TABLE IF EXISTS askdata.evaluation_narrative_results;
DROP TABLE IF EXISTS askdata.evaluation_batch_plans;
DROP TABLE IF EXISTS askdata.evaluation_shard_rotations;
DROP TABLE IF EXISTS askdata.evaluation_shard_states;

ALTER TABLE askdata.releases
  DROP CONSTRAINT IF EXISTS askdata_releases_gate_pin_key;

DROP INDEX IF EXISTS askdata.askdata_evaluation_cases_shard_idx;
ALTER TABLE askdata.evaluation_cases
  DROP CONSTRAINT IF EXISTS askdata_evaluation_cases_shard_retirement_shape_check,
  DROP COLUMN IF EXISTS retire_reason,
  DROP COLUMN IF EXISTS retired_at,
  DROP COLUMN IF EXISTS exposed_at,
  DROP COLUMN IF EXISTS usage_count,
  DROP COLUMN IF EXISTS shard_id;
