package compiler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

func TestNormalizeIsIdempotentAndMergesManifestDefaults(t *testing.T) {
	definition := compilerDefinition(t)
	canonical, hash, err := Normalize(definition)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := report.Decode(canonical)
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := Normalize(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, second) || hash != secondHash {
		t.Fatalf("Normalize(Normalize(x)) changed bytes/hash")
	}
	if !bytes.HasPrefix(canonical, []byte(`{"canvas":`)) {
		t.Fatalf("canonical object keys are not lexicographically ordered: %.80s", canonical)
	}
	if normalized.Components[0].Options.Animation == nil || !*normalized.Components[0].Options.Animation {
		t.Fatalf("manifest animation default was not merged: %#v", normalized.Components[0].Options)
	}
	if normalized.Components[0].Options.ShowLegend == nil || *normalized.Components[0].Options.ShowLegend {
		t.Fatalf("explicit false option was overwritten by defaults: %#v", normalized.Components[0].Options)
	}
}

func TestNormalizeSemanticOrderAndNullFormsHaveSameHash(t *testing.T) {
	left := compilerDefinition(t)
	addRichTextComponent(&left, 2, "<p>说明</p>")
	right, err := cloneDefinition(left)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(right.Components)
	slices.Reverse(right.Pages[0].Sections[0].Blocks)
	left.GlobalFilters = nil
	left.Provenance.SourceQuestionRunIDs = nil
	left.Provenance.AIRunIDs = nil
	left.DataContexts[0].DefaultParameters = nil
	right.GlobalFilters = []report.GlobalFilter{}
	right.Provenance.SourceQuestionRunIDs = []askdata.ID{}
	right.Provenance.AIRunIDs = []askdata.ID{}
	right.DataContexts[0].DefaultParameters = []report.DefaultParameter{}
	leftCanonical, leftHash, err := Normalize(left)
	if err != nil {
		t.Fatal(err)
	}
	rightCanonical, rightHash, err := Normalize(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftHash != rightHash || !bytes.Equal(leftCanonical, rightCanonical) {
		t.Fatal("semantic ordering/null forms produced different canonical definitions")
	}
	right.Metadata.Description += " changed"
	_, changedHash, err := Normalize(right)
	if err != nil {
		t.Fatal(err)
	}
	if changedHash == rightHash {
		t.Fatal("semantic value change did not change definition hash")
	}
}

func TestValidateDefinitionStagesAccumulateAndShortCircuit(t *testing.T) {
	t.Run("unique IDs", func(t *testing.T) {
		definition := compilerDefinition(t)
		addRichTextComponent(&definition, 2, "safe")
		definition.Components[1].ID = definition.Components[0].ID
		definition.Pages[0].Sections[0].Blocks[1].ID = definition.Pages[0].Sections[0].Blocks[0].ID
		issues := ValidateDefinition(definition, nil)
		assertIssueStage(t, issues, "REPORT_ID_DUPLICATE", 2)
	})

	t.Run("references", func(t *testing.T) {
		definition := compilerDefinition(t)
		definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots = append(
			definition.Pages[0].Sections[0].Blocks[0].Zones[0].Slots,
			report.Slot{ID: "slot_missing", Grid: report.SlotGrid{W: 1, H: 1}, ComponentID: "missing_component"},
		)
		definition.GlobalFilters = []report.GlobalFilter{{
			ID: "filter_missing", Type: report.FilterSelect,
			FieldRef: report.FieldReference{DataContextID: "missing_context", Field: "region"},
			Scope:    report.FilterScope{Type: report.FilterScopeComponent, TargetIDs: []askdata.ID{"missing_target"}},
		}}
		issues := ValidateDefinition(definition, nil)
		assertIssueStage(t, issues, "REPORT_REFERENCE_MISSING", 3)
	})

	t.Run("layout", func(t *testing.T) {
		definition := compilerDefinition(t)
		addRichTextComponent(&definition, 2, "safe")
		addRichTextComponent(&definition, 3, "safe")
		blocks := definition.Pages[0].Sections[0].Blocks
		blocks[1].Layout.Desktop = blocks[0].Layout.Desktop
		blocks[2].Layout.Desktop = blocks[0].Layout.Desktop
		issues := ValidateDefinition(definition, nil)
		assertIssueStage(t, issues, "REPORT_LAYOUT_COLLISION", 3)
	})

	t.Run("manifest and options", func(t *testing.T) {
		definition := compilerDefinition(t)
		addRichTextComponent(&definition, 2, "safe")
		definition.Components[0].TemplateRef.Type = "unknown-one"
		definition.Components[1].TemplateRef.Type = "unknown-two"
		issues := ValidateDefinition(definition, nil)
		assertIssueStage(t, issues, "REPORT_COMPONENT_INVALID", 2)
	})

	t.Run("data contract", func(t *testing.T) {
		definition := compilerDefinition(t)
		addLineComponent(&definition, 2)
		for index := range definition.Components {
			definition.Components[index].DataBinding.Dimensions[0].Role = report.RoleLabel
		}
		issues := ValidateDefinition(definition, nil)
		assertIssueStage(t, issues, "REPORT_COMPONENT_DATA_INVALID", 2)
	})
}

func TestNormalizeSanitizesRichTextAndRequiresExplicitMajorMigration(t *testing.T) {
	definition := compilerDefinition(t)
	malicious := `<p onclick="steal()">可见<strong>结论</strong><script>alert(1)</script>` +
		`<svg><text>drop</text></svg><a href="javascript:alert(1)" target="_blank" onerror="x">链接</a>` +
		`<a href="https://example.invalid" target="_blank">安全</a></p>`
	addRichTextComponent(&definition, 2, malicious)
	canonical, hash, err := Normalize(definition)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Components[1].Options.RichText != malicious {
		t.Fatal("Normalize mutated the caller definition")
	}
	var normalized report.ReportDefinition
	if err := json.Unmarshal(canonical, &normalized); err != nil {
		t.Fatal(err)
	}
	richText := ""
	for _, component := range normalized.Components {
		if component.TemplateRef.Type == "rich-text" {
			richText = component.Options.RichText
		}
	}
	for _, forbidden := range []string{"script", "onclick", "onerror", "javascript:", "<svg", "drop"} {
		if strings.Contains(strings.ToLower(richText), forbidden) {
			t.Fatalf("sanitized rich text retained %q: %s", forbidden, richText)
		}
	}
	if !strings.Contains(richText, "<strong>结论</strong>") ||
		!strings.Contains(richText, `href="https://example.invalid"`) ||
		!strings.Contains(richText, `rel="noopener noreferrer"`) {
		t.Fatalf("sanitized rich text lost allowed deterministic markup: %s", richText)
	}
	second, secondHash, err := Normalize(normalized)
	if err != nil || !bytes.Equal(canonical, second) || hash != secondHash {
		t.Fatalf("sanitized output is not hash-stable: %v", err)
	}

	compatible := compilerDefinition(t)
	compatible.SchemaVersion = "1.9"
	compatibleCanonical, _, err := Normalize(compatible)
	if err != nil || !bytes.Contains(compatibleCanonical, []byte(`"schemaVersion":"1.0"`)) {
		t.Fatalf("compatible V1 minor normalize = %s, %v", compatibleCanonical, err)
	}
	major := compilerDefinition(t)
	major.SchemaVersion = "2.0"
	if _, _, err := Normalize(major); err == nil || !strings.Contains(err.Error(), "explicit major-version migrator") {
		t.Fatalf("major version normalize error = %v", err)
	}
}

func TestNormalizeNearFiveMegabytes(t *testing.T) {
	definition := largeCompilerDefinition(t)
	canonical, _, err := Normalize(definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) < 4_500_000 || len(canonical) > report.MaxDefinitionBytes {
		t.Fatalf("canonical size = %d", len(canonical))
	}
}

func BenchmarkNormalizeNearFiveMegabytes(b *testing.B) {
	definition := largeCompilerDefinition(b)
	b.ReportAllocs()
	b.SetBytes(4_500_000)
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, _, err := Normalize(definition); err != nil {
			b.Fatal(err)
		}
	}
}

func assertIssueStage(t *testing.T, issues ValidationIssues, code string, minimum int) {
	t.Helper()
	if len(issues) < minimum {
		t.Fatalf("issues = %#v, want at least %d", issues, minimum)
	}
	for _, issue := range issues {
		if issue.Code != code {
			t.Fatalf("stage did not short-circuit: %#v", issues)
		}
	}
}

func compilerDefinition(t testing.TB) report.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve compiler test path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api",
		"examples", "report-definition", "simple-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := report.Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	zone := &definition.Pages[0].Sections[0].Blocks[0].Zones[0]
	zone.Layout.Columns, zone.Layout.Rows = 4, 3
	zone.Slots[0].Grid.W, zone.Slots[0].Grid.H = 4, 3
	return definition
}

func addRichTextComponent(definition *report.ReportDefinition, ordinal int, richText string) {
	id := askdata.ID("component_rich_" + string(rune('a'+ordinal)))
	blockID := askdata.ID("block_rich_" + string(rune('a'+ordinal)))
	zoneID := askdata.ID("zone_rich_" + string(rune('a'+ordinal)))
	slotID := askdata.ID("slot_rich_" + string(rune('a'+ordinal)))
	definition.Components = append(definition.Components, report.Component{
		ID: id, TemplateRef: report.ComponentTemplateReference{Type: "rich-text", Version: "1.0.0"},
		Options: report.ComponentOptions{RichText: richText},
	})
	definition.Pages[0].Sections[0].Blocks = append(definition.Pages[0].Sections[0].Blocks, report.Block{
		ID: blockID, Type: report.BlockContent,
		Layout: report.BlockLayout{
			Desktop: report.DesktopBlockLayout{X: 0, Y: ordinal * 6, W: 12, H: 2},
			Mobile:  report.MobileBlockLayout{Order: ordinal, Visible: true, HeightMode: report.MobileHeightAuto, SlotMode: report.MobileSlotStack},
		},
		Zones: []report.Zone{{
			ID: zoneID, Type: report.ZoneContent,
			Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 40, Columns: 2, Rows: 1, Overflow: report.OverflowExpand},
			Slots:  []report.Slot{{ID: slotID, Grid: report.SlotGrid{W: 2, H: 1}, ComponentID: id}},
		}},
	})
}

func addLineComponent(definition *report.ReportDefinition, ordinal int) {
	component := definition.Components[0]
	component.ID = askdata.ID("component_line_" + string(rune('a'+ordinal)))
	definition.Components = append(definition.Components, component)
	raw, err := json.Marshal(definition.Pages[0].Sections[0].Blocks[0])
	if err != nil {
		panic(err)
	}
	var block report.Block
	if err := json.Unmarshal(raw, &block); err != nil {
		panic(err)
	}
	block.ID = askdata.ID("block_line_" + string(rune('a'+ordinal)))
	block.Layout.Desktop.Y = ordinal * 6
	block.Layout.Mobile.Order = ordinal
	block.Zones[0].ID = askdata.ID("zone_line_" + string(rune('a'+ordinal)))
	block.Zones[0].Slots[0].ID = askdata.ID("slot_line_" + string(rune('a'+ordinal)))
	block.Zones[0].Slots[0].ComponentID = component.ID
	definition.Pages[0].Sections[0].Blocks = append(definition.Pages[0].Sections[0].Blocks, block)
}

func largeCompilerDefinition(t testing.TB) report.ReportDefinition {
	t.Helper()
	definition := compilerDefinition(t)
	definition.Components = []report.Component{}
	definition.Pages[0].Sections[0].Blocks = []report.Block{}
	definition.DataContexts = []report.DataContext{}
	value := strings.Repeat("x", 4_000)
	for contextIndex := 1; contextIndex <= 18; contextIndex++ {
		parameters := make([]report.DefaultParameter, 64)
		for parameterIndex := range parameters {
			parameterValue := value
			parameters[parameterIndex] = report.DefaultParameter{
				Name: "parameter_" + leftPad(parameterIndex+1), Type: report.ParameterString,
				StringValue: &parameterValue,
			}
		}
		definition.DataContexts = append(definition.DataContexts, report.DataContext{
			ID:               askdata.ID("context_large_" + leftPad(contextIndex)),
			DatasetID:        askdata.ID("dataset_large_" + leftPad(contextIndex)),
			DatasetVersionID: askdata.ID("dataset_version_large_" + leftPad(contextIndex)),
			Alias:            "Large context " + leftPad(contextIndex), DefaultParameters: parameters,
			QueryPolicy: report.QueryPolicy{TimeoutMS: 5_000, MaxRows: 10_000, CacheTTLSeconds: 300},
		})
	}
	return definition
}

func leftPad(value int) string {
	return fmt.Sprintf("%03d", value)
}
