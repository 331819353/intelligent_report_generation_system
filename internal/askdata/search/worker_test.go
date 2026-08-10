package search

import (
	"context"
	"errors"
	"testing"
	"time"
)

type embeddingProviderFixture struct {
	configured bool
	model      string
	dimension  int
	vectors    [][]float32
	err        error
	inputs     []string
}

func (provider *embeddingProviderFixture) Configured() bool { return provider.configured }
func (provider *embeddingProviderFixture) Model() string    { return provider.model }
func (provider *embeddingProviderFixture) Dimensions() int {
	if provider.dimension == 0 {
		return SearchEmbeddingDimension
	}
	return provider.dimension
}
func (provider *embeddingProviderFixture) Embed(_ context.Context, input []string) ([][]float32, error) {
	provider.inputs = append([]string(nil), input...)
	return provider.vectors, provider.err
}

type embeddingStoreFixture struct {
	claims       []EmbeddingClaim
	acknowledged []string
	completed    []string
	failed       map[string]string
}

func (store *embeddingStoreFixture) ListTenantIDs(context.Context) ([]string, error) {
	return []string{"tenant-1"}, nil
}
func (store *embeddingStoreFixture) ClaimBatch(context.Context, string, string, string, time.Duration, int) ([]EmbeddingClaim, error) {
	return store.claims, nil
}
func (store *embeddingStoreFixture) Acknowledge(_ context.Context, claim EmbeddingClaim, _ string) error {
	store.acknowledged = append(store.acknowledged, claim.ID)
	return nil
}
func (store *embeddingStoreFixture) Complete(_ context.Context, claim EmbeddingClaim, _, _ string, _ []float32) error {
	store.completed = append(store.completed, claim.ID)
	return nil
}
func (store *embeddingStoreFixture) Fail(_ context.Context, claim EmbeddingClaim, _, code string) error {
	if store.failed == nil {
		store.failed = map[string]string{}
	}
	store.failed[claim.ID] = code
	return nil
}

func TestEmbeddingWorkerBatchesCurrentAndNewDocuments(t *testing.T) {
	store := &embeddingStoreFixture{claims: []EmbeddingClaim{
		{ID: "event-current", Current: true},
		{ID: "event-new", Text: "type=metric | name=销售额"},
	}}
	provider := &embeddingProviderFixture{
		configured: true, model: "Qwen3-Embedding-4B",
		vectors: [][]float32{make([]float32, 2_560)},
	}
	count, err := NewEmbeddingWorker(store, provider).ProcessNext(
		context.Background(), "tenant-1", "worker-1", time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(store.acknowledged) != 1 || len(store.completed) != 1 ||
		len(provider.inputs) != 1 || provider.inputs[0] != store.claims[1].Text {
		t.Fatalf("worker result count=%d store=%#v inputs=%#v", count, store, provider.inputs)
	}
}

func TestEmbeddingWorkerFailsClosedOnProviderAndDimensionErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider *embeddingProviderFixture
		wantCode string
	}{
		{
			name:     "provider unavailable",
			provider: &embeddingProviderFixture{configured: true, model: "Qwen3-Embedding-4B", err: errors.New("offline")},
			wantCode: "EMBEDDING_PROVIDER_UNAVAILABLE",
		},
		{
			name:     "dimension mismatch",
			provider: &embeddingProviderFixture{configured: true, model: "Qwen3-Embedding-4B", vectors: [][]float32{{1, 2}}},
			wantCode: "EMBEDDING_DIMENSION_MISMATCH",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &embeddingStoreFixture{claims: []EmbeddingClaim{{ID: "event-1", Text: "document"}}}
			_, _ = NewEmbeddingWorker(store, test.provider).ProcessNext(
				context.Background(), "tenant-1", "worker-1", time.Minute,
			)
			if store.failed["event-1"] != test.wantCode {
				t.Fatalf("failure code = %q", store.failed["event-1"])
			}
		})
	}
}

func TestEmbeddingWorkerRejectsConfiguredModelDimensionMismatchBeforeClaim(t *testing.T) {
	store := &embeddingStoreFixture{}
	provider := &embeddingProviderFixture{
		configured: true, model: "Qwen3-Embedding-4B", dimension: 1_536,
	}
	if _, err := NewEmbeddingWorker(store, provider).ProcessNext(
		context.Background(), "tenant-1", "worker-1", time.Minute,
	); !errors.Is(err, ErrEmbeddingModelMismatch) {
		t.Fatalf("error = %v", err)
	}
}
