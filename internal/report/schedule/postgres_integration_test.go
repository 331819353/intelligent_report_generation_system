package schedule

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

func TestPostgresScheduleDeliveryLifecyclePermissionAndFailurePolicy(t *testing.T) {
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

	fixture := createScheduleFixture(t, ctx, adminPool)
	defer cleanupScheduleFixture(t, adminPool, fixture)
	identity := Identity{TenantID: askdata.ID(fixture.tenantID), DomainID: askdata.ID(fixture.domainID), ActorID: askdata.ID(fixture.ownerID)}
	requestContext := database.WithAccessContext(ctx, fixture.ownerID, fixture.domainID)
	authorizer := &scheduleAuthorizer{}
	store := NewPostgresStore(appPool)
	service, err := NewService(store, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, time.August, 10, 1, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	created, err := service.Create(requestContext, identity, askdata.ID(fixture.reportID), CreateInput{
		ReportVersionID: askdata.ID(fixture.versionID), Name: "Daily governed delivery",
		ScheduleKind: KindDaily, LocalTime: "10:00", Timezone: "Asia/Shanghai",
		BusinessCalendar: "CALENDAR_DAYS", MaxConsecutiveFailures: 1, MissAfterSeconds: 3600,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantNext := time.Date(2026, time.August, 10, 2, 0, 0, 0, time.UTC)
	if !created.NextRunAt.Equal(wantNext) || created.State != StateActive || len(created.Weekdays) != 0 {
		t.Fatalf("created schedule = %#v", created)
	}
	listed, err := service.List(requestContext, identity, askdata.ID(fixture.reportID), 10)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("List() = %#v, %v", listed, err)
	}
	if _, _, err = service.Get(database.WithAccessContext(ctx, fixture.ownerID, uuid.NewString()), Identity{
		TenantID: identity.TenantID, DomainID: askdata.ID(uuid.NewString()), ActorID: identity.ActorID,
	}, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-domain Get() error = %v", err)
	}

	paused, err := service.SetState(requestContext, identity, created.ID, VersionInput{ExpectedVersion: created.RecordVersion}, StatePaused)
	if err != nil || paused.State != StatePaused {
		t.Fatalf("pause = %#v, %v", paused, err)
	}
	if _, err = service.SetState(requestContext, identity, created.ID, VersionInput{ExpectedVersion: created.RecordVersion}, StateActive); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale resume error = %v", err)
	}
	resumed, err := service.SetState(requestContext, identity, created.ID, VersionInput{ExpectedVersion: paused.RecordVersion}, StateActive)
	if err != nil || resumed.State != StateActive || !resumed.NextRunAt.Equal(wantNext) {
		t.Fatalf("resume = %#v, %v", resumed, err)
	}
	subscription, err := service.Subscribe(requestContext, identity, created.ID, SubscriptionInput{RecipientUserID: identity.ActorID})
	if err != nil || subscription.Channel != "IN_APP" || subscription.State != "ACTIVE" {
		t.Fatalf("Subscribe() = %#v, %v", subscription, err)
	}

	worker, err := NewWorker(store, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	workerTime := base
	worker.now = func() time.Time { return workerTime }
	readyScheduledFor := base.Add(-time.Hour)
	if count, err := service.Backfill(requestContext, identity, created.ID, readyScheduledFor); err != nil || count != 1 {
		t.Fatalf("ready Backfill() = %d, %v", count, err)
	}
	if processed, err := worker.ProcessTenant(ctx, fixture.tenantID, 20); err != nil || processed != 1 {
		t.Fatalf("ready ProcessTenant() = %d, %v", processed, err)
	}
	deliveries, err := service.Deliveries(requestContext, identity, 20)
	if err != nil || len(deliveries) != 1 || deliveries[0].State != "READY" || deliveries[0].ReportLink == "" || deliveries[0].AccessCheckedAt == nil {
		t.Fatalf("ready deliveries = %#v, %v", deliveries, err)
	}
	read, err := service.MarkDeliveryRead(requestContext, identity, deliveries[0].ID)
	if err != nil || read.ReadAt == nil {
		t.Fatalf("MarkDeliveryRead() = %#v, %v", read, err)
	}

	authorizer.setViewDenied(true)
	permissionScheduledFor := base.Add(-2 * time.Hour)
	if count, err := service.Backfill(requestContext, identity, created.ID, permissionScheduledFor); err != nil || count != 1 {
		t.Fatalf("permission Backfill() = %d, %v", count, err)
	}
	workerTime = base.Add(time.Minute)
	if _, err := worker.ProcessTenant(ctx, fixture.tenantID, 20); err != nil {
		t.Fatal(err)
	}
	deliveries, err = service.Deliveries(requestContext, identity, 20)
	if err != nil || deliveryState(deliveries, permissionScheduledFor) != "SKIPPED:NO_PERMISSION" {
		t.Fatalf("permission-revoked deliveries = %#v, %v", deliveries, err)
	}
	authorizer.setViewDenied(false)

	archiveScheduleReport(t, ctx, adminPool, fixture)
	unavailableScheduledFor := base.Add(-3 * time.Hour)
	if count, err := service.Backfill(requestContext, identity, created.ID, unavailableScheduledFor); err != nil || count != 1 {
		t.Fatalf("unavailable Backfill() = %d, %v", count, err)
	}
	for _, advance := range []time.Duration{2 * time.Minute, 5 * time.Minute, 9 * time.Minute, 17 * time.Minute, 34 * time.Minute} {
		workerTime = base.Add(advance)
		if _, err := worker.ProcessTenant(ctx, fixture.tenantID, 20); err != nil {
			t.Fatalf("unavailable ProcessTenant(%s): %v", advance, err)
		}
	}
	final, _, err := service.Get(requestContext, identity, created.ID)
	if err != nil || final.State != StatePaused || final.ConsecutiveFailures != 1 || final.LastFailureCode != "REPORT_VERSION_UNAVAILABLE" {
		t.Fatalf("auto-paused schedule = %#v, %v", final, err)
	}
	deliveries, err = service.Deliveries(requestContext, identity, 20)
	if err != nil || deliveryState(deliveries, unavailableScheduledFor) != "FAILED:REPORT_VERSION_UNAVAILABLE" {
		t.Fatalf("unavailable deliveries = %#v, %v", deliveries, err)
	}
}

type scheduleAuthorizer struct {
	mu         sync.RWMutex
	viewDenied bool
}

func (a *scheduleAuthorizer) CheckReportView(context.Context, reportstore.Identity, askdata.ID) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.viewDenied {
		return ErrForbidden
	}
	return nil
}
func (a *scheduleAuthorizer) CheckReportEdit(context.Context, reportstore.Identity, askdata.ID) error {
	return nil
}
func (a *scheduleAuthorizer) setViewDenied(value bool) {
	a.mu.Lock()
	a.viewDenied = value
	a.mu.Unlock()
}

type scheduleFixture struct{ tenantID, domainID, ownerID, reportID, versionID string }

func createScheduleFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) scheduleFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := scheduleFixture{tenantID: uuid.NewString(), domainID: uuid.NewString(), ownerID: uuid.NewString(), reportID: uuid.NewString(), versionID: uuid.NewString()}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Schedule integration')`, fixture.tenantID, "schedule_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.users(id,tenant_id,employee_no,email,display_name,password_hash,status) VALUES($1,$2,$3,$4,'Schedule owner','integration-only-not-a-login-secret','ACTIVE')`, fixture.ownerID, fixture.tenantID, "SCH"+suffix, "schedule."+suffix+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.business_domains(id,tenant_id,code,name,is_default,created_by) VALUES($1,$2,$3,'Schedule integration',true,$4)`, fixture.domainID, fixture.tenantID, "schedule_"+suffix, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id,member_role,assigned_by,status) VALUES($1,$2,$3,'MEMBER',$3,'ACTIVE')`, fixture.tenantID, fixture.domainID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.reports(id,tenant_id,domain_id,code,name,report_type,owner_user_id,status,created_by) VALUES($1,$2,$3,$4,'Schedule report','REPORT',$5,'ACTIVE',$5)`, fixture.reportID, fixture.tenantID, fixture.domainID, "schedule_"+suffix, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.report_versions(id,tenant_id,report_id,version_no,source_revision_no,definition_json,definition_bytes,definition_hash,schema_version,object_uri,published_by,artifact_state) VALUES($1,$2,$3,1,0,'{}',2,repeat('a',64),'1.0',$4,$5,'READY')`, fixture.versionID, fixture.tenantID, fixture.reportID, "reports/"+fixture.reportID+"/v1.json", fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE platform.reports SET current_published_version_id=$1 WHERE tenant_id=$2 AND id=$3`, fixture.versionID, fixture.tenantID, fixture.reportID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func archiveScheduleReport(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture scheduleFixture) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('app.report_asset_reason','integration unavailable test',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE platform.reports SET status='ARCHIVED' WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, fixture.reportID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func cleanupScheduleFixture(t *testing.T, pool *pgxpool.Pool, fixture scheduleFixture) {
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
	for _, table := range []string{
		"platform.report_delivery_events", "platform.report_deliveries", "platform.report_subscriptions", "platform.report_schedules",
		"platform.report_asset_events", "platform.report_versions", "platform.reports", "platform.domain_memberships",
		"platform.business_domains", "platform.users", "platform.tenants",
	} {
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

func deliveryState(deliveries []Delivery, scheduledFor time.Time) string {
	for _, delivery := range deliveries {
		if delivery.ScheduledFor.Equal(scheduledFor) {
			return delivery.State + ":" + delivery.FailureCode
		}
	}
	return ""
}
