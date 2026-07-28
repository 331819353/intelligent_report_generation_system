BEGIN;

DROP POLICY IF EXISTS dimension_member_aliases_write_scope ON platform.dimension_member_aliases;
DROP POLICY IF EXISTS dimension_member_aliases_read_scope ON platform.dimension_member_aliases;
CREATE POLICY dimension_member_aliases_tenant_isolation ON platform.dimension_member_aliases
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS dimension_members_write_scope ON platform.dimension_members;
DROP POLICY IF EXISTS dimension_members_read_scope ON platform.dimension_members;
CREATE POLICY dimension_members_tenant_isolation ON platform.dimension_members
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS semantic_tag_aliases_write_scope ON platform.semantic_tag_aliases;
DROP POLICY IF EXISTS semantic_tag_aliases_read_scope ON platform.semantic_tag_aliases;
CREATE POLICY semantic_tag_aliases_tenant_isolation ON platform.semantic_tag_aliases
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS metric_dependencies_write_scope ON platform.metric_dependencies;
DROP POLICY IF EXISTS metric_dependencies_read_scope ON platform.metric_dependencies;
CREATE POLICY metric_dependencies_tenant_isolation ON platform.metric_dependencies
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS metric_dimensions_write_scope ON platform.metric_dimensions;
DROP POLICY IF EXISTS metric_dimensions_read_scope ON platform.metric_dimensions;
CREATE POLICY metric_dimensions_tenant_isolation ON platform.metric_dimensions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS metric_versions_write_scope ON platform.metric_versions;
DROP POLICY IF EXISTS metric_versions_read_scope ON platform.metric_versions;
CREATE POLICY metric_versions_tenant_isolation ON platform.metric_versions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS dataset_draft_revisions_write_scope ON platform.dataset_draft_revisions;
DROP POLICY IF EXISTS dataset_draft_revisions_read_scope ON platform.dataset_draft_revisions;
CREATE POLICY dataset_draft_revisions_tenant_isolation ON platform.dataset_draft_revisions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS dataset_dependencies_write_scope ON platform.dataset_dependencies;
DROP POLICY IF EXISTS dataset_dependencies_read_scope ON platform.dataset_dependencies;
CREATE POLICY dataset_dependencies_tenant_isolation ON platform.dataset_dependencies
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS dataset_parameters_write_scope ON platform.dataset_parameters;
DROP POLICY IF EXISTS dataset_parameters_read_scope ON platform.dataset_parameters;
CREATE POLICY dataset_parameters_tenant_isolation ON platform.dataset_parameters
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS dataset_fields_write_scope ON platform.dataset_fields;
DROP POLICY IF EXISTS dataset_fields_read_scope ON platform.dataset_fields;
CREATE POLICY dataset_fields_tenant_isolation ON platform.dataset_fields
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS dataset_versions_write_scope ON platform.dataset_versions;
DROP POLICY IF EXISTS dataset_versions_read_scope ON platform.dataset_versions;
CREATE POLICY dataset_versions_tenant_isolation ON platform.dataset_versions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS metadata_columns_write_scope ON platform.metadata_columns;
DROP POLICY IF EXISTS metadata_columns_read_scope ON platform.metadata_columns;
CREATE POLICY metadata_columns_tenant_isolation ON platform.metadata_columns
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS metadata_tables_write_scope ON platform.metadata_tables;
DROP POLICY IF EXISTS metadata_tables_read_scope ON platform.metadata_tables;
CREATE POLICY metadata_tables_tenant_isolation ON platform.metadata_tables
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY IF EXISTS data_source_versions_write_scope ON platform.data_source_versions;
DROP POLICY IF EXISTS data_source_versions_read_scope ON platform.data_source_versions;
CREATE POLICY data_source_versions_tenant_isolation ON platform.data_source_versions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP FUNCTION IF EXISTS platform.semantic_tag_can_write(uuid);
DROP FUNCTION IF EXISTS platform.semantic_tag_can_read(uuid);
DROP FUNCTION IF EXISTS platform.dimension_can_write(uuid);
DROP FUNCTION IF EXISTS platform.dimension_can_read(uuid);
DROP FUNCTION IF EXISTS platform.metric_version_can_write(uuid);
DROP FUNCTION IF EXISTS platform.metric_version_can_read(uuid);
DROP FUNCTION IF EXISTS platform.metric_can_write(uuid);
DROP FUNCTION IF EXISTS platform.metric_can_read(uuid);
DROP FUNCTION IF EXISTS platform.dataset_version_can_write(uuid);
DROP FUNCTION IF EXISTS platform.dataset_version_can_read(uuid);
DROP FUNCTION IF EXISTS platform.dataset_can_write(uuid);
DROP FUNCTION IF EXISTS platform.dataset_can_read(uuid);
DROP FUNCTION IF EXISTS platform.metadata_table_can_write(uuid);
DROP FUNCTION IF EXISTS platform.metadata_table_can_read(uuid);
DROP FUNCTION IF EXISTS platform.data_source_can_write(uuid);
DROP FUNCTION IF EXISTS platform.data_source_can_read(uuid);

DROP POLICY IF EXISTS dimension_where_decisions_write_scope ON platform.dimension_where_decisions;
DROP POLICY IF EXISTS dimension_where_decisions_read_scope ON platform.dimension_where_decisions;
CREATE POLICY dimension_where_decisions_tenant_isolation
  ON platform.dimension_where_decisions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS semantic_term_assets_write_scope ON platform.semantic_term_assets;
DROP POLICY IF EXISTS semantic_term_assets_read_scope ON platform.semantic_term_assets;
CREATE POLICY semantic_term_assets_tenant_isolation
  ON platform.semantic_term_assets
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS semantic_dimensions_write_scope ON platform.semantic_dimensions;
DROP POLICY IF EXISTS semantic_dimensions_read_scope ON platform.semantic_dimensions;
CREATE POLICY semantic_dimensions_tenant_isolation
  ON platform.semantic_dimensions
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS semantic_tags_write_scope ON platform.semantic_tags;
DROP POLICY IF EXISTS semantic_tags_read_scope ON platform.semantic_tags;
CREATE POLICY semantic_tags_tenant_isolation
  ON platform.semantic_tags
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS metrics_write_scope ON platform.metrics;
DROP POLICY IF EXISTS metrics_read_scope ON platform.metrics;
CREATE POLICY metrics_tenant_isolation ON platform.metrics
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS datasets_delete_scope ON platform.datasets;
DROP POLICY IF EXISTS datasets_update_scope ON platform.datasets;
DROP POLICY IF EXISTS datasets_insert_scope ON platform.datasets;
DROP POLICY IF EXISTS datasets_read_scope ON platform.datasets;
CREATE POLICY datasets_tenant_isolation ON platform.datasets
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP POLICY IF EXISTS data_sources_delete_scope ON platform.data_sources;
DROP POLICY IF EXISTS data_sources_update_scope ON platform.data_sources;
DROP POLICY IF EXISTS data_sources_insert_scope ON platform.data_sources;
DROP POLICY IF EXISTS data_sources_read_scope ON platform.data_sources;
CREATE POLICY data_sources_tenant_isolation ON platform.data_sources
  USING(tenant_id=platform.current_tenant_id())
  WITH CHECK(tenant_id=platform.current_tenant_id());

DROP TRIGGER IF EXISTS dimension_where_decisions_inherit_scope
  ON platform.dimension_where_decisions;
DROP FUNCTION IF EXISTS platform.inherit_dimension_decision_scope();
DROP TRIGGER IF EXISTS semantic_dimensions_enforce_derived_scope
  ON platform.semantic_dimensions;
DROP TRIGGER IF EXISTS metrics_enforce_derived_scope ON platform.metrics;
DROP FUNCTION IF EXISTS platform.enforce_derived_asset_scope();

DROP INDEX IF EXISTS platform.semantic_term_assets_domain_scope_idx;
DROP INDEX IF EXISTS platform.semantic_dimensions_domain_scope_idx;
DROP INDEX IF EXISTS platform.metrics_domain_scope_idx;
DROP INDEX IF EXISTS platform.datasets_domain_scope_idx;
DROP INDEX IF EXISTS platform.data_sources_domain_scope_idx;

ALTER TABLE platform.dimension_where_decisions
  DROP CONSTRAINT IF EXISTS dimension_where_decisions_domain_fk,
  DROP COLUMN IF EXISTS sharing_scope,
  DROP COLUMN IF EXISTS domain_id;
ALTER TABLE platform.semantic_term_assets
  DROP CONSTRAINT IF EXISTS semantic_term_assets_domain_fk,
  DROP COLUMN IF EXISTS sharing_scope,
  DROP COLUMN IF EXISTS domain_id;
ALTER TABLE platform.semantic_dimensions
  DROP CONSTRAINT IF EXISTS semantic_dimensions_domain_fk,
  DROP COLUMN IF EXISTS sharing_scope,
  DROP COLUMN IF EXISTS domain_id;
ALTER TABLE platform.semantic_tags
  DROP CONSTRAINT IF EXISTS semantic_tags_domain_fk,
  DROP COLUMN IF EXISTS sharing_scope,
  DROP COLUMN IF EXISTS domain_id;
ALTER TABLE platform.metrics
  DROP CONSTRAINT IF EXISTS metrics_domain_fk,
  DROP COLUMN IF EXISTS sharing_scope,
  DROP COLUMN IF EXISTS domain_id;
ALTER TABLE platform.datasets
  DROP CONSTRAINT IF EXISTS datasets_domain_fk,
  DROP COLUMN IF EXISTS sharing_scope,
  DROP COLUMN IF EXISTS domain_id;
ALTER TABLE platform.data_sources
  DROP CONSTRAINT IF EXISTS data_sources_domain_fk,
  DROP COLUMN IF EXISTS sharing_scope,
  DROP COLUMN IF EXISTS domain_id;

DROP FUNCTION IF EXISTS platform.asset_can_write(uuid,uuid);
DROP FUNCTION IF EXISTS platform.asset_can_read(uuid,uuid,platform.asset_share_scope);
DROP FUNCTION IF EXISTS platform.user_is_asset_administrator();
DROP FUNCTION IF EXISTS platform.user_has_active_domain_membership(uuid);
DROP FUNCTION IF EXISTS platform.default_asset_share_scope();
DROP FUNCTION IF EXISTS platform.current_or_default_domain_id();
DROP FUNCTION IF EXISTS platform.is_system_access();
DROP FUNCTION IF EXISTS platform.current_domain_id();
DROP FUNCTION IF EXISTS platform.current_user_id();

DROP TABLE IF EXISTS platform.domain_memberships;
DROP TYPE IF EXISTS platform.asset_share_scope;

COMMIT;
