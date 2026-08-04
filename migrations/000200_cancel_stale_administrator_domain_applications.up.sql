-- 兼容早期开发版本：审批按权限范围判断，管理员申请不能在身份变更时被自动
-- 取消。平台管理员可审批全部申请，领域管理员只能审批自己管理的领域。
DROP TRIGGER IF EXISTS domain_administrator_cancel_stale_domain_applications
  ON platform.domain_memberships;
DROP TRIGGER IF EXISTS user_roles_cancel_stale_domain_applications
  ON platform.user_roles;
DROP FUNCTION IF EXISTS platform.cancel_pending_domain_applications_for_administrator();

COMMENT ON TABLE platform.domain_access_applications IS
  '领域准入申请；普通用户获批为 MEMBER，领域管理员跨领域申请须由平台管理员审批并保持 DOMAIN_ADMIN 身份';
