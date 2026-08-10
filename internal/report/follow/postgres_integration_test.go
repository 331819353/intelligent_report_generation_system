package follow

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportauthorization "intelligent-report-generation-system/internal/report/authorization"
)

type followFixture struct {
	tenantID, domainID, ownerID, observerID string
	reportIDs, versionIDs                   []string
}

func TestPostgresReportFollowsRespectACLRevocationAndPagination(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	fixture := createFollowFixture(t, ctx, adminPool)
	defer cleanupFollowFixture(t, adminPool, fixture)

	store := NewStore(appPool)
	service, err := NewService(store, reportauthorization.NewPostgresAuthorizer(appPool))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time {
		now = now.Add(time.Minute)
		return now
	}
	identity := Identity{TenantID: askdata.ID(fixture.tenantID), DomainID: askdata.ID(fixture.domainID), ActorID: askdata.ID(fixture.ownerID)}
	requestContext := database.WithAccessContext(ctx, fixture.ownerID, fixture.domainID)
	for _, reportID := range fixture.reportIDs {
		if err = service.Follow(requestContext, identity, askdata.ID(reportID)); err != nil {
			t.Fatalf("follow %s: %v", reportID, err)
		}
	}
	first, err := service.List(requestContext, identity, 1, "")
	if err != nil || len(first.Items) != 1 || first.NextCursor == "" || first.Items[0].ReportID != askdata.ID(fixture.reportIDs[1]) {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := service.List(requestContext, identity, 1, first.NextCursor)
	if err != nil || len(second.Items) != 1 || second.Items[0].ReportID != askdata.ID(fixture.reportIDs[0]) {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	if _, err = service.List(requestContext, identity, 1, "invalid"); err != ErrInvalid {
		t.Fatalf("invalid cursor error = %v", err)
	}
	originalFollowedAt := second.Items[0].FollowedAt
	if err = service.Follow(requestContext, identity, askdata.ID(fixture.reportIDs[0])); err != nil {
		t.Fatalf("idempotent follow: %v", err)
	}
	all, err := service.List(requestContext, identity, 10, "")
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("all follows = %#v, %v", all, err)
	}
	for _, item := range all.Items {
		if item.ReportID == askdata.ID(fixture.reportIDs[0]) && !item.FollowedAt.Equal(originalFollowedAt) {
			t.Fatalf("idempotent follow changed followedAt: before=%s after=%s", originalFollowedAt, item.FollowedAt)
		}
	}
	observer := Identity{TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: askdata.ID(fixture.observerID)}
	observerContext := database.WithAccessContext(ctx, fixture.observerID, fixture.domainID)
	if err = service.Follow(observerContext, observer, askdata.ID(fixture.reportIDs[0])); err != ErrForbidden {
		t.Fatalf("unauthorized follow error = %v", err)
	}
	archiveFollowReport(t, ctx, adminPool, fixture.tenantID, fixture.reportIDs[1])
	all, err = service.List(requestContext, identity, 10, "")
	if err != nil || len(all.Items) != 1 || all.Items[0].ReportID != askdata.ID(fixture.reportIDs[0]) {
		t.Fatalf("revoked follow list = %#v, %v", all, err)
	}
	if err = service.Unfollow(requestContext, identity, askdata.ID(fixture.reportIDs[0])); err != nil {
		t.Fatalf("unfollow: %v", err)
	}
	if err = service.Unfollow(requestContext, identity, askdata.ID(fixture.reportIDs[0])); err != nil {
		t.Fatalf("idempotent unfollow: %v", err)
	}
}

func createFollowFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) followFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := followFixture{tenantID: uuid.NewString(), domainID: uuid.NewString(), ownerID: uuid.NewString(), observerID: uuid.NewString(), reportIDs: []string{uuid.NewString(), uuid.NewString()}, versionIDs: []string{uuid.NewString(), uuid.NewString()}}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Follow integration')`, fixture.tenantID, "follow_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.users(id,tenant_id,employee_no,email,display_name,password_hash,status) VALUES
		($1,$2,$3,$4,'Follow owner','integration-only-not-a-login-secret','ACTIVE'),
		($5,$2,$6,$7,'Follow observer','integration-only-not-a-login-secret','ACTIVE')`, fixture.ownerID, fixture.tenantID, "FOL"+suffix, "follow.owner."+suffix+"@example.invalid", fixture.observerID, "FOB"+suffix, "follow.observer."+suffix+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.business_domains(id,tenant_id,code,name,is_default,created_by) VALUES($1,$2,$3,'Follow integration',true,$4)`, fixture.domainID, fixture.tenantID, "follow_"+suffix, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id,member_role,assigned_by,status) VALUES
		($1,$2,$3,'MEMBER',$3,'ACTIVE'),($1,$2,$4,'MEMBER',$3,'ACTIVE')`, fixture.tenantID, fixture.domainID, fixture.ownerID, fixture.observerID); err != nil {
		t.Fatal(err)
	}
	for index := range fixture.reportIDs {
		if _, err = tx.Exec(ctx, `INSERT INTO platform.reports(id,tenant_id,domain_id,code,name,report_type,owner_user_id,status,created_by) VALUES($1,$2,$3,$4,$5,'REPORT',$6,'ACTIVE',$6)`, fixture.reportIDs[index], fixture.tenantID, fixture.domainID, fmt.Sprintf("follow_%s_%d", suffix, index), fmt.Sprintf("Follow report %d", index), fixture.ownerID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO platform.report_versions(id,tenant_id,report_id,version_no,source_revision_no,definition_json,definition_bytes,definition_hash,schema_version,object_uri,published_by,artifact_state) VALUES($1,$2,$3,1,0,'{}',2,$4,'1.0',$5,$6,'READY')`, fixture.versionIDs[index], fixture.tenantID, fixture.reportIDs[index], strings.Repeat(string(rune('a'+index)), 64), "reports/"+fixture.reportIDs[index]+"/v1.json", fixture.ownerID); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE platform.reports SET current_published_version_id=$1 WHERE tenant_id=$2 AND id=$3`, fixture.versionIDs[index], fixture.tenantID, fixture.reportIDs[index]); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func archiveFollowReport(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tenantID, reportID string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.report_asset_reason','follow access revoked',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE platform.reports SET status='ARCHIVED' WHERE tenant_id=$1 AND id=$2`, tenantID, reportID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func cleanupFollowFixture(t *testing.T, pool *pgxpool.Pool, fixture followFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable cleanup triggers: %v", err)
		return
	}
	for _, table := range []string{"platform.report_follows", "platform.report_asset_events", "platform.report_versions", "platform.reports", "platform.domain_memberships", "platform.business_domains", "platform.users", "platform.tenants"} {
		key := "tenant_id"
		if table == "platform.tenants" {
			key = "id"
		}
		if _, err = tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s=$1`, table, key), fixture.tenantID); err != nil {
			t.Errorf("cleanup %s: %v", table, err)
			return
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Errorf("commit cleanup: %v", err)
	}
}
