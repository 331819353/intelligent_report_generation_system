-- RPT-002: REPORT_AI_EDIT is an independent tenant capability in addition to
-- object-level REPORT EDIT. The store checks both before applying AI changes.
INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action,description)
SELECT id,'report.ai_edit','使用 AI 编辑报告','REPORT','AI_EDIT',
       '允许在具备报告对象 EDIT 权限时提交经审计且受 scope 限制的 AI 操作'
FROM platform.tenants
ON CONFLICT(tenant_id,code) DO UPDATE SET
  name=EXCLUDED.name,
  resource_type=EXCLUDED.resource_type,
  action=EXCLUDED.action,
  description=EXCLUDED.description;

INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id)
SELECT role.tenant_id,role.id,permission.id
FROM platform.roles role
JOIN platform.permissions permission
  ON permission.tenant_id=role.tenant_id AND permission.code='report.ai_edit'
WHERE role.code::text IN ('platform_admin','tenant_admin','data_admin')
ON CONFLICT DO NOTHING;
