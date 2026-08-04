DROP TRIGGER IF EXISTS user_roles_reject_domain_member_platform_admin
  ON platform.user_roles;
DROP FUNCTION IF EXISTS platform.reject_domain_member_platform_role();
DROP TRIGGER IF EXISTS domain_memberships_reject_platform_admin
  ON platform.domain_memberships;
DROP FUNCTION IF EXISTS platform.reject_platform_admin_domain_membership();

ALTER TABLE platform.users
  DROP CONSTRAINT IF EXISTS users_tenant_employee_no_key,
  DROP CONSTRAINT IF EXISTS users_employee_no_format,
  DROP CONSTRAINT IF EXISTS users_employee_no_not_blank,
  DROP COLUMN IF EXISTS employee_no;
