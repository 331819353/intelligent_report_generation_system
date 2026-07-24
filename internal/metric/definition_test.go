package metric

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const (
	testDatasetID        = "11111111-1111-4111-8111-111111111111"
	testDatasetVersionID = "22222222-2222-4222-8222-222222222222"
	testMetricID         = "33333333-3333-4333-8333-333333333333"
	testMetricVersionID  = "44444444-4444-4444-8444-444444444444"
	testDraftVersionID   = "55555555-5555-4555-8555-555555555555"
)

func validDefinition() Definition {
	return Definition{
		SchemaVersion: DefinitionVersion,
		Metric:        Descriptor{Code: "revenue", Name: "营业收入", Description: "有效订单收入", Type: "ATOMIC"},
		DatasetID:     testDatasetID, DatasetVersionID: testDatasetVersionID,
		Expression: Expression{Type: "FIELD_REF", FieldID: "field_amount"}, Aggregation: "SUM",
		Unit: "元", NumberFormat: "0,0.00", TimeGrain: "NONE", Additivity: "ADDITIVE",
		NonAdditiveDimensionFieldIDs: []string{},
		AllowedDimensions:            []Dimension{{FieldID: "field_region", Name: "地区", HierarchyFieldIDs: []string{}, SortDirection: "ASC", NullLabel: "未知"}},
		DecimalScale:                 2, RoundingMode: "HALF_UP", NullHandling: "IGNORE", DivisionByZero: "NULL",
	}
}

func definitionJSON(t *testing.T, definition Definition) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPrepareNormalizesAndHashesDefinition(t *testing.T) {
	definition := validDefinition()
	definition.Metric.Code = " revenue "
	definition.Metric.Type = " atomic "
	definition.Aggregation = " sum "
	definition.Expression = Expression{
		Type: " add ", Arguments: []Expression{
			{Type: "field_ref", FieldID: " field_amount "},
			{Type: "literal", Value: json.Number("1.20")},
		},
	}
	first, err := Prepare(definitionJSON(t, definition))
	if err != nil {
		t.Fatalf("prepare valid definition: %v", err)
	}
	if first.Definition.Metric.Code != "revenue" || first.Definition.Expression.Arguments[1].Value != "1.20" {
		t.Fatalf("definition was not normalized: %#v", first.Definition)
	}
	second, err := Prepare(first.DefinitionJSON)
	if err != nil {
		t.Fatal(err)
	}
	if first.DefinitionHash != second.DefinitionHash || string(first.DefinitionJSON) != string(second.DefinitionJSON) {
		t.Fatalf("canonical definition is not deterministic: %s != %s", first.DefinitionHash, second.DefinitionHash)
	}
}

func TestCandidateMaterializationAllowsOnlyBusinessCopyChanges(t *testing.T) {
	candidate := validDefinition()
	accepted := candidate
	accepted.Metric.Name = "付款订单数量"
	accepted.Metric.Description = "统计每位客户付款订单的数量。"
	if !sameCandidateCalculation(candidate, accepted) {
		t.Fatal("name and description enrichment should preserve the candidate calculation")
	}
	accepted.Unit = "笔"
	if sameCandidateCalculation(candidate, accepted) {
		t.Fatal("unit changes must not bypass deterministic candidate validation")
	}
}

func TestPrepareRejectsAmbiguousJSON(t *testing.T) {
	base := string(definitionJSON(t, validDefinition()))
	cases := map[string]string{
		"重复键":  strings.Replace(base, `"schemaVersion":"1.0"`, `"schemaVersion":"1.0","schemaVersion":"1.0"`, 1),
		"未知字段": strings.TrimSuffix(base, "}") + `,"sql":"select * from secret"}`,
		"尾随文档": base + `{}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Prepare([]byte(raw))
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("expected validation error, got %v", err)
			}
		})
	}
}

func TestPrepareValidatesMetricSemantics(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Definition)
		code   string
	}{
		"字段必须聚合":    {func(value *Definition) { value.Aggregation = "NONE" }, "METRIC_FIELD_AGGREGATION_REQUIRED"},
		"平均值不可完全可加": {func(value *Definition) { value.Aggregation = "AVG" }, "METRIC_ADDITIVITY_CONFLICT"},
		"半可加必须声明维度": {func(value *Definition) { value.Additivity = "SEMI_ADDITIVE" }, "METRIC_SEMI_ADDITIVE_DIMENSION_REQUIRED"},
		"时间字段必须成对":  {func(value *Definition) { value.TimeGrain = "MONTH" }, "METRIC_TIME_FIELD_MISMATCH"},
		"原子指标不能引用指标": {func(value *Definition) {
			value.Expression = Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
			value.Aggregation = "NONE"
		}, "METRIC_ATOMIC_REFERENCE_FORBIDDEN"},
		"比率必须包含除法": {func(value *Definition) {
			value.Metric.Type = "RATIO"
			value.Expression = Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
			value.Aggregation = "NONE"
			value.Additivity = "NON_ADDITIVE"
		}, "METRIC_RATIO_DIVISION_REQUIRED"},
		"常量必须精确": {func(value *Definition) {
			value.Expression = Expression{Type: "ADD", Arguments: []Expression{{Type: "FIELD_REF", FieldID: "field_amount"}, {Type: "LITERAL", Value: "1e10"}}}
		}, "METRIC_LITERAL_INVALID"},
		"常量不能作为完整指标": {func(value *Definition) {
			value.Expression = Expression{Type: "LITERAL", Value: "1"}
			value.Aggregation = "NONE"
		}, "METRIC_VALUE_REFERENCE_REQUIRED"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			definition := validDefinition()
			test.mutate(&definition)
			_, err := Prepare(definitionJSON(t, definition))
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("expected validation error, got %v", err)
			}
			found := false
			for _, issue := range validation.Issues {
				found = found || issue.Code == test.code
			}
			if !found {
				t.Fatalf("expected %s, got %#v", test.code, validation.Issues)
			}
		})
	}
}
