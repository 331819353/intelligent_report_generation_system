package datasource

import (
	"context"
	"strings"
	"testing"
	"time"
)

type metadataJobRepo struct {
	enqueued         metadataJobRequest
	claim            *metadataJobClaim
	items            []metadataJobItem
	updates          []metadataJobItemUpdate
	enriched         map[string]bool
	completed        map[string]bool
	finished         bool
	heartbeatStarted chan struct{}
	heartbeatRelease chan struct{}
}

type countingMetadataJobConnector struct {
	importConnector
	sampleCalls int
}

func (c *countingMetadataJobConnector) Sample(context.Context, Source, MetadataTable, int) (SampleResult, error) {
	c.sampleCalls++
	return c.sample, nil
}

func (r *metadataJobRepo) EnqueueMetadataJob(_ context.Context, request metadataJobRequest) (MetadataJob, error) {
	r.enqueued = request
	return MetadataJob{ID: "job-1", DataSourceID: request.DataSourceID, Kind: request.Kind, Mode: request.Mode, Status: "QUEUED", Stage: "QUEUED", Total: len(request.Tables)}, nil
}
func (r *metadataJobRepo) GetMetadataJob(context.Context, string, string, string) (MetadataJob, error) {
	return MetadataJob{}, nil
}
func (r *metadataJobRepo) LatestActiveMetadataJob(context.Context, string, string) (*MetadataJob, error) {
	return nil, nil
}
func (r *metadataJobRepo) ListMetadataJobTenantIDs(context.Context) ([]string, error) {
	return []string{"tenant-1"}, nil
}
func (r *metadataJobRepo) ClaimMetadataJob(context.Context, string, string, time.Duration) (*metadataJobClaim, error) {
	claim := r.claim
	r.claim = nil
	return claim, nil
}
func (r *metadataJobRepo) ListMetadataJobItems(context.Context, string, string) ([]metadataJobItem, error) {
	return r.items, nil
}
func (r *metadataJobRepo) IsMetadataTableEnriched(_ context.Context, _, tableID, structureHash string) (bool, error) {
	return r.enriched[tableID+"\x1f"+structureHash], nil
}
func (r *metadataJobRepo) IsMetadataJobItemCompleted(_ context.Context, _, itemID, tableID, structureHash string) (bool, error) {
	return r.completed[itemID+"\x1f"+tableID+"\x1f"+structureHash], nil
}

func (r *metadataJobRepo) HeartbeatMetadataJob(ctx context.Context, _ string, _ string, _ string, _ time.Duration) error {
	if r.heartbeatStarted != nil {
		select {
		case r.heartbeatStarted <- struct{}{}:
		default:
		}
	}
	if r.heartbeatRelease != nil {
		select {
		case <-r.heartbeatRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (r *metadataJobRepo) UpdateMetadataJobStage(context.Context, string, string, string, string, time.Duration) error {
	return nil
}
func (r *metadataJobRepo) UpdateMetadataJobItem(_ context.Context, _, _, _, _ string, update metadataJobItemUpdate, _ time.Duration) error {
	r.updates = append(r.updates, update)
	return nil
}
func (r *metadataJobRepo) FinishMetadataJob(context.Context, string, string, string) (MetadataJob, error) {
	r.finished = true
	return MetadataJob{Status: "SUCCEEDED"}, nil
}
func (r *metadataJobRepo) FailMetadataJob(context.Context, string, string, string, string, string) error {
	return nil
}

func TestQueueImportTablesReturnsBeforeSamplingOrLLM(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id"}}}
	baseRepo := &repo{source: Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive}, quota: Quota{MaxDataSources: 10}}
	connector := importConnector{connector: connector{kind: TypeMySQL}, discovered: SyncResult{Tables: []MetadataTable{table}}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	job, err := service.QueueImportTables(context.Background(), "tenant-1", "actor-1", "source-1", []TableSelection{{SchemaName: "sales", TableName: "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "QUEUED" || job.Total != 1 || jobs.enqueued.Kind != MetadataJobImport || jobs.enqueued.Mode != MetadataRefreshFull {
		t.Fatalf("job=%#v request=%#v", job, jobs.enqueued)
	}
	if len(baseRepo.selectedBatches) != 0 || len(completer.tableIDs) != 0 {
		t.Fatal("HTTP enqueue path performed sampling, persistence or LLM work")
	}
}

func TestQueueRefreshTablesCanTargetOneManagedTable(t *testing.T) {
	baseRepo := &repo{
		source: Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive},
		quota:  Quota{MaxDataSources: 10},
		activeSelections: []TableSelection{
			{TableID: "table-1", SchemaName: "sales", TableName: "orders"},
			{TableID: "table-2", SchemaName: "sales", TableName: "customers"},
		},
	}
	jobs := &metadataJobRepo{}
	service := NewService(baseRepo, importConnector{connector: connector{kind: TypeMySQL}})
	service.SetTableCompleter(&completingRecorder{})
	service.SetMetadataJobRepository(jobs)

	job, err := service.QueueRefreshTables(context.Background(), "tenant-1", "actor-1", "source-1", MetadataRefreshFull, "table-2")
	if err != nil {
		t.Fatal(err)
	}
	if job.Total != 1 || len(jobs.enqueued.Tables) != 1 || jobs.enqueued.Tables[0].TableID != "table-2" || jobs.enqueued.Kind != MetadataJobRefresh {
		t.Fatalf("job=%#v request=%#v", job, jobs.enqueued)
	}
	if _, err := service.QueueRefreshTables(context.Background(), "tenant-1", "actor-1", "source-1", MetadataRefreshFull, "other-source-table"); err == nil {
		t.Fatal("foreign or inactive managed table id was accepted")
	}
}

func TestIncrementalMetadataJobSkipsUnchangedSuccessfullyEnrichedTable(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id", CanonicalType: "INTEGER"}}}
	structureHash, _, err := metadataTableHash(table)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, err := metadataJobSourceHash(source)
	if err != nil {
		t.Fatal(err)
	}
	baseRepo := &repo{source: source, quota: Quota{MaxDataSources: 10}}
	connector := importConnector{connector: connector{kind: TypeMySQL}, discovered: SyncResult{Tables: []MetadataTable{table}}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim:    &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshIncremental}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items:    []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", PreviousStructureHash: structureHash, PreviousEnrichmentStatus: "SUCCEEDED", Status: "QUEUED"}},
		enriched: map[string]bool{"table-1\x1f" + structureHash: true},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if !jobs.finished || len(jobs.updates) != 2 || jobs.updates[1].Status != "SKIPPED" || jobs.updates[1].ErrorCode != "UNCHANGED" {
		t.Fatalf("updates=%#v finished=%v", jobs.updates, jobs.finished)
	}
	if len(baseRepo.selectedBatches) != 0 || len(completer.tableIDs) != 0 {
		t.Fatal("unchanged incremental table was persisted, sampled or sent to LLM")
	}
}

func TestIncrementalMetadataJobDoesNotTrustStaleLatestEnrichmentStatus(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id", CanonicalType: "INTEGER"}}}
	structureHash, _, err := metadataTableHash(table)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{source: source, quota: Quota{MaxDataSources: 10}, selectedIDs: map[string]string{metadataTableKey(table): "table-1"}}
	connector := importConnector{connector: connector{kind: TypeMySQL}, discovered: SyncResult{Tables: []MetadataTable{table}}, sample: SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}, {2}, {3}}}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim: &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshIncremental}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		// 入队快照中的旧任务状态看似成功，但精确结构完善查询未命中时必须重新加工。
		items: []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", PreviousStructureHash: structureHash, PreviousEnrichmentStatus: "SUCCEEDED", Status: "QUEUED"}},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if last.Status != "SUCCEEDED" || last.TableID != "table-1" || len(baseRepo.managedBatches) != 1 || len(baseRepo.selectedBatches) != 0 || len(completer.tableIDs) != 1 {
		t.Fatalf("last=%#v managed=%d selected=%d LLM=%#v", last, len(baseRepo.managedBatches), len(baseRepo.selectedBatches), completer.tableIDs)
	}
}

func TestIncrementalMetadataJobCompletesOnlyChangedColumnsWithFilteredSamples(t *testing.T) {
	table := MetadataTable{
		SchemaName: "sales",
		Name:       "orders",
		Columns: []MetadataColumn{
			{Name: "id", CanonicalType: "INTEGER"},
			{Name: "email", CanonicalType: "STRING"},
		},
	}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{
		source: source,
		quota:  Quota{MaxDataSources: 10},
		managedResult: ManagedMetadataApplyResult{
			TableID: "table-1",
			Managed: true,
			PendingColumns: []MetadataCompletionColumn{
				{ID: "column-email", Name: "email"},
			},
		},
	}
	connector := &countingMetadataJobConnector{importConnector: importConnector{
		connector:  connector{kind: TypeMySQL},
		discovered: SyncResult{Tables: []MetadataTable{table}},
		sample: SampleResult{
			Columns: []string{"id", "email"},
			Rows:    [][]any{{1, "first@example.com"}, {2, "second@example.com"}},
		},
	}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim: &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshIncremental}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items: []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", PreviousStructureHash: "previous-structure", Status: "QUEUED"}},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if last.Status != "SUCCEEDED" || connector.sampleCalls != 1 || len(completer.tableIDs) != 1 {
		t.Fatalf("last=%#v sampleCalls=%d LLM=%#v", last, connector.sampleCalls, completer.tableIDs)
	}
	if completer.targetTables[0] || len(completer.targetColumnIDs[0]) != 1 || completer.targetColumnIDs[0][0] != "column-email" {
		t.Fatalf("targetTable=%v targetColumnIDs=%#v", completer.targetTables[0], completer.targetColumnIDs[0])
	}
	if len(completer.rows) != 2 {
		t.Fatalf("rows=%#v", completer.rows)
	}
	for _, row := range completer.rows {
		if len(row) != 1 || row["email"] == nil {
			t.Fatalf("sample row was not limited to the changed column: %#v", row)
		}
		if _, exists := row["id"]; exists {
			t.Fatalf("unchanged column leaked into incremental sample: %#v", row)
		}
	}
}

func TestIncrementalMetadataJobWithNoPendingTargetsSkipsSamplingAndLLM(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id", CanonicalType: "INTEGER"}}}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{
		source:        source,
		quota:         Quota{MaxDataSources: 10},
		managedResult: ManagedMetadataApplyResult{TableID: "table-1", Managed: true},
	}
	connector := &countingMetadataJobConnector{importConnector: importConnector{
		connector:  connector{kind: TypeMySQL},
		discovered: SyncResult{Tables: []MetadataTable{table}},
		sample:     SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}}},
	}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim: &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshIncremental}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items: []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", PreviousStructureHash: "previous-structure", Status: "QUEUED"}},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if last.Status != "SUCCEEDED" || len(baseRepo.managedBatches) != 1 {
		t.Fatalf("last=%#v managed=%d", last, len(baseRepo.managedBatches))
	}
	if connector.sampleCalls != 0 || len(completer.tableIDs) != 0 {
		t.Fatalf("no-op incremental refresh sampled or invoked LLM: sampleCalls=%d LLM=%#v", connector.sampleCalls, completer.tableIDs)
	}
}

func TestRefreshMetadataJobDeactivatesMissingSourceTableWithoutSamplingOrLLM(t *testing.T) {
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{
		source:           source,
		quota:            Quota{MaxDataSources: 10},
		deactivateResult: true,
	}
	connector := &countingMetadataJobConnector{importConnector: importConnector{
		connector:  connector{kind: TypeMySQL},
		discovered: SyncResult{Watermark: "2026-07-17T09:30:00Z", SnapshotHash: strings.Repeat("a", 64)},
		sample:     SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}}},
	}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim: &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshIncremental}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items: []metadataJobItem{{
			ID: "item-1", CatalogName: "warehouse", SchemaName: "sales", TableName: "orders",
			TableID: "table-1", PreviousStructureHash: "previous-structure", Status: "QUEUED",
		}},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if last.Status != "SUCCEEDED" || last.ErrorCode != "SOURCE_TABLE_REMOVED" {
		t.Fatalf("last=%#v", last)
	}
	if len(baseRepo.deactivated) != 1 {
		t.Fatalf("deactivated=%#v", baseRepo.deactivated)
	}
	selection := baseRepo.deactivated[0]
	if selection.CatalogName != "warehouse" || selection.SchemaName != "sales" || selection.TableName != "orders" || selection.TableID != "table-1" || selection.StructureHash != "previous-structure" {
		t.Fatalf("selection=%#v", selection)
	}
	if connector.sampleCalls != 0 || len(completer.tableIDs) != 0 || len(baseRepo.managedBatches) != 0 {
		t.Fatalf("removed table sampled, persisted or invoked LLM: sampleCalls=%d managed=%d LLM=%#v", connector.sampleCalls, len(baseRepo.managedBatches), completer.tableIDs)
	}
}

func TestAuthoritativeMetadataSnapshotRejectsPartialOrAmbiguousDiscovery(t *testing.T) {
	valid := SyncResult{Assets: 0, Watermark: "2026-07-17T09:30:00Z", SnapshotHash: strings.Repeat("a", 64), Tables: []MetadataTable{}}
	if observedAt, err := authoritativeMetadataSnapshot(valid); err != nil || observedAt.IsZero() {
		t.Fatalf("valid snapshot observedAt=%v err=%v", observedAt, err)
	}

	tests := []SyncResult{
		{Assets: 1, Watermark: valid.Watermark, SnapshotHash: valid.SnapshotHash, Tables: []MetadataTable{}},
		{Assets: 0, Watermark: "invalid", SnapshotHash: valid.SnapshotHash, Tables: []MetadataTable{}},
		{Assets: 0, Watermark: valid.Watermark, SnapshotHash: "short", Tables: []MetadataTable{}},
		{Assets: 2, Watermark: valid.Watermark, SnapshotHash: valid.SnapshotHash, Tables: []MetadataTable{{SchemaName: "sales", Name: "orders"}, {SchemaName: "sales", Name: "orders"}}},
	}
	for index, snapshot := range tests {
		if _, err := authoritativeMetadataSnapshot(snapshot); err == nil {
			t.Fatalf("invalid snapshot %d was accepted: %#v", index, snapshot)
		}
	}
}

func TestRefreshMetadataJobSkipsAssetDeletedAfterQueue(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id", CanonicalType: "INTEGER"}}}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{source: source, quota: Quota{MaxDataSources: 10}, managedMissing: true}
	connector := importConnector{connector: connector{kind: TypeMySQL}, discovered: SyncResult{Tables: []MetadataTable{table}}, sample: SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}, {2}, {3}}}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim: &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshFull}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items: []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", Status: "QUEUED"}},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if !jobs.finished || last.Status != "SKIPPED" || last.Stage != "COMPLETE" || last.TableID != "table-1" || last.ErrorCode != "ASSET_NOT_MANAGED" {
		t.Fatalf("last=%#v finished=%v", last, jobs.finished)
	}
	if len(baseRepo.managedBatches) != 1 || len(baseRepo.managedTableIDs) != 1 || baseRepo.managedTableIDs[0] != "table-1" || len(baseRepo.selectedBatches) != 0 || len(completer.tableIDs) != 0 {
		t.Fatalf("managed=%d ids=%#v selected=%d LLM=%#v", len(baseRepo.managedBatches), baseRepo.managedTableIDs, len(baseRepo.selectedBatches), completer.tableIDs)
	}
}

func TestRefreshMetadataJobDoesNotOverwriteSupersedingStructure(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id", CanonicalType: "INTEGER"}}}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{source: source, quota: Quota{MaxDataSources: 10}, managedErr: ErrMetadataRefreshSuperseded}
	connector := importConnector{connector: connector{kind: TypeMySQL}, discovered: SyncResult{Tables: []MetadataTable{table}}, sample: SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}}}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim: &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshFull}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items: []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", PreviousStructureHash: "old-structure", Status: "QUEUED"}},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if last.Status != "SKIPPED" || last.ErrorCode != "STRUCTURE_SUPERSEDED" || len(completer.tableIDs) != 0 {
		t.Fatalf("last=%#v LLM=%#v", last, completer.tableIDs)
	}
}

func TestImportMetadataJobKeepsReimportSemantics(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id", CanonicalType: "INTEGER"}}}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{source: source, quota: Quota{MaxDataSources: 10}, managedMissing: true, selectedIDs: map[string]string{metadataTableKey(table): "table-1"}}
	connector := importConnector{connector: connector{kind: TypeMySQL}, discovered: SyncResult{Tables: []MetadataTable{table}}, sample: SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}, {2}, {3}}}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim: &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobImport, Mode: MetadataRefreshFull}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items: []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", Status: "QUEUED"}},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if !jobs.finished || last.Status != "SUCCEEDED" || last.TableID != "table-1" {
		t.Fatalf("last=%#v finished=%v", last, jobs.finished)
	}
	if len(baseRepo.selectedBatches) != 1 || len(baseRepo.managedBatches) != 0 || len(completer.tableIDs) != 1 {
		t.Fatalf("selected=%d managed=%d LLM=%#v", len(baseRepo.selectedBatches), len(baseRepo.managedBatches), completer.tableIDs)
	}
}

func TestFullMetadataRefreshReprocessesAlreadyEnrichedStructure(t *testing.T) {
	table := MetadataTable{
		SchemaName: "sales",
		Name:       "orders",
		Columns: []MetadataColumn{
			{Name: "id", CanonicalType: "INTEGER"},
			{Name: "email", CanonicalType: "STRING"},
		},
	}
	structureHash, _, err := metadataTableHash(table)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{
		source: source,
		quota:  Quota{MaxDataSources: 10},
		// FULL 模式必须忽略仓储返回的局部待完善字段，仍完整加工整张表。
		managedResult: ManagedMetadataApplyResult{
			TableID: "table-1",
			Managed: true,
			PendingColumns: []MetadataCompletionColumn{
				{ID: "column-email", Name: "email"},
			},
		},
	}
	connector := &countingMetadataJobConnector{importConnector: importConnector{
		connector:  connector{kind: TypeMySQL},
		discovered: SyncResult{Tables: []MetadataTable{table}},
		sample:     SampleResult{Columns: []string{"id", "email"}, Rows: [][]any{{1, "first@example.com"}}},
	}}
	completer := &completingRecorder{}
	jobs := &metadataJobRepo{
		claim:    &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshFull}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items:    []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", PreviousStructureHash: structureHash, Status: "QUEUED"}},
		enriched: map[string]bool{"table-1\x1f" + structureHash: true},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if last.Status != "SUCCEEDED" || len(baseRepo.managedBatches) != 1 || connector.sampleCalls != 1 || len(completer.tableIDs) != 1 {
		t.Fatalf("last=%#v managed=%d sampleCalls=%d LLM=%#v", last, len(baseRepo.managedBatches), connector.sampleCalls, completer.tableIDs)
	}
	if !completer.targetTables[0] || completer.targetColumnIDs[0] != nil {
		t.Fatalf("FULL refresh was narrowed: targetTable=%v targetColumnIDs=%#v", completer.targetTables[0], completer.targetColumnIDs[0])
	}
	if len(completer.rows) != 1 || len(completer.rows[0]) != 2 || completer.rows[0]["id"] == nil || completer.rows[0]["email"] == nil {
		t.Fatalf("FULL refresh did not keep the complete sample: %#v", completer.rows)
	}
}

func TestMetadataJobRecoveryClosesOnlyCurrentCompletedItem(t *testing.T) {
	table := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id", CanonicalType: "INTEGER"}}}
	structureHash, _, err := metadataTableHash(table)
	if err != nil {
		t.Fatal(err)
	}
	source := Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive, Config: map[string]any{"host": "db"}, SecretRef: "encrypted://source"}
	sourceHash, _ := metadataJobSourceHash(source)
	baseRepo := &repo{source: source, quota: Quota{MaxDataSources: 10}}
	connector := importConnector{connector: connector{kind: TypeMySQL}, discovered: SyncResult{Tables: []MetadataTable{table}}}
	completer := &completingRecorder{}
	completionKey := "item-1\x1ftable-1\x1f" + structureHash
	jobs := &metadataJobRepo{
		claim:     &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1", DataSourceID: "source-1", Kind: MetadataJobRefresh, Mode: MetadataRefreshFull}, TenantID: "tenant-1", RequestedBy: "actor-1", SourceConfigHash: sourceHash},
		items:     []metadataJobItem{{ID: "item-1", SchemaName: "sales", TableName: "orders", TableID: "table-1", PreviousStructureHash: structureHash, Status: "QUEUED"}},
		enriched:  map[string]bool{"table-1\x1f" + structureHash: true},
		completed: map[string]bool{completionKey: true},
	}
	service := NewService(baseRepo, connector)
	service.SetTableCompleter(completer)
	service.SetMetadataJobRepository(jobs)

	processed, err := service.ProcessNextMetadataJob(context.Background(), "tenant-1", "worker-1", time.Hour)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	last := jobs.updates[len(jobs.updates)-1]
	if last.Status != "SUCCEEDED" || len(baseRepo.managedBatches) != 0 || len(completer.tableIDs) != 0 {
		t.Fatalf("last=%#v managed=%d LLM=%#v", last, len(baseRepo.managedBatches), completer.tableIDs)
	}
}

func TestMetadataJobHeartbeatStopsWithoutCancellingInflightPersistence(t *testing.T) {
	jobs := &metadataJobRepo{heartbeatStarted: make(chan struct{}, 1), heartbeatRelease: make(chan struct{})}
	service := NewService(&repo{}, connector{kind: TypeMySQL})
	service.SetMetadataJobRepository(jobs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
	done := make(chan error, 1)
	claim := &metadataJobClaim{MetadataJob: MetadataJob{ID: "job-1"}, TenantID: "tenant-1"}

	go service.keepMetadataJobLease(ctx, stop, claim, "worker-1", 3*time.Second, cancel, done)
	select {
	case <-jobs.heartbeatStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not start")
	}
	close(stop)
	close(jobs.heartbeatRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("heartbeat stop error=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not stop")
	}
	if ctx.Err() != nil {
		t.Fatal("normal heartbeat shutdown cancelled the job context")
	}
}
