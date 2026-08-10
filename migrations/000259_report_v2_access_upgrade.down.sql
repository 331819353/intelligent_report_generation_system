DROP POLICY IF EXISTS report_v2_publish_idempotency_tenant_policy ON platform.report_publication_idempotency;
DROP POLICY IF EXISTS report_v2_version_write_policy ON platform.report_versions;
DROP POLICY IF EXISTS report_v2_version_read_policy ON platform.report_versions;
DROP POLICY IF EXISTS report_v2_revision_write_policy ON platform.report_revisions;
DROP POLICY IF EXISTS report_v2_revision_read_policy ON platform.report_revisions;
DROP POLICY IF EXISTS report_v2_draft_write_policy ON platform.report_drafts;
DROP POLICY IF EXISTS report_v2_draft_read_policy ON platform.report_drafts;
DROP POLICY IF EXISTS report_v2_delete_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_update_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_create_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_read_policy ON platform.reports;
CREATE POLICY report_v2_tenant_policy ON platform.reports
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_v2_draft_tenant_policy ON platform.report_drafts
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_v2_revision_tenant_policy ON platform.report_revisions
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
CREATE POLICY report_v2_version_tenant_policy ON platform.report_versions
  USING(tenant_id=platform.current_tenant_id()) WITH CHECK(tenant_id=platform.current_tenant_id());
DROP FUNCTION IF EXISTS platform.report_v2_can_access(uuid,text[]);
DROP FUNCTION IF EXISTS platform.report_v2_row_can_access(uuid,uuid,uuid,text[]);
