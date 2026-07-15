-- 为现有租户补齐数据资产权限，并按职责分配给内置角色。
INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action)
SELECT id,'data_asset.read','查看数据资产','DATA_ASSET','READ' FROM platform.tenants WHERE deleted_at IS NULL
ON CONFLICT(tenant_id,code) DO UPDATE SET name=EXCLUDED.name,resource_type=EXCLUDED.resource_type,action=EXCLUDED.action;

INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action)
SELECT id,'data_asset.manage','管理数据资产','DATA_ASSET','MANAGE' FROM platform.tenants WHERE deleted_at IS NULL
ON CONFLICT(tenant_id,code) DO UPDATE SET name=EXCLUDED.name,resource_type=EXCLUDED.resource_type,action=EXCLUDED.action;

INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id)
SELECT r.tenant_id,r.id,p.id FROM platform.roles r JOIN platform.permissions p ON p.tenant_id=r.tenant_id
WHERE r.code::text IN ('platform_admin','tenant_admin','data_admin') AND p.code::text IN ('data_asset.read','data_asset.manage')
ON CONFLICT DO NOTHING;

INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id)
SELECT r.tenant_id,r.id,p.id FROM platform.roles r JOIN platform.permissions p ON p.tenant_id=r.tenant_id
WHERE r.code::text IN ('analyst','report_designer') AND p.code::text='data_asset.read'
ON CONFLICT DO NOTHING;
