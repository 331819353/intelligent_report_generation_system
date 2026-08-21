package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/queryruntime"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

type viewerIdentityKey struct{}

func WithViewerIdentity(ctx context.Context, identity store.Identity) context.Context {
	return context.WithValue(ctx, viewerIdentityKey{}, identity)
}

// DatasetVersionRunner executes immutable Dataset DSL through the existing
// governed query runtime and projects only the fields bound by the component.
type DatasetVersionRunner struct{ service *queryruntime.Service }

func NewDatasetVersionRunner(service *queryruntime.Service) (*DatasetVersionRunner, error) {
	if service == nil {
		return nil, errors.New("dataset report runtime is unavailable")
	}
	return &DatasetVersionRunner{service: service}, nil
}

func (runner *DatasetVersionRunner) ExecuteDatasetFields(ctx context.Context, request DatasetExecutionRequest) (QueryResult, error) {
	identity, ok := ctx.Value(viewerIdentityKey{}).(store.Identity)
	if runner == nil || runner.service == nil || !ok || identity.Validate() != nil ||
		request.DatasetID.Validate() != nil || request.DatasetVersionID.Validate() != nil ||
		request.DataContextID.Validate() != nil || request.Limit < 1 {
		return QueryResult{}, errors.New("dataset report runtime request is invalid")
	}
	fields, err := requestedDatasetFields(request.Dimensions, request.Measures)
	if err != nil {
		return QueryResult{}, err
	}
	predicates, err := datasetPredicates(request.DataContextID, request.Filters)
	if err != nil {
		return QueryResult{}, err
	}
	parameters, err := datasetParameters(request.Parameters)
	if err != nil {
		return QueryResult{}, err
	}
	effectiveLimit := min(request.Limit, queryruntime.MaxVersionQueryRows)
	// Ask the runtime to group in the database when the binding drops part of
	// the version's grain: reading every detail row into memory and rolling it
	// up here hits the row ceiling on any real fact table. If the grouped plan
	// cannot execute on this source, fall back to reading at the version's grain
	// and rolling up in memory (which still fails closed on truncation).
	rollup := &queryruntime.VersionRollupRequest{
		Dimensions: bindingFields(request.Dimensions), Measures: bindingFields(request.Measures),
		Aggregations: bindingAggregations(request.Measures),
	}
	preview, contract, err := runner.service.PreviewVersionQueryWithRollup(
		ctx, string(identity.TenantID), string(identity.ActorID), string(request.DatasetID),
		string(request.DatasetVersionID), queryruntime.VersionQueryInput{
			Fields: fields, Predicates: predicates, Parameters: parameters, MaxRows: effectiveLimit, Rollup: rollup,
		},
	)
	if err != nil && (errors.Is(err, dataset.ErrPreviewInvalid) || errors.Is(err, dataset.ErrPreviewUnsupported) || errors.Is(err, dataset.ErrInvalidDocument)) {
		slog.Warn("report dataset roll-up push-down declined, reading at version grain",
			"dataset_version_id", request.DatasetVersionID, "error", err)
		preview, contract, err = runner.service.PreviewVersionQueryWithRollup(
			ctx, string(identity.TenantID), string(identity.ActorID), string(request.DatasetID),
			string(request.DatasetVersionID), queryruntime.VersionQueryInput{
				Fields: fields, Predicates: predicates, Parameters: parameters, MaxRows: effectiveLimit,
			},
		)
	}
	if err != nil {
		slog.Warn("report dataset query failed", "dataset_id", request.DatasetID, "dataset_version_id", request.DatasetVersionID, "error", err)
		return QueryResult{}, err
	}
	for _, binding := range request.Measures {
		formula := strings.ToUpper(strings.TrimSpace(binding.Aggregation))
		governed := strings.ToUpper(strings.TrimSpace(contract.Measures[binding.Field].Aggregation))
		if formula != "" && formula != governed && NeedsRollup(request.Dimensions, contract.GrainKeyFields) && !contract.Applied {
			return QueryResult{}, fmt.Errorf("measure %q formula %q requires a detail-grain dataset version", binding.Field, formula)
		}
	}
	indexes := make(map[string]int, len(preview.Columns))
	for index, column := range preview.Columns {
		indexes[column] = index
	}
	rows := make([][]any, len(preview.Rows))
	for rowIndex, source := range preview.Rows {
		row := make([]any, len(fields))
		for fieldIndex, field := range fields {
			columnIndex, exists := indexes[field]
			if !exists || columnIndex >= len(source) {
				return QueryResult{}, errors.New("dataset report result is missing a bound field")
			}
			row[fieldIndex] = source[columnIndex]
		}
		rows[rowIndex] = row
	}
	hash, err := hashResult(fields, rows)
	if err != nil {
		return QueryResult{}, err
	}
	projected := QueryResult{
		Columns: fields, Rows: rows, Hash: hash,
		Partial: len(rows) == effectiveLimit,
	}

	// The projection above drops any dataset field the component did not bind.
	// If those dropped fields were part of the version's output grain, the rows
	// are finer-grained than the component asks for and must be rolled up —
	// otherwise a chart bound to (channel, revenue) plots one mark per source
	// row instead of one per channel.
	if contract.Applied || !NeedsRollup(request.Dimensions, contract.GrainKeyFields) {
		return projected, nil
	}
	rolled, err := RollUp(projected, request.Dimensions, request.Measures, contract)
	if err != nil {
		slog.Warn("report dataset roll-up refused",
			"dataset_version_id", request.DatasetVersionID, "error", err)
		return QueryResult{}, err
	}
	return rolled, nil
}

func bindingFields(bindings []report.FieldBinding) []string {
	result := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		result = append(result, strings.TrimSpace(binding.Field))
	}
	return result
}

func bindingAggregations(bindings []report.FieldBinding) map[string]string {
	result := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		field := strings.TrimSpace(binding.Field)
		aggregation := strings.ToUpper(strings.TrimSpace(binding.Aggregation))
		if field != "" && aggregation != "" {
			result[field] = aggregation
		}
	}
	return result
}

func requestedDatasetFields(dimensions, measures []report.FieldBinding) ([]string, error) {
	if len(measures) == 0 || len(dimensions)+len(measures) > 64 {
		return nil, errors.New("dataset report binding is invalid")
	}
	fields := make([]string, 0, len(dimensions)+len(measures))
	for _, binding := range append(append([]report.FieldBinding(nil), dimensions...), measures...) {
		field := strings.TrimSpace(binding.Field)
		if field == "" || slices.Contains(fields, field) {
			return nil, errors.New("dataset report binding contains an invalid field")
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func datasetPredicates(dataContextID askdata.ID, filters []ResolvedFilter) ([]queryruntime.VersionPredicate, error) {
	result := []queryruntime.VersionPredicate{}
	for _, filter := range filters {
		if filter.DataContextID != dataContextID {
			continue
		}
		field := strings.TrimSpace(filter.Field)
		if field == "" || filter.Value == nil {
			return nil, errors.New("dataset report filter is invalid")
		}
		appendPredicate := func(operator string, value any) {
			result = append(result, queryruntime.VersionPredicate{FieldCode: field, Operator: operator, Value: value})
		}
		if operator := strings.ToUpper(strings.TrimSpace(filter.Operator)); operator != "" {
			if operator != "EQUALS" && operator != "NOT_EQUALS" {
				return nil, fmt.Errorf("dataset report filter %q has an unsupported operator", filter.ID)
			}
			appendPredicate(operator, filter.Value)
			continue
		}
		switch value := filter.Value.(type) {
		case string, bool, json.Number, float64, float32, int, int64:
			appendPredicate("EQUALS", value)
		case []string:
			appendPredicate("IN", value)
		case DateRangeValue:
			appendPredicate("GTE", value.Start)
			appendPredicate("LT", value.EndExclusive)
		case RelativeTimeWindow:
			appendPredicate("GTE", value.Start.Format("2006-01-02T15:04:05Z07:00"))
			appendPredicate("LT", value.EndExclusive.Format("2006-01-02T15:04:05Z07:00"))
		case NumberRangeValue:
			if value.Minimum != nil {
				appendPredicate("GTE", *value.Minimum)
			}
			if value.Maximum != nil {
				appendPredicate("LTE", *value.Maximum)
			}
		case map[string]any:
			if err := appendMapPredicates(value, appendPredicate); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("dataset report filter %q has an unsupported value", filter.ID)
		}
	}
	if len(result) > queryruntime.MaxVersionQueryPredicates {
		return nil, errors.New("dataset report has too many filter predicates")
	}
	return result, nil
}

func appendMapPredicates(value map[string]any, appendPredicate func(string, any)) error {
	if len(value) == 2 {
		start, startOK := value["start"].(string)
		end, endOK := value["endExclusive"].(string)
		if startOK && endOK && start != "" && end != "" {
			appendPredicate("GTE", start)
			appendPredicate("LT", end)
			return nil
		}
	}
	for key := range value {
		if key != "minimum" && key != "maximum" {
			return errors.New("dataset report filter object is invalid")
		}
	}
	minimum, hasMinimum := value["minimum"]
	maximum, hasMaximum := value["maximum"]
	if !hasMinimum && !hasMaximum {
		return errors.New("dataset report range filter is empty")
	}
	if hasMinimum && minimum != nil && !finiteNumber(minimum) {
		return errors.New("dataset report range minimum is invalid")
	}
	if hasMaximum && maximum != nil && !finiteNumber(maximum) {
		return errors.New("dataset report range maximum is invalid")
	}
	if hasMinimum && minimum != nil {
		appendPredicate("GTE", minimum)
	}
	if hasMaximum && maximum != nil {
		appendPredicate("LTE", maximum)
	}
	return nil
}

func finiteNumber(value any) bool {
	switch value := value.(type) {
	case json.Number:
		_, err := value.Float64()
		return err == nil
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func datasetParameters(definitions []report.DefaultParameter) (map[string]any, error) {
	result := make(map[string]any, len(definitions))
	for _, parameter := range definitions {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return nil, errors.New("dataset report parameter name is invalid")
		}
		switch parameter.Type {
		case report.ParameterString:
			if parameter.StringValue != nil {
				result[name] = *parameter.StringValue
			}
		case report.ParameterNumber:
			if parameter.NumberValue != nil {
				result[name] = *parameter.NumberValue
			}
		case report.ParameterBoolean:
			if parameter.BooleanValue != nil {
				result[name] = *parameter.BooleanValue
			}
		default:
			return nil, errors.New("dataset report parameter type is invalid")
		}
	}
	return result, nil
}

var _ DatasetFieldRunner = (*DatasetVersionRunner)(nil)
