package reportasset

import (
	"context"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

type ExtractionClaim struct {
	ID, TenantID, ReportID, ReportVersionID, LeaseToken askdata.ID
	Attempt                                             int
}
type ProjectionOperation string

const (
	ProjectionUpsert ProjectionOperation = "UPSERT"
	ProjectionRemove ProjectionOperation = "REMOVE"
)

type AssetProjectionClaim struct {
	ID, TenantID, AssetID, LeaseToken askdata.ID
	ContentHash                       askdata.ContentHash
	Operation                         ProjectionOperation
	Attempt                           int
}

type ProjectionRuntimeStore interface {
	TenantIDs(context.Context) ([]string, error)
	ClaimExtraction(context.Context, string, time.Duration) (*ExtractionClaim, error)
	Extract(context.Context, ExtractionClaim) error
	FinishExtraction(context.Context, ExtractionClaim, error) error
	ClaimProjection(context.Context, string, time.Duration) (*AssetProjectionClaim, error)
	LoadProjection(context.Context, AssetProjectionClaim) (Candidate, error)
	PersistSearchProjection(context.Context, AssetProjectionClaim, Projection) error
	RemoveSearchProjection(context.Context, AssetProjectionClaim) error
	LoadGraphProjection(context.Context, AssetProjectionClaim, Projection) (ReportGraphProjection, error)
	FinishProjection(context.Context, AssetProjectionClaim, error) error
}

type ReportGraphWriter interface {
	Upsert(context.Context, ReportGraphProjection) error
	Remove(context.Context, ReportGraphProjection) error
}

type ProjectionRuntimeWorker struct {
	store ProjectionRuntimeStore
	graph ReportGraphWriter
}

func NewProjectionRuntimeWorker(store ProjectionRuntimeStore, graph ReportGraphWriter) (*ProjectionRuntimeWorker, error) {
	if store == nil || graph == nil {
		return nil, ErrInvalidIntent
	}
	return &ProjectionRuntimeWorker{store: store, graph: graph}, nil
}
func (worker *ProjectionRuntimeWorker) TenantIDs(ctx context.Context) ([]string, error) {
	return worker.store.TenantIDs(ctx)
}
func (worker *ProjectionRuntimeWorker) ProcessNext(ctx context.Context, tenantID string, lease time.Duration) (bool, error) {
	if claim, err := worker.store.ClaimExtraction(ctx, tenantID, lease); err != nil {
		return false, err
	} else if claim != nil {
		runErr := worker.store.Extract(ctx, *claim)
		finishErr := worker.store.FinishExtraction(ctx, *claim, runErr)
		if errors.Is(runErr, ErrAssetSourceGone) {
			// Handled: the row was terminalised with its reason recorded. This is
			// the worker doing its job, so it must not surface as a worker error —
			// otherwise every orphaned row logs on every tick and buries the
			// failures an operator actually needs to see.
			return true, finishErr
		}
		return true, errors.Join(runErr, finishErr)
	}
	claim, err := worker.store.ClaimProjection(ctx, tenantID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	if claim.Operation == ProjectionRemove {
		graphProjection, loadErr := worker.store.LoadGraphProjection(ctx, *claim, Projection{})
		if loadErr == nil {
			loadErr = worker.graph.Remove(ctx, graphProjection)
		}
		if loadErr == nil {
			loadErr = worker.store.RemoveSearchProjection(ctx, *claim)
		}
		return true, errors.Join(loadErr, worker.store.FinishProjection(ctx, *claim, loadErr))
	}
	candidate, runErr := worker.store.LoadProjection(ctx, *claim)
	if runErr != nil {
		return true, errors.Join(runErr, worker.store.FinishProjection(ctx, *claim, runErr))
	}
	projection, _, runErr := BuildProjection(candidate)
	if runErr == nil {
		runErr = worker.store.PersistSearchProjection(ctx, *claim, projection)
	}
	var graphProjection ReportGraphProjection
	if runErr == nil {
		graphProjection, runErr = worker.store.LoadGraphProjection(ctx, *claim, projection)
	}
	if runErr == nil {
		runErr = worker.graph.Upsert(ctx, graphProjection)
	}
	return true, errors.Join(runErr, worker.store.FinishProjection(ctx, *claim, runErr))
}
