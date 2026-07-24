package materialization

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/testsupport"
)

// This test is opt-in because it creates a tenant fixture. Point both URLs only
// at a disposable database that has migrations through 000071 applied. The
// worker URL must use the warehouse execution role; the admin URL prepares
// fixtures without weakening the permissions exercised by activation.
func TestPostgresStoreLifecycle(t *testing.T) {
	databaseURL := os.Getenv("MATERIALIZATION_TEST_DATABASE_URL")
	adminDatabaseURL := os.Getenv("MATERIALIZATION_TEST_ADMIN_DATABASE_URL")
	if databaseURL == "" || adminDatabaseURL == "" {
		t.Skip("MATERIALIZATION_TEST_DATABASE_URL and MATERIALIZATION_TEST_ADMIN_DATABASE_URL are not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	adminPool, err := pgxpool.New(ctx, adminDatabaseURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer adminPool.Close()

	tenantID := uuid.NewString()
	actorID := uuid.NewString()
	upstreamDatasetID := uuid.NewString()
	upstreamDraftID := uuid.NewString()
	upstreamVersionID := uuid.NewString()
	targetDatasetID := uuid.NewString()
	targetDraftID := uuid.NewString()
	targetVersionID := uuid.NewString()
	sourceFixture := sourceInputFixture{
		databaseSourceID:           uuid.NewString(),
		databasePublishedVersionID: uuid.NewString(),
		databaseDraftVersionID:     uuid.NewString(),
		databaseTableID:            uuid.NewString(),
		fileSourceID:               uuid.NewString(),
		filePublishedVersionID:     uuid.NewString(),
		fileDraftVersionID:         uuid.NewString(),
		fileAssetID:                uuid.NewString(),
		publishedFileVersionID:     uuid.NewString(),
		draftFileVersionID:         uuid.NewString(),
	}
	if _, err := adminPool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,$3)`, tenantID, "mat_"+tenantID[:8], "Materialization integration"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	err = database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash
		) VALUES($1,$2,$3,'Materialization Worker','test-hash')`,
			actorID, tenantID, actorID+"@example.test"); err != nil {
			return err
		}
		if err := createPublishedDatasetFixture(
			ctx, tx, tenantID, actorID, upstreamDatasetID, upstreamDraftID,
			upstreamVersionID, LayerODS, "upstream_"+upstreamDatasetID[:8],
		); err != nil {
			return err
		}
		if err := createPublishedDatasetFixture(
			ctx, tx, tenantID, actorID, targetDatasetID, targetDraftID,
			targetVersionID, LayerDWD, "target_"+targetDatasetID[:8],
		); err != nil {
			return err
		}
		return createSourceInputFixtures(ctx, tx, tenantID, sourceFixture)
	})
	if err != nil {
		t.Fatalf("create dataset fixtures: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, sourceFixture.databaseSourceID,
	); err != nil {
		t.Fatalf("publish database source fixture: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, sourceFixture.fileSourceID,
	); err != nil {
		t.Fatalf("publish file source fixture: %v", err)
	}
	if err := database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		if err := advanceSourceDraft(
			ctx, tx, sourceFixture.databaseSourceID,
			sourceFixture.databaseDraftVersionID,
		); err != nil {
			return err
		}
		return advanceSourceDraft(
			ctx, tx, sourceFixture.fileSourceID,
			sourceFixture.fileDraftVersionID,
		)
	}); err != nil {
		t.Fatalf("advance source fixture drafts: %v", err)
	}
	assertCurrentPublishedVersion(
		t, ctx, adminPool, tenantID, upstreamDatasetID, upstreamVersionID,
	)
	assertCurrentPublishedVersion(
		t, ctx, adminPool, tenantID, targetDatasetID, targetVersionID,
	)

	request := RegisterRequest{
		Plan: BuildPlan{
			Version: PlanVersion, DatasetID: targetDatasetID, DatasetVersionID: targetVersionID,
			Layer: LayerDWD, Mode: RunModeFull,
			Nodes: []PlanNode{
				{ID: "extract", Kind: NodeExtract, Engine: EnginePostgres, InputOrdinals: []int{1}},
				{ID: "project", Kind: NodeProject, Engine: EnginePostgres, DependsOn: []string{"extract"}},
				{ID: "materialize", Kind: NodeMaterialize, Engine: EnginePostgres, DependsOn: []string{"project"}},
			},
			Target: TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
				RefreshMode: string(RunModeFull), StableViewName: true,
			},
		},
		Inputs: []InputSnapshot{{
			Ordinal: 1, Type: InputDatasetVersion, Layer: string(LayerODS),
			DatasetID: upstreamDatasetID, DatasetVersionID: upstreamVersionID,
			SourceVersion: "published:2", SchemaHash: testSchemaHash,
			SnapshotHash: testSnapshotHash,
			SnapshotJSON: json.RawMessage(`{"watermark":"full"}`),
		}},
		MaxAttempts: 3,
	}
	store := NewPostgresStore(pool)
	sourcePlan := BuildPlan{
		Version: PlanVersion, DatasetID: upstreamDatasetID, DatasetVersionID: upstreamVersionID,
		Layer: LayerODS, Mode: RunModeFull,
		Nodes: []PlanNode{
			{ID: "extract", Kind: NodeExtract, Engine: EngineSourceDB, InputOrdinals: []int{1}},
			{ID: "materialize", Kind: NodeMaterialize, Engine: EnginePostgres, DependsOn: []string{"extract"}},
		},
		Target: TargetPlan{
			Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
			RefreshMode: string(RunModeFull), StableViewName: true,
		},
	}
	sourceTableRequest := RegisterRequest{
		Plan: sourcePlan,
		Inputs: []InputSnapshot{{
			Ordinal: 1, Type: InputSourceTable, Layer: "SOURCE",
			DataSourceID:        sourceFixture.databaseSourceID,
			DataSourceVersionID: sourceFixture.databasePublishedVersionID,
			MetadataTableID:     sourceFixture.databaseTableID,
			SourceVersion:       "data-source-version:" + sourceFixture.databasePublishedVersionID,
			SchemaHash:          testSchemaHash, SnapshotHash: testSnapshotHash,
			SnapshotJSON: json.RawMessage(`{"watermark":"source-table"}`),
		}},
		PartitionKey: "source-table-current-published",
		MaxAttempts:  3,
	}
	sourceRun, created, err := store.Register(
		ctx, tenantID, actorID, sourceTableRequest,
	)
	if err != nil || !created {
		t.Fatalf("register exact published source table: created=%v err=%v", created, err)
	}
	staleSourceRequest := sourceTableRequest
	staleSourceRequest.Inputs = append([]InputSnapshot(nil), sourceTableRequest.Inputs...)
	staleSourceRequest.Inputs[0].DataSourceVersionID = sourceFixture.databaseDraftVersionID
	staleSourceRequest.Inputs[0].SourceVersion =
		"data-source-version:" + sourceFixture.databaseDraftVersionID
	staleSourceRequest.PartitionKey = "source-table-unpublished-draft"
	if _, _, err := store.Register(
		ctx, tenantID, actorID, staleSourceRequest,
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unpublished source version error=%v", err)
	}
	cancelQueuedBuildRun(t, ctx, pool, tenantID, sourceRun.ID)

	fileRequest := RegisterRequest{
		Plan: sourcePlan,
		Inputs: []InputSnapshot{{
			Ordinal: 1, Type: InputFileVersion, Layer: "SOURCE",
			DataSourceID:        sourceFixture.fileSourceID,
			DataSourceVersionID: sourceFixture.filePublishedVersionID,
			FileVersionID:       sourceFixture.publishedFileVersionID,
			SourceVersion:       "data-source-version:" + sourceFixture.filePublishedVersionID,
			SchemaHash:          testSchemaHash, SnapshotHash: testSnapshotHash,
			SnapshotJSON: json.RawMessage(`{"watermark":"file-v1"}`),
		}},
		PartitionKey: "file-current-published",
		MaxAttempts:  3,
	}
	fileRun, created, err := store.Register(
		ctx, tenantID, actorID, fileRequest,
	)
	if err != nil || !created {
		t.Fatalf("register exact published file: created=%v err=%v", created, err)
	}
	draftFileRequest := fileRequest
	draftFileRequest.Inputs = append([]InputSnapshot(nil), fileRequest.Inputs...)
	draftFileRequest.Inputs[0].DataSourceVersionID = sourceFixture.fileDraftVersionID
	draftFileRequest.Inputs[0].FileVersionID = sourceFixture.draftFileVersionID
	draftFileRequest.Inputs[0].SourceVersion =
		"data-source-version:" + sourceFixture.fileDraftVersionID
	draftFileRequest.Inputs[0].SnapshotHash = testOtherSnapshotHash
	draftFileRequest.PartitionKey = "file-unpublished-draft"
	if _, _, err := store.Register(
		ctx, tenantID, actorID, draftFileRequest,
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("unpublished file version error=%v", err)
	}
	cancelQueuedBuildRun(t, ctx, pool, tenantID, fileRun.ID)

	assertBuildInputMutationRejected(t, ctx, pool, tenantID, sourceRun.ID, "UPDATE")
	assertBuildInputMutationRejected(t, ctx, pool, tenantID, sourceRun.ID, "DELETE")
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.datasets
			SET layer='DWS' WHERE id=$1`, upstreamDatasetID)
		return err
	})
	assertCheckViolation(t, err, "published dataset layer update")

	run, created, err := store.Register(ctx, tenantID, actorID, request)
	if err != nil || !created {
		t.Fatalf("register run: created=%v err=%v", created, err)
	}
	replayed, created, err := store.Register(ctx, tenantID, actorID, request)
	if err != nil || created || replayed.ID != run.ID {
		t.Fatalf("idempotent replay: run=%+v created=%v err=%v", replayed, created, err)
	}
	claim, err := store.Claim(ctx, tenantID, "integration-worker", time.Minute)
	if err != nil || claim == nil || claim.ID != run.ID {
		t.Fatalf("claim run: claim=%+v err=%v", claim, err)
	}
	// Heartbeats with the same fencing token are intentionally idempotent under
	// concurrency. A different token must never extend or mutate the lease.
	var heartbeatGroup sync.WaitGroup
	heartbeatErrors := make(chan error, 2)
	for index := 0; index < 2; index++ {
		heartbeatGroup.Add(1)
		go func() {
			defer heartbeatGroup.Done()
			_, heartbeatErr := store.Heartbeat(ctx, *claim, time.Minute)
			heartbeatErrors <- heartbeatErr
		}()
	}
	heartbeatGroup.Wait()
	close(heartbeatErrors)
	for heartbeatErr := range heartbeatErrors {
		if heartbeatErr != nil {
			t.Fatalf("concurrent heartbeat: %v", heartbeatErr)
		}
	}
	lostClaim := *claim
	lostClaim.LeaseToken = uuid.NewString()
	if _, err := store.Heartbeat(ctx, lostClaim, time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("lost-token heartbeat error=%v", err)
	}
	for _, nodeID := range []string{"extract", "project", "materialize"} {
		if err := store.StartNode(ctx, *claim, nodeID); err != nil {
			t.Fatalf("start node %s: %v", nodeID, err)
		}
		rows := int64(10)
		bytes := int64(1024)
		if err := store.FinishNode(ctx, *claim, nodeID, NodeResult{
			Status: NodeSucceeded, InputRowCount: &rows,
			OutputRowCount: &rows, OutputSizeBytes: &bytes,
		}); err != nil {
			t.Fatalf("finish node %s: %v", nodeID, err)
		}
	}
	physical, err := GeneratePhysicalIdentifier(tenantID, targetDatasetID, run.ID, LayerDWD)
	if err != nil {
		t.Fatalf("generate identifier: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"CREATE TABLE "+quoteWarehouseIdentifier(physical.Schema)+"."+quoteWarehouseIdentifier(physical.Name)+
			" (id bigint NOT NULL, label text NOT NULL)"); err != nil {
		t.Fatalf("create physical table: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO "+quoteWarehouseIdentifier(physical.Schema)+"."+quoteWarehouseIdentifier(physical.Name)+
			" VALUES(1,'first')"); err != nil {
		t.Fatalf("seed physical table: %v", err)
	}
	materialized, err := store.Activate(ctx, *claim, Activation{
		Physical: physical, RelationKind: "TABLE",
		SchemaHash: testSchemaHash, SnapshotHash: testSnapshotHash,
		RowCount: 10, SizeBytes: 1024,
		Watermark: json.RawMessage(`{"watermark":"full"}`),
		Quality: []QualityResult{{
			RuleCode: "row_count_nonnegative", RuleVersion: "1",
			RuleDefinitionHash: testSchemaHash, Scope: "DATASET",
			Severity: QualityError, Status: QualityPassed,
			Expectation: json.RawMessage(`{"minimum":0}`),
			Observed:    json.RawMessage(`{"rowCount":10}`),
		}},
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if materialized.BuildRunID != run.ID || materialized.Status != "ACTIVE" ||
		materialized.Physical != physical {
		t.Fatalf("unexpected active materialization: %+v", materialized)
	}
	var firstLabel string
	if err := pool.QueryRow(ctx,
		"SELECT label FROM "+quoteWarehouseIdentifier(physical.PublishedSchema)+"."+
			quoteWarehouseIdentifier(physical.PublishedName)+" WHERE id=1").Scan(&firstLabel); err != nil || firstLabel != "first" {
		t.Fatalf("query first stable view: label=%q err=%v", firstLabel, err)
	}

	// Activate an incompatible output schema. A CREATE OR REPLACE VIEW would
	// reject this change; the transactional rename swap must publish it without
	// exposing a missing-view window.
	secondRequest := request
	secondRequest.PartitionKey = "schema-v2"
	secondRun, created, err := store.Register(ctx, tenantID, actorID, secondRequest)
	if err != nil || !created {
		t.Fatalf("register second run: created=%v err=%v", created, err)
	}
	secondClaim, err := store.Claim(ctx, tenantID, "integration-worker", time.Minute)
	if err != nil || secondClaim == nil || secondClaim.ID != secondRun.ID {
		t.Fatalf("claim second run: claim=%+v err=%v", secondClaim, err)
	}
	for _, nodeID := range []string{"extract", "project", "materialize"} {
		if err := store.StartNode(ctx, *secondClaim, nodeID); err != nil {
			t.Fatalf("start second node %s: %v", nodeID, err)
		}
		rows := int64(1)
		bytes := int64(256)
		if err := store.FinishNode(ctx, *secondClaim, nodeID, NodeResult{
			Status: NodeSucceeded, InputRowCount: &rows,
			OutputRowCount: &rows, OutputSizeBytes: &bytes,
		}); err != nil {
			t.Fatalf("finish second node %s: %v", nodeID, err)
		}
	}
	secondPhysical, err := GeneratePhysicalIdentifier(tenantID, targetDatasetID, secondRun.ID, LayerDWD)
	if err != nil {
		t.Fatalf("generate second identifier: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"CREATE TABLE "+quoteWarehouseIdentifier(secondPhysical.Schema)+"."+quoteWarehouseIdentifier(secondPhysical.Name)+
			" (business_key text NOT NULL, renamed_value integer NOT NULL)"); err != nil {
		t.Fatalf("create second physical table: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO "+quoteWarehouseIdentifier(secondPhysical.Schema)+"."+quoteWarehouseIdentifier(secondPhysical.Name)+
			" VALUES('A',42)"); err != nil {
		t.Fatalf("seed second physical table: %v", err)
	}
	secondMaterialized, err := activateWithDirectSQLGateBarrier(
		ctx, pool, store, tenantID, materialized.ID, *secondClaim,
		Activation{
			Physical: secondPhysical, RelationKind: "TABLE",
			SchemaHash: testSchemaHash, SnapshotHash: testOtherSnapshotHash,
			RowCount: 1, SizeBytes: 256,
		},
	)
	if err != nil {
		t.Fatalf("activate incompatible schema: %v", err)
	}
	if secondMaterialized.Status != "ACTIVE" || secondMaterialized.BuildRunID != secondRun.ID {
		t.Fatalf("unexpected second materialization: %+v", secondMaterialized)
	}
	var businessKey string
	var renamedValue int
	if err := pool.QueryRow(ctx,
		"SELECT business_key,renamed_value FROM "+
			quoteWarehouseIdentifier(secondPhysical.PublishedSchema)+"."+
			quoteWarehouseIdentifier(secondPhysical.PublishedName)).
		Scan(&businessKey, &renamedValue); err != nil || businessKey != "A" || renamedValue != 42 {
		t.Fatalf("query evolved stable view: key=%q value=%d err=%v", businessKey, renamedValue, err)
	}
	_, retiredName, err := publicationSwapNames(secondPhysical, secondRun.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var retiredKind string
	if err := pool.QueryRow(ctx, `SELECT class.relkind::text
		FROM pg_class AS class JOIN pg_namespace AS namespace ON namespace.oid=class.relnamespace
		WHERE namespace.nspname=$1 AND class.relname=$2`,
		physical.Schema, retiredName).Scan(&retiredKind); err != nil || retiredKind != "v" {
		t.Fatalf("retired view kind=%q err=%v", retiredKind, err)
	}
	var activeCount int
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.dataset_materializations
			WHERE dataset_id=$1 AND status='ACTIVE'`, targetDatasetID).Scan(&activeCount)
	}); err != nil || activeCount != 1 {
		t.Fatalf("active materializations=%d err=%v", activeCount, err)
	}
	appRole := os.Getenv("POSTGRES_APP_USER")
	if appRole == "" {
		appRole = "report_app"
	}
	var roleExists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, appRole).Scan(&roleExists); err != nil {
		t.Fatalf("lookup app role: %v", err)
	}
	if roleExists {
		var canSelect, canUsePublished, canUseRetiredSchema bool
		if err := pool.QueryRow(ctx, `SELECT has_table_privilege($1,$2,'SELECT')`,
			appRole, secondPhysical.PublishedSchema+"."+secondPhysical.PublishedName).Scan(&canSelect); err != nil || !canSelect {
			t.Fatalf("published default SELECT privilege=%v err=%v", canSelect, err)
		}
		if err := pool.QueryRow(ctx, `SELECT has_schema_privilege($1,$2,'USAGE'),
			has_schema_privilege($1,$3,'USAGE')`,
			appRole, secondPhysical.PublishedSchema, physical.Schema).
			Scan(&canUsePublished, &canUseRetiredSchema); err != nil ||
			!canUsePublished || canUseRetiredSchema {
			t.Fatalf(
				"app schema privileges: published=%v retired=%v err=%v",
				canUsePublished, canUseRetiredSchema, err,
			)
		}
	}
}

var errDirectGovernanceProbeRollback = errors.New(
	"rollback direct materialization governance probe",
)

// activateWithDirectSQLGateBarrier reproduces the pre-000071 lock inversion:
// Activate used to lock its build run before the statement trigger acquired
// the tenant governance gate, while direct SQL acquired the gate before rows.
// The direct transaction deliberately rolls back, so a successful probe has no
// side effects beyond proving both transactions can serialize without 40P01.
func activateWithDirectSQLGateBarrier(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *PostgresStore,
	tenantID, activeMaterializationID string,
	claim Claim,
	activation Activation,
) (Materialization, error) {
	raceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	buildRunLocked := make(chan struct{})
	releaseBuildRun := make(chan struct{})
	blockerResult := make(chan error, 1)
	go func() {
		blockerResult <- database.WithTenantTx(
			raceCtx, pool, tenantID,
			func(tx pgx.Tx) error {
				if _, err := tx.Exec(raceCtx, `SELECT 1
					FROM platform.dataset_build_runs
					WHERE id=$1::uuid FOR UPDATE`, claim.ID); err != nil {
					return err
				}
				close(buildRunLocked)
				select {
				case <-releaseBuildRun:
					return nil
				case <-raceCtx.Done():
					return raceCtx.Err()
				}
			},
		)
	}()
	select {
	case <-buildRunLocked:
	case <-raceCtx.Done():
		return Materialization{}, raceCtx.Err()
	}

	type activationResult struct {
		item Materialization
		err  error
	}
	activated := make(chan activationResult, 1)
	go func() {
		item, err := store.Activate(raceCtx, claim, activation)
		activated <- activationResult{item: item, err: err}
	}()
	if err := waitForMaterializationLockProbe(
		raceCtx, pool, "dataset_build_runs", "",
	); err != nil {
		close(releaseBuildRun)
		<-blockerResult
		return Materialization{}, err
	}

	directResult := make(chan error, 1)
	go func() {
		directResult <- database.WithTenantTx(
			raceCtx, pool, tenantID,
			func(tx pgx.Tx) error {
				if _, err := tx.Exec(raceCtx, `/* materialization-governance-direct-probe */
					UPDATE platform.dataset_materializations
					SET status='RETIRED'
					WHERE id=$1::uuid AND status='ACTIVE'`,
					activeMaterializationID,
				); err != nil {
					return err
				}
				if _, err := tx.Exec(raceCtx, `/* materialization-governance-direct-probe */
					SELECT 1 FROM platform.dataset_build_runs
					WHERE id=$1::uuid FOR UPDATE`, claim.ID); err != nil {
					return err
				}
				return errDirectGovernanceProbeRollback
			},
		)
	}()
	if err := waitForMaterializationLockProbe(
		raceCtx, pool, "", "materialization-governance-direct-probe",
	); err != nil {
		close(releaseBuildRun)
		<-blockerResult
		return Materialization{}, err
	}
	close(releaseBuildRun)

	if err := <-blockerResult; err != nil {
		return Materialization{}, err
	}
	activationOutcome := <-activated
	directErr := <-directResult
	if postgresCode(activationOutcome.err) == "40P01" ||
		postgresCode(directErr) == "40P01" {
		return Materialization{}, errors.Join(
			errors.New("materialization governance gate deadlocked"),
			activationOutcome.err,
			directErr,
		)
	}
	if activationOutcome.err != nil {
		return Materialization{}, activationOutcome.err
	}
	if !errors.Is(directErr, errDirectGovernanceProbeRollback) {
		return Materialization{}, errors.Join(
			errors.New("direct materialization gate probe did not roll back"),
			directErr,
		)
	}
	return activationOutcome.item, nil
}

func waitForMaterializationLockProbe(
	ctx context.Context,
	pool *pgxpool.Pool,
	queryFragment, marker string,
) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM pg_stat_activity
			WHERE pid<>pg_backend_pid()
			  AND wait_event_type='Lock'
			  AND ($1='' OR query LIKE '%'||$1||'%')
			  AND ($2='' OR query LIKE '%'||$2||'%')
		)`, queryFragment, marker).Scan(&waiting)
		if err != nil {
			return err
		}
		if waiting {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func createPublishedDatasetFixture(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, datasetID, draftID, publishedID string,
	layer Layer,
	code string,
) error {
	datasetType := "SINGLE_SOURCE"
	if layer != LayerODS {
		datasetType = "CROSS_SOURCE"
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.datasets(
		id,tenant_id,code,name,dataset_type,status,created_by,updated_by,layer
	) VALUES($1,$2,$3,$4,$5,'PUBLISHED',$6,$6,$7)`,
		datasetID, tenantID, code, code, datasetType, actorID, layer); err != nil {
		return err
	}
	dsl := json.RawMessage(`{"dataset":{"code":"fixture"},"nodes":[]}`)
	logicalPlan := json.RawMessage(`{"nodes":[]}`)
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
		id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
		schema_hash,logical_plan_json,plan_hash,created_by,updated_by,layer
	) VALUES($1,$2,$3,1,'DRAFT','1.0',$4,$5,$6,$5,$7,$7,$8)`,
		draftID, tenantID, datasetID, dsl, testSchemaHash, logicalPlan, actorID, layer); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
		id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
		schema_hash,logical_plan_json,plan_hash,created_by,updated_by,
		published_at,published_by,source_draft_version_id,source_draft_record_version,layer
	) VALUES($1,$2,$3,2,'PUBLISHING','1.0',$4,$5,$6,$5,$7,$7,
		now(),$7,$8,1,$9)`,
		publishedID, tenantID, datasetID, dsl, testSchemaHash, logicalPlan,
		actorID, draftID, layer); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dataset_versions
		SET status='PUBLISHED' WHERE id=$1`, publishedID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE platform.datasets SET
		current_draft_version_id=$1,current_published_version_id=$2
		WHERE id=$3`, draftID, publishedID, datasetID)
	return err
}

type sourceInputFixture struct {
	databaseSourceID           string
	databasePublishedVersionID string
	databaseDraftVersionID     string
	databaseTableID            string
	fileSourceID               string
	filePublishedVersionID     string
	fileDraftVersionID         string
	fileAssetID                string
	publishedFileVersionID     string
	draftFileVersionID         string
}

func createSourceInputFixtures(
	ctx context.Context,
	tx pgx.Tx,
	tenantID string,
	fixture sourceInputFixture,
) error {
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_sources(
			id,tenant_id,code,name,source_type,status,config,secret_ref,
			current_draft_version_id,current_published_version_id,
			validation_status,publication_status
		) VALUES(
			$1,$2,$3,'Materialization MySQL','MYSQL','DRAFT','{}',
			'env://MATERIALIZATION_MYSQL',$4,NULL,'UNTESTED','UNPUBLISHED'
		)`,
		fixture.databaseSourceID, tenantID,
		"mat_mysql_"+fixture.databaseSourceID[:8],
		fixture.databasePublishedVersionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
			id,tenant_id,data_source_id,version_no,source_type,config,secret_ref,config_hash
		) VALUES
			($1,$2,$3,1,'MYSQL','{}','env://MATERIALIZATION_MYSQL',$4),
			($5,$2,$3,2,'MYSQL','{"database":"draft"}',
			 'env://MATERIALIZATION_MYSQL',$6)`,
		fixture.databasePublishedVersionID, tenantID, fixture.databaseSourceID,
		testSchemaHash, fixture.databaseDraftVersionID, testOtherSnapshotHash,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_tables(
			id,tenant_id,data_source_id,schema_name,table_name,table_type,
			structure_hash,last_sync_at
		) VALUES($1,$2,$3,'public','orders','TABLE',$4,now())`,
		fixture.databaseTableID, tenantID, fixture.databaseSourceID,
		testSchemaHash,
	); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `INSERT INTO platform.file_assets(
			id,tenant_id,filename,mime_type,current_version
		) VALUES($1,$2,'sales.xlsx',
			'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',2)`,
		fixture.fileAssetID, tenantID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.file_asset_versions(
			id,tenant_id,file_asset_id,version,filename,mime_type,size_bytes,
			sha256,storage_bucket,storage_key
		) VALUES
			($1,$2,$3,1,'sales.xlsx',
			 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
			 1,$4,'uploads',$5),
			($6,$2,$3,2,'sales.xlsx',
			 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
			 1,$7,'uploads',$8)`,
		fixture.publishedFileVersionID, tenantID, fixture.fileAssetID,
		testSnapshotHash, "materialization/"+fixture.publishedFileVersionID,
		fixture.draftFileVersionID, testOtherSnapshotHash,
		"materialization/"+fixture.draftFileVersionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_sources(
			id,tenant_id,code,name,source_type,status,config,file_asset_id,
			current_draft_version_id,current_published_version_id,
			validation_status,publication_status
		) VALUES(
			$1,$2,$3,'Materialization Excel','EXCEL','DRAFT','{}',$4,
			$5,NULL,'UNTESTED','UNPUBLISHED'
		)`,
		fixture.fileSourceID, tenantID,
		"mat_excel_"+fixture.fileSourceID[:8], fixture.fileAssetID,
		fixture.filePublishedVersionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
			id,tenant_id,data_source_id,version_no,source_type,config,
			file_asset_id,file_version_id,config_hash
		) VALUES
			($1,$2,$3,1,'EXCEL','{}',$4,$5,$6),
			($7,$2,$3,2,'EXCEL','{}',$4,$8,$9)`,
		fixture.filePublishedVersionID, tenantID, fixture.fileSourceID,
		fixture.fileAssetID, fixture.publishedFileVersionID, testSchemaHash,
		fixture.fileDraftVersionID, fixture.draftFileVersionID,
		testOtherSnapshotHash,
	); err != nil {
		return err
	}
	return nil
}

func advanceSourceDraft(
	ctx context.Context,
	tx pgx.Tx,
	sourceID, draftVersionID string,
) error {
	command, err := tx.Exec(ctx, `UPDATE platform.data_sources SET
			current_draft_version_id=$1,validation_status='UNTESTED',
			last_tested_at=NULL,last_tested_version_id=NULL,
			last_tested_config_hash=NULL,test_expires_at=NULL
		WHERE id=$2 AND current_published_version_id IS NOT NULL`,
		draftVersionID, sourceID,
	)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func assertCurrentPublishedVersion(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, datasetID, expectedVersionID string,
) {
	t.Helper()
	var actualVersionID string
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT current_published_version_id::text
			FROM platform.datasets WHERE id=$1`, datasetID).Scan(&actualVersionID)
	})
	if err != nil || actualVersionID != expectedVersionID {
		t.Fatalf(
			"dataset %s current published version=%q want=%q err=%v",
			datasetID, actualVersionID, expectedVersionID, err,
		)
	}
}

func assertBuildInputMutationRejected(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, runID, operation string,
) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if operation == "UPDATE" {
			_, err := tx.Exec(ctx, `UPDATE platform.build_run_inputs
				SET source_version=source_version||'-forged'
				WHERE build_run_id=$1`, runID)
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM platform.build_run_inputs
			WHERE build_run_id=$1`, runID)
		return err
	})
	assertCheckViolation(t, err, "build input "+operation)
}

func cancelQueuedBuildRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, runID string,
) {
	t.Helper()
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.dataset_build_runs
			SET status='CANCELLED',completed_at=now()
			WHERE id=$1 AND status='QUEUED'`, runID)
		return err
	})
	if err != nil {
		t.Fatalf("cancel build run %s: %v", runID, err)
	}
}

func assertCheckViolation(t *testing.T, err error, operation string) {
	t.Helper()
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "23514" {
		t.Fatalf("%s error=%v", operation, err)
	}
}

func postgresCode(err error) string {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return pgError.Code
	}
	return ""
}
