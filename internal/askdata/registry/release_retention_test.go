package registry

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

func TestReleaseReferenceValidationAndRetirementImpact(t *testing.T) {
	reference := releaseReferenceFixture()
	if err := reference.Validate(); err != nil {
		t.Fatalf("valid release reference failed: %v", err)
	}
	for name, mutate := range map[string]func(*ReleaseReference){
		"unknown type": func(value *ReleaseReference) { value.Type = "UNKNOWN" },
		"non UUID":     func(value *ReleaseReference) { value.ReferenceID = "not-a-uuid" },
		"blank name":   func(value *ReleaseReference) { value.ReferenceName = " " },
		"control name": func(value *ReleaseReference) { value.ReferenceName = "report\nname" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := reference
			mutate(&candidate)
			if !errors.Is(candidate.Validate(), ErrReleaseReferenceInvalid) {
				t.Fatalf("Validate() error = %v", candidate.Validate())
			}
		})
	}

	failure := &ReleaseRetentionError{
		Code: ReleaseRetireBlockedCode, ReleaseID: reference.ReleaseID,
		References: []ReleaseReference{reference},
	}
	if failure.Error() == "" || len(failure.References) != 1 ||
		failure.References[0].ReferenceName != "经营日报 v3" ||
		failure.References[0].OwnerID != reference.OwnerID {
		t.Fatalf("incomplete retirement impact: %#v", failure)
	}
}

func TestReleaseProjectionCleanupWorkerFailsClosed(t *testing.T) {
	cleanup := RetainedProjectionCleanup{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(),
		Release: askdata.ReleaseRef{
			ReleaseID:   askdata.ID(uuid.NewString()),
			ContentHash: askdata.HashBytes([]byte("retained-release")),
		},
		ObjectCount: 3,
	}
	store := &memoryRetainedProjectionStore{cleanup: cleanup}
	first, second := &memoryRetainedProjectionCleaner{}, &memoryRetainedProjectionCleaner{}
	worker, err := NewReleaseProjectionCleanupWorker(store, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), cleanup.TenantID, cleanup.DomainID,
		string(cleanup.Release.ReleaseID)); err != nil {
		t.Fatal(err)
	}
	if store.completed != 1 || !reflect.DeepEqual(first.seen, []RetainedProjectionCleanup{cleanup}) ||
		!reflect.DeepEqual(second.seen, []RetainedProjectionCleanup{cleanup}) {
		t.Fatalf("cleanup pipeline = store:%d first:%#v second:%#v",
			store.completed, first.seen, second.seen)
	}

	blockedStore := &memoryRetainedProjectionStore{cleanup: cleanup}
	failed := &memoryRetainedProjectionCleaner{err: errors.New("graph unavailable")}
	blockedWorker, err := NewReleaseProjectionCleanupWorker(blockedStore, failed)
	if err != nil {
		t.Fatal(err)
	}
	if err := blockedWorker.Run(context.Background(), cleanup.TenantID, cleanup.DomainID,
		string(cleanup.Release.ReleaseID)); !errors.Is(err, ErrReleaseProjectionCleanup) {
		t.Fatalf("cleanup failure = %v", err)
	}
	if blockedStore.completed != 0 {
		t.Fatal("database projection proof was cleared after external cleanup failed")
	}
}

type memoryRetainedProjectionStore struct {
	cleanup    RetainedProjectionCleanup
	prepareErr error
	completed  int
}

func (store *memoryRetainedProjectionStore) PrepareRetainedProjectionCleanup(
	context.Context, string, string, string,
) (RetainedProjectionCleanup, error) {
	return store.cleanup, store.prepareErr
}

func (store *memoryRetainedProjectionStore) CompleteRetainedProjectionCleanup(
	_ context.Context, cleanup RetainedProjectionCleanup,
) error {
	if !reflect.DeepEqual(cleanup, store.cleanup) {
		return errors.New("cleanup proof changed")
	}
	store.completed++
	return nil
}

type memoryRetainedProjectionCleaner struct {
	seen []RetainedProjectionCleanup
	err  error
}

func (cleaner *memoryRetainedProjectionCleaner) CleanupRetainedProjection(
	_ context.Context, cleanup RetainedProjectionCleanup,
) error {
	cleaner.seen = append(cleaner.seen, cleanup)
	return cleaner.err
}

func releaseReferenceFixture() ReleaseReference {
	return ReleaseReference{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ReleaseID: uuid.NewString(),
		Type: ReleaseReferenceReportVersion, ReferenceID: uuid.NewString(),
		ReferenceName: "经营日报 v3", OwnerID: uuid.NewString(),
	}
}
