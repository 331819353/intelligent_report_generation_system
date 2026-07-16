package reportbinding

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/reportjson"
)

func TestResolveFilterUsesParameterCodeAndDatasetSpecificMappings(t *testing.T) {
	document := reportFixture()
	parameters := map[string]any{"period": "2026-07", "region": "华北"}

	resolved, err := Resolve(document, parameters, Interaction{
		Kind: FilterChange, SourceComponentID: "filter_region", Value: "华东",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ParameterID != "param_region" || resolved.ParameterCode != "region" {
		t.Fatalf("parameter=%s/%s", resolved.ParameterID, resolved.ParameterCode)
	}
	if got := resolved.Parameters["region"]; got != "华东" {
		t.Fatalf("runtime parameter region=%v", got)
	}
	if parameters["region"] != "华北" {
		t.Fatalf("input parameters were modified: %#v", parameters)
	}

	want := []Target{
		{ComponentID: "chart_sales", DatasetVersionID: "dsv_sales", FieldID: "field_sales_region", DatasetParameterCode: "sales_region", SemanticFieldCode: "enterprise_region", Operator: "EQUALS"},
		{ComponentID: "table_sales", DatasetVersionID: "dsv_sales", FieldID: "field_sales_region", DatasetParameterCode: "sales_region", SemanticFieldCode: "enterprise_region", Operator: "EQUALS"},
		{ComponentID: "chart_cost", DatasetVersionID: "dsv_cost", FieldID: "field_cost_area", DatasetParameterCode: "cost_area", SemanticFieldCode: "enterprise_region", Operator: "EQUALS"},
		{ComponentID: "table_remote", DatasetVersionID: "dsv_cost", FieldID: "field_cost_area", DatasetParameterCode: "cost_area", SemanticFieldCode: "enterprise_region", Operator: "EQUALS"},
	}
	if !reflect.DeepEqual(resolved.Targets, want) {
		t.Fatalf("targets=\n%#v\nwant=\n%#v", resolved.Targets, want)
	}
}

func TestResolveSupportsAllEffectScopes(t *testing.T) {
	tests := []struct {
		name  string
		scope map[string]any
		want  []string
	}{
		{name: "REPORT", scope: map[string]any{"kind": "REPORT"}, want: []string{"chart_sales", "table_sales", "chart_cost", "table_remote"}},
		{name: "PAGE", scope: map[string]any{"kind": "PAGE"}, want: []string{"chart_sales", "table_sales", "chart_cost"}},
		{name: "BLOCK", scope: map[string]any{"kind": "BLOCK"}, want: []string{"chart_sales", "table_sales"}},
		{name: "COMPONENTS", scope: map[string]any{"kind": "COMPONENTS", "componentIds": []any{"chart_cost", "table_remote"}}, want: []string{"chart_cost", "table_remote"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := reportFixture()
			findComponent(t, &document, "filter_region").Binding["effectScope"] = test.scope

			resolved, err := Resolve(document, nil, Interaction{Kind: FilterChange, SourceComponentID: "filter_region", Value: "华东"})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(resolved.Targets))
			for index, target := range resolved.Targets {
				got[index] = target.ComponentID
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("components=%v want=%v", got, test.want)
			}
		})
	}
}

func TestResolveRejectsPageParameterCrossPageEffects(t *testing.T) {
	document := reportFixture()
	document.Parameters[0].Scope = "PAGE"
	document.Parameters[0].PageID = "page_overview"
	findComponent(t, &document, "filter_region").Binding["effectScope"] = map[string]any{"kind": "REPORT"}

	_, err := Resolve(document, nil, Interaction{Kind: FilterChange, SourceComponentID: "filter_region", Value: "华东"})
	assertReasonContains(t, err, "页面参数 region 不能影响其他页面")

	document.Parameters[0].PageID = "page_remote"
	_, err = Resolve(document, nil, Interaction{Kind: FilterChange, SourceComponentID: "filter_region", Value: "华东"})
	assertReasonContains(t, err, "页面参数 region 不能从其他页面触发")
}

func TestResolveChartClickRequiresDeclaredSemanticDimension(t *testing.T) {
	document := reportFixture()
	chart := findComponent(t, &document, "chart_sales")
	chart.Interaction = map[string]any{
		"clickFilter": true,
		"linkage": map[string]any{
			"parameterId": "param_region", "operator": "EQUALS",
			"effectScope": map[string]any{"kind": "COMPONENTS", "componentIds": []string{"chart_sales", "chart_cost"}},
		},
	}

	resolved, err := Resolve(document, map[string]any{"region": "华北"}, Interaction{
		Kind: ChartClick, SourceComponentID: "chart_sales", DimensionFieldID: "field_sales_region", Value: "华南",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []Target{
		{ComponentID: "chart_sales", DatasetVersionID: "dsv_sales", FieldID: "field_sales_region", DatasetParameterCode: "sales_region", SemanticFieldCode: "enterprise_region", Operator: "EQUALS"},
		{ComponentID: "chart_cost", DatasetVersionID: "dsv_cost", FieldID: "field_cost_area", DatasetParameterCode: "cost_area", SemanticFieldCode: "enterprise_region", Operator: "EQUALS"},
	}
	if !reflect.DeepEqual(resolved.Targets, want) || resolved.Parameters["region"] != "华南" {
		t.Fatalf("resolution=%#v", resolved)
	}

	chart.Interaction["linkage"].(map[string]any)["effectScope"] = map[string]any{"kind": "COMPONENTS", "componentIds": []string{"chart_cost"}}
	resolved, err = Resolve(document, nil, Interaction{
		Kind: ChartClick, SourceComponentID: "chart_sales", DimensionFieldID: "field_sales_region", Value: "华东",
	})
	if err != nil || len(resolved.Targets) != 1 || resolved.Targets[0].ComponentID != "chart_cost" {
		t.Fatalf("source-excluded resolution=%#v, error=%v", resolved, err)
	}

	_, err = Resolve(document, nil, Interaction{
		Kind: ChartClick, SourceComponentID: "chart_sales", DimensionFieldID: "field_sales_amount", Value: "100",
	})
	assertReasonContains(t, err, "点击维度与报告参数的语义字段映射不一致")

	chart.Binding["dimensions"] = []any{map[string]any{"fieldId": "field_city", "role": "CATEGORY"}}
	_, err = Resolve(document, nil, Interaction{
		Kind: ChartClick, SourceComponentID: "chart_sales", DimensionFieldID: "field_sales_region", Value: "华东",
	})
	assertReasonContains(t, err, "图表点击字段不是已声明维度")
}

func TestResolveFailsClosedForInvalidReferencesAndMappings(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*reportjson.Document)
		reason string
	}{
		{
			name: "unknown parameter",
			mutate: func(document *reportjson.Document) {
				findComponent(t, document, "filter_region").Binding["parameterId"] = "param_missing"
			},
			reason: "引用的报告参数 param_missing 不存在",
		},
		{
			name: "duplicate parameter code",
			mutate: func(document *reportjson.Document) {
				duplicate := document.Parameters[0]
				duplicate.ID = "param_region_copy"
				document.Parameters = append(document.Parameters, duplicate)
			},
			reason: "报告参数编码 region 重复",
		},
		{
			name:   "missing semantic binding",
			mutate: func(document *reportjson.Document) { document.Parameters[0].SemanticBinding = nil },
			reason: "缺少语义字段映射",
		},
		{
			name: "missing dataset mapping",
			mutate: func(document *reportjson.Document) {
				document.Parameters[0].SemanticBinding.DatasetFields = document.Parameters[0].SemanticBinding.DatasetFields[:1]
			},
			reason: "在数据集 dsv_cost 中没有映射",
		},
		{
			name: "duplicate dataset mapping",
			mutate: func(document *reportjson.Document) {
				document.Parameters[0].SemanticBinding.DatasetFields = append(document.Parameters[0].SemanticBinding.DatasetFields,
					reportjson.DatasetFieldBinding{DatasetVersionID: "dsv_cost", FieldID: "field_duplicate", DatasetParameterCode: "duplicate"})
			},
			reason: "在数据集 dsv_cost 中存在重复映射",
		},
		{
			name: "unknown target",
			mutate: func(document *reportjson.Document) {
				findComponent(t, document, "filter_region").Binding["effectScope"] = map[string]any{"kind": "COMPONENTS", "componentIds": []string{"component_missing"}}
			},
			reason: "指定的目标组件 component_missing 不存在",
		},
		{
			name: "duplicate target",
			mutate: func(document *reportjson.Document) {
				findComponent(t, document, "filter_region").Binding["effectScope"] = map[string]any{"kind": "COMPONENTS", "componentIds": []string{"chart_sales", "chart_sales"}}
			},
			reason: "目标组件 chart_sales 重复",
		},
		{
			name: "missing operator",
			mutate: func(document *reportjson.Document) {
				delete(findComponent(t, document, "filter_region").Binding, "operator")
			},
			reason: "联动操作符无效",
		},
		{
			name: "explicit target without dataset binding",
			mutate: func(document *reportjson.Document) {
				findComponent(t, document, "filter_region").Binding["effectScope"] = map[string]any{"kind": "COMPONENTS", "componentIds": []string{"filter_region", "chart_sales"}}
			},
			reason: "目标组件 filter_region 缺少数据集版本绑定",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := reportFixture()
			test.mutate(&document)
			_, err := Resolve(document, nil, Interaction{Kind: FilterChange, SourceComponentID: "filter_region", Value: "华东"})
			assertReasonContains(t, err, test.reason)
		})
	}

	_, err := Resolve(reportFixture(), nil, Interaction{Kind: FilterChange, SourceComponentID: "component_missing", Value: "华东"})
	assertReasonContains(t, err, "交互来源组件 component_missing 不存在")
}

func TestResolveRejectsOperatorCardinalityMismatch(t *testing.T) {
	tests := []struct {
		name       string
		operator   string
		multiValue bool
		dataType   string
		value      any
		reason     string
	}{
		{name: "IN with scalar parameter", operator: "IN", value: "华东", reason: "IN 操作符只允许多值参数"},
		{name: "BETWEEN with one value", operator: "BETWEEN", multiValue: true, value: []any{"华东"}, reason: "BETWEEN 操作符要求恰好两个参数值"},
		{name: "EQUALS with multi value", operator: "EQUALS", multiValue: true, value: []any{"华东"}, reason: "EQUALS 操作符只允许单值参数"},
		{name: "DATE_RANGE with scalar operator", operator: "GTE", dataType: "DATE_RANGE", value: []any{"2026-01-01", "2026-01-31"}, reason: "GTE 操作符只允许单值参数"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := reportFixture()
			document.Parameters[0].MultiValue = test.multiValue
			if test.dataType != "" {
				document.Parameters[0].DataType = test.dataType
			}
			findComponent(t, &document, "filter_region").Binding["operator"] = test.operator
			_, err := Resolve(document, nil, Interaction{Kind: FilterChange, SourceComponentID: "filter_region", Value: test.value})
			assertReasonContains(t, err, test.reason)
		})
	}
}

func TestResolveDoesNotMutateDocumentOrInputParameters(t *testing.T) {
	document := reportFixture()
	parameters := map[string]any{"region": "华北", "nested": map[string]any{"retained": true}, "items": []any{"a", "b"}}
	documentBefore := snapshotJSON(t, document)
	parametersBefore := cloneMap(t, parameters)

	resolved, err := Resolve(document, parameters, Interaction{Kind: FilterChange, SourceComponentID: "filter_region", Value: "华东"})
	if err != nil {
		t.Fatal(err)
	}
	if snapshotJSON(t, document) != documentBefore || !reflect.DeepEqual(parameters, parametersBefore) {
		t.Fatalf("resolver modified inputs")
	}
	resolved.Parameters["nested"].(map[string]any)["retained"] = false
	resolved.Parameters["items"].([]any)[0] = "changed"
	if !reflect.DeepEqual(parameters, parametersBefore) {
		t.Fatalf("resolution shares mutable values with input: %#v", parameters)
	}

	findComponent(t, &document, "filter_region").Binding["effectScope"] = map[string]any{"kind": "COMPONENTS", "componentIds": []string{"missing"}}
	documentBefore = snapshotJSON(t, document)
	_, _ = Resolve(document, parameters, Interaction{Kind: FilterChange, SourceComponentID: "filter_region", Value: "华南"})
	if snapshotJSON(t, document) != documentBefore || !reflect.DeepEqual(parameters, parametersBefore) {
		t.Fatalf("failed resolution modified inputs")
	}
}

func reportFixture() reportjson.Document {
	return reportjson.Document{
		Parameters: []reportjson.Parameter{{
			ID: "param_region", Code: "region", Name: "区域", DataType: "STRING", Scope: "REPORT",
			SemanticBinding: &reportjson.SemanticBinding{
				SemanticFieldCode: "enterprise_region",
				DatasetFields: []reportjson.DatasetFieldBinding{
					{DatasetVersionID: "dsv_sales", FieldID: "field_sales_region", DatasetParameterCode: "sales_region"},
					{DatasetVersionID: "dsv_cost", FieldID: "field_cost_area", DatasetParameterCode: "cost_area"},
				},
			},
		}},
		Pages: []reportjson.Page{
			{
				ID: "page_overview",
				Blocks: []reportjson.Block{
					{ID: "block_primary", Components: []reportjson.Component{
						filterComponent(),
						dataComponent("chart_sales", "CHART", "dsv_sales", []any{map[string]any{"fieldId": "field_sales_region", "role": "CATEGORY"}}),
						dataComponent("table_sales", "TABLE", "dsv_sales", nil),
					}},
					{ID: "block_secondary", Components: []reportjson.Component{
						dataComponent("chart_cost", "CHART", "dsv_cost", []any{map[string]any{"fieldId": "field_cost_area", "role": "CATEGORY"}}),
					}},
				},
			},
			{
				ID: "page_remote",
				Blocks: []reportjson.Block{{ID: "block_remote", Components: []reportjson.Component{
					dataComponent("table_remote", "TABLE", "dsv_cost", nil),
				}}},
			},
		},
	}
}

func filterComponent() reportjson.Component {
	return reportjson.Component{
		ID: "filter_region", Type: "FILTER", Name: "区域筛选",
		Binding: map[string]any{
			"parameterId": "param_region", "operator": "EQUALS",
			"effectScope": map[string]any{"kind": "REPORT"},
		},
	}
}

func dataComponent(id, componentType, datasetVersionID string, dimensions []any) reportjson.Component {
	binding := map[string]any{"datasetVersionId": datasetVersionID}
	if dimensions != nil {
		binding["dimensions"] = dimensions
	}
	return reportjson.Component{ID: id, Type: componentType, Name: id, Binding: binding, Interaction: map[string]any{}}
}

func findComponent(t *testing.T, document *reportjson.Document, componentID string) *reportjson.Component {
	t.Helper()
	for pageIndex := range document.Pages {
		for blockIndex := range document.Pages[pageIndex].Blocks {
			components := document.Pages[pageIndex].Blocks[blockIndex].Components
			for componentIndex := range components {
				if components[componentIndex].ID == componentID {
					return &components[componentIndex]
				}
			}
		}
	}
	t.Fatalf("component %s not found", componentID)
	return nil
}

func assertReasonContains(t *testing.T, err error, fragment string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), fragment) {
		t.Fatalf("error=%v, want fragment %q", err, fragment)
	}
}

func snapshotJSON(t *testing.T, value any) string {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func cloneMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
