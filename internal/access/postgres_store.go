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

// Allowed 按平台、领域、用户三级固定边界判定权限。平台管理员拥有全部权限，
// 但仍需明确选择领域以保持数据隔离；领域管理员只管理所属领域，普通用户可
// 配置但不能发布。
func (s *PostgresStore) Allowed(ctx context.Context, check Check) (allowed bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, check.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (
          SELECT 1
          FROM platform.user_roles AS assignment
          JOIN platform.roles AS role
            ON role.id=assignment.role_id
           AND role.tenant_id=assignment.tenant_id
          WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
            AND role.code::text='platform_admin'
            AND role.status='ACTIVE' AND role.deleted_at IS NULL
          UNION ALL
          SELECT 1
          FROM platform.domain_memberships AS membership
          JOIN platform.business_domains AS domain
            ON domain.id=membership.domain_id
           AND domain.tenant_id=membership.tenant_id
          WHERE membership.tenant_id=$1 AND membership.user_id=$2
            AND membership.domain_id=platform.current_domain_id()
            AND membership.status='ACTIVE'
            AND domain.status='ACTIVE' AND domain.deleted_at IS NULL
            AND $3 IN ('DATA_SOURCE','DATA_ASSET','DATASET')
            AND (
              membership.member_role='DOMAIN_ADMIN'
              OR $4 IN ('READ','MANAGE')
            )
        )`, check.TenantID, check.UserID, check.ResourceType, check.Action).Scan(&allowed)
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
