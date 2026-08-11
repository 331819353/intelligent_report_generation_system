package registry

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminDraftTermSQLRoundTripInRollbackTransaction(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var scope AdminScope
	err = tx.QueryRow(ctx, `SELECT membership.tenant_id::text,
		membership.domain_id::text,membership.user_id::text
	FROM platform.domain_memberships AS membership
	JOIN platform.business_domains AS domain
	  ON domain.id=membership.domain_id AND domain.tenant_id=membership.tenant_id
	JOIN platform.users AS user_account
	  ON user_account.id=membership.user_id AND user_account.tenant_id=membership.tenant_id
	WHERE membership.status='ACTIVE' AND domain.status='ACTIVE'
	  AND domain.deleted_at IS NULL AND user_account.deleted_at IS NULL
	ORDER BY membership.created_at LIMIT 1`).Scan(
		&scope.TenantID, &scope.DomainID, &scope.ActorID)
	if errors.Is(err, pgx.ErrNoRows) {
		suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
		scope = AdminScope{
			TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
			VALUES($1,$2,'Admin term integration')`, scope.TenantID, "admin_term_"+suffix); err != nil {
			t.Fatalf("insert tenant fixture: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash,employee_no
		) VALUES($1,$2,$3,'Admin term integration','not-a-login-hash',$4)`,
			scope.ActorID, scope.TenantID, "admin_term_"+suffix+"@example.invalid",
			"ADMTERM"+strings.ToUpper(suffix)); err != nil {
			t.Fatalf("insert user fixture: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
			id,tenant_id,code,name,is_default,created_by
		) VALUES($1,$2,$3,'Admin term integration',true,$4)`, scope.DomainID,
			scope.TenantID, "admin_term_"+suffix, scope.ActorID); err != nil {
			t.Fatalf("insert domain fixture: %v", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,member_role,assigned_by
		) VALUES($1,$2,$3,'DOMAIN_ADMIN',$3)`, scope.TenantID, scope.DomainID,
			scope.ActorID); err != nil {
			t.Fatalf("insert membership fixture: %v", err)
		}
	} else if err != nil {
		t.Fatalf("select active domain identity fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),set_config('app.domain_id',$2,true),
		set_config('app.user_id',$3,true),set_config('app.access_mode','USER',true)`,
		scope.TenantID, scope.DomainID, scope.ActorID); err != nil {
		t.Fatalf("set askdata context: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(id,tenant_id,code,name,owner_id)
		SELECT id,tenant_id,code,name,$3 FROM platform.business_domains
		WHERE id=$1 AND tenant_id=$2 ON CONFLICT(id) DO NOTHING`,
		scope.DomainID, scope.TenantID, scope.ActorID); err != nil {
		t.Fatalf("ensure askdata domain: %v", err)
	}

	requestID := uuid.NewString()
	targetVersionID := uuid.NewString()
	validFrom := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	validTo := validFrom.Add(24 * time.Hour)
	input := &BusinessTermDraftInput{
		VersionedDraftInput: VersionedDraftInput{VersionNo: 1},
		Term:                "集成销售额", TermType: TermTypeOperator,
		TargetObjectType: TermTargetOperator, TargetVersionID: targetVersionID,
		TargetCode: "SUM", MatchMode: TermMatchPrefix, Priority: 120,
		NegativeContexts: []string{"平均值"}, ValidFrom: &validFrom, ValidTo: &validTo,
		Source: TermSourceFeedback,
		Code:   "integration_" + requestID[:8], Name: "集成业务词",
		Definition: "只存在于回滚事务中的管理 API SQL 夹具",
		Aliases:    []string{"集成词", "integration term"},
	}
	created, err := createDraftTx(ctx, tx, scope, AdminResourceBusinessTerm, input, requestID)
	if err != nil {
		t.Fatalf("createDraftTx() error = %v", err)
	}
	if created.Status != "DRAFT" || created.ResourceID == "" || created.UpdatedAt == nil {
		t.Fatalf("created result = %#v", created)
	}

	loadedValue, err := getObjectTx(ctx, tx, scope.DomainID,
		AdminResourceBusinessTerm, created.ResourceID, StatusDraft, false)
	if err != nil {
		t.Fatalf("getObjectTx() error = %v", err)
	}
	loaded := loadedValue.(BusinessTerm)
	if loaded.Code != input.Code || loaded.Status != VersionStatusDraft ||
		loaded.ContentHash != created.ContentHash || loaded.Term != input.Term ||
		loaded.TargetVersionID != targetVersionID || loaded.Priority != 120 ||
		loaded.ReviewStatus != TermReviewPending || loaded.ReviewedAt != nil {
		t.Fatalf("loaded term = %#v", loaded)
	}

	page, err := listObjectsTx(ctx, tx, scope.DomainID,
		AdminResourceBusinessTerm, StatusDraft, metricCursor{}, 200)
	if err != nil {
		t.Fatalf("listObjectsTx() error = %v", err)
	}
	found := false
	for _, item := range page.Items.([]BusinessTerm) {
		found = found || item.ID == created.ResourceID
	}
	if !found {
		t.Fatalf("created term missing from page: %#v", page)
	}

	input.ExpectedUpdatedAt = &loaded.UpdatedAt
	input.Definition = "更新后的回滚事务夹具"
	updated, err := updateDraftTx(ctx, tx, scope, AdminResourceBusinessTerm,
		created.ResourceID, input)
	if err != nil {
		t.Fatalf("updateDraftTx() error = %v", err)
	}
	if updated.ContentHash == created.ContentHash || updated.UpdatedAt == nil {
		t.Fatalf("updated result = %#v", updated)
	}

	deleted, err := deleteDraftTx(ctx, tx, scope.DomainID,
		AdminResourceBusinessTerm, created.ResourceID,
		DeleteDraftInput{ExpectedUpdatedAt: updated.UpdatedAt})
	if err != nil {
		t.Fatalf("deleteDraftTx() error = %v", err)
	}
	if deleted.ResourceID != created.ResourceID || deleted.Status != "DRAFT" {
		t.Fatalf("deleted result = %#v", deleted)
	}
	if _, err := getObjectTx(ctx, tx, scope.DomainID,
		AdminResourceBusinessTerm, created.ResourceID, StatusDraft, false); err != ErrRegistryNotFound {
		t.Fatalf("get deleted draft error = %v", err)
	}
}
