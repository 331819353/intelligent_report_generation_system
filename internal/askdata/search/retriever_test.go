package search

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

type retrievalStoreFixture struct {
	exact, lexical, vector []RawHit
	vectorErr              error
}

func (store retrievalStoreFixture) Exact(context.Context, askdata.PolicyScope, string, []ObjectType, int) ([]RawHit, error) {
	return store.exact, nil
}
func (store retrievalStoreFixture) Lexical(context.Context, askdata.PolicyScope, string, []ObjectType, int) ([]RawHit, error) {
	return store.lexical, nil
}
func (store retrievalStoreFixture) Vector(context.Context, askdata.PolicyScope, []float32, string, []ObjectType, int) ([]RawHit, error) {
	return store.vector, store.vectorErr
}

func TestMergeRRFCombinesEvidenceAndLimitsEachObjectType(t *testing.T) {
	metricHash := askdata.HashBytes([]byte("metric document"))
	dimensionHash := askdata.HashBytes([]byte("dimension document"))
	result, err := MergeRRF(
		[]RawHit{{ObjectType: ObjectMetric, ObjectVersionID: "metric-sales-v1", InputHash: metricHash, Score: 1}},
		[]RawHit{
			{ObjectType: ObjectDimension, ObjectVersionID: "dimension-region-v1", InputHash: dimensionHash, Score: 1.4},
			{ObjectType: ObjectMetric, ObjectVersionID: "metric-sales-v1", InputHash: metricHash, Score: 1.2},
		},
		[]RawHit{{ObjectType: ObjectMetric, ObjectVersionID: "metric-sales-v1", InputHash: metricHash, Score: 0.9}},
		RankConfig{RRFConstant: 60, ExactWeight: 4, LexicalWeight: 1, VectorWeight: 1, TopKPerType: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 || result[0].ObjectVersionID != "metric-sales-v1" || len(result[0].Evidence) != 3 {
		t.Fatalf("RRF result = %#v", result)
	}
	for _, evidence := range result[0].Evidence {
		if err := evidence.Evidence.Validate(); err != nil {
			t.Fatalf("evidence = %#v: %v", evidence, err)
		}
	}
}

func TestRetrieverDegradesToExactAndLexicalWhenVectorUnavailable(t *testing.T) {
	hash := askdata.HashBytes([]byte("metric document"))
	store := retrievalStoreFixture{
		exact:     []RawHit{{ObjectType: ObjectMetric, ObjectVersionID: "metric-sales-v1", InputHash: hash, Score: 1}},
		lexical:   []RawHit{{ObjectType: ObjectMetric, ObjectVersionID: "metric-sales-v1", InputHash: hash, Score: 1.3}},
		vectorErr: errors.New("vector index unavailable"),
	}
	retriever, err := NewRetriever(store, DefaultRankConfig())
	if err != nil {
		t.Fatal(err)
	}
	result, err := retriever.Retrieve(context.Background(), RetrievalRequest{
		Scope: testPolicyScope(t), Mention: "销售额", ObjectTypes: []ObjectType{ObjectMetric},
		Embedding: make([]float32, 2_560), EmbeddingModel: "Qwen3-Embedding-4B",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Degraded || result.DegradedReason != "VECTOR_RETRIEVAL_FAILED" ||
		len(result.Candidates) != 1 || len(result.Candidates[0].Evidence) != 2 {
		t.Fatalf("retrieval result = %#v", result)
	}

	result, err = retriever.Retrieve(context.Background(), RetrievalRequest{
		Scope: testPolicyScope(t), Mention: "销售额", ObjectTypes: []ObjectType{ObjectMetric},
	})
	if err != nil || result.DegradedReason != "EMBEDDING_UNAVAILABLE" {
		t.Fatalf("lexical-only result = %#v, %v", result, err)
	}
}

func TestRetrieverMergesGovernedDictionaryHitsIntoIndependentExactLane(t *testing.T) {
	retriever, err := NewRetriever(retrievalStoreFixture{}, DefaultRankConfig())
	if err != nil {
		t.Fatal(err)
	}
	hash := askdata.HashBytes([]byte("governed dictionary hit"))
	result, err := retriever.Retrieve(context.Background(), RetrievalRequest{
		Scope: testPolicyScope(t), Mention: "销售额", ObjectTypes: []ObjectType{ObjectMetric},
		DeterministicExact: []RawHit{{
			ObjectType: ObjectMetric, ObjectVersionID: "metric-governed-v1",
			InputHash: hash, Score: 1,
		}},
	})
	if err != nil || len(result.Candidates) != 1 || len(result.Candidates[0].Evidence) != 1 ||
		result.Candidates[0].Evidence[0].Source != SourceExact ||
		result.Candidates[0].ObjectVersionID != "metric-governed-v1" {
		t.Fatalf("dictionary exact result = %#v/%v", result, err)
	}
	_, err = retriever.Retrieve(context.Background(), RetrievalRequest{
		Scope: testPolicyScope(t), Mention: "销售额", ObjectTypes: []ObjectType{ObjectMetric},
		DeterministicExact: []RawHit{{
			ObjectType: ObjectMetric, ObjectVersionID: "metric-governed-v1",
			InputHash: hash, Score: 0.99,
		}},
	})
	if !errors.Is(err, ErrInvalidRetrieval) {
		t.Fatalf("untrusted exact score error = %v", err)
	}
}

func testPolicyScope(t *testing.T) askdata.PolicyScope {
	t.Helper()
	release := askdata.ReleaseRef{
		ReleaseID:   "11111111-1111-4111-8111-111111111111",
		ContentHash: askdata.HashBytes([]byte("release")),
	}
	scope, err := askdata.NewPolicyScope(
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
		[]askdata.ID{"44444444-4444-4444-8444-444444444444"},
		[]askdata.ID{"55555555-5555-4555-8555-555555555555"}, release,
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
