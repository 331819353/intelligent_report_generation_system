package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/config"
	"intelligent-report-generation-system/internal/platform/database"
)

// main 创建本地开发所需的租户、管理员与初始权限，整个过程在单一事务中完成。
func main() {
	cfg, err := config.Load()
	if err != nil {
		fatal("load configuration", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal("connect database", err)
	}
	defer pool.Close()

	tenantCode := env("SEED_TENANT_CODE", "demo")
	tenantName := env("SEED_TENANT_NAME", "演示组织")
	email := env("SEED_ADMIN_EMAIL", "admin@example.com")
	password := os.Getenv("SEED_ADMIN_PASSWORD")
	if password == "" {
		fatal("seed admin", fmt.Errorf("SEED_ADMIN_PASSWORD is required"))
	}

	var tenantID string
	err = pool.QueryRow(ctx, `INSERT INTO platform.tenants(code,name) VALUES ($1,$2) ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name RETURNING id`, tenantCode, tenantName).Scan(&tenantID)
	if err != nil {
		fatal("upsert tenant", err)
	}
	hash, err := auth.NewPasswordManager(cfg.AuthBcryptCost).Hash(password)
	if err != nil {
		fatal("hash seed password", err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var adminID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash,status) VALUES ($1,$2,'系统管理员',$3,'ACTIVE') ON CONFLICT (tenant_id,email) DO UPDATE SET password_hash=EXCLUDED.password_hash,status='ACTIVE',token_version=platform.users.token_version+1,deleted_at=NULL RETURNING id`, tenantID, email, hash).Scan(&adminID); err != nil {
			return err
		}
		if err := seedAccess(ctx, tx, tenantID, adminID); err != nil {
			return err
		}
		return seedDevelopmentAI(ctx, tx, tenantID)
	})
	if err != nil {
		fatal("upsert seed admin", err)
	}
	fmt.Printf("seeded tenant=%s admin=%s\n", tenantCode, email)
}

// seedAccess 写入系统权限、管理员角色及用户绑定，重复执行时保持幂等。
func seedAccess(ctx context.Context, tx pgx.Tx, tenantID, adminID string) error {
	roles := []struct{ code, name string }{
		{"platform_admin", "平台管理员"}, {"tenant_admin", "租户管理员"}, {"data_admin", "数据管理员"},
		{"analyst", "分析师"}, {"report_designer", "报告设计师"}, {"viewer", "查看者"},
	}
	permissions := []struct{ code, name, resource, action string }{
		{"tenant.manage", "管理租户", "TENANT", "MANAGE"}, {"user.manage", "管理用户", "USER", "MANAGE"},
		{"data_source.manage", "管理数据源", "DATA_SOURCE", "MANAGE"}, {"dataset.read", "查看数据集", "DATASET", "READ"},
		{"data_asset.read", "查看数据资产", "DATA_ASSET", "READ"}, {"data_asset.manage", "管理数据资产", "DATA_ASSET", "MANAGE"},
		{"dataset.manage", "管理数据集", "DATASET", "MANAGE"}, {"dataset.publish", "审批发布数据集", "DATASET", "PUBLISH"},
		{"metric.read", "查看指标", "METRIC", "READ"}, {"metric.publish", "发布指标", "METRIC", "PUBLISH"},
		{"metric.manage", "管理指标", "METRIC", "MANAGE"}, {"report.read", "查看报告", "REPORT", "READ"},
		{"report.create", "创建报告", "REPORT", "CREATE"}, {"report.update", "编辑报告", "REPORT", "UPDATE"},
		{"report.publish", "发布报告", "REPORT", "PUBLISH"}, {"report.delete", "删除报告", "REPORT", "DELETE"},
	}
	roleIDs, permissionIDs := map[string]string{}, map[string]string{}
	for _, role := range roles {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.roles(tenant_id,code,name,is_system) VALUES ($1,$2,$3,true) ON CONFLICT (tenant_id,code) DO UPDATE SET name=EXCLUDED.name,status='ACTIVE',deleted_at=NULL RETURNING id`, tenantID, role.code, role.name).Scan(&id); err != nil {
			return err
		}
		roleIDs[role.code] = id
	}
	for _, permission := range permissions {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (tenant_id,code) DO UPDATE SET name=EXCLUDED.name,resource_type=EXCLUDED.resource_type,action=EXCLUDED.action RETURNING id`, tenantID, permission.code, permission.name, permission.resource, permission.action).Scan(&id); err != nil {
			return err
		}
		permissionIDs[permission.code] = id
	}
	grants := map[string][]string{
		"platform_admin": allPermissionCodes(permissions), "tenant_admin": allPermissionCodes(permissions),
		"data_admin":      {"data_source.manage", "data_asset.read", "data_asset.manage", "dataset.read", "dataset.manage", "dataset.publish", "metric.read", "metric.manage", "metric.publish", "report.read"},
		"analyst":         {"data_asset.read", "dataset.read", "metric.read", "report.read", "report.create"},
		"report_designer": {"data_asset.read", "dataset.read", "metric.read", "report.read", "report.create", "report.update", "report.publish"},
		"viewer":          {"report.read"},
	}
	for role, codes := range grants {
		for _, code := range codes {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id,granted_by) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tenantID, roleIDs[role], permissionIDs[code], adminID); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by) VALUES ($1,$2,$3,$2) ON CONFLICT DO NOTHING`, tenantID, adminID, roleIDs["platform_admin"])
	return err
}

// seedDevelopmentAI 只为本地演示租户启用通用 AI，并合并仍需独立授权的用途。
// 指标创建随通用 AI 开关启用，不写入 allowed_purposes。已有用途会被保留。
func seedDevelopmentAI(ctx context.Context, tx pgx.Tx, tenantID string) error {
	_, err := tx.Exec(ctx, `UPDATE platform.ai_tenant_policies
		SET enabled=true,
			allowed_purposes=ARRAY(
				SELECT DISTINCT requested.purpose
				FROM unnest(allowed_purposes || ARRAY['METADATA_COMPLETION','DATASET_DAG_GENERATION']::text[]) AS requested(purpose)
				ORDER BY requested.purpose
			)
		WHERE tenant_id=$1
			AND (NOT enabled OR NOT (allowed_purposes @> ARRAY['METADATA_COMPLETION','DATASET_DAG_GENERATION']::text[]))`, tenantID)
	return err
}

// allPermissionCodes 提取角色需要绑定的完整权限代码集合。
func allPermissionCodes(items []struct{ code, name, resource, action string }) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.code)
	}
	return result
}

// env 读取环境变量，并在未配置时返回开发环境默认值。
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// fatal 输出不可恢复错误并以非零状态结束种子进程。
func fatal(message string, err error) {
	slog.Error(message, "error", err)
	os.Exit(1)
}
