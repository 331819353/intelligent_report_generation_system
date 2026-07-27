package backgroundtask

import (
	"context"
	"strings"
)

type Service struct{ store Store }

func NewService(store Store) *Service { return &Service{store: store} }

func (service *Service) List(ctx context.Context, tenantID, view string, limit int) (Page, error) {
	if service == nil || service.store == nil || strings.TrimSpace(tenantID) == "" ||
		(view != ViewActive && view != ViewRecent && view != ViewAll) ||
		limit < 1 || limit > 200 {
		return Page{}, ErrInvalidRequest
	}
	return service.store.List(ctx, tenantID, view, limit)
}

func (service *Service) Find(
	ctx context.Context,
	tenantID, kind, taskID string,
) (Task, error) {
	if service == nil || service.store == nil || strings.TrimSpace(tenantID) == "" ||
		strings.TrimSpace(kind) == "" || strings.TrimSpace(taskID) == "" {
		return Task{}, ErrInvalidRequest
	}
	return service.store.Find(ctx, tenantID, kind, taskID)
}

func (service *Service) Cancel(
	ctx context.Context,
	tenantID, actorID, kind, taskID string,
) (Task, error) {
	if service == nil || service.store == nil ||
		strings.TrimSpace(tenantID) == "" || strings.TrimSpace(actorID) == "" ||
		strings.TrimSpace(kind) == "" || strings.TrimSpace(taskID) == "" {
		return Task{}, ErrInvalidRequest
	}
	current, err := service.store.Find(ctx, tenantID, kind, taskID)
	if err != nil {
		return Task{}, err
	}
	if !current.CanCancel {
		return Task{}, ErrNotCancellable
	}
	if err := service.store.Cancel(ctx, tenantID, actorID, kind, taskID); err != nil {
		return Task{}, err
	}
	return service.store.Find(ctx, tenantID, kind, taskID)
}

func (service *Service) Retry(
	ctx context.Context,
	tenantID, actorID, kind, taskID string,
) (Task, error) {
	if service == nil || service.store == nil ||
		strings.TrimSpace(tenantID) == "" || strings.TrimSpace(actorID) == "" ||
		strings.TrimSpace(kind) == "" || strings.TrimSpace(taskID) == "" {
		return Task{}, ErrInvalidRequest
	}
	current, err := service.store.Find(ctx, tenantID, kind, taskID)
	if err != nil {
		return Task{}, err
	}
	if !current.CanRetry {
		return Task{}, ErrNotRetryable
	}
	if err := service.store.Retry(ctx, tenantID, actorID, kind, taskID); err != nil {
		return Task{}, err
	}
	return service.store.Find(ctx, tenantID, kind, taskID)
}
