package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
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
	preview, err := runner.service.PreviewVersionQuery(
		ctx, string(identity.TenantID), string(identity.ActorID), string(request.DatasetID),
		string(request.DatasetVersionID), queryruntime.VersionQueryInput{
			Fields: fields, Predicates: predicates, Parameters: parameters, MaxRows: request.Limit,
		},
	)
	if err != nil {
		return QueryResult{}, err
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
	hashPayload, err := json.Marshal(struct {
		Columns []string `json:"columns"`
		Rows    [][]any  `json:"rows"`
	}{fields, rows})
	if err != nil {
		return QueryResult{}, err
	}
	return QueryResult{
		Columns: fields, Rows: rows, Hash: askdata.HashBytes(hashPayload),
		Partial: len(rows) == request.Limit,
	}, nil
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
