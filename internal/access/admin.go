package access

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type Role struct {
	ID              string   `json:"id"`
	Code            string   `json:"code"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Status          string   `json:"status"`
	System          bool     `json:"system"`
	PermissionCodes []string `json:"permissionCodes"`
	UserCount       int      `json:"userCount"`
}
type UserRole struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}
type UserDomain struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Default bool   `json:"default"`
}
type UserSummary struct {
	ID          string       `json:"id"`
	Email       string       `json:"email"`
	DisplayName string       `json:"displayName"`
	Status      string       `json:"status"`
	Roles       []UserRole   `json:"roles"`
	Domains     []UserDomain `json:"domains"`
	LastLoginAt *string      `json:"lastLoginAt,omitempty"`
}
type PermissionDefinition struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	ResourceType string `json:"resourceType"`
	Action       string `json:"action"`
	Description  string `json:"description"`
}
type BusinessDomain struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Default     bool   `json:"default"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"createdAt"`
}
type ObjectGrant struct {
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	ObjectType  string `json:"objectType"`
	ObjectID    string `json:"objectId"`
	Action      string `json:"action"`
}

type AdminStore struct{ pool *pgxpool.Pool }

var businessDomainCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

// NewAdminStore 创建角色和对象授权的管理存储。
func NewAdminStore(pool *pgxpool.Pool) *AdminStore { return &AdminStore{pool: pool} }

// ListRoles 返回租户角色及其已绑定权限代码。
func (s *AdminStore) ListRoles(ctx context.Context, tenantID string) ([]Role, error) {
	var roles []Role
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT
			  r.id,r.code,r.name,r.description,r.is_system,r.status,
			  COALESCE(
			    array_agg(p.code::text ORDER BY p.code)
			      FILTER (WHERE p.id IS NOT NULL),
			    ARRAY[]::text[]
			  ),
			  COUNT(DISTINCT ur.user_id)
			FROM platform.roles r
			LEFT JOIN platform.role_permissions rp
			  ON rp.tenant_id=r.tenant_id AND rp.role_id=r.id
			LEFT JOIN platform.permissions p
			  ON p.tenant_id=rp.tenant_id AND p.id=rp.permission_id
			LEFT JOIN platform.user_roles ur
			  ON ur.tenant_id=r.tenant_id AND ur.role_id=r.id
			WHERE r.deleted_at IS NULL
			GROUP BY r.id,r.code,r.name,r.description,r.is_system,r.status
			ORDER BY r.is_system DESC,r.code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Role
			if err := rows.Scan(
				&r.ID, &r.Code, &r.Name, &r.Description, &r.System, &r.Status,
				&r.PermissionCodes, &r.UserCount,
			); err != nil {
				return err
			}
			roles = append(roles, r)
		}
		return rows.Err()
	})
	return roles, err
}

// ListUsers 返回租户用户和角色绑定，用于管理中心分配职责。
func (s *AdminStore) ListUsers(ctx context.Context, tenantID string) ([]UserSummary, error) {
	var users []UserSummary
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT
			  u.id,u.email,u.display_name,u.status,u.last_login_at::text,
			  COALESCE((
			    SELECT jsonb_agg(
			      jsonb_build_object('id',role.id,'code',role.code,'name',role.name)
			      ORDER BY role.code
			    )
			    FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role
			      ON role.id=assignment.role_id
			     AND role.tenant_id=assignment.tenant_id
			    WHERE assignment.tenant_id=u.tenant_id
			      AND assignment.user_id=u.id
			      AND role.deleted_at IS NULL
			  ),'[]'::jsonb),
			  COALESCE((
			    SELECT jsonb_agg(
			      jsonb_build_object(
			        'id',domain.id,'code',domain.code,'name',domain.name,
			        'default',domain.is_default
			      )
			      ORDER BY domain.is_default DESC,domain.name
			    )
			    FROM platform.domain_memberships AS membership
			    JOIN platform.business_domains AS domain
			      ON domain.id=membership.domain_id
			     AND domain.tenant_id=membership.tenant_id
			    WHERE membership.tenant_id=u.tenant_id
			      AND membership.user_id=u.id
			      AND membership.status='ACTIVE'
			      AND domain.deleted_at IS NULL
			  ),'[]'::jsonb)
			FROM platform.users u
			WHERE u.deleted_at IS NULL
			ORDER BY u.created_at,u.email`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var user UserSummary
			var rolesJSON, domainsJSON []byte
			if err := rows.Scan(
				&user.ID, &user.Email, &user.DisplayName, &user.Status,
				&user.LastLoginAt, &rolesJSON, &domainsJSON,
			); err != nil {
				return err
			}
			if err := json.Unmarshal(rolesJSON, &user.Roles); err != nil {
				return err
			}
			if err := json.Unmarshal(domainsJSON, &user.Domains); err != nil {
				return err
			}
			users = append(users, user)
		}
		return rows.Err()
	})
	return users, err
}

// ListPermissions 返回租户内可授予角色的稳定权限目录。
func (s *AdminStore) ListPermissions(ctx context.Context, tenantID string) ([]PermissionDefinition, error) {
	var permissions []PermissionDefinition
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT code,name,resource_type,action,description
			FROM platform.permissions
			ORDER BY resource_type,action,code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var permission PermissionDefinition
			if err := rows.Scan(
				&permission.Code, &permission.Name, &permission.ResourceType,
				&permission.Action, &permission.Description,
			); err != nil {
				return err
			}
			permissions = append(permissions, permission)
		}
		return rows.Err()
	})
	return permissions, err
}

// ListDomains 返回当前租户的业务领域目录，所有已登录用户均可用于切换上下文。
func (s *AdminStore) ListDomains(
	ctx context.Context, tenantID, userID string,
) ([]BusinessDomain, error) {
	var domains []BusinessDomain
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT
			  domain.id,domain.code,domain.name,domain.description,domain.status,
			  domain.is_default,domain.version,domain.created_at::text
			FROM platform.domain_memberships AS membership
			JOIN platform.business_domains AS domain
			  ON domain.id=membership.domain_id
			 AND domain.tenant_id=membership.tenant_id
			WHERE membership.user_id=$1::uuid
			  AND membership.status='ACTIVE'
			  AND domain.deleted_at IS NULL
			ORDER BY domain.is_default DESC,domain.status,domain.name`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var domain BusinessDomain
			if err := rows.Scan(
				&domain.ID, &domain.Code, &domain.Name, &domain.Description,
				&domain.Status, &domain.Default, &domain.Version, &domain.CreatedAt,
			); err != nil {
				return err
			}
			domains = append(domains, domain)
		}
		return rows.Err()
	})
	return domains, err
}

// CreateDomain 创建可切换的租户业务领域并记录审计事件。
func (s *AdminStore) CreateDomain(
	ctx context.Context, tenantID, actorID, code, name, description string,
) (BusinessDomain, error) {
	var domain BusinessDomain
	code = strings.ToLower(strings.TrimSpace(code))
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if !businessDomainCodePattern.MatchString(code) {
		return domain, errors.New("domain code must start with a letter and contain 2-32 lowercase letters, numbers, _ or -")
	}
	if name == "" {
		return domain, errors.New("domain name is required")
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.business_domains(
				tenant_id,code,name,description,created_by
			) VALUES($1,$2,$3,$4,$5)
			RETURNING id,code,name,description,status,is_default,version,created_at::text`,
			tenantID, code, name, description, actorID,
		).Scan(
			&domain.ID, &domain.Code, &domain.Name, &domain.Description,
			&domain.Status, &domain.Default, &domain.Version, &domain.CreatedAt,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
				tenant_id,domain_id,user_id,assigned_by
			)
			SELECT DISTINCT $1,$2,assignment.user_id,$3
			FROM platform.user_roles AS assignment
			JOIN platform.roles AS role
			  ON role.id=assignment.role_id
			 AND role.tenant_id=assignment.tenant_id
			WHERE role.code::text IN ('platform_admin','tenant_admin')
			  AND role.status='ACTIVE'
			  AND role.deleted_at IS NULL
			UNION
			SELECT $1,$2,$3,$3
			ON CONFLICT DO NOTHING`,
			tenantID, domain.ID, actorID,
		); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'CREATE','BUSINESS_DOMAIN',$3,jsonb_build_object(
				'code',$4::text,'name',$5::text
			))`, tenantID, actorID, domain.ID, code, name)
		return err
	})
	return domain, err
}

// AssignUserDomain grants an active user access to one active domain.
func (s *AdminStore) AssignUserDomain(
	ctx context.Context, tenantID, actorID, userID, domainID string,
) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
				tenant_id,domain_id,user_id,assigned_by,status
			)
			SELECT $1,domain.id,user_account.id,$2,'ACTIVE'
			FROM platform.business_domains AS domain
			CROSS JOIN platform.users AS user_account
			WHERE domain.id=$3::uuid
			  AND domain.status='ACTIVE'
			  AND domain.deleted_at IS NULL
			  AND user_account.id=$4::uuid
			  AND user_account.status='ACTIVE'
			  AND user_account.deleted_at IS NULL
			ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE
			SET status='ACTIVE',assigned_by=EXCLUDED.assigned_by`,
			tenantID, actorID, domainID, userID,
		)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("active user or domain was not found")
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'ASSIGN_DOMAIN','USER',$3,jsonb_build_object(
				'domainId',$4::text
			))`, tenantID, actorID, userID, domainID)
		return err
	})
}

// RevokeUserDomain removes one membership but never leaves a user without an
// active domain.
func (s *AdminStore) RevokeUserDomain(
	ctx context.Context, tenantID, actorID, userID, domainID string,
) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var otherActiveCount int
		if err := tx.QueryRow(ctx, `SELECT count(*)
			FROM platform.domain_memberships AS membership
			JOIN platform.business_domains AS domain
			  ON domain.id=membership.domain_id
			 AND domain.tenant_id=membership.tenant_id
			WHERE membership.tenant_id=$1::uuid
			  AND membership.user_id=$2::uuid
			  AND membership.domain_id<>$3::uuid
			  AND membership.status='ACTIVE'
			  AND domain.status='ACTIVE'
			  AND domain.deleted_at IS NULL`,
			tenantID, userID, domainID,
		).Scan(&otherActiveCount); err != nil {
			return err
		}
		if otherActiveCount == 0 {
			return errors.New("user must retain at least one active domain")
		}
		result, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
			WHERE tenant_id=$1::uuid
			  AND user_id=$2::uuid
			  AND domain_id=$3::uuid`,
			tenantID, userID, domainID,
		)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("domain membership was not found")
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'REVOKE_DOMAIN','USER',$3,jsonb_build_object(
				'domainId',$4::text
			))`, tenantID, actorID, userID, domainID)
		return err
	})
}

// UpdateDomainStatus 启用或停用非默认领域。
func (s *AdminStore) UpdateDomainStatus(
	ctx context.Context, tenantID, actorID, id, status string,
) (BusinessDomain, error) {
	var domain BusinessDomain
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "ACTIVE" && status != "DISABLED" {
		return domain, errors.New("domain status must be ACTIVE or DISABLED")
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var isDefault bool
		if err := tx.QueryRow(ctx, `SELECT is_default FROM platform.business_domains
			WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&isDefault); err != nil {
			return err
		}
		if isDefault && status == "DISABLED" {
			return errors.New("default domain cannot be disabled")
		}
		if err := tx.QueryRow(ctx, `UPDATE platform.business_domains
			SET status=$2,version=version+1
			WHERE id=$1 AND tenant_id=$3
			RETURNING id,code,name,description,status,is_default,version,created_at::text`,
			id, status, tenantID,
		).Scan(
			&domain.ID, &domain.Code, &domain.Name, &domain.Description,
			&domain.Status, &domain.Default, &domain.Version, &domain.CreatedAt,
		); err != nil {
			return err
		}
		var revokedSessions int64
		if status == "DISABLED" {
			result, err := tx.Exec(ctx, `UPDATE platform.auth_sessions
				SET revoked_at=now(),revoke_reason='BUSINESS_DOMAIN_DISABLED'
				WHERE tenant_id=$1
				  AND business_domain_id=$2
				  AND revoked_at IS NULL`,
				tenantID, domain.ID,
			)
			if err != nil {
				return err
			}
			revokedSessions = result.RowsAffected()
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'UPDATE_STATUS','BUSINESS_DOMAIN',$3,jsonb_build_object(
				'status',$4::text,'revokedSessions',$5::bigint
			))`, tenantID, actorID, domain.ID, status, revokedSessions)
		return err
	})
	return domain, err
}

// CreateRole 创建租户自定义角色并记录审计事件。
func (s *AdminStore) CreateRole(ctx context.Context, tenantID, actorID, code, name, description string) (Role, error) {
	var role Role
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if code == "" || name == "" {
		return role, errors.New("role code and name are required")
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.roles(tenant_id,code,name,description) VALUES($1,$2,$3,$4) RETURNING id,code,name,description,is_system,status`, tenantID, code, name, description).Scan(&role.ID, &role.Code, &role.Name, &role.Description, &role.System, &role.Status); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'CREATE','ROLE',$3,jsonb_build_object('code',$4::text))`, tenantID, actorID, role.ID, code)
		return err
	})
	return role, err
}

// ReplaceRolePermissions 在事务中以新集合完整替换角色权限。
func (s *AdminStore) ReplaceRolePermissions(ctx context.Context, tenantID, actorID, roleID string, codes []string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var system bool
		if err := tx.QueryRow(ctx, `SELECT is_system FROM platform.roles WHERE id=$1 AND deleted_at IS NULL`, roleID).Scan(&system); err != nil {
			return err
		}
		if system {
			return errors.New("system role permissions are read-only")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.role_permissions WHERE role_id=$1`, roleID); err != nil {
			return err
		}
		if len(codes) > 0 {
			result, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id,granted_by) SELECT $1,$2,id,$3 FROM platform.permissions WHERE code=ANY($4::citext[])`, tenantID, roleID, actorID, codes)
			if err != nil {
				return err
			}
			if result.RowsAffected() != int64(len(codes)) {
				return errors.New("one or more permission codes are invalid")
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'UPDATE_PERMISSIONS','ROLE',$3,jsonb_build_object('permissionCodes',$4::text[]))`, tenantID, actorID, roleID, codes)
		return err
	})
}

// AssignUserRole 为租户用户分配角色，重复分配保持幂等。
func (s *AdminStore) AssignUserRole(ctx context.Context, tenantID, actorID, userID, roleID string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by) SELECT $1,u.id,r.id,$2 FROM platform.users u CROSS JOIN platform.roles r WHERE u.id=$3 AND u.deleted_at IS NULL AND r.id=$4 AND r.deleted_at IS NULL ON CONFLICT DO NOTHING`, tenantID, actorID, userID, roleID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("user or role not found, or assignment already exists")
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'ASSIGN_ROLE','USER',$3,jsonb_build_object('roleId',$4::text))`, tenantID, actorID, userID, roleID)
		return err
	})
}

// RevokeUserRole 解除用户与角色关系并写入审计日志。
func (s *AdminStore) RevokeUserRole(ctx context.Context, tenantID, actorID, userID, roleID string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `DELETE FROM platform.user_roles WHERE user_id=$1 AND role_id=$2`, userID, roleID)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'REVOKE_ROLE','USER',$3,jsonb_build_object('roleId',$4::text))`, tenantID, actorID, userID, roleID)
		return err
	})
}

// GrantObject 创建或更新用户、角色对具体对象的动作授权。
func (s *AdminStore) GrantObject(ctx context.Context, tenantID, actorID string, g ObjectGrant) (string, error) {
	var id string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.object_permissions(tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, tenantID, g.SubjectType, g.SubjectID, g.ObjectType, g.ObjectID, g.Action, actorID).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'GRANT_OBJECT','OBJECT_PERMISSION',$3,jsonb_build_object('objectType',$4::text,'objectId',$5::text,'subjectType',$6::text,'subjectId',$7::text,'action',$8::text))`, tenantID, actorID, id, g.ObjectType, g.ObjectID, g.SubjectType, g.SubjectID, g.Action)
		return err
	})
	return id, err
}

// RevokeObject 删除对象级授权并记录撤权审计。
func (s *AdminStore) RevokeObject(ctx context.Context, tenantID, actorID, id string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `DELETE FROM platform.object_permissions WHERE id=$1`, id)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return pgx.ErrNoRows
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id) VALUES($1,$2,'REVOKE_OBJECT','OBJECT_PERMISSION',$3)`, tenantID, actorID, id)
		return err
	})
}
