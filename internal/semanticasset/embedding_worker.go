package semanticasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/embedding"
)

type EmbeddingClaim struct {
	ID           string
	TenantID     string
	AssetID      string
	EventVersion int64
	Attempt      int
}

type EmbeddingDocument struct {
	EmbeddingClaim
	Text           string
	InputHash      string
	Current        bool
	Eligible       bool
	IneligibleCode string
}

type EmbeddingStore interface {
	ListTenantIDs(context.Context) ([]string, error)
	ClaimEmbeddingBatch(
		context.Context, string, string, time.Duration, int,
	) ([]EmbeddingClaim, error)
	PrepareEmbedding(
		context.Context, EmbeddingClaim, string, string,
	) (EmbeddingDocument, error)
	AcknowledgeEmbedding(
		context.Context, EmbeddingDocument, string,
	) error
	CompleteEmbedding(
		context.Context, EmbeddingDocument, string, string, []float32,
	) error
	SkipEmbedding(
		context.Context, EmbeddingDocument, string,
	) error
	FailEmbedding(
		context.Context, EmbeddingDocument, string, string,
	) error
}

type EmbeddingWorker struct {
	store    EmbeddingStore
	provider embedding.Provider
}

func NewEmbeddingWorker(
	store EmbeddingStore,
	provider embedding.Provider,
) *EmbeddingWorker {
	return &EmbeddingWorker{store: store, provider: provider}
}

func (worker *EmbeddingWorker) TenantIDs(
	ctx context.Context,
) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, ErrInvalidRequest
	}
	return worker.store.ListTenantIDs(ctx)
}

func (worker *EmbeddingWorker) ProcessNext(
	ctx context.Context,
	tenantID string,
	workerID string,
	lease time.Duration,
) (int, error) {
	if worker == nil || worker.store == nil || worker.provider == nil ||
		!worker.provider.Configured() {
		return 0, nil
	}
	if worker.provider.Dimensions() != VectorDimensions ||
		strings.TrimSpace(worker.provider.Model()) == "" {
		return 0, ErrInvalidRequest
	}
	claims, err := worker.store.ClaimEmbeddingBatch(
		ctx, tenantID, workerID, lease, MaxBatchSize,
	)
	if err != nil || len(claims) == 0 {
		return 0, err
	}
	documents := make([]EmbeddingDocument, 0, len(claims))
	texts := make([]string, 0, len(claims))
	var combined error
	for _, claim := range claims {
		document, prepareErr := worker.store.PrepareEmbedding(
			ctx, claim, workerID, worker.provider.Model(),
		)
		if prepareErr != nil {
			document = EmbeddingDocument{EmbeddingClaim: claim}
			combined = errors.Join(
				combined, prepareErr,
				worker.store.FailEmbedding(
					ctx, document, workerID, "SEMANTIC_TERM_DOCUMENT_FAILED",
				),
			)
			continue
		}
		if !document.Eligible {
			combined = errors.Join(
				combined,
				worker.store.SkipEmbedding(ctx, document, workerID),
			)
			continue
		}
		if document.Current {
			combined = errors.Join(
				combined,
				worker.store.AcknowledgeEmbedding(ctx, document, workerID),
			)
			continue
		}
		documents = append(documents, document)
		texts = append(texts, document.Text)
	}
	if len(documents) == 0 {
		return len(claims), combined
	}
	vectors, embedErr := worker.provider.Embed(ctx, texts)
	if embedErr != nil || !validVectors(vectors, len(documents)) {
		code := "EMBEDDING_PROVIDER_UNAVAILABLE"
		if errors.Is(embedErr, embedding.ErrInvalidRequest) ||
			errors.Is(embedErr, embedding.ErrInvalidResponse) ||
			(embedErr == nil && !validVectors(vectors, len(documents))) {
			code = "EMBEDDING_INVALID_RESPONSE"
		}
		for _, document := range documents {
			combined = errors.Join(
				combined,
				worker.store.FailEmbedding(
					ctx, document, workerID, code,
				),
			)
		}
		return len(claims), errors.Join(combined, embedErr)
	}
	for index, document := range documents {
		combined = errors.Join(
			combined,
			worker.store.CompleteEmbedding(
				ctx, document, workerID,
				worker.provider.Model(), vectors[index],
			),
		)
	}
	return len(claims), combined
}

func semanticTermInputHash(term string) string {
	sum := sha256.Sum256([]byte(term))
	return hex.EncodeToString(sum[:])
}

func validVectors(vectors [][]float32, expected int) bool {
	if len(vectors) != expected {
		return false
	}
	for _, vector := range vectors {
		if len(vector) != VectorDimensions {
			return false
		}
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return false
			}
		}
	}
	return true
}

func formatVector(vector []float32) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range vector {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(
			strconv.FormatFloat(float64(value), 'g', -1, 32),
		)
	}
	builder.WriteByte(']')
	return builder.String()
}
