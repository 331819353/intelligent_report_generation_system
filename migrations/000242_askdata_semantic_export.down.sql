DROP FUNCTION IF EXISTS askdata.fail_semantic_export(uuid,uuid,text,uuid,text,boolean);
DROP FUNCTION IF EXISTS askdata.complete_semantic_export(uuid,uuid,text,uuid,text,text,integer,integer);
DROP FUNCTION IF EXISTS askdata.claim_semantic_export(uuid,text,integer);
DROP FUNCTION IF EXISTS askdata.list_semantic_export_tenants();
DROP TRIGGER IF EXISTS askdata_semantic_export_jobs_transition_guard
  ON askdata.semantic_export_jobs;
DROP TRIGGER IF EXISTS askdata_semantic_export_jobs_set_updated_at
  ON askdata.semantic_export_jobs;
DROP FUNCTION IF EXISTS askdata.enforce_semantic_export_transition();
DROP TABLE IF EXISTS askdata.semantic_export_jobs;
DROP FUNCTION IF EXISTS askdata.semantic_export_asset_types_valid(text[]);
ALTER TABLE askdata.metric_versions
  DROP COLUMN IF EXISTS description,
  DROP COLUMN IF EXISTS name;
