// Package compiler resolves version-pinned semantic contracts before adapting
// them to the deterministic Dataset Query DSL and SQL compiler.
package compiler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/binding"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	ResolutionVersion        = "semantic-contract-resolution-v1"
	MaxResolvedFields        = 512
	MaxResolvedMeasures      = 64
	MaxResolvedRelationships = graph.MaxJoinHops
	MaxContractASTBytes      = 128 << 10
)

var (
	ErrInvalidResolveRequest = errors.New("semantic contract resolve request is invalid")
	ErrContractUnavailable   = errors.New("semantic contract is unavailable")
	ErrMaterializationStale  = errors.New("semantic model materialization is stale or unavailable")
	ErrInvalidResolution     = errors.New("semantic contract resolution is invalid")
	trustedOutputNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

type ResolveRequest struct {
	BuildRequest  ir.BuildRequest  `json:"buildRequest"`
	BuildArtifact ir.BuildArtifact `json:"buildArtifact"`
}

type ContractLookup struct {
	Scope                  askdata.PolicyScope `json:"scope"`
	DomainID               askdata.ID          `json:"domainId"`
	IRHash                 askdata.ContentHash `json:"irHash"`
	ModelVersionID         askdata.ID          `json:"modelVersionId"`
	TimeDimensionVersionID *askdata.ID         `json:"timeDimensionVersionId,omitempty"`
	MetricVersionIDs       []askdata.ID        `json:"metricVersionIds"`
	DimensionVersionIDs    []askdata.ID        `json:"dimensionVersionIds"`
	MemberVersionIDs       []askdata.ID        `json:"memberVersionIds"`
	MemberBindings         []MemberBinding     `json:"memberBindings"`
	RelationshipVersionIDs []askdata.ID        `json:"relationshipVersionIds"`
}

// MemberBinding preserves the exact FILTER edge from the IR. Checking only
// that a member belongs to any selected dimension would permit a value to be
// rebound to a different filter when several dimensions are present.
type MemberBinding struct {
	DimensionVersionID askdata.ID `json:"dimensionVersionId"`
	MemberVersionID    askdata.ID `json:"memberVersionId"`
}

type FieldContract struct {
	FieldID       askdata.ID          `json:"fieldId"`
	Code          string              `json:"code"`
	Role          string              `json:"role"`
	CanonicalType string              `json:"canonicalType"`
	SemanticType  string              `json:"semanticType,omitempty"`
	Nullable      bool                `json:"nullable"`
	Visible       bool                `json:"visible"`
	ContractHash  askdata.ContentHash `json:"contractHash"`
}

type MaterializationContract struct {
	MaterializationID askdata.ID          `json:"materializationId"`
	DatasetID         askdata.ID          `json:"datasetId"`
	DatasetVersionID  askdata.ID          `json:"datasetVersionId"`
	Layer             string              `json:"layer"`
	Status            string              `json:"status"`
	PublishedSchema   string              `json:"publishedSchema"`
	PublishedName     string              `json:"publishedName"`
	SchemaHash        askdata.ContentHash `json:"schemaHash"`
	SnapshotHash      askdata.ContentHash `json:"snapshotHash"`
	RowCount          int64               `json:"rowCount"`
}

type ModelContract struct {
	ModelVersionID     askdata.ID              `json:"modelVersionId"`
	ContentHash        askdata.ContentHash     `json:"contentHash"`
	DatasetSchemaHash  askdata.ContentHash     `json:"datasetSchemaHash"`
	GrainContract      json.RawMessage         `json:"grainContract"`
	PrimaryTimeFieldID *askdata.ID             `json:"primaryTimeFieldId,omitempty"`
	Fields             []FieldContract         `json:"fields"`
	Materialization    MaterializationContract `json:"materialization"`
}

type MeasureContract struct {
	MeasureID                   askdata.ID                           `json:"measureId"`
	MeasureVersionID            askdata.ID                           `json:"measureVersionId"`
	ModelVersionID              askdata.ID                           `json:"modelVersionId"`
	ContentHash                 askdata.ContentHash                  `json:"contentHash"`
	FormulaAST                  json.RawMessage                      `json:"formulaAst"`
	Aggregation                 registry.Aggregation                 `json:"aggregation"`
	Additivity                  registry.Additivity                  `json:"additivity"`
	SemiAdditiveTimeAggregation registry.SemiAdditiveTimeAggregation `json:"semiAdditiveTimeAggregation,omitempty"`
	AggregationRestriction      registry.AggregationRestriction      `json:"aggregationRestriction,omitempty"`
	NonAdditiveDimensions       []string                             `json:"nonAdditiveDimensions"`
	DataType                    registry.NumericDataType             `json:"dataType"`
	Unit                        string                               `json:"unit,omitempty"`
	Currency                    string                               `json:"currency,omitempty"`
	ZeroDenominatorPolicy       registry.ZeroDenominatorPolicy       `json:"zeroDenominatorPolicy"`
}

type MetricContract struct {
	MetricVersionID             askdata.ID                           `json:"metricVersionId"`
	ModelVersionID              askdata.ID                           `json:"modelVersionId"`
	ContentHash                 askdata.ContentHash                  `json:"contentHash"`
	FormulaAST                  json.RawMessage                      `json:"formulaAst"`
	DefaultFilterAST            json.RawMessage                      `json:"defaultFilterAst"`
	Unit                        string                               `json:"unit,omitempty"`
	Currency                    string                               `json:"currency,omitempty"`
	TimeGrain                   string                               `json:"timeGrain"`
	Additivity                  registry.Additivity                  `json:"additivity"`
	SemiAdditiveTimeAggregation registry.SemiAdditiveTimeAggregation `json:"semiAdditiveTimeAggregation,omitempty"`
	AggregationRestriction      registry.AggregationRestriction      `json:"aggregationRestriction,omitempty"`
	NonAdditiveDimensions       []string                             `json:"nonAdditiveDimensions"`
	ZeroDenominatorPolicy       registry.ZeroDenominatorPolicy       `json:"zeroDenominatorPolicy"`
	DisplayPrecision            int16                                `json:"displayPrecision"`
	NullPolicy                  string                               `json:"nullPolicy"`
	Measures                    []MeasureContract                    `json:"measures"`
}

type DimensionContract struct {
	DimensionVersionID askdata.ID                 `json:"dimensionVersionId"`
	ModelVersionID     askdata.ID                 `json:"modelVersionId"`
	LogicalFieldID     askdata.ID                 `json:"logicalFieldId"`
	ContentHash        askdata.ContentHash        `json:"contentHash"`
	Kind               registry.DimensionKind     `json:"kind"`
	Sensitivity        registry.Sensitivity       `json:"sensitivity"`
	MemberIndexPolicy  registry.MemberIndexPolicy `json:"memberIndexPolicy"`
}

// MemberContract is deliberately label-free. Member keys, aliases, lookup
// hashes and canonical labels remain inside the authoritative PostgreSQL
// boundary and never enter a resolution or audit artifact.
type MemberContract struct {
	MemberVersionID    askdata.ID           `json:"memberVersionId"`
	DimensionVersionID askdata.ID           `json:"dimensionVersionId"`
	ContentHash        askdata.ContentHash  `json:"contentHash"`
	Sensitivity        registry.Sensitivity `json:"sensitivity"`
}

type RelationshipContract struct {
	RelationshipVersionID askdata.ID            `json:"relationshipVersionId"`
	ContentHash           askdata.ContentHash   `json:"contentHash"`
	LeftModelVersionID    askdata.ID            `json:"leftModelVersionId"`
	RightModelVersionID   askdata.ID            `json:"rightModelVersionId"`
	JoinAST               json.RawMessage       `json:"joinAst"`
	JoinType              registry.JoinType     `json:"joinType"`
	Cardinality           registry.Cardinality  `json:"cardinality"`
	FanoutPolicy          registry.FanoutPolicy `json:"fanoutPolicy"`
	BridgeModelVersionID  askdata.ID            `json:"bridgeModelVersionId,omitempty"`
}

type ContractSnapshot struct {
	Release            askdata.ReleaseRef     `json:"release"`
	ReleaseStatus      string                 `json:"releaseStatus"`
	ReleaseObjectCount int                    `json:"releaseObjectCount"`
	Model              ModelContract          `json:"model"`
	Metrics            []MetricContract       `json:"metrics"`
	Dimensions         []DimensionContract    `json:"dimensions"`
	Members            []MemberContract       `json:"members"`
	Relationships      []RelationshipContract `json:"relationships"`
	// memberParameterValues is an execution-only bridge to QUERY-003. It is
	// never serialized, hashed or returned through the public member contract.
	memberParameterValues map[askdata.ID]string
}

type ContractStore interface {
	LoadContractSnapshot(context.Context, ContractLookup) (ContractSnapshot, error)
}

type Resolution struct {
	Version                string                 `json:"version"`
	Scope                  askdata.PolicyScope    `json:"scope"`
	DomainID               askdata.ID             `json:"domainId"`
	IRHash                 askdata.ContentHash    `json:"irHash"`
	BuildArtifactHash      askdata.ContentHash    `json:"buildArtifactHash"`
	GraphPlanHash          askdata.ContentHash    `json:"graphPlanHash"`
	TimeDimensionVersionID *askdata.ID            `json:"timeDimensionVersionId,omitempty"`
	MemberBindings         []MemberBinding        `json:"memberBindings"`
	Model                  ModelContract          `json:"model"`
	Metrics                []MetricContract       `json:"metrics"`
	Dimensions             []DimensionContract    `json:"dimensions"`
	Members                []MemberContract       `json:"members"`
	GraphPath              *graph.JoinPath        `json:"graphPath,omitempty"`
	Relationships          []RelationshipContract `json:"relationships"`
	ResolutionHash         askdata.ContentHash    `json:"resolutionHash"`
	memberParameterValues  map[askdata.ID]string
}

type Resolver struct{ store ContractStore }

func NewResolver(store ContractStore) (*Resolver, error) {
	if store == nil {
		return nil, errors.New("semantic contract store is required")
	}
	return &Resolver{store: store}, nil
}

func (resolver *Resolver) Resolve(ctx context.Context, request ResolveRequest) (Resolution, error) {
	if resolver == nil || resolver.store == nil {
		return Resolution{}, ErrContractUnavailable
	}
	if err := request.BuildArtifact.ValidateAgainst(request.BuildRequest); err != nil {
		return Resolution{}, fmt.Errorf("%w: IR build replay: %v", ErrInvalidResolveRequest, err)
	}
	if err := validateResolveAccessContext(ctx, request.BuildArtifact.Scope, request.BuildArtifact.DomainID); err != nil {
		return Resolution{}, err
	}
	lookup, path, err := buildContractLookup(request)
	if err != nil {
		return Resolution{}, err
	}
	snapshot, err := resolver.store.LoadContractSnapshot(ctx, lookup)
	if err != nil {
		return Resolution{}, err
	}
	snapshot, err = normalizeSnapshot(snapshot)
	if err != nil {
		return Resolution{}, fmt.Errorf("%w: normalize snapshot: %v", ErrContractUnavailable, err)
	}
	if err := validateSnapshot(lookup, path, snapshot); err != nil {
		return Resolution{}, err
	}
	resolution := Resolution{
		Version: ResolutionVersion, Scope: lookup.Scope, DomainID: lookup.DomainID,
		IRHash: lookup.IRHash, BuildArtifactHash: request.BuildArtifact.ArtifactHash,
		GraphPlanHash:          request.BuildRequest.BindingResult.GraphPlanHash,
		TimeDimensionVersionID: cloneID(lookup.TimeDimensionVersionID),
		MemberBindings:         append([]MemberBinding(nil), lookup.MemberBindings...),
		Model:                  snapshot.Model, Metrics: snapshot.Metrics, Dimensions: snapshot.Dimensions,
		Members: snapshot.Members, GraphPath: cloneJoinPath(path), Relationships: snapshot.Relationships,
		memberParameterValues: cloneMemberParameterValues(snapshot.memberParameterValues),
	}
	return finalizeResolution(resolution)
}

func (resolution Resolution) Validate() error {
	if resolution.Version != ResolutionVersion || resolution.Scope.Validate() != nil ||
		resolution.DomainID.Validate() != nil || !containsID(resolution.Scope.DomainIDs, resolution.DomainID) ||
		resolution.IRHash.Validate() != nil || resolution.BuildArtifactHash.Validate() != nil ||
		resolution.GraphPlanHash.Validate() != nil || resolution.ResolutionHash.Validate() != nil {
		return ErrInvalidResolution
	}
	lookup := ContractLookup{
		Scope: resolution.Scope, DomainID: resolution.DomainID, IRHash: resolution.IRHash,
		ModelVersionID:         resolution.Model.ModelVersionID,
		TimeDimensionVersionID: cloneID(resolution.TimeDimensionVersionID),
		MemberBindings:         append([]MemberBinding(nil), resolution.MemberBindings...),
		MetricVersionIDs:       metricIDs(resolution.Metrics), DimensionVersionIDs: dimensionIDs(resolution.Dimensions),
		MemberVersionIDs: memberIDs(resolution.Members), RelationshipVersionIDs: relationshipIDs(resolution.Relationships),
	}
	if err := lookup.Validate(); err != nil {
		return ErrInvalidResolution
	}
	snapshot := ContractSnapshot{
		Release: resolution.Scope.Release, ReleaseStatus: "READY", ReleaseObjectCount: 1,
		Model: resolution.Model, Metrics: resolution.Metrics, Dimensions: resolution.Dimensions,
		Members: resolution.Members, Relationships: resolution.Relationships,
		memberParameterValues: cloneMemberParameterValues(resolution.memberParameterValues),
	}
	if err := validateResolvedContracts(lookup, resolution.GraphPath, snapshot); err != nil {
		return ErrInvalidResolution
	}
	expected, err := resolutionHash(resolution)
	if err != nil || expected != resolution.ResolutionHash {
		return ErrInvalidResolution
	}
	return nil
}

func buildContractLookup(request ResolveRequest) (ContractLookup, *graph.JoinPath, error) {
	artifact := request.BuildArtifact
	semanticIR := artifact.IR
	if semanticIR.DomainID != artifact.DomainID {
		return ContractLookup{}, nil, ErrInvalidResolveRequest
	}
	lookup := ContractLookup{
		Scope: artifact.Scope, DomainID: artifact.DomainID, IRHash: artifact.IRHash,
		ModelVersionID: semanticIR.ModelVersionID,
	}
	for _, metric := range semanticIR.Metrics {
		lookup.MetricVersionIDs = append(lookup.MetricVersionIDs, metric.MetricVersionID)
	}
	for _, group := range semanticIR.GroupBy {
		lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, group.DimensionVersionID)
	}
	for _, filter := range semanticIR.Filters {
		lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, filter.DimensionVersionID)
		lookup.MemberVersionIDs = append(lookup.MemberVersionIDs, filter.MemberVersionIDs...)
		for _, memberVersionID := range filter.MemberVersionIDs {
			lookup.MemberBindings = append(lookup.MemberBindings, MemberBinding{
				DimensionVersionID: filter.DimensionVersionID,
				MemberVersionID:    memberVersionID,
			})
		}
	}
	if semanticIR.TimeRange != nil {
		lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, semanticIR.TimeRange.DimensionVersionID)
		timeDimensionVersionID := semanticIR.TimeRange.DimensionVersionID
		lookup.TimeDimensionVersionID = &timeDimensionVersionID
	}
	for _, sortValue := range semanticIR.Sort {
		if sortValue.TargetType == ir.SortTargetDimension {
			lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, sortValue.TargetVersionID)
		}
	}
	lookup.MetricVersionIDs = normalizeIDs(lookup.MetricVersionIDs)
	lookup.DimensionVersionIDs = normalizeIDs(lookup.DimensionVersionIDs)
	lookup.MemberVersionIDs = normalizeIDs(lookup.MemberVersionIDs)
	lookup.MemberBindings = normalizeMemberBindings(lookup.MemberBindings)
	bundle, err := selectedBundle(request.BuildRequest.BindingResult, artifact.BundleHash)
	if err != nil {
		return ContractLookup{}, nil, fmt.Errorf("%w: selected bundle: %v", ErrInvalidResolveRequest, err)
	}
	path := cloneJoinPath(bundle.GraphPath)
	if path != nil {
		for _, step := range path.Steps {
			lookup.RelationshipVersionIDs = append(lookup.RelationshipVersionIDs, step.RelationshipVersionID)
		}
		lookup.RelationshipVersionIDs = normalizeIDs(lookup.RelationshipVersionIDs)
	}
	if err := lookup.Validate(); err != nil {
		return ContractLookup{}, nil, err
	}
	return lookup, path, nil
}

func (lookup ContractLookup) Validate() error {
	if lookup.Scope.Validate() != nil || lookup.DomainID.Validate() != nil ||
		!containsID(lookup.Scope.DomainIDs, lookup.DomainID) || lookup.IRHash.Validate() != nil ||
		lookup.ModelVersionID.Validate() != nil || len(lookup.MetricVersionIDs) < 1 ||
		len(lookup.MetricVersionIDs) > ir.MaxMetrics || len(lookup.DimensionVersionIDs) > ir.MaxGroupBy+ir.MaxFilters+1 ||
		len(lookup.MemberVersionIDs) > ir.MaxFilters*ir.MaxMembersPerFilter ||
		len(lookup.MemberBindings) > ir.MaxFilters*ir.MaxMembersPerFilter ||
		len(lookup.RelationshipVersionIDs) > MaxResolvedRelationships {
		return ErrInvalidResolveRequest
	}
	for _, values := range [][]askdata.ID{
		lookup.MetricVersionIDs, lookup.DimensionVersionIDs, lookup.MemberVersionIDs, lookup.RelationshipVersionIDs,
	} {
		if !reflect.DeepEqual(values, normalizeIDs(values)) {
			return ErrInvalidResolveRequest
		}
		for _, id := range values {
			if id.Validate() != nil {
				return ErrInvalidResolveRequest
			}
		}
	}
	if lookup.TimeDimensionVersionID != nil {
		if lookup.TimeDimensionVersionID.Validate() != nil ||
			!containsID(lookup.DimensionVersionIDs, *lookup.TimeDimensionVersionID) {
			return ErrInvalidResolveRequest
		}
	}
	if !reflect.DeepEqual(lookup.MemberBindings, normalizeMemberBindings(lookup.MemberBindings)) {
		return ErrInvalidResolveRequest
	}
	for _, binding := range lookup.MemberBindings {
		if binding.DimensionVersionID.Validate() != nil || binding.MemberVersionID.Validate() != nil ||
			!containsID(lookup.DimensionVersionIDs, binding.DimensionVersionID) ||
			!containsID(lookup.MemberVersionIDs, binding.MemberVersionID) {
			return ErrInvalidResolveRequest
		}
	}
	if len(lookup.MemberBindings) != len(lookup.MemberVersionIDs) {
		return ErrInvalidResolveRequest
	}
	return nil
}

func validateSnapshot(lookup ContractLookup, path *graph.JoinPath, snapshot ContractSnapshot) error {
	if snapshot.Release != lookup.Scope.Release || snapshot.ReleaseObjectCount < 1 {
		return fmt.Errorf("%w: release manifest proof mismatch", ErrContractUnavailable)
	}
	switch snapshot.ReleaseStatus {
	case "READY", "ACTIVE", "SUPERSEDED", "RETAINED":
	default:
		return fmt.Errorf("%w: release is not resolvable", ErrContractUnavailable)
	}
	if snapshot.Model.Materialization.Status != "ACTIVE" {
		return ErrMaterializationStale
	}
	if err := validateResolvedContracts(lookup, path, snapshot); err != nil {
		return err
	}
	return validateMemberParameterValues(snapshot)
}

func validateResolvedContracts(lookup ContractLookup, path *graph.JoinPath, snapshot ContractSnapshot) error {
	model := snapshot.Model
	if model.ModelVersionID != lookup.ModelVersionID || model.ModelVersionID.Validate() != nil ||
		model.ContentHash.Validate() != nil || model.DatasetSchemaHash.Validate() != nil ||
		len(model.Fields) == 0 || len(model.Fields) > MaxResolvedFields {
		return fmt.Errorf("%w: model contract", ErrContractUnavailable)
	}
	if err := validateCanonicalObject(model.GrainContract, MaxContractASTBytes); err != nil {
		return fmt.Errorf("%w: grain contract: %v", ErrContractUnavailable, err)
	}
	materialization := model.Materialization
	if materialization.MaterializationID.Validate() != nil || materialization.DatasetID.Validate() != nil ||
		materialization.DatasetVersionID.Validate() != nil || materialization.Status != "ACTIVE" ||
		materialization.SchemaHash.Validate() != nil ||
		materialization.SnapshotHash.Validate() != nil || materialization.SchemaHash != model.DatasetSchemaHash ||
		(materialization.Layer != "DWS" && materialization.Layer != "ADS") ||
		materialization.PublishedSchema != "warehouse_published" ||
		!trustedOutputNamePattern.MatchString(materialization.PublishedName) || materialization.RowCount < 0 {
		return ErrMaterializationStale
	}
	fieldsByID := map[askdata.ID]FieldContract{}
	for index, field := range model.Fields {
		expectedFieldHash, hashErr := fieldContractHash(field)
		if field.FieldID.Validate() != nil || !trustedOutputNamePattern.MatchString(field.Code) ||
			!validFieldRole(field.Role) || !validCanonicalType(field.CanonicalType) ||
			field.ContractHash.Validate() != nil || hashErr != nil || expectedFieldHash != field.ContractHash {
			return fmt.Errorf("%w: fields[%d]", ErrContractUnavailable, index)
		}
		if _, duplicate := fieldsByID[field.FieldID]; duplicate {
			return fmt.Errorf("%w: duplicate field", ErrContractUnavailable)
		}
		fieldsByID[field.FieldID] = field
	}
	if model.PrimaryTimeFieldID != nil {
		field, exists := fieldsByID[*model.PrimaryTimeFieldID]
		if !exists || field.Role != "TIME" || (field.CanonicalType != "DATE" && field.CanonicalType != "DATETIME") {
			return fmt.Errorf("%w: primary time field", ErrContractUnavailable)
		}
	}
	if !reflect.DeepEqual(metricIDs(snapshot.Metrics), lookup.MetricVersionIDs) ||
		!reflect.DeepEqual(dimensionIDs(snapshot.Dimensions), lookup.DimensionVersionIDs) ||
		!reflect.DeepEqual(memberIDs(snapshot.Members), lookup.MemberVersionIDs) ||
		!reflect.DeepEqual(relationshipIDs(snapshot.Relationships), lookup.RelationshipVersionIDs) {
		return fmt.Errorf("%w: exact object set mismatch", ErrContractUnavailable)
	}
	seenMetrics := map[askdata.ID]struct{}{}
	for index, metric := range snapshot.Metrics {
		if metric.MetricVersionID.Validate() != nil || metric.ModelVersionID != model.ModelVersionID ||
			metric.ContentHash.Validate() != nil || validateResolvedAdditivity(
			metric.Additivity, metric.SemiAdditiveTimeAggregation, metric.AggregationRestriction,
			metric.NonAdditiveDimensions, metric.Unit, metric.Currency, metric.ZeroDenominatorPolicy,
		) != nil ||
			metric.DisplayPrecision < 0 || metric.DisplayPrecision > 12 ||
			!validNullPolicy(metric.NullPolicy) || !validMetricTimeGrain(metric.TimeGrain) ||
			len(metric.Measures) < 1 || len(metric.Measures) > MaxResolvedMeasures ||
			validateCanonicalObject(metric.FormulaAST, MaxContractASTBytes) != nil ||
			validateCanonicalObject(metric.DefaultFilterAST, MaxContractASTBytes) != nil {
			return fmt.Errorf("%w: metrics[%d]", ErrContractUnavailable, index)
		}
		if _, duplicate := seenMetrics[metric.MetricVersionID]; duplicate {
			return fmt.Errorf("%w: duplicate metric", ErrContractUnavailable)
		}
		seenMetrics[metric.MetricVersionID] = struct{}{}
		seenMeasures := map[askdata.ID]struct{}{}
		seenMeasureObjects := map[askdata.ID]struct{}{}
		for measureIndex, measure := range metric.Measures {
			if measure.MeasureID.Validate() != nil || measure.MeasureVersionID.Validate() != nil ||
				measure.ModelVersionID != model.ModelVersionID ||
				!validAggregation(measure.Aggregation) || validateResolvedAdditivity(
				measure.Additivity, measure.SemiAdditiveTimeAggregation, measure.AggregationRestriction,
				measure.NonAdditiveDimensions, measure.Unit, measure.Currency, measure.ZeroDenominatorPolicy,
			) != nil ||
				!validNumericDataType(measure.DataType) ||
				measure.ContentHash.Validate() != nil || validateCanonicalObject(measure.FormulaAST, MaxContractASTBytes) != nil {
				return fmt.Errorf("%w: metrics[%d].measures[%d]", ErrContractUnavailable, index, measureIndex)
			}
			if _, duplicate := seenMeasures[measure.MeasureVersionID]; duplicate {
				return fmt.Errorf("%w: duplicate measure", ErrContractUnavailable)
			}
			if _, duplicate := seenMeasureObjects[measure.MeasureID]; duplicate {
				return fmt.Errorf("%w: duplicate measure object", ErrContractUnavailable)
			}
			seenMeasures[measure.MeasureVersionID] = struct{}{}
			seenMeasureObjects[measure.MeasureID] = struct{}{}
		}
	}
	dimensionSet := map[askdata.ID]struct{}{}
	for index, dimension := range snapshot.Dimensions {
		if dimension.DimensionVersionID.Validate() != nil || dimension.ModelVersionID != model.ModelVersionID ||
			dimension.LogicalFieldID.Validate() != nil || dimension.ContentHash.Validate() != nil ||
			!validDimensionKind(dimension.Kind) || !validSensitivity(dimension.Sensitivity) ||
			!validMemberIndexPolicy(dimension.MemberIndexPolicy) {
			return fmt.Errorf("%w: dimensions[%d] model", ErrContractUnavailable, index)
		}
		field, exists := fieldsByID[dimension.LogicalFieldID]
		if !exists || !dimensionFieldCompatible(dimension.Kind, field) {
			return fmt.Errorf("%w: dimensions[%d] field", ErrContractUnavailable, index)
		}
		if _, duplicate := dimensionSet[dimension.DimensionVersionID]; duplicate {
			return fmt.Errorf("%w: duplicate dimension", ErrContractUnavailable)
		}
		dimensionSet[dimension.DimensionVersionID] = struct{}{}
	}
	if lookup.TimeDimensionVersionID != nil {
		if model.PrimaryTimeFieldID == nil {
			return fmt.Errorf("%w: time dimension without model primary time field", ErrContractUnavailable)
		}
		timeDimension, exists := dimensionByID(snapshot.Dimensions, *lookup.TimeDimensionVersionID)
		if !exists || timeDimension.Kind != registry.DimensionTime ||
			timeDimension.LogicalFieldID != *model.PrimaryTimeFieldID {
			return fmt.Errorf("%w: time dimension field mismatch", ErrContractUnavailable)
		}
	}
	memberByID := make(map[askdata.ID]MemberContract, len(snapshot.Members))
	for index, member := range snapshot.Members {
		if member.MemberVersionID.Validate() != nil || member.DimensionVersionID.Validate() != nil ||
			member.ContentHash.Validate() != nil || !validSensitivity(member.Sensitivity) {
			return fmt.Errorf("%w: members[%d] contract", ErrContractUnavailable, index)
		}
		dimension, exists := dimensionByID(snapshot.Dimensions, member.DimensionVersionID)
		if !exists || sensitivityRank(member.Sensitivity) < sensitivityRank(dimension.Sensitivity) {
			return fmt.Errorf("%w: members[%d] dimension", ErrContractUnavailable, index)
		}
		if _, duplicate := memberByID[member.MemberVersionID]; duplicate {
			return fmt.Errorf("%w: duplicate member", ErrContractUnavailable)
		}
		memberByID[member.MemberVersionID] = member
	}
	for _, binding := range lookup.MemberBindings {
		member, exists := memberByID[binding.MemberVersionID]
		if !exists || member.DimensionVersionID != binding.DimensionVersionID {
			return fmt.Errorf("%w: member FILTER binding mismatch", ErrContractUnavailable)
		}
	}
	if err := validateRelationshipPath(path, snapshot.Relationships); err != nil {
		return err
	}
	return nil
}

// validateMemberParameterValues is deliberately separate from the public
// Resolution contract. A serialized Resolution remains replayable and
// label-free; only the live PostgreSQL resolution path must carry the exact
// canonical member keys needed by the adapter in this process.
func validateMemberParameterValues(snapshot ContractSnapshot) error {
	memberByID := make(map[askdata.ID]struct{}, len(snapshot.Members))
	for _, member := range snapshot.Members {
		memberByID[member.MemberVersionID] = struct{}{}
	}
	if len(snapshot.memberParameterValues) != len(memberByID) {
		return fmt.Errorf("%w: member parameter set", ErrContractUnavailable)
	}
	for memberVersionID, parameterValue := range snapshot.memberParameterValues {
		if _, exists := memberByID[memberVersionID]; !exists || !validMemberParameterValue(parameterValue) {
			return fmt.Errorf("%w: member parameter", ErrContractUnavailable)
		}
	}
	return nil
}

func validateRelationshipPath(path *graph.JoinPath, relationships []RelationshipContract) error {
	if path == nil {
		if len(relationships) != 0 {
			return fmt.Errorf("%w: relationships without GraphPath", ErrContractUnavailable)
		}
		return nil
	}
	if path.Validate() != nil || !path.Allowed || len(path.Steps) != len(relationships) {
		return fmt.Errorf("%w: GraphPath is not uniquely resolved", ErrContractUnavailable)
	}
	byID := map[askdata.ID]RelationshipContract{}
	for _, relationship := range relationships {
		if relationship.RelationshipVersionID.Validate() != nil || relationship.LeftModelVersionID.Validate() != nil ||
			relationship.RightModelVersionID.Validate() != nil || relationship.ContentHash.Validate() != nil ||
			!validJoinType(relationship.JoinType) || !validCardinality(relationship.Cardinality) ||
			!validFanoutPolicy(relationship.FanoutPolicy) ||
			validateCanonicalObject(relationship.JoinAST, MaxContractASTBytes) != nil {
			return fmt.Errorf("%w: relationship contract", ErrContractUnavailable)
		}
		bridgeModelVersionID := string(relationship.BridgeModelVersionID)
		if (relationship.BridgeModelVersionID != "" && relationship.BridgeModelVersionID.Validate() != nil) ||
			registry.ValidateRelationshipCombination(
				relationship.Cardinality, relationship.FanoutPolicy, bridgeModelVersionID,
			) != nil {
			return fmt.Errorf("%w: relationship fanout contract", ErrContractUnavailable)
		}
		if _, duplicate := byID[relationship.RelationshipVersionID]; duplicate {
			return fmt.Errorf("%w: duplicate relationship", ErrContractUnavailable)
		}
		byID[relationship.RelationshipVersionID] = relationship
	}
	for _, step := range path.Steps {
		contract, exists := byID[step.RelationshipVersionID]
		if !exists || contract.JoinType != step.JoinType || contract.Cardinality != step.Cardinality ||
			contract.FanoutPolicy != step.FanoutPolicy ||
			!sameModelPair(contract.LeftModelVersionID, contract.RightModelVersionID, step.FromModelVersionID, step.ToModelVersionID) {
			return fmt.Errorf("%w: GraphPath relationship mismatch", ErrContractUnavailable)
		}
	}
	return nil
}

func normalizeSnapshot(snapshot ContractSnapshot) (ContractSnapshot, error) {
	result := snapshot
	result.memberParameterValues = cloneMemberParameterValues(snapshot.memberParameterValues)
	var err error
	result.Model.GrainContract, err = canonicalObject(result.Model.GrainContract)
	if err != nil {
		return ContractSnapshot{}, err
	}
	result.Model.Fields = append([]FieldContract(nil), snapshot.Model.Fields...)
	sort.Slice(result.Model.Fields, func(i, j int) bool { return result.Model.Fields[i].FieldID < result.Model.Fields[j].FieldID })
	result.Metrics = append([]MetricContract(nil), snapshot.Metrics...)
	for index := range result.Metrics {
		if result.Metrics[index].ZeroDenominatorPolicy == "" {
			result.Metrics[index].ZeroDenominatorPolicy = registry.ZeroDenominatorNull
		}
		result.Metrics[index].NonAdditiveDimensions = append([]string(nil), snapshot.Metrics[index].NonAdditiveDimensions...)
		sort.Slice(result.Metrics[index].NonAdditiveDimensions, func(i, j int) bool {
			return result.Metrics[index].NonAdditiveDimensions[i] < result.Metrics[index].NonAdditiveDimensions[j]
		})
		result.Metrics[index].FormulaAST, err = canonicalObject(result.Metrics[index].FormulaAST)
		if err != nil {
			return ContractSnapshot{}, err
		}
		result.Metrics[index].DefaultFilterAST, err = canonicalObject(result.Metrics[index].DefaultFilterAST)
		if err != nil {
			return ContractSnapshot{}, err
		}
		result.Metrics[index].Measures = append([]MeasureContract(nil), result.Metrics[index].Measures...)
		for measureIndex := range result.Metrics[index].Measures {
			if result.Metrics[index].Measures[measureIndex].ZeroDenominatorPolicy == "" {
				result.Metrics[index].Measures[measureIndex].ZeroDenominatorPolicy = registry.ZeroDenominatorNull
			}
			result.Metrics[index].Measures[measureIndex].NonAdditiveDimensions = append(
				[]string(nil), result.Metrics[index].Measures[measureIndex].NonAdditiveDimensions...,
			)
			sort.Slice(result.Metrics[index].Measures[measureIndex].NonAdditiveDimensions, func(i, j int) bool {
				return result.Metrics[index].Measures[measureIndex].NonAdditiveDimensions[i] <
					result.Metrics[index].Measures[measureIndex].NonAdditiveDimensions[j]
			})
			result.Metrics[index].Measures[measureIndex].FormulaAST, err = canonicalObject(result.Metrics[index].Measures[measureIndex].FormulaAST)
			if err != nil {
				return ContractSnapshot{}, err
			}
		}
		sort.Slice(result.Metrics[index].Measures, func(i, j int) bool {
			return result.Metrics[index].Measures[i].MeasureVersionID < result.Metrics[index].Measures[j].MeasureVersionID
		})
	}
	sort.Slice(result.Metrics, func(i, j int) bool { return result.Metrics[i].MetricVersionID < result.Metrics[j].MetricVersionID })
	result.Dimensions = append([]DimensionContract(nil), snapshot.Dimensions...)
	sort.Slice(result.Dimensions, func(i, j int) bool {
		return result.Dimensions[i].DimensionVersionID < result.Dimensions[j].DimensionVersionID
	})
	result.Members = append([]MemberContract(nil), snapshot.Members...)
	sort.Slice(result.Members, func(i, j int) bool { return result.Members[i].MemberVersionID < result.Members[j].MemberVersionID })
	result.Relationships = append([]RelationshipContract(nil), snapshot.Relationships...)
	for index := range result.Relationships {
		result.Relationships[index].JoinAST, err = canonicalObject(result.Relationships[index].JoinAST)
		if err != nil {
			return ContractSnapshot{}, err
		}
	}
	sort.Slice(result.Relationships, func(i, j int) bool {
		return result.Relationships[i].RelationshipVersionID < result.Relationships[j].RelationshipVersionID
	})
	return result, nil
}

func finalizeResolution(resolution Resolution) (Resolution, error) {
	resolution.ResolutionHash = ""
	hash, err := resolutionHash(resolution)
	if err != nil {
		return Resolution{}, err
	}
	resolution.ResolutionHash = hash
	if err := resolution.Validate(); err != nil {
		return Resolution{}, err
	}
	return resolution, nil
}

func resolutionHash(resolution Resolution) (askdata.ContentHash, error) {
	copy := resolution
	copy.ResolutionHash = ""
	copy.memberParameterValues = nil
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func (resolution Resolution) memberParameterValue(memberVersionID askdata.ID) (string, bool) {
	value, exists := resolution.memberParameterValues[memberVersionID]
	return value, exists && validMemberParameterValue(value)
}

func fieldContractHash(field FieldContract) (askdata.ContentHash, error) {
	payload, err := registry.CanonicalValue(struct {
		FieldID       askdata.ID `json:"fieldId"`
		Code          string     `json:"code"`
		Role          string     `json:"role"`
		CanonicalType string     `json:"canonicalType"`
		SemanticType  string     `json:"semanticType,omitempty"`
		Nullable      bool       `json:"nullable"`
		Visible       bool       `json:"visible"`
	}{field.FieldID, field.Code, field.Role, field.CanonicalType, field.SemanticType, field.Nullable, field.Visible})
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func validateResolveAccessContext(ctx context.Context, scope askdata.PolicyScope, domainID askdata.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID != string(scope.ActorID) || access.DomainID != string(domainID) {
		return fmt.Errorf("%w: access context", ErrInvalidResolveRequest)
	}
	return nil
}

func validateCanonicalObject(raw []byte, maxBytes int) error {
	if len(raw) == 0 || len(raw) > maxBytes {
		return errors.New("contract JSON size is invalid")
	}
	canonical, err := registry.CanonicalJSON(raw)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("contract JSON is not canonical")
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) == 0 || !safeContractJSON(value, 0) {
		return errors.New("contract JSON is unsafe")
	}
	return nil
}

func canonicalObject(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > MaxContractASTBytes {
		return nil, errors.New("contract JSON size is invalid")
	}
	return registry.CanonicalJSON(raw)
}

func safeContractJSON(value any, depth int) bool {
	if depth > 64 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			switch normalized {
			case "sql", "rawsql", "query", "statement", "password", "secret", "credentials", "rows", "rawdata":
				return false
			}
			if !safeContractJSON(child, depth+1) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !safeContractJSON(child, depth+1) {
				return false
			}
		}
	case string:
		return utf8.ValidString(typed)
	}
	return true
}

func selectedBundle(result binding.Result, hash askdata.ContentHash) (binding.Bundle, error) {
	var selected *binding.Bundle
	for index := range result.Bundles {
		if result.Bundles[index].BundleHash == hash {
			if selected != nil {
				return binding.Bundle{}, errors.New("duplicate bundle hash")
			}
			copy := result.Bundles[index]
			selected = &copy
		}
	}
	if selected == nil {
		return binding.Bundle{}, errors.New("bundle not found")
	}
	return *selected, nil
}

func cloneJoinPath(value *graph.JoinPath) *graph.JoinPath {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Steps = append([]graph.JoinStep(nil), value.Steps...)
	copy.RiskCodes = append([]graph.JoinRiskCode(nil), value.RiskCodes...)
	return &copy
}

func normalizeIDs(values []askdata.ID) []askdata.ID {
	result := append([]askdata.ID(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	write := 0
	for _, value := range result {
		if write > 0 && value == result[write-1] {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func normalizeMemberBindings(values []MemberBinding) []MemberBinding {
	result := append([]MemberBinding(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].DimensionVersionID != result[j].DimensionVersionID {
			return result[i].DimensionVersionID < result[j].DimensionVersionID
		}
		return result[i].MemberVersionID < result[j].MemberVersionID
	})
	write := 0
	for _, value := range result {
		if write > 0 && value == result[write-1] {
			continue
		}
		result[write] = value
		write++
	}
	return result[:write]
}

func dimensionByID(values []DimensionContract, target askdata.ID) (DimensionContract, bool) {
	for _, value := range values {
		if value.DimensionVersionID == target {
			return value, true
		}
	}
	return DimensionContract{}, false
}

func cloneID(value *askdata.ID) *askdata.ID {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneMemberParameterValues(values map[askdata.ID]string) map[askdata.ID]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[askdata.ID]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func validMemberParameterValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= 512 &&
		utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func validAggregation(value registry.Aggregation) bool {
	switch value {
	case registry.AggregationSum, registry.AggregationAverage, registry.AggregationMinimum,
		registry.AggregationMaximum, registry.AggregationCount, registry.AggregationCountDistinct:
		return true
	default:
		return false
	}
}

func validAdditivity(value registry.Additivity) bool {
	return value == registry.Additive || value == registry.SemiAdditive || value == registry.NonAdditive
}

func validateResolvedAdditivity(
	additivity registry.Additivity,
	semi registry.SemiAdditiveTimeAggregation,
	restriction registry.AggregationRestriction,
	nonAdditiveDimensions []string,
	unit, currency string,
	zeroPolicy registry.ZeroDenominatorPolicy,
) error {
	if !validAdditivity(additivity) || strings.TrimSpace(unit) == "" ||
		(zeroPolicy != registry.ZeroDenominatorNull && zeroPolicy != registry.ZeroDenominatorZero) {
		return ErrContractUnavailable
	}
	if strings.EqualFold(strings.TrimSpace(unit), "CURRENCY") && strings.TrimSpace(currency) == "" {
		return ErrContractUnavailable
	}
	if additivity == registry.SemiAdditive && semi != registry.SemiAdditivePeriodEnd &&
		semi != registry.SemiAdditivePeriodBegin && semi != registry.SemiAdditivePeriodAverage {
		return ErrContractUnavailable
	}
	if additivity == registry.NonAdditive && restriction != registry.PostAggregate {
		return ErrContractUnavailable
	}
	seen := make(map[string]struct{}, len(nonAdditiveDimensions))
	for _, raw := range nonAdditiveDimensions {
		id := askdata.ID(raw)
		if id.Validate() != nil {
			return ErrContractUnavailable
		}
		if _, duplicate := seen[raw]; duplicate {
			return ErrContractUnavailable
		}
		seen[raw] = struct{}{}
	}
	return nil
}

func validNumericDataType(value registry.NumericDataType) bool {
	return value == registry.NumericInteger || value == registry.NumericDecimal
}

func validDimensionKind(value registry.DimensionKind) bool {
	return value == registry.DimensionCategorical || value == registry.DimensionTime || value == registry.DimensionEntity
}

func validSensitivity(value registry.Sensitivity) bool {
	return value == registry.SensitivityPublic || value == registry.SensitivityInternal ||
		value == registry.SensitivityConfidential || value == registry.SensitivityRestricted
}

func validMemberIndexPolicy(value registry.MemberIndexPolicy) bool {
	return value == registry.MemberIndexFull || value == registry.MemberIndexExactOnly ||
		value == registry.MemberIndexOnDemand || value == registry.MemberIndexNone
}

func validJoinType(value registry.JoinType) bool {
	return value == registry.JoinInner || value == registry.JoinLeft || value == registry.JoinRight || value == registry.JoinFull
}

func validCardinality(value registry.Cardinality) bool {
	return value == registry.CardinalityOneToOne || value == registry.CardinalityManyToOne ||
		value == registry.CardinalityOneToMany || value == registry.CardinalityManyToMany
}

func validFanoutPolicy(value registry.FanoutPolicy) bool {
	return registry.ValidFanoutPolicy(value)
}

func validMetricTimeGrain(value string) bool {
	switch value {
	case "NONE", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR":
		return true
	default:
		return false
	}
}

func validNullPolicy(value string) bool {
	return value == "PRESERVE" || value == "ZERO" || value == "REJECT"
}

func validFieldRole(value string) bool {
	switch value {
	case "DIMENSION", "MEASURE", "ATTRIBUTE", "TIME", "IDENTIFIER":
		return true
	default:
		return false
	}
}

func validCanonicalType(value string) bool {
	switch value {
	case "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME":
		return true
	default:
		return false
	}
}

func dimensionFieldCompatible(kind registry.DimensionKind, field FieldContract) bool {
	if kind == registry.DimensionTime {
		return field.Role == "TIME" && (field.CanonicalType == "DATE" || field.CanonicalType == "DATETIME")
	}
	return field.Role == "DIMENSION" || field.Role == "ATTRIBUTE" || field.Role == "IDENTIFIER"
}

func sensitivityRank(value registry.Sensitivity) int {
	switch value {
	case registry.SensitivityPublic:
		return 1
	case registry.SensitivityInternal:
		return 2
	case registry.SensitivityConfidential:
		return 3
	case registry.SensitivityRestricted:
		return 4
	default:
		return 0
	}
}

func metricIDs(values []MetricContract) []askdata.ID {
	result := make([]askdata.ID, len(values))
	for index, value := range values {
		result[index] = value.MetricVersionID
	}
	return normalizeIDs(result)
}

func dimensionIDs(values []DimensionContract) []askdata.ID {
	result := make([]askdata.ID, len(values))
	for index, value := range values {
		result[index] = value.DimensionVersionID
	}
	return normalizeIDs(result)
}

func memberIDs(values []MemberContract) []askdata.ID {
	result := make([]askdata.ID, len(values))
	for index, value := range values {
		result[index] = value.MemberVersionID
	}
	return normalizeIDs(result)
}

func relationshipIDs(values []RelationshipContract) []askdata.ID {
	result := make([]askdata.ID, len(values))
	for index, value := range values {
		result[index] = value.RelationshipVersionID
	}
	return normalizeIDs(result)
}

func containsID(values []askdata.ID, target askdata.ID) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= target })
	return index < len(values) && values[index] == target
}

func sameModelPair(leftA, rightA, leftB, rightB askdata.ID) bool {
	return (leftA == leftB && rightA == rightB) || (leftA == rightB && rightA == leftB)
}
