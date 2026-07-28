package metricsemantic

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"intelligent-report-generation-system/internal/embedding"
)

type Service struct {
	store    Store
	provider embedding.Provider
}

func NewService(store Store, provider embedding.Provider) *Service {
	return &Service{store: store, provider: provider}
}

func (s *Service) Search(ctx context.Context, tenantID, query string, limit int) (SearchResponse, error) {
	query = strings.TrimSpace(query)
	if s == nil || s.store == nil || tenantID == "" || !validSearchText(query) || limit < 1 || limit > 50 {
		return SearchResponse{}, ErrInvalidRequest
	}
	var vector []float32
	degraded := true
	model, dimensions := "", 0
	if s.provider != nil && s.provider.Configured() {
		model, dimensions = s.provider.Model(), s.provider.Dimensions()
		vectors, err := s.provider.Embed(ctx, []string{query})
		if err == nil && len(vectors) == 1 {
			vector = vectors[0]
			degraded = false
		}
	}
	items, err := s.store.Search(ctx, tenantID, query, vector, limit)
	if err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{Items: items, Degraded: degraded, Model: model, Dimensions: dimensions}, nil
}

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
	return w.store.ListPendingTenantIDs(ctx)
}

func (w *Worker) ProcessNext(ctx context.Context, tenantID, workerID string, lease time.Duration) (bool, error) {
	if w == nil || w.store == nil || w.provider == nil || !w.provider.Configured() {
		return false, nil
	}
	claims := make([]EmbeddingClaim, 0, 32)
	for len(claims) < cap(claims) {
		claim, err := w.store.Claim(ctx, tenantID, workerID, lease)
		if err != nil {
			return len(claims) > 0, err
		}
		if claim == nil {
			break
		}
		claims = append(claims, *claim)
	}
	if len(claims) == 0 {
		return false, nil
	}
	documents := make([]string, len(claims))
	for index := range claims {
		documents[index] = claims[index].Document
	}
	vectors, embedErr := w.provider.Embed(ctx, documents)
	if embedErr == nil && len(vectors) != len(claims) {
		embedErr = embedding.ErrInvalidResponse
	}
	if embedErr == nil {
		var completionErr error
		for index := range claims {
			if err := w.store.Complete(
				ctx, claims[index], workerID, w.provider.Model(),
				vectors[index],
			); err != nil {
				completionErr = errors.Join(completionErr, err)
			}
		}
		if completionErr == nil {
			return true, nil
		}
		return true, completionErr
	}

	code := "EMBEDDING_PROVIDER_UNAVAILABLE"
	if errors.Is(embedErr, embedding.ErrInvalidRequest) || errors.Is(embedErr, embedding.ErrInvalidResponse) {
		code = "EMBEDDING_INVALID_RESPONSE"
	}
	var failErr error
	for index := range claims {
		if err := w.store.Fail(
			ctx, claims[index], workerID, code,
		); err != nil {
			failErr = errors.Join(failErr, err)
		}
	}
	return true, errors.Join(embedErr, failErr)
}

func validSearchText(value string) bool {
	if len([]rune(value)) < 1 || len([]rune(value)) > 1000 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
