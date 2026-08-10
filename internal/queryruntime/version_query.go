package queryruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

const MaxVersionQueryPredicates = 128

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
	if s == nil || tenantID == "" || actorID == "" || datasetID == "" || versionID == "" ||
		input.MaxRows < 1 || input.MaxRows > 10_000 || len(input.Fields) == 0 || len(input.Fields) > 64 ||
		len(input.Predicates) > MaxVersionQueryPredicates {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	version, err := s.datasets.GetVersion(ctx, tenantID, datasetID, versionID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	if version.Status != "PUBLISHED" {
		return dataset.PreviewResult{}, dataset.ErrVersionUnavailable
	}
	document, err := dataset.DecodeAndNormalize(version.DSL)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrInvalidDocument
	}
	if input.MaxRows > document.ExecutionPolicy.ResultLimit {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	fields := make(map[string]dataset.Field, len(document.Fields))
	for _, field := range document.Fields {
		fields[field.Code] = field
	}
	seenFields := map[string]struct{}{}
	for _, code := range input.Fields {
		field, exists := fields[code]
		if !exists || field.Visible != nil && !*field.Visible {
			return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
		}
		if _, duplicate := seenFields[code]; duplicate {
			return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
		}
		seenFields[code] = struct{}{}
	}
	for index, predicate := range input.Predicates {
		field, exists := fields[predicate.FieldCode]
		if !exists || field.Visible != nil && !*field.Visible || !validVersionPredicate(predicate) {
			return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
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
	if document.ExecutionPolicy.PreviewLimit < input.MaxRows {
		document.ExecutionPolicy.PreviewLimit = input.MaxRows
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrInvalidDocument
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrInvalidDocument
	}
	return s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		DatasetID: datasetID, VersionID: version.ID, PlanHash: prepared.PlanHash,
		DSL: prepared.DSLJSON, ExactVersion: true,
	}, dataset.PreviewInput{
		QueryID: input.QueryID, Parameters: input.Parameters, MaxRows: input.MaxRows,
	}, "REPORT_RUNTIME")
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
