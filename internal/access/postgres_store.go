package access

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建 PostgreSQL 权限判定存储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Allowed 同时评估角色权限与对象级授权，任一命中即可放行。
func (s *PostgresStore) Allowed(ctx context.Context, check Check) (allowed bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, check.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (
          SELECT 1 FROM platform.user_roles ur
          JOIN platform.roles r ON r.tenant_id=ur.tenant_id AND r.id=ur.role_id AND r.status='ACTIVE' AND r.deleted_at IS NULL
          JOIN platform.role_permissions rp ON rp.tenant_id=ur.tenant_id AND rp.role_id=ur.role_id
          JOIN platform.permissions p ON p.tenant_id=rp.tenant_id AND p.id=rp.permission_id
          WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND p.resource_type=$3 AND p.action=$4
          UNION ALL
          SELECT 1 FROM platform.object_permissions op
          WHERE op.tenant_id=$1 AND op.object_type=$3 AND op.object_id=$5 AND op.action=$4
            AND (op.subject_type='USER' AND op.subject_id=$2 OR op.subject_type='ROLE' AND EXISTS (
              SELECT 1 FROM platform.user_roles ur JOIN platform.roles r ON r.tenant_id=ur.tenant_id AND r.id=ur.role_id
              WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND ur.role_id=op.subject_id AND r.status='ACTIVE' AND r.deleted_at IS NULL))
        )`, check.TenantID, check.UserID, check.ResourceType, check.Action, nullableUUID(check.ObjectID)).Scan(&allowed)
	})
	return allowed, err
}

// nullableUUID 将空对象标识转换为 SQL NULL，以匹配全局资源权限。
func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
