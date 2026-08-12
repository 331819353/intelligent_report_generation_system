package registry

import (
	"encoding/json"
	"errors"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

type semanticModelContractDocument struct {
	Type                  string              `json:"type"`
	ModelID               string              `json:"modelId"`
	VersionNo             int                 `json:"versionNo"`
	Code                  string              `json:"code"`
	Name                  string              `json:"name"`
	DatasetID             string              `json:"datasetId"`
	DatasetVersionID      string              `json:"datasetVersionId"`
	MaterializationID     string              `json:"materializationId"`
	DatasetSchemaHash     askdata.ContentHash `json:"datasetSchemaHash"`
	Layer                 string              `json:"layer"`
	GrainContract         json.RawMessage     `json:"grainContract"`
	PrimaryTimeFieldID    string              `json:"primaryTimeFieldId,omitempty"`
	TimeContractVersionID string              `json:"timeContractVersionId,omitempty"`
}

type measureContractDocument struct {
	Type                        string                      `json:"type"`
	MeasureID                   string                      `json:"measureId"`
	VersionNo                   int                         `json:"versionNo"`
	SemanticModelVersionID      string                      `json:"semanticModelVersionId"`
	Code                        string                      `json:"code"`
	Name                        string                      `json:"name"`
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
}

type metricContractDocument struct {
	Type                           string                      `json:"type"`
	MetricID                       string                      `json:"metricId"`
	VersionNo                      int                         `json:"versionNo"`
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
	NullPolicy                     string                      `json:"nullPolicy"`
	IncompletePeriodPolicyOverride IncompletePeriodPolicy      `json:"incompletePeriodPolicyOverride,omitempty"`
	MeasureVersionIDs              []string                    `json:"measureVersionIds"`
	BusinessDefinition             string                      `json:"businessDefinition,omitempty"`
}

type dimensionContractDocument struct {
	Type                   string            `json:"type"`
	DimensionID            string            `json:"dimensionId"`
	VersionNo              int               `json:"versionNo"`
	SemanticModelVersionID string            `json:"semanticModelVersionId"`
	LogicalFieldID         string            `json:"logicalFieldId"`
	Code                   string            `json:"code"`
	Name                   string            `json:"name"`
	Kind                   DimensionKind     `json:"kind"`
	Sensitivity            Sensitivity       `json:"sensitivity"`
	MemberIndexPolicy      MemberIndexPolicy `json:"memberIndexPolicy"`
}

func semanticModelContract(model SemanticModel) semanticModelContractDocument {
	return semanticModelContractDocument{
		Type: "SEMANTIC_MODEL", ModelID: model.ObjectID, VersionNo: model.VersionNo,
		Code: model.Code, Name: model.Name, DatasetID: model.DatasetID,
		DatasetVersionID: model.DatasetVersionID, MaterializationID: model.MaterializationID,
		DatasetSchemaHash: model.DatasetSchemaHash, Layer: model.Layer,
		GrainContract: model.GrainContract, PrimaryTimeFieldID: model.PrimaryTimeFieldID,
		TimeContractVersionID: model.TimeContractVersionID,
	}
}

func measureContract(measure Measure) measureContractDocument {
	return measureContractDocument{
		Type: "MEASURE", MeasureID: measure.ObjectID, VersionNo: measure.VersionNo,
		SemanticModelVersionID: measure.SemanticModelVersionID,
		Code:                   measure.Code, Name: measure.Name, FormulaAST: measure.FormulaAST,
		Aggregation: measure.Aggregation, Additivity: measure.Additivity,
		SemiAdditiveTimeAggregation: measure.SemiAdditiveTimeAggregation,
		AggregationRestriction:      measure.AggregationRestriction,
		NonAdditiveDimensions:       append([]string(nil), measure.NonAdditiveDimensions...),
		DataType:                    measure.DataType, Unit: measure.Unit, Currency: measure.Currency,
		ZeroDenominatorPolicy: measure.ZeroDenominatorPolicy, DisplayPrecision: measure.DisplayPrecision,
	}
}

func dimensionContract(dimension Dimension) dimensionContractDocument {
	return dimensionContractDocument{
		Type: "DIMENSION", DimensionID: dimension.ObjectID, VersionNo: dimension.VersionNo,
		SemanticModelVersionID: dimension.SemanticModelVersionID,
		LogicalFieldID:         dimension.LogicalFieldID, Code: dimension.Code,
		Name: dimension.Name, Kind: dimension.Kind, Sensitivity: dimension.Sensitivity,
		MemberIndexPolicy: dimension.MemberIndexPolicy,
	}
}

func contentHashForContract(contract any) askdata.ContentHash {
	hash, _, err := CanonicalContentHash(contract)
	if err != nil {
		panic(err)
	}
	return hash
}

// NewReleaseObject converts a deterministic typed contract to a release
// object and verifies it against the authoritative source content hash.
func NewReleaseObject(
	objectType ReleaseObjectType,
	objectID, objectVersionID string,
	sensitivity Sensitivity,
	contract any,
	expectedHash askdata.ContentHash,
) (ReleaseObject, error) {
	hash, canonical, err := CanonicalContentHash(contract)
	if err != nil {
		return ReleaseObject{}, err
	}
	if expectedHash != hash {
		return ReleaseObject{}, errors.New("semantic object content hash does not match its canonical release contract")
	}
	return ReleaseObject{
		Type: objectType, ObjectID: objectID, ObjectVersionID: objectVersionID,
		ContentHash: hash, Sensitivity: sensitivity, Contract: canonical,
	}, nil
}

func SemanticModelReleaseObject(model SemanticModel) (ReleaseObject, error) {
	if model.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("semantic model must be CERTIFIED before release")
	}
	if model.TimeContractVersionID == "" {
		return ReleaseObject{}, errors.New("TIME_CONTRACT_MISSING: semantic model release requires a time contract version")
	}
	return NewReleaseObject(ReleaseObjectSemanticModel, model.ObjectID, model.ID,
		SensitivityInternal, semanticModelContract(model), model.ContentHash)
}

func MeasureReleaseObject(measure Measure) (ReleaseObject, error) {
	if measure.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("measure must be CERTIFIED before release")
	}
	if err := ValidateMeasureAdditivity(measure); err != nil {
		return ReleaseObject{}, err
	}
	return NewReleaseObject(ReleaseObjectMeasure, measure.ObjectID, measure.ID,
		SensitivityInternal, measureContract(measure), measure.ContentHash)
}

func DimensionReleaseObject(dimension Dimension) (ReleaseObject, error) {
	if dimension.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("dimension must be CERTIFIED before release")
	}
	return NewReleaseObject(ReleaseObjectDimension, dimension.ObjectID, dimension.ID,
		dimension.Sensitivity, dimensionContract(dimension), dimension.ContentHash)
}

func MetricVersionReleaseObject(metric MetricVersion) (ReleaseObject, error) {
	if metric.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("metric version must be CERTIFIED before release")
	}
	if err := ValidateAdditivity(metric); err != nil {
		return ReleaseObject{}, err
	}
	dependencies := append([]string(nil), metric.MeasureVersionIDs...)
	sort.Strings(dependencies)
	contract := metricContractDocument{
		Type: "METRIC", MetricID: metric.MetricID, VersionNo: metric.VersionNo,
		SemanticModelVersionID: metric.SemanticModelVersionID,
		FormulaAST:             metric.FormulaAST, DefaultFiltersAST: metric.DefaultFiltersAST,
		Unit: metric.Unit, Currency: metric.Currency, TimeGrain: metric.TimeGrain,
		Additivity:                  metric.Additivity,
		SemiAdditiveTimeAggregation: metric.SemiAdditiveTimeAggregation,
		AggregationRestriction:      metric.AggregationRestriction,
		NonAdditiveDimensions:       append([]string(nil), metric.NonAdditiveDimensions...),
		ZeroDenominatorPolicy:       metric.ZeroDenominatorPolicy, DisplayPrecision: metric.DisplayPrecision,
		NullPolicy: metric.NullPolicy, IncompletePeriodPolicyOverride: metric.IncompletePeriodPolicyOverride,
		MeasureVersionIDs: dependencies, BusinessDefinition: metric.BusinessDefinition,
	}
	return NewReleaseObject(ReleaseObjectMetric, metric.MetricID, metric.ID,
		SensitivityInternal, contract, metric.ContentHash)
}

// MetricDimensionReleaseObject keeps governed metric/dimension compatibility
// in the immutable release manifest so KPI and ad-hoc group-by planning use
// the exact certified compatibility fact.
func MetricDimensionReleaseObject(compatibility MetricDimension) (ReleaseObject, error) {
	if compatibility.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("metric dimension must be CERTIFIED before release")
	}
	if err := compatibility.Validate(); err != nil {
		return ReleaseObject{}, err
	}
	return NewReleaseObject(
		ReleaseObjectMetricDimension,
		compatibility.ObjectID,
		compatibility.ID,
		SensitivityInternal,
		metricDimensionContract(compatibility),
		compatibility.ContentHash,
	)
}

// BusinessTermReleaseObject publishes only the certified, canonical business
// vocabulary contract. Review metadata remains audit evidence and is not part
// of the executable release content hash.
// QualityRuleReleaseObject freezes a governed quality rule into a Release. The
// contract carries the binding itself, so what a release promises to check is
// content-addressed alongside everything else it promises.
func QualityRuleReleaseObject(rule QualityRule) (ReleaseObject, error) {
	if rule.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("quality rule must be CERTIFIED before release")
	}
	if err := rule.Validate(); err != nil {
		return ReleaseObject{}, err
	}
	return NewReleaseObject(
		ReleaseObjectQualityRule,
		rule.ObjectID,
		rule.ID,
		SensitivityInternal,
		map[string]any{
			"type": "QUALITY_RULE", "qualityRuleId": rule.ObjectID,
			"versionNo": rule.VersionNo, "targetType": rule.TargetType,
			"targetVersionId": rule.TargetVersionID, "code": rule.Code,
			"name": rule.Name, "ruleAst": json.RawMessage(rule.RuleAST),
			"severity": rule.Severity,
		},
		rule.ContentHash,
	)
}

// RowAccessPolicyReleaseObject freezes a row access policy into the Release, so
// which rows a reader may see is pinned by the same immutable manifest as what
// the numbers mean.
func RowAccessPolicyReleaseObject(policy RowAccessPolicy) (ReleaseObject, error) {
	if policy.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("row access policy must be CERTIFIED before release")
	}
	if err := policy.Validate(); err != nil {
		return ReleaseObject{}, err
	}
	return NewReleaseObject(
		ReleaseObjectRowAccessPolicy,
		policy.ObjectID,
		policy.ID,
		SensitivityInternal,
		rowAccessPolicyContract(policy),
		policy.ContentHash,
	)
}

func rowAccessPolicyContract(policy RowAccessPolicy) map[string]any {
	return map[string]any{
		"type": "ROW_ACCESS_POLICY", "rowAccessPolicyId": policy.ObjectID,
		"versionNo": policy.VersionNo, "modelVersionId": policy.ModelVersionID,
		"code": policy.Code, "name": policy.Name,
		"predicateAst":         json.RawMessage(policy.PredicateAST),
		"subjectAttributeKeys": sortedAdminAliases(policy.SubjectAttributeKeys),
	}
}

func BusinessTermReleaseObject(term BusinessTerm) (ReleaseObject, error) {
	if term.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("business term must be CERTIFIED before release")
	}
	// BusinessTerm.Validate checks the editable draft shape and therefore expects
	// PENDING review metadata. Certification stores APPROVED audit evidence, which
	// is intentionally outside the executable contract; validate an equivalent
	// draft-shaped copy while preserving the certified source value and hash.
	validated := term
	validated.ReviewStatus = TermReviewPending
	validated.ReviewedBy = ""
	validated.ReviewedAt = nil
	if err := validated.Validate(); err != nil {
		return ReleaseObject{}, err
	}
	return NewReleaseObject(
		ReleaseObjectBusinessTerm,
		term.ObjectID,
		term.ID,
		SensitivityInternal,
		businessTermContract(term),
		term.ContentHash,
	)
}
