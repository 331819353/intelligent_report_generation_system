package semanticcatalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/embedding"
)

// Worker drains both deterministic rebuild events and embedding events. Rebuilds
// remain available even when the embedding provider is not configured.
type Worker struct {
	store             Store
	provider          embedding.Provider
	heartbeatInterval func(time.Duration) time.Duration
}

func NewWorker(store Store, provider embedding.Provider) *Worker {
	return &Worker{
		store: store, provider: provider,
		heartbeatInterval: defaultHeartbeatInterval,
	}
}

func (w *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	if w == nil || w.store == nil {
		return nil, ErrInvalidRequest
	}
	return w.store.ListTenantIDs(ctx)
}

// ProcessNext claims at most MaxBatchSize events. Applying a changed source
// document fires the database trigger that enqueues a separate
// SEMANTIC_DOCUMENT event; embedding is therefore asynchronous and retryable.
func (w *Worker) ProcessNext(
	ctx context.Context,
	tenantID string,
	workerID string,
	lease time.Duration,
) (int, error) {
	if w == nil || w.store == nil || strings.TrimSpace(tenantID) == "" ||
		!validWorkerID(workerID) || lease < time.Second {
		return 0, ErrInvalidRequest
	}
	embeddingReady := w.provider != nil && w.provider.Configured() &&
		w.provider.Dimensions() == VectorDimensions && strings.TrimSpace(w.provider.Model()) != ""
	model := ""
	if embeddingReady {
		model = w.provider.Model()
	}
	claims, err := w.store.ClaimBatch(
		ctx, tenantID, workerID, lease, MaxBatchSize, embeddingReady,
	)
	if err != nil || len(claims) == 0 {
		return 0, err
	}

	documents := make([]Work, 0, len(claims))
	var combined error
	for _, claim := range claims {
		work, prepareErr := w.store.Prepare(ctx, claim, workerID, model)
		if prepareErr != nil {
			combined = errors.Join(combined, prepareErr, w.fail(ctx, claim, workerID, "DOCUMENT_PREPARE_FAILED"))
			continue
		}
		if claim.SubjectType != SubjectDocument {
			if applyErr := w.store.ApplyDocument(ctx, work, workerID); applyErr != nil {
				combined = errors.Join(combined, applyErr, w.fail(ctx, claim, workerID, "DOCUMENT_REBUILD_FAILED"))
			}
			continue
		}
		if work.Missing || work.Current || work.EmbeddingSuppressed {
			combined = errors.Join(combined, w.store.Acknowledge(ctx, work, workerID))
			continue
		}
		documents = append(documents, work)
	}

	if len(documents) == 0 {
		return len(claims), combined
	}
	for start := 0; start < len(documents); {
		end, inputs := embeddingBatch(documents, start)
		batch := documents[start:end]

		// Preparation may consume part of the claim lifetime. Extend every exact
		// event fence before sending text outside the process, then continue at
		// lease/3 while the provider call is in flight.
		if heartbeatErr := w.renewBatch(ctx, batch, workerID, lease); heartbeatErr != nil {
			return len(claims), errors.Join(combined, heartbeatErr)
		}
		workCtx, cancelWork := context.WithCancel(ctx)
		heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
		heartbeatDone := make(chan error, 1)
		go w.heartbeatBatch(
			heartbeatCtx, cancelWork, batch, workerID, lease, heartbeatDone,
		)

		vectors, embedErr := w.provider.Embed(workCtx, inputs)
		stopHeartbeat()
		heartbeatErr := <-heartbeatDone
		cancelWork()
		if heartbeatErr != nil {
			// Ownership of at least one event is uncertain. Do not complete or
			// fail any event in this provider batch; expiry/reclaim will retry
			// the still-live claims, while the stale claim remains fenced out.
			return len(claims), errors.Join(combined, heartbeatErr)
		}
		if embedErr != nil || !validVectors(vectors, len(batch)) {
			code := "EMBEDDING_PROVIDER_UNAVAILABLE"
			if errors.Is(embedErr, embedding.ErrInvalidRequest) ||
				errors.Is(embedErr, embedding.ErrInvalidResponse) ||
				(embedErr == nil && !validVectors(vectors, len(batch))) {
				code = "EMBEDDING_INVALID_RESPONSE"
			}
			for _, document := range batch {
				combined = errors.Join(combined, w.fail(ctx, document.Claim, workerID, code))
			}
			if embedErr == nil {
				embedErr = fmt.Errorf("%w: vector batch shape", embedding.ErrInvalidResponse)
			}
			combined = errors.Join(combined, embedErr)
			start = end
			continue
		}
		for index, document := range batch {
			completeErr := w.store.CompleteEmbedding(
				ctx, document, workerID, w.provider.Model(), vectors[index],
			)
			if completeErr != nil {
				combined = errors.Join(combined, completeErr)
				if !errors.Is(completeErr, ErrLeaseLost) && !errors.Is(completeErr, ErrSubjectChanged) {
					combined = errors.Join(combined, w.fail(ctx, document.Claim, workerID, "EMBEDDING_WRITE_FAILED"))
				}
			}
		}
		start = end
	}
	return len(claims), combined
}

func (w *Worker) heartbeatBatch(
	ctx context.Context,
	cancelWork context.CancelFunc,
	batch []Work,
	workerID string,
	lease time.Duration,
	done chan<- error,
) {
	interval := w.heartbeatInterval(lease)
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
			err := w.renewBatch(ctx, batch, workerID, lease)
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

func (w *Worker) renewBatch(
	ctx context.Context,
	batch []Work,
	workerID string,
	lease time.Duration,
) error {
	for _, work := range batch {
		callCtx, cancel := context.WithTimeout(ctx, heartbeatCallTimeout(lease))
		err := w.store.Heartbeat(callCtx, work.Claim, workerID, lease)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
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

func (w *Worker) fail(ctx context.Context, claim Claim, workerID, code string) error {
	err := w.store.Fail(ctx, claim, workerID, code)
	// Losing a fence means a newer edit or worker already owns the event. The
	// stale worker must not convert that expected race into another mutation.
	if errors.Is(err, ErrLeaseLost) {
		return nil
	}
	return err
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

func embeddingBatch(documents []Work, start int) (int, []string) {
	end := start
	totalBytes := 0
	inputs := make([]string, 0, MaxBatchSize)
	for end < len(documents) && len(inputs) < MaxBatchSize {
		size := len(documents[end].Text)
		if len(inputs) > 0 && totalBytes+size > maxBatchInputBytes {
			break
		}
		inputs = append(inputs, documents[end].Text)
		totalBytes += size
		end++
	}
	return end, inputs
}

func validWorkerID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxWorkerIDLength {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
