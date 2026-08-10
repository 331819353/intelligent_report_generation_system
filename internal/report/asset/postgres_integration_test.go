package asset

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

func TestPostgresAssetLifecyclePermissionsAndTimeline(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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

	fixture := createAssetFixture(t, ctx, adminPool)
	defer cleanupAssetFixture(t, adminPool, fixture.tenantID)
	owner := store.Identity{TenantID: askdata.ID(fixture.tenantID), DomainID: askdata.ID(fixture.domainID), ActorID: askdata.ID(fixture.ownerID)}
	observer := store.Identity{TenantID: askdata.ID(fixture.tenantID), DomainID: askdata.ID(fixture.domainID), ActorID: askdata.ID(fixture.observerID)}
	reportStore := store.NewPostgresStore(appPool)
	definition := assetDefinition(t, fixture.reportID, fixture.code)
	if _, _, err := reportStore.CreateReport(ctx, owner, store.CreateInput{
		ID: askdata.ID(fixture.reportID), Code: fixture.code, Name: definition.Metadata.Name,
		ReportType: definition.Metadata.ReportType, Definition: definition,
	}); err != nil {
		t.Fatal(err)
	}

	repository := NewPostgresRepository(appPool)
	service := Service{Repository: repository}
	page, err := repository.List(ctx, owner, ListQuery{Scope: "all", Limit: 20})
	if err != nil || len(page.Items) != 1 || page.Items[0].Lifecycle != LifecycleDraftOnly || !containsAction(page.Items[0].AllowedActions, ActionArchive) {
		t.Fatalf("owner list = %#v, %v", page, err)
	}
	if page, err := repository.List(ctx, observer, ListQuery{Scope: "all", Limit: 20}); err != nil || len(page.Items) != 0 {
		t.Fatalf("ungranted observer list = %#v, %v", page, err)
	}

	grant, created, err := repository.Grant(ctx, owner, askdata.ID(fixture.reportID), GrantInput{
		SubjectType: "USER", SubjectID: askdata.ID(fixture.observerID), Action: ActionView,
	})
	if err != nil || !created || grant.SubjectName == "" {
		t.Fatalf("grant = %#v/%v, %v", grant, created, err)
	}
	if _, _, err := repository.Grant(ctx, observer, askdata.ID(fixture.reportID), GrantInput{
		SubjectType: "USER", SubjectID: askdata.ID(fixture.observerID), Action: ActionEdit,
	}); errorCode(err) != "REPORT_PERMISSION_FORBIDDEN" {
		t.Fatalf("observer grant error = %v", err)
	}
	observerPage, err := repository.List(ctx, observer, ListQuery{Scope: "shared", Search: fixture.code, Limit: 20})
	if err != nil || len(observerPage.Items) != 1 || !containsAction(observerPage.Items[0].AllowedActions, ActionView) || containsAction(observerPage.Items[0].AllowedActions, ActionEdit) {
		t.Fatalf("granted observer list = %#v, %v", observerPage, err)
	}

	var wait sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- service.Archive(ctx, owner, askdata.ID(fixture.reportID), "集成测试并发下架")
		}()
	}
	wait.Wait()
	close(results)
	success, conflict := 0, 0
	for result := range results {
		if result == nil {
			success++
		} else if errorCode(result) == "REPORT_ASSET_STATE_CONFLICT" {
			conflict++
		} else {
			t.Fatalf("archive error = %v", result)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("archive results success/conflict = %d/%d", success, conflict)
	}
	if _, err := reportStore.GetVersion(ctx, owner, askdata.ID(fixture.reportID), nil); !errors.Is(err, store.ErrReportOffline) {
		t.Fatalf("offline current version error = %v", err)
	}
	if err := service.Restore(ctx, owner, askdata.ID(fixture.reportID), "集成测试重新上架"); err != nil {
		t.Fatal(err)
	}

	events, err := repository.ListEvents(ctx, owner, askdata.ID(fixture.reportID), 100)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := map[string]bool{"CREATED": false, "PERMISSION_GRANTED": false, "ARCHIVED": false, "RESTORED": false}
	for _, event := range events {
		if _, tracked := wantEvents[event.EventType]; tracked {
			wantEvents[event.EventType] = true
		}
	}
	for eventType, found := range wantEvents {
		if !found {
			t.Fatalf("timeline %#v is missing %s", events, eventType)
		}
	}
	if _, err := repository.Revoke(ctx, owner, askdata.ID(fixture.reportID), grant.ID); err != nil {
		t.Fatal(err)
	}
	if page, err := repository.List(ctx, observer, ListQuery{Scope: "all", Limit: 20}); err != nil || len(page.Items) != 0 {
		t.Fatalf("revoked observer list = %#v, %v", page, err)
	}
}

type assetFixture struct{ tenantID, domainID, ownerID, observerID, reportID, code string }

func createAssetFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) assetFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := assetFixture{tenantID: uuid.NewString(), domainID: uuid.NewString(), ownerID: uuid.NewString(), observerID: uuid.NewString(), reportID: uuid.NewString(), code: "asset_" + suffix}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Report asset integration')`, fixture.tenantID, fixture.code); err != nil {
		t.Fatal(err)
	}
	for index, userID := range []string{fixture.ownerID, fixture.observerID} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(id,tenant_id,employee_no,email,display_name,password_hash,status)
			VALUES($1,$2,$3,$4,$5,'integration-only-not-a-login-secret','ACTIVE')`, userID, fixture.tenantID,
			strings.ToUpper(fixture.code)+string(rune('A'+index)), fixture.code+string(rune('a'+index))+"@example.invalid", []string{"资产 Owner", "资产观察者"}[index]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(id,tenant_id,code,name,is_default,created_by)
		VALUES($1,$2,$3,'Report asset integration',true,$4)`, fixture.domainID, fixture.tenantID, fixture.code, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{fixture.ownerID, fixture.observerID} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id,member_role,assigned_by,status)
			VALUES($1,$2,$3,'MEMBER',$4,'ACTIVE')`, fixture.tenantID, fixture.domainID, userID, fixture.ownerID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupAssetFixture(t *testing.T, pool *pgxpool.Pool, tenantID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Error(err)
		return
	}
	for _, table := range []string{"platform.report_asset_events", "platform.object_permissions", "platform.report_versions", "platform.report_revisions", "platform.report_drafts", "platform.reports", "platform.domain_memberships", "platform.business_domains", "platform.users", "platform.tenants"} {
		key := "tenant_id"
		if table == "platform.tenants" {
			key = "id"
		}
		if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE "+key+"=$1", tenantID); err != nil {
			t.Errorf("cleanup %s: %v", table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Error(err)
	}
}

func assetDefinition(t *testing.T, reportID, code string) reportmodel.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve asset test path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", "simple-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := reportmodel.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	definition.Metadata.ID = askdata.ID(reportID)
	definition.Metadata.Code = code
	definition.Metadata.Name = "报告资产治理集成测试"
	return definition
}

func errorCode(err error) string {
	var assetErr *Error
	if errors.As(err, &assetErr) {
		return assetErr.Code()
	}
	return ""
}
