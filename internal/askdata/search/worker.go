package search

import (
	"context"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/embedding"
)

const MaxEmbeddingBatchSize = 16

var ErrInvalidEmbeddingWork = errors.New("askdata embedding work is invalid")

type EmbeddingClaim struct {
	ID, TenantID, DomainID, SearchDocumentID string
	InputHash, Text, LeaseToken              string
	Attempt, MaxAttempts                     int
	Current                                  bool
}

type EmbeddingStore interface {
	ListTenantIDs(context.Context) ([]string, error)
	ClaimBatch(context.Context, string, string, string, time.Duration, int) ([]EmbeddingClaim, error)
	Acknowledge(context.Context, EmbeddingClaim, string) error
	Complete(context.Context, EmbeddingClaim, string, string, []float32) error
	Fail(context.Context, EmbeddingClaim, string, string) error
}

type EmbeddingWorker struct {
	store    EmbeddingStore
	provider embedding.Provider
}

func NewEmbeddingWorker(store EmbeddingStore, provider embedding.Provider) *EmbeddingWorker {
	return &EmbeddingWorker{store: store, provider: provider}
}

func (worker *EmbeddingWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, ErrInvalidEmbeddingWork
	}
	return worker.store.ListTenantIDs(ctx)
}

func (worker *EmbeddingWorker) ProcessNext(
	ctx context.Context, tenantID, workerID string, lease time.Duration,
) (int, error) {
	if worker == nil || worker.store == nil || worker.provider == nil || !worker.provider.Configured() {
		return 0, nil
	}
	claims, err := worker.store.ClaimBatch(
		ctx, tenantID, workerID, worker.provider.Model(), lease, MaxEmbeddingBatchSize,
	)
	if err != nil || len(claims) == 0 {
		return 0, err
	}
	documents := make([]EmbeddingClaim, 0, len(claims))
	texts := make([]string, 0, len(claims))
	var combined error
	for _, claim := range claims {
		if claim.Current {
			combined = errors.Join(combined, worker.store.Acknowledge(ctx, claim, workerID))
			continue
		}
		documents = append(documents, claim)
		texts = append(texts, claim.Text)
	}
	if len(documents) == 0 {
		return len(claims), combined
	}
	vectors, embedErr := worker.provider.Embed(ctx, texts)
	if embedErr != nil || len(vectors) != len(documents) {
		code := "EMBEDDING_PROVIDER_UNAVAILABLE"
		if errors.Is(embedErr, embedding.ErrInvalidRequest) || errors.Is(embedErr, embedding.ErrInvalidResponse) ||
			(embedErr == nil && len(vectors) != len(documents)) {
			code = "EMBEDDING_INVALID_RESPONSE"
		}
		for _, document := range documents {
			combined = errors.Join(combined, worker.store.Fail(ctx, document, workerID, code))
		}
		return len(claims), errors.Join(combined, embedErr)
	}
	for index, document := range documents {
		if len(vectors[index]) != 2_560 {
			combined = errors.Join(combined, worker.store.Fail(ctx, document, workerID, "EMBEDDING_DIMENSION_MISMATCH"))
			continue
		}
		combined = errors.Join(combined, worker.store.Complete(
			ctx, document, workerID, worker.provider.Model(), vectors[index],
		))
	}
	return len(claims), combined
}
