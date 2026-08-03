package semanticgraph

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidRequest   = errors.New("invalid semantic graph request")
	ErrGraphUnavailable = errors.New("semantic graph unavailable")
	ErrNoCertifiedPath  = errors.New("no certified semantic graph path")
	ErrProjectionLease  = errors.New("semantic graph projection lease lost")
)

const (
	MaximumOnlineHops       = 4
	MaximumOnlineCandidates = 30
	MaximumJoinPaths        = 3
)

// Scope pins every graph read to the same tenant and immutable semantic
// release as the query plan. A response from another version is never usable.
type Scope struct {
	TenantID        string
	SemanticVersion string
	ContentHash     string
	RoleIDs         []string
	Purpose         string
	EffectiveAt     time.Time
}

type Evidence struct {
	Source          string    `json:"source"`
	SemanticVersion string    `json:"semanticVersion"`
	ContentHash     string    `json:"contentHash"`
	EvidenceID      string    `json:"evidenceId"`
	ObservedAt      time.Time `json:"observedAt"`
	Cached          bool      `json:"cached"`
}

type Candidate struct {
	VID          string `json:"vid"`
	RelationType string `json:"relationType"`
}

type ValueOwnershipRequest struct {
	DimensionVID string
	ValueVID     string
}

type ValueOwnership struct {
	DimensionVID string `json:"dimensionVid"`
	ValueVID     string `json:"valueVid"`
	Certified    bool   `json:"certified"`
	Hierarchy    string `json:"hierarchy,omitempty"`
}

type ValueBinding struct {
	DimensionVID string `json:"dimensionVid"`
	ValueVID     string `json:"valueVid"`
}

type Bundle struct {
	MetricVIDs    []string       `json:"metricVids"`
	DimensionVIDs []string       `json:"dimensionVids"`
	Values        []ValueBinding `json:"values"`
}

type BundleValidation struct {
	Valid       bool     `json:"valid"`
	Conflicts   []string `json:"conflicts"`
	JoinPathIDs []string `json:"joinPathIds"`
}

type JoinPathRequest struct {
	FactDatasetVID      string
	DimensionDatasetVID string
	MaxHops             int
	Limit               int
}

type JoinEdge struct {
	RelationID         string  `json:"relationId"`
	FromVID            string  `json:"fromVid"`
	ToVID              string  `json:"toVid"`
	Cardinality        string  `json:"cardinality"`
	Certified          bool    `json:"certified"`
	AllowedForQuery    bool    `json:"allowedForQuery"`
	BaseCost           float64 `json:"baseCost"`
	FanoutPenalty      float64 `json:"fanoutPenalty"`
	StalePenalty       float64 `json:"stalePenalty"`
	CrossSourcePenalty float64 `json:"crossSourcePenalty"`
	PolicyPenalty      float64 `json:"policyPenalty"`
}

type JoinPath struct {
	VIDs     []string   `json:"vids"`
	Edges    []JoinEdge `json:"edges"`
	Cost     float64    `json:"cost"`
	PathHash string     `json:"pathHash"`
}

type AuthorizationRequest struct {
	RoleVIDs      []string
	CandidateVIDs []string
}

type ImpactRequest struct {
	ChangedVIDs []string
	MaxHops     int
	Limit       int
}

type ImpactedObject struct {
	VID          string `json:"vid"`
	RelationType string `json:"relationType"`
	Distance     int    `json:"distance"`
}

// Graph exposes the six bounded online uses required by the implementation
// plan. Implementations must fail closed on version, permission or graph
// availability mismatches.
type Graph interface {
	ExpandCandidates(context.Context, Scope, []string) ([]Candidate, Evidence, error)
	ValidateValueOwnership(context.Context, Scope, ValueOwnershipRequest) (ValueOwnership, Evidence, error)
	ValidateBundle(context.Context, Scope, Bundle) (BundleValidation, Evidence, error)
	FindJoinPaths(context.Context, Scope, JoinPathRequest) ([]JoinPath, Evidence, error)
	FilterAuthorized(context.Context, Scope, AuthorizationRequest) ([]string, Evidence, error)
	ImpactAnalysis(context.Context, Scope, ImpactRequest) ([]ImpactedObject, Evidence, error)
}

type CachedGraphPlan struct {
	Scope       Scope           `json:"scope"`
	RequestHash string          `json:"requestHash"`
	Paths       []JoinPath      `json:"paths"`
	Evidence    Evidence        `json:"evidence"`
	Certified   bool            `json:"certified"`
	ExpiresAt   time.Time       `json:"expiresAt"`
	Detail      json.RawMessage `json:"detail,omitempty"`
}

type PlanCache interface {
	Get(context.Context, Scope, string) (CachedGraphPlan, bool, error)
	Put(context.Context, CachedGraphPlan) error
}
