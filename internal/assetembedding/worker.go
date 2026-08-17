package assetembedding

import (
	"context"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/embedding"
)

type Worker struct {
	store    Store
	provider embedding.Provider
}

func NewWorker(store Store, provider embedding.Provider) *Worker {
	return &Worker{store: store, provider: provider}
}

func (w *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	if w == nil || w.store == nil {
		return nil, ErrInvalidRequest
	}
	return w.store.ListTenantIDs(ctx)
}

// RequeueStaleDocuments makes a document-format rollout self-healing. A deploy
// may apply its migration while the previous worker image is still draining;
// startup reconciliation guarantees those assets are rebuilt by the new code.
func (w *Worker) RequeueStaleDocuments(ctx context.Context) error {
	requeuer, ok := w.store.(interface {
		RequeueStaleDocuments(context.Context, string) error
	})
	if !ok {
		return nil
	}
	tenantIDs, err := w.store.ListTenantIDs(ctx)
	if err != nil {
		return err
	}
	for _, tenantID := range tenantIDs {
		if err := requeuer.RequeueStaleDocuments(ctx, tenantID); err != nil {
			return err
		}
	}
	return nil
}

// ProcessNext claims and embeds one bounded batch. Metadata transactions never wait on this call.
func (w *Worker) ProcessNext(ctx context.Context, tenantID, workerID string, lease time.Duration) (int, error) {
	if w == nil || w.store == nil || w.provider == nil || !w.provider.Configured() {
		return 0, nil
	}
	claims, err := w.store.ClaimBatch(ctx, tenantID, workerID, lease, MaxBatchSize)
	if err != nil || len(claims) == 0 {
		return 0, err
	}
	documents := make([]Document, 0, len(claims))
	texts := make([]string, 0, len(claims))
	var combined error
	for _, claim := range claims {
		document, prepareErr := w.store.Prepare(ctx, claim, w.provider.Model())
		if prepareErr != nil {
			document = Document{Claim: claim}
			combined = errors.Join(combined, prepareErr, w.store.Fail(ctx, document, workerID, "ASSET_DOCUMENT_FAILED"))
			continue
		}
		if !document.Eligible {
			combined = errors.Join(combined, w.store.Skip(ctx, document, workerID))
			continue
		}
		if document.Current {
			combined = errors.Join(combined, w.store.Acknowledge(ctx, document, workerID))
			continue
		}
		documents = append(documents, document)
		texts = append(texts, document.Text)
	}
	if len(documents) == 0 {
		return len(claims), combined
	}
	vectors, embedErr := w.provider.Embed(ctx, texts)
	if embedErr != nil || len(vectors) != len(documents) {
		code := "EMBEDDING_PROVIDER_UNAVAILABLE"
		if errors.Is(embedErr, embedding.ErrInvalidRequest) || errors.Is(embedErr, embedding.ErrInvalidResponse) ||
			(embedErr == nil && len(vectors) != len(documents)) {
			code = "EMBEDDING_INVALID_RESPONSE"
		}
		for _, document := range documents {
			combined = errors.Join(combined, w.store.Fail(ctx, document, workerID, code))
		}
		return len(claims), errors.Join(combined, embedErr)
	}
	for index, document := range documents {
		combined = errors.Join(combined, w.store.Complete(ctx, document, workerID, w.provider.Model(), vectors[index]))
	}
	return len(claims), combined
}
