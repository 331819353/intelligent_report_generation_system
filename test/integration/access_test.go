//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/access"
	assetpkg "intelligent-report-generation-system/internal/asset"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
)

func TestPostgresRBACAndObjectPermissionMatrix(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantA, tenantB := insertTenant(t, ctx, pool, "it-a-"+suffix), insertTenant(t, ctx, pool, "it-b-"+suffix)
	t.Cleanup(func() { cleanupTenant(pool, tenantA); cleanupTenant(pool, tenantB) })

	var viewer, designer, roleViewer, roleDesigner, readPermission, updatePermission string
	err = database.WithTenantTx(ctx, pool, tenantA, func(tx pgx.Tx) error {
		for _, item := range []struct {
			email  string
			target *string
		}{{"viewer@it.test", &viewer}, {"designer@it.test", &designer}} {
			if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,$2,$3,'integration-hash') RETURNING id`, tenantA, item.email, item.email).Scan(item.target); err != nil {
				return err
			}
		}
		for _, item := range []struct {
			code   string
			target *string
		}{{"viewer", &roleViewer}, {"designer", &roleDesigner}} {
			if err := tx.QueryRow(ctx, `INSERT INTO platform.roles(tenant_id,code,name) VALUES($1,$2,$3) RETURNING id`, tenantA, item.code, item.code).Scan(item.target); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action) VALUES($1,'report.read','read','REPORT','READ') RETURNING id`, tenantA).Scan(&readPermission); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action) VALUES($1,'report.update','update','REPORT','UPDATE') RETURNING id`, tenantA).Scan(&updatePermission); err != nil {
			return err
		}
		for _, args := range [][]string{{viewer, roleViewer}, {designer, roleDesigner}} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id) VALUES($1,$2,$3)`, tenantA, args[0], args[1]); err != nil {
				return err
			}
		}
		for _, args := range [][]string{{roleViewer, readPermission}, {roleDesigner, updatePermission}} {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id) VALUES($1,$2,$3)`, tenantA, args[0], args[1]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	service := access.NewService(access.NewPostgresStore(pool))
	objectUser := "550e8400-e29b-41d4-a716-446655440000"
	objectRole := "550e8400-e29b-41d4-a716-446655440001"
	assertAllowed(t, ctx, service, access.Check{TenantID: tenantA, UserID: viewer, ResourceType: "REPORT", Action: "READ"}, true)
	assertAllowed(t, ctx, service, access.Check{TenantID: tenantA, UserID: viewer, ResourceType: "REPORT", Action: "UPDATE"}, false)
	err = database.WithTenantTx(ctx, pool, tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(tenant_id,subject_type,subject_id,object_type,object_id,action) VALUES($1,'USER',$2,'REPORT',$3,'UPDATE'),($1,'ROLE',$4,'REPORT',$5,'DELETE')`, tenantA, viewer, objectUser, roleViewer, objectRole)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAllowed(t, ctx, service, access.Check{TenantID: tenantA, UserID: viewer, ResourceType: "REPORT", Action: "UPDATE", ObjectID: objectUser}, true)
	assertAllowed(t, ctx, service, access.Check{TenantID: tenantA, UserID: viewer, ResourceType: "REPORT", Action: "DELETE", ObjectID: objectRole}, true)
	assertAllowed(t, ctx, service, access.Check{TenantID: tenantA, UserID: designer, ResourceType: "REPORT", Action: "DELETE", ObjectID: objectRole}, false)

	datasetID := "550e8400-e29b-41d4-a716-446655440010"
	err = database.WithTenantTx(ctx, pool, tenantA, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.users SET attributes='{"region_code":"CN-SH"}' WHERE id=$1`, viewer); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.data_row_policies(tenant_id,object_type,object_id,name,expression_dsl,applicable_role_ids) VALUES($1,'DATASET',$2,'viewer region','{"type":"EQUALS","left":{"type":"FIELD_REF","fieldCode":"region_code"},"right":{"type":"USER_ATTRIBUTE_REF","attribute":"region_code"}}',ARRAY[$3::uuid])`, tenantA, datasetID, roleViewer); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.data_column_policies(tenant_id,object_type,object_id,field_code,policy_type,allowed_aggregations,minimum_group_size,applicable_user_ids) VALUES($1,'DATASET',$2,'salary','AGGREGATE_ONLY',ARRAY['AVG'],10,ARRAY[$3::uuid])`, tenantA, datasetID, viewer)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	scope, rowPolicies, columnPolicies, err := policy.NewPostgresStore(pool).Load(ctx, tenantA, viewer, "DATASET", datasetID)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Attributes["region_code"] != "CN-SH" || len(rowPolicies) != 1 || len(columnPolicies) != 1 {
		t.Fatalf("unexpected loaded policy scope: %#v rows=%d columns=%d", scope, len(rowPolicies), len(columnPolicies))
	}
	filter, err := policy.CompileRows(rowPolicies, scope)
	if err != nil || len(filter.Args) != 1 || filter.Args[0] != "CN-SH" {
		t.Fatalf("unexpected row compilation: %#v err=%v", filter, err)
	}
	columnPlan, err := policy.CompileColumnPlan("salary", &columnPolicies[0], policy.QueryContext{Aggregation: "AVG"})
	if err != nil || columnPlan.Having != "COUNT(*) >= 10" {
		t.Fatalf("unexpected column plan: %#v err=%v", columnPlan, err)
	}
	_, designerRows, designerColumns, err := policy.NewPostgresStore(pool).Load(ctx, tenantA, designer, "DATASET", datasetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(designerRows) != 0 || len(designerColumns) != 0 {
		t.Fatal("user received policies outside its applicable scope")
	}
	var sourceID, tableID string
	err = database.WithTenantTx(ctx, pool, tenantA, func(tx pgx.Tx) error {
		var err error
		sourceID, _, err = insertVersionedDataSourceTx(
			ctx, tx, tenantA, "asset-it", "Asset IT", "MYSQL", "env://ASSET_IT", "DRAFT", "{}",
		)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at) VALUES($1,$2,'sales','orders','TABLE','hash',now()) RETURNING id`, tenantA, sourceID).Scan(&tableID)
	})
	if err != nil {
		t.Fatal(err)
	}
	assetRepo := assetpkg.NewRepository(pool)
	if _, err := assetRepo.GetTable(ctx, tenantA, tableID); err != nil {
		t.Fatalf("own tenant asset missing: %v", err)
	}
	if _, err := assetRepo.GetTable(ctx, tenantB, tableID); err == nil {
		t.Fatal("cross-tenant asset detail was visible")
	}
	foreignItems, total, err := assetRepo.SearchTables(ctx, tenantB, assetpkg.Search{Limit: 20})
	if err != nil || total != 0 || len(foreignItems) != 0 {
		t.Fatalf("cross-tenant asset search leaked data: total=%d items=%d err=%v", total, len(foreignItems), err)
	}

	err = database.WithTenantTx(ctx, pool, tenantB, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(tenant_id,subject_type,subject_id,object_type,object_id,action) VALUES($1,'USER',$2,'REPORT',$3,'READ')`, tenantB, viewer, objectUser)
		return err
	})
	if err == nil {
		t.Fatal("cross-tenant object permission subject was accepted")
	}
}

func insertTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, code string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO platform.tenants(code,name) VALUES($1,$2) RETURNING id`, code, code).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
func assertAllowed(t *testing.T, ctx context.Context, s *access.Service, c access.Check, want bool) {
	t.Helper()
	got, err := s.Allowed(ctx, c)
	if err != nil || got != want {
		t.Fatalf("check %#v got=%v err=%v want=%v", c, got, err, want)
	}
}
func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func cleanupTenant(pool *pgxpool.Pool, tenantID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"data_source_metadata_job_items", "data_source_metadata_jobs", "ai_metadata_suggestions", "ai_metadata_jobs", "asset_dependencies", "metadata_diffs", "metadata_snapshots", "metadata_columns", "metadata_tables", "data_sources", "object_permissions", "auth_sessions", "audit_logs", "users", "roles", "permissions", "data_row_policies", "data_column_policies"} {
			if _, err := tx.Exec(ctx, "DELETE FROM platform."+table+" WHERE tenant_id=$1", tenantID); err != nil {
				return err
			}
		}
		return nil
	})
	_, _ = pool.Exec(ctx, `DELETE FROM platform.tenants WHERE id=$1`, tenantID)
}
