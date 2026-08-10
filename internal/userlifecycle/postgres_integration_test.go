package userlifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresOwnerTransferDisablesAtomicallyAndPreservesCertifiedContent(t *testing.T) {
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
	fixture := createLifecycleFixture(t, ctx, adminPool)
	defer cleanupLifecycleFixture(t, adminPool, fixture.tenantID)

	service, err := NewService(appPool, allowLifecycleAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(ctx, fixture.tenantID, fixture.adminID, fixture.targetID)
	if err != nil || !preview.CanDisable {
		t.Fatalf("Preview() = %#v, %v", preview, err)
	}
	wanted := map[string]bool{"REPORT": true, "SEMANTIC_DOMAIN": true, "SEMANTIC_ENTITY": true, "REPORT_TEMPLATE": true}
	mappings := []Mapping{}
	for _, item := range preview.Items {
		if item.Disposition == Transfer {
			mappings = append(mappings, Mapping{Category: item.Category, DomainID: item.DomainID, ReceiverUserID: fixture.receiverID})
			delete(wanted, item.Category)
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("preview missed owner categories: %#v; items=%#v", wanted, preview.Items)
	}
	batch, err := service.PlanAndExecute(ctx, fixture.tenantID, fixture.adminID, fixture.targetID, mappings)
	if err != nil || batch.Status != "COMPLETED" || batch.CompletedAt == nil {
		retryErr := service.execute(ctx, fixture.tenantID, fixture.adminID, batch.ID, batch.RecordVersion)
		t.Fatalf("PlanAndExecute() = %#v, %v; retry detail = %v", batch, err, retryErr)
	}
	var userStatus, reportOwner, domainOwner, entityOwner, templateOwner, entityHash string
	err = adminPool.QueryRow(ctx, `SELECT user_account.status::text,report.owner_user_id::text,domain.owner_id::text,entity.owner_id::text,template.owner_user_id::text,entity.content_hash FROM platform.users user_account JOIN platform.reports report ON report.tenant_id=user_account.tenant_id AND report.id=$3 JOIN askdata.domains domain ON domain.tenant_id=user_account.tenant_id AND domain.id=$4 JOIN askdata.entities entity ON entity.tenant_id=user_account.tenant_id AND entity.id=$5 JOIN platform.report_templates template ON template.tenant_id=user_account.tenant_id AND template.id=$6 WHERE user_account.tenant_id=$1 AND user_account.id=$2`, fixture.tenantID, fixture.targetID, fixture.reportID, fixture.domainID, fixture.entityID, fixture.templateID).Scan(&userStatus, &reportOwner, &domainOwner, &entityOwner, &templateOwner, &entityHash)
	if err != nil {
		t.Fatal(err)
	}
	if userStatus != "DISABLED" || reportOwner != fixture.receiverID || domainOwner != fixture.receiverID || entityOwner != fixture.receiverID || templateOwner != fixture.receiverID || entityHash != strings.Repeat("a", 64) {
		t.Fatalf("transfer result = status:%s report:%s domain:%s entity:%s template:%s hash:%s", userStatus, reportOwner, domainOwner, entityOwner, templateOwner, entityHash)
	}
	if _, err = adminPool.Exec(ctx, `UPDATE askdata.entities SET name='tampered' WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, fixture.entityID); err == nil {
		t.Fatal("certified semantic content was mutable outside owner transfer mode")
	}
}

func TestPostgresOwnerTransferFailureLeavesUserActiveAndRetryable(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	fixture := createLifecycleFixture(t, ctx, adminPool)
	defer cleanupLifecycleFixture(t, adminPool, fixture.tenantID)
	adapter := failingLifecycleAdapter{domainID: fixture.domainID, objectID: uuid.NewString()}
	service, err := NewService(appPool, allowLifecycleAdmin{}, adapter)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.PlanAndExecute(ctx, fixture.tenantID, fixture.adminID, fixture.targetID, []Mapping{{Category: adapter.Category(), DomainID: fixture.domainID, ReceiverUserID: fixture.receiverID}})
	if err != nil || batch.Status != "TRANSFER_FAILED" || batch.FailureCode != "TRANSFER_APPLY_FAILED" {
		t.Fatalf("failed batch = %#v, %v", batch, err)
	}
	batch, err = service.Retry(ctx, fixture.tenantID, fixture.adminID, batch.ID, batch.RecordVersion)
	if err != nil || batch.Status != "TRANSFER_FAILED" || batch.FailureCode != "TRANSFER_RETRY_FAILED" {
		t.Fatalf("retry batch = %#v, %v", batch, err)
	}
	var active bool
	if err = adminPool.QueryRow(ctx, `SELECT status='ACTIVE' FROM platform.users WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, fixture.targetID).Scan(&active); err != nil || !active {
		t.Fatalf("target active after rollback = %v, %v", active, err)
	}
}

func TestPostgresPreviewBlocksOnlyTheLastActiveAdministrator(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
	fixture := createLifecycleFixture(t, ctx, adminPool)
	defer cleanupLifecycleFixture(t, adminPool, fixture.tenantID)

	roleID := uuid.NewString()
	if _, err = adminPool.Exec(ctx, `UPDATE platform.domain_memberships SET member_role='DOMAIN_ADMIN' WHERE tenant_id=$1 AND domain_id=$2 AND user_id=$3`, fixture.tenantID, fixture.domainID, fixture.targetID); err != nil {
		t.Fatal(err)
	}

	service, err := NewService(appPool, allowLifecycleAdmin{})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.Preview(ctx, fixture.tenantID, fixture.adminID, fixture.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if countCategory(preview.Items, "DOMAIN_ADMIN") != 1 || countCategory(preview.Items, "PLATFORM_ADMIN") != 0 || preview.CanDisable {
		t.Fatalf("last domain administrator preview = %#v", preview)
	}

	if _, err = adminPool.Exec(ctx, `INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id,member_role,assigned_by,status) VALUES($1,$2,$3,'DOMAIN_ADMIN',$3,'ACTIVE')`, fixture.tenantID, fixture.domainID, fixture.adminID); err != nil {
		t.Fatal(err)
	}
	preview, err = service.Preview(ctx, fixture.tenantID, fixture.adminID, fixture.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if countCategory(preview.Items, "DOMAIN_ADMIN") != 0 || countCategory(preview.Items, "PLATFORM_ADMIN") != 0 || !preview.CanDisable {
		t.Fatalf("redundant active domain administrators preview = %#v", preview)
	}

	if _, err = adminPool.Exec(ctx, `DELETE FROM platform.domain_memberships WHERE tenant_id=$1 AND domain_id=$2 AND user_id=$3`, fixture.tenantID, fixture.domainID, fixture.targetID); err != nil {
		t.Fatal(err)
	}
	otherPlatformID := uuid.NewString()
	otherSuffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if _, err = adminPool.Exec(ctx, `INSERT INTO platform.users(id,tenant_id,employee_no,email,display_name,password_hash,status) VALUES($1,$2,$3,$4,'Other platform administrator','integration-only-not-a-login-secret','ACTIVE')`, otherPlatformID, fixture.tenantID, "LFP"+otherSuffix, "lfp."+otherSuffix+"@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err = adminPool.Exec(ctx, `INSERT INTO platform.roles(id,tenant_id,code,name,is_system) VALUES($1,$2,'platform_admin','Platform administrator',true)`, roleID, fixture.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err = adminPool.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by) VALUES($1,$2,$3,$4),($1,$5,$3,$4)`, fixture.tenantID, fixture.targetID, roleID, fixture.adminID, otherPlatformID); err != nil {
		t.Fatal(err)
	}
	preview, err = service.Preview(ctx, fixture.tenantID, fixture.adminID, fixture.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if countCategory(preview.Items, "DOMAIN_ADMIN") != 0 || countCategory(preview.Items, "PLATFORM_ADMIN") != 0 || !preview.CanDisable {
		t.Fatalf("redundant active platform administrators preview = %#v", preview)
	}

	if _, err = adminPool.Exec(ctx, `UPDATE platform.users SET status='DISABLED' WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, otherPlatformID); err != nil {
		t.Fatal(err)
	}
	preview, err = service.Preview(ctx, fixture.tenantID, fixture.adminID, fixture.targetID)
	if err != nil {
		t.Fatal(err)
	}
	if countCategory(preview.Items, "DOMAIN_ADMIN") != 0 || countCategory(preview.Items, "PLATFORM_ADMIN") != 1 || preview.CanDisable {
		t.Fatalf("last active platform administrator preview = %#v", preview)
	}

	if _, err = adminPool.Exec(ctx, `UPDATE platform.users SET status='ACTIVE' WHERE tenant_id=$1 AND id=$2`, fixture.tenantID, otherPlatformID); err != nil {
		t.Fatal(err)
	}
	preview, err = service.Preview(ctx, fixture.tenantID, fixture.adminID, fixture.targetID)
	if err != nil || !preview.CanDisable {
		t.Fatalf("restored administrator preview = %#v, %v", preview, err)
	}
	mappings := make([]Mapping, 0, len(preview.Items))
	for _, item := range preview.Items {
		if item.Disposition == Transfer {
			mappings = append(mappings, Mapping{Category: item.Category, DomainID: item.DomainID, ReceiverUserID: fixture.receiverID})
		}
	}
	batch, err := service.PlanAndExecute(ctx, fixture.tenantID, fixture.adminID, fixture.targetID, mappings)
	if err != nil || batch.Status != "COMPLETED" {
		t.Fatalf("disable redundant platform administrator = %#v, %v", batch, err)
	}
}

func countCategory(items []Item, category string) int {
	count := 0
	for _, item := range items {
		if item.Category == category {
			count++
		}
	}
	return count
}

type allowLifecycleAdmin struct{}

func (allowLifecycleAdmin) IsPlatformAdministrator(context.Context, string, string) (bool, error) {
	return true, nil
}

type failingLifecycleAdapter struct{ domainID, objectID string }

func (a failingLifecycleAdapter) Category() string { return "FAILURE_FIXTURE" }
func (a failingLifecycleAdapter) Preview(context.Context, pgx.Tx, string, string) ([]Item, error) {
	return []Item{{Category: a.Category(), DomainID: a.domainID, ObjectID: a.objectID, Disposition: Transfer, SourceVersion: "1"}}, nil
}
func (a failingLifecycleAdapter) Apply(context.Context, pgx.Tx, string, string, Item, time.Time) error {
	return errors.New("injected owner transfer failure")
}

type lifecycleFixture struct{ tenantID, domainID, adminID, targetID, receiverID, reportID, entityID, templateID string }

func createLifecycleFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) lifecycleFixture {
	t.Helper()
	value := lifecycleFixture{tenantID: uuid.NewString(), domainID: uuid.NewString(), adminID: uuid.NewString(), targetID: uuid.NewString(), receiverID: uuid.NewString(), reportID: uuid.NewString(), entityID: uuid.NewString(), templateID: uuid.NewString()}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Lifecycle integration')`, value.tenantID, "lifecycle_"+suffix); err != nil {
		t.Fatal(err)
	}
	users := []struct{ id, prefix string }{{value.adminID, "LFA"}, {value.targetID, "LFT"}, {value.receiverID, "LFR"}}
	for _, user := range users {
		if _, err = tx.Exec(ctx, `INSERT INTO platform.users(id,tenant_id,employee_no,email,display_name,password_hash,status) VALUES($1,$2,$3,$4,$5,'integration-only-not-a-login-secret','ACTIVE')`, user.id, value.tenantID, user.prefix+suffix, strings.ToLower(user.prefix)+"."+suffix+"@example.invalid", user.prefix+" lifecycle user"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.business_domains(id,tenant_id,code,name,is_default,created_by) VALUES($1,$2,$3,'Lifecycle domain',true,$4)`, value.domainID, value.tenantID, "lifecycle_"+suffix, value.adminID); err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{value.targetID, value.receiverID} {
		if _, err = tx.Exec(ctx, `INSERT INTO platform.domain_memberships(tenant_id,domain_id,user_id,member_role,assigned_by,status) VALUES($1,$2,$3,'MEMBER',$4,'ACTIVE')`, value.tenantID, value.domainID, userID, value.adminID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,status,owner_id) VALUES($1,$2,$3,'Lifecycle semantic domain','ACTIVE',$4)`, value.domainID, value.tenantID, "lifecycle_"+suffix, value.targetID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO askdata.entities(id,tenant_id,domain_id,entity_id,version_no,code,name,key_contract,status,content_hash,owner_id) VALUES($1,$2,$3,$4,1,$5,'Lifecycle entity','{}','CERTIFIED',repeat('a',64),$6)`, value.entityID, value.tenantID, value.domainID, uuid.NewString(), "entity_"+suffix, value.targetID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.reports(id,tenant_id,domain_id,code,name,report_type,owner_user_id,status,created_by) VALUES($1,$2,$3,$4,'Lifecycle report','REPORT',$5,'ACTIVE',$6)`, value.reportID, value.tenantID, value.domainID, "lifecycle_"+suffix, value.targetID, value.adminID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO platform.report_templates(id,tenant_id,code,name,category,owner_user_id) VALUES($1,$2,$3,'Lifecycle report template','OPERATIONS',$4)`, value.templateID, value.tenantID, "lifecycle_"+suffix, value.targetID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return value
}

func cleanupLifecycleFixture(t *testing.T, pool *pgxpool.Pool, tenantID string) {
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
		for _, table := range []string{
			"platform.user_lifecycle_events", "platform.user_lifecycle_batch_items", "platform.user_lifecycle_batches",
			"platform.report_asset_events", "platform.report_templates", "platform.reports", "askdata.entities", "askdata.domains",
			"platform.domain_memberships", "platform.user_roles", "platform.roles", "platform.business_domains", "platform.users", "platform.tenants",
		} {
			key := "tenant_id"
			if table == "platform.tenants" {
				key = "id"
			}
			if _, err = tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s=$1`, table, key), tenantID); err != nil {
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
