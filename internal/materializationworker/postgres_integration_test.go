package materializationworker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/testsupport"
	"intelligent-report-generation-system/internal/warehouse"
)

// This opt-in test proves both PostgreSQL paths: DWD is built from a frozen
// ACTIVE ODS view, then DWS is built from that frozen ACTIVE DWD view. Both are
// compiled into CTAS, quality checked and atomically activated under tenant RLS.
// Use only a disposable database migrated through the materialization schema.
func TestPostgresWorkerBuildsDWDAndDWSFromFrozenLayers(t *testing.T) {
	databaseURL := os.Getenv("MATERIALIZATION_TEST_DATABASE_URL")
	adminDatabaseURL := os.Getenv("MATERIALIZATION_TEST_ADMIN_DATABASE_URL")
	if databaseURL == "" || adminDatabaseURL == "" {
		t.Skip("MATERIALIZATION_TEST_DATABASE_URL and MATERIALIZATION_TEST_ADMIN_DATABASE_URL are not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open worker pool: %v", err)
	}
	defer pool.Close()
	adminPool, err := pgxpool.New(ctx, adminDatabaseURL)
	if err != nil {
		t.Fatalf("open admin pool: %v", err)
	}
	defer adminPool.Close()

	tenantID := uuid.NewString()
	actorID := uuid.NewString()
	sourceID, sourceVersionID, sourceTableID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	odsDatasetID, odsDraftID, odsVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	dwdDatasetID, dwdDraftID, dwdVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	dwsDatasetID, dwsDraftID, dwsVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()

	odsPrepared := prepareIntegrationDocument(t, integrationODSDocument(
		odsDatasetID, sourceID, sourceTableID,
	))
	dwdPrepared := prepareIntegrationDocument(t, integrationDWDDocument(dwdDatasetID, odsVersionID))
	dwsPrepared := prepareIntegrationDocument(t, integrationDWSDocument(dwsDatasetID, dwdVersionID))

	if _, err := adminPool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,$3)`,
		tenantID, "mat_worker_"+tenantID[:8], "Materialization worker integration"); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	err = database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash
		) VALUES($1,$2,$3,'Materialization Worker','test-hash')`,
			actorID, tenantID, actorID+"@example.test"); err != nil {
			return err
		}
		if err := createSourceForWorker(
			ctx, tx, tenantID, actorID, sourceID, sourceVersionID, sourceTableID,
		); err != nil {
			return err
		}
		fixtures := []struct {
			datasetID, draftID, versionID string
			layer                         dataset.Layer
			prepared                      dataset.Prepared
		}{
			{odsDatasetID, odsDraftID, odsVersionID, dataset.LayerODS, odsPrepared},
			{dwdDatasetID, dwdDraftID, dwdVersionID, dataset.LayerDWD, dwdPrepared},
			{dwsDatasetID, dwsDraftID, dwsVersionID, dataset.LayerDWS, dwsPrepared},
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
		t.Fatalf("create dataset fixtures: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, sourceID,
	); err != nil {
		t.Fatalf("publish source fixture: %v", err)
	}

	store := materialization.NewPostgresStore(pool)
	odsRequest := integrationODSBuildRequest(
		odsDatasetID, odsVersionID, sourceID, sourceVersionID, sourceTableID,
	)
	odsRun, created, err := store.Register(ctx, tenantID, actorID, odsRequest)
	if err != nil || !created {
		t.Fatalf("register ODS fixture run: created=%v err=%v", created, err)
	}
	worker := NewWorker(store, NewCompositeResolver(
		NewODSResolver(
			pool,
			warehouse.NewStager(pool, integrationSourceStream{
				sourceID: sourceID, sourceVersionID: sourceVersionID,
			}),
			nil,
			nil,
		),
		NewPostgresResolver(pool),
	), &integrationBuilder{
		test: t, delegate: warehouse.NewExecutor(pool),
		expectedInputSchema: "warehouse_staging",
	})
	processed, err := worker.ProcessNext(
		ctx, tenantID, "integration-ods-worker", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process ODS run: processed=%v err=%v", processed, err)
	}
	odsMaterialization := loadActiveIntegrationMaterialization(
		t, ctx, pool, tenantID, odsRun.ID,
	)
	if odsMaterialization.RowCount != 3 ||
		odsMaterialization.SchemaHash != odsPrepared.DSLHash ||
		odsMaterialization.Physical.Schema != "warehouse_ods" {
		t.Fatalf("ODS materialization=%+v", odsMaterialization)
	}

	dwdRequest := integrationBuildRequest(
		dwdDatasetID, dwdVersionID,
		materialization.LayerDWD, materialization.LayerODS,
		odsDatasetID, odsVersionID,
		odsPrepared.DSLHash, odsMaterialization.SnapshotHash,
	)
	dwdRun, created, err := store.Register(ctx, tenantID, actorID, dwdRequest)
	if err != nil || !created {
		t.Fatalf("register DWD run: created=%v err=%v", created, err)
	}
	worker = NewWorker(store, NewPostgresResolver(pool), &integrationBuilder{
		test: t, delegate: warehouse.NewExecutor(pool),
		expectedInputSchema: "warehouse_ods",
	})
	processed, err = worker.ProcessNext(
		ctx, tenantID, "integration-materialization-worker", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process DWD run: processed=%v err=%v", processed, err)
	}
	var dwdMaterialization materialization.Materialization
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,dataset_id::text,
				dataset_version_id::text,build_run_id::text,layer,status,
				physical_schema,physical_name,published_schema,published_name,
				schema_hash,snapshot_hash,row_count,size_bytes,activated_at
			FROM platform.dataset_materializations
			WHERE build_run_id=$1 AND status='ACTIVE'`,
			dwdRun.ID).Scan(
			&dwdMaterialization.ID, &dwdMaterialization.TenantID,
			&dwdMaterialization.DatasetID, &dwdMaterialization.DatasetVersionID,
			&dwdMaterialization.BuildRunID, &dwdMaterialization.Layer,
			&dwdMaterialization.Status, &dwdMaterialization.Physical.Schema,
			&dwdMaterialization.Physical.Name,
			&dwdMaterialization.Physical.PublishedSchema,
			&dwdMaterialization.Physical.PublishedName,
			&dwdMaterialization.SchemaHash, &dwdMaterialization.SnapshotHash,
			&dwdMaterialization.RowCount, &dwdMaterialization.SizeBytes,
			&dwdMaterialization.ActivatedAt,
		)
	}); err != nil {
		t.Fatalf("load active DWD materialization: %v", err)
	}
	if dwdMaterialization.RowCount != 3 ||
		dwdMaterialization.SchemaHash != dwdPrepared.DSLHash {
		t.Fatalf("DWD materialization=%+v", dwdMaterialization)
	}

	dwsRequest := integrationBuildRequest(
		dwsDatasetID, dwsVersionID,
		materialization.LayerDWS, materialization.LayerDWD,
		dwdDatasetID, dwdVersionID,
		dwdPrepared.DSLHash, dwdMaterialization.SnapshotHash,
	)
	dwsRequest.Plan.Nodes[1].Kind = materialization.NodeAggregate
	dwsRun, created, err := store.Register(ctx, tenantID, actorID, dwsRequest)
	if err != nil || !created {
		t.Fatalf("register DWS run: created=%v err=%v", created, err)
	}
	worker = NewWorker(store, NewPostgresResolver(pool), &integrationBuilder{
		test: t, delegate: warehouse.NewExecutor(pool),
		expectedInputSchema: "warehouse_dwd",
	})
	processed, err = worker.ProcessNext(
		ctx, tenantID, "integration-materialization-worker", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process DWS run: processed=%v err=%v", processed, err)
	}

	dwsPhysical, err := materialization.GeneratePhysicalIdentifier(
		tenantID, dwsDatasetID, dwsRun.ID, materialization.LayerDWS,
	)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `SELECT region,revenue::text
		FROM `+quoteIntegrationIdentifier(dwsPhysical.PublishedSchema)+"."+
		quoteIntegrationIdentifier(dwsPhysical.PublishedName)+`
		ORDER BY region`)
	if err != nil {
		t.Fatalf("query DWS stable view: %v", err)
	}
	defer rows.Close()
	got := [][2]string{}
	for rows.Next() {
		var region, revenue string
		if err := rows.Scan(&region, &revenue); err != nil {
			t.Fatal(err)
		}
		got = append(got, [2]string{region, revenue})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"north", "30"}, {"south", "5"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("DWS rows=%v want=%v", got, want)
	}
	var status string
	var rowCount, sizeBytes int64
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT run.status,materialization.row_count,
				materialization.size_bytes
			FROM platform.dataset_build_runs AS run
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.build_run_id=run.id
			WHERE run.id=$1 AND materialization.status='ACTIVE'`,
			dwsRun.ID).Scan(&status, &rowCount, &sizeBytes)
	}); err != nil {
		t.Fatalf("load DWS outcome: %v", err)
	}
	if status != string(materialization.RunSucceeded) || rowCount != 2 || sizeBytes <= 0 {
		t.Fatalf("status=%s rows=%d size=%d", status, rowCount, sizeBytes)
	}

	// Excel follows the same server-derived registration and PostgreSQL
	// activation path, but reads an exact immutable object version instead of a
	// remote database stream.
	excelSourceID := uuid.NewString()
	excelSourceVersionID := uuid.NewString()
	excelAssetID := uuid.NewString()
	excelFileVersionID := uuid.NewString()
	excelTableID := uuid.NewString()
	excelDatasetID := uuid.NewString()
	excelDraftID := uuid.NewString()
	excelDatasetVersionID := uuid.NewString()
	excelPrepared := prepareIntegrationDocument(t, integrationExcelODSDocument(
		excelDatasetID, excelSourceID, excelTableID, excelFileVersionID,
	))
	if err := database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		if err := createExcelSourceForWorker(
			ctx, tx, tenantID, actorID,
			excelSourceID, excelSourceVersionID,
			excelAssetID, excelFileVersionID, excelTableID,
		); err != nil {
			return err
		}
		return createPublishedDatasetForWorker(
			ctx, tx, tenantID, actorID,
			excelDatasetID, excelDraftID, excelDatasetVersionID,
			dataset.LayerODS, excelPrepared,
		)
	}); err != nil {
		t.Fatalf("create Excel ODS fixtures: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, excelSourceID,
	); err != nil {
		t.Fatalf("publish Excel source fixture: %v", err)
	}
	excelRun, created, err := store.RegisterCurrent(
		ctx, tenantID, actorID, excelDatasetID,
		materialization.RegisterCurrentRequest{
			Mode: materialization.RunModeFull, MaxAttempts: 3,
		},
	)
	if err != nil || !created {
		t.Fatalf("register Excel ODS run: created=%v err=%v", created, err)
	}
	excelWorker := NewWorker(store, NewCompositeResolver(
		NewODSResolver(
			pool,
			nil,
			nil,
			warehouse.NewFileStager(pool, integrationFileReader{
				version: datasource.FileVersion{FileAsset: datasource.FileAsset{
					ID: excelAssetID, TenantID: tenantID,
					Filename: "orders.xlsx", MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
					CurrentVersion: 1, Version: 1, VersionID: excelFileVersionID,
					SizeBytes: 256, SHA256: integrationExcelFileHash,
				}},
				tables: []datasource.FileTableData{{
					Name:    "Orders",
					Columns: []string{"order_id", "region", "amount"},
					Types: map[string]string{
						"order_id": "NUMBER", "region": "TEXT", "amount": "DECIMAL",
					},
					Rows: [][]string{
						{"10", "east", "1,234.50"},
						{"11", "west", "12.5%"},
					},
				}},
			}),
		),
		NewPostgresResolver(pool),
	), &integrationBuilder{
		test: t, delegate: warehouse.NewExecutor(pool),
		expectedInputSchema: "warehouse_staging",
	})
	processed, err = excelWorker.ProcessNext(
		ctx, tenantID, "integration-excel-ods-worker", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process Excel ODS run: processed=%v err=%v", processed, err)
	}
	excelMaterialization := loadActiveIntegrationMaterialization(
		t, ctx, pool, tenantID, excelRun.ID,
	)
	if excelMaterialization.RowCount != 2 ||
		excelMaterialization.SchemaHash != excelPrepared.DSLHash ||
		excelMaterialization.Physical.Schema != "warehouse_ods" {
		t.Fatalf("Excel ODS materialization=%+v", excelMaterialization)
	}
	excelRows, err := pool.Query(ctx, `SELECT order_id::text,region,amount::text
		FROM `+quoteIntegrationIdentifier(excelMaterialization.Physical.PublishedSchema)+"."+
		quoteIntegrationIdentifier(excelMaterialization.Physical.PublishedName)+`
		ORDER BY order_id`)
	if err != nil {
		t.Fatalf("query Excel ODS stable view: %v", err)
	}
	defer excelRows.Close()
	excelGot := [][3]string{}
	for excelRows.Next() {
		var orderID, region, amount string
		if err := excelRows.Scan(&orderID, &region, &amount); err != nil {
			t.Fatal(err)
		}
		excelGot = append(excelGot, [3]string{orderID, region, amount})
	}
	if err := excelRows.Err(); err != nil {
		t.Fatal(err)
	}
	excelWant := [][3]string{{"10", "east", "1234.50"}, {"11", "west", "0.125"}}
	if len(excelGot) != len(excelWant) ||
		excelGot[0] != excelWant[0] ||
		excelGot[1] != excelWant[1] {
		t.Fatalf("Excel ODS rows=%v want=%v", excelGot, excelWant)
	}

	// A run registered against source v1 must fail if v2 becomes current before
	// the worker resolves it. The immutable v1 config still exists, but it is no
	// longer allowed to masquerade as the current published source contract.
	republishDatasetID := uuid.NewString()
	republishDraftID := uuid.NewString()
	republishVersionID := uuid.NewString()
	republishPrepared := prepareIntegrationDocument(t, integrationODSDocument(
		republishDatasetID, sourceID, sourceTableID,
	))
	if err := database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		return createPublishedDatasetForWorker(
			ctx, tx, tenantID, actorID,
			republishDatasetID, republishDraftID, republishVersionID,
			dataset.LayerODS, republishPrepared,
		)
	}); err != nil {
		t.Fatalf("create republish guard dataset: %v", err)
	}
	republishRun, created, err := store.Register(
		ctx, tenantID, actorID,
		integrationODSBuildRequest(
			republishDatasetID, republishVersionID,
			sourceID, sourceVersionID, sourceTableID,
		),
	)
	if err != nil || !created {
		t.Fatalf("register republish guard: created=%v err=%v", created, err)
	}
	sourceVersionV2 := uuid.NewString()
	if err := database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		return createNextSourceVersionForWorker(
			ctx, tx, tenantID, actorID, sourceID, sourceVersionV2,
		)
	}); err != nil {
		t.Fatalf("create source v2: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, sourceID,
	); err != nil {
		t.Fatalf("publish source v2: %v", err)
	}
	odsWorker := NewWorker(store, NewCompositeResolver(
		NewODSResolver(
			pool,
			warehouse.NewStager(pool, integrationSourceStream{
				sourceID: sourceID, sourceVersionID: sourceVersionV2,
			}),
			nil,
			nil,
		),
		NewPostgresResolver(pool),
	), warehouse.NewExecutor(pool))
	processed, err = odsWorker.ProcessNext(
		ctx, tenantID, "integration-ods-guard-worker", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process republished source guard: processed=%v err=%v", processed, err)
	}
	assertIntegrationBuildFailed(
		t, ctx, pool, tenantID, republishRun.ID,
		CodeODSSourceContractInvalid,
	)

	// Metadata can drift after registration without changing the immutable
	// connection version. The resolver must compare the current table hash to
	// the frozen input before it opens the source stream.
	schemaDatasetID := uuid.NewString()
	schemaDraftID := uuid.NewString()
	schemaVersionID := uuid.NewString()
	schemaPrepared := prepareIntegrationDocument(t, integrationODSDocument(
		schemaDatasetID, sourceID, sourceTableID,
	))
	if err := database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		return createPublishedDatasetForWorker(
			ctx, tx, tenantID, actorID,
			schemaDatasetID, schemaDraftID, schemaVersionID,
			dataset.LayerODS, schemaPrepared,
		)
	}); err != nil {
		t.Fatalf("create schema guard dataset: %v", err)
	}
	schemaRun, created, err := store.Register(
		ctx, tenantID, actorID,
		integrationODSBuildRequest(
			schemaDatasetID, schemaVersionID,
			sourceID, sourceVersionV2, sourceTableID,
		),
	)
	if err != nil || !created {
		t.Fatalf("register schema guard: created=%v err=%v", created, err)
	}
	if err := database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE platform.metadata_tables
			SET structure_hash=$1,metadata_version=metadata_version+1
			WHERE id=$2`, integrationDriftedSchemaHash, sourceTableID)
		return err
	}); err != nil {
		t.Fatalf("drift metadata structure: %v", err)
	}
	processed, err = odsWorker.ProcessNext(
		ctx, tenantID, "integration-ods-guard-worker", time.Minute,
	)
	if err != nil || !processed {
		t.Fatalf("process schema drift guard: processed=%v err=%v", processed, err)
	}
	assertIntegrationBuildFailed(
		t, ctx, pool, tenantID, schemaRun.ID,
		CodeODSSourceContractInvalid,
	)
}

const (
	integrationInputSnapshotHash     = "1111111111111111111111111111111111111111111111111111111111111111"
	integrationODSOutputSnapshotHash = "2222222222222222222222222222222222222222222222222222222222222222"
	integrationSourceSchemaHash      = "3333333333333333333333333333333333333333333333333333333333333333"
	integrationSourceConfigHash      = "4444444444444444444444444444444444444444444444444444444444444444"
	integrationSourceConfigHashV2    = "8888888888888888888888888888888888888888888888888888888888888888"
	integrationDriftedSchemaHash     = "9999999999999999999999999999999999999999999999999999999999999999"
	integrationExcelFileHash         = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	integrationExcelConfigHash       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	integrationExcelSchemaHash       = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
)

func integrationODSBuildRequest(
	datasetID, versionID, sourceID, sourceVersionID, metadataTableID string,
) materialization.RegisterRequest {
	return materialization.RegisterRequest{
		Plan: materialization.BuildPlan{
			Version:   materialization.PlanVersion,
			DatasetID: datasetID, DatasetVersionID: versionID,
			Layer: materialization.LayerODS, Mode: materialization.RunModeFull,
			Nodes: []materialization.PlanNode{
				{
					ID: "extract", Kind: materialization.NodeExtract,
					Engine: materialization.EngineSourceDB, InputOrdinals: []int{1},
				},
				{
					ID: "stage", Kind: materialization.NodeStage,
					Engine: materialization.EnginePostgres, DependsOn: []string{"extract"},
				},
				{
					ID: "materialize", Kind: materialization.NodeMaterialize,
					Engine: materialization.EnginePostgres, DependsOn: []string{"stage"},
				},
			},
			Target: materialization.TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
				RefreshMode: string(materialization.RunModeFull), StableViewName: true,
			},
		},
		Inputs: []materialization.InputSnapshot{{
			Ordinal: 1, Type: materialization.InputSourceTable, Layer: "SOURCE",
			DataSourceID: sourceID, DataSourceVersionID: sourceVersionID,
			MetadataTableID: metadataTableID,
			SourceVersion:   "data-source-version:" + sourceVersionID,
			SchemaHash:      integrationSourceSchemaHash,
			SnapshotHash:    integrationInputSnapshotHash, RowCount: int64Pointer(3),
			SnapshotJSON: json.RawMessage(`{"capture":"integration"}`),
		}},
		MaxAttempts: 3,
	}
}

func integrationBuildRequest(
	datasetID, versionID string,
	layer, inputLayer materialization.Layer,
	inputDatasetID, inputVersionID, schemaHash, snapshotHash string,
) materialization.RegisterRequest {
	return materialization.RegisterRequest{
		Plan: materialization.BuildPlan{
			Version:   materialization.PlanVersion,
			DatasetID: datasetID, DatasetVersionID: versionID,
			Layer: layer, Mode: materialization.RunModeFull,
			Nodes: []materialization.PlanNode{
				{
					ID: "extract", Kind: materialization.NodeExtract,
					Engine: materialization.EnginePostgres, InputOrdinals: []int{1},
				},
				{
					ID: "project", Kind: materialization.NodeProject,
					Engine: materialization.EnginePostgres, DependsOn: []string{"extract"},
				},
				{
					ID: "materialize", Kind: materialization.NodeMaterialize,
					Engine: materialization.EnginePostgres, DependsOn: []string{"project"},
				},
			},
			Target: materialization.TargetPlan{
				Storage: "POSTGRES", AtomicPublish: true, RelationKind: "TABLE",
				RefreshMode: string(materialization.RunModeFull), StableViewName: true,
			},
		},
		Inputs: []materialization.InputSnapshot{{
			Ordinal: 1, Type: materialization.InputDatasetVersion,
			Layer:     string(inputLayer),
			DatasetID: inputDatasetID, DatasetVersionID: inputVersionID,
			SourceVersion: "published:2", SchemaHash: schemaHash,
			SnapshotHash: snapshotHash, RowCount: int64Pointer(3),
			SnapshotJSON: json.RawMessage(`{"capture":"integration"}`),
		}},
		MaxAttempts: 3,
	}
}

func prepareIntegrationDocument(t *testing.T, document dataset.Document) dataset.Prepared {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare integration DSL: %v", err)
	}
	return prepared
}

func integrationODSDocument(datasetID, sourceID, sourceTableID string) dataset.Document {
	return integrationDocument(
		datasetID, dataset.LayerODS,
		dataset.Node{
			ID: "source", Type: "TABLE",
			DataSourceID: sourceID,
			TableID:      sourceTableID,
			Alias:        "src", Projection: []string{"order_id", "region", "amount"},
		},
		[]dataset.Field{
			integrationField("field_order_id", "order_id", "INTEGER", "IDENTIFIER", "source"),
			integrationField("field_region", "region", "STRING", "DIMENSION", "source"),
			integrationField("field_amount", "amount", "DECIMAL", "MEASURE", "source"),
		},
		dataset.OutputGrain{
			Description: "one row per order", KeyFields: []string{"order_id"},
		},
	)
}

func integrationDWDDocument(datasetID, odsVersionID string) dataset.Document {
	return integrationDocument(
		datasetID, dataset.LayerDWD,
		dataset.Node{
			ID: "orders", Type: "DATASET", DatasetVersionID: odsVersionID,
			Alias: "orders", Projection: []string{"order_id", "region", "amount"},
		},
		[]dataset.Field{
			integrationField("field_order_id", "order_id", "INTEGER", "IDENTIFIER", "orders"),
			integrationField("field_region", "region", "STRING", "DIMENSION", "orders"),
			integrationField("field_amount", "amount", "DECIMAL", "MEASURE", "orders"),
		},
		dataset.OutputGrain{
			Description: "one row per order", KeyFields: []string{"order_id"},
		},
	)
}

func integrationExcelODSDocument(
	datasetID, sourceID, sourceTableID, fileVersionID string,
) dataset.Document {
	document := integrationODSDocument(datasetID, sourceID, sourceTableID)
	document.Nodes[0].FileVersionID = fileVersionID
	return document
}

func integrationDWSDocument(datasetID, dwdVersionID string) dataset.Document {
	document := integrationDocument(
		datasetID, dataset.LayerDWS,
		dataset.Node{
			ID: "orders", Type: "DATASET", DatasetVersionID: dwdVersionID,
			Alias: "orders", Projection: []string{"region", "amount"},
		},
		[]dataset.Field{
			integrationField("field_region", "region", "STRING", "DIMENSION", "orders"),
			{
				ID: "field_revenue", Code: "revenue", Name: "Revenue",
				Role: "MEASURE", CanonicalType: "DECIMAL", Nullable: false,
				Expression: dataset.Expression{
					Type: "AGGREGATE", Function: "SUM",
					Argument: &dataset.Expression{
						Type: "FIELD_REF", NodeID: "orders", Field: "amount",
					},
				},
			},
		},
		dataset.OutputGrain{
			Description: "one row per region", KeyFields: []string{"region"},
		},
	)
	document.GroupBy = []string{"field_region"}
	return document
}

func integrationDocument(
	datasetID string,
	layer dataset.Layer,
	node dataset.Node,
	fields []dataset.Field,
	grain dataset.OutputGrain,
) dataset.Document {
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "integration_" + datasetID[:8], Name: "Integration " + string(layer),
			Type: "SINGLE_SOURCE", Layer: layer,
		},
		Nodes: []dataset.Node{node}, Fields: fields,
		Joins: []dataset.Join{}, Filters: []dataset.Filter{},
		GroupBy: []string{}, Having: []dataset.Filter{},
		Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain: grain,
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 30_000,
			PreviewLimit: 100, ResultLimit: 10_000,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
}

func integrationField(id, code, canonicalType, role, nodeID string) dataset.Field {
	return dataset.Field{
		ID: id, Code: code, Name: code, Role: role,
		CanonicalType: canonicalType, Nullable: false,
		Expression: dataset.Expression{
			Type: "FIELD_REF", NodeID: nodeID, Field: code,
		},
	}
}

func createSourceForWorker(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, sourceID, sourceVersionID, tableID string,
) error {
	config := json.RawMessage(`{"host":"fixture","port":3306,"database":"fixture","username":"reader"}`)
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_sources(
			id,tenant_id,code,name,source_type,status,config,secret_ref,
			owner_user_id,created_by,updated_by,
			validation_status,publication_status,current_draft_version_id,
			current_published_version_id,last_synced_at
		) VALUES(
			$1,$2,$3,'Integration source','MYSQL','DRAFT',$4,'encrypted:fixture',
			$5,$5,$5,'UNTESTED','UNPUBLISHED',$6,NULL,now()
		)`,
		sourceID, tenantID, "source_"+sourceID[:8], config, actorID,
		sourceVersionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
		id,tenant_id,data_source_id,version_no,source_type,config,secret_ref,
		config_hash,created_by
	) VALUES($1,$2,$3,1,'MYSQL',$4,'encrypted:fixture',$5,$6)`,
		sourceVersionID, tenantID, sourceID, config,
		integrationSourceConfigHash, actorID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_tables(
		id,tenant_id,data_source_id,catalog_name,schema_name,table_name,
		table_type,structure_hash,last_sync_at,asset_status,management_status
	) VALUES($1,$2,$3,'fixture','public','orders','TABLE',$4,now(),'ACTIVE','ENABLED')`,
		tableID, tenantID, sourceID, integrationSourceSchemaHash); err != nil {
		return err
	}
	columns := []struct {
		name, nativeType, canonicalType, structureHash string
		ordinal                                        int
	}{
		{
			name: "order_id", nativeType: "BIGINT", canonicalType: "INTEGER",
			structureHash: "5555555555555555555555555555555555555555555555555555555555555555",
			ordinal:       1,
		},
		{
			name: "region", nativeType: "VARCHAR", canonicalType: "STRING",
			structureHash: "6666666666666666666666666666666666666666666666666666666666666666",
			ordinal:       2,
		},
		{
			name: "amount", nativeType: "DECIMAL", canonicalType: "DECIMAL",
			structureHash: "7777777777777777777777777777777777777777777777777777777777777777",
			ordinal:       3,
		},
	}
	for _, column := range columns {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
			tenant_id,table_id,column_name,ordinal_position,native_type,
			canonical_type,nullable,structure_hash,last_sync_at,asset_status
		) VALUES($1,$2,$3,$4,$5,$6,false,$7,now(),'ACTIVE')`,
			tenantID, tableID, column.name, column.ordinal,
			column.nativeType, column.canonicalType, column.structureHash,
		); err != nil {
			return err
		}
	}
	return nil
}

func createPublishedDatasetForWorker(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, datasetID, draftID, publishedID string,
	layer dataset.Layer,
	prepared dataset.Prepared,
) error {
	if _, err := tx.Exec(ctx, `INSERT INTO platform.datasets(
		id,tenant_id,code,name,dataset_type,status,created_by,updated_by,layer
	) VALUES($1,$2,$3,$4,'SINGLE_SOURCE','PUBLISHED',$5,$5,$6)`,
		datasetID, tenantID, prepared.Document.Dataset.Code,
		prepared.Document.Dataset.Name, actorID, layer); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
		id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
		schema_hash,logical_plan_json,plan_hash,created_by,updated_by,layer
	) VALUES($1,$2,$3,1,'DRAFT','1.0',$4,$5,$6,$7,$8,$8,$9)`,
		draftID, tenantID, datasetID, prepared.DSLJSON, prepared.DSLHash,
		prepared.LogicalPlanJSON, prepared.PlanHash, actorID, layer); err != nil {
		return err
	}
	if err := insertDatasetFieldsForWorker(
		ctx, tx, tenantID, draftID, prepared.Document.Fields,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
		id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
		schema_hash,logical_plan_json,plan_hash,created_by,updated_by,
		published_at,published_by,source_draft_version_id,source_draft_record_version,layer
	) VALUES($1,$2,$3,2,'PUBLISHING','1.0',$4,$5,$6,$7,$8,$8,
		now(),$8,$9,1,$10)`,
		publishedID, tenantID, datasetID, prepared.DSLJSON, prepared.DSLHash,
		prepared.LogicalPlanJSON, prepared.PlanHash, actorID, draftID, layer); err != nil {
		return err
	}
	if err := insertDatasetFieldsForWorker(
		ctx, tx, tenantID, publishedID, prepared.Document.Fields,
	); err != nil {
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

func createNextSourceVersionForWorker(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, sourceID, sourceVersionID string,
) error {
	config := json.RawMessage(`{"host":"fixture-v2","port":3306,"database":"fixture","username":"reader"}`)
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
		id,tenant_id,data_source_id,version_no,source_type,config,secret_ref,
		config_hash,created_by
	) VALUES($1,$2,$3,2,'MYSQL',$4,'encrypted:fixture',$5,$6)`,
		sourceVersionID, tenantID, sourceID, config,
		integrationSourceConfigHashV2, actorID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.data_sources SET
		current_draft_version_id=$1,validation_status='UNTESTED',
		last_tested_version_id=NULL,last_tested_config_hash=NULL,
		last_tested_at=NULL,test_expires_at=NULL
		WHERE id=$2`, sourceVersionID, sourceID); err != nil {
		return err
	}
	return nil
}

func createExcelSourceForWorker(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, sourceID, sourceVersionID,
	assetID, fileVersionID, tableID string,
) error {
	if _, err := tx.Exec(ctx, `INSERT INTO platform.file_assets(
		id,tenant_id,filename,mime_type,current_version
	) VALUES(
		$1,$2,'orders.xlsx',
		'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',1
	)`, assetID, tenantID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.file_asset_versions(
		id,tenant_id,file_asset_id,version,filename,mime_type,size_bytes,
		sha256,storage_bucket,storage_key,parse_config,workbook_summary
	) VALUES(
		$1,$2,$3,1,'orders.xlsx',
		'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
		256,$4,'integration',$5,'{}','{}'
	)`, fileVersionID, tenantID, assetID, integrationExcelFileHash,
		assetID+"/orders.xlsx"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_sources(
			id,tenant_id,code,name,source_type,status,config,file_asset_id,
			owner_user_id,created_by,updated_by,
			validation_status,publication_status,current_draft_version_id,
			current_published_version_id,last_synced_at
		) VALUES(
			$1,$2,$3,'Integration Excel','EXCEL','DRAFT','{}',$4,
			$5,$5,$5,'UNTESTED','UNPUBLISHED',$6,NULL,now()
		)`, sourceID, tenantID, "excel_"+sourceID[:8],
		assetID, actorID, sourceVersionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.data_source_versions(
		id,tenant_id,data_source_id,version_no,source_type,config,
		file_asset_id,file_version_id,config_hash,created_by
	) VALUES($1,$2,$3,1,'EXCEL','{}',$4,$5,$6,$7)`,
		sourceVersionID, tenantID, sourceID, assetID, fileVersionID,
		integrationExcelConfigHash, actorID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_tables(
		id,tenant_id,data_source_id,catalog_name,schema_name,table_name,
		table_type,structure_hash,last_sync_at,asset_status,management_status
	) VALUES(
		$1,$2,$3,'','','Orders','SHEET',$4,now(),'ACTIVE','ENABLED'
	)`, tableID, tenantID, sourceID, integrationExcelSchemaHash); err != nil {
		return err
	}
	columns := []struct {
		name, canonicalType, structureHash string
		ordinal                            int
	}{
		{
			name: "order_id", canonicalType: "NUMBER",
			structureHash: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			ordinal:       1,
		},
		{
			name: "region", canonicalType: "TEXT",
			structureHash: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
			ordinal:       2,
		},
		{
			name: "amount", canonicalType: "DECIMAL",
			structureHash: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			ordinal:       3,
		},
	}
	for _, column := range columns {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
			tenant_id,table_id,column_name,ordinal_position,native_type,
			canonical_type,nullable,structure_hash,last_sync_at,asset_status
		) VALUES($1,$2,$3,$4,$5,$5,false,$6,now(),'ACTIVE')`,
			tenantID, tableID, column.name, column.ordinal,
			column.canonicalType, column.structureHash); err != nil {
			return err
		}
	}
	return nil
}

func insertDatasetFieldsForWorker(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, versionID string,
	fields []dataset.Field,
) error {
	for index, field := range fields {
		expression, err := json.Marshal(field.Expression)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_fields(
			tenant_id,dataset_version_id,field_id,field_code,field_name,
			description,expression_json,canonical_type,semantic_type,
			field_role,aggregation,nullable,visible,ordinal_position
		) VALUES($1,$2,$3,$4,$5,'',$6,$7,$8,$9,$10,$11,true,$12)`,
			tenantID, versionID, field.ID, field.Code, field.Name,
			expression, field.CanonicalType, field.SemanticType,
			field.Role, field.Aggregation, field.Nullable, index+1); err != nil {
			return err
		}
	}
	return nil
}

func quoteIntegrationIdentifier(value string) string {
	return `"` + value + `"`
}

func int64Pointer(value int64) *int64 {
	return &value
}

type integrationBuilder struct {
	test                *testing.T
	delegate            *warehouse.Executor
	expectedInputSchema string
}

type integrationSourceStream struct {
	sourceID, sourceVersionID string
}

type integrationFileReader struct {
	version datasource.FileVersion
	tables  []datasource.FileTableData
}

func (reader integrationFileReader) ReadVersionTablesWithExpansionLimit(
	ctx context.Context,
	tenantID, versionID string,
	maxFileBytes, maxExpandedBytes int64,
) (datasource.FileVersion, []datasource.FileTableData, error) {
	if err := ctx.Err(); err != nil {
		return datasource.FileVersion{}, nil, err
	}
	if tenantID != reader.version.TenantID ||
		versionID != reader.version.VersionID ||
		maxFileBytes < reader.version.SizeBytes ||
		maxExpandedBytes < 1 {
		return datasource.FileVersion{}, nil, errors.New("invalid immutable file read contract")
	}
	return reader.version, reader.tables, nil
}

func (stream integrationSourceStream) StreamQuery(
	ctx context.Context,
	source datasource.Source,
	_ string,
	_ string,
	_ []any,
	_ int,
	maxRows int,
	consume datasource.StreamConsumer,
) (datasource.StreamSummary, error) {
	if err := ctx.Err(); err != nil {
		return datasource.StreamSummary{}, err
	}
	if source.ID != stream.sourceID ||
		source.ConfigVersionID != stream.sourceVersionID ||
		source.PublishedVersionID != stream.sourceVersionID ||
		maxRows != warehouse.MaxODSRows {
		return datasource.StreamSummary{}, errors.New("worker did not use the exact published source version")
	}
	rows := [][]any{
		{json.Number("1"), "north", json.Number("10")},
		{json.Number("2"), "north", json.Number("20")},
		{json.Number("3"), "south", json.Number("5")},
	}
	if err := consume(datasource.StreamBatch{
		Columns: []string{"order_id", "region", "amount"},
		Rows:    rows,
	}); err != nil {
		return datasource.StreamSummary{}, err
	}
	return datasource.StreamSummary{RowCount: len(rows), DurationMS: 1}, nil
}

func loadActiveIntegrationMaterialization(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, buildRunID string,
) materialization.Materialization {
	t.Helper()
	var item materialization.Materialization
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,dataset_id::text,
				dataset_version_id::text,build_run_id::text,layer,status,
				physical_schema,physical_name,published_schema,published_name,
				schema_hash,snapshot_hash,row_count,size_bytes,activated_at
			FROM platform.dataset_materializations
			WHERE build_run_id=$1 AND status='ACTIVE'`,
			buildRunID).Scan(
			&item.ID, &item.TenantID, &item.DatasetID,
			&item.DatasetVersionID, &item.BuildRunID, &item.Layer,
			&item.Status, &item.Physical.Schema, &item.Physical.Name,
			&item.Physical.PublishedSchema, &item.Physical.PublishedName,
			&item.SchemaHash, &item.SnapshotHash,
			&item.RowCount, &item.SizeBytes, &item.ActivatedAt,
		)
	}); err != nil {
		t.Fatalf("load active materialization: %v", err)
	}
	return item
}

func assertIntegrationBuildFailed(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, buildRunID, wantCode string,
) {
	t.Helper()
	var status, code string
	var materializations int
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT status,error_code
			FROM platform.dataset_build_runs WHERE id=$1`,
			buildRunID).Scan(&status, &code); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.dataset_materializations WHERE build_run_id=$1`,
			buildRunID).Scan(&materializations)
	}); err != nil {
		t.Fatalf("load failed build: %v", err)
	}
	if status != string(materialization.RunFailed) ||
		code != wantCode ||
		materializations != 0 {
		t.Fatalf(
			"status=%s code=%s materializations=%d",
			status, code, materializations,
		)
	}
}

func (builder *integrationBuilder) Build(
	ctx context.Context,
	input warehouse.BuildInput,
) (warehouse.BuildResult, error) {
	builder.test.Helper()
	for nodeID, table := range input.Tables {
		if table.Schema != builder.expectedInputSchema {
			builder.test.Fatalf(
				"node %s reads schema %q, want immutable %q",
				nodeID, table.Schema, builder.expectedInputSchema,
			)
		}
	}
	return builder.delegate.Build(ctx, input)
}
