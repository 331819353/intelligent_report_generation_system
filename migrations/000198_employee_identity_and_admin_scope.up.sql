-- 用户同时拥有唯一工号与邮箱；平台管理员不保存领域成员关系。
ALTER TABLE platform.users ADD COLUMN employee_no citext;

UPDATE platform.users
SET employee_no='EMP'||upper(substr(replace(id::text,'-',''),1,12))
WHERE employee_no IS NULL;

ALTER TABLE platform.users
  ALTER COLUMN employee_no SET NOT NULL,
  ADD CONSTRAINT users_employee_no_not_blank CHECK(btrim(employee_no::text)<>''),
  ADD CONSTRAINT users_employee_no_format CHECK(
    employee_no::text ~ '^[A-Za-z0-9][A-Za-z0-9_-]{2,31}$'
  ),
  ADD CONSTRAINT users_tenant_employee_no_key UNIQUE(tenant_id,employee_no);

COMMENT ON COLUMN platform.users.employee_no IS
  '组织内唯一工号，登录时可与邮箱二选一作为账号标识';

-- 若历史领域只有平台管理员，优先从已有普通成员中补一名领域管理员；
-- 没有普通成员时从当前租户的活跃非平台用户中选择，随后移除全部平台管理员领域归属。
INSERT INTO platform.domain_memberships(
  tenant_id,domain_id,user_id,assigned_by,status,member_role
)
SELECT domain.tenant_id,domain.id,candidate.id,NULL,'ACTIVE','DOMAIN_ADMIN'
FROM platform.business_domains AS domain
CROSS JOIN LATERAL (
  SELECT user_account.id
  FROM platform.users AS user_account
  LEFT JOIN platform.domain_memberships AS existing_membership
    ON existing_membership.tenant_id=domain.tenant_id
   AND existing_membership.domain_id=domain.id
   AND existing_membership.user_id=user_account.id
   AND existing_membership.status='ACTIVE'
  WHERE user_account.tenant_id=domain.tenant_id
    AND user_account.status='ACTIVE'
    AND user_account.deleted_at IS NULL
    AND NOT EXISTS(
      SELECT 1
      FROM platform.user_roles AS assignment
      JOIN platform.roles AS role
        ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
      WHERE assignment.tenant_id=user_account.tenant_id
        AND assignment.user_id=user_account.id
        AND role.code::text='platform_admin'
        AND role.status='ACTIVE' AND role.deleted_at IS NULL
    )
  ORDER BY (existing_membership.user_id IS NOT NULL) DESC,user_account.created_at,user_account.id
  LIMIT 1
) AS candidate
WHERE domain.deleted_at IS NULL
  AND NOT EXISTS(
    SELECT 1
    FROM platform.domain_memberships AS administrator_membership
    WHERE administrator_membership.tenant_id=domain.tenant_id
      AND administrator_membership.domain_id=domain.id
      AND administrator_membership.status='ACTIVE'
      AND administrator_membership.member_role='DOMAIN_ADMIN'
      AND NOT EXISTS(
        SELECT 1
        FROM platform.user_roles AS assignment
        JOIN platform.roles AS role
          ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
        WHERE assignment.tenant_id=administrator_membership.tenant_id
          AND assignment.user_id=administrator_membership.user_id
          AND role.code::text='platform_admin'
          AND role.status='ACTIVE' AND role.deleted_at IS NULL
      )
  )
ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE
SET status='ACTIVE',member_role='DOMAIN_ADMIN',assigned_by=NULL;

UPDATE platform.auth_sessions AS session
SET business_domain_id=NULL,last_used_at=now()
WHERE business_domain_id IS NOT NULL
  AND EXISTS(
    SELECT 1
    FROM platform.user_roles AS assignment
    JOIN platform.roles AS role
      ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
    WHERE assignment.tenant_id=session.tenant_id
      AND assignment.user_id=session.user_id
      AND role.code::text='platform_admin'
      AND role.status='ACTIVE' AND role.deleted_at IS NULL
  );

DELETE FROM platform.domain_memberships AS membership
WHERE EXISTS(
  SELECT 1
  FROM platform.user_roles AS assignment
  JOIN platform.roles AS role
    ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
  WHERE assignment.tenant_id=membership.tenant_id
    AND assignment.user_id=membership.user_id
    AND role.code::text='platform_admin'
    AND role.status='ACTIVE' AND role.deleted_at IS NULL
);

CREATE OR REPLACE FUNCTION platform.reject_platform_admin_domain_membership()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF EXISTS(
    SELECT 1
    FROM platform.user_roles AS assignment
    JOIN platform.roles AS role
      ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
    WHERE assignment.tenant_id=NEW.tenant_id
      AND assignment.user_id=NEW.user_id
      AND role.code::text='platform_admin'
      AND role.status='ACTIVE' AND role.deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'platform administrator cannot belong to a business domain'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER domain_memberships_reject_platform_admin
BEFORE INSERT OR UPDATE OF user_id,status ON platform.domain_memberships
FOR EACH ROW WHEN(NEW.status='ACTIVE')
EXECUTE FUNCTION platform.reject_platform_admin_domain_membership();

CREATE OR REPLACE FUNCTION platform.reject_domain_member_platform_role()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,platform
AS $$
BEGIN
  IF EXISTS(
    SELECT 1 FROM platform.roles AS role
    WHERE role.id=NEW.role_id AND role.tenant_id=NEW.tenant_id
      AND role.code::text='platform_admin'
      AND role.status='ACTIVE' AND role.deleted_at IS NULL
  ) AND EXISTS(
    SELECT 1 FROM platform.domain_memberships AS membership
    WHERE membership.tenant_id=NEW.tenant_id
      AND membership.user_id=NEW.user_id
      AND membership.status='ACTIVE'
  ) THEN
    RAISE EXCEPTION 'domain member cannot be appointed as platform administrator'
      USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END
$$;

CREATE TRIGGER user_roles_reject_domain_member_platform_admin
BEFORE INSERT OR UPDATE OF user_id,role_id ON platform.user_roles
FOR EACH ROW
EXECUTE FUNCTION platform.reject_domain_member_platform_role();
