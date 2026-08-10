-- Early Report V2 installations had tenant-only policies. Install the final
-- object/domain-aware access functions and replace those permissive policies.
CREATE OR REPLACE FUNCTION platform.report_v2_row_can_access(
  target_report_id uuid,
  target_domain_id uuid,
  target_owner_user_id uuid,
  required_actions text[]
)
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      platform.current_user_id() IS NOT NULL
      AND (
        target_domain_id IS NULL OR (
          target_domain_id=platform.current_domain_id()
          AND platform.user_has_active_domain_membership(target_domain_id)
        )
      )
      AND (
        target_owner_user_id=platform.current_user_id()
        OR platform.user_is_asset_administrator()
        OR (target_domain_id IS NOT NULL AND platform.user_is_domain_administrator(target_domain_id))
        OR EXISTS(
          SELECT 1 FROM platform.object_permissions permission
          WHERE permission.tenant_id=platform.current_tenant_id()
            AND permission.object_type='REPORT'
            AND permission.object_id=target_report_id
            AND permission.action=ANY(COALESCE(required_actions,ARRAY[]::text[]))
            AND (
              (permission.subject_type='USER' AND permission.subject_id=platform.current_user_id())
              OR (permission.subject_type='ROLE' AND EXISTS(
                SELECT 1 FROM platform.user_roles assignment
                JOIN platform.roles role ON role.id=assignment.role_id
                  AND role.tenant_id=assignment.tenant_id
                WHERE assignment.tenant_id=platform.current_tenant_id()
                  AND assignment.user_id=platform.current_user_id()
                  AND assignment.role_id=permission.subject_id
                  AND role.status='ACTIVE' AND role.deleted_at IS NULL
              ))
            )
        )
      )
    )
$$;

CREATE OR REPLACE FUNCTION platform.report_v2_can_access(target_report_id uuid,required_actions text[])
RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1 FROM platform.reports target
    WHERE target.id=target_report_id
      AND target.tenant_id=platform.current_tenant_id()
      AND platform.report_v2_row_can_access(
        target.id,target.domain_id,target.owner_user_id,required_actions
      )
  )
$$;

DROP POLICY IF EXISTS report_v2_tenant_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_draft_tenant_policy ON platform.report_drafts;
DROP POLICY IF EXISTS report_v2_revision_tenant_policy ON platform.report_revisions;
DROP POLICY IF EXISTS report_v2_version_tenant_policy ON platform.report_versions;
DROP POLICY IF EXISTS report_v2_publish_idempotency_tenant_policy ON platform.report_publication_idempotency;
DROP POLICY IF EXISTS report_v2_read_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_create_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_update_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_delete_policy ON platform.reports;
DROP POLICY IF EXISTS report_v2_draft_read_policy ON platform.report_drafts;
DROP POLICY IF EXISTS report_v2_draft_write_policy ON platform.report_drafts;
DROP POLICY IF EXISTS report_v2_revision_read_policy ON platform.report_revisions;
DROP POLICY IF EXISTS report_v2_revision_write_policy ON platform.report_revisions;
DROP POLICY IF EXISTS report_v2_version_read_policy ON platform.report_versions;
DROP POLICY IF EXISTS report_v2_version_write_policy ON platform.report_versions;

CREATE POLICY report_v2_read_policy ON platform.reports FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_create_policy ON platform.reports FOR INSERT
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR (
      owner_user_id=platform.current_user_id() AND created_by=platform.current_user_id()
      AND (domain_id IS NULL OR (
        domain_id=platform.current_domain_id()
        AND platform.user_has_active_domain_membership(domain_id)
      ))
    )
  ));
CREATE POLICY report_v2_update_policy ON platform.reports FOR UPDATE
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['EDIT','PUBLISH']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_delete_policy ON platform.reports FOR DELETE
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_row_can_access(
    id,domain_id,owner_user_id,ARRAY['EDIT']::text[]
  ));

CREATE POLICY report_v2_draft_read_policy ON platform.report_drafts FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_draft_write_policy ON platform.report_drafts FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ));
CREATE POLICY report_v2_revision_read_policy ON platform.report_revisions FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_revision_write_policy ON platform.report_revisions FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['EDIT']::text[]
  ));
CREATE POLICY report_v2_version_read_policy ON platform.report_versions FOR SELECT
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['VIEW','EDIT','PUBLISH']::text[]
  ));
CREATE POLICY report_v2_version_write_policy ON platform.report_versions FOR ALL
  USING(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['PUBLISH','EDIT']::text[]
  ))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_v2_can_access(
    report_id,ARRAY['PUBLISH','EDIT']::text[]
  ));
CREATE POLICY report_v2_publish_idempotency_tenant_policy ON platform.report_publication_idempotency
  USING(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR actor_user_id=platform.current_user_id()
  ) AND platform.report_v2_can_access(report_id,ARRAY['PUBLISH']::text[]))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND (
    platform.is_system_access() OR actor_user_id=platform.current_user_id()
  ) AND platform.report_v2_can_access(report_id,ARRAY['PUBLISH']::text[]));

REVOKE ALL ON FUNCTION platform.report_v2_row_can_access(uuid,uuid,uuid,text[]) FROM PUBLIC;
REVOKE ALL ON FUNCTION platform.report_v2_can_access(uuid,text[]) FROM PUBLIC;
