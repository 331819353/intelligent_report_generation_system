package datasettagsuggestion

import (
	"context"
	"errors"
	"strings"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type Worker struct {
	store             Store
	generator         *Generator
	heartbeatInterval func(time.Duration) time.Duration
}

func NewWorker(store Store, generator *Generator) *Worker {
	return &Worker{
		store: store, generator: generator,
		heartbeatInterval: defaultHeartbeatInterval,
	}
}

func (worker *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, ErrInvalidRequest
	}
	return worker.store.ListTenantIDs(ctx)
}

// ProcessNext processes one exact published-version job. An unconfigured
// provider leaves jobs pending so a later configuration change can recover
// without manufacturing failed suggestions.
func (worker *Worker) ProcessNext(
	ctx context.Context,
	tenantID string,
	workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.generator == nil ||
		strings.TrimSpace(tenantID) == "" || !validWorkerID(workerID) ||
		lease < time.Second || lease > time.Hour {
		return false, ErrInvalidRequest
	}
	if !worker.generator.Configured() {
		return false, nil
	}
	claim, err := worker.store.ClaimNext(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	input, err := worker.store.LoadInput(ctx, *claim, workerID)
	if err != nil {
		if errors.Is(err, ErrSubjectChanged) {
			return true, worker.skip(ctx, *claim, workerID, "SUBJECT_CHANGED")
		}
		if errors.Is(err, ErrInputLimit) {
			return true, worker.fail(ctx, *claim, workerID, "INPUT_LIMIT_EXCEEDED", false)
		}
		return true, errors.Join(err, worker.fail(ctx, *claim, workerID, "INPUT_PREPARATION_FAILED", true))
	}
	if len(input.Taxonomy) == 0 {
		return true, worker.skip(ctx, *claim, workerID, "NO_ACTIVE_CONTROLLED_TAGS")
	}

	// Refresh once immediately so input preparation time cannot consume most of
	// the original lease before the external model call starts.
	if err := worker.renewLease(ctx, *claim, workerID, lease); err != nil {
		return true, err
	}
	workCtx, cancelWork := context.WithCancel(ctx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go worker.heartbeat(
		heartbeatCtx, cancelWork, *claim, workerID, lease, heartbeatDone,
	)

	completion, err := worker.generator.Generate(workCtx, *claim, input)
	stopHeartbeat()
	heartbeatErr := <-heartbeatDone
	cancelWork()
	if heartbeatErr != nil {
		// A failed renewal makes ownership uncertain. Leave the job RUNNING so
		// normal lease expiry/reclaim preserves retry semantics, and never write
		// this provider result (or a terminal failure) with a stale fence.
		return true, heartbeatErr
	}
	if err != nil {
		classification := aiplatform.ClassifyError(err)
		code := "AI_INVALID_OUTPUT"
		retryable := classification.Retryable
		if classification.Code != "" && classification.Code != aiplatform.ErrorCodeCompletionFailed {
			code = string(classification.Code)
		} else if errors.Is(err, ErrInputLimit) {
			code = "INPUT_LIMIT_EXCEEDED"
			retryable = false
		} else if errors.Is(err, ErrInvalidRequest) {
			retryable = false
		}
		return true, errors.Join(err, worker.fail(ctx, *claim, workerID, code, retryable))
	}
	if err := worker.store.Complete(ctx, *claim, workerID, completion); err != nil {
		if errors.Is(err, ErrSubjectChanged) {
			return true, nil
		}
		return true, errors.Join(err, worker.fail(ctx, *claim, workerID, "RESULT_WRITE_FAILED", true))
	}
	return true, nil
}

func (worker *Worker) heartbeat(
	ctx context.Context,
	cancelWork context.CancelFunc,
	claim Claim,
	workerID string,
	lease time.Duration,
	done chan<- error,
) {
	interval := worker.heartbeatInterval(lease)
	if interval <= 0 {
		interval = defaultHeartbeatInterval(lease)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-timer.C:
			err := worker.renewLease(ctx, claim, workerID, lease)
			if err != nil {
				if ctx.Err() != nil &&
					(errors.Is(err, context.Canceled) ||
						errors.Is(err, context.DeadlineExceeded)) {
					done <- nil
					return
				}
				cancelWork()
				done <- err
				return
			}
			timer.Reset(interval)
		}
	}
}

func (worker *Worker) renewLease(
	ctx context.Context,
	claim Claim,
	workerID string,
	lease time.Duration,
) error {
	callCtx, cancel := context.WithTimeout(ctx, heartbeatCallTimeout(lease))
	defer cancel()
	return worker.store.Heartbeat(callCtx, claim, workerID, lease)
}

func defaultHeartbeatInterval(lease time.Duration) time.Duration {
	return lease / 3
}

func heartbeatCallTimeout(lease time.Duration) time.Duration {
	timeout := lease / 6
	if timeout < 250*time.Millisecond {
		return 250 * time.Millisecond
	}
	if timeout > 10*time.Second {
		return 10 * time.Second
	}
	return timeout
}

func (worker *Worker) skip(
	ctx context.Context,
	claim Claim,
	workerID string,
	code string,
) error {
	err := worker.store.Skip(ctx, claim, workerID, code)
	if errors.Is(err, ErrLeaseLost) {
		return nil
	}
	return err
}

func (worker *Worker) fail(
	ctx context.Context,
	claim Claim,
	workerID string,
	code string,
	retryable bool,
) error {
	err := worker.store.Fail(ctx, claim, workerID, code, retryable)
	if errors.Is(err, ErrLeaseLost) {
		return nil
	}
	return err
}

func validWorkerID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
