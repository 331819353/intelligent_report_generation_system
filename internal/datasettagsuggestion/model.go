// Package datasettagsuggestion creates governed tag proposals for exact
// published dataset versions. It never approves tags and never reads source
// business rows.
package datasettagsuggestion

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	aiplatform "intelligent-report-generation-system/internal/ai"
)

const (
	PromptVersion      = "dataset-tag-suggestion-v1"
	MaxSuggestions     = 256
	MaxTaxonomyTags    = 1024
	MaxTaxonomyAliases = 4096
	MaxDatasetFields   = 1024
	MaxSourceTables    = 16
	MaxSourceColumns   = 2048
	MaxUpstreams       = 16
	MaxUpstreamFields  = 2048
	MaxInputBytes      = 192 << 10
	MaxRationaleRunes  = 1024
)

var (
	ErrInvalidRequest = errors.New("dataset tag suggestion request is invalid")
	ErrLeaseLost      = errors.New("dataset tag suggestion lease was lost")
	ErrSubjectChanged = errors.New("dataset tag suggestion subject changed")
	ErrInputLimit     = errors.New("dataset tag suggestion input exceeds safety limit")
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Claim struct {
	ID               string
	TenantID         string
	DatasetID        string
	DatasetVersionID string
	SchemaHash       string
	Layer            string
	PromptVersion    string
	ActorID          string
	LeaseToken       string
	Attempt          int
	MaxAttempts      int
}

type DatasetContext struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Layer       string `json:"layer"`
	Type        string `json:"type"`
	VersionID   string `json:"versionId"`
	SchemaHash  string `json:"schemaHash"`
}

type FieldContext struct {
	ID            string `json:"id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	Role          string `json:"role"`
	CanonicalType string `json:"canonicalType"`
	SemanticType  string `json:"semanticType"`
	Aggregation   string `json:"aggregation"`
	Nullable      bool   `json:"nullable"`
	Expression    string `json:"expression"`
}

type NodeContext struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Alias      string   `json:"alias"`
	Projection []string `json:"projection"`
}

type JoinContext struct {
	ID            string   `json:"id"`
	LeftNodeID    string   `json:"leftNodeId"`
	RightNodeID   string   `json:"rightNodeId"`
	JoinType      string   `json:"joinType"`
	Cardinality   string   `json:"cardinality"`
	ConditionRefs []string `json:"conditionRefs"`
}

type DAGContext struct {
	Nodes         []NodeContext `json:"nodes"`
	Joins         []JoinContext `json:"joins"`
	GroupBy       []string      `json:"groupBy"`
	OutputGrain   string        `json:"outputGrain"`
	OutputKeys    []string      `json:"outputKeys"`
	HasTransforms bool          `json:"hasTransforms"`
}

type SourceColumnContext struct {
	Name                string   `json:"name"`
	NativeType          string   `json:"nativeType"`
	CanonicalType       string   `json:"canonicalType"`
	SourceComment       string   `json:"sourceComment"`
	BusinessName        string   `json:"businessName"`
	BusinessDescription string   `json:"businessDescription"`
	SemanticType        string   `json:"semanticType"`
	Tags                []string `json:"tags"`
	PrimaryKey          bool     `json:"primaryKey"`
	ForeignKey          bool     `json:"foreignKey"`
	Unique              bool     `json:"unique"`
	Nullable            bool     `json:"nullable"`
}

type SourceTableContext struct {
	ID                  string                `json:"id"`
	DataSourceType      string                `json:"dataSourceType"`
	CatalogName         string                `json:"catalogName"`
	SchemaName          string                `json:"schemaName"`
	TableName           string                `json:"tableName"`
	TableType           string                `json:"tableType"`
	SourceComment       string                `json:"sourceComment"`
	BusinessName        string                `json:"businessName"`
	BusinessDescription string                `json:"businessDescription"`
	Tags                []string              `json:"tags"`
	PrimaryKeys         []string              `json:"primaryKeys"`
	Columns             []SourceColumnContext `json:"columns"`
}

type UpstreamContext struct {
	DatasetID    string         `json:"datasetId"`
	VersionID    string         `json:"versionId"`
	SchemaHash   string         `json:"schemaHash"`
	Layer        string         `json:"layer"`
	Code         string         `json:"code"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	OutputGrain  string         `json:"outputGrain"`
	Fields       []FieldContext `json:"fields"`
	ApprovedTags []string       `json:"approvedTags"`
}

type TaxonomyTag struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Category    string   `json:"category"`
	Aliases     []string `json:"aliases"`
}

type Input struct {
	Dataset      DatasetContext       `json:"dataset"`
	Fields       []FieldContext       `json:"fields"`
	DAG          DAGContext           `json:"dag"`
	SourceTables []SourceTableContext `json:"sourceTables"`
	Upstreams    []UpstreamContext    `json:"upstreams"`
	Taxonomy     []TaxonomyTag        `json:"controlledTaxonomy"`
}

type Suggestion struct {
	TagID      string  `json:"tagId"`
	TagCode    string  `json:"tagCode"`
	TagName    string  `json:"tagName"`
	Category   string  `json:"category"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}

type Completion struct {
	AIRequestID string
	InputHash   string
	OutputHash  string
	Suggestions []Suggestion
}

type Store interface {
	ListTenantIDs(context.Context) ([]string, error)
	ClaimNext(context.Context, string, string, time.Duration) (*Claim, error)
	Heartbeat(context.Context, Claim, string, time.Duration) error
	LoadInput(context.Context, Claim, string) (Input, error)
	Complete(context.Context, Claim, string, Completion) error
	Skip(context.Context, Claim, string, string) error
	Fail(context.Context, Claim, string, string, bool) error
}

// Invoker is implemented by the common AI orchestration service. Its audit
// store records hashes and usage only; prompts and model output are not stored.
type Invoker interface {
	Configured() bool
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type providerOutput struct {
	Items []providerSuggestion `json:"items"`
}

type providerSuggestion struct {
	TagID      string  `json:"tagId"`
	Confidence float64 `json:"confidence"`
	Rationale  string  `json:"rationale"`
}

func canonicalOutput(suggestions []Suggestion) ([]byte, error) {
	return json.Marshal(struct {
		PromptVersion string       `json:"promptVersion"`
		Items         []Suggestion `json:"items"`
	}{PromptVersion: PromptVersion, Items: suggestions})
}

func validClaim(claim Claim) bool {
	return uuid.Validate(claim.ID) == nil &&
		uuid.Validate(claim.TenantID) == nil &&
		uuid.Validate(claim.DatasetID) == nil &&
		uuid.Validate(claim.DatasetVersionID) == nil &&
		uuid.Validate(claim.LeaseToken) == nil &&
		sha256Pattern.MatchString(claim.SchemaHash) &&
		(claim.Layer == "ODS" || claim.Layer == "DWD" || claim.Layer == "DWS") &&
		strings.TrimSpace(claim.PromptVersion) == PromptVersion &&
		claim.Attempt >= 1 && claim.Attempt <= claim.MaxAttempts &&
		claim.MaxAttempts >= 1 && claim.MaxAttempts <= 5
}
