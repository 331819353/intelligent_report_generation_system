-- SEM-4S-003: 成员目标的导出解析器。
--
-- 原始成员材料（member_key/canonical_label）被列级 ACL 挡在 API 与 worker
-- 角色之外（见 scripts/migrate.sh 的敏感三表列授权）。词条与业务知识导出
-- 需要把 MEMBER 目标还原为可移植的 dimensionCode::memberKey，直接查列会在
-- 计划期就因权限失败——这是既有词条导出的潜在缺陷，业务知识分区第一次
-- 出现真实行时暴露了它。
--
-- 与其放宽列授权，这里提供一个 SECURITY DEFINER 解析器：只对 PUBLIC/
-- INTERNAL 成员返回目标文本，敏感成员返回 NULL——与导出端“省略敏感成员”
-- 的策略同一条边界，材料不因导出通道而泄露。
BEGIN;

CREATE OR REPLACE FUNCTION askdata.resolve_member_export_target(selected_member_version_id uuid)
RETURNS text
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path TO 'pg_catalog', 'askdata'
AS $$
  SELECT dimension.code::text||'::'||member.member_key
  FROM askdata.dimension_members AS member
  JOIN askdata.dimensions AS dimension ON dimension.id=member.dimension_version_id
  WHERE member.id=selected_member_version_id
    AND member.tenant_id=askdata.current_tenant_id()
    AND member.sensitivity IN ('PUBLIC','INTERNAL')
$$;

COMMENT ON FUNCTION askdata.resolve_member_export_target(uuid) IS
  '导出侧的成员目标解析：仅对 PUBLIC/INTERNAL 成员返回 dimensionCode::memberKey，敏感成员返回 NULL 并由导出端计入省略';

COMMIT;
