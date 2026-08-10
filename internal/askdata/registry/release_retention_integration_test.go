package registry

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

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestReleaseRetentionPostgresLifecycleAndRLS(t *testing.T) {
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	if appURL == "" || adminURL == "" || workerURL == "" {
		t.Skip("set AskData app, admin and worker integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	workerPool, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()

	primary := createReleaseRetentionFixture(t, ctx, admin, true)
	observer := createReleaseRetentionFixture(t, ctx, admin, false)
	defer cleanupReleaseRetentionFixtures(t, admin, primary.tenantID, observer.tenantID)

	store := NewPostgresStore(app)
	primaryContext := database.WithAccessContext(ctx, primary.actorID, primary.domainID)
	certifiedExampleVersionID := addCertifiedExampleRetentionFixture(t, ctx, admin, primary)
	if count, err := store.CountActiveReferences(primaryContext, primary.tenantID,
		primary.domainID, primary.releaseID); err != nil || count != 1 {
		t.Fatalf("certification reference count = %d, %v", count, err)
	}
	certifiedReferences, err := store.ListActiveReferences(primaryContext, primary.tenantID,
		primary.domainID, primary.releaseID)
	if err != nil || len(certifiedReferences) != 1 ||
		certifiedReferences[0].Type != ReleaseReferenceCertifiedExample ||
		certifiedReferences[0].ReferenceID != certifiedExampleVersionID ||
		certifiedReferences[0].OwnerID != primary.actorID {
		t.Fatalf("certification references = %#v, %v", certifiedReferences, err)
	}
	if err := store.ReleaseReference(primaryContext, primary.tenantID, primary.domainID,
		primary.releaseID, ReleaseReferenceCertifiedExample, certifiedExampleVersionID); err != nil {
		t.Fatalf("release certified example reference: %v", err)
	}

	reference := ReleaseReference{
		TenantID: primary.tenantID, DomainID: primary.domainID,
		ReleaseID: primary.releaseID, Type: ReleaseReferenceReportVersion,
		ReferenceID: uuid.NewString(), ReferenceName: "经营日报 2026-08",
		OwnerID: primary.actorID,
	}
	created, err := store.AddReference(primaryContext, reference)
	if err != nil {
		t.Fatalf("AddReference() error = %v", err)
	}
	if created.ID == "" || created.ReleasedAt != nil {
		t.Fatalf("created reference = %#v", created)
	}
	if count, err := store.CountActiveReferences(primaryContext, primary.tenantID,
		primary.domainID, primary.releaseID); err != nil || count != 1 {
		t.Fatalf("CountActiveReferences() = %d, %v", count, err)
	}

	if _, err := admin.Exec(ctx, `UPDATE askdata.releases SET status='SUPERSEDED'
		WHERE tenant_id=$1 AND id=$2`, primary.tenantID, primary.releaseID); err != nil {
		t.Fatalf("supersede release: %v", err)
	}
	var status string
	var retainedAt, retentionUntil *time.Time
	if err := admin.QueryRow(ctx, `SELECT status,retained_at,retention_until
		FROM askdata.releases WHERE tenant_id=$1 AND id=$2`, primary.tenantID,
		primary.releaseID).Scan(&status, &retainedAt, &retentionUntil); err != nil ||
		status != "RETAINED" || retainedAt == nil || retentionUntil == nil ||
		!retentionUntil.After(*retainedAt) {
		t.Fatalf("retained release = %s %v %v, %v", status, retainedAt, retentionUntil, err)
	}

	references, err := store.ListActiveReferences(primaryContext, primary.tenantID,
		primary.domainID, primary.releaseID)
	if err != nil || len(references) != 1 || references[0].ReferenceName != reference.ReferenceName ||
		references[0].OwnerID != primary.actorID {
		t.Fatalf("ListActiveReferences() = %#v, %v", references, err)
	}
	retireErr := store.Retire(primaryContext, primary.tenantID, primary.domainID, primary.releaseID)
	var retentionFailure *ReleaseRetentionError
	if !errors.As(retireErr, &retentionFailure) || retentionFailure.Code != ReleaseRetireBlockedCode ||
		len(retentionFailure.References) != 1 || retentionFailure.References[0].ReferenceID != reference.ReferenceID {
		t.Fatalf("blocked Retire() = %#v, %v", retentionFailure, retireErr)
	}
	workerStore := NewPostgresStore(workerPool)
	externalCleaner := &memoryRetainedProjectionCleaner{}
	cleanupWorker, err := NewReleaseProjectionCleanupWorker(workerStore, externalCleaner)
	if err != nil {
		t.Fatal(err)
	}
	if err := cleanupWorker.Run(ctx, primary.tenantID, primary.domainID, primary.releaseID); err != nil {
		t.Fatalf("retained projection cleanup: %v", err)
	}
	if len(externalCleaner.seen) != 1 || externalCleaner.seen[0].ObjectCount != 1 {
		t.Fatalf("external cleanup proof = %#v", externalCleaner.seen)
	}
	projectionStates := map[string]string{}
	rows, err := admin.Query(ctx, `SELECT target,status FROM askdata.release_projections
		WHERE tenant_id=$1 AND release_id=$2 ORDER BY target`, primary.tenantID, primary.releaseID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var target, projectionStatus string
		if err := rows.Scan(&target, &projectionStatus); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		projectionStates[target] = projectionStatus
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if projectionStates["POSTGRES_REGISTRY"] != "READY" ||
		projectionStates["EXECUTION_SEMANTIC_LAYER"] != "READY" ||
		projectionStates["SEARCH_INDEX"] != "STALE" ||
		projectionStates["NEBULA_GRAPH"] != "STALE" {
		t.Fatalf("projection states after cleanup = %#v", projectionStates)
	}

	observerContext := database.WithAccessContext(ctx, observer.actorID, observer.domainID)
	visibleAcrossTenants := -1
	if err := database.WithTenantTx(observerContext, app, observer.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*)::integer FROM askdata.release_references`).Scan(&visibleAcrossTenants)
	}); err != nil || visibleAcrossTenants != 0 {
		t.Fatalf("cross-tenant visible references = %d, %v", visibleAcrossTenants, err)
	}

	if err := store.ReleaseReference(primaryContext, primary.tenantID, primary.domainID,
		primary.releaseID, reference.Type, reference.ReferenceID); err != nil {
		t.Fatalf("ReleaseReference() error = %v", err)
	}
	if count, err := store.CountActiveReferences(primaryContext, primary.tenantID,
		primary.domainID, primary.releaseID); err != nil || count != 0 {
		t.Fatalf("released CountActiveReferences() = %d, %v", count, err)
	}
	retireErr = store.Retire(primaryContext, primary.tenantID, primary.domainID, primary.releaseID)
	if !errors.As(retireErr, &retentionFailure) || retentionFailure.Code != ReleaseRetentionNotExpiredCode {
		t.Fatalf("unexpired Retire() error = %v", retireErr)
	}

	if _, err := admin.Exec(ctx, `UPDATE askdata.releases SET
		retained_at=clock_timestamp()-interval '25 months',
		retention_until=clock_timestamp()-interval '1 month'
		WHERE tenant_id=$1 AND id=$2`, primary.tenantID, primary.releaseID); err != nil {
		t.Fatalf("expire retention fixture: %v", err)
	}
	if err := store.Retire(primaryContext, primary.tenantID, primary.domainID, primary.releaseID); err != nil {
		t.Fatalf("expired Retire() error = %v", err)
	}
	var retiredAt *time.Time
	if err := admin.QueryRow(ctx, `SELECT status,retired_at FROM askdata.releases
		WHERE tenant_id=$1 AND id=$2`, primary.tenantID, primary.releaseID).Scan(
		&status, &retiredAt); err != nil || status != "RETIRED" || retiredAt == nil {
		t.Fatalf("retired release = %s %v, %v", status, retiredAt, err)
	}
}

func addCertifiedExampleRetentionFixture(
	t *testing.T,
	ctx context.Context,
	admin *pgxpool.Pool,
	fixture releaseRetentionFixture,
) string {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),set_config('app.domain_id',$2,true),
		set_config('app.user_id',$3,true),set_config('app.access_mode','USER',true)`,
		fixture.tenantID, fixture.domainID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	exampleID, versionID := uuid.NewString(), uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.certified_examples(
		id,tenant_id,domain_id,question_hash,created_by
	) VALUES($1,$2,$3,$4,$5)`, exampleID, fixture.tenantID, fixture.domainID,
		string(askdata.HashBytes([]byte("certified-question-"+versionID))), fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.certified_example_versions(
		id,tenant_id,domain_id,certified_example_id,version_no,question,
		expected_metric_version_ids,expected_dimension_version_ids,
		expected_member_values,expected_time_expression,applicable_role_ids,
		notes,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,1,'retention certification fixture','{}','{}','[]','',
		'{}','','DRAFT',$5,$6)`, versionID, fixture.tenantID, fixture.domainID,
		exampleID, string(askdata.HashBytes([]byte("certified-example-"+versionID))),
		fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE askdata.certified_example_versions
		SET status='CERTIFIED' WHERE id=$1`, versionID); err != nil {
		t.Fatalf("certify example retention fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return versionID
}

type releaseRetentionFixture struct {
	tenantID, actorID, domainID, releaseID, releaseHash string
}

func createReleaseRetentionFixture(
	t *testing.T,
	ctx context.Context,
	admin *pgxpool.Pool,
	withRelease bool,
) releaseRetentionFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	fixture := releaseRetentionFixture{
		tenantID: uuid.NewString(), actorID: uuid.NewString(), domainID: uuid.NewString(),
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'Release retention integration')`, fixture.tenantID, "retain_"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
		id,tenant_id,email,display_name,password_hash,employee_no,status
	) VALUES($1,$2,$3,'Retention owner','not-a-login-hash',$4,'ACTIVE')`,
		fixture.actorID, fixture.tenantID, "retain_"+suffix+"@example.invalid",
		"RET"+strings.ToUpper(suffix)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
		id,tenant_id,code,name,is_default,created_by,status
	) VALUES($1,$2,$3,'Release retention',true,$4,'ACTIVE')`,
		fixture.domainID, fixture.tenantID, "retain_"+suffix, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
		tenant_id,domain_id,user_id,status,member_role,assigned_by
	) VALUES($1,$2,$3,'ACTIVE','DOMAIN_ADMIN',$3)`, fixture.tenantID,
		fixture.domainID, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(
		id,tenant_id,code,name,owner_id,status
	) VALUES($1,$2,$3,'Release retention',$4,'ACTIVE')`, fixture.domainID,
		fixture.tenantID, "retain_"+suffix, fixture.actorID); err != nil {
		t.Fatal(err)
	}
	if withRelease {
		objectID, objectVersionID := uuid.NewString(), uuid.NewString()
		objectHash := askdata.HashBytes([]byte("retained-object-" + suffix))
		fixture.releaseHash = string(askdata.HashBytes([]byte(fmt.Sprintf(
			"QUALITY_RULE:%s:%s:%s", objectID, objectVersionID, objectHash,
		))))
		if err := tx.QueryRow(ctx, `INSERT INTO askdata.releases(
			tenant_id,domain_id,semantic_version,content_hash,status,object_count,
			created_by,updated_by,activated_by,ready_at,activated_at
		) VALUES($1,$2,$3,$4,'ACTIVE',1,$5,$5,$5,clock_timestamp(),clock_timestamp())
		RETURNING id::text`, fixture.tenantID, fixture.domainID, "retain-"+suffix,
			fixture.releaseHash, fixture.actorID,
		).Scan(&fixture.releaseID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
			tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
			content_hash,sensitivity,contract_json
		) VALUES($1,$2,$3,'QUALITY_RULE',$4,$5,$6,'INTERNAL','{}'::jsonb)`,
			fixture.tenantID, fixture.domainID, fixture.releaseID, objectID,
			objectVersionID, objectHash); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_projections(
			tenant_id,domain_id,release_id,target,status,expected_content_hash,
			applied_content_hash,resource_version,object_count,completed_at
		) SELECT $1,$2,$3,target,'READY',$4,$4,'retention-integration-v1',1,clock_timestamp()
		FROM unnest(ARRAY[
			'POSTGRES_REGISTRY','SEARCH_INDEX','NEBULA_GRAPH','EXECUTION_SEMANTIC_LAYER'
		]) AS target`, fixture.tenantID, fixture.domainID, fixture.releaseID,
			fixture.releaseHash); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupReleaseRetentionFixtures(t *testing.T, admin *pgxpool.Pool, tenantIDs ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Errorf("begin retention cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable retention cleanup triggers: %v", err)
		return
	}
	for _, tenantID := range tenantIDs {
		for _, statement := range []string{
			`DELETE FROM askdata.release_references WHERE tenant_id=$1`,
			`DELETE FROM askdata.release_events WHERE tenant_id=$1`,
			`DELETE FROM askdata.release_projection_artifacts WHERE tenant_id=$1`,
			`DELETE FROM askdata.release_projections WHERE tenant_id=$1`,
			`DELETE FROM askdata.release_objects WHERE tenant_id=$1`,
			`DELETE FROM askdata.releases WHERE tenant_id=$1`,
			`DELETE FROM askdata.certified_example_versions WHERE tenant_id=$1`,
			`DELETE FROM askdata.certified_examples WHERE tenant_id=$1`,
			`DELETE FROM askdata.release_state WHERE tenant_id=$1`,
			`DELETE FROM askdata.domains WHERE tenant_id=$1`,
			`DELETE FROM platform.domain_memberships WHERE tenant_id=$1`,
			`DELETE FROM platform.business_domains WHERE tenant_id=$1`,
			`DELETE FROM platform.users WHERE tenant_id=$1`,
			`DELETE FROM platform.tenants WHERE id=$1`,
		} {
			if _, err := tx.Exec(ctx, statement, tenantID); err != nil {
				t.Errorf("retention cleanup %q: %v", statement, err)
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit retention cleanup: %v", err)
	}
}
