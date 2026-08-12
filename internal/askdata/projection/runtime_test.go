package projection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	claim             *Claim
	proof             Proof
	err               error
	completed, failed bool
}

func (store *fakeStore) TenantIDs(context.Context, Target) ([]string, error) {
	return []string{uuid.NewString()}, nil
}
func (store *fakeStore) Claim(context.Context, string, Target, string, time.Duration) (*Claim, error) {
	return store.claim, nil
}
func (store *fakeStore) Project(context.Context, Claim, string) (Proof, error) {
	return store.proof, store.err
}
func (store *fakeStore) Complete(context.Context, Claim, string, Proof) error {
	store.completed = true
	return nil
}
func (store *fakeStore) Fail(context.Context, Claim, string, string, bool) error {
	store.failed = true
	return nil
}

func TestWorkerCompletesRuntimeProjection(t *testing.T) {
	tenantID := uuid.NewString()
	store := &fakeStore{
		claim: &Claim{ProjectionID: uuid.NewString(), TenantID: tenantID, DomainID: uuid.NewString(),
			ReleaseID: uuid.NewString(), Target: TargetSearch, SemanticVersion: "v1",
			ContentHash: contentHash([]byte("release")), LeaseToken: uuid.NewString(), Attempt: 1},
		proof: Proof{ContentHash: contentHash([]byte("release")), ResourceVersion: "v1", ObjectCount: 1, Detail: map[string]any{"count": 1}},
	}
	worker, err := NewWorker(store)
	if err != nil {
		t.Fatal(err)
	}
	processed, err := worker.ProcessNext(context.Background(), tenantID, TargetSearch, "worker-1", DefaultLease)
	if err != nil || !processed || !store.completed || store.failed {
		t.Fatalf("processed=%t completed=%t failed=%t err=%v", processed, store.completed, store.failed, err)
	}
}

func TestWorkerFailsClosedOnProjectionError(t *testing.T) {
	tenantID := uuid.NewString()
	store := &fakeStore{
		claim: &Claim{ProjectionID: uuid.NewString(), TenantID: tenantID, DomainID: uuid.NewString(),
			ReleaseID: uuid.NewString(), Target: TargetRegistry, SemanticVersion: "v1",
			ContentHash: contentHash([]byte("release")), LeaseToken: uuid.NewString(), Attempt: 1},
		err: ErrContract,
	}
	worker, _ := NewWorker(store)
	processed, err := worker.ProcessNext(context.Background(), tenantID, TargetRegistry, "worker-1", DefaultLease)
	if !processed || !store.failed || !errors.Is(err, ErrContract) {
		t.Fatalf("processed=%t failed=%t err=%v", processed, store.failed, err)
	}
}

func TestSearchDocumentTextUsesGovernedFields(t *testing.T) {
	document := searchDocumentText(map[string]any{
		"name": "销售金额", "code": "sales_amount",
		"aliases": []any{"销售额", "sales_amount"},
	})
	if document != "销售金额 sales_amount 销售额" {
		t.Fatalf("document = %q", document)
	}
}

func TestSearchDocumentShapeMatchesQuestionFacingObjectTypes(t *testing.T) {
	for _, fixture := range []struct {
		input, want string
	}{
		{"MEASURE", "METRIC"},
		{"METRIC", "METRIC"},
		{"SEMANTIC_MODEL", "SEMANTIC_MODEL"},
		{"DIMENSION", "DIMENSION"},
		{"BUSINESS_TERM", "BUSINESS_TERM"},
	} {
		got, _, ok := searchDocumentShape(fixture.input)
		if !ok || got != fixture.want {
			t.Fatalf("searchDocumentShape(%q) = (%q, %t), want %q", fixture.input, got, ok, fixture.want)
		}
	}
	if _, _, ok := searchDocumentShape("ENTITY"); ok {
		t.Fatal("ENTITY must not be projected into the question retrieval index")
	}
}

func TestSearchDocumentTextDoesNotInventLabelsForExecutableOnlyContracts(t *testing.T) {
	document := searchDocumentText(map[string]any{
		"type":       "METRIC",
		"formulaAst": map[string]any{"type": "MEASURE_REF", "measureVersionId": uuid.NewString()},
	})
	if document != "" {
		t.Fatalf("document = %q", document)
	}
}
