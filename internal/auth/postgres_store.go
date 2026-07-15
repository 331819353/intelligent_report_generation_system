package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建基于 PostgreSQL 的身份与会话存储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// FindTenantID 根据公开租户编码查询内部标识。
func (s *PostgresStore) FindTenantID(ctx context.Context, code string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT id FROM platform.tenants WHERE code = $1 AND status = 'ACTIVE' AND deleted_at IS NULL`, code).Scan(&id)
	return id, err
}

// FindUserByEmail 在指定租户内按邮箱加载登录用户。
func (s *PostgresStore) FindUserByEmail(ctx context.Context, tenantID, email string) (LoginUser, error) {
	return s.findUser(ctx, tenantID, `email = $1`, email)
}

// FindUserByID 在指定租户内按标识加载用户。
func (s *PostgresStore) FindUserByID(ctx context.Context, tenantID, userID string) (LoginUser, error) {
	return s.findUser(ctx, tenantID, `id = $1`, userID)
}

// findUser 复用用户查询与角色、权限聚合逻辑。
func (s *PostgresStore) findUser(ctx context.Context, tenantID, predicate string, value any) (LoginUser, error) {
	var user LoginUser
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		query := `SELECT id, tenant_id, email, display_name, password_hash, status, token_version FROM platform.users WHERE ` + predicate + ` AND deleted_at IS NULL`
		return tx.QueryRow(ctx, query, value).
			Scan(&user.ID, &user.TenantID, &user.Email, &user.DisplayName, &user.PasswordHash, &user.Status, &user.TokenVersion)
	})
	return user, err
}

// CreateSession 保存刷新令牌摘要及登录终端信息。
func (s *PostgresStore) CreateSession(ctx context.Context, session Session, userAgent, ipAddress string) error {
	return database.WithTenantTx(ctx, s.pool, session.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.auth_sessions(id, tenant_id, user_id, refresh_token_hash, user_agent, ip_address, expires_at) VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::inet,$7)`, session.ID, session.TenantID, session.UserID, session.RefreshTokenHash, userAgent, ipAddress, session.ExpiresAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id, actor_user_id, action, resource_type, resource_id, ip_address, user_agent) VALUES ($1,$2,'LOGIN','AUTH_SESSION',$3,NULLIF($4,'')::inet,$5)`, session.TenantID, session.UserID, session.ID, ipAddress, userAgent)
		return err
	})
}

// FindSession 加载会话及其关联用户的实时状态。
func (s *PostgresStore) FindSession(ctx context.Context, tenantID, sessionID string) (Session, error) {
	var session Session
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT s.id,s.tenant_id,s.user_id,s.refresh_token_hash,u.token_version,u.status,s.expires_at,s.revoked_at FROM platform.auth_sessions s JOIN platform.users u ON u.id=s.user_id AND u.tenant_id=s.tenant_id WHERE s.id=$1`, sessionID).
			Scan(&session.ID, &session.TenantID, &session.UserID, &session.RefreshTokenHash, &session.TokenVersion, &session.UserStatus, &session.ExpiresAt, &session.RevokedAt)
	})
	return session, err
}

// RotateSession 以旧摘要为并发条件原子替换刷新令牌。
func (s *PostgresStore) RotateSession(ctx context.Context, tenantID, sessionID string, oldHash, newHash []byte, expiresAt time.Time) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE platform.auth_sessions SET refresh_token_hash=$1,last_used_at=now(),expires_at=$2 WHERE id=$3 AND refresh_token_hash=$4 AND revoked_at IS NULL`, newHash, expiresAt, sessionID, oldHash)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("session rotation conflict")
		}
		return nil
	})
}

// RevokeSession 仅在令牌摘要匹配时撤销目标会话。
func (s *PostgresStore) RevokeSession(ctx context.Context, tenantID, sessionID string, tokenHash []byte, reason string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE platform.auth_sessions SET revoked_at=now(),revoke_reason=$1 WHERE id=$2 AND refresh_token_hash=$3 AND revoked_at IS NULL`, reason, sessionID, tokenHash)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("session not found")
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,action,resource_type,resource_id,detail) VALUES ($1,'LOGOUT','AUTH_SESSION',$2,jsonb_build_object('reason',$3::text))`, tenantID, sessionID, reason)
		return err
	})
}

// RecordLoginFailure 记录安全审计事件；审计失败不覆盖原始登录结果。
func (s *PostgresStore) RecordLoginFailure(ctx context.Context, tenantID, userID, email, requestID, ipAddress, userAgent string) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var actor any
		if userID != "" {
			actor = userID
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,request_id,ip_address,user_agent,result,detail) VALUES ($1,$2,'LOGIN','AUTH_SESSION',$3,NULLIF($4,'')::inet,$5,'FAILURE',jsonb_build_object('email',$6::text))`, tenantID, actor, requestID, ipAddress, userAgent, email)
		return err
	})
	if err != nil {
		_ = fmt.Sprintf("record login failure: %v", err)
	}
}
