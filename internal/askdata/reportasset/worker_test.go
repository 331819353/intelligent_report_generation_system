package reportasset

import (
	"context"
	"errors"
	"testing"
	"time"
)

type deliveryStoreStub struct {
	claims    []*IntentDeliveryClaim
	complete  int
	rejected  int
	retried   int
	revision  int64
	tenantIDs []string
}

func (stub *deliveryStoreStub) ListIntentTenantIDs(context.Context) ([]string, error) {
	return append([]string(nil), stub.tenantIDs...), nil
}
func (stub *deliveryStoreStub) ClaimIntent(context.Context, string, time.Duration) (*IntentDeliveryClaim, error) {
	if len(stub.claims) == 0 {
		return nil, nil
	}
	claim := stub.claims[0]
	stub.claims = stub.claims[1:]
	return claim, nil
}
func (stub *deliveryStoreStub) CompleteIntent(_ context.Context, _ IntentDeliveryClaim, revision int64) error {
	stub.complete++
	stub.revision = revision
	return nil
}
func (stub *deliveryStoreStub) RejectIntent(context.Context, IntentDeliveryClaim, string, string) error {
	stub.rejected++
	return nil
}
func (stub *deliveryStoreStub) RetryIntent(context.Context, IntentDeliveryClaim, string) error {
	stub.retried++
	return nil
}

type intentApplierStub struct {
	errors []error
	calls  int
}

func (stub *intentApplierStub) ApplyIntent(context.Context, IntentDeliveryClaim) (int64, error) {
	stub.calls++
	if len(stub.errors) != 0 {
		err := stub.errors[0]
		stub.errors = stub.errors[1:]
		if err != nil {
			return 0, err
		}
	}
	return 7, nil
}

func TestIntentWorkerRetriesAfterCrashAndCompletesOnRedelivery(t *testing.T) {
	claim := &IntentDeliveryClaim{}
	store := &deliveryStoreStub{claims: []*IntentDeliveryClaim{claim, claim}, tenantIDs: []string{"tenant-a"}}
	applier := &intentApplierStub{errors: []error{errors.New("transient database failure"), nil}}
	worker, err := NewIntentWorker(store, applier)
	if err != nil {
		t.Fatal(err)
	}
	if tenants, err := worker.TenantIDs(context.Background()); err != nil || len(tenants) != 1 {
		t.Fatalf("TenantIDs() = %#v, %v", tenants, err)
	}
	processed, firstErr := worker.ProcessNext(context.Background(), "tenant-a", time.Minute)
	if !processed || firstErr == nil || store.retried != 1 || store.complete != 0 {
		t.Fatalf("first delivery processed=%v err=%v store=%#v", processed, firstErr, store)
	}
	processed, secondErr := worker.ProcessNext(context.Background(), "tenant-a", time.Minute)
	if !processed || secondErr != nil || store.complete != 1 || store.revision != 7 || applier.calls != 2 {
		t.Fatalf("redelivery processed=%v err=%v store=%#v applier=%#v", processed, secondErr, store, applier)
	}
}

func TestIntentWorkerRejectsNonRetryableAuthorizationFailure(t *testing.T) {
	store := &deliveryStoreStub{claims: []*IntentDeliveryClaim{{}}}
	applier := &intentApplierStub{errors: []error{&DeliveryFailure{
		Code: "REPORT_EDIT_FORBIDDEN", Retryable: false, Cause: errors.New("denied"),
	}}}
	worker, _ := NewIntentWorker(store, applier)
	processed, err := worker.ProcessNext(context.Background(), "tenant-a", time.Minute)
	if !processed || err == nil || store.rejected != 1 || store.retried != 0 {
		t.Fatalf("authorization delivery processed=%v err=%v store=%#v", processed, err, store)
	}
}
