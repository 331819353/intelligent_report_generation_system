BEGIN;

DROP POLICY semantic_question_artifacts_domain_isolation ON platform.semantic_question_artifacts;
CREATE POLICY semantic_question_artifacts_tenant_isolation ON platform.semantic_question_artifacts
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY semantic_question_run_events_domain_isolation ON platform.semantic_question_run_events;
CREATE POLICY semantic_question_run_events_tenant_isolation ON platform.semantic_question_run_events
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY semantic_question_runs_domain_isolation ON platform.semantic_question_runs;
CREATE POLICY semantic_question_runs_tenant_isolation ON platform.semantic_question_runs
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP FUNCTION platform.question_run_in_current_domain(uuid);
DROP INDEX platform.semantic_question_runs_domain_recent_idx;
ALTER TABLE platform.semantic_question_runs DROP CONSTRAINT semantic_question_runs_domain_fk;
ALTER TABLE platform.semantic_question_runs DROP COLUMN domain_id;

DROP POLICY report_publication_idempotency_domain_isolation ON platform.report_publication_idempotency;
CREATE POLICY report_publication_idempotency_tenant_isolation ON platform.report_publication_idempotency
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_version_dependencies_domain_isolation ON platform.report_version_dependencies;
CREATE POLICY report_version_dependencies_tenant_isolation ON platform.report_version_dependencies
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_version_component_indexes_domain_isolation ON platform.report_version_component_indexes;
CREATE POLICY report_version_component_indexes_tenant_isolation ON platform.report_version_component_indexes
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_versions_domain_isolation ON platform.report_versions;
CREATE POLICY report_versions_tenant_isolation ON platform.report_versions
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_edit_guards_domain_isolation ON platform.report_edit_guards;
CREATE POLICY report_edit_guards_tenant_isolation ON platform.report_edit_guards
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_draft_dependencies_domain_isolation ON platform.report_draft_dependencies;
CREATE POLICY report_draft_dependencies_tenant_isolation ON platform.report_draft_dependencies
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_draft_component_indexes_domain_isolation ON platform.report_draft_component_indexes;
CREATE POLICY report_draft_component_indexes_tenant_isolation ON platform.report_draft_component_indexes
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_idempotency_records_domain_isolation ON platform.report_idempotency_records;
CREATE POLICY report_idempotency_records_tenant_isolation ON platform.report_idempotency_records
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_revisions_domain_isolation ON platform.report_revisions;
CREATE POLICY report_revisions_tenant_isolation ON platform.report_revisions
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY report_drafts_domain_isolation ON platform.report_drafts;
CREATE POLICY report_drafts_tenant_isolation ON platform.report_drafts
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP POLICY reports_domain_isolation ON platform.reports;
CREATE POLICY reports_tenant_isolation ON platform.reports
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP FUNCTION platform.report_in_current_domain(uuid);
DROP INDEX platform.reports_domain_status_idx;
ALTER TABLE platform.reports DROP CONSTRAINT reports_domain_fk;
ALTER TABLE platform.reports DROP COLUMN domain_id;

CREATE OR REPLACE FUNCTION platform.asset_can_read(
  asset_domain_id uuid,asset_owner_user_id uuid,asset_scope platform.asset_share_scope
)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access() OR (
    platform.current_user_id() IS NOT NULL
    AND platform.current_domain_id() IS NOT NULL
    AND platform.user_has_active_domain_membership(platform.current_domain_id())
    AND (
      asset_scope='PLATFORM' OR (
        asset_domain_id=platform.current_domain_id() AND (
          asset_scope='DOMAIN' OR asset_owner_user_id=platform.current_user_id()
          OR platform.user_is_asset_administrator()
        )
      )
    )
  )
$$;
CREATE OR REPLACE FUNCTION platform.asset_can_write(
  asset_domain_id uuid,asset_owner_user_id uuid
)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access() OR (
    asset_domain_id=platform.current_domain_id()
    AND platform.user_has_active_domain_membership(asset_domain_id)
    AND (
      asset_owner_user_id=platform.current_user_id()
      OR platform.user_is_asset_administrator()
    )
  )
$$;

ALTER TABLE platform.dimension_where_decisions DROP CONSTRAINT dimension_where_decisions_no_cross_domain_sharing;
ALTER TABLE platform.semantic_term_assets DROP CONSTRAINT semantic_term_assets_no_cross_domain_sharing;
ALTER TABLE platform.semantic_dimensions DROP CONSTRAINT semantic_dimensions_no_cross_domain_sharing;
ALTER TABLE platform.semantic_tags DROP CONSTRAINT semantic_tags_no_cross_domain_sharing;
ALTER TABLE platform.metrics DROP CONSTRAINT metrics_no_cross_domain_sharing;
ALTER TABLE platform.datasets DROP CONSTRAINT datasets_no_cross_domain_sharing;
ALTER TABLE platform.data_sources DROP CONSTRAINT data_sources_no_cross_domain_sharing;

DROP POLICY domain_access_applications_update_scope ON platform.domain_access_applications;
DROP POLICY domain_access_applications_insert_scope ON platform.domain_access_applications;
DROP POLICY domain_access_applications_read_scope ON platform.domain_access_applications;
DROP TABLE platform.domain_access_applications;
DROP FUNCTION platform.user_is_domain_administrator(uuid);
DROP FUNCTION platform.user_is_platform_administrator();
DROP INDEX platform.domain_memberships_admin_idx;
ALTER TABLE platform.domain_memberships DROP COLUMN member_role;
DROP TYPE platform.domain_application_status;
DROP TYPE platform.domain_member_role;

COMMIT;
