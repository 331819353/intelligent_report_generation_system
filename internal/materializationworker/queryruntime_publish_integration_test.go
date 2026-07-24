//go:build integration

package materializationworker

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

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/testsupport"
)

// This is the vertical acceptance test for the strict layered contract:
//
//	published ODS + ACTIVE materialization
//	  -> DWD publication validation through warehouse_published
//	  -> immutable DWD publication + ACTIVE materialization
//	  -> DWS publication validation through warehouse_published
//	  -> immutable DWS publication + ACTIVE materialization
//	  -> DWS metric preview and publication through warehouse_published
//
// No client SQL or physical warehouse identifier crosses the service boundary.
func TestStrictLayeredPublicationAndDWSMetricUseActiveMaterializations(t *testing.T) {
	appURL := os.Getenv("DATABASE_URL")
	workerURL := os.Getenv("MATERIALIZATION_TEST_DATABASE_URL")
	adminURL := os.Getenv("MATERIALIZATION_TEST_ADMIN_DATABASE_URL")
	if appURL == "" || workerURL == "" || adminURL == "" {
		t.Skip("DATABASE_URL and materialization integration URLs are not set")
	}
	ctx := context.Background()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()
	workerPool, err := pgxpool.New(ctx, workerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()

	tenantID, actorID := uuid.NewString(), uuid.NewString()
	sourceID, sourceVersionID, sourceTableID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	odsDatasetID, odsDraftID, odsVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	dwdDatasetID := uuid.NewString()
	odsPrepared := prepareIntegrationDocument(
		t, integrationODSDocument(odsDatasetID, sourceID, sourceTableID),
	)
	dwdDocument := integrationDWDDocument(dwdDatasetID, odsVersionID)
	dwdRaw, err := json.Marshal(dwdDocument)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := adminPool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name)
		VALUES($1,$2,'Query runtime publication integration')`,
		tenantID, "query_publish_"+tenantID[:8]); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	err = database.WithTenantTx(ctx, adminPool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash
		) VALUES($1,$2,$3,'Query runtime publisher','test-hash')`,
			actorID, tenantID, actorID+"@example.test"); err != nil {
			return err
		}
		if err := createSourceForWorker(
			ctx, tx, tenantID, actorID, sourceID, sourceVersionID, sourceTableID,
		); err != nil {
			return err
		}
		return createPublishedDatasetForWorker(
			ctx, tx, tenantID, actorID,
			odsDatasetID, odsDraftID, odsVersionID,
			dataset.LayerODS, odsPrepared,
		)
	})
	if err != nil {
		t.Fatalf("create ODS fixture: %v", err)
	}
	if _, err := testsupport.AttestAndPublishDataSource(
		ctx, adminPool, tenantID, actorID, sourceID,
	); err != nil {
		t.Fatalf("publish source fixture: %v", err)
	}

	materializationStore := materialization.NewPostgresStore(workerPool)
	odsRequest := integrationODSBuildRequest(
		odsDatasetID, odsVersionID, sourceID, sourceVersionID, sourceTableID,
	)
	odsRequest.PartitionKey = "queryruntime-" + tenantID[:8]
	odsRun, created, err := materializationStore.Register(
		ctx, tenantID, actorID, odsRequest,
	)
	if err != nil || !created {
		t.Fatalf("register ODS fixture: created=%v err=%v", created, err)
	}
	claim, err := materializationStore.Claim(
		ctx, tenantID, "queryruntime-publish-worker", time.Minute,
	)
	if err != nil || claim == nil || claim.ID != odsRun.ID {
		t.Fatalf("claim ODS fixture: claim=%+v err=%v", claim, err)
	}
	for _, nodeID := range []string{"extract", "stage", "materialize"} {
		if err := materializationStore.StartNode(ctx, *claim, nodeID); err != nil {
			t.Fatalf("start %s: %v", nodeID, err)
		}
		rows, size := int64(2), int64(8192)
		if err := materializationStore.FinishNode(
			ctx, *claim, nodeID, materialization.NodeResult{
				Status:        materialization.NodeSucceeded,
				InputRowCount: &rows, OutputRowCount: &rows,
				OutputSizeBytes: &size,
			},
		); err != nil {
			t.Fatalf("finish %s: %v", nodeID, err)
		}
	}
	physical, err := materialization.GeneratePhysicalIdentifier(
		tenantID, odsDatasetID, odsRun.ID, materialization.LayerODS,
	)
	if err != nil {
		t.Fatal(err)
	}
	qualified := quoteIntegrationIdentifier(physical.Schema) + "." +
		quoteIntegrationIdentifier(physical.Name)
	if _, err := workerPool.Exec(ctx, "CREATE TABLE "+qualified+
		" (order_id bigint NOT NULL, region text NOT NULL, amount numeric NOT NULL)"); err != nil {
		t.Fatalf("create ODS relation: %v", err)
	}
	if _, err := workerPool.Exec(ctx, "INSERT INTO "+qualified+
		" (order_id,region,amount) VALUES (1,'east',10),(2,'west',20)"); err != nil {
		t.Fatalf("seed ODS relation: %v", err)
	}
	activeODS, err := materializationStore.Activate(
		ctx, *claim, materialization.Activation{
			Physical: physical, RelationKind: "TABLE",
			SchemaHash:   odsPrepared.DSLHash,
			SnapshotHash: strings.Repeat("c", 64),
			RowCount:     2, SizeBytes: 8192,
		},
	)
	if err != nil {
		t.Fatalf("activate ODS: %v", err)
	}

	datasetStore := dataset.NewPostgresStore(appPool)
	queryStore := queryruntime.NewPostgresStore(appPool)
	queryService := queryruntime.NewService(
		datasetStore,
		datasource.NewPostgresRepository(appPool),
		policy.NewPostgresStore(appPool),
		queryStore,
		nil,
	)
	queryService.SetWarehouseExecutor(
		queryruntime.NewPostgresWarehouseExecutor(appPool),
	)
	datasetService := dataset.NewService(datasetStore)
	datasetService.SetPublicationValidator(queryService)
	createdDWD, err := datasetService.Create(
		ctx, tenantID, actorID, dataset.CreateInput{
			Code: dwdDocument.Dataset.Code, Name: dwdDocument.Dataset.Name,
			Description: dwdDocument.Dataset.Description,
			Type:        dwdDocument.Dataset.Type, Layer: dataset.LayerDWD,
			DSL: dwdRaw,
		},
	)
	if err != nil {
		t.Fatalf("create strict DWD: %v", err)
	}
	dwdDatasetID = createdDWD.ID
	err = database.WithTenantTx(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
			tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
		) VALUES($1,'USER',$2,'DATASET',$3,'PUBLISH',$2)`,
			tenantID, actorID, dwdDatasetID)
		return err
	})
	if err != nil {
		t.Fatalf("grant DWD publication permission: %v", err)
	}
	publishedDWD, err := datasetService.Publish(
		ctx, tenantID, actorID, dwdDatasetID,
		"queryruntime-publish-"+tenantID[:8],
		dataset.PublishInput{
			DraftVersionID:             createdDWD.DraftVersionID,
			ExpectedVersion:            createdDWD.Version,
			ExpectedDraftRecordVersion: createdDWD.DraftRecordVersion,
			ExpectedDSLHash:            createdDWD.DSLHash,
			ValidationParameters:       map[string]any{},
		},
	)
	if err != nil {
		var validation *dataset.PublicationValidationError
		if errors.As(err, &validation) {
			t.Fatalf(
				"publish strict DWD through PostgreSQL preview: %+v",
				validation.Issues,
			)
		}
		t.Fatalf("publish strict DWD through PostgreSQL preview: %v", err)
	}
	if publishedDWD.Layer != dataset.LayerDWD ||
		publishedDWD.Status != "PUBLISHED" {
		t.Fatalf("published DWD=%+v", publishedDWD)
	}

	var engine, boundMaterializationID string
	err = database.WithTenantTx(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT run.execution_engine,
				binding.materialization_id::text
			FROM platform.query_runs AS run
			JOIN platform.query_run_materializations AS binding
			  ON binding.query_run_id=run.id AND binding.tenant_id=run.tenant_id
			WHERE run.dataset_id=$1 AND run.dataset_version_id=$2
			  AND run.run_type='VALIDATION'
			ORDER BY run.created_at DESC LIMIT 1`,
			dwdDatasetID, createdDWD.DraftVersionID).
			Scan(&engine, &boundMaterializationID)
	})
	if err != nil || engine != queryruntime.ExecutionPostgreSQL ||
		boundMaterializationID != activeODS.ID {
		t.Fatalf(
			"query audit engine=%s materialization=%s err=%v",
			engine, boundMaterializationID, err,
		)
	}

	dwdRun, created, err := materializationStore.RegisterCurrent(
		ctx, tenantID, actorID, dwdDatasetID,
		materialization.RegisterCurrentRequest{
			Mode: materialization.RunModeFull, MaxAttempts: 3,
		},
	)
	if err != nil || !created {
		t.Fatalf("register published DWD: created=%v err=%v", created, err)
	}
	detail, err := materializationStore.GetBuild(
		ctx, tenantID, dwdDatasetID, dwdRun.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if detail.DatasetVersionID != publishedDWD.ID ||
		len(detail.Inputs) != 1 ||
		detail.Inputs[0].MaterializationID != activeODS.ID ||
		detail.Inputs[0].Layer != string(materialization.LayerODS) {
		t.Fatalf("registered DWD detail=%+v", detail)
	}

	activeDWD := activateQueryRuntimeBuild(
		t, ctx, materializationStore, workerPool, tenantID, dwdRun,
		publishedDWD.DSLHash, strings.Repeat("d", 64),
		"order_id bigint NOT NULL, region text NOT NULL, amount numeric NOT NULL",
		"(order_id,region,amount) VALUES (1,'east',10),(2,'west',20)",
	)

	dwsDocument := integrationDWSDocument(uuid.NewString(), publishedDWD.ID)
	dwsRaw, err := json.Marshal(dwsDocument)
	if err != nil {
		t.Fatal(err)
	}
	createdDWS, err := datasetService.Create(
		ctx, tenantID, actorID, dataset.CreateInput{
			Code: dwsDocument.Dataset.Code, Name: dwsDocument.Dataset.Name,
			Description: dwsDocument.Dataset.Description,
			Type:        dwsDocument.Dataset.Type, Layer: dataset.LayerDWS,
			DSL: dwsRaw,
		},
	)
	if err != nil {
		t.Fatalf("create strict DWS: %v", err)
	}
	if err := grantQueryRuntimeAction(
		ctx, appPool, tenantID, actorID,
		"DATASET", createdDWS.ID, "PUBLISH",
	); err != nil {
		t.Fatalf("grant DWS publication permission: %v", err)
	}
	publishedDWS, err := datasetService.Publish(
		ctx, tenantID, actorID, createdDWS.ID,
		"queryruntime-dws-publish-"+tenantID[:8],
		dataset.PublishInput{
			DraftVersionID:             createdDWS.DraftVersionID,
			ExpectedVersion:            createdDWS.Version,
			ExpectedDraftRecordVersion: createdDWS.DraftRecordVersion,
			ExpectedDSLHash:            createdDWS.DSLHash,
			ValidationParameters:       map[string]any{},
		},
	)
	if err != nil {
		var validation *dataset.PublicationValidationError
		if errors.As(err, &validation) {
			t.Fatalf(
				"publish strict DWS through PostgreSQL preview: %+v",
				validation.Issues,
			)
		}
		t.Fatalf("publish strict DWS through PostgreSQL preview: %v", err)
	}
	if publishedDWS.Layer != dataset.LayerDWS ||
		publishedDWS.Status != "PUBLISHED" {
		t.Fatalf("published DWS=%+v", publishedDWS)
	}

	engine, boundMaterializationID = "", ""
	err = database.WithTenantTx(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT run.execution_engine,
				binding.materialization_id::text
			FROM platform.query_runs AS run
			JOIN platform.query_run_materializations AS binding
			  ON binding.query_run_id=run.id AND binding.tenant_id=run.tenant_id
			WHERE run.dataset_id=$1 AND run.dataset_version_id=$2
			  AND run.run_type='VALIDATION'
			ORDER BY run.created_at DESC LIMIT 1`,
			createdDWS.ID, createdDWS.DraftVersionID).
			Scan(&engine, &boundMaterializationID)
	})
	if err != nil || engine != queryruntime.ExecutionPostgreSQL ||
		boundMaterializationID != activeDWD.ID {
		t.Fatalf(
			"DWS audit engine=%s materialization=%s err=%v",
			engine, boundMaterializationID, err,
		)
	}

	dwsRun, created, err := materializationStore.RegisterCurrent(
		ctx, tenantID, actorID, createdDWS.ID,
		materialization.RegisterCurrentRequest{
			Mode: materialization.RunModeFull, MaxAttempts: 3,
		},
	)
	if err != nil || !created {
		t.Fatalf("register published DWS: created=%v err=%v", created, err)
	}
	dwsDetail, err := materializationStore.GetBuild(
		ctx, tenantID, createdDWS.ID, dwsRun.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if dwsDetail.DatasetVersionID != publishedDWS.ID ||
		len(dwsDetail.Inputs) != 1 ||
		dwsDetail.Inputs[0].MaterializationID != activeDWD.ID ||
		dwsDetail.Inputs[0].Layer != string(materialization.LayerDWD) {
		t.Fatalf("registered DWS detail=%+v", dwsDetail)
	}
	activeDWS := activateQueryRuntimeBuild(
		t, ctx, materializationStore, workerPool, tenantID, dwsRun,
		publishedDWS.DSLHash, strings.Repeat("e", 64),
		"region text NOT NULL, revenue numeric NOT NULL",
		"(region,revenue) VALUES ('east',10),('west',20)",
	)

	metricDefinition := metric.Definition{
		SchemaVersion: metric.DefinitionVersion,
		Metric: metric.Descriptor{
			Code: "governed_revenue", Name: "Governed revenue",
			Description: "DWS metric bound to the active published materialization",
			Type:        "DERIVED",
		},
		DatasetID: createdDWS.ID, DatasetVersionID: publishedDWS.ID,
		Expression:                   metric.Expression{Type: "FIELD_REF", FieldID: "field_revenue"},
		Aggregation:                  "NONE",
		Unit:                         "USD",
		NumberFormat:                 "#,##0.00",
		TimeGrain:                    "NONE",
		Additivity:                   "ADDITIVE",
		DecimalScale:                 2,
		RoundingMode:                 "HALF_UP",
		NullHandling:                 "IGNORE",
		DivisionByZero:               "NULL",
		NonAdditiveDimensionFieldIDs: []string{},
		AllowedDimensions: []metric.Dimension{{
			FieldID: "field_region", Name: "Region",
			HierarchyFieldIDs: []string{},
			SortDirection:     "ASC", NullLabel: "Unknown",
		}},
	}
	metricRaw, err := json.Marshal(metricDefinition)
	if err != nil {
		t.Fatal(err)
	}
	metricService := metric.NewService(
		metric.NewPostgresStore(appPool),
		queryService,
	)
	createdMetric, err := metricService.Create(
		ctx, tenantID, actorID,
		metric.CreateInput{Definition: metricRaw},
	)
	if err != nil {
		t.Fatalf("create DWS metric: %v", err)
	}
	metricPreview, err := metricService.Preview(
		ctx, tenantID, actorID, createdMetric.ID,
		metric.PreviewInput{
			Parameters:        map[string]any{},
			DimensionFieldIDs: []string{"field_region"},
			MaxRows:           10,
		},
	)
	if err != nil {
		t.Fatalf("preview DWS metric through PostgreSQL: %v", err)
	}
	if metricPreview.RowCount != 2 ||
		len(metricPreview.Columns) != 2 ||
		metricPreview.Columns[0] != "region" ||
		metricPreview.Columns[1] != "governed_revenue" {
		t.Fatalf("DWS metric preview=%+v", metricPreview)
	}
	if err := grantQueryRuntimeAction(
		ctx, appPool, tenantID, actorID,
		"METRIC", createdMetric.ID, "PUBLISH",
	); err != nil {
		t.Fatalf("grant metric publication permission: %v", err)
	}
	publishedMetric, err := metricService.Publish(
		ctx, tenantID, actorID, createdMetric.ID,
		"queryruntime-metric-publish-"+tenantID[:8],
		metric.PublishInput{
			DraftVersionID:             createdMetric.DraftVersionID,
			ExpectedVersion:            createdMetric.Version,
			ExpectedDraftRecordVersion: createdMetric.DraftRecordVersion,
			ExpectedDefinitionHash:     createdMetric.DefinitionHash,
			ValidationParameters:       map[string]any{},
		},
	)
	if err != nil {
		t.Fatalf("publish DWS metric through PostgreSQL preview: %v", err)
	}
	if publishedMetric.Status != "PUBLISHED" ||
		publishedMetric.DatasetVersionID != publishedDWS.ID {
		t.Fatalf("published metric=%+v", publishedMetric)
	}

	engine, boundMaterializationID = "", ""
	err = database.WithTenantTx(ctx, appPool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT run.execution_engine,
				binding.materialization_id::text
			FROM platform.query_runs AS run
			JOIN platform.query_run_materializations AS binding
			  ON binding.query_run_id=run.id AND binding.tenant_id=run.tenant_id
			WHERE run.metric_id=$1 AND run.metric_version_id=$2
			  AND run.run_type='VALIDATION'
			ORDER BY run.created_at DESC LIMIT 1`,
			createdMetric.ID, createdMetric.DraftVersionID).
			Scan(&engine, &boundMaterializationID)
	})
	if err != nil || engine != queryruntime.ExecutionPostgreSQL ||
		boundMaterializationID != activeDWS.ID {
		t.Fatalf(
			"metric audit engine=%s materialization=%s err=%v",
			engine, boundMaterializationID, err,
		)
	}
}

func activateQueryRuntimeBuild(
	t *testing.T,
	ctx context.Context,
	store *materialization.PostgresStore,
	workerPool *pgxpool.Pool,
	tenantID string,
	expected materialization.Run,
	schemaHash, snapshotHash, columns, seed string,
) materialization.Materialization {
	t.Helper()
	claim, err := store.Claim(
		ctx, tenantID, "queryruntime-layer-worker", time.Minute,
	)
	if err != nil || claim == nil || claim.ID != expected.ID {
		t.Fatalf("claim %s fixture: claim=%+v err=%v", expected.Layer, claim, err)
	}
	for _, node := range claim.Plan.Nodes {
		if err := store.StartNode(ctx, *claim, node.ID); err != nil {
			t.Fatalf("start %s node %s: %v", expected.Layer, node.ID, err)
		}
		rows, size := int64(2), int64(8192)
		if err := store.FinishNode(
			ctx, *claim, node.ID, materialization.NodeResult{
				Status:        materialization.NodeSucceeded,
				InputRowCount: &rows, OutputRowCount: &rows,
				OutputSizeBytes: &size,
			},
		); err != nil {
			t.Fatalf("finish %s node %s: %v", expected.Layer, node.ID, err)
		}
	}
	physical, err := materialization.GeneratePhysicalIdentifier(
		tenantID, expected.DatasetID, expected.ID, expected.Layer,
	)
	if err != nil {
		t.Fatal(err)
	}
	qualified := quoteIntegrationIdentifier(physical.Schema) + "." +
		quoteIntegrationIdentifier(physical.Name)
	// The raw fragments are fixed test fixtures. Production query and build
	// service contracts remain structured and never accept caller SQL.
	if _, err := workerPool.Exec(
		ctx, "CREATE TABLE "+qualified+" ("+columns+")",
	); err != nil {
		t.Fatalf("create %s relation: %v", expected.Layer, err)
	}
	if _, err := workerPool.Exec(
		ctx, "INSERT INTO "+qualified+" "+seed,
	); err != nil {
		t.Fatalf("seed %s relation: %v", expected.Layer, err)
	}
	active, err := store.Activate(
		ctx, *claim, materialization.Activation{
			Physical: physical, RelationKind: "TABLE",
			SchemaHash: schemaHash, SnapshotHash: snapshotHash,
			RowCount: 2, SizeBytes: 8192,
		},
	)
	if err != nil {
		t.Fatalf("activate %s: %v", expected.Layer, err)
	}
	return active
}

func grantQueryRuntimeAction(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, actorID, objectType, objectID, action string,
) error {
	return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(
			tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
		) VALUES($1,'USER',$2,$3,$4,$5,$2)`,
			tenantID, actorID, objectType, objectID, action)
		return err
	})
}
