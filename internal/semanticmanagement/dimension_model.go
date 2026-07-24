package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrRefreshPolicySkipped = errors.New("dimension member refresh policy skips automatic discovery")
	ErrRefreshCardinality   = errors.New("dimension member cardinality exceeds the configured maximum")
	ErrRefreshTimeout       = errors.New("dimension member refresh timed out")
	ErrRefreshUnsafeView    = errors.New("dimension member refresh view is not trusted")
	ErrRefreshInvalidValue  = errors.New("dimension member value cannot be indexed")
	ErrRefreshSourceChanged = errors.New("dimension member refresh source changed")
	ErrRefreshLeaseLost     = errors.New("dimension member refresh lease was lost")
	ErrIdempotencyConflict  = errors.New("dimension member refresh idempotency conflict")
	ErrMemberAccessDenied   = errors.New("dimension member access is denied by dataset access or data policy")
	ErrProfileLeaseLost     = errors.New("dimension profile lease was lost")
	ErrProfileSourceChanged = errors.New("dimension profile source changed")
	ErrProfileTimeout       = errors.New("dimension profile timed out")
	ErrProfileResourceLimit = errors.New("dimension profile exceeded its resource limit")
	ErrProfileUnsafeView    = errors.New("dimension profile view is not trusted")
)

type Dimension struct {
	ID                      string     `json:"id"`
	DatasetID               string     `json:"datasetId"`
	DatasetVersionID        string     `json:"datasetVersionId"`
	FieldID                 string     `json:"fieldId"`
	FieldCode               string     `json:"fieldCode"`
	Code                    string     `json:"code"`
	Name                    string     `json:"name"`
	Description             string     `json:"description"`
	DimensionType           string     `json:"dimensionType"`
	MemberIndexPolicy       string     `json:"memberIndexPolicy"`
	HighCardinality         bool       `json:"highCardinality"`
	Sensitive               bool       `json:"sensitive"`
	Status                  string     `json:"status"`
	DefinitionHash          string     `json:"definitionHash"`
	Version                 int64      `json:"version"`
	MemberRefreshGeneration string     `json:"memberRefreshGeneration,omitempty"`
	MemberCount             *int64     `json:"memberCount,omitempty"`
	MemberRefreshedAt       *time.Time `json:"memberRefreshedAt,omitempty"`
	LastMemberRefreshJobID  string     `json:"lastMemberRefreshJobId,omitempty"`
	CreatedBy               string     `json:"createdBy"`
	UpdatedBy               string     `json:"updatedBy"`
	CreatedAt               time.Time  `json:"createdAt"`
	UpdatedAt               time.Time  `json:"updatedAt"`
}

type DimensionFilter struct {
	Page
	Query            string
	DatasetVersionID string
	DimensionType    string
	Status           string
}

type CreateDimensionInput struct {
	DatasetID         string `json:"datasetId"`
	DatasetVersionID  string `json:"datasetVersionId"`
	FieldID           string `json:"fieldId"`
	Code              string `json:"code"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	DimensionType     string `json:"dimensionType"`
	MemberIndexPolicy string `json:"memberIndexPolicy"`
	HighCardinality   bool   `json:"highCardinality"`
	Sensitive         bool   `json:"sensitive"`
	Status            string `json:"status"`
}

type UpdateDimensionInput struct {
	ExpectedVersion   int64  `json:"expectedVersion"`
	Code              string `json:"code"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	DimensionType     string `json:"dimensionType"`
	MemberIndexPolicy string `json:"memberIndexPolicy"`
	HighCardinality   bool   `json:"highCardinality"`
	Sensitive         bool   `json:"sensitive"`
	Status            string `json:"status"`
}

type PreparedDimension struct {
	CreateDimensionInput
	DefinitionHash string
}

type DimensionMember struct {
	ID                string     `json:"id"`
	DimensionID       string     `json:"dimensionId"`
	MemberKey         string     `json:"memberKey"`
	CanonicalLabel    string     `json:"canonicalLabel"`
	NormalizedValue   string     `json:"normalizedValue"`
	Status            string     `json:"status"`
	FirstSeenAt       time.Time  `json:"firstSeenAt"`
	LastSeenAt        time.Time  `json:"lastSeenAt"`
	ValidFrom         *time.Time `json:"validFrom,omitempty"`
	ValidTo           *time.Time `json:"validTo,omitempty"`
	RefreshGeneration string     `json:"refreshGeneration,omitempty"`
	LastRefreshJobID  string     `json:"lastRefreshJobId,omitempty"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type DimensionMemberFilter struct {
	Page
	DimensionID string
	Query       string
	Status      string
}

type DimensionMemberAlias struct {
	ID                string     `json:"id"`
	DimensionID       string     `json:"dimensionId"`
	DimensionMemberID string     `json:"dimensionMemberId"`
	Alias             string     `json:"alias"`
	NormalizedAlias   string     `json:"normalizedAlias"`
	AliasType         string     `json:"aliasType"`
	ValidFrom         *time.Time `json:"validFrom,omitempty"`
	ValidTo           *time.Time `json:"validTo,omitempty"`
	Version           int64      `json:"version"`
	CreatedBy         string     `json:"createdBy,omitempty"`
	UpdatedBy         string     `json:"updatedBy,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type DimensionMemberAliasFilter struct {
	Page
	DimensionID       string
	DimensionMemberID string
	Query             string
	AliasType         string
}

type CreateDimensionMemberAliasInput struct {
	DimensionID       string     `json:"dimensionId"`
	DimensionMemberID string     `json:"dimensionMemberId"`
	Alias             string     `json:"alias"`
	AliasType         string     `json:"aliasType"`
	ValidFrom         *time.Time `json:"validFrom,omitempty"`
	ValidTo           *time.Time `json:"validTo,omitempty"`
}

type UpdateDimensionMemberAliasInput struct {
	ExpectedVersion int64      `json:"expectedVersion"`
	Alias           string     `json:"alias"`
	AliasType       string     `json:"aliasType"`
	ValidFrom       *time.Time `json:"validFrom,omitempty"`
	ValidTo         *time.Time `json:"validTo,omitempty"`
}

type DimensionMetricCompatibility struct {
	ID                     string          `json:"id"`
	DimensionID            string          `json:"dimensionId"`
	MetricID               string          `json:"metricId"`
	MetricVersionID        string          `json:"metricVersionId"`
	MetricDatasetVersionID string          `json:"metricDatasetVersionId"`
	CompatibilityType      string          `json:"compatibilityType"`
	FanoutPolicy           string          `json:"fanoutPolicy"`
	JoinPath               json.RawMessage `json:"joinPath"`
	EvidenceSource         string          `json:"evidenceSource"`
	Confidence             *float64        `json:"confidence,omitempty"`
	Status                 string          `json:"status"`
	Version                int64           `json:"version"`
	VerifiedBy             string          `json:"verifiedBy,omitempty"`
	VerifiedAt             *time.Time      `json:"verifiedAt,omitempty"`
	CreatedBy              string          `json:"createdBy,omitempty"`
	UpdatedBy              string          `json:"updatedBy,omitempty"`
	CreatedAt              time.Time       `json:"createdAt"`
	UpdatedAt              time.Time       `json:"updatedAt"`
}

type CompatibilityFilter struct {
	Page
	DimensionID     string
	MetricVersionID string
	Status          string
}

type ProposeCompatibilityInput struct {
	DimensionID            string          `json:"dimensionId"`
	MetricID               string          `json:"metricId"`
	MetricVersionID        string          `json:"metricVersionId"`
	MetricDatasetVersionID string          `json:"metricDatasetVersionId"`
	CompatibilityType      string          `json:"compatibilityType"`
	FanoutPolicy           string          `json:"fanoutPolicy"`
	JoinPath               json.RawMessage `json:"joinPath"`
	EvidenceSource         string          `json:"evidenceSource"`
	Confidence             *float64        `json:"confidence,omitempty"`
}

type UpdateCompatibilityInput struct {
	ExpectedVersion   int64           `json:"expectedVersion"`
	CompatibilityType string          `json:"compatibilityType"`
	FanoutPolicy      string          `json:"fanoutPolicy"`
	JoinPath          json.RawMessage `json:"joinPath"`
	EvidenceSource    string          `json:"evidenceSource"`
	Confidence        *float64        `json:"confidence,omitempty"`
}

type CompatibilityDecisionInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type RefreshJob struct {
	ID                string     `json:"id"`
	DimensionID       string     `json:"dimensionId"`
	DimensionVersion  int64      `json:"dimensionVersion"`
	DatasetID         string     `json:"datasetId"`
	DatasetVersionID  string     `json:"datasetVersionId"`
	FieldID           string     `json:"fieldId"`
	FieldCode         string     `json:"fieldCode"`
	MemberIndexPolicy string     `json:"memberIndexPolicy"`
	MaterializationID string     `json:"materializationId,omitempty"`
	RefreshGeneration string     `json:"refreshGeneration"`
	Status            string     `json:"status"`
	MaxMembers        int        `json:"maxMembers"`
	TimeoutSeconds    int        `json:"timeoutSeconds"`
	RequestHash       string     `json:"requestHash"`
	RequestedBy       string     `json:"requestedBy"`
	Attempt           int        `json:"attempt"`
	MaxAttempts       int        `json:"maxAttempts"`
	MemberCount       *int64     `json:"memberCount,omitempty"`
	ResultCode        string     `json:"resultCode,omitempty"`
	ErrorMessage      string     `json:"errorMessage,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	StartedAt         *time.Time `json:"startedAt,omitempty"`
	CompletedAt       *time.Time `json:"completedAt,omitempty"`
}

type RefreshJobFilter struct {
	Page
	DimensionID string
	Status      string
}

type CreateRefreshJobInput struct {
	ExpectedDimensionVersion int64 `json:"expectedDimensionVersion"`
	MaxMembers               int   `json:"maxMembers,omitempty"`
	TimeoutSeconds           int   `json:"timeoutSeconds,omitempty"`
}

type PreparedRefreshJob struct {
	CreateRefreshJobInput
	DimensionID    string
	RequestHash    string
	IdempotencyKey string
}

type DimensionRefreshClaim struct {
	RefreshJob
	TenantID       string
	LeaseOwner     string
	LeaseToken     string
	LeaseExpiresAt time.Time
}

type MemberMetricSearchResult struct {
	MatchedValue      string `json:"matchedValue"`
	MatchType         string `json:"matchType"`
	DimensionID       string `json:"dimensionId"`
	DimensionCode     string `json:"dimensionCode"`
	DimensionName     string `json:"dimensionName"`
	DimensionMemberID string `json:"dimensionMemberId"`
	MemberKey         string `json:"memberKey"`
	CanonicalLabel    string `json:"canonicalLabel"`
	MetricID          string `json:"metricId"`
	MetricVersionID   string `json:"metricVersionId"`
	MetricCode        string `json:"metricCode"`
	MetricName        string `json:"metricName"`
	DatasetID         string `json:"datasetId"`
	DatasetVersionID  string `json:"datasetVersionId"`
	DatasetCode       string `json:"datasetCode"`
	DatasetName       string `json:"datasetName"`
	CompatibilityType string `json:"compatibilityType"`
	FanoutPolicy      string `json:"fanoutPolicy"`
	PublishedSchema   string `json:"publishedSchema"`
	PublishedName     string `json:"publishedName"`
}

type DimensionStore interface {
	ListDimensions(context.Context, string, DimensionFilter) ([]Dimension, int, error)
	GetDimension(context.Context, string, string) (Dimension, error)
	CreateDimension(context.Context, string, string, PreparedDimension) (Dimension, error)
	UpdateDimension(context.Context, string, string, string, int64, PreparedDimension) (Dimension, error)
	DeprecateDimension(context.Context, string, string, string, int64) (Dimension, error)

	ListDimensionMembers(context.Context, string, string, DimensionMemberFilter) ([]DimensionMember, int, error)
	ListDimensionMemberAliases(context.Context, string, string, DimensionMemberAliasFilter) ([]DimensionMemberAlias, int, error)
	CreateDimensionMemberAlias(context.Context, string, string, CreateDimensionMemberAliasInput, string) (DimensionMemberAlias, error)
	UpdateDimensionMemberAlias(context.Context, string, string, string, UpdateDimensionMemberAliasInput, string) (DimensionMemberAlias, error)
	DeleteDimensionMemberAlias(context.Context, string, string, string, int64) error

	ListCompatibilities(context.Context, string, CompatibilityFilter) ([]DimensionMetricCompatibility, int, error)
	ProposeCompatibility(context.Context, string, string, ProposeCompatibilityInput) (DimensionMetricCompatibility, error)
	UpdateCompatibility(context.Context, string, string, string, UpdateCompatibilityInput) (DimensionMetricCompatibility, error)
	DecideCompatibility(context.Context, string, string, string, int64, string) (DimensionMetricCompatibility, error)

	CreateRefreshJob(context.Context, string, string, PreparedRefreshJob) (RefreshJob, bool, error)
	ListRefreshJobs(context.Context, string, RefreshJobFilter) ([]RefreshJob, int, error)
	SearchMemberMetrics(context.Context, string, string, string, int) ([]MemberMetricSearchResult, error)
}

type DimensionRefreshStore interface {
	ListRefreshTenantIDs(context.Context) ([]string, error)
	ClaimDimensionRefresh(context.Context, string, string, time.Duration) (*DimensionRefreshClaim, error)
	RefreshDimensionMembers(context.Context, DimensionRefreshClaim, string) error
	FailDimensionRefresh(context.Context, DimensionRefreshClaim, string, string) error
}
