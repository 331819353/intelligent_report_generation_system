package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

const AdapterVersion = "semantic-query-adapter-v2"

var (
	ErrInvalidAdaptRequest = errors.New("semantic query adapt request is invalid")
	ErrUnsupportedQuery    = errors.New("semantic query is not supported by the deterministic adapter")
	ErrInvalidQueryPlan    = errors.New("semantic query plan is invalid")
)

type QueryRole string

const (
	QueryRoleCurrent  QueryRole = "CURRENT"
	QueryRoleBaseline QueryRole = "BASELINE"
)

// ParameterShape is safe to persist: it records only placeholder shape, never
// the canonical member key or a time boundary supplied to PostgreSQL.
type ParameterShape struct {
	Code        string `json:"code"`
	DataType    string `json:"dataType"`
	MultiValue  bool   `json:"multiValue"`
	Required    bool   `json:"required"`
	Cardinality int    `json:"cardinality"`
}

type PhysicalColumn struct {
	Code          string `json:"code"`
	CanonicalType string `json:"canonicalType"`
}

// PhysicalSource is derived only from the release-pinned ACTIVE
// materialization. It is the exact whitelist replayed by QUERY-004/005.
type PhysicalSource struct {
	NodeID            string           `json:"nodeId"`
	DatasetVersionID  askdata.ID       `json:"datasetVersionId"`
	MaterializationID askdata.ID       `json:"materializationId"`
	PublishedSchema   string           `json:"publishedSchema"`
	PublishedName     string           `json:"publishedName"`
	Columns           []PhysicalColumn `json:"columns"`
}

type QueryPlan struct {
	Role     QueryRole        `json:"role"`
	Document dataset.Document `json:"document"`
	Source   PhysicalSource   `json:"source"`
	// JoinedSources lists the physical tables reached through the resolved join
	// path. Omitted for single-model plans, which keeps their canonical form —
	// and every plan hash compiled before joins existed — byte-identical.
	JoinedSources    []PhysicalSource    `json:"joinedSources,omitempty"`
	ParameterShapes  []ParameterShape    `json:"parameterShapes"`
	DSLHash          askdata.ContentHash `json:"dslHash"`
	LogicalPlanHash  askdata.ContentHash `json:"logicalPlanHash"`
	CompiledPlanHash askdata.ContentHash `json:"compiledPlanHash"`
	PlanHash         askdata.ContentHash `json:"planHash"`
	compiled         *querycompiler.CompiledQuery
	parameterValues  map[string]any
}

type ComparisonContract struct {
	Type    ir.ComparisonType `json:"type"`
	Periods int               `json:"periods"`
}

// MetricAggregationContract carries the release-pinned aggregation behavior
// into result verification. ADD-004 can project it to result columns without
// re-reading mutable registry state.
type MetricAggregationContract struct {
	MetricVersionID             askdata.ID                           `json:"metricVersionId"`
	ResultColumnName            string                               `json:"resultColumnName"`
	Additivity                  registry.Additivity                  `json:"additivity"`
	SemiAdditiveTimeAggregation registry.SemiAdditiveTimeAggregation `json:"semiAdditiveTimeAggregation,omitempty"`
	AggregationRestriction      registry.AggregationRestriction      `json:"aggregationRestriction,omitempty"`
	NonAdditiveDimensions       []string                             `json:"nonAdditiveDimensions"`
	TotalsNotSummable           bool                                 `json:"totalsNotSummable"`
	Unit                        string                               `json:"unit"`
	Currency                    string                               `json:"currency,omitempty"`
	DisplayPrecision            int16                                `json:"displayPrecision"`
	ZeroDenominatorPolicy       registry.ZeroDenominatorPolicy       `json:"zeroDenominatorPolicy"`
}

// QueryArtifact is the replay-safe QUERY_PLAN boundary. SQL and parameter
// values are intentionally not JSON fields; only their deterministic shape
// hashes enter the artifact.
type QueryArtifact struct {
	Version            string                      `json:"version"`
	Scope              askdata.PolicyScope         `json:"scope"`
	DomainID           askdata.ID                  `json:"domainId"`
	IRHash             askdata.ContentHash         `json:"irHash"`
	BuildArtifactHash  askdata.ContentHash         `json:"buildArtifactHash"`
	ResolutionHash     askdata.ContentHash         `json:"resolutionHash"`
	GraphPlanHash      askdata.ContentHash         `json:"graphPlanHash"`
	Timezone           string                      `json:"timezone,omitempty"`
	Comparison         *ComparisonContract         `json:"comparison,omitempty"`
	ResolvedTimeSpec   *ir.ResolvedTimeSpec        `json:"resolvedTimeSpec,omitempty"`
	MetricAggregations []MetricAggregationContract `json:"metricAggregations"`
	Plans              []QueryPlan                 `json:"plans"`
	PlanHash           askdata.ContentHash         `json:"planHash"`
}

type AdaptRequest struct {
	ResolveRequest   ResolveRequest
	Resolution       Resolution
	ResolvedTimeSpec *ir.ResolvedTimeSpec
}

// Adapt replays the entire Binding -> Semantic IR boundary, verifies the
// release-pinned Resolution and produces one formal plan, or CURRENT and
// BASELINE plans for a comparison. It never accepts a physical identifier or
// member value from the user/LLM boundary.
func Adapt(request AdaptRequest) (QueryArtifact, error) {
	if err := validateAdaptRequest(request); err != nil {
		return QueryArtifact{}, err
	}
	return compileResolvedArtifact(
		request.ResolveRequest.BuildArtifact.IR, request.Resolution, request.ResolvedTimeSpec,
	)
}

func compileResolvedArtifact(
	semanticIR ir.SemanticIR,
	resolution Resolution,
	resolvedTimeSpec *ir.ResolvedTimeSpec,
) (QueryArtifact, error) {
	queryIR, err := applyResolvedTimeSpec(semanticIR, resolvedTimeSpec)
	if err != nil {
		return QueryArtifact{}, err
	}
	// A resolved join path is compiled through the Query DSL's own join support
	// (buildDatasetJoins). Relationships without loaded contracts for the models
	// they reach are still refused there, and fanout-bearing edges are refused
	// by assertNonFanoutJoin rather than compiled into an inflated aggregate.
	if len(resolution.Relationships) != 0 && len(resolution.JoinedModels) == 0 {
		return QueryArtifact{}, fmt.Errorf(
			"%w: join path resolved without the joined model contracts", ErrUnsupportedQuery,
		)
	}

	document, source, parameterValues, shapes, err := buildQueryDocument(queryIR, resolution)
	if err != nil {
		return QueryArtifact{}, err
	}
	placement, err := placeJoinedModels(resolution)
	if err != nil {
		return QueryArtifact{}, err
	}
	joinedSources := placement.sources
	metricPlans, err := planMetrics(resolution.Metrics, queryIR)
	if err != nil {
		return QueryArtifact{}, err
	}
	artifact := QueryArtifact{
		Version: AdapterVersion, Scope: resolution.Scope, DomainID: resolution.DomainID,
		IRHash: resolution.IRHash, BuildArtifactHash: resolution.BuildArtifactHash,
		ResolutionHash: resolution.ResolutionHash, GraphPlanHash: resolution.GraphPlanHash,
		MetricAggregations: metricAggregationContracts(resolution.Metrics, metricPlans, queryIR.Metrics),
		Plans:              []QueryPlan{},
	}
	if queryIR.TimeRange != nil {
		artifact.Timezone = queryIR.TimeRange.Timezone
	}
	if resolvedTimeSpec != nil {
		copy := *resolvedTimeSpec
		if copy.Comparison != nil {
			comparisonCopy := *copy.Comparison
			copy.Comparison = &comparisonCopy
		}
		artifact.ResolvedTimeSpec = &copy
	}
	if semanticIR.Comparison != nil {
		artifact.Comparison = &ComparisonContract{
			Type: semanticIR.Comparison.Type, Periods: semanticIR.Comparison.Periods,
		}
	}

	current, err := compileQueryPlan(QueryRoleCurrent, document, source, joinedSources, shapes, parameterValues, ir.MaxResultRows)
	if err != nil {
		return QueryArtifact{}, err
	}
	artifact.Plans = append(artifact.Plans, current)
	if semanticIR.Comparison != nil {
		baselineValues, err := baselineParameterValues(queryIR, resolution, parameterValues, resolvedTimeSpec)
		if err != nil {
			return QueryArtifact{}, err
		}
		baseline, err := compileQueryPlan(QueryRoleBaseline, document, source, joinedSources, shapes, baselineValues, ir.MaxResultRows)
		if err != nil {
			return QueryArtifact{}, err
		}
		artifact.Plans = append(artifact.Plans, baseline)
	}
	artifact.PlanHash, err = queryArtifactHash(artifact)
	if err != nil {
		return QueryArtifact{}, err
	}
	if err := artifact.Validate(); err != nil {
		return QueryArtifact{}, err
	}
	return artifact, nil
}

func validateAdaptRequest(request AdaptRequest) error {
	buildArtifact := request.ResolveRequest.BuildArtifact
	if err := buildArtifact.ValidateAgainst(request.ResolveRequest.BuildRequest); err != nil {
		return fmt.Errorf("%w: replay Semantic IR: %v", ErrInvalidAdaptRequest, err)
	}
	if err := request.Resolution.Validate(); err != nil {
		return fmt.Errorf("%w: resolution: %v", ErrInvalidAdaptRequest, err)
	}
	resolution := request.Resolution
	if !reflect.DeepEqual(resolution.Scope, buildArtifact.Scope) || resolution.DomainID != buildArtifact.DomainID ||
		resolution.IRHash != buildArtifact.IRHash || resolution.BuildArtifactHash != buildArtifact.ArtifactHash ||
		resolution.DomainID != buildArtifact.IR.DomainID ||
		resolution.Model.ModelVersionID != buildArtifact.IR.ModelVersionID ||
		resolution.Scope.Release.ReleaseID != buildArtifact.IR.SemanticReleaseID ||
		resolution.Scope.Release.ContentHash != buildArtifact.IR.SemanticContentHash ||
		resolution.GraphPlanHash != request.ResolveRequest.BuildRequest.BindingResult.GraphPlanHash {
		return fmt.Errorf("%w: resolution is not bound to the exact IR artifact", ErrInvalidAdaptRequest)
	}
	return nil
}

func applyResolvedTimeSpec(semanticIR ir.SemanticIR, spec *ir.ResolvedTimeSpec) (ir.SemanticIR, error) {
	if spec == nil {
		return semanticIR, nil
	}
	if semanticIR.TimeRange == nil || validateResolvedTimeSpec(*spec) != nil {
		return ir.SemanticIR{}, fmt.Errorf("%w: resolved time spec", ErrInvalidAdaptRequest)
	}
	if semanticIR.TimeRange.RequestedPeriod != "" && semanticIR.TimeRange.RequestedPeriod != spec.RequestedPeriod {
		return ir.SemanticIR{}, fmt.Errorf("%w: resolved period does not match IR", ErrInvalidAdaptRequest)
	}
	if (semanticIR.Comparison == nil) != (spec.Comparison == nil) {
		return ir.SemanticIR{}, fmt.Errorf("%w: resolved comparison does not match IR", ErrInvalidAdaptRequest)
	}
	if semanticIR.Comparison != nil && (spec.Comparison.Periods != semanticIR.Comparison.Periods ||
		spec.Comparison.Type != resolvedComparisonType(semanticIR.Comparison.Type, spec.Grain)) {
		return ir.SemanticIR{}, fmt.Errorf("%w: resolved comparison contract mismatch", ErrInvalidAdaptRequest)
	}
	loc, err := time.LoadLocation(spec.Timezone)
	if err != nil || !isLocalMidnight(spec.ResolvedStart, loc) || !isLocalMidnight(spec.ResolvedEndExclusive, loc) {
		return ir.SemanticIR{}, fmt.Errorf("%w: resolved time boundary", ErrInvalidAdaptRequest)
	}
	result := semanticIR
	timeRange := *semanticIR.TimeRange
	timeRange.Start = spec.ResolvedStart.In(loc).Format("2006-01-02")
	timeRange.EndExclusive = spec.ResolvedEndExclusive.In(loc).Format("2006-01-02")
	timeRange.Timezone = spec.Timezone
	result.TimeRange = &timeRange
	return result, nil
}

func resolvedComparisonType(value ir.ComparisonType, grain string) string {
	switch value {
	case ir.ComparisonYearOverYear:
		return "YEAR_OVER_YEAR"
	case ir.ComparisonMonthOverMonth:
		return "MONTH_OVER_MONTH"
	case ir.ComparisonPeriodOverPeriod:
		switch registry.TimeGrain(grain) {
		case registry.TimeGrainWeek:
			return "WEEK_OVER_WEEK"
		case registry.TimeGrainMonth, registry.TimeGrainFiscalMonth:
			return "MONTH_OVER_MONTH"
		case registry.TimeGrainQuarter, registry.TimeGrainFiscalQuarter:
			return "QUARTER_OVER_QUARTER"
		case registry.TimeGrainYear, registry.TimeGrainFiscalYear:
			return "YEAR_OVER_YEAR"
		default:
			return "PERIOD_OVER_PERIOD"
		}
	default:
		return ""
	}
}

func isLocalMidnight(value time.Time, loc *time.Location) bool {
	local := value.In(loc)
	return local.Hour() == 0 && local.Minute() == 0 && local.Second() == 0 && local.Nanosecond() == 0
}

func buildQueryDocument(
	semanticIR ir.SemanticIR,
	resolution Resolution,
) (dataset.Document, PhysicalSource, map[string]any, []ParameterShape, error) {
	_, _, semanticIRHash, err := ir.Canonicalize(semanticIR)
	if err != nil {
		return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: canonical Semantic IR: %v", ErrInvalidAdaptRequest, err)
	}
	fieldsByID := make(map[askdata.ID]FieldContract, len(resolution.Model.Fields))
	fieldCodes := make(map[string]struct{}, len(resolution.Model.Fields))
	columns := make([]PhysicalColumn, 0, len(resolution.Model.Fields))
	projection := make([]string, 0, len(resolution.Model.Fields))
	for _, field := range resolution.Model.Fields {
		if _, duplicate := fieldCodes[field.Code]; duplicate {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: duplicate model field code", ErrInvalidQueryPlan)
		}
		fieldCodes[field.Code] = struct{}{}
		fieldsByID[field.FieldID] = field
		columns = append(columns, PhysicalColumn{Code: field.Code, CanonicalType: field.CanonicalType})
		projection = append(projection, field.Code)
	}
	sort.Slice(columns, func(i, j int) bool { return columns[i].Code < columns[j].Code })
	sort.Strings(projection)
	source := PhysicalSource{
		NodeID: anchorNodeID, DatasetVersionID: resolution.Model.Materialization.DatasetVersionID,
		MaterializationID: resolution.Model.Materialization.MaterializationID,
		PublishedSchema:   resolution.Model.Materialization.PublishedSchema,
		PublishedName:     resolution.Model.Materialization.PublishedName, Columns: columns,
	}
	// Measure fields stay anchor-only. A metric is defined on the model the IR
	// names, so letting metric compilation see a joined model's fields would let
	// an aggregate silently move across a join and change its grain. The copy is
	// taken before placement, which adds the joined models' fields to fieldsByID
	// for dimension resolution.
	anchorFieldsByID := make(map[askdata.ID]FieldContract, len(fieldsByID))
	for id, field := range fieldsByID {
		anchorFieldsByID[id] = field
	}
	placement, err := placeJoinedModels(resolution)
	if err != nil {
		return dataset.Document{}, PhysicalSource{}, nil, nil, err
	}
	joinedSources, nodeByFieldID, joinedProjection := placement.sources, placement.nodeByFieldID, placement.projections
	for id, field := range placement.fields {
		fieldsByID[id] = field
	}

	dimensionsByID := make(map[askdata.ID]DimensionContract, len(resolution.Dimensions))
	for _, dimension := range resolution.Dimensions {
		dimensionsByID[dimension.DimensionVersionID] = dimension
	}
	metricsByID := make(map[askdata.ID]MetricContract, len(resolution.Metrics))
	for _, metric := range resolution.Metrics {
		metricsByID[metric.MetricVersionID] = metric
	}
	metricPlans, err := planMetrics(resolution.Metrics, semanticIR)
	if err != nil {
		return dataset.Document{}, PhysicalSource{}, nil, nil, err
	}
	useTimeReduction := false
	for _, plan := range metricPlans {
		useTimeReduction = useTimeReduction || plan.UseTimeReduction
	}
	var timeReductionField FieldContract
	var timeReduction *dataset.PreAggregation
	preAggregationGroups := map[string]struct{}{}
	if useTimeReduction {
		if resolution.Model.PrimaryTimeFieldID == nil {
			return dataset.Document{}, PhysicalSource{}, nil, nil,
				aggregationFailure(SemiAdditiveTimeDimensionMissingCode, "")
		}
		var exists bool
		timeReductionField, exists = fieldsByID[*resolution.Model.PrimaryTimeFieldID]
		if !exists || (timeReductionField.CanonicalType != "DATE" && timeReductionField.CanonicalType != "DATETIME") {
			return dataset.Document{}, PhysicalSource{}, nil, nil,
				aggregationFailure(SemiAdditiveTimeDimensionMissingCode, "")
		}
		timeReduction = &dataset.PreAggregation{
			ID: "askdata_time_reduction", NodeID: source.NodeID,
			GroupBy: []dataset.PreAggregationGroup{}, Metrics: []dataset.PreAggregationMetric{},
		}
	}

	visible := true
	outputFields := make([]dataset.Field, 0, len(semanticIR.GroupBy)+len(semanticIR.Metrics))
	groupBy := make([]string, 0, len(semanticIR.GroupBy))
	fieldIDByDimension := make(map[askdata.ID]string, len(semanticIR.GroupBy))
	fieldIDByMetric := make(map[askdata.ID]string, len(semanticIR.Metrics))
	for _, group := range semanticIR.GroupBy {
		dimension, exists := dimensionsByID[group.DimensionVersionID]
		if !exists {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: group dimension contract is missing", ErrInvalidAdaptRequest)
		}
		field, exists := fieldsByID[dimension.LogicalFieldID]
		if !exists {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: group field contract is missing", ErrInvalidAdaptRequest)
		}
		expression := fieldReferenceOn(field, nodeByFieldID[field.FieldID])
		if group.Grain != nil {
			truncationArgument := expression
			truncated := dataset.Expression{Type: "DATE_TRUNC", Unit: string(*group.Grain), Argument: &truncationArgument}
			expression = truncated
			if field.CanonicalType == "DATE" {
				castArgument := truncated
				expression = dataset.Expression{Type: "CAST", TargetType: "DATE", Argument: &castArgument}
			}
		}
		fieldID := string(field.FieldID)
		outputFields = append(outputFields, dataset.Field{
			ID: fieldID, Code: field.Code, Name: field.Code, Role: field.Role,
			Expression: expression, CanonicalType: field.CanonicalType,
			SemanticType: field.SemanticType, Nullable: field.Nullable, Visible: &visible,
		})
		groupBy = append(groupBy, fieldID)
		fieldIDByDimension[group.DimensionVersionID] = fieldID
		if timeReduction != nil {
			if _, duplicate := preAggregationGroups[field.Code]; !duplicate {
				timeReduction.GroupBy = append(timeReduction.GroupBy, dataset.PreAggregationGroup{Field: field.Code})
				preAggregationGroups[field.Code] = struct{}{}
			}
		}
	}
	if timeReduction != nil {
		if _, duplicate := preAggregationGroups[timeReductionField.Code]; !duplicate {
			timeReduction.GroupBy = append(timeReduction.GroupBy, dataset.PreAggregationGroup{Field: timeReductionField.Code})
			preAggregationGroups[timeReductionField.Code] = struct{}{}
		}
	}
	for _, selected := range semanticIR.Metrics {
		metric, exists := metricsByID[selected.MetricVersionID]
		if !exists {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: metric contract is missing", ErrInvalidAdaptRequest)
		}
		var expression dataset.Expression
		var canonicalType string
		if timeReduction == nil {
			expression, canonicalType, err = compileMetricExpression(metric, anchorFieldsByID)
		} else {
			var inner []dataset.PreAggregationMetric
			expression, canonicalType, inner, err = compileMetricExpressionPreAggregated(metric, anchorFieldsByID, timeReductionField)
			timeReduction.Metrics = append(timeReduction.Metrics, inner...)
		}
		if err != nil {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("metric %s: %w", selected.MetricVersionID, err)
		}
		fieldID := stableDatasetIdentifier("metric", selected.MetricVersionID)
		outputFields = append(outputFields, dataset.Field{
			ID: fieldID, Code: selected.Alias, Name: selected.Alias, Role: "MEASURE",
			Expression: expression, CanonicalType: canonicalType, Unit: metric.Unit,
			Nullable: metric.NullPolicy != "ZERO", Visible: &visible,
		})
		fieldIDByMetric[selected.MetricVersionID] = fieldID
	}
	// The result projection is flat, so two selected columns sharing a code are
	// ambiguous no matter which models they came from. Join keys legitimately
	// repeat a code across models, which is why this is checked on the selected
	// output rather than on every field the joined models expose.
	outputCodes := make(map[string]struct{}, len(outputFields))
	for _, field := range outputFields {
		if _, duplicate := outputCodes[field.Code]; duplicate {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf(
				"%w: result column %q is ambiguous across joined models",
				ErrInvalidQueryPlan, field.Code,
			)
		}
		outputCodes[field.Code] = struct{}{}
	}

	filters := make([]dataset.Filter, 0, len(semanticIR.Filters)+2)
	parameters := make([]dataset.Parameter, 0, len(semanticIR.Filters)+2)
	parameterValues := make(map[string]any, len(semanticIR.Filters)+2)
	shapes := make([]ParameterShape, 0, len(semanticIR.Filters)+2)
	for index, filter := range semanticIR.Filters {
		dimension, exists := dimensionsByID[filter.DimensionVersionID]
		if !exists {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: filter dimension contract is missing", ErrInvalidAdaptRequest)
		}
		field, exists := fieldsByID[dimension.LogicalFieldID]
		if !exists {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: filter field contract is missing", ErrInvalidAdaptRequest)
		}
		left := fieldReferenceOn(field, nodeByFieldID[field.FieldID])
		filterID := stableDatasetIdentifier("member_filter", askdata.ID(fmt.Sprintf("%s:%s:%d", filter.DimensionVersionID, filter.Operator, index)))
		if filter.Operator == ir.FilterIsNull || filter.Operator == ir.FilterIsNotNull {
			argument := left
			filters = append(filters, dataset.Filter{ID: filterID, Stage: "PRE_AGGREGATION", Expression: dataset.Expression{
				Type: string(filter.Operator), Argument: &argument,
			}})
			continue
		}
		multi := filter.Operator == ir.FilterIn || filter.Operator == ir.FilterNotIn
		if !multi && len(filter.MemberVersionIDs) != 1 {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: scalar member filter must bind exactly one member", ErrInvalidAdaptRequest)
		}
		code := stableDatasetIdentifier("member", askdata.ID(fmt.Sprintf("%s:%s:%d", filter.DimensionVersionID, filter.Operator, index)))
		values := make([]string, 0, len(filter.MemberVersionIDs))
		for _, memberVersionID := range filter.MemberVersionIDs {
			value, exists := resolution.memberParameterValue(memberVersionID)
			if !exists {
				return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: live member parameter is unavailable", ErrInvalidAdaptRequest)
			}
			values = append(values, value)
		}
		parameter := dataset.Parameter{
			Code: code, Name: "governed member", DataType: field.CanonicalType,
			MultiValue: multi, Required: true,
		}
		parameters = append(parameters, parameter)
		shapes = append(shapes, parameterShape(parameter, len(values)))
		if multi {
			parameterValues[code] = values
		} else {
			parameterValues[code] = values[0]
		}
		right := dataset.Expression{Type: "PARAM_REF", Code: code}
		filters = append(filters, dataset.Filter{ID: filterID, Stage: "PRE_AGGREGATION", Expression: dataset.Expression{
			Type: string(filter.Operator), Left: &left, Right: &right,
		}})
	}
	if semanticIR.TimeRange != nil {
		dimension, exists := dimensionsByID[semanticIR.TimeRange.DimensionVersionID]
		if !exists {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: time dimension contract is missing", ErrInvalidAdaptRequest)
		}
		field, exists := fieldsByID[dimension.LogicalFieldID]
		if !exists {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: time field contract is missing", ErrInvalidAdaptRequest)
		}
		start, end, err := timeBoundaryValues(*semanticIR.TimeRange, field.CanonicalType)
		if err != nil {
			return dataset.Document{}, PhysicalSource{}, nil, nil, err
		}
		for _, boundary := range []struct {
			code, operator, value string
		}{
			{"time_start", "GTE", start},
			{"time_end_exclusive", "LT", end},
		} {
			parameter := dataset.Parameter{
				Code: boundary.code, Name: "governed time boundary",
				DataType: field.CanonicalType, Required: true,
			}
			parameters = append(parameters, parameter)
			shapes = append(shapes, parameterShape(parameter, 1))
			parameterValues[boundary.code] = boundary.value
			left := fieldReferenceOn(field, nodeByFieldID[field.FieldID])
			right := dataset.Expression{Type: "PARAM_REF", Code: boundary.code}
			filters = append(filters, dataset.Filter{
				ID: stableDatasetIdentifier("time_filter", askdata.ID(boundary.code)), Stage: "PRE_AGGREGATION",
				Expression: dataset.Expression{Type: boundary.operator, Left: &left, Right: &right},
			})
		}
	}

	sorts := make([]dataset.Sort, 0, len(semanticIR.Sort))
	for _, item := range semanticIR.Sort {
		fieldID := ""
		if item.TargetType == ir.SortTargetMetric {
			fieldID = fieldIDByMetric[item.TargetVersionID]
		} else {
			fieldID = fieldIDByDimension[item.TargetVersionID]
		}
		if fieldID == "" {
			return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: sort target is not projected", ErrInvalidAdaptRequest)
		}
		sorts = append(sorts, dataset.Sort{FieldID: fieldID, Direction: string(item.Direction), Nulls: string(item.Nulls)})
	}
	keyFields := make([]string, 0, len(semanticIR.GroupBy))
	for _, group := range semanticIR.GroupBy {
		field := fieldsByID[dimensionsByID[group.DimensionVersionID].LogicalFieldID]
		keyFields = append(keyFields, field.Code)
	}
	if len(keyFields) == 0 {
		keyFields = append(keyFields, semanticIR.Metrics[0].Alias)
	}
	timeField := ""
	defaultTimeGrain := ""
	for _, group := range semanticIR.GroupBy {
		dimension := dimensionsByID[group.DimensionVersionID]
		if dimension.Kind == registry.DimensionTime {
			timeField = fieldsByID[dimension.LogicalFieldID].Code
			if group.Grain != nil {
				defaultTimeGrain = string(*group.Grain)
			}
			break
		}
	}
	previewLimit := semanticIR.Limit
	if previewLimit > 5000 {
		previewLimit = 5000
	}
	node := dataset.Node{
		ID: source.NodeID, Type: "DATASET", DatasetVersionID: string(source.DatasetVersionID),
		Alias: source.NodeID, Projection: projection, SourceFilters: []dataset.SourceFilter{},
	}
	joins, err := buildDatasetJoins(resolution)
	if err != nil {
		return dataset.Document{}, PhysicalSource{}, nil, nil, err
	}
	preAggregations := []dataset.PreAggregation{}
	if timeReduction != nil {
		for _, filter := range filters {
			expression := cloneDatasetExpression(filter.Expression)
			node.SourceFilters = append(node.SourceFilters, dataset.SourceFilter{Expression: &expression})
		}
		filters = []dataset.Filter{}
		preAggregations = append(preAggregations, *timeReduction)
	}
	// Built after the time-reduction block, which appends source filters to the
	// anchor node. Copying the node before that would silently drop them.
	nodes := []dataset.Node{node}
	for _, joined := range joinedSources {
		nodes = append(nodes, dataset.Node{
			ID: joined.NodeID, Type: "DATASET", DatasetVersionID: string(joined.DatasetVersionID),
			Alias: joined.NodeID, Projection: joinedProjection[joined.NodeID],
			SourceFilters: []dataset.SourceFilter{},
		})
	}
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "askdata_query_" + string(semanticIRHash[:16]),
			Name: "AskData governed query", Type: "SINGLE_SOURCE",
		},
		Nodes: nodes,
		Joins: joins, Transforms: []dataset.Transform{}, PreAggregations: preAggregations,
		Fields: outputFields, Filters: filters, GroupBy: groupBy, Having: []dataset.Filter{}, Sorts: sorts,
		Parameters: parameters,
		OutputGrain: dataset.OutputGrain{
			Description: "one row per governed AskData result grain", KeyFields: keyFields,
			TimeField: timeField, DefaultTimeGrain: defaultTimeGrain,
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 25000, PreviewLimit: previewLimit,
			ResultLimit: ir.MaxResultRows, CacheTTLSeconds: 0,
		},
	}
	if err := dataset.Validate(document); err != nil {
		return dataset.Document{}, PhysicalSource{}, nil, nil, fmt.Errorf("%w: generated Dataset DSL: %w", ErrInvalidQueryPlan, err)
	}
	return document, source, parameterValues, shapes, nil
}

func compileQueryPlan(
	role QueryRole,
	document dataset.Document,
	source PhysicalSource,
	joinedSources []PhysicalSource,
	shapes []ParameterShape,
	parameterValues map[string]any,
	maxRows int,
) (QueryPlan, error) {
	raw, err := json.Marshal(document)
	if err != nil {
		return QueryPlan{}, fmt.Errorf("marshal generated Dataset DSL: %w", err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		return QueryPlan{}, fmt.Errorf("%w: prepare generated Dataset DSL: %v", ErrInvalidQueryPlan, err)
	}
	compiled, err := querycompiler.Compile(querycompiler.Input{
		Document: prepared.Document, Dialect: querycompiler.PostgreSQL,
		Tables:     tableRefsFor(source, joinedSources),
		Parameters: cloneParameterValues(parameterValues),
		Scope:      policy.UserScope{}, MaxRows: maxRows, LimitKind: querycompiler.LimitResult,
	})
	if err != nil {
		return QueryPlan{}, fmt.Errorf("%w: compile generated Dataset DSL: %v", ErrInvalidQueryPlan, err)
	}
	plan := QueryPlan{
		Role: role, Document: prepared.Document, Source: source,
		JoinedSources:   append([]PhysicalSource(nil), joinedSources...),
		ParameterShapes: append([]ParameterShape(nil), shapes...),
		DSLHash:         askdata.ContentHash(prepared.DSLHash), LogicalPlanHash: askdata.ContentHash(prepared.PlanHash),
		CompiledPlanHash: askdata.ContentHash(compiled.PlanHash), compiled: &compiled,
		parameterValues: cloneParameterValues(parameterValues),
	}
	plan.PlanHash, err = queryPlanHash(plan)
	if err != nil {
		return QueryPlan{}, err
	}
	return plan, nil
}

func (artifact QueryArtifact) Validate() error {
	if artifact.Version != AdapterVersion || artifact.Scope.Validate() != nil ||
		artifact.DomainID.Validate() != nil || !containsID(artifact.Scope.DomainIDs, artifact.DomainID) ||
		artifact.IRHash.Validate() != nil || artifact.BuildArtifactHash.Validate() != nil ||
		artifact.ResolutionHash.Validate() != nil || artifact.GraphPlanHash.Validate() != nil ||
		artifact.PlanHash.Validate() != nil {
		return ErrInvalidQueryPlan
	}
	if err := validateMetricAggregationContracts(artifact.MetricAggregations); err != nil {
		return err
	}
	if artifact.Comparison == nil {
		if len(artifact.Plans) != 1 || artifact.Plans[0].Role != QueryRoleCurrent {
			return ErrInvalidQueryPlan
		}
	} else {
		if artifact.Comparison.Periods < 1 || artifact.Comparison.Periods > 120 ||
			!validComparisonType(artifact.Comparison.Type) || artifact.Timezone == "" ||
			len(artifact.Plans) != 2 || artifact.Plans[0].Role != QueryRoleCurrent ||
			artifact.Plans[1].Role != QueryRoleBaseline {
			return ErrInvalidQueryPlan
		}
	}
	if artifact.Timezone != "" {
		if _, err := time.LoadLocation(artifact.Timezone); err != nil {
			return ErrInvalidQueryPlan
		}
	}
	if artifact.ResolvedTimeSpec != nil {
		loc, err := time.LoadLocation(artifact.ResolvedTimeSpec.Timezone)
		if err != nil || validateResolvedTimeSpec(*artifact.ResolvedTimeSpec) != nil ||
			artifact.Timezone != artifact.ResolvedTimeSpec.Timezone ||
			(artifact.Comparison == nil) != (artifact.ResolvedTimeSpec.Comparison == nil) ||
			!plansHaveTimeBoundaries(artifact.Plans) {
			return ErrInvalidQueryPlan
		}
		if artifact.Comparison != nil {
			comparison := artifact.ResolvedTimeSpec.Comparison
			if comparison.Type != resolvedComparisonType(artifact.Comparison.Type, artifact.ResolvedTimeSpec.Grain) ||
				comparison.Periods != artifact.Comparison.Periods ||
				!isLocalMidnight(comparison.ResolvedStart, loc) || !isLocalMidnight(comparison.ResolvedEndExclusive, loc) {
				return ErrInvalidQueryPlan
			}
		}
	}
	for index := range artifact.Plans {
		if err := artifact.Plans[index].validate(); err != nil {
			return err
		}
		if index > 0 && !samePlanShape(artifact.Plans[0], artifact.Plans[index]) {
			return ErrInvalidQueryPlan
		}
	}
	expected, err := queryArtifactHash(artifact)
	if err != nil || expected != artifact.PlanHash {
		return ErrInvalidQueryPlan
	}
	return nil
}

func plansHaveTimeBoundaries(plans []QueryPlan) bool {
	for _, plan := range plans {
		start, end := false, false
		for _, shape := range plan.ParameterShapes {
			start = start || shape.Code == "time_start"
			end = end || shape.Code == "time_end_exclusive"
		}
		if !start || !end {
			return false
		}
	}
	return len(plans) > 0
}

func (plan QueryPlan) validate() error {
	if plan.Role != QueryRoleCurrent && plan.Role != QueryRoleBaseline ||
		plan.DSLHash.Validate() != nil || plan.LogicalPlanHash.Validate() != nil ||
		plan.CompiledPlanHash.Validate() != nil || plan.PlanHash.Validate() != nil {
		return ErrInvalidQueryPlan
	}
	if err := plan.Source.validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(plan.Document)
	if err != nil {
		return ErrInvalidQueryPlan
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil || prepared.DSLHash != string(plan.DSLHash) || prepared.PlanHash != string(plan.LogicalPlanHash) {
		return ErrInvalidQueryPlan
	}
	if err := validateParameterShapes(prepared.Document.Parameters, plan.ParameterShapes); err != nil {
		return err
	}
	dummy, err := dummyParameterValues(plan.ParameterShapes)
	if err != nil {
		return err
	}
	compiled, err := querycompiler.Compile(querycompiler.Input{
		Document: prepared.Document, Dialect: querycompiler.PostgreSQL,
		Tables:     tableRefsFor(plan.Source, plan.JoinedSources),
		Parameters: dummy, Scope: policy.UserScope{},
		MaxRows: prepared.Document.ExecutionPolicy.ResultLimit, LimitKind: querycompiler.LimitResult,
	})
	if err != nil || compiled.PlanHash != string(plan.CompiledPlanHash) {
		return ErrInvalidQueryPlan
	}
	if plan.compiled != nil && (plan.compiled.PlanHash != compiled.PlanHash || plan.compiled.MaxRows != compiled.MaxRows) {
		return ErrInvalidQueryPlan
	}
	expected, err := queryPlanHash(plan)
	if err != nil || expected != plan.PlanHash {
		return ErrInvalidQueryPlan
	}
	return nil
}

func (source PhysicalSource) validate() error {
	if source.NodeID != "semantic_model" || source.DatasetVersionID.Validate() != nil ||
		source.MaterializationID.Validate() != nil || source.PublishedSchema != "warehouse_published" ||
		!trustedOutputNamePattern.MatchString(source.PublishedName) || len(source.Columns) == 0 ||
		len(source.Columns) > MaxResolvedFields {
		return ErrInvalidQueryPlan
	}
	previous := ""
	for _, column := range source.Columns {
		if !trustedOutputNamePattern.MatchString(column.Code) || !validCanonicalType(column.CanonicalType) ||
			(previous != "" && column.Code <= previous) {
			return ErrInvalidQueryPlan
		}
		previous = column.Code
	}
	return nil
}

// CompiledQuery returns an isolated copy of the in-process executable plan.
// A JSON-replayed artifact deliberately has no executable values and must be
// rehydrated from the pinned member registry before QUERY-005 can run it.
func (plan QueryPlan) CompiledQuery() (querycompiler.CompiledQuery, bool) {
	if plan.compiled == nil {
		return querycompiler.CompiledQuery{}, false
	}
	result := *plan.compiled
	result.Args = append([]any(nil), plan.compiled.Args...)
	return result, true
}

func (source PhysicalSource) tableRef() querycompiler.TableRef {
	columns := make(map[string]bool, len(source.Columns))
	types := make(map[string]string, len(source.Columns))
	for _, column := range source.Columns {
		columns[column.Code] = true
		types[column.Code] = column.CanonicalType
	}
	return querycompiler.TableRef{
		NodeID: source.NodeID, Schema: source.PublishedSchema, Name: source.PublishedName,
		Columns: columns, ColumnTypes: types,
	}
}

func parameterShape(parameter dataset.Parameter, cardinality int) ParameterShape {
	return ParameterShape{
		Code: parameter.Code, DataType: parameter.DataType, MultiValue: parameter.MultiValue,
		Required: parameter.Required, Cardinality: cardinality,
	}
}

func validateParameterShapes(parameters []dataset.Parameter, shapes []ParameterShape) error {
	if len(parameters) != len(shapes) {
		return ErrInvalidQueryPlan
	}
	for index, parameter := range parameters {
		shape := shapes[index]
		if shape.Code != parameter.Code || shape.DataType != parameter.DataType ||
			shape.MultiValue != parameter.MultiValue || shape.Required != parameter.Required ||
			shape.Cardinality < 1 || (!shape.MultiValue && shape.Cardinality != 1) {
			return ErrInvalidQueryPlan
		}
	}
	return nil
}

func dummyParameterValues(shapes []ParameterShape) (map[string]any, error) {
	result := make(map[string]any, len(shapes))
	for _, shape := range shapes {
		var scalar any
		switch shape.DataType {
		case "STRING":
			scalar = "x"
		case "INTEGER":
			scalar = int64(1)
		case "DECIMAL":
			scalar = float64(1)
		case "BOOLEAN":
			scalar = true
		case "DATE":
			scalar = "2000-01-01"
		case "DATETIME":
			scalar = "2000-01-01 00:00:00"
		default:
			return nil, ErrInvalidQueryPlan
		}
		if shape.MultiValue {
			values := make([]any, shape.Cardinality)
			for index := range values {
				values[index] = scalar
			}
			result[shape.Code] = values
		} else {
			result[shape.Code] = scalar
		}
	}
	return result, nil
}

func baselineParameterValues(
	semanticIR ir.SemanticIR,
	resolution Resolution,
	current map[string]any,
	resolved *ir.ResolvedTimeSpec,
) (map[string]any, error) {
	if semanticIR.TimeRange == nil || semanticIR.Comparison == nil || resolution.TimeDimensionVersionID == nil {
		return nil, fmt.Errorf("%w: comparison time contract", ErrInvalidAdaptRequest)
	}
	dimension, exists := dimensionByID(resolution.Dimensions, *resolution.TimeDimensionVersionID)
	if !exists {
		return nil, fmt.Errorf("%w: comparison time dimension", ErrInvalidAdaptRequest)
	}
	field, exists := fieldByID(resolution.Model.Fields, dimension.LogicalFieldID)
	if !exists {
		return nil, fmt.Errorf("%w: comparison time field", ErrInvalidAdaptRequest)
	}
	shifted := *semanticIR.TimeRange
	if resolved != nil {
		if resolved.Comparison == nil {
			return nil, fmt.Errorf("%w: resolved comparison", ErrInvalidAdaptRequest)
		}
		loc, err := time.LoadLocation(resolved.Timezone)
		if err != nil {
			return nil, fmt.Errorf("%w: resolved comparison timezone", ErrInvalidAdaptRequest)
		}
		shifted.Start = resolved.Comparison.ResolvedStart.In(loc).Format("2006-01-02")
		shifted.EndExclusive = resolved.Comparison.ResolvedEndExclusive.In(loc).Format("2006-01-02")
		shifted.Timezone = resolved.Timezone
	} else {
		var err error
		shifted, err = shiftComparisonRange(*semanticIR.TimeRange, *semanticIR.Comparison)
		if err != nil {
			return nil, err
		}
	}
	start, end, err := timeBoundaryValues(shifted, field.CanonicalType)
	if err != nil {
		return nil, err
	}
	result := cloneParameterValues(current)
	result["time_start"], result["time_end_exclusive"] = start, end
	return result, nil
}

func shiftComparisonRange(value ir.TimeRange, comparison ir.Comparison) (ir.TimeRange, error) {
	start, startErr := time.Parse("2006-01-02", value.Start)
	end, endErr := time.Parse("2006-01-02", value.EndExclusive)
	if startErr != nil || endErr != nil || !end.After(start) || comparison.Periods < 1 {
		return ir.TimeRange{}, fmt.Errorf("%w: comparison range", ErrInvalidAdaptRequest)
	}
	var shiftedStart, shiftedEnd time.Time
	switch comparison.Type {
	case ir.ComparisonYearOverYear:
		shiftedStart = shiftMonthsClamped(start, -12*comparison.Periods)
		shiftedEnd = shiftMonthsClamped(end, -12*comparison.Periods)
	case ir.ComparisonMonthOverMonth:
		shiftedStart = shiftMonthsClamped(start, -comparison.Periods)
		shiftedEnd = shiftMonthsClamped(end, -comparison.Periods)
	case ir.ComparisonPeriodOverPeriod:
		days := int(end.Sub(start).Hours() / 24)
		shiftedStart = start.AddDate(0, 0, -days*comparison.Periods)
		shiftedEnd = end.AddDate(0, 0, -days*comparison.Periods)
	default:
		return ir.TimeRange{}, fmt.Errorf("%w: comparison type", ErrUnsupportedQuery)
	}
	if !shiftedEnd.After(shiftedStart) {
		return ir.TimeRange{}, fmt.Errorf("%w: shifted comparison range", ErrUnsupportedQuery)
	}
	value.Start = shiftedStart.Format("2006-01-02")
	value.EndExclusive = shiftedEnd.Format("2006-01-02")
	return value, nil
}

func shiftMonthsClamped(value time.Time, months int) time.Time {
	monthIndex := int(value.Month()) - 1 + months
	year := value.Year() + monthIndex/12
	monthIndex %= 12
	if monthIndex < 0 {
		monthIndex += 12
		year--
	}
	month := time.Month(monthIndex + 1)
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	day := value.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func timeBoundaryValues(value ir.TimeRange, canonicalType string) (string, string, error) {
	if err := value.Validate(); err != nil {
		return "", "", fmt.Errorf("%w: time range: %v", ErrInvalidAdaptRequest, err)
	}
	switch canonicalType {
	case "DATE":
		return value.Start, value.EndExclusive, nil
	case "DATETIME":
		return value.Start + " 00:00:00", value.EndExclusive + " 00:00:00", nil
	default:
		return "", "", fmt.Errorf("%w: time field type", ErrInvalidAdaptRequest)
	}
}

// anchorNodeID is the Query DSL node for the model the Semantic IR names. It is
// a literal rather than a derived identifier because every plan hash compiled
// before joins existed used this exact string.
const anchorNodeID = "semantic_model"

func fieldReference(field FieldContract) dataset.Expression {
	return fieldReferenceOn(field, anchorNodeID)
}

func fieldReferenceOn(field FieldContract, nodeID string) dataset.Expression {
	if nodeID == "" {
		nodeID = anchorNodeID
	}
	return dataset.Expression{Type: "FIELD_REF", NodeID: nodeID, Field: field.Code}
}

// placeJoinedModels registers every joined model's fields alongside the
// anchor's and records which DSL node each one belongs to.
//
// Field codes must stay globally unique across the joined set: the Query DSL
// addresses columns by code within a node, but the output projection is flat, so
// two models exposing the same code would produce an ambiguous result column.
// Rejecting that is a fail-closed choice — silently preferring one side would
// mean a question answered from a column the asker did not name.
type joinPlacement struct {
	sources       []PhysicalSource
	fields        map[askdata.ID]FieldContract
	nodeByFieldID map[askdata.ID]string
	projections   map[string][]string
}

func placeJoinedModels(resolution Resolution) (joinPlacement, error) {
	placement := joinPlacement{
		fields:        map[askdata.ID]FieldContract{},
		nodeByFieldID: map[askdata.ID]string{},
		projections:   map[string][]string{},
	}
	if len(resolution.JoinedModels) == 0 {
		return placement, nil
	}
	for _, model := range resolution.JoinedModels {
		nodeID := joinedNodeID(model.ModelVersionID)
		columns := make([]PhysicalColumn, 0, len(model.Fields))
		projection := make([]string, 0, len(model.Fields))
		modelCodes := make(map[string]struct{}, len(model.Fields))
		for _, field := range model.Fields {
			if _, duplicate := modelCodes[field.Code]; duplicate {
				return joinPlacement{}, fmt.Errorf(
					"%w: duplicate model field code", ErrInvalidQueryPlan,
				)
			}
			modelCodes[field.Code] = struct{}{}
			placement.fields[field.FieldID] = field
			placement.nodeByFieldID[field.FieldID] = nodeID
			columns = append(columns, PhysicalColumn{Code: field.Code, CanonicalType: field.CanonicalType})
			projection = append(projection, field.Code)
		}
		sort.Slice(columns, func(i, j int) bool { return columns[i].Code < columns[j].Code })
		sort.Strings(projection)
		placement.projections[nodeID] = projection
		placement.sources = append(placement.sources, PhysicalSource{
			NodeID: nodeID, DatasetVersionID: model.Materialization.DatasetVersionID,
			MaterializationID: model.Materialization.MaterializationID,
			PublishedSchema:   model.Materialization.PublishedSchema,
			PublishedName:     model.Materialization.PublishedName, Columns: columns,
		})
	}
	sort.Slice(placement.sources, func(i, j int) bool {
		return placement.sources[i].NodeID < placement.sources[j].NodeID
	})
	return placement, nil
}

// tableRefsFor builds the node -> physical table map the query compiler needs.
func tableRefsFor(source PhysicalSource, joined []PhysicalSource) map[string]querycompiler.TableRef {
	tables := map[string]querycompiler.TableRef{source.NodeID: source.tableRef()}
	for _, value := range joined {
		tables[value.NodeID] = value.tableRef()
	}
	return tables
}

func joinedNodeID(modelVersionID askdata.ID) string {
	return stableDatasetIdentifier("semantic_join", modelVersionID)
}

// buildDatasetJoins turns the resolved relationship path into Query DSL joins.
//
// The Query DSL already compiles joins, including fanout policy and bridge
// contracts, so this maps governed contracts onto that vocabulary rather than
// generating SQL. compiler.CompileJoin generates join SQL at the string layer
// and is deliberately not used here: emitting the typed document keeps one
// compilation path, so row policies, parameter binding and plan hashing all
// continue to apply to a joined query exactly as they do to a single-model one.
func buildDatasetJoins(resolution Resolution) ([]dataset.Join, error) {
	steps := 0
	if resolution.GraphPath != nil {
		steps = len(resolution.GraphPath.Steps)
	}
	if len(resolution.Relationships) == 0 && steps == 0 {
		return []dataset.Join{}, nil
	}
	// The two must agree. A path with no relationship contracts would otherwise
	// compile to a join-free query that silently answers a cross-model question
	// from the anchor table alone — a plausible-looking wrong number.
	if len(resolution.Relationships) == 0 || steps == 0 {
		return nil, fmt.Errorf(
			"%w: join path and relationship contracts disagree", ErrUnsupportedQuery,
		)
	}
	relationshipsByID := make(map[askdata.ID]RelationshipContract, len(resolution.Relationships))
	for _, relationship := range resolution.Relationships {
		relationshipsByID[relationship.RelationshipVersionID] = relationship
	}
	joins := make([]dataset.Join, 0, len(resolution.GraphPath.Steps))
	for index, step := range resolution.GraphPath.Steps {
		relationship, exists := relationshipsByID[step.RelationshipVersionID]
		if !exists {
			return nil, fmt.Errorf("%w: join step has no relationship contract", ErrInvalidQueryPlan)
		}
		var joinAST relationshipJoinAST
		if err := json.Unmarshal(relationship.JoinAST, &joinAST); err != nil {
			return nil, fmt.Errorf("%w: relationship join AST is unreadable", ErrInvalidQueryPlan)
		}
		leftField, leftOK := fieldByID(resolution.Model.Fields, askdata.ID(joinAST.LeftFieldID))
		rightModel, rightOK := joinedModelByVersion(resolution, step.ToModelVersionID)
		if !leftOK || !rightOK {
			return nil, fmt.Errorf("%w: join step references an unresolved model field", ErrInvalidQueryPlan)
		}
		rightField, fieldOK := fieldByID(rightModel.Fields, askdata.ID(joinAST.RightFieldID))
		if !fieldOK {
			return nil, fmt.Errorf("%w: join step references an unresolved model field", ErrInvalidQueryPlan)
		}
		if err := assertNonFanoutJoin(relationship); err != nil {
			return nil, err
		}
		rightNodeID := joinedNodeID(rightModel.ModelVersionID)
		joins = append(joins, dataset.Join{
			ID:          stableDatasetIdentifier("semantic_edge", step.RelationshipVersionID),
			LeftNodeID:  anchorNodeID,
			RightNodeID: rightNodeID,
			// LEFT preserves anchor rows: a fact without a matching dimension row
			// must still be counted, or the join would silently filter the result.
			// The DSL derives cardinality from join type and validates the pair,
			// so MANY_TO_ONE here is the shape assertNonFanoutJoin just proved.
			JoinType:    "LEFT",
			Cardinality: joinCardinalityForLeftJoin,
			// FanoutPolicy is deliberately left unset: the DSL derives fanout from
			// the join type and rejects a document that also declares it. The
			// governed policy is not discarded — assertNonFanoutJoin has already
			// refused every value that would need one.
			Conditions: []dataset.JoinCondition{{
				LeftExpression:  fieldReferenceOn(leftField, anchorNodeID),
				RightExpression: fieldReferenceOn(rightField, rightNodeID),
				Operator:        "EQUALS",
			}},
			ManualConfirmed: true,
		})
		_ = index
	}
	return joins, nil
}

// joinCardinalityForLeftJoin mirrors dataset.joinCardinalityForType("LEFT").
// The DSL validates that the declared cardinality matches the join type, so
// this is a constraint to satisfy rather than a value to choose.
const joinCardinalityForLeftJoin = "MANY_TO_ONE"

// assertNonFanoutJoin refuses any relationship whose governed contract says the
// join can multiply anchor rows.
//
// A fanout-bearing edge needs pre-aggregation of the right side or a bridge
// dedup before it is safe to aggregate across, and compiling it as a plain LEFT
// join would inflate every measure without any visible error. Refusing is the
// only honest option until the pre-aggregation path is built: a wrong number
// presented confidently is the worst outcome this compiler can produce.
func assertNonFanoutJoin(relationship RelationshipContract) error {
	switch relationship.Cardinality {
	case registry.CardinalityOneToOne, registry.CardinalityManyToOne:
	default:
		return fmt.Errorf(
			"%w: %s join needs pre-aggregation before it can be compiled",
			ErrUnsupportedQuery, relationship.Cardinality,
		)
	}
	if relationship.FanoutPolicy != registry.FanoutSafe {
		return fmt.Errorf(
			"%w: fanout policy %s is not compilable as a direct join",
			ErrUnsupportedQuery, relationship.FanoutPolicy,
		)
	}
	return nil
}

func joinedModelByVersion(resolution Resolution, modelVersionID askdata.ID) (ModelContract, bool) {
	for _, model := range resolution.JoinedModels {
		if model.ModelVersionID == modelVersionID {
			return model, true
		}
	}
	return ModelContract{}, false
}

func stableDatasetIdentifier(prefix string, value askdata.ID) string {
	hash := askdata.HashBytes([]byte(prefix + "\x00" + string(value)))
	return prefix + "_" + string(hash[:24])
}

func fieldByID(values []FieldContract, id askdata.ID) (FieldContract, bool) {
	for _, value := range values {
		if value.FieldID == id {
			return value, true
		}
	}
	return FieldContract{}, false
}

func cloneParameterValues(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		reflected := reflect.ValueOf(value)
		if reflected.IsValid() && (reflected.Kind() == reflect.Slice || reflected.Kind() == reflect.Array) {
			items := make([]any, reflected.Len())
			for index := range items {
				items[index] = reflected.Index(index).Interface()
			}
			result[key] = items
			continue
		}
		result[key] = value
	}
	return result
}

func queryPlanHash(plan QueryPlan) (askdata.ContentHash, error) {
	copy := plan
	copy.PlanHash = ""
	copy.compiled = nil
	copy.parameterValues = nil
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return "", fmt.Errorf("hash query plan: %w", err)
	}
	return askdata.HashBytes(payload), nil
}

func queryArtifactHash(artifact QueryArtifact) (askdata.ContentHash, error) {
	copy := artifact
	copy.PlanHash = ""
	copy.Plans = append([]QueryPlan(nil), artifact.Plans...)
	for index := range copy.Plans {
		copy.Plans[index].compiled = nil
		copy.Plans[index].parameterValues = nil
	}
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return "", fmt.Errorf("hash query artifact: %w", err)
	}
	return askdata.HashBytes(payload), nil
}

func samePlanShape(left, right QueryPlan) bool {
	left.Role, right.Role = "", ""
	left.PlanHash, right.PlanHash = "", ""
	left.compiled, right.compiled = nil, nil
	left.parameterValues, right.parameterValues = nil, nil
	return reflect.DeepEqual(left, right)
}

func validComparisonType(value ir.ComparisonType) bool {
	return value == ir.ComparisonYearOverYear || value == ir.ComparisonMonthOverMonth ||
		value == ir.ComparisonPeriodOverPeriod
}
