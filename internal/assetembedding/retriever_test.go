package assetembedding

import (
	"context"
	"testing"
	"time"
)

type retrievalStore struct {
	vector  map[string][]Rank
	lexical map[string][]Rank
	tables  []string
}

func (s *retrievalStore) ListTenantIDs(context.Context) ([]string, error) { return nil, nil }
func (s *retrievalStore) ClaimBatch(context.Context, string, string, time.Duration, int) ([]Claim, error) {
	return nil, nil
}
func (s *retrievalStore) Prepare(context.Context, Claim, string) (Document, error) {
	return Document{}, nil
}
func (s *retrievalStore) Acknowledge(context.Context, Document, string) error { return nil }
func (s *retrievalStore) Complete(context.Context, Document, string, string, []float32) error {
	return nil
}
func (s *retrievalStore) Skip(context.Context, Document, string) error         { return nil }
func (s *retrievalStore) Fail(context.Context, Document, string, string) error { return nil }
func (s *retrievalStore) VectorRanks(_ context.Context, _ string, assetType string, tableIDs []string, _ []float32, _ int) ([]Rank, error) {
	if assetType == "COLUMN" {
		s.tables = append([]string(nil), tableIDs...)
	}
	return s.vector[assetType], nil
}
func (s *retrievalStore) LexicalRanks(_ context.Context, _ string, assetType string, tableIDs []string, _ string, _ []string, _ int) ([]Rank, error) {
	if assetType == "COLUMN" {
		s.tables = append([]string(nil), tableIDs...)
	}
	return s.lexical[assetType], nil
}

type retrievalProvider struct{ configured bool }

func (p retrievalProvider) Configured() bool { return p.configured }
func (p retrievalProvider) Model() string    { return "test" }
func (p retrievalProvider) Dimensions() int  { return 2 }
func (p retrievalProvider) Embed(context.Context, []string) ([][]float32, error) {
	return [][]float32{{0.1, 0.2}}, nil
}

func TestRetrieverUsesRRFAndRestrictsColumnSearch(t *testing.T) {
	store := &retrievalStore{
		vector: map[string][]Rank{
			"TABLE":  {{AssetID: "sales"}, {AssetID: "stores"}},
			"COLUMN": {{AssetID: "sales_amount"}, {AssetID: "region_name"}},
		},
		lexical: map[string][]Rank{
			"TABLE":  {{AssetID: "regions"}, {AssetID: "sales"}},
			"COLUMN": {{AssetID: "region_name"}, {AssetID: "order_date"}},
		},
	}
	result, err := NewRetriever(store, retrievalProvider{configured: true}).Retrieve(context.Background(), "tenant", "每个月各区域销售", []string{"required"}, 12, 160)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.TableIDs) != 3 || result.TableIDs[0] != "sales" || result.Degraded || !result.EmbeddingReady {
		t.Fatalf("result=%#v", result)
	}
	want := map[string]bool{"sales": true, "stores": true, "regions": true, "required": true}
	for _, id := range store.tables {
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("column search missed selected tables: %#v", want)
	}
	if result.ColumnScores["region_name"] <= result.ColumnScores["sales_amount"] {
		t.Fatal("RRF did not reward a column present in both rankings")
	}
}

func TestRetrieverFallsBackToChineseLexicalTokens(t *testing.T) {
	store := &retrievalStore{vector: map[string][]Rank{}, lexical: map[string][]Rank{"TABLE": {{AssetID: "regions"}}, "COLUMN": {{AssetID: "region_name"}}}}
	result, err := NewRetriever(store, retrievalProvider{}).Retrieve(context.Background(), "tenant", "每个月各区域的销售情况", nil, 12, 160)
	if err != nil || !result.Degraded || result.TableIDs[0] != "regions" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	tokens := queryTokens("每个月各区域的销售情况")
	for _, expected := range []string{"区域", "销售"} {
		found := false
		for _, token := range tokens {
			found = found || token == expected
		}
		if !found {
			t.Fatalf("missing Chinese token %q in %#v", expected, tokens)
		}
	}
}
