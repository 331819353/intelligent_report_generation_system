package semanticmanagement

import (
	"context"
	"errors"
	"strings"
	"time"
)

type DimensionProfileWorker struct {
	store             DimensionProfileStore
	heartbeatInterval func(time.Duration) time.Duration
}

func NewDimensionProfileWorker(store DimensionProfileStore) *DimensionProfileWorker {
	return &DimensionProfileWorker{
		store:             store,
		heartbeatInterval: defaultDimensionProfileHeartbeat,
	}
}

func (w *DimensionProfileWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if w == nil || w.store == nil {
		return nil, ErrInvalidRequest
	}
	return w.store.ListProfileTenantIDs(ctx)
}

func (w *DimensionProfileWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if w == nil || w.store == nil || !validUUID(tenantID) ||
		!validWorkerName(workerID) || lease < 3*time.Second || lease > time.Hour {
		return false, ErrInvalidRequest
	}
	claim, err := w.store.ClaimDimensionProfile(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go w.heartbeat(heartbeatCtx, cancelWork, *claim, lease, heartbeatDone)

	runCtx, cancelRun := context.WithTimeout(
		workCtx,
		time.Duration(claim.TimeoutSeconds)*time.Second,
	)
	observation, runErr := w.store.MeasureDimensionProfile(runCtx, *claim)
	cancelRun()

	if completed, heartbeatErr := pollDimensionProfileHeartbeat(heartbeatDone); completed {
		stopHeartbeat()
		cancelWork()
		if heartbeatErr != nil {
			return true, heartbeatErr
		}
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, runErr
	}

	if runErr == nil {
		completeErr := w.store.CompleteDimensionProfile(ctx, *claim, observation)
		stopHeartbeat()
		cancelWork()
		heartbeatErr := <-heartbeatDone
		if completeErr == nil {
			return true, nil
		}
		if errors.Is(completeErr, ErrProfileLeaseLost) && heartbeatErr != nil {
			return true, heartbeatErr
		}
		return true, completeErr
	}

	code := dimensionProfileFailureCode(runErr)
	failCtx, failCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	failErr := w.store.FailDimensionProfile(failCtx, *claim, code)
	failCancel()
	stopHeartbeat()
	cancelWork()
	heartbeatErr := <-heartbeatDone
	if failErr == nil {
		return true, runErr
	}
	if errors.Is(failErr, ErrProfileLeaseLost) && heartbeatErr != nil {
		return true, heartbeatErr
	}
	return true, errors.Join(runErr, failErr)
}

func (w *DimensionProfileWorker) heartbeat(
	ctx context.Context,
	cancelWork context.CancelFunc,
	claim DimensionProfileJob,
	lease time.Duration,
	done chan<- error,
) {
	interval := w.heartbeatInterval(lease)
	if interval <= 0 {
		interval = defaultDimensionProfileHeartbeat(lease)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-timer.C:
			updated, err := w.store.HeartbeatDimensionProfile(ctx, claim, lease)
			if err != nil {
				cancelWork()
				done <- err
				return
			}
			claim.LeaseExpiresAt = updated.LeaseExpiresAt
			timer.Reset(interval)
		}
	}
}

func defaultDimensionProfileHeartbeat(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < time.Second {
		return time.Second
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval
}

func pollDimensionProfileHeartbeat(done <-chan error) (bool, error) {
	select {
	case err := <-done:
		return true, err
	default:
		return false, nil
	}
}

func dimensionProfileFailureCode(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ErrProfileTimeout):
		return "PROFILE_TIMEOUT"
	case errors.Is(err, ErrProfileSourceChanged):
		return "PROFILE_SOURCE_CHANGED"
	case errors.Is(err, ErrProfileResourceLimit):
		return "PROFILE_RESOURCE_LIMIT"
	case errors.Is(err, ErrProfileUnsafeView):
		return "PROFILE_VIEW_UNTRUSTED"
	case errors.Is(err, ErrProfileLeaseLost):
		return "PROFILE_LEASE_LOST"
	default:
		// Do not persist or log driver messages that could contain relation
		// details. The stable code is the complete operational disclosure.
		return "PROFILE_FAILED"
	}
}

func validDimensionProfileStatus(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "QUEUED", "RUNNING", "SUCCEEDED", "SKIPPED_POLICY", "FAILED", "STALE":
		return true
	default:
		return false
	}
}
