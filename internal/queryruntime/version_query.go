package queryruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

const (
	MaxVersionQueryPredicates = 128
	// Dataset DSL intentionally caps an interactive preview at 5,000 rows. A
	// report may carry an older 10,000-row query policy, so the immutable-version
	// adapter must clamp it before re-normalizing the DSL instead of turning a
	// valid published report into an execution error.
	MaxVersionQueryRows = 5_000
)

// VersionPredicate is a closed, SQL-free predicate derived by a trusted
// caller from a typed UI/runtime filter. FieldCode always names a logical
// output field in the immutable dataset version.
type VersionPredicate struct {
	FieldCode string
	Operator  string
	Value     any
}

type VersionQueryInput struct {
	QueryID    string
	Fields     []string
	Predicates []VersionPredicate
	Parameters map[string]any
	MaxRows    int
	// Distinct asks the runtime to group by the sole requested field. It is used
	// for bounded filter option discovery and never changes the stored version.
	Distinct bool
	// Rollup asks the runtime to return rows grouped to Dimensions with Measures
	// aggregated by their governed function, when the version's grain is finer
	// than Dimensions and its DSL does not already aggregate. The database then
	// does the roll-up instead of the caller reading every source row. When it
	// cannot be pushed down the query runs at the version's grain and
	// VersionRollupContract.Applied stays false.
	Rollup *VersionRollupRequest
}

// VersionRollupRequest splits the requested fields into the dimensions to
// group by and the measures to aggregate. Fields must list the same codes.
type VersionRollupRequest struct {
	Dimensions []string
	Measures   []string
}

// PreviewVersionQuery executes a logical query derived from one exact
// PUBLISHED dataset version. It may add bounded predicates and raise the
// preview row ceiling up to the immutable resultLimit, but it cannot change
// sources, joins, expressions, policies, or the version identity.
func (s *Service) PreviewVersionQuery(
	ctx context.Context,
	tenantID, actorID, datasetID, versionID string,
	input VersionQueryInput,
) (dataset.PreviewResult, error) {
	result, _, err := s.PreviewVersionQueryWithRollup(ctx, tenantID, actorID, datasetID, versionID, input)
	return result, err
}

// PreviewVersionQueryWithRollup additionally returns the version's grain and
// measure semantics. Callers that project a subset of the version's fields need
// both from one read: fetching the contract separately would decode the same
// immutable DSL twice on every component execution.
func (s *Service) PreviewVersionQueryWithRollup(
	ctx context.Context,
	tenantID, actorID, datasetID, versionID string,
	input VersionQueryInput,
) (dataset.PreviewResult, VersionRollupContract, error) {
	if s == nil || tenantID == "" || actorID == "" || datasetID == "" || versionID == "" ||
		input.MaxRows < 1 || input.MaxRows > 10_000 || len(input.Fields) == 0 || len(input.Fields) > 64 ||
		len(input.Predicates) > MaxVersionQueryPredicates || input.Distinct && (len(input.Fields) != 1 || input.Rollup != nil) {
		return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("version query input contract: %w", dataset.ErrPreviewInvalid)
	}
	version, err := s.datasets.GetVersion(ctx, tenantID, datasetID, versionID)
	if err != nil {
		return dataset.PreviewResult{}, VersionRollupContract{}, err
	}
	if version.Status != "PUBLISHED" {
		return dataset.PreviewResult{}, VersionRollupContract{}, dataset.ErrVersionUnavailable
	}
	document, err := dataset.DecodeAndNormalize(version.DSL)
	if err != nil {
		return dataset.PreviewResult{}, VersionRollupContract{}, dataset.ErrInvalidDocument
	}
	if input.MaxRows > document.ExecutionPolicy.ResultLimit {
		return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("version query row limit: %w", dataset.ErrPreviewInvalid)
	}
	effectiveMaxRows := min(input.MaxRows, MaxVersionQueryRows)
	fields := make(map[string]dataset.Field, len(document.Fields))
	for _, field := range document.Fields {
		fields[field.Code] = field
	}
	seenFields := map[string]struct{}{}
	for _, code := range input.Fields {
		field, exists := fields[code]
		if !exists || field.Visible != nil && !*field.Visible {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("version query field unavailable: %w", dataset.ErrPreviewInvalid)
		}
		if _, duplicate := seenFields[code]; duplicate {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("version query field duplicated: %w", dataset.ErrPreviewInvalid)
		}
		seenFields[code] = struct{}{}
	}
	for index, predicate := range input.Predicates {
		field, exists := fields[predicate.FieldCode]
		if !exists || field.Visible != nil && !*field.Visible || !validVersionPredicate(predicate) {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("version query predicate invalid: %w", dataset.ErrPreviewInvalid)
		}
		filter := dataset.Filter{
			ID: fmt.Sprintf("report_runtime_%03d", index+1), Optional: false,
			Expression: dataset.Expression{
				Type: strings.ToUpper(predicate.Operator), Left: cloneExpression(&field.Expression),
				Right: &dataset.Expression{Type: "LITERAL", Value: predicate.Value},
			},
		}
		if expressionContainsAggregate(field.Expression) {
			filter.Stage = "POST_AGGREGATION"
			document.Having = append(document.Having, filter)
		} else {
			filter.Stage = "PRE_AGGREGATION"
			document.Filters = append(document.Filters, filter)
		}
	}
	if document.ExecutionPolicy.PreviewLimit < effectiveMaxRows {
		document.ExecutionPolicy.PreviewLimit = effectiveMaxRows
	}
	contract := rollupContractOf(document)
	snapshot := runtimeSnapshot{DatasetID: datasetID, VersionID: version.ID, ExactVersion: true}
	if input.Distinct {
		grouped, groupErr := dataset.BuildRuntimeDistinct(document, input.Fields[0])
		if groupErr != nil {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("build version distinct field query: %w", dataset.ErrPreviewUnsupported)
		}
		prepared, prepareErr := dataset.PrepareDocument(grouped)
		if prepareErr != nil {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("normalize version distinct field query: %w: %v", dataset.ErrInvalidDocument, prepareErr)
		}
		snapshot.PlanHash, snapshot.DSL, snapshot.Document = prepared.PlanHash, prepared.DSLJSON, &prepared.Document
	} else if grouped, ok := runtimeRollupDocument(document, contract, input.Rollup); ok {
		// The roll-up is pushed into the query: the private execution document
		// is validated and planned in memory so its runtime marker survives.
		prepared, err := dataset.PrepareDocument(grouped)
		if err != nil {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("normalize version roll-up document: %w: %v", dataset.ErrInvalidDocument, err)
		}
		contract.Applied = true
		snapshot.PlanHash, snapshot.DSL, snapshot.Document = prepared.PlanHash, prepared.DSLJSON, &prepared.Document
	} else {
		raw, err := json.Marshal(document)
		if err != nil {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("encode version query document: %w", dataset.ErrInvalidDocument)
		}
		prepared, err := dataset.Prepare(raw)
		if err != nil {
			return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("normalize version query document: %w", dataset.ErrInvalidDocument)
		}
		snapshot.PlanHash, snapshot.DSL = prepared.PlanHash, prepared.DSLJSON
	}
	result, err := s.previewSnapshot(ctx, tenantID, actorID, snapshot, dataset.PreviewInput{
		QueryID: input.QueryID, Parameters: input.Parameters, MaxRows: effectiveMaxRows,
	}, "ONLINE")
	if err != nil {
		return dataset.PreviewResult{}, VersionRollupContract{}, fmt.Errorf("execute version query snapshot: %w", err)
	}
	return result, contract, nil
}

// runtimeRollupDocument decides whether a requested roll-up can be pushed into
// the query and, if so, builds the grouped execution document. It declines —
// leaving the caller on the read-at-grain path — when the request already
// covers the version's grain, when a measure has no governed aggregation or is
// semi-additive, or when the DSL already changes its source grain.
func runtimeRollupDocument(document dataset.Document, contract VersionRollupContract, request *VersionRollupRequest) (dataset.Document, bool) {
	if request == nil || len(request.Measures) == 0 || len(contract.GrainKeyFields) == 0 {
		return dataset.Document{}, false
	}
	bound := make(map[string]bool, len(request.Dimensions))
	for _, dimension := range request.Dimensions {
		bound[strings.TrimSpace(dimension)] = true
	}
	covered := true
	for _, key := range contract.GrainKeyFields {
		if !bound[strings.TrimSpace(key)] {
			covered = false
			break
		}
	}
	if covered {
		return dataset.Document{}, false
	}
	measures := make([]dataset.RuntimeRollupMeasure, 0, len(request.Measures))
	for _, code := range request.Measures {
		measure := contract.Measures[strings.TrimSpace(code)]
		if !measure.Declared && measure.Aggregation == "" {
			return dataset.Document{}, false
		}
		function := strings.ToUpper(measure.Aggregation)
		switch strings.ToUpper(measure.Additivity) {
		case "", "ADDITIVE", "FULLY_ADDITIVE":
		case "NON_ADDITIVE":
			// A non-additive measure is defined by its own function over the
			// rows (AVG, COUNT_DISTINCT); summing it would change its meaning.
			if function == "SUM" || function == "COUNT" {
				return dataset.Document{}, false
			}
		default:
			return dataset.Document{}, false
		}
		measures = append(measures, dataset.RuntimeRollupMeasure{Field: code, Function: function})
	}
	grouped, err := dataset.BuildRuntimeRollup(document, request.Dimensions, measures)
	if err != nil {
		return dataset.Document{}, false
	}
	return grouped, true
}

// VersionMeasure describes how one logical measure of a dataset version may be
// re-aggregated. Additivity and Aggregation come from the version's fact or
// analysis contract when it declares one, otherwise from the field itself.
type VersionMeasure struct {
	Field       string
	Aggregation string
	Additivity  string
	// Unit is the measure's declared unit. Evidence facts must carry it: a
	// number without its unit cannot be checked or safely quoted in prose.
	Unit string
	// Declared reports whether a governed contract named this measure. An
	// undeclared measure is never rolled up: guessing its aggregation would
	// silently change what the number means.
	Declared bool
}

// VersionRollupContract exposes the logical grain and measure semantics of one
// PUBLISHED dataset version. Callers that project a subset of the version's
// fields need it to decide whether the projection still matches the version's
// grain, and if not, whether rolling up is safe.
// VersionField is one logical output field's governed identity. Narrative
// verification needs it: an object name in prose is only checkable against a
// catalog of real field identities.
type VersionField struct {
	ID      string
	Code    string
	Name    string
	Measure bool
}

type VersionRollupContract struct {
	GrainKeyFields []string
	Measures       map[string]VersionMeasure
	// Fields is every visible output field, keyed by code.
	Fields map[string]VersionField
	// Applied reports that the query returned rows already grouped to the
	// requested VersionRollupRequest; the caller must not roll them up again.
	Applied bool
}

// VersionRollupContract reads one PUBLISHED version's grain and measure
// semantics without executing a query. Evidence derivation needs the declared
// units; the execution path gets the same contract from
// PreviewVersionQueryWithRollup so it never pays for a second read.
func (s *Service) VersionRollupContract(
	ctx context.Context,
	tenantID, datasetID, versionID string,
) (VersionRollupContract, error) {
	if s == nil || tenantID == "" || datasetID == "" || versionID == "" {
		return VersionRollupContract{}, fmt.Errorf("version contract input: %w", dataset.ErrPreviewInvalid)
	}
	version, err := s.datasets.GetVersion(ctx, tenantID, datasetID, versionID)
	if err != nil {
		return VersionRollupContract{}, err
	}
	if version.Status != "PUBLISHED" {
		return VersionRollupContract{}, dataset.ErrVersionUnavailable
	}
	document, err := dataset.DecodeAndNormalize(version.DSL)
	if err != nil {
		return VersionRollupContract{}, dataset.ErrInvalidDocument
	}
	return rollupContractOf(document), nil
}

func rollupContractOf(document dataset.Document) VersionRollupContract {
	contract := VersionRollupContract{
		GrainKeyFields: append([]string(nil), document.OutputGrain.KeyFields...),
		Measures:       map[string]VersionMeasure{},
		Fields:         map[string]VersionField{},
	}
	for _, field := range document.Fields {
		if field.Visible != nil && !*field.Visible {
			continue
		}
		measure := field.Aggregation != "" || expressionContainsAggregate(field.Expression)
		name := strings.TrimSpace(field.Name)
		if name == "" {
			name = field.Code
		}
		contract.Fields[field.Code] = VersionField{
			ID: field.ID, Code: field.Code, Name: name, Measure: measure,
		}
		if !measure {
			continue
		}
		contract.Measures[field.Code] = VersionMeasure{
			Field: field.Code, Aggregation: strings.ToUpper(field.Aggregation), Unit: field.Unit,
		}
	}
	if document.FactContract != nil {
		for _, measure := range document.FactContract.AtomicMeasures {
			aggregation := measure.DefaultAggregation
			if aggregation == "" {
				aggregation = contract.Measures[measure.Field].Aggregation
			}
			contract.Measures[measure.Field] = VersionMeasure{
				Field: measure.Field, Aggregation: strings.ToUpper(aggregation),
				Additivity: strings.ToUpper(measure.Additivity), Unit: measure.Unit, Declared: true,
			}
		}
	}
	if document.AnalysisContract != nil {
		for _, measure := range document.AnalysisContract.Measures {
			contract.Measures[measure.Field] = VersionMeasure{
				Field: measure.Field, Aggregation: strings.ToUpper(measure.Aggregation),
				Additivity: strings.ToUpper(measure.Additivity), Unit: measure.Unit, Declared: true,
			}
		}
	}
	return contract
}

func validVersionPredicate(predicate VersionPredicate) bool {
	if strings.TrimSpace(predicate.FieldCode) == "" || predicate.Value == nil {
		return false
	}
	switch strings.ToUpper(predicate.Operator) {
	case "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE":
		return isVersionScalar(predicate.Value)
	case "IN", "NOT_IN":
		return isNonEmptyCollection(predicate.Value)
	default:
		return false
	}
}

func isVersionScalar(value any) bool {
	switch value := value.(type) {
	case string, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	case float32:
		return !float32IsInvalid(value)
	case float64:
		return !math.IsNaN(value) && !math.IsInf(value, 0)
	case json.Number:
		number, err := value.Float64()
		return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	default:
		return false
	}
}

func float32IsInvalid(value float32) bool {
	number := float64(value)
	return math.IsNaN(number) || math.IsInf(number, 0)
}

func isCollection(value any) bool {
	switch value.(type) {
	case []string, []any, []int, []int64, []float64:
		return true
	default:
		return false
	}
}

func isNonEmptyCollection(value any) bool {
	switch typed := value.(type) {
	case []string:
		return len(typed) > 0 && len(typed) <= 1_000
	case []any:
		if len(typed) == 0 || len(typed) > 1_000 {
			return false
		}
		for _, item := range typed {
			if !isVersionScalar(item) {
				return false
			}
		}
		return true
	case []int:
		return len(typed) > 0 && len(typed) <= 1_000
	case []int64:
		return len(typed) > 0 && len(typed) <= 1_000
	case []float64:
		if len(typed) == 0 || len(typed) > 1_000 {
			return false
		}
		for _, item := range typed {
			if !isVersionScalar(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func cloneExpression(value *dataset.Expression) *dataset.Expression {
	if value == nil {
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result dataset.Expression
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return &result
}

func expressionContainsAggregate(expression dataset.Expression) bool {
	if expression.Type == "AGGREGATE" {
		return true
	}
	children := []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else}
	for _, child := range children {
		if child != nil && expressionContainsAggregate(*child) {
			return true
		}
	}
	for _, child := range expression.Arguments {
		if expressionContainsAggregate(child) {
			return true
		}
	}
	for _, branch := range expression.Whens {
		if expressionContainsAggregate(branch.When) || expressionContainsAggregate(branch.Then) {
			return true
		}
	}
	return false
}
