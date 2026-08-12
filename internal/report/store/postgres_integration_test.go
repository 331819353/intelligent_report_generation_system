package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/operation"
)

func TestPrepareNearFiveMegabyteDefinition(t *testing.T) {
	definition := reportStoreDefinition(t, uuid.NewString(), "report_near_limit", "Near limit")
	_, prepared := nearLimitDefinition(t, definition)
	if len(prepared.Canonical) < reportmodel.MaxDefinitionBytes-256*1024 ||
		len(prepared.Canonical) > reportmodel.MaxDefinitionBytes {
		t.Fatalf("canonical definition bytes = %d", len(prepared.Canonical))
	}
}

func TestPostgresStoreOperationUndoRedoAndAIGuards(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
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

	fixture := createReportStoreFixture(t, ctx, adminPool)
	defer cleanupReportStoreFixture(t, adminPool, fixture)
	store := NewPostgresStore(appPool)
	owner := Identity{TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.ownerID), DomainID: askdata.ID(fixture.domainID)}
	definition := reportStoreDefinition(t, fixture.reportID, fixture.code, "Operation integration")
	created, initial, err := store.CreateReport(ctx, owner, CreateInput{
		ID: askdata.ID(fixture.reportID), Code: fixture.code, Name: definition.Metadata.Name,
		ReportType: definition.Metadata.ReportType, Definition: definition,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: 0,
		Operations: []operation.Operation{
			pageRename(definition, "temporary"),
			{Op: operation.PageUpdate, TargetID: "page_missing", Payload: &operation.PageUpdatePayload{Name: "fail"}},
		},
	})
	var applyError *operation.ApplyError
	if !errors.As(err, &applyError) || applyError.Index != 1 {
		t.Fatalf("atomic bundle error = %#v / %v", applyError, err)
	}
	unchanged, err := store.GetDraft(ctx, owner, created.ID)
	if err != nil || unchanged.RevisionNo != 0 || unchanged.DefinitionHash != initial.DefinitionHash {
		t.Fatalf("failed bundle changed draft = %#v, %v", unchanged, err)
	}
	replacement := definition
	replacement.Metadata.ID = "report_other_identity"
	_, _, err = store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: 0,
		Operations: []operation.Operation{{
			Op: operation.ReportCreate, TargetID: created.ID,
			Payload: &operation.ReportCreatePayload{Definition: replacement},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot change report ID") {
		t.Fatalf("report identity replacement error = %v", err)
	}

	pageID := definition.Pages[0].ID
	aiRunID := askdata.ID(uuid.NewString())
	_, _, err = store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: 0,
		Operations: []operation.Operation{{
			Op: operation.ComponentUpdate, TargetID: definition.Components[0].ID,
			Payload: &operation.ComponentUpdatePayload{Options: definition.Components[0].Options},
		}},
		Source: string(operation.SourceAI), AIRunID: aiRunID, Scope: &operation.Scope{PageID: &pageID},
	})
	if !errors.Is(err, ErrAIEditForbidden) {
		t.Fatalf("AI edit without capability error = %v", err)
	}
	grantReportAIEdit(t, ctx, adminPool, fixture)
	missingPageID := askdata.ID("page_missing")
	_, _, err = store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: 0,
		Operations: []operation.Operation{{
			Op: operation.ComponentUpdate, TargetID: definition.Components[0].ID,
			Payload: &operation.ComponentUpdatePayload{Options: definition.Components[0].Options},
		}},
		Source: string(operation.SourceAI), AIRunID: aiRunID, Scope: &operation.Scope{PageID: &missingPageID},
	})
	if operation.ErrorCode(err) != operation.CodeOutOfScope {
		t.Fatalf("AI out-of-scope error = %v", err)
	}
	revisions, err := store.ListRevisions(ctx, owner, created.ID, 20)
	if err != nil || len(revisions) != 0 {
		t.Fatalf("rejected operations created revisions = %#v, %v", revisions, err)
	}

	first, _, err := store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: 0, Operations: []operation.Operation{pageRename(definition, "Revision A")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: first.RevisionNo, Operations: []operation.Operation{pageRename(first.Definition, "Revision B")},
	})
	if err != nil {
		t.Fatal(err)
	}
	undoB, revision3, err := store.Undo(ctx, owner, created.ID)
	if err != nil || undoB.Definition.Pages[0].Name != "Revision A" || revision3.Source != "UNDO" {
		t.Fatalf("undo B = %#v / %#v, %v", undoB, revision3, err)
	}
	undoA, _, err := store.Undo(ctx, owner, created.ID)
	if err != nil || undoA.DefinitionHash != initial.DefinitionHash {
		t.Fatalf("undo A = %#v, %v", undoA, err)
	}
	redoA, _, err := store.Redo(ctx, owner, created.ID)
	if err != nil || redoA.Definition.Pages[0].Name != "Revision A" {
		t.Fatalf("redo A = %#v, %v", redoA, err)
	}
	redoB, _, err := store.Redo(ctx, owner, created.ID)
	if err != nil || redoB.Definition.Pages[0].Name != "Revision B" {
		t.Fatalf("redo B = %#v, %v", redoB, err)
	}
	if _, _, err := store.Redo(ctx, owner, created.ID); !errors.Is(err, ErrNothingToRedo) {
		t.Fatalf("extra redo error = %v", err)
	}
	undoRedo, _, err := store.Undo(ctx, owner, created.ID)
	if err != nil || undoRedo.Definition.Pages[0].Name != "Revision A" {
		t.Fatalf("undo of redo = %#v, %v", undoRedo, err)
	}
	redoAgain, _, err := store.Redo(ctx, owner, created.ID)
	if err != nil || redoAgain.Definition.Pages[0].Name != "Revision B" {
		t.Fatalf("redo after undo of redo = %#v, %v", redoAgain, err)
	}

	beforeTemplateHash := redoAgain.DefinitionHash
	templateRef := redoAgain.Definition.TemplateRef
	templateRef.ReportTemplateVersion = "9.9.9"
	templated, _, err := store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: redoAgain.RevisionNo,
		Operations: []operation.Operation{{Op: operation.TemplateApply, TargetID: created.ID,
			Payload: &operation.TemplateApplyPayload{TemplateRef: templateRef}}},
	})
	if err != nil || templated.DefinitionHash == beforeTemplateHash {
		t.Fatalf("template apply = %#v, %v", templated, err)
	}
	restored, snapshotUndo, err := store.Undo(ctx, owner, created.ID)
	if err != nil || restored.DefinitionHash != beforeTemplateHash {
		t.Fatalf("template snapshot undo = %#v / %#v, %v", restored, snapshotUndo, err)
	}
	var inverse []operation.Operation
	if err := json.Unmarshal(snapshotUndo.OperationJSON, &inverse); err != nil || len(inverse) != 1 || inverse[0].Op != operation.ReportCreate {
		t.Fatalf("template inverse operation = %#v, %v", inverse, err)
	}
	settings := restored.Definition.Metadata
	settings.Name = "Renamed operation report"
	renamed, _, err := store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: restored.RevisionNo,
		Operations: []operation.Operation{{
			Op: operation.ReportSettingsUpdate, TargetID: created.ID,
			Payload: &operation.ReportSettingsUpdatePayload{Metadata: settings, RuntimePolicy: restored.Definition.RuntimePolicy},
		}},
	})
	reportRow, getErr := store.GetReport(ctx, owner, created.ID)
	if err != nil || getErr != nil || reportRow.Name != settings.Name || renamed.Definition.Metadata.Name != settings.Name {
		t.Fatalf("report settings synchronization = %#v / %#v, %v / %v", renamed, reportRow, err, getErr)
	}
	if _, _, err := store.Undo(ctx, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	restoredReport, err := store.GetReport(ctx, owner, created.ID)
	if err != nil || restoredReport.Name != definition.Metadata.Name {
		t.Fatalf("report settings undo synchronization = %#v, %v", restoredReport, err)
	}
}

func TestPostgresStoreReportLifecycleConcurrencyImmutabilityAndRLS(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
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

	fixture := createReportStoreFixture(t, ctx, adminPool)
	defer cleanupReportStoreFixture(t, adminPool, fixture)
	store := NewPostgresStore(appPool)
	owner := Identity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.ownerID),
		DomainID: askdata.ID(fixture.domainID),
	}
	observer := Identity{
		TenantID: askdata.ID(fixture.tenantID), ActorID: askdata.ID(fixture.observerID),
		DomainID: askdata.ID(fixture.domainID),
	}
	crossTenant := Identity{
		TenantID: askdata.ID(fixture.otherTenantID), ActorID: askdata.ID(fixture.otherOwnerID),
		DomainID: askdata.ID(fixture.otherDomainID),
	}

	definition := reportStoreDefinition(t, fixture.reportID, fixture.code, "Report Store Integration")
	created, draft, err := store.CreateReport(ctx, owner, CreateInput{
		ID: askdata.ID(fixture.reportID), Code: fixture.code, Name: definition.Metadata.Name,
		ReportType: definition.Metadata.ReportType, Definition: definition,
	})
	if err != nil {
		t.Fatalf("CreateReport() error = %v", err)
	}
	if created.OwnerUserID != owner.ActorID || created.DomainID != owner.DomainID || draft.RevisionNo != 0 {
		t.Fatalf("created report/draft = %#v / %#v", created, draft)
	}

	if count := visibleDraftIndexRows(t, ctx, appPool, observer, created.ID); count != 0 {
		t.Fatalf("ungranted observer saw %d draft index rows", count)
	}
	if _, err := store.GetDraft(ctx, observer, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ungranted same-tenant observer error = %v", err)
	}
	if _, err := store.GetDraft(ctx, crossTenant, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant draft error = %v", err)
	}
	if _, err := adminPool.Exec(ctx, `INSERT INTO platform.object_permissions(
		tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
	) VALUES($1,'USER',$2,'REPORT',$3,'VIEW',$4)`, fixture.tenantID, fixture.observerID,
		fixture.reportID, fixture.ownerID); err != nil {
		t.Fatalf("grant report VIEW: %v", err)
	}
	if observed, err := store.GetDraft(ctx, observer, created.ID); err != nil || observed.ReportID != created.ID {
		t.Fatalf("granted observer GetDraft() = %#v, %v", observed, err)
	}
	if count := visibleDraftIndexRows(t, ctx, appPool, observer, created.ID); count == 0 {
		t.Fatal("VIEW-granted observer could not read draft indexes")
	}
	if _, _, err := store.SaveDraftWithRevision(ctx, observer, created.ID, SaveInput{
		ExpectedRevision: 0, Operations: []operation.Operation{pageRename(definition, "forbidden")},
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("VIEW-only observer save error = %v", err)
	}

	start := make(chan struct{})
	type saveResult struct {
		draft    Draft
		revision Revision
		err      error
	}
	results := make(chan saveResult, 2)
	var wait sync.WaitGroup
	for _, name := range []string{"Concurrent A", "Concurrent B"} {
		name := name
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			saved, revision, saveErr := store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
				ExpectedRevision: 0, Operations: []operation.Operation{pageRename(definition, name)},
			})
			results <- saveResult{draft: saved, revision: revision, err: saveErr}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			if result.draft.RevisionNo != 1 || result.revision.RevisionNo != 1 || result.revision.BaseRevisionNo != 0 {
				t.Fatalf("first revision = %#v / %#v", result.draft, result.revision)
			}
		case errors.Is(result.err, ErrRevisionConflict):
			conflicts++
			var conflict *RevisionConflict
			if !errors.As(result.err, &conflict) || conflict.Current != 1 {
				t.Fatalf("concurrent conflict = %#v / %v", conflict, result.err)
			}
		default:
			t.Fatalf("concurrent save error = %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent saves: successes=%d conflicts=%d", successes, conflicts)
	}

	current, err := store.GetDraft(ctx, owner, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, revisionTwo, err := store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: current.RevisionNo,
		Operations:       []operation.Operation{pageRename(current.Definition, "Revision Two")},
	})
	if err != nil || second.RevisionNo != 2 || revisionTwo.RevisionNo != 2 || revisionTwo.BaseRevisionNo != 1 {
		t.Fatalf("second revision = %#v / %#v, %v", second, revisionTwo, err)
	}
	rollbackTx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rollbackTx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),set_config('app.access_mode','USER',true),
		set_config('app.user_id',$2,true),set_config('app.domain_id',$3,true)`,
		fixture.tenantID, fixture.ownerID, fixture.domainID); err != nil {
		_ = rollbackTx.Rollback(ctx)
		t.Fatal(err)
	}
	transientTemplate := second.Definition.TemplateRef
	transientTemplate.ReportTemplateVersion = "9.9.9"
	transientDraft, _, err := saveDraftWithRevisionTx(ctx, rollbackTx, owner, created.ID, SaveInput{
		ExpectedRevision: second.RevisionNo,
		Source:           string(operation.SourceUser),
		Operations: []operation.Operation{{
			Op: operation.TemplateApply, TargetID: created.ID,
			Payload: &operation.TemplateApplyPayload{TemplateRef: transientTemplate},
		}},
	})
	if err != nil {
		_ = rollbackTx.Rollback(ctx)
		t.Fatalf("transient save for rollback: %v", err)
	}
	if err := verifyDraftIndexes(ctx, rollbackTx, created.ID, transientDraft.RevisionNo,
		compiler.BuildIndexes(transientDraft.Definition)); err != nil {
		_ = rollbackTx.Rollback(ctx)
		t.Fatalf("transient draft indexes: %v", err)
	}
	if err := rollbackTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := store.GetDraft(ctx, owner, created.ID)
	if err != nil || rolledBack.RevisionNo != second.RevisionNo ||
		rolledBack.Definition.TemplateRef.ReportTemplateVersion == "9.9.9" {
		t.Fatalf("draft/index transaction rollback = %#v, %v", rolledBack, err)
	}
	if _, _, err := store.SaveDraftWithRevision(ctx, owner, created.ID, SaveInput{
		ExpectedRevision: 0, Operations: []operation.Operation{pageRename(second.Definition, "Stale")},
	}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale save error = %v", err)
	} else {
		var conflict *RevisionConflict
		if !errors.As(err, &conflict) || conflict.Current != 2 || len(conflict.Summaries) != 2 {
			t.Fatalf("stale conflict = %#v", conflict)
		}
	}
	revisions, err := store.ListRevisions(ctx, owner, created.ID, 10)
	if err != nil || len(revisions) != 2 || revisions[0].RevisionNo != 2 || revisions[1].RevisionNo != 1 {
		t.Fatalf("ListRevisions() = %#v, %v", revisions, err)
	}
	for _, expected := range []Draft{draft, current, second} {
		revisionNo := expected.RevisionNo
		historical, historicalErr := store.GetDraftRevision(ctx, owner, created.ID, &revisionNo)
		if historicalErr != nil || historical.RevisionNo != revisionNo || historical.DefinitionHash != expected.DefinitionHash {
			t.Fatalf("GetDraftRevision(%d) = %#v, %v; want hash %s", revisionNo, historical, historicalErr, expected.DefinitionHash)
		}
	}
	unavailableRevision := second.RevisionNo + 1
	if _, err := store.GetDraftRevision(ctx, owner, created.ID, &unavailableRevision); !errors.Is(err, ErrRevisionUnavailable) {
		t.Fatalf("unavailable revision error = %v", err)
	}

	largeDefinition, prepared := nearLimitDefinition(t, second.Definition)
	largeDefinition.Components[0].DataBinding = reportStoreSemanticBinding(
		t, fixture.releaseID, fixture.domainID,
	)
	prepared, err = Prepare(largeDefinition)
	if err != nil {
		t.Fatalf("prepare semantic release version: %v", err)
	}
	version, err := store.CreateVersion(ctx, owner, created.ID, CreateVersionInput{
		ID: askdata.ID(uuid.NewString()), SourceRevisionNo: second.RevisionNo,
		Definition: largeDefinition, ObjectURI: "s3://report-integration/version-1.json",
	})
	if err != nil {
		t.Fatalf("CreateVersion(%d bytes) error = %v", len(prepared.Canonical), err)
	}
	if version.VersionNo != 1 || version.SourceRevisionNo != 2 || version.DefinitionHash != prepared.Hash {
		t.Fatalf("created version = %#v", version)
	}
	if err := store.CompletePublication(ctx, owner, created.ID, version.ID); err != nil {
		t.Fatalf("complete initial publication: %v", err)
	}
	version.ArtifactState = "READY"
	var retainedReferenceName, releaseStatus string
	if err := adminPool.QueryRow(ctx, `SELECT reference.reference_name,release.status
		FROM askdata.release_references AS reference
		JOIN askdata.releases AS release
		  ON release.id=reference.release_id AND release.tenant_id=reference.tenant_id
		WHERE reference.release_id=$1 AND reference.reference_type='REPORT_VERSION'
		  AND reference.reference_id=$2 AND reference.released_at IS NULL`,
		fixture.releaseID, version.ID,
	).Scan(&retainedReferenceName, &releaseStatus); err != nil {
		t.Fatalf("semantic release report-version reference: %v", err)
	}
	if retainedReferenceName == "" || releaseStatus != "ACTIVE" {
		t.Fatalf("semantic release reference = %q / %q", retainedReferenceName, releaseStatus)
	}
	if _, err := adminPool.Exec(ctx, `UPDATE askdata.releases SET status='SUPERSEDED'
		WHERE id=$1 AND tenant_id=$2`, fixture.releaseID, fixture.tenantID); err != nil {
		t.Fatalf("supersede referenced release: %v", err)
	}
	if err := adminPool.QueryRow(ctx, `SELECT status FROM askdata.releases WHERE id=$1`, fixture.releaseID).
		Scan(&releaseStatus); err != nil || releaseStatus != "RETAINED" {
		t.Fatalf("referenced release retention = %q, %v", releaseStatus, err)
	}
	assertPostgresCode(t, func() error {
		_, updateErr := adminPool.Exec(ctx, `UPDATE askdata.releases SET status='RETIRED'
			WHERE id=$1 AND tenant_id=$2`, fixture.releaseID, fixture.tenantID)
		return updateErr
	}(), "23514")
	one := 1
	loadedVersion, err := store.GetVersion(ctx, owner, created.ID, &one)
	if err != nil || len(loadedVersion.DefinitionRaw) != len(prepared.Canonical) || loadedVersion.DefinitionHash != prepared.Hash {
		t.Fatalf("GetVersion() bytes/hash = %d/%s, %v", len(loadedVersion.DefinitionRaw), loadedVersion.DefinitionHash, err)
	}
	if _, err := store.GetVersion(ctx, crossTenant, created.ID, &one); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant version error = %v", err)
	}
	assertStoredReportIndexes(t, ctx, adminPool, created.ID, second.RevisionNo,
		compiler.BuildIndexes(second.Definition), version.ID, prepared.Indexes)
	if _, err := store.RebuildAllIndexes(ctx, observer, created.ID); err == nil {
		t.Fatal("VIEW-only observer rebuilt report indexes")
	}
	initialRebuild, err := store.RebuildAllIndexes(ctx, owner, created.ID)
	if err != nil || initialRebuild.BackfilledVersions != 0 || initialRebuild.VerifiedVersions != 1 {
		t.Fatalf("RebuildAllIndexes(incremental) = %#v, %v", initialRebuild, err)
	}
	clearStoredReportIndexes(t, ctx, adminPool, created.ID, version.ID)
	repaired, err := store.RebuildAllIndexes(ctx, owner, created.ID)
	if err != nil || repaired.DraftRevisionNo != second.RevisionNo ||
		repaired.DraftComponents != len(compiler.BuildIndexes(second.Definition).Components) ||
		repaired.DraftDependencies != len(compiler.BuildIndexes(second.Definition).Dependencies) ||
		repaired.VerifiedVersions != 1 || repaired.BackfilledVersions != 1 {
		t.Fatalf("RebuildAllIndexes(repair) = %#v, %v", repaired, err)
	}
	assertStoredReportIndexes(t, ctx, adminPool, created.ID, second.RevisionNo,
		compiler.BuildIndexes(second.Definition), version.ID, prepared.Indexes)

	ownerContext := database.WithAccessContext(ctx, fixture.ownerID, fixture.domainID)
	assertImmutableSQLState(t, database.WithTenantTx(ownerContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.report_versions SET definition_hash=repeat('f',64) WHERE id=$1`, version.ID)
		return err
	}))
	assertImmutableSQLState(t, database.WithTenantTx(ownerContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM platform.report_versions WHERE id=$1`, version.ID)
		return err
	}))
	assertImmutableSQLState(t, database.WithTenantTx(ownerContext, appPool, fixture.tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM platform.report_revisions WHERE report_id=$1`, created.ID)
		return err
	}))
	_, immutableComponentErr := adminPool.Exec(ctx, `UPDATE platform.report_version_component_indexes
		SET component_type=component_type WHERE report_version_id=$1`, version.ID)
	assertImmutableSQLState(t, immutableComponentErr)
	_, immutableDependencyErr := adminPool.Exec(ctx, `DELETE FROM platform.report_version_dependencies
		WHERE report_version_id=$1`, version.ID)
	assertImmutableSQLState(t, immutableDependencyErr)

	targetVersionNo := version.VersionNo
	mismatchedDefinition := largeDefinition
	mismatchedDefinition.Metadata.Name = "Not the rollback target"
	mismatchedPrepared, err := Prepare(mismatchedDefinition)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateVersion(ctx, owner, created.ID, CreateVersionInput{
		ID: askdata.ID(uuid.NewString()), SourceRevisionNo: version.SourceRevisionNo,
		Definition: mismatchedDefinition, ObjectURI: "s3://report-integration/invalid-rollback.json",
		RollbackOfVersionNo: &targetVersionNo, RollbackReason: "Invalid mismatched rollback",
		Operation: "ROLLBACK", IdempotencyKey: "rollback-integration-invalid",
		RequestHash: askdata.HashBytes([]byte("rollback-integration-invalid")), Prepared: &mismatchedPrepared,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match its target version") {
		t.Fatalf("mismatched rollback error = %v", err)
	}
	_, invalidTargetErr := adminPool.Exec(ctx, `INSERT INTO platform.report_versions(
		id,tenant_id,report_id,version_no,source_revision_no,definition_json,definition_bytes,
		definition_hash,schema_version,object_uri,published_by,rollback_of_version_no,
		rollback_reason,stale_insights_acknowledged,artifact_state
	) SELECT $1,tenant_id,report_id,99,source_revision_no,definition_json,definition_bytes,
		definition_hash,schema_version,'s3://report-integration/invalid-target.json',published_by,98,
		'Invalid missing target',false,'PENDING'
		FROM platform.report_versions WHERE id=$2`, uuid.NewString(), version.ID)
	assertPostgresCode(t, invalidTargetErr, "23503")

	rollbackVersion, err := store.CreateVersion(ctx, owner, created.ID, CreateVersionInput{
		ID: askdata.ID(uuid.NewString()), SourceRevisionNo: version.SourceRevisionNo,
		Definition: largeDefinition, ObjectURI: "s3://report-integration/version-2.json",
		RollbackOfVersionNo: &targetVersionNo, RollbackReason: "Restore the governed baseline",
		Operation: "ROLLBACK", IdempotencyKey: "rollback-integration-001",
		RequestHash: askdata.HashBytes([]byte("rollback-integration-001")), Prepared: &prepared,
	})
	if err != nil {
		t.Fatalf("CreateVersion(rollback) error = %v", err)
	}
	if err := store.CompletePublication(ctx, owner, created.ID, rollbackVersion.ID); err != nil {
		t.Fatalf("complete rollback publication: %v", err)
	}
	if rollbackVersion.VersionNo != 2 || rollbackVersion.RollbackOfVersionNo == nil ||
		*rollbackVersion.RollbackOfVersionNo != 1 || rollbackVersion.RollbackReason != "Restore the governed baseline" ||
		rollbackVersion.DefinitionHash != version.DefinitionHash {
		t.Fatalf("rollback version = %#v", rollbackVersion)
	}
	originalAfterRollback, err := store.GetVersion(ctx, owner, created.ID, &targetVersionNo)
	if err != nil || originalAfterRollback.DefinitionHash != version.DefinitionHash || originalAfterRollback.RollbackOfVersionNo != nil {
		t.Fatalf("rollback mutated target = %#v, %v", originalAfterRollback, err)
	}
	reportAfterRollback, err := store.GetReport(ctx, owner, created.ID)
	if err != nil || reportAfterRollback.CurrentPublishedVersionID != rollbackVersion.ID {
		t.Fatalf("rollback pointer = %#v, %v", reportAfterRollback, err)
	}
}

func visibleDraftIndexRows(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, identity Identity, reportID askdata.ID,
) int {
	t.Helper()
	accessContext := database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	count := 0
	if err := database.WithTenantTx(accessContext, pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM platform.report_draft_component_indexes WHERE report_id=$1)+
			(SELECT count(*) FROM platform.report_draft_dependencies WHERE report_id=$1)`, reportID,
		).Scan(&count)
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertStoredReportIndexes(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, reportID askdata.ID, revisionNo int64,
	draftIndexes compiler.Indexes, versionID askdata.ID, versionIndexes compiler.Indexes,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := verifyDraftIndexes(ctx, tx, reportID, revisionNo, draftIndexes); err != nil {
		t.Fatalf("stored draft indexes: %v", err)
	}
	components, dependencies, err := versionIndexCounts(ctx, tx, versionID)
	if err != nil || components != len(versionIndexes.Components) || dependencies != len(versionIndexes.Dependencies) {
		t.Fatalf("stored version index counts = %d/%d, %v", components, dependencies, err)
	}
	if err := verifyVersionIndexes(ctx, tx, versionID, versionIndexes); err != nil {
		t.Fatalf("stored version indexes: %v", err)
	}
}

func clearStoredReportIndexes(
	t *testing.T, ctx context.Context, pool *pgxpool.Pool, reportID, versionID askdata.ID,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatalf("disable immutable index triggers for repair fixture: %v", err)
	}
	for _, statement := range []string{
		`DELETE FROM platform.report_version_dependencies WHERE report_version_id=$1`,
		`DELETE FROM platform.report_version_component_indexes WHERE report_version_id=$1`,
	} {
		if _, err := tx.Exec(ctx, statement, versionID); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`DELETE FROM platform.report_draft_dependencies WHERE report_id=$1`,
		`DELETE FROM platform.report_draft_component_indexes WHERE report_id=$1`,
	} {
		if _, err := tx.Exec(ctx, statement, reportID); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

type reportStoreFixture struct {
	tenantID, domainID, ownerID, observerID    string
	otherTenantID, otherDomainID, otherOwnerID string
	reportID, releaseID, code                  string
}

func createReportStoreFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) reportStoreFixture {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	fixture := reportStoreFixture{
		tenantID: uuid.NewString(), domainID: uuid.NewString(), ownerID: uuid.NewString(), observerID: uuid.NewString(),
		otherTenantID: uuid.NewString(), otherDomainID: uuid.NewString(), otherOwnerID: uuid.NewString(),
		reportID: uuid.NewString(), releaseID: uuid.NewString(), code: "report_" + suffix,
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, tenant := range []struct{ id, code string }{
		{fixture.tenantID, "rptstore_" + suffix}, {fixture.otherTenantID, "rptother_" + suffix},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES($1,$2,'Report store integration')`, tenant.id, tenant.code); err != nil {
			t.Fatalf("insert tenant: %v", err)
		}
	}
	insertUser := func(tenantID, userID, prefix string) {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,employee_no,email,display_name,password_hash,status
		) VALUES($1,$2,$3,$4,$5,'integration-only-not-a-login-secret','ACTIVE')`, userID, tenantID,
			strings.ToUpper(prefix)+suffix, prefix+"."+suffix+"@example.invalid", prefix+" report integration"); err != nil {
			t.Fatalf("insert %s user: %v", prefix, err)
		}
	}
	insertUser(fixture.tenantID, fixture.ownerID, "owner")
	insertUser(fixture.tenantID, fixture.observerID, "observer")
	insertUser(fixture.otherTenantID, fixture.otherOwnerID, "other")
	for _, domain := range []struct{ id, tenantID, creatorID, code string }{
		{fixture.domainID, fixture.tenantID, fixture.ownerID, "rptstore_" + suffix},
		{fixture.otherDomainID, fixture.otherTenantID, fixture.otherOwnerID, "rptother_" + suffix},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.business_domains(
			id,tenant_id,code,name,is_default,created_by
		) VALUES($1,$2,$3,'Report store integration',true,$4)`, domain.id, domain.tenantID, domain.code, domain.creatorID); err != nil {
			t.Fatalf("insert domain: %v", err)
		}
	}
	for _, membership := range []struct{ tenantID, domainID, userID, assignedBy string }{
		{fixture.tenantID, fixture.domainID, fixture.ownerID, fixture.ownerID},
		{fixture.tenantID, fixture.domainID, fixture.observerID, fixture.ownerID},
		{fixture.otherTenantID, fixture.otherDomainID, fixture.otherOwnerID, fixture.otherOwnerID},
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
			tenant_id,domain_id,user_id,member_role,assigned_by,status
		) VALUES($1,$2,$3,'MEMBER',$4,'ACTIVE')`, membership.tenantID, membership.domainID,
			membership.userID, membership.assignedBy); err != nil {
			t.Fatalf("insert domain membership: %v", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(
		id,tenant_id,code,name,owner_id
	) VALUES($1,$2,$3,'Report store integration',$4)`, fixture.domainID,
		fixture.tenantID, "rptstore_"+suffix, fixture.ownerID); err != nil {
		t.Fatalf("insert ask-data domain: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
		id,tenant_id,domain_id,semantic_version,content_hash,status,created_by,updated_by,
		activated_by,ready_at,activated_at
	) VALUES($1,$2,$3,$4,repeat('a',64),'ACTIVE',$5,$5,$5,now(),now())`,
		fixture.releaseID, fixture.tenantID, fixture.domainID, "report-"+suffix, fixture.ownerID); err != nil {
		t.Fatalf("insert semantic release: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func cleanupReportStoreFixture(t *testing.T, pool *pgxpool.Pool, fixture reportStoreFixture) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin report fixture cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Errorf("disable cleanup triggers: %v", err)
		return
	}
	for _, table := range []string{
		"askdata.release_references", "askdata.releases", "askdata.domains",
		"platform.report_asset_events", "platform.report_shares", "platform.report_publication_idempotency",
		"platform.report_inbound_idempotency", "platform.report_version_dependencies",
		"platform.report_version_component_indexes", "platform.report_draft_dependencies",
		"platform.report_draft_component_indexes", "platform.report_versions",
		"platform.report_revisions", "platform.report_drafts", "platform.reports",
		"platform.object_permissions", "platform.role_permissions", "platform.user_roles",
		"platform.permissions", "platform.roles", "platform.domain_memberships", "platform.business_domains",
		"platform.users", "platform.tenants",
	} {
		var relation *string
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1)::text`, table).Scan(&relation); err != nil {
			t.Errorf("resolve cleanup relation %s: %v", table, err)
			return
		}
		if relation == nil {
			continue
		}
		key := "tenant_id"
		if table == "platform.tenants" {
			key = "id"
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s=ANY($1::uuid[])`, table, key),
			[]string{fixture.tenantID, fixture.otherTenantID}); err != nil {
			t.Errorf("cleanup %s: %v", table, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit report fixture cleanup: %v", err)
	}
}

func grantReportAIEdit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture reportStoreFixture) {
	t.Helper()
	roleID := uuid.NewString()
	permissionID := uuid.NewString()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO platform.roles(id,tenant_id,code,name)
		VALUES($1,$2,'report_ai_editor','Report AI editor')`, roleID, fixture.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.permissions(
		id,tenant_id,code,name,resource_type,action,description
	) VALUES($1,$2,'report.ai_edit','Report AI edit','REPORT','AI_EDIT','integration permission')`,
		permissionID, fixture.tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by)
		VALUES($1,$2,$3,$2)`, fixture.tenantID, fixture.ownerID, roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(
		tenant_id,role_id,permission_id,granted_by
	) VALUES($1,$2,$3,$4)`, fixture.tenantID, roleID, permissionID, fixture.ownerID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func reportStoreDefinition(t *testing.T, reportID, code, name string) reportmodel.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve report store test path")
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
	definition.Metadata.Name = name
	definition.Pages[0].Sections[0].Blocks[0].Zones[0].Layout.Columns = 4
	definition.Pages[0].Sections[0].Blocks[0].Zones[0].Layout.Rows = 3
	definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots[0].Grid.W = 4
	definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots[0].Grid.H = 3
	return definition
}

func reportStoreSemanticBinding(t *testing.T, releaseID, domainID string) *reportmodel.DataBinding {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve semantic binding fixture path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", "ask-data-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := reportmodel.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range definition.Components {
		if component.DataBinding == nil || component.DataBinding.SemanticQueryRef == nil {
			continue
		}
		binding := *component.DataBinding
		reference := *binding.SemanticQueryRef
		reference.SemanticReleaseID = askdata.ID(releaseID)
		reference.SemanticIR.SemanticReleaseID = askdata.ID(releaseID)
		reference.SemanticIR.DomainID = askdata.ID(domainID)
		binding.SemanticQueryRef = &reference
		return &binding
	}
	t.Fatal("ask-data report does not contain a semantic binding")
	return nil
}

func pageRename(definition reportmodel.ReportDefinition, name string) operation.Operation {
	return operation.Operation{
		Op: operation.PageUpdate, TargetID: definition.Pages[0].ID,
		Payload: &operation.PageUpdatePayload{Name: name},
	}
}

func nearLimitDefinition(t *testing.T, base reportmodel.ReportDefinition) (reportmodel.ReportDefinition, PreparedDefinition) {
	t.Helper()
	maxY, maxMobileOrder := 0, 0
	var lastErr error
	lastSize := 0
	maxValidSize := 0
	for _, block := range base.Pages[0].Sections[0].Blocks {
		if bottom := block.Layout.Desktop.Y + block.Layout.Desktop.H; bottom > maxY {
			maxY = bottom
		}
		if block.Layout.Mobile.Order > maxMobileOrder {
			maxMobileOrder = block.Layout.Mobile.Order
		}
	}
	for count := 400; count >= 370; count-- {
		candidate := base
		candidate.Components = append([]reportmodel.Component(nil), base.Components...)
		candidate.Pages = append([]reportmodel.Page(nil), base.Pages...)
		candidate.Pages[0].Sections = append([]reportmodel.Section(nil), base.Pages[0].Sections...)
		candidate.Pages[0].Sections[0].Blocks = append([]reportmodel.Block(nil), base.Pages[0].Sections[0].Blocks...)
		for index := 0; index < count; index++ {
			componentID := askdata.ID(fmt.Sprintf("component_large_%03d", index))
			component := base.Components[0]
			component.ID = componentID
			component.Options.Title = strings.Repeat("t", reportmodel.MaxStringLength)
			component.Options.Subtitle = strings.Repeat("s", reportmodel.MaxStringLength)
			component.Options.ColorPaletteRef = strings.Repeat("p", reportmodel.MaxStringLength)
			candidate.Components = append(candidate.Components, component)
		}
		for blockIndex, start := 0, 0; start < count; blockIndex, start = blockIndex+1, start+16 {
			end := start + 16
			if end > count {
				end = count
			}
			slots := make([]reportmodel.Slot, 0, end-start)
			for index := start; index < end; index++ {
				position := index - start
				slots = append(slots, reportmodel.Slot{
					ID: askdata.ID(fmt.Sprintf("slot_large_%03d", index)),
					Grid: reportmodel.SlotGrid{
						X: (position % 6) * 4, Y: (position / 6) * 3, W: 4, H: 3,
					},
					ComponentID: askdata.ID(fmt.Sprintf("component_large_%03d", index)),
				})
			}
			candidate.Pages[0].Sections[0].Blocks = append(candidate.Pages[0].Sections[0].Blocks, reportmodel.Block{
				ID: askdata.ID(fmt.Sprintf("block_large_%03d", blockIndex)), Type: reportmodel.BlockChart,
				Layout: reportmodel.BlockLayout{
					Desktop: reportmodel.DesktopBlockLayout{X: 0, Y: maxY + blockIndex*9, W: 24, H: 9},
					Mobile: reportmodel.MobileBlockLayout{Order: maxMobileOrder + blockIndex + 1, Visible: true,
						HeightMode: reportmodel.MobileHeightAuto, SlotMode: reportmodel.MobileSlotStack},
				},
				Zones: []reportmodel.Zone{{
					Order: 1,
					ID:    askdata.ID(fmt.Sprintf("zone_large_%03d", blockIndex)), Type: reportmodel.ZoneContent,
					Layout: reportmodel.ZoneLayout{HeightMode: reportmodel.ZoneHeightAuto, MinHeight: 180,
						Columns: 24, Rows: 9, Overflow: reportmodel.OverflowExpand, EmptyPriority: 1},
					Slots: slots,
				}},
			})
		}
		prepared, err := Prepare(candidate)
		lastErr = err
		lastSize = len(prepared.Canonical)
		if err == nil && len(prepared.Canonical) > maxValidSize {
			maxValidSize = len(prepared.Canonical)
		}
		if err == nil && len(prepared.Canonical) >= reportmodel.MaxDefinitionBytes-256*1024 {
			return candidate, prepared
		}
	}
	t.Fatalf("could not construct a valid near-5MB report definition: max valid=%d last size=%d error=%v", maxValidSize, lastSize, lastErr)
	return reportmodel.ReportDefinition{}, PreparedDefinition{}
}

func assertImmutableSQLState(t *testing.T, err error) {
	t.Helper()
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != "55000" {
		t.Fatalf("immutable mutation error = %v", err)
	}
}

func assertPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) || databaseError.Code != code {
		t.Fatalf("PostgreSQL error = %v, want SQLSTATE %s", err, code)
	}
}
