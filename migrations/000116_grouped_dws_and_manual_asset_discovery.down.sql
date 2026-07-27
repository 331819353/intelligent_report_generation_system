DROP FUNCTION IF EXISTS platform.trigger_manual_dws_modeling(uuid);

ALTER TABLE platform.dws_modeling_outputs
  DROP CONSTRAINT IF EXISTS dws_modeling_outputs_group_template_key,
  DROP CONSTRAINT IF EXISTS dws_modeling_outputs_group_key_check,
  DROP COLUMN IF EXISTS group_key;
ALTER TABLE platform.dws_modeling_outputs
  DROP CONSTRAINT IF EXISTS dws_modeling_outputs_template_code_check;
ALTER TABLE platform.dws_modeling_outputs
  ADD CONSTRAINT dws_modeling_outputs_template_code_check CHECK(
    template_code IN (
      'TREND','PERIOD_COMPARISON','DISTRIBUTION',
      'RANKING','DRILLDOWN','ANOMALY'
    )
  ),
  ADD CONSTRAINT dws_modeling_outputs_source_template_key
    UNIQUE(tenant_id,source_dwd_dataset_id,template_code);

ALTER TABLE platform.dws_modeling_jobs
  DROP CONSTRAINT IF EXISTS dws_modeling_jobs_scope_key,
  DROP CONSTRAINT IF EXISTS dws_modeling_jobs_group_key_check,
  DROP CONSTRAINT IF EXISTS dws_modeling_jobs_source_scope_check,
  DROP CONSTRAINT IF EXISTS dws_modeling_jobs_scope_hash_check,
  DROP COLUMN IF EXISTS group_key,
  DROP COLUMN IF EXISTS source_scope,
  DROP COLUMN IF EXISTS scope_hash;
ALTER TABLE platform.dws_modeling_jobs
  ADD CONSTRAINT dws_modeling_jobs_version_key
    UNIQUE(tenant_id,source_dwd_version_id);

CREATE OR REPLACE FUNCTION platform.trigger_manual_dws_modeling(actor_id uuid)
RETURNS TABLE(eligible_count bigint,enqueued_count bigint,blocked_count bigint)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  RETURN QUERY SELECT 0::bigint,0::bigint,0::bigint;
END
$$;

CREATE TRIGGER dataset_versions_enqueue_dws_metric_discovery
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dws_metric_discovery();
CREATE TRIGGER dataset_versions_enqueue_dimension_survey
AFTER UPDATE OF status ON platform.dataset_versions
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_dws_dimension_survey();
CREATE TRIGGER dataset_materializations_00_enqueue_dimension_profiles
AFTER INSERT OR UPDATE OF status ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION platform.enqueue_active_dws_dimension_profiles();
CREATE TRIGGER dataset_materializations_complete_dimension_survey
AFTER INSERT OR UPDATE OF status ON platform.dataset_materializations
FOR EACH ROW EXECUTE FUNCTION platform.complete_dws_dimension_survey();
