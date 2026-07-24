package materialization

import (
	"context"
	"strings"
)

type ControlService struct {
	store ControlStore
}

func NewControlService(store ControlStore) *ControlService {
	return &ControlService{store: store}
}

func (service *ControlService) Register(
	ctx context.Context,
	tenantID, actorID, datasetID string,
	input CreateBuildInput,
) (BuildDetail, bool, error) {
	if service == nil || service.store == nil ||
		!validUUID(tenantID) || !validUUID(actorID) || !validUUID(datasetID) {
		return BuildDetail{}, false, ErrInvalidRequest
	}
	mode := input.Mode
	if mode == "" {
		mode = RunModeFull
	}
	maxAttempts := 3
	if input.MaxAttempts != nil {
		maxAttempts = *input.MaxAttempts
	}
	if mode != RunModeFull ||
		input.PartitionKey != strings.TrimSpace(input.PartitionKey) ||
		input.PartitionKey != "" ||
		maxAttempts < 1 || maxAttempts > 10 {
		return BuildDetail{}, false, ErrInvalidRequest
	}
	run, created, err := service.store.RegisterCurrent(
		ctx, tenantID, actorID, datasetID,
		RegisterCurrentRequest{
			Mode: mode, PartitionKey: input.PartitionKey, MaxAttempts: maxAttempts,
		},
	)
	if err != nil {
		return BuildDetail{}, false, err
	}
	detail, err := service.store.GetBuild(ctx, tenantID, datasetID, run.ID)
	if err != nil {
		return BuildDetail{}, false, err
	}
	return detail, created, nil
}

func (service *ControlService) List(
	ctx context.Context,
	tenantID, datasetID string,
	limit, offset int,
) (BuildPage, error) {
	if service == nil || service.store == nil ||
		!validUUID(tenantID) || !validUUID(datasetID) ||
		limit < 1 || limit > MaxBuildPageLimit || offset < 0 {
		return BuildPage{}, ErrInvalidRequest
	}
	runs, total, err := service.store.ListBuilds(ctx, tenantID, datasetID, limit, offset)
	if err != nil {
		return BuildPage{}, err
	}
	items := make([]Build, len(runs))
	for index, run := range runs {
		items[index] = buildFromRun(run)
	}
	return BuildPage{Items: items, Total: total, Limit: limit, Offset: offset}, nil
}

func (service *ControlService) Get(
	ctx context.Context,
	tenantID, datasetID, buildID string,
) (BuildDetail, error) {
	if service == nil || service.store == nil ||
		!validUUID(tenantID) || !validUUID(datasetID) || !validUUID(buildID) {
		return BuildDetail{}, ErrInvalidRequest
	}
	return service.store.GetBuild(ctx, tenantID, datasetID, buildID)
}

func (service *ControlService) Cancel(
	ctx context.Context,
	tenantID, actorID, datasetID, buildID string,
) (BuildDetail, error) {
	if service == nil || service.store == nil ||
		!validUUID(tenantID) || !validUUID(actorID) ||
		!validUUID(datasetID) || !validUUID(buildID) {
		return BuildDetail{}, ErrInvalidRequest
	}
	if _, err := service.store.CancelQueued(ctx, tenantID, actorID, datasetID, buildID); err != nil {
		return BuildDetail{}, err
	}
	return service.store.GetBuild(ctx, tenantID, datasetID, buildID)
}
