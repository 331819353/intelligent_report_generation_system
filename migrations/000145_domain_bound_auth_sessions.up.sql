BEGIN;

-- 会话绑定当前业务领域，使领域停用可以精确撤销正在该领域工作的会话。
ALTER TABLE platform.auth_sessions
  ADD COLUMN business_domain_id uuid,
  ADD CONSTRAINT auth_sessions_business_domain_fk
    FOREIGN KEY(business_domain_id,tenant_id)
    REFERENCES platform.business_domains(id,tenant_id);

-- 升级前的活动会话绑定到该用户仍可访问的默认/首个活动领域；
-- 已没有任何活动领域的会话立即失效。
UPDATE platform.auth_sessions AS session
SET business_domain_id=(
  SELECT domain.id AS domain_id
  FROM platform.domain_memberships AS membership
  JOIN platform.business_domains AS domain
    ON domain.id=membership.domain_id
   AND domain.tenant_id=membership.tenant_id
  WHERE membership.tenant_id=session.tenant_id
    AND membership.user_id=session.user_id
    AND membership.status='ACTIVE'
    AND domain.status='ACTIVE'
    AND domain.deleted_at IS NULL
  ORDER BY domain.is_default DESC,domain.name
  LIMIT 1
)
WHERE session.revoked_at IS NULL;

UPDATE platform.auth_sessions
SET revoked_at=now(),revoke_reason='NO_ACTIVE_BUSINESS_DOMAIN'
WHERE revoked_at IS NULL
  AND business_domain_id IS NULL;

CREATE INDEX auth_sessions_domain_active_idx
  ON platform.auth_sessions(tenant_id,business_domain_id)
  WHERE revoked_at IS NULL;

COMMENT ON COLUMN platform.auth_sessions.business_domain_id IS
  'Current business domain bound to this login session; disabled domains revoke matching sessions';

COMMIT;
