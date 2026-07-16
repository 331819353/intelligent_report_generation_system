package reportjson

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestPrepareExampleIsDeterministic(t *testing.T) {
	first, err := Prepare(readExample(t))
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	second, err := Prepare(first.JSON)
	if err != nil {
		t.Fatalf("Prepare(normalized) error = %v", err)
	}
	if first.Hash != second.Hash || string(first.JSON) != string(second.JSON) {
		t.Fatal("规范 JSON 或哈希不稳定")
	}
	if first.Document.Pages[0].ContentGridRows != 14 {
		t.Fatalf("contentGridRows = %d, want 14", first.Document.Pages[0].ContentGridRows)
	}
}

func TestDecodeAndNormalizeMigratesLegacyCanvas(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(readExample(t), &input); err != nil {
		t.Fatal(err)
	}
	input["schemaVersion"] = "0.9"
	canvas := input["canvas"].(map[string]any)
	canvas["logicalHeight"] = canvas["viewportHeight"]
	canvas["gridRows"] = canvas["viewportGridRows"]
	for _, key := range []string{"viewportHeight", "viewportGridRows", "contentGridRows", "minContentGridRows", "innerGridMultiplier", "scaleMode", "verticalOverflow"} {
		delete(canvas, key)
	}
	for _, pageValue := range input["pages"].([]any) {
		for blockIndex, blockValue := range pageValue.(map[string]any)["blocks"].([]any) {
			block := blockValue.(map[string]any)
			delete(block, "innerGrid")
			if blockIndex == 0 {
				block["sticky"] = map[string]any{}
			} else {
				delete(block, "sticky")
			}
			for componentIndex, componentValue := range block["components"].([]any) {
				component := componentValue.(map[string]any)
				if blockIndex == 0 && componentIndex == 0 {
					component["sticky"] = map[string]any{"enabled": false, "top": 0}
				} else {
					delete(component, "sticky")
				}
			}
		}
	}
	raw, _ := json.Marshal(input)
	prepared, err := Prepare(raw)
	if err != nil {
		t.Fatalf("Prepare(legacy) error = %v", err)
	}
	if prepared.Document.Canvas.ViewportHeight != 1080 || prepared.Document.Canvas.ViewportGridRows != 10 {
		t.Fatalf("旧画布字段迁移失败: %#v", prepared.Document.Canvas)
	}
	if strings.Contains(string(prepared.JSON), "logicalHeight") || strings.Contains(string(prepared.JSON), "gridRows") {
		t.Fatal("规范 JSON 仍包含旧字段")
	}
	for _, page := range prepared.Document.Pages {
		for _, block := range page.Blocks {
			if block.Sticky == nil || block.Sticky.Enabled {
				t.Fatalf("0.9 分块冻结配置未迁移为禁用态: %#v", block.Sticky)
			}
			for _, component := range block.Components {
				if component.Sticky == nil || component.Sticky.Enabled {
					t.Fatalf("0.9 组件冻结配置未迁移为禁用态: %#v", component.Sticky)
				}
			}
		}
	}
}

func TestDecodeAndNormalizeKeepsLegacyEnabledStickyDefaultTop(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(readExample(t), &input); err != nil {
		t.Fatal(err)
	}
	input["schemaVersion"] = "0.9"
	firstComponent(input)["sticky"] = map[string]any{"enabled": true, "scope": "PAGE", "zIndex": 1}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(raw)
	if err != nil {
		t.Fatalf("0.9 启用态应沿用缺省 top=0: %v", err)
	}
	sticky := prepared.Document.Pages[0].Blocks[0].Components[0].Sticky
	if sticky == nil || !sticky.Enabled || sticky.Top != 0 {
		t.Fatalf("0.9 启用态冻结迁移错误: %#v", sticky)
	}
}

func TestPrepareRejectsUnknownField(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(readExample(t), &input); err != nil {
		t.Fatal(err)
	}
	input["designerTemporaryState"] = map[string]any{"selected": true}
	raw, _ := json.Marshal(input)
	if _, err := Prepare(raw); err == nil {
		t.Fatal("Prepare() 接受了未知顶层字段")
	}
}

func TestPrepareRejectsLegacyCanvasFieldsInCurrentVersion(t *testing.T) {
	var input map[string]any
	if err := json.Unmarshal(readExample(t), &input); err != nil {
		t.Fatal(err)
	}
	input["canvas"].(map[string]any)["logicalHeight"] = nil
	raw, _ := json.Marshal(input)
	if _, err := Prepare(raw); err == nil {
		t.Fatal("1.0 文档接受了旧画布字段")
	}
}

func TestValidateReportsCollisionBoundsAndUnknownComponent(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	block := &document.Pages[0].Blocks[0]
	component := block.Components[0]
	component.ID = "unknown_component"
	component.Type = "EXECUTABLE_HTML"
	component.Grid = block.Components[0].Grid
	block.Components = append(block.Components, component)
	block.InnerGrid.Columns--
	err = Validate(document)
	for _, want := range []string{"pages[0].blocks[0].innerGrid.columns", "pages[0].blocks[0].components[4].type", "pages[0].blocks[0].components[4].grid"} {
		if !hasPath(err, want) {
			t.Fatalf("缺少校验路径 %s: %v", want, err)
		}
	}
}

func TestValidateRejectsStaleContentRowsAndInvalidSticky(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	document.Pages[0].ContentGridRows = 10
	document.Pages[0].Blocks[0].Sticky = &Sticky{Enabled: true, Top: -1, Scope: "CONTAINER"}
	err = Validate(document)
	for _, want := range []string{"pages[0].contentGridRows", "pages[0].blocks[0].sticky.top", "pages[0].blocks[0].sticky.containerId", "pages[0].blocks[0].sticky.zIndex"} {
		if !hasPath(err, want) {
			t.Fatalf("缺少校验路径 %s: %v", want, err)
		}
	}
}

func TestPrepareRejectsNonCanonicalStickyShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "当前版本缺少 sticky",
			mutate: func(input map[string]any) {
				delete(firstBlock(input), "sticky")
			},
		},
		{
			name: "空 sticky 对象",
			mutate: func(input map[string]any) {
				firstComponent(input)["sticky"] = map[string]any{}
			},
		},
		{
			name: "禁用态夹带零值参数",
			mutate: func(input map[string]any) {
				firstBlock(input)["sticky"] = map[string]any{"enabled": false, "top": 0}
			},
		},
		{
			name: "启用态缺少 top",
			mutate: func(input map[string]any) {
				firstComponent(input)["sticky"] = map[string]any{"enabled": true, "scope": "PAGE", "zIndex": 1}
			},
		},
		{
			name: "enabled 不能为 null",
			mutate: func(input map[string]any) {
				firstBlock(input)["sticky"] = map[string]any{"enabled": nil}
			},
		},
		{
			name: "启用态 top 不能为 null",
			mutate: func(input map[string]any) {
				firstComponent(input)["sticky"] = map[string]any{"enabled": true, "top": nil, "scope": "PAGE", "zIndex": 1}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var input map[string]any
			if err := json.Unmarshal(readExample(t), &input); err != nil {
				t.Fatal(err)
			}
			test.mutate(input)
			raw, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Prepare(raw); err == nil {
				t.Fatal("Prepare() 接受了非规范冻结配置")
			}
		})
	}
}

func TestValidateStickyContainerMustReferenceSamePageAncestor(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	page := &document.Pages[0]
	block := &page.Blocks[0]
	component := &block.Components[0]
	block.Sticky = &Sticky{Enabled: true, Top: 0, Scope: "CONTAINER", ContainerID: page.ID, ZIndex: 1}
	component.Sticky = &Sticky{Enabled: true, Top: 0, Scope: "CONTAINER", ContainerID: block.ID, ZIndex: 2}
	if err := Validate(document); err != nil {
		t.Fatalf("所属页面和分块祖先应为合法冻结容器: %v", err)
	}

	block.Sticky.ContainerID = block.ID
	component.Sticky.ContainerID = document.Pages[0].Blocks[1].ID
	err = Validate(document)
	for _, want := range []string{
		"pages[0].blocks[0].sticky.containerId",
		"pages[0].blocks[0].components[0].sticky.containerId",
	} {
		if !hasPath(err, want) {
			t.Fatalf("缺少非祖先冻结容器校验路径 %s: %v", want, err)
		}
	}
}

func TestValidateStickyContainerRejectsAmbiguousAncestorID(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	page := &document.Pages[0]
	block := &page.Blocks[0]
	page.ID = block.ID
	block.Components[0].Sticky = &Sticky{
		Enabled: true, Top: 0, Scope: "CONTAINER", ContainerID: block.ID, ZIndex: 1,
	}
	err = Validate(document)
	if !hasPath(err, "pages[0].blocks[0].components[0].sticky.containerId") {
		t.Fatalf("缺少歧义冻结容器校验路径: %v", err)
	}
}

func TestValidateStickyScopeAndLimitsMatchSchema(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	document.Pages[0].Blocks[0].Sticky = &Sticky{
		Enabled: true, Top: MaxStickyTop + 1, Scope: "BLOCK", ZIndex: MaxStickyZIndex + 1,
	}
	document.Pages[0].Blocks[0].Components[0].Sticky = &Sticky{
		Enabled: true, Top: 0, Scope: "BLOCK", ZIndex: 1,
	}
	err = Validate(document)
	for _, want := range []string{
		"pages[0].blocks[0].sticky.top",
		"pages[0].blocks[0].sticky.scope",
		"pages[0].blocks[0].sticky.zIndex",
	} {
		if !hasPath(err, want) {
			t.Fatalf("缺少冻结范围或上限校验路径 %s: %v", want, err)
		}
	}
}

func TestValidateRejectsComponentMinimumSizeAndUnknownConfig(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	chart := &document.Pages[0].Blocks[0].Components[2]
	chart.Grid.W = 3
	chart.Style["executableOption"] = true
	chart.Style["legendPosition"] = true
	chart.Binding["chart"] = map[string]any{"type": "RADAR"}
	document.Pages[0].Blocks[0].Components[0].RefreshPolicy["mode"] = "INHERIT"
	err = Validate(document)
	for _, want := range []string{
		"pages[0].blocks[0].components[0].refreshPolicy.mode",
		"pages[0].blocks[0].components[2].grid",
		"pages[0].blocks[0].components[2].style.executableOption",
		"pages[0].blocks[0].components[2].style.legendPosition",
		"pages[0].blocks[0].components[2].binding.chart.type",
	} {
		if !hasPath(err, want) {
			t.Fatalf("缺少组件配置校验路径 %s: %v", want, err)
		}
	}
}

func TestValidateConclusionReferencesMustExistAndMatchKind(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	conclusion := &document.Pages[0].Blocks[0].Components[3]
	conclusion.Binding["metricIds"] = []any{"metric_missing"}
	conclusion.Binding["chartComponentIds"] = []any{"title_main", "component_missing"}
	err = Validate(document)
	for _, want := range []string{
		"pages[0].blocks[0].components[3].binding.metricIds[0]",
		"pages[0].blocks[0].components[3].binding.chartComponentIds[0]",
		"pages[0].blocks[0].components[3].binding.chartComponentIds[1]",
	} {
		if !hasPath(err, want) {
			t.Fatalf("缺少结论引用校验路径 %s: %v", want, err)
		}
	}
}

func TestValidateRejectsAmbiguousSemanticMappingAndUnknownInteractionTarget(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	document.Parameters[0].SemanticBinding.DatasetFields = append(document.Parameters[0].SemanticBinding.DatasetFields,
		DatasetFieldBinding{DatasetVersionID: "dsv_enterprise_revenue_v3", FieldID: "field_duplicate", DatasetParameterCode: "stat_month_duplicate"})
	filter := &document.Pages[0].Blocks[0].Components[1]
	filter.Binding["effectScope"] = map[string]any{"kind": "COMPONENTS", "componentIds": []any{"component_missing", "component_missing"}}

	err = Validate(document)
	for _, want := range []string{
		"parameters[0].semanticBinding.datasetFields[1].datasetVersionId",
		"pages[0].blocks[0].components[1].binding.effectScope.componentIds[1]",
		"pages[0].blocks[0].components[1].binding.effectScope.componentIds[0]",
	} {
		if !hasPath(err, want) {
			t.Fatalf("缺少联动合同校验路径 %s: %v", want, err)
		}
	}
}

func TestValidateRejectsPageParameterCrossPageScopeAndBrokenChartLinkage(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	document.Parameters[0].Scope = "PAGE"
	document.Parameters[0].PageID = "page_overview"
	filter := &document.Pages[0].Blocks[0].Components[1]
	filter.Binding["effectScope"] = map[string]any{"kind": "REPORT"}
	chart := &document.Pages[0].Blocks[0].Components[2]
	chart.Interaction["linkage"] = map[string]any{
		"parameterId": "param_missing", "operator": "EQUALS", "effectScope": map[string]any{"kind": "PAGE"},
	}

	err = Validate(document)
	for _, want := range []string{
		"pages[0].blocks[0].components[1].binding.effectScope.kind",
		"pages[0].blocks[0].components[2].interaction.linkage.parameterId",
	} {
		if !hasPath(err, want) {
			t.Fatalf("缺少页面参数或图表联动校验路径 %s: %v", want, err)
		}
	}
}

func TestValidateAllowsLegacyDraftFilterAndKPIWithoutExecutableBindings(t *testing.T) {
	prepared, err := Prepare(readExample(t))
	if err != nil {
		t.Fatal(err)
	}
	document := prepared.Document
	filter := &document.Pages[0].Blocks[0].Components[1]
	filter.Binding = map[string]any{"parameterId": ""}
	filter.Interaction = map[string]any{}
	filter.Style = map[string]any{}
	kpi := &document.Pages[0].Blocks[1].Components[0]
	kpi.ID = "kpi_draft"
	kpi.Type = "KPI"
	kpi.Name = "草稿指标"
	kpi.Style = map[string]any{}
	kpi.Binding = map[string]any{"metricId": ""}
	kpi.Interaction = map[string]any{}
	kpi.RefreshPolicy = map[string]any{"mode": "INHERIT"}
	// 草稿合同允许绑定尚未完成；发布和运行解析器仍会失败关闭。
	if err := Validate(document); err != nil {
		t.Fatalf("渐进配置草稿不应被结构合同拒绝: %v", err)
	}
}

func TestSchemaAndExampleAreValidJSON(t *testing.T) {
	for _, path := range []string{"../../api/schemas/report-json-v1.schema.json", "../../api/examples/report-json-v1.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			t.Fatalf("%s 不是合法 JSON: %v", path, err)
		}
	}
}

func readExample(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../api/examples/report-json-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func firstBlock(input map[string]any) map[string]any {
	return input["pages"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)
}

func firstComponent(input map[string]any) map[string]any {
	return firstBlock(input)["components"].([]any)[0].(map[string]any)
}

func hasPath(err error, path string) bool {
	var validation *ValidationError
	if !errors.As(err, &validation) {
		return false
	}
	for _, issue := range validation.Issues {
		if issue.Path == path {
			return true
		}
	}
	return false
}
