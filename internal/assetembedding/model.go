// Package assetembedding owns deterministic table/column search documents, their vectors,
// the transactional rebuild outbox and tenant-safe hybrid retrieval.
package assetembedding

import (
	"context"
	"errors"
	"time"
)

const (
	DocumentVersion = "asset-search-document-v1"
	MaxBatchSize    = 16
)

var ErrInvalidRequest = errors.New("asset embedding request is invalid")

type Claim struct {
	ID           string
	TenantID     string
	AssetType    string
	AssetID      string
	TableID      string
	EventVersion int64
}

type Document struct {
	Claim
	Text           string
	InputHash      string
	Current        bool
	Eligible       bool
	IneligibleCode string
}

type Rank struct {
	AssetID string
	TableID string
	Score   float64
}

type RetrievalResult struct {
	TableIDs       []string
	TableScores    map[string]float64
	ColumnScores   map[string]float64
	Degraded       bool
	DegradedReason string
	EmbeddingReady bool
}

type Store interface {
	ListTenantIDs(context.Context) ([]string, error)
	ClaimBatch(context.Context, string, string, time.Duration, int) ([]Claim, error)
	Prepare(context.Context, Claim, string) (Document, error)
	Acknowledge(context.Context, Document, string) error
	Complete(context.Context, Document, string, string, []float32) error
	Skip(context.Context, Document, string) error
	Fail(context.Context, Document, string, string) error
	VectorRanks(context.Context, string, string, []string, []float32, int) ([]Rank, error)
	LexicalRanks(context.Context, string, string, []string, string, []string, int) ([]Rank, error)
}
