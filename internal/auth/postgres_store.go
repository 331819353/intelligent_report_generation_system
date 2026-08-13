package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建基于 PostgreSQL 的身份与会话存储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// FindWorkspaceID 返回平台唯一的内部工作区标识。租户不再是登录输入；如果
// 数据库仍存在多个活动工作区则失败关闭，避免账号被解析到错误空间。
func (s *PostgresStore) FindWorkspaceID(ctx context.Context) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `SELECT min(id::text)
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		HAVING count(*)=1`).Scan(&id)
	return id, err
}

// FindUserByIdentifier 在指定租户内按工号或邮箱加载登录用户。
func (s *PostgresStore) FindUserByIdentifier(
	ctx context.Context, tenantID, identifier string,
) (LoginUser, error) {
	return s.findUser(ctx, tenantID, `(employee_no = $1 OR email = $1)`, identifier)
}

// FindUserByID 在指定租户内按标识加载用户。
func (s *PostgresStore) FindUserByID(ctx context.Context, tenantID, userID string) (LoginUser, error) {
	return s.findUser(ctx, tenantID, `id = $1`, userID)
}

func (s *PostgresStore) LoadCurrentProfile(ctx context.Context, tenantID, userID, domainID string) (CurrentProfile, error) {
	result := CurrentProfile{Roles: []string{}, DomainID: domainID}
	err := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id::text,employee_no::text,email::text,display_name,COALESCE(attributes->>'avatarUrl',''),status::text FROM platform.users WHERE tenant_id=$1 AND id=$2 AND deleted_at IS NULL`, tenantID, userID).Scan(&result.UserID, &result.EmployeeNo, &result.Email, &result.DisplayName, &result.AvatarURL, &result.Status); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT role_code FROM (
		  SELECT role.code::text AS role_code FROM platform.user_roles assignment JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id WHERE assignment.tenant_id=$1 AND assignment.user_id=$2 AND role.status='ACTIVE' AND role.deleted_at IS NULL
		  UNION
		  SELECT membership.member_role::text FROM platform.domain_memberships membership WHERE membership.tenant_id=$1 AND membership.user_id=$2 AND membership.domain_id=NULLIF($3,'')::uuid AND membership.status='ACTIVE'
		) roles ORDER BY role_code`, tenantID, userID, domainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var role string
			if err = rows.Scan(&role); err != nil {
				return err
			}
			result.Roles = append(result.Roles, role)
		}
		return rows.Err()
	})
	return result, err
}

func (s *PostgresStore) UpdateCurrentProfile(ctx context.Context, tenantID, userID, displayName string) error {
	return database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.users SET display_name=$1,updated_at=now() WHERE tenant_id=$2 AND id=$3 AND status='ACTIVE' AND deleted_at IS NULL`, displayName, tenantID, userID)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, errors.New("active user not found"))
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'UPDATE_PROFILE','USER',$2::text,jsonb_build_object('displayName',$3::text))`, tenantID, userID, displayName)
		return err
	})
}

func (s *PostgresStore) ChangePassword(ctx context.Context, tenantID, userID, passwordHash string) error {
	return database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.users SET password_hash=$1,token_version=token_version+1,updated_at=now() WHERE tenant_id=$2 AND id=$3 AND status='ACTIVE' AND deleted_at IS NULL`, passwordHash, tenantID, userID)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, errors.New("active user not found"))
		}
		if _, err = tx.Exec(ctx, `UPDATE platform.auth_sessions SET revoked_at=COALESCE(revoked_at,now()),revoke_reason=CASE WHEN revoked_at IS NULL THEN 'PASSWORD_CHANGED' ELSE revoke_reason END WHERE tenant_id=$1 AND user_id=$2`, tenantID, userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,'CHANGE_PASSWORD','USER',$2::text)`, tenantID, userID)
		return err
	})
}

// RegisterUser 原子创建账号及受限基础身份；领域归属必须另行申请或分配。
func (s *PostgresStore) RegisterUser(ctx context.Context, input RegisterUserRecord) error {
	tenantID, err := s.FindWorkspaceID(ctx)
	if err != nil {
		return ErrRegistrationUnavailable
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var registrationEnabled bool
		var roleCode string
		if err := tx.QueryRow(ctx, `SELECT
			COALESCE(lower(settings->>'selfRegistrationEnabled')<>'false',true),
			COALESCE(NULLIF(btrim(settings->>'selfRegistrationRoleCode'),''),'data_source_editor')
			FROM platform.tenants WHERE id=$1 AND status='ACTIVE' AND deleted_at IS NULL`, tenantID).
			Scan(&registrationEnabled, &roleCode); err != nil || !registrationEnabled {
			return ErrRegistrationUnavailable
		}
		var roleID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM platform.roles
			WHERE tenant_id=$2 AND code=$1 AND status='ACTIVE' AND deleted_at IS NULL`, roleCode, tenantID).Scan(&roleID); err != nil {
			return ErrRegistrationUnavailable
		}
		var userID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(
				tenant_id,employee_no,email,display_name,password_hash
			) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, tenantID, input.EmployeeNo,
			input.Email, input.DisplayName, input.PasswordHash).Scan(&userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(
				tenant_id,user_id,role_id,assigned_by
			) VALUES($1,$2,$3,NULL)`, tenantID, userID, roleID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1::uuid,$2::uuid,'REGISTER','USER',$2::text,jsonb_build_object(
				'roleCode',$3::text,'employeeNo',$4::text,'defaultDomainAssigned',false
			))`, tenantID, userID, roleCode, input.EmployeeNo)
		return err
	})
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && pgError.Code == "23505" {
		return ErrRegistrationConflict
	}
	return err
}

// ResolveBusinessDomain returns an active domain the user may enter. Platform
// administrators may enter every active domain; other users require an active
// membership in the requested domain.
func (s *PostgresStore) ResolveBusinessDomain(
	ctx context.Context, tenantID, userID, requestedDomainID string,
) (string, error) {
	var domainID string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT domain.id::text
			FROM platform.business_domains AS domain
			WHERE domain.tenant_id=$3::uuid
			  AND domain.status='ACTIVE'
			  AND domain.deleted_at IS NULL
			  AND ($2::text='' OR domain.id::text=$2)
			  AND (
			    EXISTS(
			      SELECT 1 FROM platform.user_roles AS assignment
			      JOIN platform.roles AS role
			        ON role.id=assignment.role_id
			       AND role.tenant_id=assignment.tenant_id
			      WHERE assignment.tenant_id=domain.tenant_id
			        AND assignment.user_id=$1::uuid
			        AND role.code::text='platform_admin'
			        AND role.status='ACTIVE'
			        AND role.deleted_at IS NULL
			    )
			    OR EXISTS(
			      SELECT 1 FROM platform.domain_memberships AS membership
			      WHERE membership.tenant_id=domain.tenant_id
			        AND membership.domain_id=domain.id
			        AND membership.user_id=$1::uuid
			        AND membership.status='ACTIVE'
			    )
			  )
			ORDER BY
			  CASE WHEN $2::text<>'' THEN 0
			       WHEN domain.is_default THEN 0 ELSE 1 END,
			  domain.name
			LIMIT 1`, userID, requestedDomainID, tenantID).Scan(&domainID)
	})
	if errors.Is(err, pgx.ErrNoRows) && requestedDomainID == "" {
		return "", nil
	}
	return domainID, err
}

// findUser 复用用户查询与角色、权限聚合逻辑。
func (s *PostgresStore) findUser(ctx context.Context, tenantID, predicate string, value any) (LoginUser, error) {
	var user LoginUser
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		query := `SELECT id,tenant_id,employee_no,email,display_name,password_hash,status,token_version FROM platform.users WHERE ` + predicate + ` AND deleted_at IS NULL`
		return tx.QueryRow(ctx, query, value).
			Scan(&user.ID, &user.TenantID, &user.EmployeeNo, &user.Email, &user.DisplayName, &user.PasswordHash, &user.Status, &user.TokenVersion)
	})
	return user, err
}

// CreateSession 保存刷新令牌摘要及登录终端信息。
func (s *PostgresStore) CreateSession(ctx context.Context, session Session, userAgent, ipAddress string) error {
	return database.WithTenantTx(ctx, s.pool, session.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.auth_sessions(
				id,tenant_id,user_id,business_domain_id,refresh_token_hash,
				user_agent,ip_address,expires_at
			) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,NULLIF($7,'')::inet,$8)`,
			session.ID, session.TenantID, session.UserID, session.DomainID,
			session.RefreshTokenHash, userAgent, ipAddress, session.ExpiresAt,
		); err != nil {
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
		return tx.QueryRow(ctx, `SELECT
				s.id,s.tenant_id,s.user_id,COALESCE(s.business_domain_id::text,''),
				s.refresh_token_hash,u.token_version,u.status,s.expires_at,s.revoked_at
			FROM platform.auth_sessions s
			JOIN platform.users u ON u.id=s.user_id AND u.tenant_id=s.tenant_id
			WHERE s.id=$1`, sessionID).
			Scan(
				&session.ID, &session.TenantID, &session.UserID, &session.DomainID,
				&session.RefreshTokenHash, &session.TokenVersion, &session.UserStatus,
				&session.ExpiresAt, &session.RevokedAt,
			)
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

// SetSessionDomain 将已验证的领域绑定到当前活动会话。
func (s *PostgresStore) SetSessionDomain(
	ctx context.Context, tenantID, sessionID, userID, domainID string,
) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `UPDATE platform.auth_sessions
			SET business_domain_id=NULLIF($1,'')::uuid,last_used_at=now()
			WHERE id=$2
			  AND user_id=$3
			  AND revoked_at IS NULL`,
			domainID, sessionID, userID,
		)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("active session was not found")
		}
		return nil
	})
}

// RecordLoginFailure 记录安全审计事件；审计失败不覆盖原始登录结果。
func (s *PostgresStore) RecordLoginFailure(ctx context.Context, tenantID, userID, identifier, requestID, ipAddress, userAgent string) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var actor any
		if userID != "" {
			actor = userID
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,request_id,ip_address,user_agent,result,detail) VALUES ($1,$2,'LOGIN','AUTH_SESSION',$3,NULLIF($4,'')::inet,$5,'FAILURE',jsonb_build_object('identifier',$6::text))`, tenantID, actor, requestID, ipAddress, userAgent, identifier)
		return err
	})
	if err != nil {
		_ = fmt.Sprintf("record login failure: %v", err)
	}
}
