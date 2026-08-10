package registryimport

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

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresImportIdempotencyPartialCommitAndTenantIsolation(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	if adminURL == "" || appURL == "" || workerURL == "" {
		t.Skip("set AskData admin, app, and worker integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	app, err := database.Open(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workerPool, err := database.Open(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()

	fixture := createImportFixture(t, ctx, admin)
	defer cleanupImportFixture(t, admin, fixture)
	appStore := NewPostgresStore(app)
	workerStore := NewPostgresStore(workerPool)
	userContext := database.WithAccessContext(ctx, fixture.userA, fixture.domainA)
	templateReferences, err := NewPostgresTemplateCatalog(app).ListTemplateReferences(
		userContext, fixture.tenantA, fixture.domainA,
	)
	if err != nil || len(templateReferences) != 0 {
		t.Fatalf("empty-domain template references = %#v, %v", templateReferences, err)
	}
	for name, catalog := range map[string]*PostgresValidationCatalog{
		"app":    NewPostgresValidationCatalog(app),
		"worker": NewPostgresValidationCatalog(workerPool),
	} {
		catalogContext := ctx
		if name == "app" {
			catalogContext = userContext
		}
		snapshot, err := catalog.LoadValidationSnapshot(
			catalogContext, fixture.tenantA, fixture.domainA,
		)
		if err != nil {
			t.Fatalf("%s validation catalog = %v", name, err)
		}
		if snapshot.References == nil || snapshot.Owners == nil || snapshot.Roles == nil {
			t.Fatalf("%s validation catalog returned nil maps", name)
		}
	}
	input := CreateImportInput{
		TenantID: fixture.tenantA, DomainID: fixture.domainA,
		AssetType: AssetMetric, FileObjectURI: "minio://semantic-imports/integration.csv",
		FileHash: strings.Repeat("a", 64), FileName: "metrics.csv", CreatedBy: fixture.userA,
	}
	batch, created, err := appStore.CreateImport(userContext, input)
	if err != nil || !created {
		t.Fatalf("CreateImport() = %#v, %v, %v", batch, created, err)
	}
	replayed, created, err := appStore.CreateImport(userContext, input)
	if err != nil || created || replayed.ID != batch.ID {
		t.Fatalf("idempotent CreateImport() = %#v, %v, %v", replayed, created, err)
	}
	assertImportStateRejected(t, userContext, app, fixture.tenantA, batch.ID, StateValidated)
	assertImportStateRejected(t, userContext, app, fixture.tenantA, batch.ID, StateFailed)
	assertImportStateRejected(t, userContext, app, fixture.tenantA, batch.ID, StateCommitted)

	claim, err := workerStore.ClaimForValidation(
		ctx, fixture.tenantA, "integration-import-worker", time.Minute,
	)
	if err != nil || claim == nil || claim.ImportID != batch.ID {
		t.Fatalf("ClaimForValidation() = %#v, %v", claim, err)
	}
	assertImportStateRejected(t, ctx, workerPool, fixture.tenantA, batch.ID, StateUploaded)
	assertImportStateRejected(t, ctx, workerPool, fixture.tenantA, batch.ID, StateCommitted)
	rows := make([]ValidatedRow, 3)
	for index := range rows {
		payload := json.RawMessage(`{"code":"metric_` + string(rune('a'+index)) + `"}`)
		rows[index] = ValidatedRow{
			RowNo: index + 1, RawJSON: payload, NormalizedJSON: payload, State: RowValid,
		}
	}
	if err := workerStore.UpsertRows(ctx, *claim, "integration-import-worker", rows); err != nil {
		t.Fatalf("UpsertRows() error = %v", err)
	}
	if err := workerStore.CompleteValidation(ctx, *claim, "integration-import-worker"); err != nil {
		t.Fatalf("CompleteValidation() error = %v", err)
	}
	persistedRows, err := appStore.ListRows(userContext, fixture.tenantA, fixture.domainA, batch.ID)
	if err != nil || len(persistedRows) != 3 {
		t.Fatalf("ListRows() = %d, %v", len(persistedRows), err)
	}
	assertImportStateRejected(t, userContext, app, fixture.tenantA, batch.ID, StateValidating)
	assertImportStateRejected(t, userContext, app, fixture.tenantA, batch.ID, StateFailed)

	creator := integrationDraftCreator{}
	_, _, err = appStore.CommitValidRows(
		userContext, fixture.tenantA, fixture.domainA, fixture.userA, batch.ID, []int{1},
		integrationDraftCreator{status: "CERTIFIED"},
	)
	if !errors.Is(err, ErrImportDraftRejected) {
		t.Fatalf("non-DRAFT creator error = %v", err)
	}
	state, references, err := appStore.CommitValidRows(
		userContext, fixture.tenantA, fixture.domainA, fixture.userA, batch.ID, []int{1}, creator,
	)
	if err != nil || state != StatePartiallyCommitted || len(references) != 1 {
		t.Fatalf("partial CommitValidRows() = %s, %#v, %v", state, references, err)
	}
	state, references, err = appStore.CommitValidRows(
		userContext, fixture.tenantA, fixture.domainA, fixture.userA, batch.ID, []int{2, 3}, creator,
	)
	if err != nil || state != StateCommitted || len(references) != 2 {
		t.Fatalf("final CommitValidRows() = %s, %#v, %v", state, references, err)
	}

	otherContext := database.WithAccessContext(ctx, fixture.userB, fixture.domainB)
	if _, err := appStore.Get(otherContext, fixture.tenantB, fixture.domainA, batch.ID); !errors.Is(err, ErrImportNotFound) {
		t.Fatalf("cross-tenant Get() error = %v, want not found", err)
	}
	if _, err := appStore.ListRows(otherContext, fixture.tenantB, fixture.domainA, batch.ID); !errors.Is(err, ErrImportNotFound) {
		t.Fatalf("cross-tenant ListRows() error = %v, want not found", err)
	}

	withdrawInput := input
	withdrawInput.FileHash = strings.Repeat("b", 64)
	withdrawInput.FileName = "dimensions.csv"
	withdrawInput.AssetType = AssetDimension
	withdrawBatch, created, err := appStore.CreateImport(userContext, withdrawInput)
	if err != nil || !created {
		t.Fatalf("create withdraw fixture = %#v, %v, %v", withdrawBatch, created, err)
	}
	withdrawClaim, err := workerStore.ClaimForValidation(
		ctx, fixture.tenantA, "integration-import-worker", time.Minute,
	)
	if err != nil || withdrawClaim == nil || withdrawClaim.ImportID != withdrawBatch.ID {
		t.Fatalf("claim withdraw fixture = %#v, %v", withdrawClaim, err)
	}
	payload := json.RawMessage(`{"code":"dimension_a"}`)
	if err := workerStore.UpsertRows(ctx, *withdrawClaim, "integration-import-worker", []ValidatedRow{{
		RowNo: 1, RawJSON: payload, NormalizedJSON: payload, State: RowValid,
	}}); err != nil {
		t.Fatalf("write withdraw fixture = %v", err)
	}
	if err := workerStore.CompleteValidation(ctx, *withdrawClaim, "integration-import-worker"); err != nil {
		t.Fatalf("validate withdraw fixture = %v", err)
	}
	withdrawer := &integrationDraftWithdrawer{}
	if err := appStore.WithdrawImport(
		userContext, fixture.tenantA, fixture.domainA, fixture.userA,
		withdrawBatch.ID, withdrawer,
	); err != nil {
		t.Fatalf("WithdrawImport() error = %v", err)
	}
	withdrawn, err := appStore.Get(userContext, fixture.tenantA, fixture.domainA, withdrawBatch.ID)
	if err != nil || withdrawn.State != StateWithdrawn || withdrawer.rowCount != 0 {
		t.Fatalf("withdrawn batch = %#v, rows=%d, error=%v", withdrawn, withdrawer.rowCount, err)
	}
}

func TestPostgresSemanticExportJobLeaseCompletionRetryAndIsolation(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	if adminURL == "" || appURL == "" || workerURL == "" {
		t.Skip("set AskData admin, app, and worker integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	app, err := database.Open(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workerPool, err := database.Open(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()

	fixture := createImportFixture(t, ctx, admin)
	defer cleanupImportFixture(t, admin, fixture)
	appStore := NewPostgresExportJobStore(app)
	workerStore := NewPostgresExportJobStore(workerPool)
	userContext := database.WithAccessContext(ctx, fixture.userA, fixture.domainA)
	selection := ExportSelection{
		TenantID: fixture.tenantA, DomainID: fixture.domainA, ActorID: fixture.userA,
		AssetTypes: []AssetType{AssetEvalCase},
	}
	job, err := appStore.Create(userContext, CreateExportJobInput{Selection: selection})
	if err != nil || job.State != ExportPending || job.SourceRowCount != 0 ||
		job.ExpiresAt.IsZero() || job.PinnedVersionIDs == nil ||
		job.PinnedVersionIDs[AssetEvalCase] == nil {
		t.Fatalf("create export job = %#v, %v", job, err)
	}
	if err := database.WithTenantTx(ctx, workerPool, fixture.tenantA, func(tx pgx.Tx) error {
		var count int
		return tx.QueryRow(ctx, `SELECT count(*) FROM askdata.semantic_export_jobs`).Scan(&count)
	}); err == nil {
		t.Fatal("worker direct semantic_export_jobs SELECT unexpectedly succeeded")
	}
	tenantIDs, err := workerStore.ListTenantIDs(ctx)
	if err != nil || !containsString(tenantIDs, fixture.tenantA) {
		t.Fatalf("export tenant list = %#v, %v", tenantIDs, err)
	}
	claim, err := workerStore.Claim(ctx, fixture.tenantA, "export-worker", time.Minute)
	if err != nil || claim == nil || claim.ID != job.ID || claim.Attempt != 1 ||
		claim.LeaseToken == "" {
		t.Fatalf("claim export job = %#v, %v", claim, err)
	}
	artifact := ExportArtifact{
		ContentHash: strings.Repeat("c", 64), RowCount: 0,
		OmittedSensitiveMembers: 0,
	}
	if err := workerStore.Complete(
		ctx, *claim, "export-worker", artifact,
		"s3://uploads/semantic-exports/integration.xlsx",
	); err != nil {
		t.Fatalf("complete export job = %v", err)
	}
	ready, err := appStore.Get(userContext, fixture.tenantA, fixture.domainA, fixture.userA, job.ID)
	if err != nil || ready.State != ExportReady || ready.ContentHash != artifact.ContentHash ||
		ready.ObjectURI == "" || ready.CompletedAt == nil {
		t.Fatalf("ready export job = %#v, %v", ready, err)
	}
	otherContext := database.WithAccessContext(ctx, fixture.userB, fixture.domainB)
	if _, err := appStore.Get(otherContext, fixture.tenantB, fixture.domainA, fixture.userB, job.ID); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("cross-tenant export Get error = %v", err)
	}

	retryJob, err := appStore.Create(userContext, CreateExportJobInput{Selection: selection})
	if err != nil {
		t.Fatalf("create retry export job = %v", err)
	}
	retryClaim, err := workerStore.Claim(ctx, fixture.tenantA, "export-worker", time.Minute)
	if err != nil || retryClaim == nil || retryClaim.ID != retryJob.ID {
		t.Fatalf("claim retry export job = %#v, %v", retryClaim, err)
	}
	if err := workerStore.Fail(
		ctx, *retryClaim, "export-worker", "EXPORT_STORAGE_FAILED", true,
	); err != nil {
		t.Fatalf("retry export job = %v", err)
	}
	retryClaim, err = workerStore.Claim(ctx, fixture.tenantA, "export-worker", time.Minute)
	if err != nil || retryClaim == nil || retryClaim.Attempt != 2 {
		t.Fatalf("reclaim export job = %#v, %v", retryClaim, err)
	}
	if err := workerStore.Fail(
		ctx, *retryClaim, "export-worker", "EXPORT_CONTRACT_INVALID", false,
	); err != nil {
		t.Fatalf("fail export job = %v", err)
	}
	failed, err := appStore.Get(userContext, fixture.tenantA, fixture.domainA, fixture.userA, retryJob.ID)
	if err != nil || failed.State != ExportFailed || failed.FailureCode != "EXPORT_CONTRACT_INVALID" {
		t.Fatalf("failed export job = %#v, %v", failed, err)
	}
}

func TestPostgresImportCommitCertificationVersioningAndSelectiveWithdrawal(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	workerURL := os.Getenv("ASKDATA_INTEGRATION_WORKER_DATABASE_URL")
	if adminURL == "" || appURL == "" || workerURL == "" {
		t.Skip("set AskData admin, app, and worker integration database URLs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	app, err := database.Open(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	workerPool, err := database.Open(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()

	fixture := createImportFixture(t, ctx, admin)
	defer cleanupImportFixture(t, admin, fixture)
	appStore, workerStore := NewPostgresStore(app), NewPostgresStore(workerPool)
	adminContext := database.WithAccessContext(ctx, fixture.userA, fixture.domainA)
	viewerContext := database.WithAccessContext(ctx, fixture.viewerA, fixture.domainA)
	assertGovernedTermAdminCRUD(t, adminContext, app, fixture)
	creator := NewPostgresDraftCreator()
	rollbackRows := []map[string]string{
		{
			"term": "rollback_first", "termType": "OPERATOR", "targetCode": "SUM",
			"matchMode": "EXACT", "priority": "100", "negativeContexts": "",
			"validFrom": "2026-01-01", "validTo": "", "source": "IMPORT",
		},
		{
			"term": "rollback_second", "termType": "METRIC", "targetCode": "missing_metric",
			"matchMode": "EXACT", "priority": "100", "negativeContexts": "",
			"validFrom": "2026-01-01", "validTo": "", "source": "IMPORT",
		},
	}
	rollbackBatch := createValidatedImportBatch(
		t, ctx, appStore, workerStore, fixture, AssetTerm, rollbackRows, nil,
	)
	if _, _, err := appStore.CommitRows(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA, rollbackBatch.ID,
		CommitSelection{}, creator,
	); err == nil {
		t.Fatal("commit with a failing second row unexpectedly succeeded")
	} else {
		var rowErr *RowOperationError
		if !errors.As(err, &rowErr) || rowErr.RowNo != 2 {
			t.Fatalf("commit rollback row error = %T/%v", err, err)
		}
	}
	assertNoImportedTerms(t, adminContext, app, fixture.tenantA, "rollback_first", "rollback_second")

	rows := []map[string]string{
		{
			"term": "sales_total", "termType": "OPERATOR", "targetCode": "SUM",
			"matchMode": "EXACT", "priority": "100", "negativeContexts": "",
			"validFrom": "2026-01-01", "validTo": "", "source": "IMPORT",
		},
		{
			"term": "a+", "termType": "OPERATOR", "targetCode": "SUM",
			"matchMode": "REGEX_SAFE", "priority": "100", "negativeContexts": "",
			"validFrom": "2026-01-01", "validTo": "", "source": "IMPORT",
		},
	}
	batch := createValidatedImportBatch(t, ctx, appStore, workerStore, fixture, AssetTerm, rows,
		map[int][]ValidationIssue{1: {{
			Column: "impact", Code: ImportImpactRequiresReview,
			Message: "certified mapping has consumers", Expected: "explicit owner acknowledgement",
		}}})
	if _, _, err := appStore.CommitRows(
		viewerContext, fixture.tenantA, fixture.domainA, fixture.viewerA, batch.ID,
		CommitSelection{AcknowledgeImpact: true}, creator,
	); !errors.Is(err, ErrImportPermission) {
		t.Fatalf("viewer commit error = %v, want owner permission", err)
	}
	if _, _, err := appStore.CommitRows(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA, batch.ID,
		CommitSelection{}, creator,
	); !errors.Is(err, ErrImportImpactAck) {
		t.Fatalf("unacknowledged commit error = %v", err)
	}
	state, references, err := appStore.CommitRows(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA, batch.ID,
		CommitSelection{AcknowledgeImpact: true}, creator,
	)
	if err != nil || state != StateCommitted || len(references) != 2 {
		t.Fatalf("real TERM commit = %s/%#v/%v", state, references, err)
	}
	versionIDs := []string{references[0].VersionID, references[1].VersionID}
	certifier := registry.NewCertificationService(app)
	scope := registry.AdminScope{
		TenantID: fixture.tenantA, DomainID: fixture.domainA, ActorID: fixture.userA,
	}
	if _, err := certifier.BulkCertify(adminContext, scope, fixture.domainA, versionIDs, "owner review"); err == nil {
		t.Fatal("unsafe REGEX_SAFE term was bulk certified")
	} else {
		var rejected *registry.BulkCertificationError
		if !errors.As(err, &rejected) || len(rejected.Failures) == 0 {
			t.Fatalf("bulk rejection = %T/%v", err, err)
		}
	}
	assertImportVersions(t, adminContext, app, fixture.tenantA, versionIDs, "DRAFT")
	if err := database.WithTenantTx(adminContext, app, fixture.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE askdata.business_term_versions
			SET match_mode='EXACT',match_pattern=NULL,content_hash=$1 WHERE id=$2`,
			strings.Repeat("c", 64), references[1].VersionID)
		return err
	}); err != nil {
		t.Fatalf("repair unsafe draft: %v", err)
	}
	result, err := certifier.BulkCertify(adminContext, scope, fixture.domainA, versionIDs, "owner review")
	if err != nil || len(result.Certified) != 2 {
		t.Fatalf("bulk certification = %#v/%v", result, err)
	}
	assertImportVersions(t, adminContext, app, fixture.tenantA, versionIDs, "CERTIFIED")
	var auditCount int
	if err := database.WithTenantTx(adminContext, app, fixture.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM askdata.audit_events
			WHERE domain_id=$1 AND event_type='SEMANTIC_VERSION_CERTIFIED'
			  AND resource_id=ANY($2::text[])`, fixture.domainA,
			[]string{references[0].ObjectID, references[1].ObjectID}).Scan(&auditCount)
	}); err != nil || auditCount != 2 {
		t.Fatalf("certification audit count = %d/%v", auditCount, err)
	}

	conflictingRow := map[string]string{
		"term": "sales_total", "termType": "OPERATOR", "targetCode": "AVG",
		"matchMode": "EXACT", "priority": "100", "negativeContexts": "",
		"validFrom": "2026-01-01", "validTo": "", "source": "IMPORT",
	}
	conflictingBatch := createValidatedImportBatch(
		t, ctx, appStore, workerStore, fixture, AssetTerm, []map[string]string{conflictingRow}, nil,
	)
	_, conflictingReferences, err := appStore.CommitRows(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA, conflictingBatch.ID,
		CommitSelection{}, creator,
	)
	if err != nil || len(conflictingReferences) != 1 {
		t.Fatalf("commit conflicting TERM version = %#v/%v", conflictingReferences, err)
	}
	if _, err := certifier.BulkCertify(adminContext, scope, fixture.domainA,
		[]string{conflictingReferences[0].VersionID}, "same-priority conflict"); err == nil {
		t.Fatal("same-priority overlapping TERM unexpectedly certified")
	} else {
		var rejected *registry.BulkCertificationError
		if !errors.As(err, &rejected) || len(rejected.Failures) != 1 ||
			rejected.Failures[0].Code != "TERM_PRIORITY_CONFLICT" ||
			len(rejected.Failures[0].Conflicts) == 0 {
			t.Fatalf("same-priority conflict = %T/%#v/%v", err, rejected, err)
		}
	}
	assertImportVersions(t, adminContext, app, fixture.tenantA,
		[]string{conflictingReferences[0].VersionID}, "DRAFT")

	higherPriorityReference := conflictingReferences[0]
	if err := database.WithTenantTx(adminContext, app, fixture.tenantA, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE askdata.business_term_versions
			SET priority=200,content_hash=$1 WHERE id=$2 AND status='DRAFT'`,
			strings.Repeat("d", 64), higherPriorityReference.VersionID)
		return err
	}); err != nil {
		t.Fatalf("record explicit higher-priority TERM selection: %v", err)
	}
	if _, err := certifier.BulkCertify(adminContext, scope, fixture.domainA,
		[]string{higherPriorityReference.VersionID}, ""); err == nil {
		t.Fatal("different-priority TERM without an owner note unexpectedly certified")
	} else {
		var rejected *registry.BulkCertificationError
		if !errors.As(err, &rejected) || len(rejected.Failures) != 1 ||
			rejected.Failures[0].Code != "TERM_PRIORITY_OVERRIDE_NOTE_REQUIRED" {
			t.Fatalf("missing override note rejection = %T/%#v/%v", err, rejected, err)
		}
	}
	if _, err := certifier.BulkCertify(adminContext, scope, fixture.domainA,
		[]string{higherPriorityReference.VersionID}, "different-priority shadow"); err != nil {
		t.Fatalf("different-priority TERM certification error = %v", err)
	}
	var shadowCount int
	if err := database.WithTenantTx(adminContext, app, fixture.tenantA, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(jsonb_array_length(detail->'termPriorityShadows'),0)
			FROM askdata.audit_events
			WHERE domain_id=$1 AND event_type='SEMANTIC_VERSION_CERTIFIED'
			  AND resource_id=$2 AND detail->>'objectVersionId'=$3
			ORDER BY created_at DESC LIMIT 1`, fixture.domainA,
			higherPriorityReference.ObjectID, higherPriorityReference.VersionID).
			Scan(&shadowCount)
	}); err != nil || shadowCount == 0 {
		t.Fatalf("different-priority audit shadows = %d/%v", shadowCount, err)
	}

	nonOverlappingRows := []map[string]string{
		{
			"term": "seasonal_sales", "termType": "OPERATOR", "targetCode": "SUM",
			"matchMode": "EXACT", "priority": "100", "negativeContexts": "",
			"validFrom": "2026-01-01", "validTo": "2026-06-01", "source": "IMPORT",
		},
		{
			"term": "seasonal_sales", "termType": "OPERATOR", "targetCode": "AVG",
			"matchMode": "EXACT", "priority": "100", "negativeContexts": "",
			"validFrom": "2026-06-01", "validTo": "2026-12-01", "source": "IMPORT",
		},
	}
	for index, row := range nonOverlappingRows {
		batch := createValidatedImportBatch(
			t, ctx, appStore, workerStore, fixture, AssetTerm, []map[string]string{row}, nil,
		)
		_, termReferences, err := appStore.CommitRows(
			adminContext, fixture.tenantA, fixture.domainA, fixture.userA, batch.ID,
			CommitSelection{}, creator,
		)
		if err != nil || len(termReferences) != 1 {
			t.Fatalf("commit non-overlapping TERM %d = %#v/%v", index, termReferences, err)
		}
		if _, err := certifier.BulkCertify(adminContext, scope, fixture.domainA,
			[]string{termReferences[0].VersionID}, "non-overlapping validity"); err != nil {
			t.Fatalf("certify non-overlapping TERM %d: %v", index, err)
		}
		conflicts, err := registry.NewTermService(app).DetectConflicts(
			adminContext, scope, termReferences[0].VersionID,
		)
		if err != nil || len(conflicts) != 0 {
			t.Fatalf("non-overlapping TERM conflicts %d = %#v/%v", index, conflicts, err)
		}
	}

	newVersionBatch := createValidatedImportBatch(t, ctx, appStore, workerStore, fixture, AssetTerm,
		[]map[string]string{rows[0]}, nil)
	newState, newer, err := appStore.CommitRows(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA, newVersionBatch.ID,
		CommitSelection{}, creator,
	)
	if err != nil || newState != StateCommitted || len(newer) != 1 ||
		newer[0].ObjectID != references[0].ObjectID || newer[0].VersionID == references[0].VersionID {
		t.Fatalf("new version commit = %s/%#v/%v", newState, newer, err)
	}
	withdrawer := NewPostgresDraftWithdrawer()
	rejections, err := appStore.WithdrawImportSelective(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA,
		newVersionBatch.ID, "replaced source row", withdrawer,
	)
	if err != nil || len(rejections) != 0 {
		t.Fatalf("withdraw removable DRAFT = %#v/%v", rejections, err)
	}
	withdrawn, err := appStore.Get(adminContext, fixture.tenantA, fixture.domainA, newVersionBatch.ID)
	if err != nil || withdrawn.State != StateWithdrawn {
		t.Fatalf("read withdrawn batch = %#v/%v", withdrawn, err)
	}

	referencedBatch := createValidatedImportBatch(t, ctx, appStore, workerStore, fixture, AssetTerm,
		[]map[string]string{rows[0]}, nil)
	_, referenced, err := appStore.CommitRows(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA, referencedBatch.ID,
		CommitSelection{}, creator,
	)
	if err != nil || len(referenced) != 1 {
		t.Fatalf("create release-referenced DRAFT = %#v/%v", referenced, err)
	}
	insertDraftReleaseReference(t, ctx, admin, fixture, referenced[0])
	rejections, err = appStore.WithdrawImportSelective(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA,
		referencedBatch.ID, "incorrect import", withdrawer,
	)
	if err != nil || len(rejections) != 1 || rejections[0].Reason != "VERSION_REFERENCED" ||
		len(rejections[0].References) != 1 || !strings.HasPrefix(rejections[0].References[0], "RELEASE:") {
		t.Fatalf("withdraw release-referenced DRAFT = %#v/%v", rejections, err)
	}
	assertImportVersions(t, adminContext, app, fixture.tenantA,
		[]string{referenced[0].VersionID}, "DRAFT")

	rejections, err = appStore.WithdrawImportSelective(
		adminContext, fixture.tenantA, fixture.domainA, fixture.userA,
		batch.ID, "certified in error", withdrawer,
	)
	if err != nil || len(rejections) != 2 {
		t.Fatalf("withdraw certified versions = %#v/%v", rejections, err)
	}
	for _, rejection := range rejections {
		if rejection.Reason != "VERSION_NOT_DRAFT" {
			t.Fatalf("certified rejection = %#v", rejection)
		}
	}
}

func assertNoImportedTerms(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID string,
	terms ...string,
) {
	t.Helper()
	var count int
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM askdata.business_terms
			WHERE term=ANY($1::text[])`, terms).Scan(&count)
	}); err != nil || count != 0 {
		t.Fatalf("rolled-back imported term count = %d/%v", count, err)
	}
}

func insertDraftReleaseReference(
	t *testing.T,
	ctx context.Context,
	admin *pgxpool.Pool,
	fixture importFixture,
	reference DraftReference,
) {
	t.Helper()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	releaseID := uuid.NewString()
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,status,object_count,created_by,updated_by
	) VALUES($1,$2,$3,$4,$5,'DRAFT',1,$6,$6)`, releaseID, fixture.tenantA,
		fixture.domainA, "import-reference-"+releaseID[:8], strings.Repeat("e", 64), fixture.userA); err != nil {
		t.Fatalf("insert release fixture: %v", err)
	}
	var contentHash string
	if err := tx.QueryRow(ctx, `SELECT content_hash FROM askdata.business_term_versions
		WHERE id=$1`, reference.VersionID).Scan(&contentHash); err != nil {
		t.Fatalf("read referenced content hash: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
		tenant_id,domain_id,release_id,object_type,object_id,object_version_id,
		content_hash,contract_json
	) VALUES($1,$2,$3,'BUSINESS_TERM',$4,$5,$6,'{}'::jsonb)`, fixture.tenantA,
		fixture.domainA, releaseID, reference.ObjectID, reference.VersionID, contentHash); err != nil {
		t.Fatalf("insert release object fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func createValidatedImportBatch(
	t *testing.T,
	ctx context.Context,
	appStore, workerStore *PostgresStore,
	fixture importFixture,
	assetType AssetType,
	values []map[string]string,
	issues map[int][]ValidationIssue,
) SemanticImport {
	t.Helper()
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	input := CreateImportInput{
		TenantID: fixture.tenantA, DomainID: fixture.domainA, AssetType: assetType,
		FileObjectURI: "minio://semantic-imports/" + token + ".csv",
		FileHash:      token + token, FileName: strings.ToLower(string(assetType)) + ".csv",
		CreatedBy: fixture.userA,
	}
	userContext := database.WithAccessContext(ctx, fixture.userA, fixture.domainA)
	batch, created, err := appStore.CreateImport(userContext, input)
	if err != nil || !created {
		t.Fatalf("create %s import = %#v/%v/%v", assetType, batch, created, err)
	}
	claim, err := workerStore.ClaimForValidation(ctx, fixture.tenantA, "import-004-worker", time.Minute)
	if err != nil || claim == nil || claim.ImportID != batch.ID {
		t.Fatalf("claim %s import = %#v/%v", assetType, claim, err)
	}
	validated := make([]ValidatedRow, len(values))
	for index, value := range values {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		validated[index] = ValidatedRow{
			RowNo: index + 1, RawJSON: payload, NormalizedJSON: payload,
			State: RowValid, Errors: issues[index+1],
		}
	}
	if err := workerStore.UpsertRows(ctx, *claim, "import-004-worker", validated); err != nil {
		t.Fatalf("write %s rows: %v", assetType, err)
	}
	if err := workerStore.CompleteValidation(ctx, *claim, "import-004-worker"); err != nil {
		t.Fatalf("complete %s validation: %v", assetType, err)
	}
	return batch
}

func assertGovernedTermAdminCRUD(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture importFixture,
) {
	t.Helper()
	store := registry.NewPostgresStore(pool)
	scope := registry.AdminScope{
		TenantID: fixture.tenantA, DomainID: fixture.domainA, ActorID: fixture.userA,
	}
	targetID := uuid.NewString()
	input := &registry.BusinessTermDraftInput{
		VersionedDraftInput: registry.VersionedDraftInput{VersionNo: 1},
		Term:                "管理词典", TermType: registry.TermTypeOperator,
		TargetObjectType: registry.TermTargetOperator, TargetVersionID: targetID,
		TargetCode: "SUM", MatchMode: registry.TermMatchExact, Priority: 150,
		NegativeContexts: []string{"平均"}, Source: registry.TermSourceFeedback,
		Code: "admin_term_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:8],
		Name: "管理词典", Definition: "真实应用角色下的完整业务词 CRUD",
		Aliases: []string{"管理术语"},
	}
	createID := uuid.NewString()
	created, err := store.CreateDraft(ctx, scope, registry.AdminResourceBusinessTerm,
		registry.AdminMutation{BusinessTerm: input}, registry.AdminCommand{
			RequestID: createID, ActionHash: askdata.HashBytes([]byte("create-governed-term")),
		})
	if err != nil {
		t.Fatalf("public governed TERM create: %v", err)
	}
	loadedValue, err := store.GetDraft(ctx, scope, registry.AdminResourceBusinessTerm, created.ResourceID)
	if err != nil {
		t.Fatalf("public governed TERM get: %v", err)
	}
	loaded := loadedValue.(registry.BusinessTerm)
	if loaded.TargetVersionID != targetID || loaded.Priority != 150 ||
		loaded.ReviewStatus != registry.TermReviewPending || loaded.Source != registry.TermSourceFeedback {
		t.Fatalf("public governed TERM loaded = %#v", loaded)
	}
	input.ExpectedUpdatedAt = &loaded.UpdatedAt
	input.Definition = "更新后的真实应用角色业务词"
	updateID := uuid.NewString()
	updated, err := store.UpdateDraft(ctx, scope, registry.AdminResourceBusinessTerm,
		created.ResourceID, registry.AdminMutation{BusinessTerm: input}, registry.AdminCommand{
			RequestID: updateID, ActionHash: askdata.HashBytes([]byte("update-governed-term")),
		})
	if err != nil {
		t.Fatalf("public governed TERM update: %v", err)
	}
	deleteID := uuid.NewString()
	if _, err := store.DeleteDraft(ctx, scope, registry.AdminResourceBusinessTerm,
		created.ResourceID, registry.DeleteDraftInput{ExpectedUpdatedAt: updated.UpdatedAt},
		registry.AdminCommand{
			RequestID: deleteID, ActionHash: askdata.HashBytes([]byte("delete-governed-term")),
		}); err != nil {
		t.Fatalf("public governed TERM delete: %v", err)
	}
}

func assertImportVersions(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID string,
	versionIDs []string,
	want string,
) {
	t.Helper()
	var count int
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM askdata.business_term_versions
			WHERE id=ANY($1::uuid[]) AND status=$2`, versionIDs, want).Scan(&count)
	}); err != nil || count != len(versionIDs) {
		t.Fatalf("version status %s count = %d/%v", want, count, err)
	}
}

type integrationDraftCreator struct{ status string }

func (creator integrationDraftCreator) CreateDraft(
	context.Context,
	pgx.Tx,
	SemanticImport,
	ImportRow,
) (DraftReference, error) {
	status := "DRAFT"
	if creator.status != "" {
		status = creator.status
	}
	return DraftReference{
		ObjectID: uuid.NewString(), VersionID: uuid.NewString(), Status: status,
	}, nil
}

type integrationDraftWithdrawer struct{ rowCount int }

func (withdrawer *integrationDraftWithdrawer) WithdrawDrafts(
	_ context.Context,
	_ pgx.Tx,
	_ SemanticImport,
	rows []ImportRow,
) error {
	withdrawer.rowCount = len(rows)
	return nil
}

func assertImportStateRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, importID string,
	target State,
) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE askdata.semantic_imports SET state=$1 WHERE id=$2`, target, importID)
		return err
	})
	if err == nil {
		t.Fatalf("illegal database transition to %s was accepted", target)
	}
}

type importFixture struct {
	tenantA, domainA, userA, viewerA string
	tenantB, domainB, userB          string
}

func createImportFixture(t *testing.T, ctx context.Context, admin *pgxpool.Pool) importFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := importFixture{
		tenantA: uuid.NewString(), domainA: uuid.NewString(), userA: uuid.NewString(), viewerA: uuid.NewString(),
		tenantB: uuid.NewString(), domainB: uuid.NewString(), userB: uuid.NewString(),
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for index, values := range [][3]string{
		{fixture.tenantA, fixture.domainA, fixture.userA},
		{fixture.tenantB, fixture.domainB, fixture.userB},
	} {
		letter := string(rune('a' + index))
		if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
			VALUES($1,$2,$3)`, values[0], "import_"+suffix+letter, "Import fixture "+letter); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash,employee_no
		) VALUES($1,$2,$3,$4,$5,$6)`, values[2], values[0],
			"import_"+suffix+letter+"@example.invalid", "Import fixture", "not-a-login-hash",
			"IMPORT"+strings.ToUpper(suffix)+strings.ToUpper(letter)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
			id,tenant_id,code,name,is_default,created_by
		) VALUES($1,$2,$3,$4,true,$5)`, values[1], values[0],
			"import_"+suffix+letter, "Import domain", values[2]); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(
			id,tenant_id,code,name,owner_id
		) VALUES($1,$2,$3,$4,$5)`, values[1], values[0],
			"import_"+suffix+letter, "Import domain", values[2]); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,member_role,assigned_by
		) VALUES($1,$2,$3,'DOMAIN_ADMIN',$3)`, values[0], values[1], values[2]); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
		id,tenant_id,email,display_name,password_hash,employee_no
	) VALUES($1,$2,$3,'Import viewer','not-a-login-hash',$4)`, fixture.viewerA,
		fixture.tenantA, "import_"+suffix+"_viewer@example.invalid",
		"IMPORT"+strings.ToUpper(suffix)+"VIEW"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
		tenant_id,domain_id,user_id,member_role,assigned_by
	) VALUES($1,$2,$3,'MEMBER',$4)`, fixture.tenantA, fixture.domainA,
		fixture.viewerA, fixture.userA); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupImportFixture(t *testing.T, admin *pgxpool.Pool, fixture importFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Errorf("begin cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable cleanup triggers: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.audit_events
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete audit fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.release_objects
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete release object fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.semantic_export_jobs
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete export fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.releases
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete release fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.business_term_versions
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete business term version fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.business_terms
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete business term fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.semantic_imports
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete import fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM askdata.domains
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete AskData domain fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete membership fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.business_domains
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete domain fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.users
		WHERE tenant_id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete user fixtures: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.tenants
		WHERE id=ANY($1::uuid[])`, []string{fixture.tenantA, fixture.tenantB}); err != nil {
		t.Errorf("delete tenant fixtures: %v", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit cleanup: %v", err)
	}
}
