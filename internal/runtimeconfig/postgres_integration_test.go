package runtimeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresRuntimeConfigApprovalRolloutRestartRollbackAndRejection(t *testing.T) {
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
	tenant, creator, approver := createRuntimeConfigFixture(t, ctx, adminPool)
	defer cleanupRuntimeConfigFixture(t, adminPool, tenant)

	service, err := NewService(appPool, allowRuntimeAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, time.August, 10, 2, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return clock }
	first, err := service.Create(ctx, tenant, creator, CreateInput{
		ScopeType: "TENANT", ScopeID: tenant, Config: json.RawMessage(`{"budget.dailyRuns":100}`), ImpactSummary: "Initial governed run budget",
	})
	if err != nil || first.State != "DRAFT" || first.VersionNo != 1 {
		t.Fatalf("Create() = %#v, %v", first, err)
	}
	first, err = service.Submit(ctx, tenant, creator, string(first.ID), VersionInput{ExpectedVersion: first.RecordVersion})
	if err != nil || first.State != "IN_REVIEW" {
		t.Fatalf("Submit() = %#v, %v", first, err)
	}
	if _, err = service.Approve(ctx, tenant, creator, string(first.ID), VersionInput{ExpectedVersion: first.RecordVersion}); !errors.Is(err, ErrConflict) {
		t.Fatalf("self approval error = %v", err)
	}
	first, err = service.Approve(ctx, tenant, approver, string(first.ID), VersionInput{ExpectedVersion: first.RecordVersion})
	if err != nil || first.State != "APPROVED" || string(first.ApprovedBy) != approver {
		t.Fatalf("Approve() = %#v, %v", first, err)
	}
	first, err = service.Apply(ctx, tenant, approver, string(first.ID), VersionInput{ExpectedVersion: first.RecordVersion})
	if err != nil || first.State != "ROLLING_OUT" || len(first.Nodes) != 3 {
		t.Fatalf("Apply() = %#v, %v", first, err)
	}
	worker, err := NewWorker(appPool)
	if err != nil {
		t.Fatal(err)
	}
	worker.now = func() time.Time { return clock }
	if processed, err := worker.ProcessTenant(ctx, tenant, 20); err != nil || processed != 3 {
		t.Fatalf("hot rollout = %d, %v", processed, err)
	}
	first, err = service.Get(ctx, tenant, creator, string(first.ID))
	if err != nil || first.State != "ACTIVE" {
		t.Fatalf("active first = %#v, %v", first, err)
	}
	value, found, err := service.Resolve(ctx, tenant, "TENANT", tenant, "budget.dailyRuns")
	if err != nil || !found || value.(json.Number).String() != "100" {
		t.Fatalf("Resolve(first) = %#v, %v, %v", value, found, err)
	}

	clock = clock.Add(time.Hour)
	second, err := service.Create(ctx, tenant, creator, CreateInput{
		ScopeType: "TENANT", ScopeID: tenant, BaseVersionID: first.ID,
		Config: json.RawMessage(`{"provider.routingMode":"PRIMARY_FAILOVER"}`), ImpactSummary: "Provider routing change requiring restart",
	})
	if err != nil || second.Compatibility != "NEXT_RESTART" {
		t.Fatalf("Create(second) = %#v, %v", second, err)
	}
	second, err = service.Submit(ctx, tenant, creator, string(second.ID), VersionInput{ExpectedVersion: second.RecordVersion})
	if err == nil {
		second, err = service.Approve(ctx, tenant, approver, string(second.ID), VersionInput{ExpectedVersion: second.RecordVersion})
	}
	if err == nil {
		second, err = service.Apply(ctx, tenant, approver, string(second.ID), VersionInput{ExpectedVersion: second.RecordVersion})
	}
	if err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.ProcessTenant(ctx, tenant, 20); err != nil || processed != 3 {
		t.Fatalf("restart rollout = %d, %v", processed, err)
	}
	second, err = service.Get(ctx, tenant, approver, string(second.ID))
	if err != nil || second.State != "ROLLING_OUT" {
		t.Fatalf("waiting restart = %#v, %v", second, err)
	}
	for _, node := range second.Nodes {
		if node.State != "WAITING_RESTART" {
			t.Fatalf("node before restart = %#v", node)
		}
		second, err = service.AcknowledgeRestart(ctx, tenant, approver, string(second.ID), string(node.ID))
		if err != nil {
			t.Fatal(err)
		}
	}
	if second.State != "ACTIVE" {
		t.Fatalf("second after restart = %#v", second)
	}
	first, err = service.Get(ctx, tenant, approver, string(first.ID))
	if err != nil || first.State != "SUPERSEDED" {
		t.Fatalf("superseded first = %#v, %v", first, err)
	}
	rolledTarget, err := service.Rollback(ctx, tenant, approver, string(second.ID), VersionInput{ExpectedVersion: second.RecordVersion})
	if err != nil || rolledTarget.ID != first.ID || rolledTarget.State != "ACTIVE" {
		t.Fatalf("Rollback() = %#v, %v", rolledTarget, err)
	}
	rolledSecond, err := service.Get(ctx, tenant, approver, string(second.ID))
	if err != nil || rolledSecond.State != "ROLLED_BACK" {
		t.Fatalf("rolled second = %#v, %v", rolledSecond, err)
	}

	third, err := service.Create(ctx, tenant, creator, CreateInput{ScopeType: "TENANT", ScopeID: tenant, BaseVersionID: first.ID, Config: json.RawMessage(`{"budget.dailyRuns":200}`), ImpactSummary: "Rejected budget change"})
	if err == nil {
		third, err = service.Submit(ctx, tenant, creator, string(third.ID), VersionInput{ExpectedVersion: third.RecordVersion})
	}
	if err == nil {
		third, err = service.Reject(ctx, tenant, approver, string(third.ID), RejectInput{ExpectedVersion: third.RecordVersion, Reason: "Capacity evidence is incomplete"})
	}
	if err != nil || third.State != "REJECTED" || string(third.RejectedBy) != approver || third.RejectionReason == "" {
		t.Fatalf("Reject() = %#v, %v", third, err)
	}

	fourth, err := service.Create(ctx, tenant, creator, CreateInput{ScopeType: "TENANT", ScopeID: tenant, BaseVersionID: first.ID, Config: json.RawMessage(`{"budget.dailyRuns":300}`), ImpactSummary: "Guard test"})
	if err != nil {
		t.Fatal(err)
	}
	err = database.WithTenantTx(database.WithoutAccessContext(ctx), appPool, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='ACTIVE',record_version=record_version+1,updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenant, fourth.ID)
		return e
	})
	if err == nil {
		t.Fatal("database accepted DRAFT -> ACTIVE")
	}
	err = database.WithTenantTx(database.WithoutAccessContext(ctx), appPool, tenant, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `UPDATE platform.runtime_config_events SET event_type='TAMPERED' WHERE tenant_id=$1 LIMIT 1`, tenant)
		return e
	})
	if err == nil {
		t.Fatal("database accepted runtime event mutation")
	}
}

type allowRuntimeAdmin struct{}

func (allowRuntimeAdmin) IsPlatformAdministrator(context.Context, string, string) (bool, error) {
	return true, nil
}

func createRuntimeConfigFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string, string) {
	t.Helper()
	tenant, creator, approver := uuid.NewString(), uuid.NewString(), uuid.NewString()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err := pool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Runtime config integration')`, tenant, "runtime_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO platform.users(id,tenant_id,employee_no,email,display_name,password_hash,status) VALUES($1,$2,$3,$4,'Config creator','integration-only-not-a-login-secret','ACTIVE'),($5,$2,$6,$7,'Config approver','integration-only-not-a-login-secret','ACTIVE')`, creator, tenant, "RTC"+suffix, "creator."+suffix+"@example.invalid", approver, "RTA"+suffix, "approver."+suffix+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	return tenant, creator, approver
}

func cleanupRuntimeConfigFixture(t *testing.T, pool *pgxpool.Pool, tenant string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Error(err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err == nil {
		for _, table := range []string{"platform.runtime_config_events", "platform.runtime_config_effective", "platform.runtime_config_rollout_nodes", "platform.runtime_config_versions", "platform.users", "platform.tenants"} {
			key := "tenant_id"
			if table == "platform.tenants" {
				key = "id"
			}
			if _, err = tx.Exec(ctx, `DELETE FROM `+table+` WHERE `+key+`=$1`, tenant); err != nil {
				break
			}
		}
	}
	if err != nil {
		t.Errorf("cleanup: %v", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		t.Errorf("commit cleanup: %v", err)
	}
}
