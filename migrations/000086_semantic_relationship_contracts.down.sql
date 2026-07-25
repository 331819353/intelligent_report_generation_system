DROP INDEX IF EXISTS
  platform.dimension_member_aliases_tenant_normalized_dimension_idx;
DROP INDEX IF EXISTS
  platform.dimension_members_tenant_normalized_dimension_active_idx;

DROP TRIGGER IF EXISTS
  semantic_dimensions_propose_metric_compatibility
ON platform.semantic_dimensions;
DROP TRIGGER IF EXISTS
  metric_versions_propose_semantic_dimension_compatibility
ON platform.metric_versions;

DROP FUNCTION IF EXISTS
  platform.propose_dimension_metric_compatibility();
DROP FUNCTION IF EXISTS
  platform.propose_metric_semantic_dimension_compatibility();

ALTER TABLE platform.dimension_metric_compatibility
  DROP CONSTRAINT IF EXISTS
    dimension_metric_compatibility_join_path_contract_check;

DROP FUNCTION IF EXISTS
  platform.semantic_join_path_is_valid(jsonb,text,text);
