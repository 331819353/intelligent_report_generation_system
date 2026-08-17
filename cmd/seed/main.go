package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
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
	employeeNo := strings.ToUpper(strings.TrimSpace(env("SEED_ADMIN_EMPLOYEE_NO", "ADMIN001")))
	password := os.Getenv("SEED_ADMIN_PASSWORD")
	if password == "" {
		fatal("seed admin", fmt.Errorf("SEED_ADMIN_PASSWORD is required"))
	}
	ownerEmail := strings.ToLower(strings.TrimSpace(env("SEED_DOMAIN_OWNER_EMAIL", "biz.owner@example.com")))
	ownerEmployeeNo := strings.ToUpper(strings.TrimSpace(env("SEED_DOMAIN_OWNER_EMPLOYEE_NO", "BIZ001")))
	ownerDisplayName := strings.TrimSpace(env("SEED_DOMAIN_OWNER_DISPLAY_NAME", "企业经营负责人"))
	ownerPassword := env("SEED_DOMAIN_OWNER_PASSWORD", password)

	var tenantID string
	err = pool.QueryRow(ctx, `INSERT INTO platform.tenants(code,name) VALUES ($1,$2) ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name RETURNING id`, tenantCode, tenantName).Scan(&tenantID)
	if err != nil {
		fatal("upsert tenant", err)
	}
	passwords := auth.NewPasswordManager(cfg.AuthBcryptCost)
	hash, err := passwords.Hash(password)
	if err != nil {
		fatal("hash seed password", err)
	}
	ownerHash, err := passwords.Hash(ownerPassword)
	if err != nil {
		fatal("hash seed domain owner password", err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var adminID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(
			tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES ($1,$2,$3,'系统管理员',$4,'ACTIVE')
		ON CONFLICT (tenant_id,email) DO UPDATE SET
			employee_no=EXCLUDED.employee_no,password_hash=EXCLUDED.password_hash,
			status='ACTIVE',token_version=platform.users.token_version+1,deleted_at=NULL
		RETURNING id`, tenantID, employeeNo, email, hash).Scan(&adminID); err != nil {
			return err
		}
		if err := seedDomains(ctx, tx, tenantID, adminID); err != nil {
			return err
		}
		if err := seedAccess(ctx, tx, tenantID, adminID); err != nil {
			return err
		}
		if err := seedDomainOwner(
			ctx, tx, tenantID, adminID,
			ownerEmployeeNo, ownerEmail, ownerDisplayName, ownerHash,
		); err != nil {
			return err
		}
		if err := seedSemanticTaxonomy(ctx, tx, tenantID, adminID); err != nil {
			return err
		}
		return seedDevelopmentAI(ctx, tx, tenantID)
	})
	if err != nil {
		fatal("upsert seed admin", err)
	}
	fmt.Printf("seeded tenant=%s admin=%s domain_owner=%s\n", tenantCode, email, ownerEmail)
}

// seedDomainOwner creates the delegated domain-administrator account used to
// verify domain-scoped governance workflows independently from the global
// platform administrator.
func seedDomainOwner(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, administratorID, employeeNo, email, displayName, passwordHash string,
) error {
	var ownerID string
	if err := tx.QueryRow(ctx, `INSERT INTO platform.users(
			tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,$5,'ACTIVE')
		ON CONFLICT (tenant_id,email) DO UPDATE SET
			employee_no=EXCLUDED.employee_no,
			display_name=EXCLUDED.display_name,
			password_hash=EXCLUDED.password_hash,
			status='ACTIVE',
			token_version=platform.users.token_version+1,
			deleted_at=NULL
		RETURNING id`, tenantID, employeeNo, email, displayName, passwordHash).Scan(&ownerID); err != nil {
		return err
	}

	// The platform and business identities are mutually exclusive by design.
	// Remove a stale platform role before restoring this dedicated seed account.
	if _, err := tx.Exec(ctx, `DELETE FROM platform.user_roles AS assignment
		USING platform.roles AS role
		WHERE assignment.tenant_id=$1
		  AND assignment.user_id=$2
		  AND role.tenant_id=assignment.tenant_id
		  AND role.id=assignment.role_id
		  AND role.code='platform_admin'`, tenantID, ownerID); err != nil {
		return err
	}

	var editorRoleID string
	if err := tx.QueryRow(ctx, `SELECT id FROM platform.roles
		WHERE tenant_id=$1 AND code='data_source_editor'
		  AND status='ACTIVE' AND deleted_at IS NULL`, tenantID).Scan(&editorRoleID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(
			tenant_id,user_id,role_id,assigned_by
		) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		tenantID, ownerID, editorRoleID, administratorID,
	); err != nil {
		return err
	}

	var defaultDomainID string
	if err := tx.QueryRow(ctx, `SELECT id FROM platform.business_domains
		WHERE tenant_id=$1 AND is_default AND status='ACTIVE' AND deleted_at IS NULL
		ORDER BY created_at,id LIMIT 1`, tenantID).Scan(&defaultDomainID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
		WHERE tenant_id=$1 AND user_id=$2 AND status='ACTIVE' AND member_role='MEMBER'`,
		tenantID, ownerID,
	); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,assigned_by,status,member_role
		) VALUES($1,$2,$3,$4,'ACTIVE','DOMAIN_ADMIN')
		ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE SET
			assigned_by=EXCLUDED.assigned_by,
			status='ACTIVE',
			member_role='DOMAIN_ADMIN'`,
		tenantID, defaultDomainID, ownerID, administratorID,
	)
	return err
}

// seedSemanticTaxonomy guarantees that intelligent DIM/DWD modeling can bind
// its controlled classification result even when the tenant was created after
// the schema migration that introduced these tags.
func seedSemanticTaxonomy(ctx context.Context, tx pgx.Tx, tenantID, adminID string) error {
	_, err := tx.Exec(ctx, `WITH taxonomy(category,code,name,description) AS (
		VALUES
		  ('TABLE_FUNCTION','system.function.ods_fact','作用:ODS事实表','当前精确 ODS 版本的主要行粒度是原子事实、事件或周期快照'),
		  ('TABLE_FUNCTION','system.function.ods_dimension','作用:ODS维度表','当前精确 ODS 版本可形成一个或多个稳定实体维度'),
		  ('TABLE_FUNCTION','system.function.ods_fact_dimension','作用:ODS事实兼维度表','当前精确 ODS 版本既保留事实粒度，也可抽取稳定实体维度'),
		  ('TABLE_FUNCTION','system.function.ods_other','作用:ODS其他表','当前精确 ODS 版本未识别为事实表或维度表'),
		  ('TABLE_FUNCTION','system.function.fact_detail','作用:事实明细','保持业务事件或交易明细粒度'),
		  ('TABLE_FUNCTION','system.function.entity_dimension','作用:实体维度','提供稳定实体说明属性'),
		  ('TABLE_FUNCTION','system.function.subject_summary','作用:主题汇总','面向分析主题形成聚合结果'),
		  ('TABLE_FUNCTION','system.function.application_delivery','作用:应用交付','面向报表或应用场景交付'),
		  ('BUSINESS_ENTITY','system.entity.order','主题:订单','订单业务实体'),
		  ('BUSINESS_ENTITY','system.entity.order_item','主题:订单商品','订单商品行项目业务实体'),
		  ('BUSINESS_ENTITY','system.entity.after_sales','主题:售后服务','售后工单、退货与退款业务实体'),
		  ('BUSINESS_ENTITY','system.entity.payment','主题:支付','支付与结算业务实体'),
		  ('BUSINESS_ENTITY','system.entity.fulfillment','主题:履约','发货、配送与签收业务实体'),
		  ('BUSINESS_ENTITY','system.entity.customer','主题:客户','客户、用户或会员业务实体'),
		  ('BUSINESS_ENTITY','system.entity.product','主题:商品','商品、产品或 SKU 业务实体'),
		  ('BUSINESS_ENTITY','system.entity.store','主题:门店','门店或商户业务实体'),
		  ('BUSINESS_ENTITY','system.entity.inventory','主题:库存','库存记录业务实体'),
		  ('BUSINESS_ENTITY','system.entity.warehouse','主题:仓库','仓库业务实体'),
		  ('BUSINESS_ENTITY','system.entity.supplier','主题:供应商','供应商业务实体'),
		  ('BUSINESS_ENTITY','system.entity.employee','主题:员工','员工或人员业务实体'),
		  ('BUSINESS_ENTITY','system.entity.organization','主题:组织','组织或部门业务实体'),
			  ('BUSINESS_ENTITY','system.entity.channel','主题:渠道','销售、获客或投放渠道业务实体'),
			  ('BUSINESS_ENTITY','system.entity.campaign','主题:营销活动','营销活动业务实体'),
			  ('BUSINESS_PROCESS','system.process.sales','过程:销售','下单、成交与销售业务过程'),
			  ('BUSINESS_PROCESS','system.process.payment','过程:支付','支付与结算业务过程'),
			  ('BUSINESS_PROCESS','system.process.fulfillment','过程:履约','发货、配送与签收业务过程'),
			  ('BUSINESS_PROCESS','system.process.after_sales','过程:售后','售后、退货与退款业务过程'),
			  ('BUSINESS_PROCESS','system.process.customer_operations','过程:客户经营','获客、会员与客户运营过程'),
			  ('BUSINESS_PROCESS','system.process.product_management','过程:商品管理','商品、SKU 与品类管理过程'),
			  ('BUSINESS_PROCESS','system.process.store_operations','过程:门店经营','门店与商户经营过程'),
			  ('BUSINESS_PROCESS','system.process.inventory_management','过程:库存管理','入库、出库与库存管理过程'),
			  ('BUSINESS_PROCESS','system.process.procurement','过程:采购','采购与供应商协同过程'),
			  ('BUSINESS_PROCESS','system.process.marketing','过程:营销','营销活动、渠道与投放过程'),
		  ('USAGE_SCOPE','system.usage.operations','范围:运营分析','用于运营过程与效率分析'),
		  ('USAGE_SCOPE','system.usage.business','范围:经营分析','用于经营结果与趋势分析'),
		  ('USAGE_SCOPE','system.usage.finance','范围:财务分析','用于金额、收入、成本与结算分析'),
		  ('USAGE_SCOPE','system.usage.risk','范围:风险分析','用于异常、风险与合规分析'),
		  ('USAGE_SCOPE','system.usage.product','范围:商品分析','用于商品、SKU 与品类分析'),
		  ('USAGE_SCOPE','system.usage.fulfillment','范围:履约分析','用于发货、配送与履约分析'),
		  ('USAGE_SCOPE','system.usage.customer','范围:客户分析','用于客户、用户与会员分析'),
		  ('USAGE_SCOPE','system.usage.supply_chain','范围:供应链分析','用于采购、库存与供应保障分析'),
		  ('USAGE_SCOPE','system.usage.marketing','范围:营销分析','用于渠道、活动与获客分析'),
		  ('USAGE_SCOPE','system.usage.human_resources','范围:人力资源分析','用于员工、组织与人才分析'),
		  ('DATA_GRAIN','system.grain.order','粒度:订单','每行代表一个订单'),
		  ('DATA_GRAIN','system.grain.order_item','粒度:订单商品','每行代表一个订单商品行项目'),
		  ('DATA_GRAIN','system.grain.after_sales_ticket','粒度:售后工单','每行代表一个售后工单'),
		  ('DATA_GRAIN','system.grain.payment','粒度:支付','每行代表一笔支付或结算记录'),
		  ('DATA_GRAIN','system.grain.customer','粒度:客户','每行代表一个客户或用户'),
		  ('DATA_GRAIN','system.grain.product','粒度:商品','每行代表一个商品或 SKU'),
		  ('DATA_GRAIN','system.grain.store','粒度:门店','每行代表一个门店或商户'),
		  ('DATA_GRAIN','system.grain.inventory_record','粒度:库存记录','每行代表一个库存位置与商品组合记录'),
		  ('DATA_GRAIN','system.grain.warehouse','粒度:仓库','每行代表一个仓库'),
		  ('DATA_GRAIN','system.grain.supplier','粒度:供应商','每行代表一个供应商'),
		  ('DATA_GRAIN','system.grain.employee','粒度:员工','每行代表一个员工或人员'),
		  ('DATA_GRAIN','system.grain.organization','粒度:组织','每行代表一个组织或部门'),
		  ('DATA_GRAIN','system.grain.channel','粒度:渠道','每行代表一个业务渠道'),
		  ('DATA_GRAIN','system.grain.event','粒度:事件','每行代表一次业务事件'),
		  ('DATA_GRAIN','system.grain.day','粒度:自然日','每行代表一个自然日粒度结果'),
		  ('DATA_GRAIN','system.grain.month','粒度:自然月','每行代表一个自然月粒度结果'),
		  ('JOIN_ROLE','system.join.fact','关联:事实中心','作为明细或聚合分析的事实中心'),
		  ('JOIN_ROLE','system.join.dimension','关联:维度扩充','作为事实模型的维度说明来源'),
		  ('JOIN_ROLE','system.join.master','关联:主数据','作为稳定核心实体主数据来源')
	), default_domain AS (
		SELECT id FROM platform.business_domains
		WHERE tenant_id=$1 AND is_default AND status='ACTIVE' AND deleted_at IS NULL
		ORDER BY created_at,id LIMIT 1
	)
	INSERT INTO platform.semantic_tags(
		tenant_id,domain_id,sharing_scope,code,name,description,
		category,governance,status,created_by,updated_by
	)
	SELECT $1,default_domain.id,'PLATFORM',taxonomy.code,taxonomy.name,
		taxonomy.description,taxonomy.category,'CONTROLLED','ACTIVE',$2,$2
	FROM default_domain CROSS JOIN taxonomy
	ON CONFLICT(tenant_id,code) DO UPDATE SET
		name=EXCLUDED.name,description=EXCLUDED.description,
		category=EXCLUDED.category,governance='CONTROLLED',
		sharing_scope='PLATFORM',status='ACTIVE',updated_by=EXCLUDED.updated_by,
		updated_at=now()`, tenantID, adminID)
	return err
}

// seedDomains 创建本地演示工作空间的默认业务领域，重复执行保持幂等。
func seedDomains(ctx context.Context, tx pgx.Tx, tenantID, adminID string) error {
	domains := []struct {
		code, name, description string
		isDefault               bool
	}{
		{"enterprise", "企业经营", "企业级经营分析与管理驾驶舱", true},
		{"marketing", "营销运营", "市场活动、渠道与客户经营分析", false},
		{"supply-chain", "供应链", "采购、库存、履约与供应保障分析", false},
	}
	for _, domain := range domains {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
				tenant_id,code,name,description,is_default,created_by
			) VALUES($1,$2,$3,$4,$5,$6)
			ON CONFLICT (tenant_id,code) DO UPDATE SET
				name=EXCLUDED.name,
				description=EXCLUDED.description,
				status='ACTIVE',
				deleted_at=NULL`,
			tenantID, domain.code, domain.name, domain.description, domain.isDefault, adminID,
		); err != nil {
			return err
		}
	}
	return nil
}

// seedAccess 写入系统权限、管理员角色及用户绑定，重复执行时保持幂等。
func seedAccess(ctx context.Context, tx pgx.Tx, tenantID, adminID string) error {
	roles := []struct{ code, name string }{
		{"platform_admin", "平台管理员"}, {"tenant_admin", "租户管理员"}, {"data_admin", "数据管理员"},
		{"data_source_editor", "数据源配置员"}, {"data_viewer", "数据查看者"},
	}
	permissions := []struct{ code, name, resource, action string }{
		{"tenant.manage", "管理租户", "TENANT", "MANAGE"}, {"user.manage", "管理用户", "USER", "MANAGE"},
		{"data_source.manage", "管理数据源", "DATA_SOURCE", "MANAGE"}, {"data_source.publish", "审批发布数据源", "DATA_SOURCE", "PUBLISH"},
		{"dataset.read", "查看数据集", "DATASET", "READ"},
		{"data_asset.read", "查看数据资产", "DATA_ASSET", "READ"}, {"data_asset.manage", "管理数据资产", "DATA_ASSET", "MANAGE"},
		{"dataset.manage", "管理数据集", "DATASET", "MANAGE"}, {"dataset.publish", "审批发布数据集", "DATASET", "PUBLISH"},
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
		"data_admin":         {"data_source.manage", "data_source.publish", "data_asset.read", "data_asset.manage", "dataset.read", "dataset.manage", "dataset.publish"},
		"data_source_editor": {"data_source.manage", "data_asset.read", "data_asset.manage", "dataset.read"},
		"data_viewer":        {"data_asset.read", "dataset.read"},
	}
	for role, codes := range grants {
		for _, code := range codes {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id,granted_by) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`, tenantID, roleIDs[role], permissionIDs[code], adminID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(
			tenant_id,user_id,role_id,assigned_by
		) VALUES ($1,$2,$3,$2) ON CONFLICT DO NOTHING`,
		tenantID, adminID, roleIDs["platform_admin"],
	); err != nil {
		return err
	}
	// 平台管理员通过全局角色进入全部领域，不需要重复保存领域成员关系。
	_, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
		WHERE tenant_id=$1 AND user_id=$2`, tenantID, adminID)
	return err
}

// seedDevelopmentAI enables every governed AI workflow in the local demo
// tenant. Production tenants still opt in through the trusted administration
// path; this seed exists so the checked-in development product is end-to-end.
func seedDevelopmentAI(ctx context.Context, tx pgx.Tx, tenantID string) error {
	const governedPurposes = `ARRAY[
		'METADATA_COMPLETION','DATASET_DAG_GENERATION','DATASET_TAG_SUGGESTION',
		'DATASET_SEMANTIC_NAMING','DATA_SOURCE_CONFIGURATION','SEMANTIC_QUESTION',
		'REPORT_GENERATION','BLOCK_EDIT','CONCLUSION_GENERATION'
	]::text[]`
	_, err := tx.Exec(ctx, `UPDATE platform.ai_tenant_policies
		SET enabled=true,
			allowed_purposes=ARRAY(
				SELECT DISTINCT requested.purpose
				FROM unnest(allowed_purposes || `+governedPurposes+`) AS requested(purpose)
				ORDER BY requested.purpose
			)
		WHERE tenant_id=$1
			AND (NOT enabled OR NOT (allowed_purposes @> `+governedPurposes+`))`, tenantID)
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
