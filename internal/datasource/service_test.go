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
	discovered SyncResult
	sample     SampleResult
}

func (c importConnector) Sync(context.Context, Source) (SyncResult, error) {
	return c.discovered, nil
}

func (c importConnector) Sample(context.Context, Source, MetadataTable, int) (SampleResult, error) {
	return c.sample, nil
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
	if _, err := service.SampleTable(context.Background(), "tenant-1", "source-1", MetadataTable{Name: "orders"}, 6); err == nil {
		t.Fatal("sample limit above five was accepted")
	}
}

func TestImportTablesPersistsOnlySelectionAndCompletesWithThreeSamples(t *testing.T) {
	orders := MetadataTable{SchemaName: "sales", Name: "orders", Columns: []MetadataColumn{{Name: "id"}, {Name: "amount"}}}
	customers := MetadataTable{SchemaName: "sales", Name: "customers", Columns: []MetadataColumn{{Name: "id"}}}
	tableKey := metadataTableKey(orders)
	r := &repo{
		source:      Source{ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL, Status: StatusActive},
		quota:       Quota{MaxDataSources: 10},
		selectedIDs: map[string]string{tableKey: "table-1"},
	}
	connector := importConnector{
		connector:  connector{kind: TypeMySQL},
		discovered: SyncResult{Tables: []MetadataTable{orders, customers}},
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
	if len(completer.rows) != 3 || completer.rows[0]["id"] != 1 || completer.rows[2]["amount"] != 8.5 {
		t.Fatalf("sample rows=%#v", completer.rows)
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
	r := &repo{source: Source{ID: "source-1", TenantID: "t", Code: "old", Name: "Old", Type: TypeMySQL, Status: StatusActive, SecretRef: "encrypted://current"}}
	s := NewService(r, connector{kind: TypeMySQL})
	updated, err := s.Update(context.Background(), Source{ID: "source-1", TenantID: "t", Code: "new", Name: "New", Type: TypeMySQL, Config: map[string]any{"host": "db"}})
	if err != nil || updated.SecretRef != "encrypted://current" || updated.Status != StatusDraft {
		t.Fatalf("updated=%#v err=%v", updated, err)
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
