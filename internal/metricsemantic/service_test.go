package metricsemantic

import (
	"context"
	"errors"
	"testing"
	"time"
)

type embeddingProviderStub struct {
	configured bool
	err        error
	vector     []float32
}

func (s embeddingProviderStub) Configured() bool { return s.configured }
func (s embeddingProviderStub) Model() string    { return "embedding-model" }
func (s embeddingProviderStub) Dimensions() int  { return 3 }
func (s embeddingProviderStub) Embed(context.Context, []string) ([][]float32, error) {
	if s.err != nil {
		return nil, s.err
	}
	return [][]float32{append([]float32(nil), s.vector...)}, nil
}

type semanticStoreStub struct {
	claim        *EmbeddingClaim
	searchVector []float32
	completed    bool
	failed       bool
}

func (s *semanticStoreStub) ListPendingTenantIDs(context.Context) ([]string, error) {
	return []string{"tenant-1"}, nil
}
func (s *semanticStoreStub) Claim(context.Context, string, string, time.Duration) (*EmbeddingClaim, error) {
	return s.claim, nil
}
func (s *semanticStoreStub) Complete(_ context.Context, _ EmbeddingClaim, _, _ string, _ []float32) error {
	s.completed = true
	return nil
}
func (s *semanticStoreStub) Fail(context.Context, EmbeddingClaim, string, string) error {
	s.failed = true
	return nil
}
func (s *semanticStoreStub) Search(_ context.Context, _ string, _ string, vector []float32, _ int, _ bool) ([]SearchResult, error) {
	s.searchVector = append([]float32(nil), vector...)
	return []SearchResult{{SubjectType: "METRIC_VERSION", Name: "销售额", BindingAllowed: true}}, nil
}

func TestSearchUsesVectorAndDegradesWithoutProvider(t *testing.T) {
	store := &semanticStoreStub{}
	service := NewService(store, embeddingProviderStub{configured: true, vector: []float32{0.1, 0.2, 0.3}})
	response, err := service.Search(context.Background(), "tenant-1", "月度销售", 10, false)
	if err != nil || response.Degraded || len(store.searchVector) != 3 || len(response.Items) != 1 {
		t.Fatalf("vector response=%#v err=%v vector=%v", response, err, store.searchVector)
	}
	service = NewService(store, embeddingProviderStub{configured: true, err: errors.New("temporary")})
	response, err = service.Search(context.Background(), "tenant-1", "月度销售", 10, false)
	if err != nil || !response.Degraded || len(store.searchVector) != 0 {
		t.Fatalf("fallback response=%#v err=%v vector=%v", response, err, store.searchVector)
	}
}

func TestEmbeddingWorkerCompletesClaim(t *testing.T) {
	store := &semanticStoreStub{claim: &EmbeddingClaim{ID: "doc-1", TenantID: "tenant-1", Document: "指标名称：销售额"}}
	worker := NewWorker(store, embeddingProviderStub{configured: true, vector: []float32{0.1, 0.2, 0.3}})
	handled, err := worker.ProcessNext(context.Background(), "tenant-1", "worker-1", time.Minute)
	if err != nil || !handled || !store.completed || store.failed {
		t.Fatalf("handled=%v err=%v completed=%v failed=%v", handled, err, store.completed, store.failed)
	}
}
