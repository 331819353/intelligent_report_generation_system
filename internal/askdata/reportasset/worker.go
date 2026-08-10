package reportasset

import (
	"context"
	"errors"
	"strings"
	"time"
)

type DeliveryFailure struct {
	Code      string
	Retryable bool
	Cause     error
}

func (failure *DeliveryFailure) Error() string {
	if failure == nil {
		return "report delivery failed"
	}
	if failure.Cause != nil {
		return failure.Code + ": " + failure.Cause.Error()
	}
	return failure.Code
}

func (failure *DeliveryFailure) Unwrap() error { return failure.Cause }

type IntentApplier interface {
	ApplyIntent(context.Context, IntentDeliveryClaim) (int64, error)
}

type IntentWorker struct {
	store   IntentDeliveryStore
	applier IntentApplier
}

func NewIntentWorker(store IntentDeliveryStore, applier IntentApplier) (*IntentWorker, error) {
	if store == nil || applier == nil {
		return nil, ErrInvalidIntent
	}
	return &IntentWorker{store: store, applier: applier}, nil
}

func (worker *IntentWorker) TenantIDs(ctx context.Context) ([]string, error) {
	return worker.store.ListIntentTenantIDs(ctx)
}

func (worker *IntentWorker) ProcessNext(ctx context.Context, tenantID string, lease time.Duration) (bool, error) {
	if worker == nil || worker.store == nil || worker.applier == nil {
		return false, ErrInvalidIntent
	}
	claim, err := worker.store.ClaimIntent(ctx, tenantID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	revision, applyErr := worker.applier.ApplyIntent(ctx, *claim)
	if applyErr == nil {
		return true, worker.store.CompleteIntent(ctx, *claim, revision)
	}
	var failure *DeliveryFailure
	if errors.As(applyErr, &failure) && !failure.Retryable {
		code := strings.TrimSpace(failure.Code)
		if code == "" {
			code = "REPORT_DELIVERY_REJECTED"
		}
		return true, errors.Join(applyErr, worker.store.RejectIntent(ctx, *claim, code, boundedError(applyErr)))
	}
	return true, errors.Join(applyErr, worker.store.RetryIntent(ctx, *claim, boundedError(applyErr)))
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 4096 {
		value = value[:4096]
	}
	return value
}
