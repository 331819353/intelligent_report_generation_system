DROP FUNCTION IF EXISTS askdata.record_cost_usage(uuid,uuid,uuid,uuid,text,text,text,bigint,bigint,bigint,bigint);
DROP FUNCTION IF EXISTS askdata.load_quota_usage_snapshots(uuid,uuid,uuid,timestamptz);
DROP INDEX IF EXISTS askdata.askdata_cost_records_run_time_idx;
ALTER TABLE IF EXISTS askdata.quotas
  DROP CONSTRAINT IF EXISTS askdata_quotas_scope_period_check;
