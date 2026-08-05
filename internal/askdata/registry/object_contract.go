package registry

import (
	"encoding/json"
	"errors"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

type semanticModelContractDocument struct {
	Type               string              `json:"type"`
	ModelID            string              `json:"modelId"`
	VersionNo          int                 `json:"versionNo"`
	Code               string              `json:"code"`
	Name               string              `json:"name"`
	DatasetID          string              `json:"datasetId"`
	DatasetVersionID   string              `json:"datasetVersionId"`
	MaterializationID  string              `json:"materializationId"`
	DatasetSchemaHash  askdata.ContentHash `json:"datasetSchemaHash"`
	Layer              string              `json:"layer"`
	GrainContract      json.RawMessage     `json:"grainContract"`
	PrimaryTimeFieldID string              `json:"primaryTimeFieldId,omitempty"`
}

type measureContractDocument struct {
	Type                   string          `json:"type"`
	MeasureID              string          `json:"measureId"`
	VersionNo              int             `json:"versionNo"`
	SemanticModelVersionID string          `json:"semanticModelVersionId"`
	Code                   string          `json:"code"`
	Name                   string          `json:"name"`
	FormulaAST             json.RawMessage `json:"formulaAst"`
	Aggregation            Aggregation     `json:"aggregation"`
	Additivity             Additivity      `json:"additivity"`
	DataType               NumericDataType `json:"dataType"`
	Unit                   string          `json:"unit,omitempty"`
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
	}
}

func measureContract(measure Measure) measureContractDocument {
	return measureContractDocument{
		Type: "MEASURE", MeasureID: measure.ObjectID, VersionNo: measure.VersionNo,
		SemanticModelVersionID: measure.SemanticModelVersionID,
		Code:                   measure.Code, Name: measure.Name, FormulaAST: measure.FormulaAST,
		Aggregation: measure.Aggregation, Additivity: measure.Additivity,
		DataType: measure.DataType, Unit: measure.Unit,
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
	return NewReleaseObject(ReleaseObjectSemanticModel, model.ObjectID, model.ID,
		SensitivityInternal, semanticModelContract(model), model.ContentHash)
}

func MeasureReleaseObject(measure Measure) (ReleaseObject, error) {
	if measure.Status != VersionStatusCertified {
		return ReleaseObject{}, errors.New("measure must be CERTIFIED before release")
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
	dependencies := append([]string(nil), metric.MeasureVersionIDs...)
	sort.Strings(dependencies)
	contract := struct {
		Type                   string          `json:"type"`
		MetricID               string          `json:"metricId"`
		VersionNo              int             `json:"versionNo"`
		SemanticModelVersionID string          `json:"semanticModelVersionId"`
		FormulaAST             json.RawMessage `json:"formulaAst"`
		DefaultFiltersAST      json.RawMessage `json:"defaultFiltersAst"`
		Unit                   string          `json:"unit,omitempty"`
		TimeGrain              string          `json:"timeGrain"`
		Additivity             Additivity      `json:"additivity"`
		NullPolicy             string          `json:"nullPolicy"`
		MeasureVersionIDs      []string        `json:"measureVersionIds"`
	}{
		"METRIC", metric.MetricID, metric.VersionNo, metric.SemanticModelVersionID,
		metric.FormulaAST, metric.DefaultFiltersAST, metric.Unit, metric.TimeGrain,
		metric.Additivity, metric.NullPolicy, dependencies,
	}
	return NewReleaseObject(ReleaseObjectMetric, metric.MetricID, metric.ID,
		SensitivityInternal, contract, metric.ContentHash)
}
