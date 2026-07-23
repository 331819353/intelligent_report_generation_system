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
	claim, err := w.store.Claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	vectors, embedErr := w.provider.Embed(ctx, []string{claim.Document})
	if embedErr == nil && len(vectors) == 1 {
		embedErr = w.store.Complete(ctx, *claim, workerID, w.provider.Model(), vectors[0])
	}
	if embedErr == nil {
		return true, nil
	}
	code := "EMBEDDING_PROVIDER_UNAVAILABLE"
	if errors.Is(embedErr, embedding.ErrInvalidRequest) || errors.Is(embedErr, embedding.ErrInvalidResponse) {
		code = "EMBEDDING_INVALID_RESPONSE"
	}
	failErr := w.store.Fail(ctx, *claim, workerID, code)
	if failErr != nil {
		return true, errors.Join(embedErr, failErr)
	}
	return true, embedErr
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
