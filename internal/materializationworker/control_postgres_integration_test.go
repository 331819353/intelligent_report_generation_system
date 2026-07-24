package materializationworker

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/testsupport"
)

// This opt-in test covers the public control-plane persistence contract against
// migrations through v62. It does not execute ODS extraction: the first ODS
// build is cancelled, while a separate trusted fixture is manually activated
// only to prove DWD registration freezes an ACTIVE materialization.
func TestPostgresControlPlaneDerivesCurrentBuilds(t *testing.T) {
	databaseURL := os.Getenv("MATERIALIZATION_TEST_DATABASE_URL")
	adminDatabaseURL := os.Getenv("MATERIALIZATION_TEST_ADMIN_DATABASE_URL")
	if databaseURL == "" || adminDatabaseURL == "" {
		t.Skip("MATERIALIZATION_TEST_DATABASE_URL and MATERIALIZATION_TEST_ADMIN_DATABASE_URL are not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open control pool: %v", err)
	}
	defer pool.Close()
	adminPool, err := pgxpool.New(ctx, adminDatabaseURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer adminPool.Close()

	tenantID, actorID := uuid.NewString(), uuid.NewString()
	sourceID, sourceVersionID, sourceTableID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	excelSourceID, excelSourceVersionID := uuid.NewString(), uuid.NewString()
	excelTableID, excelFileAssetID, excelFileVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	odsDatasetID, odsDraftID, odsVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	excelDatasetID, excelDraftID, excelDatasetVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	dwdDatasetID, dwdDraftID, dwdVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	odsPrepared := prepareIntegrationDocument(t, integrationODSDocument(
		odsDatasetID, sourceID, sourceTableID,
	))
	excelDocument := integrationODSDocument(
		excelDatasetID, excelSourceID, excelTableID,
	)
	excelDocument.Nodes[0].FileVersionID = excelFileVersionID
	excelPrepared := prepareIntegrationDocument(t, excelDocument)
	dwdPrepared := prepareIntegrationDocument(t, integrationDWDDocument(
		dwdDatasetID, odsVersionID,
	))

	if _, err := adminPool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,$3)`,
		tenantID, "mat_control_"+tenantID[:8], "Materialization control integration"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	err = database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash
		) VALUES($1,$2,$3,'Materialization Controller','test-hash')`,
			actorID, tenantID, actorID+"@example.test"); err != nil {
			return err
		}
		if err := createSourceForWorker(
			ctx, tx, tenantID, actorID, sourceID, sourceVersionID, sourceTableID,
		); err != nil {
			return err
		}
		if err := createExcelSourceForControl(
			ctx, tx, tenantID, actorID,
			excelSourceID, excelSourceVersionID, excelTableID,
			excelFileAssetID, excelFileVersionID,
		); err != nil {
			return err
		}
		fixtures := []struct {
			datasetID, draftID, versionID string
			layer                         dataset.Layer
			prepared                      dataset.Prepared
		}{
			{odsDatasetID, odsDraftID, odsVersionID, dataset.LayerODS, odsPrepared},
			{
				excelDatasetID, excelDraftID, excelDatasetVersionID,
				dataset.LayerODS, excelPrepared,
			},
			{dwdDatasetID, dwdDraftID, dwdVersionID, dataset.LayerDWD, dwdPrepared},
		}
		for _, fixture := range fixtures {
			if err := createPublishedDatasetForWorker(
				ctx, tx, tenantID, actorID,
				fixture.datasetID, fixture.draftID, fixture.versionID,
				fixture.layer, fixture.prepared,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("create control fixtures: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, sourceID,
	); err != nil {
		t.Fatalf("publish database source fixture: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, excelSourceID,
	); err != nil {
		t.Fatalf("publish Excel source fixture: %v", err)
	}

	store := materialization.NewPostgresStore(pool)
	odsRun, created, err := store.RegisterCurrent(
		ctx, tenantID, actorID, odsDatasetID,
		materialization.RegisterCurrentRequest{
			Mode: materialization.RunModeFull, MaxAttempts: 3,
		},
	)
	if err != nil || !created {
		t.Fatalf("register derived ODS: created=%v err=%v", created, err)
	}
	odsDetail, err := store.GetBuild(ctx, tenantID, odsDatasetID, odsRun.ID)
	if err != nil {
		t.Fatalf("get derived ODS: %v", err)
	}
	if len(odsDetail.Inputs) != 1 ||
		odsDetail.Inputs[0].Type != materialization.InputSourceTable ||
		odsDetail.Inputs[0].DataSourceID != sourceID ||
		odsDetail.Inputs[0].DataSourceVersionID != sourceVersionID ||
		odsDetail.Inputs[0].MetadataTableID != sourceTableID ||
		odsDetail.Inputs[0].SchemaHash != integrationSourceSchemaHash ||
		len(odsDetail.Nodes) != 3 {
		t.Fatalf("derived ODS detail=%+v", odsDetail)
	}
	if _, err := store.CancelQueued(
		ctx, tenantID, actorID, odsDatasetID, odsRun.ID,
	); err != nil {
		t.Fatalf("cancel derived ODS: %v", err)
	}

	excelRun, created, err := store.RegisterCurrent(
		ctx, tenantID, actorID, excelDatasetID,
		materialization.RegisterCurrentRequest{
			Mode: materialization.RunModeFull, MaxAttempts: 3,
		},
	)
	if err != nil || !created {
		t.Fatalf("register derived Excel ODS: created=%v err=%v", created, err)
	}
	excelDetail, err := store.GetBuild(
		ctx, tenantID, excelDatasetID, excelRun.ID,
	)
	if err != nil {
		t.Fatalf("get derived Excel ODS: %v", err)
	}
	if len(excelDetail.Inputs) != 1 ||
		excelDetail.Inputs[0].Type != materialization.InputFileVersion ||
		excelDetail.Inputs[0].DataSourceID != excelSourceID ||
		excelDetail.Inputs[0].DataSourceVersionID != excelSourceVersionID ||
		excelDetail.Inputs[0].FileVersionID != excelFileVersionID ||
		excelDetail.Inputs[0].SchemaHash != integrationSourceSchemaHash ||
		excelDetail.Inputs[0].SnapshotHash != integrationInputSnapshotHash {
		t.Fatalf("derived Excel ODS detail=%+v", excelDetail)
	}
	if _, err := store.CancelQueued(
		ctx, tenantID, actorID, excelDatasetID, excelRun.ID,
	); err != nil {
		t.Fatalf("cancel derived Excel ODS: %v", err)
	}

	// Create an ACTIVE ODS fixture without claiming the cancelled public task.
	odsFixtureRequest := integrationODSBuildRequest(
		odsDatasetID, odsVersionID, sourceID, sourceVersionID, sourceTableID,
	)
	odsFixtureRequest.PartitionKey = "control-active-fixture"
	odsFixtureRun, created, err := store.Register(
		ctx, tenantID, actorID, odsFixtureRequest,
	)
	if err != nil || !created {
		t.Fatalf("register ODS fixture: created=%v err=%v", created, err)
	}
	odsClaim, err := store.Claim(ctx, tenantID, "control-ods-fixture", time.Minute)
	if err != nil || odsClaim == nil || odsClaim.ID != odsFixtureRun.ID {
		t.Fatalf("claim ODS fixture: claim=%+v err=%v", odsClaim, err)
	}
	for _, nodeID := range []string{"extract", "stage", "materialize"} {
		if err := store.StartNode(ctx, *odsClaim, nodeID); err != nil {
			t.Fatalf("start ODS fixture node %s: %v", nodeID, err)
		}
		rows, size := int64(3), int64(8192)
		if err := store.FinishNode(ctx, *odsClaim, nodeID, materialization.NodeResult{
			Status:        materialization.NodeSucceeded,
			InputRowCount: &rows, OutputRowCount: &rows, OutputSizeBytes: &size,
		}); err != nil {
			t.Fatalf("finish ODS fixture node %s: %v", nodeID, err)
		}
	}
	physical, err := materialization.GeneratePhysicalIdentifier(
		tenantID, odsDatasetID, odsFixtureRun.ID, materialization.LayerODS,
	)
	if err != nil {
		t.Fatal(err)
	}
	qualified := quoteIntegrationIdentifier(physical.Schema) + "." +
		quoteIntegrationIdentifier(physical.Name)
	if _, err := pool.Exec(ctx, "CREATE TABLE "+qualified+
		" (order_id bigint NOT NULL, region text NOT NULL, amount numeric NOT NULL)"); err != nil {
		t.Fatalf("create ODS fixture relation: %v", err)
	}
	odsActive, err := store.Activate(ctx, *odsClaim, materialization.Activation{
		Physical: physical, RelationKind: "TABLE",
		SchemaHash: odsPrepared.DSLHash, SnapshotHash: integrationODSOutputSnapshotHash,
		RowCount: 3, SizeBytes: 8192,
	})
	if err != nil {
		t.Fatalf("activate ODS fixture: %v", err)
	}

	dwdRun, created, err := store.RegisterCurrent(
		ctx, tenantID, actorID, dwdDatasetID,
		materialization.RegisterCurrentRequest{
			Mode: materialization.RunModeFull, MaxAttempts: 4,
		},
	)
	if err != nil || !created {
		t.Fatalf("register derived DWD: created=%v err=%v", created, err)
	}
	dwdDetail, err := store.GetBuild(ctx, tenantID, dwdDatasetID, dwdRun.ID)
	if err != nil {
		t.Fatalf("get derived DWD: %v", err)
	}
	if len(dwdDetail.Inputs) != 1 ||
		dwdDetail.Inputs[0].Type != materialization.InputMaterialization ||
		dwdDetail.Inputs[0].Layer != string(materialization.LayerODS) ||
		dwdDetail.Inputs[0].MaterializationID != odsActive.ID ||
		dwdDetail.Inputs[0].SchemaHash != odsPrepared.DSLHash ||
		dwdDetail.Inputs[0].SnapshotHash != integrationODSOutputSnapshotHash ||
		dwdDetail.Inputs[0].RowCount == nil ||
		*dwdDetail.Inputs[0].RowCount != 3 {
		t.Fatalf("derived DWD detail=%+v", dwdDetail)
	}
	for _, node := range dwdDetail.Nodes {
		if node.Engine != string(materialization.EnginePostgres) {
			t.Fatalf("derived DWD node is not PostgreSQL: %+v", node)
		}
	}
	items, total, err := store.ListBuilds(ctx, tenantID, dwdDatasetID, 10, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].ID != dwdRun.ID {
		t.Fatalf("list DWD builds total=%d items=%+v err=%v", total, items, err)
	}
	if _, err := store.CancelQueued(
		ctx, tenantID, actorID, dwdDatasetID, dwdRun.ID,
	); err != nil {
		t.Fatalf("cancel derived DWD: %v", err)
	}
	if _, err := store.CancelQueued(
		ctx, tenantID, actorID, dwdDatasetID, dwdRun.ID,
	); !errors.Is(err, materialization.ErrInvalidTransition) {
		t.Fatalf("second cancellation error=%v", err)
	}

	var registerAudits, cancelAudits int
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			count(*) FILTER(WHERE action='REGISTER_MATERIALIZATION_BUILD')::int,
			count(*) FILTER(WHERE action='CANCEL_MATERIALIZATION_BUILD')::int
			FROM platform.audit_logs
			WHERE resource_type='DATASET_BUILD_RUN'
			  AND resource_id IN ($1,$2,$3)`,
			odsRun.ID, excelRun.ID, dwdRun.ID).
			Scan(&registerAudits, &cancelAudits)
	})
	if err != nil || registerAudits != 3 || cancelAudits != 3 {
		t.Fatalf(
			"control audits register=%d cancel=%d err=%v",
			registerAudits, cancelAudits, err,
		)
	}
}

func createExcelSourceForControl(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID string,
	sourceID, sourceVersionID, tableID string,
	fileAssetID, fileVersionID string,
) error {
	if _, err := tx.Exec(ctx, `INSERT INTO platform.file_assets(
		id,tenant_id,filename,mime_type,current_version
	) VALUES(
		$1,$2,'control.xlsx',
		'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',1
	)`, fileAssetID, tenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.file_asset_versions(
		id,tenant_id,file_asset_id,version,filename,mime_type,size_bytes,
		sha256,storage_bucket,storage_key
	) VALUES(
		$1,$2,$3,1,'control.xlsx',
		'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
		1024,$4,'uploads',$5
	)`, fileVersionID, tenantID, fileAssetID,
		integrationInputSnapshotHash, "control/"+fileVersionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_sources(
		id,tenant_id,code,name,source_type,status,config,file_asset_id,
		last_synced_at,owner_user_id,created_by,updated_by,
		validation_status,publication_status,current_draft_version_id
	) VALUES(
		$1,$2,$3,'Control Excel','EXCEL','DRAFT','{}',$4,
		now(),$5,$5,$5,'UNTESTED','UNPUBLISHED',$6
	)`, sourceID, tenantID, "excel_"+sourceID[:8], fileAssetID,
		actorID, sourceVersionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
		id,tenant_id,data_source_id,version_no,source_type,config,
		file_asset_id,file_version_id,config_hash,created_by
	) VALUES($1,$2,$3,1,'EXCEL','{}',$4,$5,$6,$7)`,
		sourceVersionID, tenantID, sourceID, fileAssetID, fileVersionID,
		integrationSourceConfigHash, actorID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.metadata_tables(
		id,tenant_id,data_source_id,catalog_name,schema_name,table_name,
		table_type,structure_hash,last_sync_at,asset_status,management_status
	) VALUES(
		$1,$2,$3,'','workbook','orders','SHEET',$4,now(),'ACTIVE','ENABLED'
	)`, tableID, tenantID, sourceID, integrationSourceSchemaHash)
	return err
}
