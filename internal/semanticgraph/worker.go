package semanticgraph

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

type ProjectionWorker struct {
	store     *PostgresStore
	projector *Projector
	space     string
}

func NewProjectionWorker(store *PostgresStore, projector *Projector, space string) *ProjectionWorker {
	return &ProjectionWorker{store: store, projector: projector, space: strings.TrimSpace(space)}
}

func (worker *ProjectionWorker) ProcessNext(
	ctx context.Context, tenantID, workerID string, lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.projector == nil || worker.space == "" {
		return false, ErrInvalidRequest
	}
	claim, err := worker.store.Claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	projectionContext, cancel := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	heartbeatResult := make(chan error, 1)
	go worker.heartbeatLoop(projectionContext, cancel, *claim, workerID, lease, heartbeatDone, heartbeatResult)

	manifest, projectErr := worker.store.LoadManifest(projectionContext, *claim)
	verification := ProjectionVerification{}
	if projectErr == nil {
		verification, projectErr = worker.projector.Project(projectionContext, manifest)
	}
	close(heartbeatDone)
	cancel()
	if heartbeatErr := <-heartbeatResult; projectErr == nil && heartbeatErr != nil {
		projectErr = heartbeatErr
	}
	if projectErr == nil {
		projectErr = worker.store.Complete(
			ctx, *claim, workerID, claim.ResourceVersion(worker.space), verification,
		)
	}
	if projectErr == nil {
		slog.Info("NebulaGraph semantic release projected",
			"tenant_id", tenantID, "release_id", claim.ReleaseID,
			"semantic_version", claim.SemanticVersion,
			"vertices", verification.VertexCount, "edges", verification.EdgeCount)
		return true, nil
	}
	failErr := worker.store.Fail(ctx, *claim, workerID, projectionErrorCode(projectErr), projectErr)
	return true, errors.Join(projectErr, failErr)
}

func (worker *ProjectionWorker) heartbeatLoop(
	ctx context.Context,
	cancel context.CancelFunc,
	claim ProjectionClaim,
	workerID string,
	lease time.Duration,
	done <-chan struct{},
	result chan<- error,
) {
	interval := lease / 3
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			result <- nil
			return
		case <-ctx.Done():
			result <- nil
			return
		case <-ticker.C:
			renewed, err := worker.store.Heartbeat(ctx, claim, workerID, lease)
			if err != nil || !renewed {
				cancel()
				if err == nil {
					err = ErrProjectionLease
				}
				result <- err
				return
			}
		}
	}
}

func RunProjectionWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *ProjectionWorker,
	workerID string,
	pollInterval time.Duration,
) {
	if worker == nil || worker.store == nil {
		logger.Error("NebulaGraph projection worker disabled", "error", ErrInvalidRequest)
		return
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		tenantIDs, err := worker.store.TenantIDs(ctx)
		if err != nil {
			logger.Error("list NebulaGraph projection tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				processed, processErr := worker.ProcessNext(
					ctx, tenantID, workerID+"-nebula", 3*time.Minute,
				)
				if processErr != nil {
					logger.Error("project semantic release to NebulaGraph",
						"tenant_id", tenantID, "error", processErr)
				}
				if processed {
					continue
				}
			}
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func projectionErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrProjectionLease):
		return "NEBULA_PROJECTION_LEASE_LOST"
	case errors.Is(err, ErrInvalidRequest):
		return "NEBULA_PROJECTION_CONTRACT_INVALID"
	case errors.Is(err, ErrNoCertifiedPath):
		return "NEBULA_PROJECTION_VERIFY_FAILED"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "NEBULA_PROJECTION_TIMEOUT"
	default:
		return "NEBULA_PROJECTION_UNAVAILABLE"
	}
}
