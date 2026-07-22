package assetembedding

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type workerStore struct {
	claims       []Claim
	documents    map[string]Document
	acknowledged []string
	completed    []string
	skipped      []string
	failed       []string
}

func (s *workerStore) ListTenantIDs(context.Context) ([]string, error) {
	return []string{"tenant"}, nil
}
func (s *workerStore) ClaimBatch(context.Context, string, string, time.Duration, int) ([]Claim, error) {
	return append([]Claim(nil), s.claims...), nil
}
func (s *workerStore) Prepare(_ context.Context, claim Claim, _ string) (Document, error) {
	document := s.documents[claim.AssetID]
	document.Claim = claim
	return document, nil
}
func (s *workerStore) Acknowledge(_ context.Context, document Document, _ string) error {
	s.acknowledged = append(s.acknowledged, document.AssetID)
	return nil
}
func (s *workerStore) Complete(_ context.Context, document Document, _, _ string, _ []float32) error {
	s.completed = append(s.completed, document.AssetID)
	return nil
}
func (s *workerStore) Skip(_ context.Context, document Document, _ string) error {
	s.skipped = append(s.skipped, document.AssetID)
	return nil
}
func (s *workerStore) Fail(_ context.Context, document Document, _, code string) error {
	s.failed = append(s.failed, document.AssetID+":"+code)
	return nil
}
func (s *workerStore) VectorRanks(context.Context, string, string, []string, []float32, int) ([]Rank, error) {
	return nil, nil
}
func (s *workerStore) LexicalRanks(context.Context, string, string, []string, string, []string, int) ([]Rank, error) {
	return nil, nil
}

type workerProvider struct {
	inputs []string
	err    error
}

func (p *workerProvider) Configured() bool { return true }
func (p *workerProvider) Model() string    { return "embedding-test" }
func (p *workerProvider) Dimensions() int  { return 2 }
func (p *workerProvider) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	p.inputs = append([]string(nil), inputs...)
	if p.err != nil {
		return nil, p.err
	}
	result := make([][]float32, len(inputs))
	for index := range result {
		result[index] = []float32{0.1, 0.2}
	}
	return result, nil
}

func TestWorkerBatchesOnlyChangedEligibleDocuments(t *testing.T) {
	store := &workerStore{
		claims: []Claim{{AssetID: "changed"}, {AssetID: "current"}, {AssetID: "inactive"}},
		documents: map[string]Document{
			"changed":  {Eligible: true, Text: "changed document", InputHash: "hash-changed"},
			"current":  {Eligible: true, Current: true, Text: "current document", InputHash: "hash-current"},
			"inactive": {IneligibleCode: "ASSET_NOT_ELIGIBLE"},
		},
	}
	provider := &workerProvider{}
	processed, err := NewWorker(store, provider).ProcessNext(context.Background(), "tenant", "worker", time.Minute)
	if err != nil || processed != 3 {
		t.Fatalf("ProcessNext() processed/error = %d/%v", processed, err)
	}
	if !reflect.DeepEqual(provider.inputs, []string{"changed document"}) ||
		!reflect.DeepEqual(store.completed, []string{"changed"}) ||
		!reflect.DeepEqual(store.acknowledged, []string{"current"}) ||
		!reflect.DeepEqual(store.skipped, []string{"inactive"}) {
		t.Fatalf("inputs/completed/acknowledged/skipped = %#v/%#v/%#v/%#v",
			provider.inputs, store.completed, store.acknowledged, store.skipped)
	}
}

func TestWorkerRecordsProviderFailureWithoutBlockingMetadata(t *testing.T) {
	store := &workerStore{
		claims:    []Claim{{AssetID: "table"}},
		documents: map[string]Document{"table": {Eligible: true, Text: "table document", InputHash: "hash"}},
	}
	provider := &workerProvider{err: errors.New("provider unavailable")}
	processed, err := NewWorker(store, provider).ProcessNext(context.Background(), "tenant", "worker", time.Minute)
	if processed != 1 || err == nil || !reflect.DeepEqual(store.failed, []string{"table:EMBEDDING_PROVIDER_UNAVAILABLE"}) {
		t.Fatalf("processed/error/failed = %d/%v/%#v", processed, err, store.failed)
	}
}
