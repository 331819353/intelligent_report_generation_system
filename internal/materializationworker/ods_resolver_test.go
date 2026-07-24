package materializationworker

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/warehouse"
)

type recordingDatabaseStager struct {
	calls  int
	input  warehouse.StageInput
	result warehouse.StageResult
	err    error
}

func (stager *recordingDatabaseStager) Stage(
	_ context.Context,
	input warehouse.StageInput,
) (warehouse.StageResult, error) {
	stager.calls++
	stager.input = input
	return stager.result, stager.err
}

type recordingFileStager struct {
	calls  int
	input  warehouse.FileStageInput
	result warehouse.StageResult
	err    error
}

type blockingDatabaseStager struct {
	started chan struct{}
}

func (stager blockingDatabaseStager) Stage(
	ctx context.Context,
	_ warehouse.StageInput,
) (warehouse.StageResult, error) {
	close(stager.started)
	<-ctx.Done()
	return warehouse.StageResult{}, ctx.Err()
}

func (stager *recordingFileStager) Stage(
	_ context.Context,
	input warehouse.FileStageInput,
) (warehouse.StageResult, error) {
	stager.calls++
	stager.input = input
	return stager.result, stager.err
}

func TestODSResolverRoutesDatabaseSourceToExactConnector(t *testing.T) {
	for _, test := range []struct {
		name        string
		sourceType  datasource.Type
		wantDialect querycompiler.Dialect
		wantMySQL   int
		wantOracle  int
	}{
		{
			name: "mysql", sourceType: datasource.TypeMySQL,
			wantDialect: querycompiler.MySQL, wantMySQL: 1,
		},
		{
			name: "oracle", sourceType: datasource.TypeOracle,
			wantDialect: querycompiler.Oracle, wantOracle: 1,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mysql := &recordingDatabaseStager{
				result: warehouse.StageResult{
					Schema: "warehouse_staging", Table: "mysql_stage", RowCount: 3,
				},
			}
			oracle := &recordingDatabaseStager{
				result: warehouse.StageResult{
					Schema: "warehouse_staging", Table: "oracle_stage", RowCount: 4,
				},
			}
			resolver := NewODSResolver(nil, mysql, oracle, nil)
			claim, plan := odsStagePlan(materialization.InputSourceTable)
			plan.source.Type = test.sourceType

			result, err := resolver.stage(context.Background(), claim, plan)
			if err != nil {
				t.Fatal(err)
			}
			if mysql.calls != test.wantMySQL || oracle.calls != test.wantOracle {
				t.Fatalf("mysql=%d oracle=%d", mysql.calls, oracle.calls)
			}
			selected := mysql
			if test.sourceType == datasource.TypeOracle {
				selected = oracle
			}
			if selected.input.Source.ConfigVersionID != plan.input.DataSourceVersionID ||
				selected.input.Scan.Dialect != test.wantDialect ||
				selected.input.Scan.MaxRows != warehouse.MaxODSRows ||
				selected.input.Scan.Table.Name != "orders" ||
				selected.input.Scan.Table.Schema != "sales" ||
				selected.input.BatchSize != odsStageBatchSize {
				t.Fatalf("input=%#v", selected.input)
			}
			if result.Schema != "warehouse_staging" {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func TestODSResolverRoutesPublishedFileVersionToFileStager(t *testing.T) {
	file := &recordingFileStager{result: warehouse.StageResult{
		Schema: "warehouse_staging", Table: "file_stage", RowCount: 2,
	}}
	resolver := NewODSResolver(nil, nil, nil, file)
	claim, plan := odsStagePlan(materialization.InputFileVersion)

	result, err := resolver.stage(context.Background(), claim, plan)
	if err != nil {
		t.Fatal(err)
	}
	if file.calls != 1 ||
		file.input.FileVersionID != plan.input.FileVersionID ||
		file.input.Source.ConfigVersionID != plan.input.DataSourceVersionID ||
		file.input.ExpectedFileAssetID != plan.fileAssetID ||
		file.input.ExpectedSHA256 != plan.fileSHA256 ||
		file.input.TableName != "Orders" ||
		file.input.MaxRows != warehouse.MaxODSRows ||
		result.RowCount != 2 {
		t.Fatalf("calls=%d input=%#v result=%#v", file.calls, file.input, result)
	}
}

func TestODSResolverConvertsStagingFailuresToStableExecutionErrors(t *testing.T) {
	mysql := &recordingDatabaseStager{err: errors.New("remote connection failed")}
	resolver := NewODSResolver(nil, mysql, nil, nil)
	claim, plan := odsStagePlan(materialization.InputSourceTable)
	_, stageErr := resolver.stage(context.Background(), claim, plan)
	err := mapODSStageError(context.Background(), context.Background(), stageErr)
	var execution *ExecutionError
	if !errors.As(err, &execution) || execution.Code != CodeODSStagingFailed {
		t.Fatalf("error=%v", err)
	}

	plan.source.Type = datasource.TypeOracle
	_, err = resolver.stage(context.Background(), claim, plan)
	if !errors.As(err, &execution) ||
		execution.Code != CodeODSSourceStagingNotConfigured {
		t.Fatalf("error=%v", err)
	}
}

func TestODSResolverPropagatesLeaseCancellationIntoSourceStager(t *testing.T) {
	started := make(chan struct{})
	resolver := NewODSResolver(
		nil,
		blockingDatabaseStager{started: started},
		nil,
		nil,
	)
	claim, plan := odsStagePlan(materialization.InputSourceTable)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := resolver.stage(ctx, claim, plan)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestCompositeResolverSeparatesODSAndPostgresLayers(t *testing.T) {
	odsCalls, postgresCalls := 0, 0
	ods := resolverFunc(func(context.Context, materialization.Claim) (ResolvedBuild, error) {
		odsCalls++
		return ResolvedBuild{VersionNo: 1}, nil
	})
	postgres := resolverFunc(func(context.Context, materialization.Claim) (ResolvedBuild, error) {
		postgresCalls++
		return ResolvedBuild{VersionNo: 2}, nil
	})
	resolver := NewCompositeResolver(ods, postgres)
	claim := testDWDClaim()
	claim.Layer = materialization.LayerODS
	result, err := resolver.Resolve(context.Background(), claim)
	if err != nil || result.VersionNo != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	claim.Layer = materialization.LayerDWS
	result, err = resolver.Resolve(context.Background(), claim)
	if err != nil || result.VersionNo != 2 ||
		odsCalls != 1 || postgresCalls != 1 {
		t.Fatalf(
			"result=%#v err=%v ods=%d postgres=%d",
			result, err, odsCalls, postgresCalls,
		)
	}
}

func odsStagePlan(
	inputType materialization.InputType,
) (materialization.Claim, odsSourcePlan) {
	claim := materialization.Claim{Run: materialization.Run{
		ID: workerRunID, TenantID: workerTenantID,
		DatasetID: workerDatasetID, DatasetVersionID: workerVersionID,
		Layer: materialization.LayerODS, Mode: materialization.RunModeFull,
	}}
	node := dataset.Node{
		ID: "orders", Type: "TABLE",
		DataSourceID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		TableID:      "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		Alias:        "orders",
		Projection:   []string{"id"},
	}
	input := materialization.InputSnapshot{
		Ordinal:             1,
		Type:                inputType,
		Layer:               "SOURCE",
		DataSourceID:        node.DataSourceID,
		DataSourceVersionID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
		SchemaHash:          workerHash,
		SnapshotHash:        workerInputHash,
	}
	sourceType := datasource.TypeMySQL
	tableName := "orders"
	if inputType == materialization.InputSourceTable {
		input.MetadataTableID = node.TableID
	} else {
		sourceType = datasource.TypeExcel
		tableName = "Orders"
		input.FileVersionID = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
		node.FileVersionID = input.FileVersionID
	}
	source := datasource.Source{
		ID: node.DataSourceID, TenantID: workerTenantID,
		Type: sourceType, Status: datasource.StatusActive,
		PublicationStatus:  datasource.PublicationPublished,
		ConfigVersionID:    input.DataSourceVersionID,
		PublishedVersionID: input.DataSourceVersionID,
		FileAssetID:        "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
		FileVersionID:      input.FileVersionID,
	}
	plan := odsSourcePlan{
		document: dataset.Document{
			Dataset: dataset.Descriptor{Layer: dataset.LayerODS},
			Nodes:   []dataset.Node{node},
		},
		node: node, input: input, source: source,
		sourceTable: querycompiler.TableRef{
			NodeID: node.ID, Schema: "sales", Name: tableName,
			Columns:     map[string]bool{"id": true},
			ColumnTypes: map[string]string{"id": "INTEGER"},
		},
		stageColumns: []warehouse.StageColumn{{
			Name: "id", CanonicalType: "INTEGER",
		}},
		tableName:        tableName,
		fileAssetID:      source.FileAssetID,
		fileSHA256:       input.SnapshotHash,
		maxExcelFileSize: 1024,
	}
	return claim, plan
}
