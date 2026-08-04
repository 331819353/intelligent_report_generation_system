BEGIN;

CREATE TYPE platform.domain_member_role AS ENUM ('MEMBER','DOMAIN_ADMIN');
CREATE TYPE platform.domain_application_status AS ENUM (
  'PENDING','APPROVED','REJECTED','CANCELLED'
);

ALTER TABLE platform.domain_memberships
  ADD COLUMN member_role platform.domain_member_role NOT NULL DEFAULT 'MEMBER';

-- 兼容升级：每个已有领域只保留一位现有平台管理员作为领域管理员。
-- 新领域自本迁移起必须由平台管理员显式指定至少一位领域管理员。
WITH designated AS (
  SELECT domain.tenant_id,domain.id AS domain_id,administrator.user_id
  FROM platform.business_domains AS domain
  JOIN LATERAL (
    SELECT assignment.user_id
    FROM platform.user_roles AS assignment
    JOIN platform.roles AS role
      ON role.id=assignment.role_id
     AND role.tenant_id=assignment.tenant_id
    JOIN platform.users AS user_account
      ON user_account.id=assignment.user_id
     AND user_account.tenant_id=assignment.tenant_id
    WHERE assignment.tenant_id=domain.tenant_id
      AND role.code::text='platform_admin'
      AND role.status='ACTIVE'
      AND role.deleted_at IS NULL
      AND user_account.status='ACTIVE'
      AND user_account.deleted_at IS NULL
    ORDER BY assignment.assigned_at,assignment.user_id
    LIMIT 1
  ) AS administrator ON true
  WHERE domain.deleted_at IS NULL
)
INSERT INTO platform.domain_memberships(
  tenant_id,domain_id,user_id,assigned_by,member_role,status
)
SELECT tenant_id,domain_id,user_id,user_id,'DOMAIN_ADMIN','ACTIVE'
FROM designated
ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE
SET member_role='DOMAIN_ADMIN',status='ACTIVE',updated_at=now();

-- 清理由旧模型自动铺到所有领域的平台/租户管理员成员关系。被上一步显式
-- 选为领域管理员的成员保留；其他管理员后续也必须申请或由平台管理员指定。
DELETE FROM platform.domain_memberships AS membership
USING platform.user_roles AS assignment,platform.roles AS role
WHERE assignment.tenant_id=membership.tenant_id
  AND assignment.user_id=membership.user_id
  AND role.tenant_id=assignment.tenant_id
  AND role.id=assignment.role_id
  AND role.code::text IN ('platform_admin','tenant_admin')
  AND membership.member_role<>'DOMAIN_ADMIN';

CREATE INDEX domain_memberships_admin_idx
  ON platform.domain_memberships(tenant_id,domain_id,user_id)
  WHERE status='ACTIVE' AND member_role='DOMAIN_ADMIN';

CREATE TABLE platform.domain_access_applications(
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id uuid NOT NULL REFERENCES platform.tenants(id),
  domain_id uuid NOT NULL,
  applicant_user_id uuid NOT NULL,
  status platform.domain_application_status NOT NULL DEFAULT 'PENDING',
  reason text NOT NULL DEFAULT '' CHECK(octet_length(reason)<=1000),
  review_comment text NOT NULL DEFAULT '' CHECK(octet_length(review_comment)<=1000),
  reviewed_by uuid,
  reviewed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(applicant_user_id,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE CASCADE,
  FOREIGN KEY(reviewed_by,tenant_id)
    REFERENCES platform.users(id,tenant_id) ON DELETE SET NULL (reviewed_by),
  CONSTRAINT domain_access_applications_review_shape CHECK(
    (status='PENDING' AND reviewed_by IS NULL AND reviewed_at IS NULL)
    OR (status='CANCELLED' AND reviewed_by IS NULL)
    OR (status IN ('APPROVED','REJECTED')
      AND reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)
  )
);

CREATE UNIQUE INDEX domain_access_applications_one_pending_idx
  ON platform.domain_access_applications(tenant_id,domain_id,applicant_user_id)
  WHERE status='PENDING';
CREATE INDEX domain_access_applications_review_queue_idx
  ON platform.domain_access_applications(tenant_id,domain_id,status,created_at);
CREATE INDEX domain_access_applications_applicant_idx
  ON platform.domain_access_applications(tenant_id,applicant_user_id,created_at DESC);
CREATE TRIGGER domain_access_applications_set_updated_at
BEFORE UPDATE ON platform.domain_access_applications
FOR EACH ROW EXECUTE FUNCTION platform.set_updated_at();

CREATE OR REPLACE FUNCTION platform.user_is_platform_administrator()
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.user_roles AS assignment
    JOIN platform.roles AS role
      ON role.id=assignment.role_id
     AND role.tenant_id=assignment.tenant_id
    WHERE assignment.tenant_id=platform.current_tenant_id()
      AND assignment.user_id=platform.current_user_id()
      AND role.code::text='platform_admin'
      AND role.status='ACTIVE'
      AND role.deleted_at IS NULL
  )
$$;

CREATE OR REPLACE FUNCTION platform.user_is_domain_administrator(
  target_domain_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT EXISTS(
    SELECT 1
    FROM platform.domain_memberships AS membership
    JOIN platform.business_domains AS domain
      ON domain.id=membership.domain_id
     AND domain.tenant_id=membership.tenant_id
    WHERE membership.tenant_id=platform.current_tenant_id()
      AND membership.user_id=platform.current_user_id()
      AND membership.domain_id=target_domain_id
      AND membership.status='ACTIVE'
      AND membership.member_role='DOMAIN_ADMIN'
      AND domain.status='ACTIVE'
      AND domain.deleted_at IS NULL
  )
$$;

ALTER TABLE platform.domain_access_applications ENABLE ROW LEVEL SECURITY;
ALTER TABLE platform.domain_access_applications FORCE ROW LEVEL SECURITY;
CREATE POLICY domain_access_applications_read_scope
  ON platform.domain_access_applications FOR SELECT
  USING(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR applicant_user_id=platform.current_user_id()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
    )
  );
CREATE POLICY domain_access_applications_insert_scope
  ON platform.domain_access_applications FOR INSERT
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND applicant_user_id=platform.current_user_id()
    AND status='PENDING'
  );
CREATE POLICY domain_access_applications_update_scope
  ON platform.domain_access_applications FOR UPDATE
  USING(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR platform.user_is_platform_administrator()
      OR platform.user_is_domain_administrator(domain_id)
      OR (applicant_user_id=platform.current_user_id() AND status='PENDING')
    )
  )
  WITH CHECK(tenant_id=platform.current_tenant_id());

-- 领域是最高业务数据边界。旧 PLATFORM 共享统一降为领域内共享，
-- 并通过约束阻止任何新写入再次扩大到跨领域范围。
UPDATE platform.data_sources SET sharing_scope='DOMAIN' WHERE sharing_scope='PLATFORM';
UPDATE platform.datasets SET sharing_scope='DOMAIN' WHERE sharing_scope='PLATFORM';
UPDATE platform.metrics SET sharing_scope='DOMAIN' WHERE sharing_scope='PLATFORM';
UPDATE platform.semantic_tags SET sharing_scope='DOMAIN' WHERE sharing_scope='PLATFORM';
UPDATE platform.semantic_dimensions SET sharing_scope='DOMAIN' WHERE sharing_scope='PLATFORM';
UPDATE platform.semantic_term_assets SET sharing_scope='DOMAIN' WHERE sharing_scope='PLATFORM';
UPDATE platform.dimension_where_decisions SET sharing_scope='DOMAIN' WHERE sharing_scope='PLATFORM';

ALTER TABLE platform.data_sources
  ADD CONSTRAINT data_sources_no_cross_domain_sharing CHECK(sharing_scope<>'PLATFORM');
ALTER TABLE platform.datasets
  ADD CONSTRAINT datasets_no_cross_domain_sharing CHECK(sharing_scope<>'PLATFORM');
ALTER TABLE platform.metrics
  ADD CONSTRAINT metrics_no_cross_domain_sharing CHECK(sharing_scope<>'PLATFORM');
ALTER TABLE platform.semantic_tags
  ADD CONSTRAINT semantic_tags_no_cross_domain_sharing CHECK(sharing_scope<>'PLATFORM');
ALTER TABLE platform.semantic_dimensions
  ADD CONSTRAINT semantic_dimensions_no_cross_domain_sharing CHECK(sharing_scope<>'PLATFORM');
ALTER TABLE platform.semantic_term_assets
  ADD CONSTRAINT semantic_term_assets_no_cross_domain_sharing CHECK(sharing_scope<>'PLATFORM');
ALTER TABLE platform.dimension_where_decisions
  ADD CONSTRAINT dimension_where_decisions_no_cross_domain_sharing CHECK(sharing_scope<>'PLATFORM');

CREATE OR REPLACE FUNCTION platform.asset_can_read(
  asset_domain_id uuid,
  asset_owner_user_id uuid,
  asset_scope platform.asset_share_scope
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      asset_domain_id=platform.current_domain_id()
      AND platform.user_has_active_domain_membership(asset_domain_id)
      AND (
        asset_scope='DOMAIN'
        OR asset_owner_user_id=platform.current_user_id()
        OR platform.user_is_domain_administrator(asset_domain_id)
      )
    )
$$;

CREATE OR REPLACE FUNCTION platform.asset_can_write(
  asset_domain_id uuid,
  asset_owner_user_id uuid
)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access()
    OR (
      asset_domain_id=platform.current_domain_id()
      AND platform.user_has_active_domain_membership(asset_domain_id)
      AND (
        asset_owner_user_id=platform.current_user_id()
        OR platform.user_is_domain_administrator(asset_domain_id)
      )
    )
$$;

-- 报表根记录绑定唯一领域，全部子表通过根记录继承同一隔离边界。
ALTER TABLE platform.reports ADD COLUMN domain_id uuid;
UPDATE platform.reports AS report
SET domain_id=domain.id
FROM platform.business_domains AS domain
WHERE domain.tenant_id=report.tenant_id AND domain.is_default;
ALTER TABLE platform.reports
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ADD CONSTRAINT reports_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
CREATE INDEX reports_domain_status_idx
  ON platform.reports(tenant_id,domain_id,status,updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE OR REPLACE FUNCTION platform.report_in_current_domain(target_report_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access() OR EXISTS(
    SELECT 1 FROM platform.reports AS report
    WHERE report.id=target_report_id
      AND report.tenant_id=platform.current_tenant_id()
      AND report.domain_id=platform.current_domain_id()
      AND platform.user_has_active_domain_membership(report.domain_id)
      AND report.deleted_at IS NULL
  )
$$;

DROP POLICY reports_tenant_isolation ON platform.reports;
CREATE POLICY reports_domain_isolation ON platform.reports
  USING(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR (
        domain_id=platform.current_domain_id()
        AND platform.user_has_active_domain_membership(domain_id)
      )
    )
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR (
        domain_id=platform.current_domain_id()
        AND platform.user_has_active_domain_membership(domain_id)
      )
    )
  );

DROP POLICY report_drafts_tenant_isolation ON platform.report_drafts;
CREATE POLICY report_drafts_domain_isolation ON platform.report_drafts
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_revisions_tenant_isolation ON platform.report_revisions;
CREATE POLICY report_revisions_domain_isolation ON platform.report_revisions
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_idempotency_records_tenant_isolation ON platform.report_idempotency_records;
CREATE POLICY report_idempotency_records_domain_isolation ON platform.report_idempotency_records
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_draft_component_indexes_tenant_isolation ON platform.report_draft_component_indexes;
CREATE POLICY report_draft_component_indexes_domain_isolation ON platform.report_draft_component_indexes
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_draft_dependencies_tenant_isolation ON platform.report_draft_dependencies;
CREATE POLICY report_draft_dependencies_domain_isolation ON platform.report_draft_dependencies
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_edit_guards_tenant_isolation ON platform.report_edit_guards;
CREATE POLICY report_edit_guards_domain_isolation ON platform.report_edit_guards
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_versions_tenant_isolation ON platform.report_versions;
CREATE POLICY report_versions_domain_isolation ON platform.report_versions
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_version_component_indexes_tenant_isolation ON platform.report_version_component_indexes;
CREATE POLICY report_version_component_indexes_domain_isolation ON platform.report_version_component_indexes
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_version_dependencies_tenant_isolation ON platform.report_version_dependencies;
CREATE POLICY report_version_dependencies_domain_isolation ON platform.report_version_dependencies
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));
DROP POLICY report_publication_idempotency_tenant_isolation ON platform.report_publication_idempotency;
CREATE POLICY report_publication_idempotency_domain_isolation ON platform.report_publication_idempotency
  USING(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.report_in_current_domain(report_id));

-- 问数运行同样绑定唯一领域；会话 ID 在不同领域出现也无法串联上下文。
ALTER TABLE platform.semantic_question_runs ADD COLUMN domain_id uuid;
UPDATE platform.semantic_question_runs AS run
SET domain_id=COALESCE(
  (
    SELECT membership.domain_id
    FROM platform.domain_memberships AS membership
    JOIN platform.business_domains AS member_domain
      ON member_domain.id=membership.domain_id
     AND member_domain.tenant_id=membership.tenant_id
    WHERE membership.tenant_id=run.tenant_id
      AND membership.user_id=run.actor_id
      AND membership.status='ACTIVE'
      AND member_domain.status='ACTIVE'
      AND member_domain.deleted_at IS NULL
    ORDER BY member_domain.is_default DESC,member_domain.name
    LIMIT 1
  ),
  (
    SELECT default_domain.id
    FROM platform.business_domains AS default_domain
    WHERE default_domain.tenant_id=run.tenant_id AND default_domain.is_default
    LIMIT 1
  )
);
ALTER TABLE platform.semantic_question_runs
  ALTER COLUMN domain_id SET DEFAULT platform.current_or_default_domain_id(),
  ALTER COLUMN domain_id SET NOT NULL,
  ADD CONSTRAINT semantic_question_runs_domain_fk FOREIGN KEY(domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);
CREATE INDEX semantic_question_runs_domain_recent_idx
  ON platform.semantic_question_runs(tenant_id,domain_id,created_at DESC,id);

CREATE OR REPLACE FUNCTION platform.question_run_in_current_domain(target_run_id uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
  SELECT platform.is_system_access() OR EXISTS(
    SELECT 1 FROM platform.semantic_question_runs AS run
    WHERE run.id=target_run_id
      AND run.tenant_id=platform.current_tenant_id()
      AND run.domain_id=platform.current_domain_id()
      AND platform.user_has_active_domain_membership(run.domain_id)
  )
$$;

DROP POLICY semantic_question_runs_tenant_isolation ON platform.semantic_question_runs;
CREATE POLICY semantic_question_runs_domain_isolation
  ON platform.semantic_question_runs
  USING(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR (
        domain_id=platform.current_domain_id()
        AND platform.user_has_active_domain_membership(domain_id)
      )
    )
  )
  WITH CHECK(
    tenant_id=platform.current_tenant_id()
    AND (
      platform.is_system_access()
      OR (
        domain_id=platform.current_domain_id()
        AND platform.user_has_active_domain_membership(domain_id)
      )
    )
  );
DROP POLICY semantic_question_run_events_tenant_isolation ON platform.semantic_question_run_events;
CREATE POLICY semantic_question_run_events_domain_isolation
  ON platform.semantic_question_run_events
  USING(tenant_id=platform.current_tenant_id() AND platform.question_run_in_current_domain(question_run_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.question_run_in_current_domain(question_run_id));
DROP POLICY semantic_question_artifacts_tenant_isolation ON platform.semantic_question_artifacts;
CREATE POLICY semantic_question_artifacts_domain_isolation
  ON platform.semantic_question_artifacts
  USING(tenant_id=platform.current_tenant_id() AND platform.question_run_in_current_domain(question_run_id))
  WITH CHECK(tenant_id=platform.current_tenant_id() AND platform.question_run_in_current_domain(question_run_id));

COMMENT ON COLUMN platform.domain_memberships.member_role IS
  'MEMBER=领域普通成员；DOMAIN_ADMIN=由平台管理员指定的领域管理员';
COMMENT ON TABLE platform.domain_access_applications IS
  '用户主动发起的入域申请，由目标领域管理员审批';
COMMENT ON COLUMN platform.reports.domain_id IS
  '报告唯一所属领域；不允许通过共享或对象授权跨领域访问';
COMMENT ON COLUMN platform.semantic_question_runs.domain_id IS
  '问数运行唯一所属领域；对话上下文只能在同一领域解析';

COMMIT;
