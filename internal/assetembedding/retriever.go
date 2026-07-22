package assetembedding

import (
	"context"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/embedding"
)

const rrfK = 60.0

type Retriever struct {
	store    Store
	provider embedding.Provider
}

func NewRetriever(store Store, provider embedding.Provider) *Retriever {
	return &Retriever{store: store, provider: provider}
}

// Retrieve performs table-first hybrid retrieval and only ranks columns inside selected tables.
func (r *Retriever) Retrieve(ctx context.Context, tenantID, query string, requiredTableIDs []string, tableLimit, columnLimit int) (RetrievalResult, error) {
	query = strings.TrimSpace(query)
	if r == nil || r.store == nil || tenantID == "" || query == "" || tableLimit < 1 || tableLimit > 64 || columnLimit < 1 || columnLimit > 256 {
		return RetrievalResult{}, ErrInvalidRequest
	}
	result := RetrievalResult{
		TableScores: map[string]float64{}, ColumnScores: map[string]float64{},
		Degraded: true, DegradedReason: "EMBEDDING_NOT_CONFIGURED",
	}
	tokens := queryTokens(query)
	lexicalTables, err := r.store.LexicalRanks(ctx, tenantID, "TABLE", nil, query, tokens, 32)
	if err != nil {
		return RetrievalResult{}, err
	}
	var vector []float32
	if r.provider != nil && r.provider.Configured() {
		vectors, embedErr := r.provider.Embed(ctx, []string{query})
		if embedErr == nil && len(vectors) == 1 {
			vector = vectors[0]
			result.EmbeddingReady = true
			result.DegradedReason = ""
		} else {
			result.DegradedReason = "QUERY_EMBEDDING_FAILED"
		}
	}
	vectorTables := []Rank{}
	if len(vector) > 0 {
		if ranked, vectorErr := r.store.VectorRanks(ctx, tenantID, "TABLE", nil, vector, 32); vectorErr == nil {
			vectorTables = ranked
			if len(ranked) == 0 {
				result.DegradedReason = "TABLE_VECTOR_COVERAGE_EMPTY"
			}
		} else {
			result.DegradedReason = "TABLE_VECTOR_QUERY_FAILED"
		}
	}
	tableOrder, tableScores := mergeRRF(vectorTables, lexicalTables, tableLimit)
	result.TableIDs, result.TableScores = tableOrder, tableScores
	result.Degraded = len(vector) == 0 || len(vectorTables) == 0
	if !result.Degraded {
		result.DegradedReason = ""
	}

	selected := uniqueStrings(append(append([]string(nil), tableOrder...), requiredTableIDs...))
	if len(selected) == 0 {
		return result, nil
	}
	lexicalColumns, err := r.store.LexicalRanks(ctx, tenantID, "COLUMN", selected, query, tokens, columnLimit)
	if err != nil {
		return RetrievalResult{}, err
	}
	vectorColumns := []Rank{}
	if len(vector) > 0 {
		if ranked, vectorErr := r.store.VectorRanks(ctx, tenantID, "COLUMN", selected, vector, columnLimit); vectorErr == nil {
			vectorColumns = ranked
		} else {
			result.Degraded = true
			result.DegradedReason = "COLUMN_VECTOR_QUERY_FAILED"
		}
	}
	_, result.ColumnScores = mergeRRF(vectorColumns, lexicalColumns, columnLimit)
	if len(vectorColumns) == 0 {
		result.Degraded = true
		if result.DegradedReason == "" {
			result.DegradedReason = "COLUMN_VECTOR_COVERAGE_EMPTY"
		}
	}
	return result, nil
}

func mergeRRF(vectorRanks, lexicalRanks []Rank, limit int) ([]string, map[string]float64) {
	scores := map[string]float64{}
	for _, ranks := range [][]Rank{vectorRanks, lexicalRanks} {
		for index, item := range ranks {
			scores[item.AssetID] += 1.0 / (rrfK + float64(index+1))
		}
	}
	type scored struct {
		id    string
		score float64
	}
	values := make([]scored, 0, len(scores))
	for id, score := range scores {
		values = append(values, scored{id: id, score: score})
	}
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].score != values[j].score {
			return values[i].score > values[j].score
		}
		return values[i].id < values[j].id
	})
	if len(values) > limit {
		values = values[:limit]
	}
	order := make([]string, len(values))
	for index, value := range values {
		order[index] = value.id
	}
	return order, scores
}

func queryTokens(value string) []string {
	chunks := strings.FieldsFunc(strings.ToLower(value), func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsPunct(character) || unicode.IsSymbol(character)
	})
	result := []string{}
	seen := map[string]bool{}
	add := func(token string) {
		token = strings.TrimSpace(token)
		if utf8.RuneCountInString(token) < 2 || seen[token] || len(result) >= 64 {
			return
		}
		seen[token] = true
		result = append(result, token)
	}
	for _, chunk := range chunks {
		add(chunk)
		runes := []rune(chunk)
		for index := 0; index+1 < len(runes); index++ {
			if unicode.Is(unicode.Han, runes[index]) && unicode.Is(unicode.Han, runes[index+1]) {
				add(string(runes[index : index+2]))
			}
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
