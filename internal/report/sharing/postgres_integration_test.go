package sharing_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/authorization"
	"intelligent-report-generation-system/internal/report/sharing"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

func TestPostgresShareAuthorizationExpiryRevocationAndRLS(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	if adminURL == "" || appURL == "" || workerURL == "" {
		t.Skip("set AskData admin, app, and worker integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adminPool := openPool(t, ctx, adminURL)
	defer adminPool.Close()
	appPool := openPool(t, ctx, appURL)
	defer appPool.Close()
	workerPool := openPool(t, ctx, workerURL)
	defer workerPool.Close()

	fixture := createShareFixture(t, ctx, adminPool)
	defer cleanupShareFixture(t, adminPool, fixture)
	owner := reportstore.Identity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.ownerID),
		DomainID: askdata.ID(fixture.domainID),
	}
	viewer := reportstore.Identity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.viewerID),
		DomainID: askdata.ID(fixture.domainID),
	}
	outsider := reportstore.Identity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.outsiderID),
		DomainID: askdata.ID(fixture.domainID),
	}
	crossTenant := reportstore.Identity{
		TenantID: askdata.ID(fixture.otherTenantID), ActorID: askdata.ID(fixture.otherOwnerID),
		DomainID: askdata.ID(fixture.otherDomainID),
	}
	reportID := askdata.ID(fixture.reportID)
	definition := loadShareDefinition(t, fixture.reportID, fixture.code)
	reports := reportstore.NewPostgresStore(appPool)
	if _, _, err := reports.CreateReport(ctx, owner, reportstore.CreateInput{
		ID: reportID, Code: fixture.code, Name: definition.Metadata.Name,
		ReportType: definition.Metadata.ReportType, Definition: definition,
	}); err != nil {
		t.Fatalf("CreateReport() error = %v", err)
	}
	version, err := reports.CreateVersion(ctx, owner, reportID, reportstore.CreateVersionInput{
		ID: askdata.ID(uuid.NewString()), SourceRevisionNo: 0, Definition: definition,
		ObjectURI: "s3://share-integration/report-v1.json",
	})
	if err != nil {
		t.Fatalf("CreateVersion() error = %v", err)
	}
	if err := reports.CompletePublication(ctx, owner, reportID, version.ID); err != nil {
		t.Fatalf("CompletePublication() error = %v", err)
	}

	now := time.Now().UTC().Add(-time.Second)
	service := sharing.Service{
		Repository: sharing.NewPostgresRepository(appPool),
		Authorizer: authorization.NewPostgresAuthorizer(appPool), Versions: reports,
		Now: func() time.Time { return now },
	}
	created, err := service.Create(ctx, owner, sharing.CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, ReportVersionID: version.ID,
		Type: sharing.ShareInternalUser, PrincipalID: viewer.ActorID,
		FilterSnapshot: map[string]any{"region": "east"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, _, err := service.AccessShare(ctx, created.Token, viewer); shareCode(err) != "SHARE_NOT_FOUND" {
		t.Fatalf("token elevated viewer without report grant: %v", err)
	}
	if _, err := service.Create(ctx, viewer, sharing.CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, Type: sharing.ShareInternalUser,
		PrincipalID: viewer.ActorID,
	}); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("VIEW-less user created share: %v", err)
	}
	grantReportView(t, ctx, adminPool, fixture, fixture.viewerID)
	loaded, filters, err := service.AccessShare(ctx, created.Token, viewer)
	if err != nil || loaded.ID != version.ID || filters["region"] != "east" {
		t.Fatalf("granted AccessShare() = %#v, %#v, %v", loaded, filters, err)
	}
	assertShareAccessCount(t, ctx, adminPool, created.Record.ID, 1)
	groupShare, err := service.Create(ctx, owner, sharing.CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID,
		Type: sharing.ShareInternalGroup, PrincipalID: askdata.ID(fixture.groupID),
	})
	if err != nil {
		t.Fatalf("Create(group) error = %v", err)
	}
	if groupVersion, _, err := service.AccessShare(ctx, groupShare.Token, viewer); err != nil || groupVersion.ID != version.ID {
		t.Fatalf("group member AccessShare() = %#v, %v", groupVersion, err)
	}
	if _, _, err := service.AccessShare(ctx, created.Token, outsider); shareCode(err) != "SHARE_NOT_FOUND" {
		t.Fatalf("wrong principal saw share: %v", err)
	}
	if _, _, err := service.AccessShare(ctx, created.Token, crossTenant); shareCode(err) != "SHARE_NOT_FOUND" {
		t.Fatalf("cross-tenant actor saw share: %v", err)
	}

	expiresAt := now.Add(time.Hour)
	expiring, err := service.Create(ctx, owner, sharing.CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, Type: sharing.ShareInternalUser,
		PrincipalID: viewer.ActorID, ExpiresAt: &expiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	now = expiresAt
	if _, _, err := service.AccessShare(ctx, expiring.Token, viewer); shareCode(err) != "SHARE_EXPIRED" {
		t.Fatalf("runtime expiry without worker error = %v", err)
	}
	assertExpiredMarker(t, ctx, adminPool, expiring.Record.ID, false)
	worker := sharing.NewExpiryWorker(workerPool)
	if count, err := worker.ProcessTenant(ctx, fixture.tenantID, now, sharing.MaxShareExpiryBatch); err != nil || count != 1 {
		t.Fatalf("ProcessTenant() = %d, %v", count, err)
	}
	assertExpiredMarker(t, ctx, adminPool, expiring.Record.ID, true)

	now = expiresAt.Add(time.Minute)
	revocable, err := service.Create(ctx, owner, sharing.CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, Type: sharing.ShareInternalUser,
		PrincipalID: viewer.ActorID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Revoke(ctx, owner, revocable.Record.ID); err != nil {
		t.Fatalf("Revoke() error = %v", err)
	}
	if _, _, err := service.AccessShare(ctx, revocable.Token, viewer); shareCode(err) != "SHARE_NOT_FOUND" {
		t.Fatalf("revoked share remained accessible: %v", err)
	}

	assertPostgresCode(t, insertOverlongShare(ctx, adminPool, fixture, version.ID), "23514")
	assertPostgresCode(t, insertInvalidPrincipal(ctx, adminPool, fixture), "23514")
	assertPostgresCode(t, mutateShareToken(ctx, adminPool, created.Record.ID), "55000")
	if _, err := service.Create(ctx, owner, sharing.CreateRequest{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, Type: sharing.ShareExternalAccount,
		PrincipalID: viewer.ActorID,
	}); shareCode(err) != "SHARE_EXTERNAL_NOT_IMPLEMENTED" {
		t.Fatalf("external account create error = %v", err)
	}
}

type shareFixture struct {
	tenantID, domainID, ownerID, viewerID, outsiderID, groupID string
	otherTenantID, otherDomainID, otherOwnerID                 string
	reportID, code                                             string
}

func createShareFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) shareFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := shareFixture{
		tenantID: uuid.NewString(), domainID: uuid.NewString(), ownerID: uuid.NewString(),
		viewerID: uuid.NewString(), outsiderID: uuid.NewString(), otherTenantID: uuid.NewString(),
		groupID: uuid.NewString(), otherDomainID: uuid.NewString(), otherOwnerID: uuid.NewString(),
		reportID: uuid.NewString(), code: "share_" + suffix,
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, tenant := range []struct{ id, code string }{
		{fixture.tenantID, "share_" + suffix}, {fixture.otherTenantID, "share_other_" + suffix},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
			VALUES($1,$2,'Share integration')`, tenant.id, tenant.code); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range []struct{ tenantID, id, prefix string }{
		{fixture.tenantID, fixture.ownerID, "owner"}, {fixture.tenantID, fixture.viewerID, "viewer"},
		{fixture.tenantID, fixture.outsiderID, "outsider"},
		{fixture.otherTenantID, fixture.otherOwnerID, "other"},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,'Share integration','integration-only-not-a-login-secret','ACTIVE')`,
			user.id, user.tenantID, strings.ToUpper(user.prefix)+suffix,
			user.prefix+"."+suffix+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.roles(
		id,tenant_id,code,name,status
	) VALUES($1,$2,$3,'Share integration group','ACTIVE')`, fixture.groupID,
		fixture.tenantID, "share_group_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(
		tenant_id,user_id,role_id,assigned_by
	) VALUES($1,$2,$3,$4)`, fixture.tenantID, fixture.viewerID, fixture.groupID,
		fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	for _, domain := range []struct{ id, tenantID, ownerID, code string }{
		{fixture.domainID, fixture.tenantID, fixture.ownerID, "share_" + suffix},
		{fixture.otherDomainID, fixture.otherTenantID, fixture.otherOwnerID, "share_other_" + suffix},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
			id,tenant_id,code,name,is_default,created_by
		) VALUES($1,$2,$3,'Share integration',true,$4)`, domain.id, domain.tenantID,
			domain.code, domain.ownerID); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []struct{ tenantID, domainID, userID, assignedBy string }{
		{fixture.tenantID, fixture.domainID, fixture.ownerID, fixture.ownerID},
		{fixture.tenantID, fixture.domainID, fixture.viewerID, fixture.ownerID},
		{fixture.tenantID, fixture.domainID, fixture.outsiderID, fixture.ownerID},
		{fixture.otherTenantID, fixture.otherDomainID, fixture.otherOwnerID, fixture.otherOwnerID},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,member_role,assigned_by,status
		) VALUES($1,$2,$3,'MEMBER',$4,'ACTIVE')`, member.tenantID, member.domainID,
			member.userID, member.assignedBy); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupShareFixture(t *testing.T, pool *pgxpool.Pool, fixture shareFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable cleanup triggers: %v", err)
		return
	}
	for _, table := range []string{
		"platform.report_shares", "platform.report_version_dependencies",
		"platform.report_version_component_indexes", "platform.report_draft_dependencies",
		"platform.report_draft_component_indexes", "platform.report_versions",
		"platform.report_revisions", "platform.report_drafts", "platform.reports",
		"platform.object_permissions", "platform.domain_memberships", "platform.business_domains",
		"platform.user_roles", "platform.roles", "platform.users", "platform.tenants",
	} {
		column := "tenant_id"
		if table == "platform.tenants" {
			column = "id"
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s=ANY($1::uuid[])`, table, column),
			[]string{fixture.tenantID, fixture.otherTenantID}); err != nil {
			t.Errorf("cleanup %s: %v", table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit cleanup: %v", err)
	}
}

func loadShareDefinition(t *testing.T, reportID, code string) reportmodel.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve integration test path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api",
		"examples", "report-definition", "simple-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := reportmodel.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	definition.Metadata.ID = askdata.ID(reportID)
	definition.Metadata.Code = code
	definition.Metadata.Name = "Share integration"
	zone := &definition.Pages[0].Sections[0].Blocks[0].Zones[0]
	zone.Layout.Columns, zone.Layout.Rows = 4, 3
	zone.Slots[0].Grid.W, zone.Slots[0].Grid.H = 4, 3
	return definition
}

func openPool(t *testing.T, ctx context.Context, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func grantReportView(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture shareFixture, userID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO platform.object_permissions(
		tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
	) VALUES($1,'USER',$2,'REPORT',$3,'VIEW',$4)`, fixture.tenantID, userID,
		fixture.reportID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
}

func assertShareAccessCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id askdata.ID, want int64) {
	t.Helper()
	var got int64
	if err := pool.QueryRow(ctx, `SELECT access_count FROM platform.report_shares WHERE id=$1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("access_count = %d, want %d", got, want)
	}
}

func assertExpiredMarker(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id askdata.ID, want bool) {
	t.Helper()
	var got bool
	if err := pool.QueryRow(ctx, `SELECT expired_at IS NOT NULL FROM platform.report_shares WHERE id=$1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("expired marker = %v, want %v", got, want)
	}
}

func insertOverlongShare(ctx context.Context, pool *pgxpool.Pool, fixture shareFixture, versionID askdata.ID) error {
	_, err := pool.Exec(ctx, `INSERT INTO platform.report_shares(
		id,tenant_id,report_id,report_version_id,share_type,principal_id,share_token_hash,
		created_by,created_at,expires_at
	) VALUES($1,$2,$3,$4,'INTERNAL_USER',$5,$6,$5,$7::timestamptz,$7::timestamptz+interval '181 days')`,
		uuid.NewString(), fixture.tenantID, fixture.reportID, versionID, fixture.ownerID,
		strings.Repeat("a", 64), time.Now().UTC())
	return err
}

func insertInvalidPrincipal(ctx context.Context, pool *pgxpool.Pool, fixture shareFixture) error {
	_, err := pool.Exec(ctx, `INSERT INTO platform.report_shares(
		id,tenant_id,report_id,share_type,principal_id,share_token_hash,created_by,created_at,expires_at
	) VALUES($1,$2,$3,'INTERNAL_USER',$4,$5,$6,$7::timestamptz,$7::timestamptz+interval '1 day')`,
		uuid.NewString(), fixture.tenantID, fixture.reportID, uuid.NewString(), strings.Repeat("b", 64),
		fixture.ownerID, time.Now().UTC())
	return err
}

func mutateShareToken(ctx context.Context, pool *pgxpool.Pool, id askdata.ID) error {
	_, err := pool.Exec(ctx, `UPDATE platform.report_shares SET share_token_hash=$2 WHERE id=$1`, id,
		strings.Repeat("c", 64))
	return err
}

func assertPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var target *pgconn.PgError
	if !errors.As(err, &target) || target.Code != code {
		t.Fatalf("PostgreSQL error = %v, want SQLSTATE %s", err, code)
	}
}

func shareCode(err error) string {
	var target *sharing.Error
	if errors.As(err, &target) {
		return target.Code
	}
	return ""
}
