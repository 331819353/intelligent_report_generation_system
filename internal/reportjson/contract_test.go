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
		for _, blockValue := range pageValue.(map[string]any)["blocks"].([]any) {
			delete(blockValue.(map[string]any), "innerGrid")
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
	for _, want := range []string{"pages[0].blocks[0].innerGrid.columns", "pages[0].blocks[0].components[3].type", "pages[0].blocks[0].components[3].grid"} {
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
	document.Pages[0].Blocks[0].Sticky = Sticky{Enabled: true, Top: -1, Scope: "CONTAINER"}
	err = Validate(document)
	for _, want := range []string{"pages[0].contentGridRows", "pages[0].blocks[0].sticky.top", "pages[0].blocks[0].sticky.containerId", "pages[0].blocks[0].sticky.zIndex"} {
		if !hasPath(err, want) {
			t.Fatalf("缺少校验路径 %s: %v", want, err)
		}
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
