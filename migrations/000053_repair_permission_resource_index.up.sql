-- permissions_tenant_resource_idx is redundant with the unique
-- (tenant_id, resource_type, action) index. A stale copy can make an indexed
-- authorization lookup return a false negative even though the permission row
-- is visible to a sequential scan. Rebuild the authoritative unique index first,
-- then remove the redundant access path so every caller uses one consistent index.
REINDEX INDEX platform.permissions_tenant_id_resource_type_action_key;
DROP INDEX IF EXISTS platform.permissions_tenant_resource_idx;
