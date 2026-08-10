package registry

import (
	"encoding/json"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

type VersionStatus string

const (
	VersionStatusDraft      VersionStatus = "DRAFT"
	VersionStatusCertified  VersionStatus = "CERTIFIED"
	VersionStatusDeprecated VersionStatus = "DEPRECATED"
)

type Aggregation string

const (
	AggregationSum           Aggregation = "SUM"
	AggregationAverage       Aggregation = "AVG"
	AggregationMinimum       Aggregation = "MIN"
	AggregationMaximum       Aggregation = "MAX"
	AggregationCount         Aggregation = "COUNT"
	AggregationCountDistinct Aggregation = "COUNT_DISTINCT"
)

type Additivity string

const (
	FullyAdditive Additivity = "FULLY_ADDITIVE"
	SemiAdditive  Additivity = "SEMI_ADDITIVE"
	NonAdditive   Additivity = "NON_ADDITIVE"
	// Additive is retained as a Go source-compatibility alias. Its serialized
	// value is the certified FULLY_ADDITIVE contract, never the legacy enum.
	Additive Additivity = FullyAdditive
)

type SemiAdditiveTimeAggregation string

const (
	SemiAdditivePeriodEnd     SemiAdditiveTimeAggregation = "PERIOD_END"
	SemiAdditivePeriodBegin   SemiAdditiveTimeAggregation = "PERIOD_BEGIN"
	SemiAdditivePeriodAverage SemiAdditiveTimeAggregation = "PERIOD_AVERAGE"
)

type AggregationRestriction string

const (
	PreAggregate  AggregationRestriction = "PRE_AGGREGATE"
	PostAggregate AggregationRestriction = "POST_AGGREGATE"
)

type ZeroDenominatorPolicy string

const (
	ZeroDenominatorNull ZeroDenominatorPolicy = "NULL"
	ZeroDenominatorZero ZeroDenominatorPolicy = "ZERO"
)

type NumericDataType string

const (
	NumericInteger NumericDataType = "INTEGER"
	NumericDecimal NumericDataType = "DECIMAL"
)

type DimensionKind string

const (
	DimensionCategorical DimensionKind = "CATEGORICAL"
	DimensionTime        DimensionKind = "TIME"
	DimensionEntity      DimensionKind = "ENTITY"
)

type Sensitivity string

const (
	SensitivityPublic       Sensitivity = "PUBLIC"
	SensitivityInternal     Sensitivity = "INTERNAL"
	SensitivityConfidential Sensitivity = "CONFIDENTIAL"
	SensitivityRestricted   Sensitivity = "RESTRICTED"
)

type MemberIndexPolicy string

const (
	MemberIndexFull      MemberIndexPolicy = "FULL"
	MemberIndexExactOnly MemberIndexPolicy = "EXACT_ONLY"
	MemberIndexOnDemand  MemberIndexPolicy = "ON_DEMAND"
	MemberIndexNone      MemberIndexPolicy = "NONE"
)

type RelationshipType string

const (
	RelationshipModelJoin              RelationshipType = "MODEL_JOIN"
	RelationshipEntityLink             RelationshipType = "ENTITY_LINK"
	RelationshipDimensionCompatibility RelationshipType = "DIMENSION_COMPATIBILITY"
)

type JoinType string

const (
	JoinInner JoinType = "INNER"
	JoinLeft  JoinType = "LEFT"
	JoinRight JoinType = "RIGHT"
	JoinFull  JoinType = "FULL"
	JoinNone  JoinType = "NONE"
)

type Cardinality string

const (
	CardinalityOneToOne   Cardinality = "ONE_TO_ONE"
	CardinalityManyToOne  Cardinality = "MANY_TO_ONE"
	CardinalityOneToMany  Cardinality = "ONE_TO_MANY"
	CardinalityManyToMany Cardinality = "MANY_TO_MANY"
)

type FanoutPolicy string

const (
	FanoutBlock                FanoutPolicy = "BLOCK"
	FanoutPreAggregateRequired FanoutPolicy = "PRE_AGGREGATE_REQUIRED"
	FanoutBridgeRequired       FanoutPolicy = "BRIDGE_REQUIRED"
	FanoutSafe                 FanoutPolicy = "SAFE"
)

type VersionIdentity struct {
	ID          string              `json:"id"`
	TenantID    string              `json:"tenantId"`
	DomainID    string              `json:"domainId"`
	ObjectID    string              `json:"objectId"`
	VersionNo   int                 `json:"versionNo"`
	Status      VersionStatus       `json:"status"`
	ContentHash askdata.ContentHash `json:"contentHash"`
	OwnerID     string              `json:"ownerId"`
	CreatedAt   time.Time           `json:"createdAt,omitempty"`
	UpdatedAt   time.Time           `json:"updatedAt,omitempty"`
}

type Entity struct {
	VersionIdentity
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	KeyContract json.RawMessage `json:"keyContract"`
}

type SemanticModel struct {
	VersionIdentity
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

type Measure struct {
	VersionIdentity
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
	ZeroDenominatorPolicy       ZeroDenominatorPolicy       `json:"zeroDenominatorPolicy"`
	DisplayPrecision            int16                       `json:"displayPrecision"`
	AdditivitySuggestion        Additivity                  `json:"additivitySuggestion,omitempty"`
	AdditivityConfirmedBy       string                      `json:"additivityConfirmedBy,omitempty"`
	AdditivityConfirmedAt       *time.Time                  `json:"additivityConfirmedAt,omitempty"`
}

type Metric struct {
	ID          string    `json:"id"`
	TenantID    string    `json:"tenantId"`
	DomainID    string    `json:"domainId"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	OwnerID     string    `json:"ownerId"`
	Version     int64     `json:"version"`
	CreatedAt   time.Time `json:"createdAt,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

type MetricVersion struct {
	VersionIdentity
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
	ZeroDenominatorPolicy          ZeroDenominatorPolicy       `json:"zeroDenominatorPolicy"`
	DisplayPrecision               int16                       `json:"displayPrecision"`
	AdditivitySuggestion           Additivity                  `json:"additivitySuggestion,omitempty"`
	AdditivityConfirmedBy          string                      `json:"additivityConfirmedBy,omitempty"`
	AdditivityConfirmedAt          *time.Time                  `json:"additivityConfirmedAt,omitempty"`
	NullPolicy                     string                      `json:"nullPolicy"`
	IncompletePeriodPolicyOverride IncompletePeriodPolicy      `json:"incompletePeriodPolicyOverride,omitempty"`
	MeasureVersionIDs              []string                    `json:"measureVersionIds"`
}

type Dimension struct {
	VersionIdentity
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

type Hierarchy struct {
	VersionIdentity
	Code                string   `json:"code"`
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	DimensionVersionIDs []string `json:"dimensionVersionIds"`
}

type Relationship struct {
	VersionIdentity
	LeftModelVersionID   string           `json:"leftModelVersionId"`
	RightModelVersionID  string           `json:"rightModelVersionId"`
	Type                 RelationshipType `json:"type"`
	JoinType             JoinType         `json:"joinType"`
	Cardinality          Cardinality      `json:"cardinality"`
	JoinAST              json.RawMessage  `json:"joinAst"`
	FanoutPolicy         FanoutPolicy     `json:"fanoutPolicy"`
	BridgeModelVersionID string           `json:"bridgeModelVersionId,omitempty"`
}

type QualityRule struct {
	VersionIdentity
	TargetType      string          `json:"targetType"`
	TargetVersionID string          `json:"targetVersionId"`
	Code            string          `json:"code"`
	Name            string          `json:"name"`
	RuleAST         json.RawMessage `json:"ruleAst"`
	Severity        string          `json:"severity"`
}

type BusinessTerm struct {
	VersionIdentity
	Term              string     `json:"term"`
	TermType          string     `json:"termType"`
	TargetObjectType  string     `json:"targetObjectType"`
	TargetVersionID   string     `json:"targetVersionId"`
	TargetCode        string     `json:"targetCode"`
	MatchMode         string     `json:"matchMode"`
	MatchPattern      string     `json:"matchPattern,omitempty"`
	Priority          int        `json:"priority"`
	NegativeContexts  []string   `json:"negativeContexts"`
	ApplicableRoleIDs []string   `json:"applicableRoleIds"`
	ValidFrom         *time.Time `json:"validFrom,omitempty"`
	ValidTo           *time.Time `json:"validTo,omitempty"`
	Source            string     `json:"source"`
	ReviewStatus      string     `json:"reviewStatus"`
	ReviewedBy        string     `json:"reviewedBy,omitempty"`
	ReviewedAt        *time.Time `json:"reviewedAt,omitempty"`
	Code              string     `json:"code"`
	Name              string     `json:"name"`
	Definition        string     `json:"definition"`
	Aliases           []string   `json:"aliases"`
}

type MetricPage struct {
	Items      []Metric `json:"items"`
	NextCursor string   `json:"nextCursor,omitempty"`
}
