package publication

import (
	"context"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/report/store"
)

type RecoveryRepository interface {
	PublicationTenantIDs(context.Context) ([]string, error)
	ClaimPublication(context.Context, string, time.Duration) (*store.PublicationClaim, error)
	CompletePublicationClaim(context.Context, store.PublicationClaim) error
	FailPublicationClaim(context.Context, store.PublicationClaim, error) error
}

type RecoveryWorker struct {
	repository RecoveryRepository
	artifacts  ArtifactStore
}

func NewRecoveryWorker(repository RecoveryRepository, artifacts ArtifactStore) (*RecoveryWorker, error) {
	if repository == nil || artifacts == nil {
		return nil, errors.New("report publication recovery worker is not configured")
	}
	return &RecoveryWorker{repository: repository, artifacts: artifacts}, nil
}

func (worker *RecoveryWorker) TenantIDs(ctx context.Context) ([]string, error) {
	return worker.repository.PublicationTenantIDs(ctx)
}

func (worker *RecoveryWorker) ProcessNext(ctx context.Context, tenantID string, lease time.Duration) (bool, error) {
	claim, err := worker.repository.ClaimPublication(ctx, tenantID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	runErr := worker.artifacts.Promote(ctx, claim.Version.ObjectURI+".tmp", claim.Version.ObjectURI)
	if runErr == nil {
		runErr = worker.repository.CompletePublicationClaim(ctx, *claim)
	}
	if runErr != nil {
		return true, errors.Join(runErr, worker.repository.FailPublicationClaim(context.WithoutCancel(ctx), *claim, runErr))
	}
	return true, nil
}
