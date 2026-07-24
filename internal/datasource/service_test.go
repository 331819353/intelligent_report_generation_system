package datasource

import (
	"context"
	"errors"
	"testing"
	"time"
)

type repo struct {
	source           Source
	count            int
	quota            Quota
	statuses         []Status
	selectedMetadata SyncResult
	selectedBatches  []SyncResult
	selectedIDs      map[string]string
	managedBatches   []SyncResult
	managedTableIDs  []string
	managedMissing   bool
	managedErr       error
	managedResult    ManagedMetadataApplyResult
	deactivated      []TableSelection
	deactivateResult bool
	deactivateErr    error
	activeSelections []TableSelection
}

func (r *repo) Count(context.Context, string) (int, error)              { return r.count, nil }
func (r *repo) Create(_ context.Context, s Source) (Source, error)      { r.source = s; return s, nil }
func (r *repo) List(context.Context, string) ([]Source, error)          { return []Source{r.source}, nil }
func (r *repo) Get(context.Context, string, string) (Source, error)     { return r.source, nil }
func (r *repo) Update(_ context.Context, s Source) (Source, error)      { r.source = s; return s, nil }
func (r *repo) ApplyMetadata(context.Context, Source, SyncResult) error { return nil }
func (r *repo) ApplySelectedMetadata(_ context.Context, _ Source, result SyncResult) (map[string]string, error) {
	r.selectedMetadata = result
	r.selectedBatches = append(r.selectedBatches, result)
	return r.selectedIDs, nil
}
func (r *repo) ApplyManagedMetadata(_ context.Context, _ Source, expectedTableID, _ string, result SyncResult) (ManagedMetadataApplyResult, error) {
	r.managedBatches = append(r.managedBatches, result)
	r.managedTableIDs = append(r.managedTableIDs, expectedTableID)
	if r.managedErr != nil {
		return ManagedMetadataApplyResult{}, r.managedErr
	}
	if r.managedMissing || expectedTableID == "" {
		return ManagedMetadataApplyResult{}, nil
	}
	if r.managedResult.TableID != "" || r.managedResult.Managed {
		return r.managedResult, nil
	}
	return ManagedMetadataApplyResult{TableID: expectedTableID, Managed: true, TablePending: true}, nil
}
func (r *repo) DeactivateManagedMetadata(_ context.Context, _ Source, selection TableSelection, _ time.Time) (bool, error) {
	r.deactivated = append(r.deactivated, selection)
	if r.deactivateErr != nil {
		return false, r.deactivateErr
	}
	return r.deactivateResult, nil
}
func (r *repo) ListActiveTableSelections(context.Context, string, string) ([]TableSelection, error) {
	return r.activeSelections, nil
}
func (r *repo) Audit(context.Context, string, string, string, string, any) error { return nil }
func (r *repo) UpdateStatus(_ context.Context, _, _ string, status Status, _ string) error {
	r.source.Status = status
	r.statuses = append(r.statuses, status)
	return nil
}
func (r *repo) Quota(context.Context, string) (Quota, error) { return r.quota, nil }

type versionedRepo struct {
	repo
	draft       Source
	published   Source
	testRuns    []ConnectionTestRun
	publishErr  error
	publishedAt time.Time
}

func (r *versionedRepo) Get(_ context.Context, _, _ string) (Source, error) {
	if r.published.ID != "" {
		return r.published, nil
	}
	return r.draft, nil
}

func (r *versionedRepo) GetDraft(context.Context, string, string) (Source, error) {
	return r.draft, nil
}

func (r *versionedRepo) Update(_ context.Context, source Source) (Source, error) {
	source.ConfigVersion++
	source.Version++
	source.ConfigVersionID = "draft-version-next"
	hash, err := sourceConfigurationHash(source)
	if err != nil {
		return Source{}, err
	}
	source.ConfigHash = hash
	source.ValidationStatus = ValidationUntested
	source.PublishedVersionID = r.published.ConfigVersionID
	source.PublicationStatus = r.draft.PublicationStatus
	source.HasUnpublishedChanges = source.PublishedVersionID != source.ConfigVersionID
	if r.published.ID != "" {
		source.Status = r.published.Status
	}
	r.draft = source
	return source, nil
}

func (r *versionedRepo) RecordConnectionTest(_ context.Context, _, _ string, run ConnectionTestRun) (ConnectionTestRun, error) {
	if run.ConfigVersion != r.draft.ConfigVersionID || run.ConfigHash != r.draft.ConfigHash {
		return ConnectionTestRun{}, ErrSourceVersionChanged
	}
	r.testRuns = append(r.testRuns, run)
	r.draft.ValidationStatus = run.Status
	r.draft.LastTestedAt = &run.CompletedAt
	r.draft.TestExpiresAt = run.ExpiresAt
	return run, nil
}

func (r *versionedRepo) Publish(_ context.Context, _, _, _, versionID, configHash string, now time.Time) (Source, error) {
	r.publishedAt = now
	if r.publishErr != nil {
		return Source{}, r.publishErr
	}
	if versionID != r.draft.ConfigVersionID || configHash != r.draft.ConfigHash {
		return Source{}, ErrSourceVersionChanged
	}
	var passed *ConnectionTestRun
	for index := len(r.testRuns) - 1; index >= 0; index-- {
		run := &r.testRuns[index]
		if run.Status == ValidationPassed && run.ConfigVersion == versionID && run.ConfigHash == configHash {
			passed = run
			break
		}
	}
	if passed == nil {
		return Source{}, ErrTestRequired
	}
	if passed.ExpiresAt == nil || !passed.ExpiresAt.After(now) {
		return Source{}, ErrTestExpired
	}
	r.draft.Status = StatusActive
	r.draft.PublicationStatus = PublicationPublished
	r.draft.PublishedVersionID = r.draft.ConfigVersionID
	r.draft.PublishedConfigVersion = r.draft.ConfigVersion
	r.draft.HasUnpublishedChanges = false
	r.published = r.draft
	return r.draft, nil
}

type connector struct {
	kind    Type
	testErr error
}

func (c connector) Type() Type { return c.kind }
func (c connector) Test(context.Context, Source) (TestResult, error) {
	return TestResult{ServerVersion: "test"}, c.testErr
}
func (c connector) Sync(context.Context, Source) (SyncResult, error) {
	return SyncResult{Assets: 3}, nil
}
func (c connector) Close(context.Context, Source) error { return nil }

type importConnector struct {
	connector
	discovered  SyncResult
	sample      SampleResult
	sampleLimit *int
}

type fileInspectConnector struct {
	connector
	inspection ExcelWorkbookInspection
	err        error
	seen       *Source
}

func (c fileInspectConnector) Inspect(_ context.Context, source Source) (ExcelWorkbookInspection, error) {
	if c.seen != nil {
		*c.seen = source
	}
	return c.inspection, c.err
}

func (c importConnector) Sync(context.Context, Source) (SyncResult, error) {
	return c.discovered, nil
}

func (c importConnector) Sample(_ context.Context, _ Source, _ MetadataTable, maxRows int) (SampleResult, error) {
	if c.sampleLimit != nil {
		*c.sampleLimit = maxRows
	}
	return c.sample, nil
}

func TestInspectFileSourceRequiresActiveInspectorAndReturnsPreview(t *testing.T) {
	inspection := ExcelWorkbookInspection{SampleLimit: 10, Sheets: []ExcelSheetInspection{{Name: "Sales", HeaderRow: 2}}}
	r := &repo{source: Source{ID: "source-1", TenantID: "tenant-1", Type: TypeExcel, Status: StatusActive, FileAssetID: "file-1"}, quota: Quota{MaxDataSources: 10, MaxExcelFileBytes: 1024}}
	service := NewService(r, fileInspectConnector{connector: connector{kind: TypeExcel}, inspection: inspection})

	result, err := service.InspectFileSource(context.Background(), "tenant-1", "source-1")
	if err != nil || len(result.Sheets) != 1 || result.Sheets[0].HeaderRow != 2 {
		t.Fatalf("inspection=%#v err=%v", result, err)
	}
	r.source.Status = StatusDraft
	if _, err := service.InspectFileSource(context.Background(), "tenant-1", "source-1"); err == nil {
		t.Fatal("draft file source was inspected")
	}
	r.source.Status, r.source.Type = StatusActive, TypeMySQL
	service = NewService(r, connector{kind: TypeMySQL})
	if _, err := service.InspectFileSource(context.Background(), "tenant-1", "source-1"); err == nil {
		t.Fatal("database source was treated as a file inspector")
	}
}

func TestInspectFileSourcePinsPublishedFileVersionWhileNewDraftWaitsForPublish(t *testing.T) {
	published := Source{
		ID: "source-1", TenantID: "tenant-1", Type: TypeExcel, Status: StatusActive,
		FileAssetID: "file-1", FileVersionID: "file-version-1",
		ConfigVersionID: "config-version-1", PublishedVersionID: "config-version-1",
		PublicationStatus: PublicationPublished,
	}
	draft := published
	draft.FileVersionID = "file-version-2"
	draft.ConfigVersionID = "config-version-2"
	draft.ValidationStatus = ValidationPassed
	draft.HasUnpublishedChanges = true
	r := &versionedRepo{
		repo:      repo{quota: Quota{MaxDataSources: 10, MaxExcelFileBytes: 1024}},
		published: published,
		draft:     draft,
	}
	var inspected Source
	service := NewService(r, fileInspectConnector{
		connector: connector{kind: TypeExcel},
		inspection: ExcelWorkbookInspection{
			SampleLimit: 10,
			Sheets:      []ExcelSheetInspection{{Name: "Published"}},
		},
		seen: &inspected,
	})

	if _, err := service.InspectFileSource(context.Background(), "tenant-1", "source-1"); err != nil {
		t.Fatal(err)
	}
	if inspected.ConfigVersionID != "config-version-1" || inspected.FileVersionID != "file-version-1" {
		t.Fatalf("inspection did not use published snapshot: %#v", inspected)
	}
}

type completingRecorder struct {
	tableID         string
	tableIDs        []string
	rows            []map[string]any
	hashes          []string
	targetTables    []bool
	targetColumnIDs [][]string
	failIDs         map[string]error
}

func (c *completingRecorder) CompleteTable(_ context.Context, _, _, tableID string, rows []map[string]any, targetTable bool, targetColumnIDs []string, structureHash, _, _ string, _ int64) error {
	c.tableID, c.rows = tableID, rows
	c.tableIDs = append(c.tableIDs, tableID)
	c.hashes = append(c.hashes, structureHash)
	c.targetTables = append(c.targetTables, targetTable)
	c.targetColumnIDs = append(c.targetColumnIDs, append([]string(nil), targetColumnIDs...))
	if c.failIDs != nil {
		return c.failIDs[tableID]
	}
	return nil
}

func TestLifecycleAndQuota(t *testing.T) {
	r := &repo{quota: Quota{MaxDataSources: 1}}
	s := NewService(r, connector{kind: TypeMySQL})
	source := Source{TenantID: "t", Code: "mysql", Name: "MySQL", Type: TypeMySQL, SecretRef: "secret://mysql"}
	created, err := s.Create(context.Background(), source)
	if err != nil || created.Status != StatusDraft {
		t.Fatal(err)
	}
	if _, err := s.Test(context.Background(), "t", "id"); err != nil || r.source.Status != StatusActive {
		t.Fatal(err)
	}
	if _, err := s.Sync(context.Background(), "t", "id"); err != nil || r.source.Status != StatusActive {
		t.Fatal(err)
	}
	if err := s.Disable(context.Background(), "t", "id"); err != nil || r.source.Status != StatusDisabled {
		t.Fatal(err)
	}
	if err := s.Enable(context.Background(), "t", "id"); err != nil || r.source.Status != StatusActive {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), "t", "id"); err != nil || r.source.Status != StatusDeleted {
		t.Fatal(err)
	}
	r.count = 1
	if _, err := s.Create(context.Background(), source); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota error=%v", err)
	}
}

func TestCreateClassifiesInvalidConfiguration(t *testing.T) {
	r := &repo{quota: Quota{MaxDataSources: 1}}
	s := NewService(r)
	if _, err := s.Create(context.Background(), Source{TenantID: "t", Type: TypeExcel}); !errors.Is(err, ErrInvalidConfiguration) {
		t.Fatalf("validation error=%v", err)
	}
}

func TestSampleTableUsesRegisteredMetadataSamplerAndCapsRows(t *testing.T) {
	r := &repo{source: Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive}, quota: Quota{MaxDataSources: 10}}
	service := NewService(r, importConnector{connector: connector{kind: TypeMySQL}, sample: SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}, {2}}}})

	result, err := service.SampleTable(context.Background(), "tenant-1", "source-1", MetadataTable{SchemaName: "sales", Name: "orders"}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 2 || result.Rows[1][0] != 2 {
		t.Fatalf("sample=%#v", result)
	}
	if _, err := service.SampleTable(context.Background(), "tenant-1", "source-1", MetadataTable{Name: "orders"}, 11); err == nil {
		t.Fatal("sample limit above ten was accepted")
	}
}

func TestLegacyImportTablesUsesTechnicalMetadataOnly(t *testing.T) {
	orders := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id"}, {Name: "amount"}}}
	customers := MetadataTable{SchemaName: "sales", Name: "customers", Columns: []MetadataColumn{{Name: "id"}}}
	tableKey := metadataTableKey(orders)
	r := &repo{
		source:      Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive},
		quota:       Quota{MaxDataSources: 10},
		selectedIDs: map[string]string{tableKey: "table-1"},
	}
	requestedLimit := 0
	connector := importConnector{
		connector:   connector{kind: TypeMySQL},
		discovered:  SyncResult{Tables: []MetadataTable{orders, customers}},
		sampleLimit: &requestedLimit,
		sample: SampleResult{
			Columns: []string{"id", "amount"},
			Rows:    [][]any{{1, 12.5}, {2, 19.0}, {3, 8.5}},
		},
	}
	completer := &completingRecorder{}
	service := NewService(r, connector)
	service.SetTableCompleter(completer)

	imported, err := service.ImportTables(context.Background(), "tenant-1", "actor-1", "source-1", []TableSelection{{SchemaName: "sales", TableName: "orders"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.selectedMetadata.Tables) != 1 || r.selectedMetadata.Tables[0].Name != "orders" {
		t.Fatalf("persisted tables=%#v", r.selectedMetadata.Tables)
	}
	if len(imported) != 1 || imported[0].ID != "table-1" || completer.tableID != "table-1" {
		t.Fatalf("imported=%#v completedTable=%s", imported, completer.tableID)
	}
	if completer.rows != nil || imported[0].Samples != nil || requestedLimit != 0 {
		t.Fatalf("legacy path leaked samples: completer=%#v imported=%#v sampleLimit=%d", completer.rows, imported[0].Samples, requestedLimit)
	}
}

func TestRefreshTablesReloadsEveryManagedTableWithoutImportingOthers(t *testing.T) {
	orders := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id"}}}
	customers := MetadataTable{SchemaName: "sales", Name: "customers", Columns: []MetadataColumn{{Name: "id"}}}
	unmanaged := MetadataTable{SchemaName: "sales", Name: "audit_log", Columns: []MetadataColumn{{Name: "id"}}}
	r := &repo{
		source:           Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive},
		quota:            Quota{MaxDataSources: 10},
		activeSelections: []TableSelection{{SchemaName: "sales", TableName: "orders"}, {SchemaName: "sales", TableName: "customers"}},
		selectedIDs: map[string]string{
			metadataTableKey(orders):    "table-orders",
			metadataTableKey(customers): "table-customers",
		},
	}
	connector := importConnector{
		connector:  connector{kind: TypeMySQL},
		discovered: SyncResult{Tables: []MetadataTable{orders, customers, unmanaged}},
		sample:     SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}, {2}, {3}}},
	}
	completer := &completingRecorder{}
	service := NewService(r, connector)
	service.SetTableCompleter(completer)

	result, err := service.RefreshTables(context.Background(), "tenant-1", "actor-1", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "SUCCEEDED" || result.Succeeded != 2 || result.Failed != 0 || len(r.selectedBatches) != 2 {
		t.Fatalf("result=%#v batches=%#v", result, r.selectedBatches)
	}
	if r.selectedBatches[0].Tables[0].Name != "orders" || r.selectedBatches[1].Tables[0].Name != "customers" {
		t.Fatalf("unexpected refresh scope=%#v", r.selectedBatches)
	}
	if len(completer.tableIDs) != 2 || completer.tableIDs[0] != "table-orders" || completer.tableIDs[1] != "table-customers" {
		t.Fatalf("completed tables=%#v", completer.tableIDs)
	}
}

func TestRefreshTablesContinuesAfterOneLLMFailure(t *testing.T) {
	orders := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id"}}}
	customers := MetadataTable{SchemaName: "sales", Name: "customers", Columns: []MetadataColumn{{Name: "id"}}}
	r := &repo{
		source:           Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive},
		quota:            Quota{MaxDataSources: 10},
		activeSelections: []TableSelection{{SchemaName: "sales", TableName: "orders"}, {SchemaName: "sales", TableName: "customers"}},
		selectedIDs: map[string]string{
			metadataTableKey(orders):    "table-orders",
			metadataTableKey(customers): "table-customers",
		},
	}
	connector := importConnector{
		connector:  connector{kind: TypeMySQL},
		discovered: SyncResult{Tables: []MetadataTable{orders, customers}},
		sample:     SampleResult{Columns: []string{"id"}, Rows: [][]any{{1}, {2}, {3}}},
	}
	completer := &completingRecorder{failIDs: map[string]error{"table-orders": errors.New("provider unavailable")}}
	service := NewService(r, connector)
	service.SetTableCompleter(completer)

	result, err := service.RefreshTables(context.Background(), "tenant-1", "actor-1", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "PARTIAL" || result.Succeeded != 1 || result.Failed != 1 || result.TechnicalUpdated != 2 {
		t.Fatalf("result=%#v", result)
	}
	if len(completer.tableIDs) != 2 || completer.tableIDs[1] != "table-customers" {
		t.Fatalf("refresh stopped before later table: %#v", completer.tableIDs)
	}
	if result.Items[0].Code != "LLM_COMPLETION_FAILED" || result.Items[1].Status != "SUCCEEDED" {
		t.Fatalf("items=%#v", result.Items)
	}
}
func TestConnectionFailureMovesToError(t *testing.T) {
	r := &repo{quota: Quota{MaxDataSources: 1}, source: Source{TenantID: "t", Type: TypeOracle, Status: StatusDraft}}
	s := NewService(r, connector{kind: TypeOracle, testErr: errors.New("offline")})
	if _, err := s.Test(context.Background(), "t", "id"); err == nil || r.source.Status != StatusError {
		t.Fatalf("status=%s err=%v", r.source.Status, err)
	}
}

func TestEnableAndSyncRequireValidatedState(t *testing.T) {
	r := &repo{quota: Quota{MaxDataSources: 1}, source: Source{TenantID: "t", Type: TypeMySQL, Status: StatusDraft}}
	s := NewService(r, connector{kind: TypeMySQL})
	if err := s.Enable(context.Background(), "t", "id"); err == nil {
		t.Fatal("draft source was enabled without connection test")
	}
	if _, err := s.Sync(context.Background(), "t", "id"); err == nil {
		t.Fatal("draft source was synchronized")
	}
	r.source.Status = StatusError
	if err := s.Enable(context.Background(), "t", "id"); err == nil {
		t.Fatal("error source was enabled without retest")
	}
	if _, err := s.Sync(context.Background(), "t", "id"); err == nil {
		t.Fatal("error source was synchronized without retest")
	}
	r.source.Status = StatusDisabled
	if err := s.Enable(context.Background(), "t", "id"); err != nil || r.source.Status != StatusActive {
		t.Fatalf("disabled source enable failed: %v", err)
	}
}

func TestUpdateKeepsCurrentSecretWhenPasswordIsNotReentered(t *testing.T) {
	r := &repo{source: Source{ID: "source-1", TenantID: "t", Code: "old", Name: "Old", Type: TypeMySQL, Status: StatusActive, SecretRef: "encrypted://current", Version: 3}}
	s := NewService(r, connector{kind: TypeMySQL})
	updated, err := s.Update(context.Background(), Source{ID: "source-1", TenantID: "t", Code: "new", Name: "New", Type: TypeMySQL, Config: map[string]any{"host": "db"}, Version: 3})
	if err != nil || updated.SecretRef != "encrypted://current" || updated.Status != StatusDraft {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
}

func TestUpdateRejectsStaleExpectedVersion(t *testing.T) {
	r := &repo{source: Source{
		ID: "source-1", TenantID: "t", Code: "current", Name: "Current",
		Type: TypeMySQL, Status: StatusActive, SecretRef: "encrypted://current", Version: 4,
	}}
	s := NewService(r, connector{kind: TypeMySQL})

	_, err := s.Update(context.Background(), Source{
		ID: "source-1", TenantID: "t", Code: "stale", Name: "Stale",
		Type: TypeMySQL, Config: map[string]any{"host": "db"}, Version: 3,
	})
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("err=%v want=%v", err, ErrVersionConflict)
	}
	if r.source.Code != "current" {
		t.Fatalf("stale edit overwrote source: %#v", r.source)
	}
}

func TestVersionedConnectionTestBindsDraftAndRequiresExplicitPublish(t *testing.T) {
	fixedNow := time.Date(2026, 7, 24, 8, 0, 0, 0, time.UTC)
	draft := Source{
		ID: "source-1", TenantID: "tenant-1", Code: "sales", Name: "Sales", Type: TypeMySQL,
		Status: StatusDraft, SecretRef: "encrypted://draft", Config: map[string]any{"host": "draft-db"},
		ConfigVersionID: "draft-version-2", ConfigVersion: 2,
		ValidationStatus: ValidationUntested, PublicationStatus: PublicationUnpublished,
	}
	draft.ConfigHash, _ = sourceConfigurationHash(draft)
	r := &versionedRepo{repo: repo{quota: Quota{MaxDataSources: 10}}, draft: draft}
	service := NewService(r, connector{kind: TypeMySQL})
	service.now = func() time.Time { return fixedNow }

	result, err := service.Test(context.Background(), draft.TenantID, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.draft.Status != StatusDraft || r.draft.ValidationStatus != ValidationPassed || len(r.testRuns) != 1 {
		t.Fatalf("draft=%#v runs=%#v", r.draft, r.testRuns)
	}
	if result.ConfigVersionID != draft.ConfigVersionID || result.ConfigHash != draft.ConfigHash ||
		result.ExpiresAt == nil || !result.ExpiresAt.Equal(fixedNow.Add(30*time.Minute)) {
		t.Fatalf("test result=%#v", result)
	}

	published, err := service.Publish(context.Background(), draft.TenantID, "actor-1", draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != StatusActive || published.PublishedVersionID != draft.ConfigVersionID ||
		published.PublicationStatus != PublicationPublished || published.HasUnpublishedChanges {
		t.Fatalf("published=%#v", published)
	}
}

func TestVersionedDraftEditAndFailedTestKeepPublishedRuntime(t *testing.T) {
	published := Source{
		ID: "source-1", TenantID: "tenant-1", Code: "sales", Name: "Sales", Type: TypeMySQL,
		Status: StatusActive, SecretRef: "encrypted://published", Config: map[string]any{"host": "online-db"},
		ConfigVersionID: "published-version-1", PublishedVersionID: "published-version-1",
		ConfigVersion: 1, PublishedConfigVersion: 1, Version: 1,
		ValidationStatus: ValidationPassed, PublicationStatus: PublicationPublished,
	}
	published.ConfigHash, _ = sourceConfigurationHash(published)
	r := &versionedRepo{repo: repo{quota: Quota{MaxDataSources: 10}}, draft: published, published: published}
	service := NewService(r, connector{kind: TypeMySQL, testErr: errors.New("draft database is offline")})

	updated, err := service.Update(context.Background(), Source{
		ID: published.ID, TenantID: published.TenantID, Code: published.Code, Name: published.Name,
		Type: TypeMySQL, SecretRef: "encrypted://draft", Config: map[string]any{"host": "draft-db"},
		Version: published.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.HasUnpublishedChanges || updated.Status != StatusActive {
		t.Fatalf("updated=%#v", updated)
	}
	if _, err := service.Test(context.Background(), published.TenantID, published.ID); err == nil {
		t.Fatal("failed draft connection test unexpectedly succeeded")
	}
	runtime, err := r.Get(context.Background(), published.TenantID, published.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Config["host"] != "online-db" || runtime.Status != StatusActive ||
		r.draft.ValidationStatus != ValidationFailed {
		t.Fatalf("runtime=%#v draft=%#v", runtime, r.draft)
	}
}

func TestVersionedPublishRejectsExpiredTest(t *testing.T) {
	fixedNow := time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC)
	expiredAt := fixedNow.Add(-time.Minute)
	draft := Source{
		ID: "source-1", TenantID: "tenant-1", Code: "sales", Name: "Sales", Type: TypeMySQL,
		Status: StatusDraft, SecretRef: "encrypted://draft", Config: map[string]any{"host": "draft-db"},
		ConfigVersionID: "draft-version-1", ConfigVersion: 1,
		ValidationStatus: ValidationPassed, TestExpiresAt: &expiredAt,
	}
	draft.ConfigHash, _ = sourceConfigurationHash(draft)
	r := &versionedRepo{
		repo:  repo{quota: Quota{MaxDataSources: 10}},
		draft: draft,
		testRuns: []ConnectionTestRun{{
			ConfigVersion: draft.ConfigVersionID, ConfigHash: draft.ConfigHash,
			Status: ValidationPassed, ExpiresAt: &expiredAt,
		}},
	}
	service := NewService(r, connector{kind: TypeMySQL})
	service.now = func() time.Time { return fixedNow }
	if _, err := service.Publish(context.Background(), draft.TenantID, "actor-1", draft.ID); !errors.Is(err, ErrTestExpired) {
		t.Fatalf("publish error=%v", err)
	}
}

func TestMetadataStructureHashIgnoresEstimatedRows(t *testing.T) {
	one, two := int64(1), int64(999)
	table := MetadataTable{SchemaName: "s", Name: "orders", EstimatedRowCount: &one, Columns: []MetadataColumn{{Name: "id", CanonicalType: "NUMBER"}}}
	first, _, err := metadataTableHash(table)
	if err != nil {
		t.Fatal(err)
	}
	table.EstimatedRowCount = &two
	second, _, err := metadataTableHash(table)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("estimated row count changed the structure hash")
	}
}
