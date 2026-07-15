package datasource

import (
	"context"
	"errors"
	"testing"
)

type repo struct {
	source   Source
	count    int
	quota    Quota
	statuses []Status
}

func (r *repo) Count(context.Context, string) (int, error)                       { return r.count, nil }
func (r *repo) Create(_ context.Context, s Source) (Source, error)               { r.source = s; return s, nil }
func (r *repo) List(context.Context, string) ([]Source, error)                   { return []Source{r.source}, nil }
func (r *repo) Get(context.Context, string, string) (Source, error)              { return r.source, nil }
func (r *repo) Update(_ context.Context, s Source) (Source, error)               { r.source = s; return s, nil }
func (r *repo) ApplyMetadata(context.Context, Source, SyncResult) error          { return nil }
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
	if _, err := s.Create(context.Background(), source); err == nil {
		t.Fatal("quota was not enforced")
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
