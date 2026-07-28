package semanticasset

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type embeddingWorkerStore struct {
	claims    []EmbeddingClaim
	documents map[string]EmbeddingDocument
	completed []string
}

func (store *embeddingWorkerStore) ListTenantIDs(
	context.Context,
) ([]string, error) {
	return []string{"tenant"}, nil
}

func (store *embeddingWorkerStore) ClaimEmbeddingBatch(
	context.Context, string, string, time.Duration, int,
) ([]EmbeddingClaim, error) {
	return append([]EmbeddingClaim(nil), store.claims...), nil
}

func (store *embeddingWorkerStore) PrepareEmbedding(
	_ context.Context,
	claim EmbeddingClaim,
	_ string,
	_ string,
) (EmbeddingDocument, error) {
	document := store.documents[claim.AssetID]
	document.EmbeddingClaim = claim
	return document, nil
}

func (store *embeddingWorkerStore) AcknowledgeEmbedding(
	context.Context, EmbeddingDocument, string,
) error {
	return nil
}

func (store *embeddingWorkerStore) CompleteEmbedding(
	_ context.Context,
	document EmbeddingDocument,
	_ string,
	_ string,
	_ []float32,
) error {
	store.completed = append(store.completed, document.AssetID)
	return nil
}

func (store *embeddingWorkerStore) SkipEmbedding(
	context.Context, EmbeddingDocument, string,
) error {
	return nil
}

func (store *embeddingWorkerStore) FailEmbedding(
	context.Context, EmbeddingDocument, string, string,
) error {
	return nil
}

type semanticEmbeddingProvider struct{ inputs []string }

func (provider *semanticEmbeddingProvider) Configured() bool { return true }
func (provider *semanticEmbeddingProvider) Model() string    { return "embedding-test" }
func (provider *semanticEmbeddingProvider) Dimensions() int  { return VectorDimensions }
func (provider *semanticEmbeddingProvider) Embed(
	_ context.Context,
	inputs []string,
) ([][]float32, error) {
	provider.inputs = append([]string(nil), inputs...)
	result := make([][]float32, len(inputs))
	for index := range result {
		result[index] = make([]float32, VectorDimensions)
	}
	return result, nil
}

func TestEmbeddingWorkerSendsOnlyCommonTerm(t *testing.T) {
	store := &embeddingWorkerStore{
		claims: []EmbeddingClaim{{AssetID: "asset-1"}},
		documents: map[string]EmbeddingDocument{
			"asset-1": {
				Eligible:  true,
				Text:      "财资",
				InputHash: semanticTermInputHash("财资"),
			},
		},
	}
	provider := &semanticEmbeddingProvider{}
	processed, err := NewEmbeddingWorker(store, provider).ProcessNext(
		context.Background(), "tenant", "worker", time.Minute,
	)
	if err != nil || processed != 1 {
		t.Fatalf("ProcessNext() processed/error = %d/%v", processed, err)
	}
	if !reflect.DeepEqual(provider.inputs, []string{"财资"}) ||
		!reflect.DeepEqual(store.completed, []string{"asset-1"}) {
		t.Fatalf(
			"provider inputs/completed = %#v/%#v",
			provider.inputs, store.completed,
		)
	}
}
