package materialization

import (
	"context"
	"testing"
)

type controlServiceStoreStub struct {
	request       RegisterCurrentRequest
	cancelBuildID string
}

func (store *controlServiceStoreStub) RegisterCurrent(
	_ context.Context,
	_, _, datasetID string,
	request RegisterCurrentRequest,
) (Run, bool, error) {
	store.request = request
	return Run{ID: "2cb7694d-7a65-46fc-b422-7c2fa4426e70", DatasetID: datasetID}, true, nil
}

func (*controlServiceStoreStub) ListBuilds(context.Context, string, string, int, int) ([]Run, int, error) {
	return nil, 0, nil
}

func (*controlServiceStoreStub) GetBuild(_ context.Context, _, datasetID, buildID string) (BuildDetail, error) {
	return BuildDetail{Build: Build{ID: buildID, DatasetID: datasetID}}, nil
}

func (store *controlServiceStoreStub) CancelActive(_ context.Context, _, _, _, buildID string) (Run, error) {
	store.cancelBuildID = buildID
	return Run{ID: buildID, Status: RunCancelled}, nil
}

func TestControlServiceRegisterUsesRequestIDAsRunIdentity(t *testing.T) {
	store := &controlServiceStoreStub{}
	service := NewControlService(store)
	requestID := "3e828fa0-dd0b-49a1-bf66-da08593c9214"
	publishedVersionID := "811c575a-ac36-4f58-9e1e-bfe8410cc278"
	_, created, err := service.Register(
		context.Background(),
		"f7bb5b5c-a98d-4a6c-9e7c-310952743450",
		"0331aebd-9fc7-485e-8474-5b0bf58bf9a3",
		"71adfb01-823f-4868-af7d-5066317c68d9",
		CreateBuildInput{RequestID: requestID, PublishedVersionID: publishedVersionID},
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !created {
		t.Fatal("expected a newly registered DAG run")
	}
	if store.request.PartitionKey != "run:"+requestID {
		t.Fatalf("partition key = %q", store.request.PartitionKey)
	}
	if store.request.ExpectedVersionID != publishedVersionID {
		t.Fatalf("expected version ID = %q", store.request.ExpectedVersionID)
	}
}

func TestControlServiceRegisterRejectsInvalidRequestID(t *testing.T) {
	service := NewControlService(&controlServiceStoreStub{})
	_, _, err := service.Register(
		context.Background(),
		"f7bb5b5c-a98d-4a6c-9e7c-310952743450",
		"0331aebd-9fc7-485e-8474-5b0bf58bf9a3",
		"71adfb01-823f-4868-af7d-5066317c68d9",
		CreateBuildInput{
			RequestID:          "not-a-uuid",
			PublishedVersionID: "811c575a-ac36-4f58-9e1e-bfe8410cc278",
		},
	)
	if err != ErrInvalidRequest {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
}

func TestControlServiceCancelStopsActiveBuild(t *testing.T) {
	store := &controlServiceStoreStub{}
	service := NewControlService(store)
	buildID := "2cb7694d-7a65-46fc-b422-7c2fa4426e70"
	_, err := service.Cancel(
		context.Background(),
		"f7bb5b5c-a98d-4a6c-9e7c-310952743450",
		"0331aebd-9fc7-485e-8474-5b0bf58bf9a3",
		"71adfb01-823f-4868-af7d-5066317c68d9",
		buildID,
	)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if store.cancelBuildID != buildID {
		t.Fatalf("cancelled build = %q", store.cancelBuildID)
	}
}
