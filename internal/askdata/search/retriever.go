package search

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrInvalidRetrieval = errors.New("semantic retrieval request is invalid")

type RetrievalStore interface {
	Exact(context.Context, askdata.PolicyScope, string, []ObjectType, int) ([]RawHit, error)
	Lexical(context.Context, askdata.PolicyScope, string, []ObjectType, int) ([]RawHit, error)
	Vector(context.Context, askdata.PolicyScope, []float32, string, []ObjectType, int) ([]RawHit, error)
}

type RetrievalRequest struct {
	Scope          askdata.PolicyScope
	Mention        string
	ObjectTypes    []ObjectType
	Embedding      []float32
	EmbeddingModel string
	TopKPerType    int
}

type RetrievalResult struct {
	Candidates     []Candidate `json:"candidates"`
	Degraded       bool        `json:"degraded"`
	DegradedReason string      `json:"degradedReason,omitempty"`
}

type Retriever struct {
	store RetrievalStore
	rank  RankConfig
}

func NewRetriever(store RetrievalStore, rank RankConfig) (*Retriever, error) {
	if store == nil {
		return nil, errors.New("retrieval store is required")
	}
	if rank.RRFConstant == 0 {
		rank = DefaultRankConfig()
	}
	if err := rank.Validate(); err != nil {
		return nil, err
	}
	return &Retriever{store: store, rank: rank}, nil
}

func (retriever *Retriever) Retrieve(ctx context.Context, request RetrievalRequest) (RetrievalResult, error) {
	if retriever == nil || retriever.store == nil {
		return RetrievalResult{}, ErrInvalidRetrieval
	}
	if err := request.Scope.Validate(); err != nil {
		return RetrievalResult{}, fmt.Errorf("%w: scope: %v", ErrInvalidRetrieval, err)
	}
	mention, err := normalizeText(request.Mention, 512)
	if err != nil || mention == "" {
		return RetrievalResult{}, ErrInvalidRetrieval
	}
	mention = strings.ToLower(mention)
	objectTypes := append([]ObjectType(nil), request.ObjectTypes...)
	if len(objectTypes) == 0 || len(objectTypes) > 5 {
		return RetrievalResult{}, ErrInvalidRetrieval
	}
	sort.Slice(objectTypes, func(i, j int) bool { return objectTypes[i] < objectTypes[j] })
	for index, objectType := range objectTypes {
		if !validRetrievalObjectType(objectType) || index > 0 && objectTypes[index-1] == objectType {
			return RetrievalResult{}, ErrInvalidRetrieval
		}
	}
	topK := request.TopKPerType
	if topK == 0 {
		topK = retriever.rank.TopKPerType
	}
	if topK < 1 || topK > 100 {
		return RetrievalResult{}, ErrInvalidRetrieval
	}
	queryLimit := min(topK*3, 100)
	exact, err := retriever.store.Exact(ctx, request.Scope, mention, objectTypes, queryLimit)
	if err != nil {
		return RetrievalResult{}, err
	}
	lexical, err := retriever.store.Lexical(ctx, request.Scope, mention, objectTypes, queryLimit)
	if err != nil {
		return RetrievalResult{}, err
	}
	var vector []RawHit
	result := RetrievalResult{}
	if len(request.Embedding) == 0 || strings.TrimSpace(request.EmbeddingModel) == "" {
		result.Degraded = true
		result.DegradedReason = "EMBEDDING_UNAVAILABLE"
	} else if len(request.Embedding) != 2_560 {
		return RetrievalResult{}, ErrInvalidRetrieval
	} else {
		vector, err = retriever.store.Vector(
			ctx, request.Scope, request.Embedding, strings.TrimSpace(request.EmbeddingModel), objectTypes, queryLimit,
		)
		if err != nil {
			result.Degraded = true
			result.DegradedReason = "VECTOR_RETRIEVAL_FAILED"
			vector = nil
		}
	}
	rank := retriever.rank
	rank.TopKPerType = topK
	result.Candidates, err = MergeRRF(exact, lexical, vector, rank)
	if err != nil {
		return RetrievalResult{}, err
	}
	return result, nil
}
