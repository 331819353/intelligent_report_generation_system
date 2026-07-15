package access

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type Role struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	System      bool   `json:"system"`
}
type ObjectGrant struct {
	SubjectType string `json:"subjectType"`
	SubjectID   string `json:"subjectId"`
	ObjectType  string `json:"objectType"`
	ObjectID    string `json:"objectId"`
	Action      string `json:"action"`
}

type AdminStore struct{ pool *pgxpool.Pool }

// NewAdminStore 创建角色和对象授权的管理存储。
func NewAdminStore(pool *pgxpool.Pool) *AdminStore { return &AdminStore{pool: pool} }

// ListRoles 返回租户角色及其已绑定权限代码。
func (s *AdminStore) ListRoles(ctx context.Context, tenantID string) ([]Role, error) {
	var roles []Role
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id,code,name,description,is_system,status FROM platform.roles WHERE deleted_at IS NULL ORDER BY is_system DESC,code`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Role
			if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Description, &r.System, &r.Status); err != nil {
				return err
			}
			roles = append(roles, r)
		}
		return rows.Err()
	})
	return roles, err
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
