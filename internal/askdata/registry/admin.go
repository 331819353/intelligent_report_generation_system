package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type AdminResource string

const (
	AdminResourceSemanticModel AdminResource = "SEMANTIC_MODEL"
	AdminResourceMeasure       AdminResource = "MEASURE"
	AdminResourceMetric        AdminResource = "METRIC"
	AdminResourceMetricVersion AdminResource = "METRIC_VERSION"
	AdminResourceDimension     AdminResource = "DIMENSION"
	AdminResourceBusinessTerm  AdminResource = "BUSINESS_TERM"
	AdminResourceKPIBundle     AdminResource = "KPI_BUNDLE"
	AdminResourceRelationship  AdminResource = "RELATIONSHIP"
	AdminResourceRelease       AdminResource = "SEMANTIC_RELEASE"
)

const (
	AdminActionView      = "VIEW"
	AdminActionEditDraft = "EDIT_DRAFT"
	AdminActionRelease   = "RELEASE"
)

var (
	ErrRegistryPermissionDenied    = errors.New("semantic registry permission denied")
	ErrRegistryIdempotencyConflict = errors.New("semantic registry idempotency conflict")
	ErrRegistryDraftInUse          = errors.New("semantic registry draft is referenced by another object")
	ErrRegistryInvalidRequest      = errors.New("semantic registry request is invalid")
)

type AdminScope struct {
	TenantID string
	DomainID string
	ActorID  string
}

func (scope AdminScope) Validate(ctx context.Context) error {
	for label, value := range map[string]string{
		"tenant ID": scope.TenantID,
		"domain ID": scope.DomainID,
		"actor ID":  scope.ActorID,
	} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != strings.ToLower(value) {
			return fmt.Errorf("%w: %s must be a canonical UUID", ErrRegistryInvalidRequest, label)
		}
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID != scope.ActorID || access.DomainID != scope.DomainID {
		return fmt.Errorf("%w: actor access context does not match the request scope", ErrRegistryPermissionDenied)
	}
	return nil
}

type AdminCommand struct {
	RequestID  string
	ActionHash askdata.ContentHash
}

func (command AdminCommand) Validate() error {
	parsed, err := uuid.Parse(command.RequestID)
	if err != nil || parsed.String() != strings.ToLower(command.RequestID) {
		return fmt.Errorf("%w: request ID must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	if err := command.ActionHash.Validate(); err != nil {
		return fmt.Errorf("%w: action hash: %v", ErrRegistryInvalidRequest, err)
	}
	return nil
}

type VersionedDraftInput struct {
	ObjectID          string     `json:"objectId,omitempty"`
	VersionNo         int        `json:"versionNo,omitempty"`
	OwnerID           string     `json:"ownerId,omitempty"`
	ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt,omitempty"`
}

type SemanticModelDraftInput struct {
	VersionedDraftInput
	Code                  string              `json:"code"`
	Name                  string              `json:"name"`
	Description           string              `json:"description"`
	EntityVersionID       string              `json:"entityVersionId,omitempty"`
	DatasetID             string              `json:"datasetId"`
	DatasetVersionID      string              `json:"datasetVersionId"`
	MaterializationID     string              `json:"materializationId"`
	DatasetSchemaHash     askdata.ContentHash `json:"datasetSchemaHash"`
	Layer                 string              `json:"layer"`
	GrainContract         json.RawMessage     `json:"grainContract"`
	PrimaryTimeFieldID    string              `json:"primaryTimeFieldId,omitempty"`
	TimeContractVersionID string              `json:"timeContractVersionId,omitempty"`
}

type MeasureDraftInput struct {
	VersionedDraftInput
	SemanticModelVersionID      string                      `json:"semanticModelVersionId"`
	Code                        string                      `json:"code"`
	Name                        string                      `json:"name"`
	Description                 string                      `json:"description"`
	FormulaAST                  json.RawMessage             `json:"formulaAst"`
	Aggregation                 Aggregation                 `json:"aggregation"`
	Additivity                  Additivity                  `json:"additivity,omitempty"`
	SemiAdditiveTimeAggregation SemiAdditiveTimeAggregation `json:"semiAdditiveTimeAggregation,omitempty"`
	AggregationRestriction      AggregationRestriction      `json:"aggregationRestriction,omitempty"`
	NonAdditiveDimensions       []string                    `json:"nonAdditiveDimensions"`
	DataType                    NumericDataType             `json:"dataType"`
	Unit                        string                      `json:"unit,omitempty"`
	Currency                    string                      `json:"currency,omitempty"`
	ZeroDenominatorPolicy       ZeroDenominatorPolicy       `json:"zeroDenominatorPolicy,omitempty"`
	DisplayPrecision            int16                       `json:"displayPrecision"`
	AdditivitySuggestion        Additivity                  `json:"additivitySuggestion,omitempty"`
}

type MetricDraftInput struct {
	OwnerID         string `json:"ownerId,omitempty"`
	Code            string `json:"code"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	ExpectedVersion int64  `json:"expectedVersion,omitempty"`
}

type MetricVersionDraftInput struct {
	VersionedDraftInput
	MetricID                       string                      `json:"metricId"`
	SemanticModelVersionID         string                      `json:"semanticModelVersionId"`
	FormulaAST                     json.RawMessage             `json:"formulaAst"`
	DefaultFiltersAST              json.RawMessage             `json:"defaultFiltersAst"`
	Unit                           string                      `json:"unit,omitempty"`
	Currency                       string                      `json:"currency,omitempty"`
	TimeGrain                      string                      `json:"timeGrain"`
	Additivity                     Additivity                  `json:"additivity,omitempty"`
	SemiAdditiveTimeAggregation    SemiAdditiveTimeAggregation `json:"semiAdditiveTimeAggregation,omitempty"`
	AggregationRestriction         AggregationRestriction      `json:"aggregationRestriction,omitempty"`
	NonAdditiveDimensions          []string                    `json:"nonAdditiveDimensions"`
	ZeroDenominatorPolicy          ZeroDenominatorPolicy       `json:"zeroDenominatorPolicy,omitempty"`
	DisplayPrecision               int16                       `json:"displayPrecision"`
	AdditivitySuggestion           Additivity                  `json:"additivitySuggestion,omitempty"`
	NullPolicy                     string                      `json:"nullPolicy"`
	IncompletePeriodPolicyOverride IncompletePeriodPolicy      `json:"incompletePeriodPolicyOverride,omitempty"`
	MeasureVersionIDs              []string                    `json:"measureVersionIds"`
}

type DimensionDraftInput struct {
	VersionedDraftInput
	SemanticModelVersionID string            `json:"semanticModelVersionId"`
	LogicalFieldID         string            `json:"logicalFieldId"`
	Code                   string            `json:"code"`
	Name                   string            `json:"name"`
	Description            string            `json:"description"`
	Kind                   DimensionKind     `json:"kind"`
	Sensitivity            Sensitivity       `json:"sensitivity"`
	MemberIndexPolicy      MemberIndexPolicy `json:"memberIndexPolicy"`
	HighCardinality        bool              `json:"highCardinality"`
}

type BusinessTermDraftInput struct {
	VersionedDraftInput
	Term              string     `json:"term,omitempty"`
	TermType          string     `json:"termType,omitempty"`
	TargetObjectType  string     `json:"targetObjectType,omitempty"`
	TargetVersionID   string     `json:"targetVersionId,omitempty"`
	TargetCode        string     `json:"targetCode,omitempty"`
	MatchMode         string     `json:"matchMode,omitempty"`
	MatchPattern      string     `json:"matchPattern,omitempty"`
	Priority          int        `json:"priority,omitempty"`
	NegativeContexts  []string   `json:"negativeContexts"`
	ApplicableRoleIDs []string   `json:"applicableRoleIds"`
	ValidFrom         *time.Time `json:"validFrom,omitempty"`
	ValidTo           *time.Time `json:"validTo,omitempty"`
	Source            string     `json:"source,omitempty"`
	Code              string     `json:"code,omitempty"`
	Name              string     `json:"name"`
	Definition        string     `json:"definition"`
	Aliases           []string   `json:"aliases"`
}

type KPIBundleDraftInput struct {
	VersionedDraftInput
	Code                       string          `json:"code"`
	Name                       string          `json:"name"`
	Items                      []KPIBundleItem `json:"items"`
	DefaultDimensionVersionIDs []string        `json:"defaultDimensionVersionIds"`
	DefaultTimeExpression      string          `json:"defaultTimeExpression"`
	DefaultChartTypes          []string        `json:"defaultChartTypes"`
	RoleMapping                json.RawMessage `json:"roleMapping"`
	ApplicableQuestionPatterns []string        `json:"applicableQuestionPatterns"`
}

type RelationshipDraftInput struct {
	VersionedDraftInput
	LeftModelVersionID   string           `json:"leftModelVersionId"`
	RightModelVersionID  string           `json:"rightModelVersionId"`
	Type                 RelationshipType `json:"type"`
	JoinType             JoinType         `json:"joinType"`
	Cardinality          Cardinality      `json:"cardinality"`
	JoinAST              json.RawMessage  `json:"joinAst"`
	FanoutPolicy         FanoutPolicy     `json:"fanoutPolicy"`
	BridgeModelVersionID string           `json:"bridgeModelVersionId,omitempty"`
}

type DeleteDraftInput struct {
	ExpectedUpdatedAt *time.Time `json:"expectedUpdatedAt,omitempty"`
	ExpectedVersion   int64      `json:"expectedVersion,omitempty"`
}

type ReleaseDraftInput struct {
	SemanticVersion string          `json:"semanticVersion"`
	Objects         []ReleaseObject `json:"objects"`
}

type AdminMutation struct {
	SemanticModel *SemanticModelDraftInput
	Measure       *MeasureDraftInput
	Metric        *MetricDraftInput
	MetricVersion *MetricVersionDraftInput
	Dimension     *DimensionDraftInput
	BusinessTerm  *BusinessTermDraftInput
	KPIBundle     *KPIBundleDraftInput
	Relationship  *RelationshipDraftInput
}

func (mutation AdminMutation) payload(resource AdminResource) (any, error) {
	var value any
	switch resource {
	case AdminResourceSemanticModel:
		value = mutation.SemanticModel
	case AdminResourceMeasure:
		value = mutation.Measure
	case AdminResourceMetric:
		value = mutation.Metric
	case AdminResourceMetricVersion:
		value = mutation.MetricVersion
	case AdminResourceDimension:
		value = mutation.Dimension
	case AdminResourceBusinessTerm:
		value = mutation.BusinessTerm
	case AdminResourceKPIBundle:
		value = mutation.KPIBundle
	case AdminResourceRelationship:
		value = mutation.Relationship
	default:
		return nil, fmt.Errorf("%w: unsupported semantic resource %q", ErrRegistryInvalidRequest, resource)
	}
	if value == nil {
		return nil, fmt.Errorf("%w: mutation payload does not match %s", ErrRegistryInvalidRequest, resource)
	}
	return value, nil
}

type AdminPage struct {
	Items      any    `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

// 语义对象的生命周期状态。指标标识表使用 ACTIVE 表示已认证，其余对象使用
// CERTIFIED；读接口按原样透传，不做跨对象的状态归一化。
const (
	StatusDraft      = "DRAFT"
	StatusCertified  = "CERTIFIED"
	StatusActive     = "ACTIVE"
	StatusDeprecated = "DEPRECATED"
)

// AdminListFilter 描述语义对象读取接口的分页与状态过滤条件。
// Status 为空表示不过滤，返回该领域下全部状态的对象。
type AdminListFilter struct {
	Status string
	Cursor string
	Limit  int
}

func validObjectStatusFilter(status string) bool {
	switch status {
	case "", StatusDraft, StatusCertified, StatusActive, StatusDeprecated:
		return true
	default:
		return false
	}
}

// statusArg 把空状态映射为 SQL NULL，让查询里的
// `($n::text IS NULL OR status=$n)` 谓词在不过滤时短路。
func statusArg(status string) any {
	if status == "" {
		return nil
	}
	return status
}

type AdminWriteResult struct {
	ResourceType    AdminResource       `json:"resourceType"`
	ResourceID      string              `json:"resourceId"`
	ObjectID        string              `json:"objectId,omitempty"`
	ContentHash     askdata.ContentHash `json:"contentHash,omitempty"`
	Status          string              `json:"status"`
	RecordVersion   int64               `json:"recordVersion,omitempty"`
	SemanticVersion string              `json:"semanticVersion,omitempty"`
	UpdatedAt       *time.Time          `json:"updatedAt,omitempty"`
	Replayed        bool                `json:"replayed"`
}

type AdminBackend interface {
	ListDrafts(context.Context, AdminScope, AdminResource, string, int) (AdminPage, error)
	GetDraft(context.Context, AdminScope, AdminResource, string) (any, error)
	ListObjects(context.Context, AdminScope, AdminResource, AdminListFilter) (AdminPage, error)
	GetObject(context.Context, AdminScope, AdminResource, string) (any, error)
	CreateDraft(context.Context, AdminScope, AdminResource, AdminMutation, AdminCommand) (AdminWriteResult, error)
	UpdateDraft(context.Context, AdminScope, AdminResource, string, AdminMutation, AdminCommand) (AdminWriteResult, error)
	DeleteDraft(context.Context, AdminScope, AdminResource, string, DeleteDraftInput, AdminCommand) (AdminWriteResult, error)
	CreateAdminReleaseDraft(context.Context, AdminScope, ReleaseDraftInput, AdminCommand) (AdminWriteResult, error)
}
