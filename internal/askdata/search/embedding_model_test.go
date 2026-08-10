package search

import (
	"context"
	"errors"
	"testing"
)

func TestEmbeddingCompletionRejectsMixedModelAndDimensionBeforeWrite(t *testing.T) {
	store := &PostgresEmbeddingStore{}
	claim := EmbeddingClaim{
		ExpectedModel: "Qwen3-Embedding-4B", ExpectedDimension: SearchEmbeddingDimension,
	}
	for name, run := range map[string]func() error{
		"model": func() error {
			return store.Complete(
				context.Background(), claim, "worker", "other-model",
				make([]float32, SearchEmbeddingDimension),
			)
		},
		"dimension": func() error {
			return store.Complete(
				context.Background(), claim, "worker", claim.ExpectedModel,
				make([]float32, SearchEmbeddingDimension-1),
			)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); !errors.Is(err, ErrEmbeddingModelMismatch) ||
				err.Error() != "SEARCH_EMBEDDING_MODEL_MISMATCH" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
