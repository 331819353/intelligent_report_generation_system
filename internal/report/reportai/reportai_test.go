package reportai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/operation"
	"intelligent-report-generation-system/internal/report/template"
)

type planGeneratorFunc func(context.Context, PlanRequest) (Plan, error)

func (function planGeneratorFunc) GenerateReportPlan(ctx context.Context, request PlanRequest) (Plan, error) {
	return function(ctx, request)
}

type scopedGeneratorFunc func(context.Context, ScopedContext) (operation.Bundle, error)

func (function scopedGeneratorFunc) GenerateScopedOperations(ctx context.Context, request ScopedContext) (operation.Bundle, error) {
	return function(ctx, request)
}

func TestInstantiateIsDeterministicAndPassesFullDefinitionValidation(t *testing.T) {
	base := reportAIBaseDefinition(t)
	components := reportAIComponents(t)
	methods := insight.NewRegistry()
	plan := validReportPlan()
	input := InstantiateInput{Base: base, DataContextID: base.DataContexts[0].ID, AllowedFields: []string{"month", "sales_amount"}}
	first, err := Instantiate(plan, input, components, methods)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Instantiate(plan, input, components, methods)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, firstHash, err := compiler.Normalize(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, secondHash, err := compiler.Normalize(second)
	if err != nil || firstHash != secondHash || !reflect.DeepEqual(firstRaw, secondRaw) {
		t.Fatalf("deterministic instantiate hashes %s/%s err=%v", firstHash, secondHash, err)
	}
	if len(compiler.ValidateDefinition(first, components)) != 0 {
		t.Fatalf("instantiated definition is invalid: %#v", compiler.ValidateDefinition(first, components))
	}
}

func TestReportAIUsesTheSharedChartRecommendationFunction(t *testing.T) {
	components := reportAIComponents(t)
	block := validReportPlan().Sections[0].Blocks[0]
	input := InstantiateInput{AllMetricsAdditive: true, EstimatedRows: 12}
	got := RecommendPlanBlock(block, input, components)
	want := answer.RecommendChart(answer.ChartRuleInput{
		MetricCount: 1, TimeGrain: "AUTO", NonTimeGroupByCount: 0, RowCount: 12, Additive: true,
	}, components)
	if !reflect.DeepEqual(got, want) || got.ComponentType != "line-trend" || got.RuleVersion != answer.ChartRuleVersion {
		t.Fatalf("report recommendation = %#v; Ask Data = %#v", got, want)
	}

	share := block
	share.AnalysisMethods = []insight.AnalysisMethod{insight.AnalysisShareOfTotal}
	share.DataRoles.Dimensions = []string{"region"}
	unsafe := RecommendPlanBlock(share, InstantiateInput{AllMetricsAdditive: false, EstimatedRows: 8}, components)
	if unsafe.ComponentType == "pie-donut" {
		t.Fatalf("non-additive report metric received share chart: %#v", unsafe)
	}
}

func TestGeneratePlanRejectsUnregisteredComponentMethodAndField(t *testing.T) {
	components := reportAIComponents(t)
	methods := insight.NewRegistry()
	request := PlanRequest{
		Intent: "sales trend", AllowedFieldNames: []string{"month", "sales_amount"},
		AllowedComponents: []string{"line-trend"}, AllowedMethods: []insight.AnalysisMethod{insight.AnalysisTrend},
		TemplateVersions: []string{"1.0.0"},
	}
	for name, mutate := range map[string]func(*Plan){
		"component": func(plan *Plan) { plan.Sections[0].Blocks[0].RecommendedComponent = "unknown-chart" },
		"method":    func(plan *Plan) { plan.Sections[0].Blocks[0].AnalysisMethods[0] = "UNKNOWN_METHOD" },
		"field":     func(plan *Plan) { plan.Sections[0].Blocks[0].DataRoles.Measures[0] = "secret_field" },
	} {
		t.Run(name, func(t *testing.T) {
			plan := validReportPlan()
			mutate(&plan)
			_, err := GeneratePlan(context.Background(), planGeneratorFunc(func(context.Context, PlanRequest) (Plan, error) {
				return plan, nil
			}), request, components, methods)
			if err == nil {
				t.Fatal("invalid plan was accepted")
			}
		})
	}
}

func TestValidatePlanRejectsUnapprovedReportTemplateVersion(t *testing.T) {
	components := reportAIComponents(t)
	methods := insight.NewRegistry()
	plan := validReportPlan()
	request := PlanRequest{
		Intent: "sales trend", AllowedFieldNames: []string{"month", "sales_amount"},
		AllowedComponents: []string{"line-trend"}, AllowedMethods: []insight.AnalysisMethod{insight.AnalysisTrend},
		TemplateVersions: []string{"2.0.0"},
	}
	if err := ValidatePlan(plan, request, components, methods); err == nil {
		t.Fatal("unapproved report template version was accepted")
	}
}

func TestScopedEditContextIsMinimalAndPreviewDoesNotMutateDefinition(t *testing.T) {
	definition := reportAIBaseDefinition(t)
	components := reportAIComponents(t)
	pageID := definition.Pages[0].ID
	scope := operation.Scope{PageID: &pageID}
	before, _ := json.Marshal(definition)
	runID := askdata.ID("ai-run-preview")
	seen := false
	preview, err := PreviewScopedEdit(context.Background(), scopedGeneratorFunc(func(_ context.Context, bounded ScopedContext) (operation.Bundle, error) {
		seen = !strings.Contains(string(bounded.Selection), `"metadata"`) &&
			!strings.Contains(string(bounded.Selection), `"dataContexts"`) &&
			!strings.Contains(string(bounded.Selection), "secret_field") && bounded.AIRunID == runID
		return operation.Bundle{
			SchemaVersion: operation.SchemaVersion, ReportID: definition.Metadata.ID, BaseRevision: 7,
			Source: operation.SourceAI, AIRunID: &runID, Scope: &scope,
			Operations: []operation.Operation{{
				Op: operation.PageUpdate, TargetID: pageID, Payload: &operation.PageUpdatePayload{Name: "AI preview"},
			}},
		}, nil
	}), definition, definition.Metadata.ID, scope, 7, "rename page", []string{"month"}, components, runID)
	if err != nil || !seen || preview.BeforeHash == preview.AfterHash {
		t.Fatalf("PreviewScopedEdit() = %#v, seen=%t, err=%v", preview, seen, err)
	}
	after, _ := json.Marshal(definition)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("preview mutated the input definition")
	}
}

func TestScopedEditRejectsWrongRunTooManyOutOfScopeAndForbiddenOperations(t *testing.T) {
	definition := reportAIBaseDefinition(t)
	components := reportAIComponents(t)
	pageID := definition.Pages[0].ID
	scope := operation.Scope{PageID: &pageID}
	runID := askdata.ID("ai-run-expected")
	base := operation.Bundle{
		SchemaVersion: operation.SchemaVersion, ReportID: definition.Metadata.ID, BaseRevision: 7,
		Source: operation.SourceAI, AIRunID: &runID, Scope: &scope,
		Operations: []operation.Operation{{Op: operation.PageUpdate, TargetID: pageID, Payload: &operation.PageUpdatePayload{Name: "preview"}}},
	}
	tests := map[string]func(*operation.Bundle){
		"wrong run": func(bundle *operation.Bundle) { other := askdata.ID("ai-run-other"); bundle.AIRunID = &other },
		"too many": func(bundle *operation.Bundle) {
			bundle.Operations = make([]operation.Operation, operation.MaxAIOperations+1)
			for index := range bundle.Operations {
				bundle.Operations[index] = operation.Operation{Op: operation.PageUpdate, TargetID: pageID, Payload: &operation.PageUpdatePayload{Name: "preview"}}
			}
		},
		"out of scope": func(bundle *operation.Bundle) {
			sectionID := definition.Pages[0].Sections[0].ID
			blockID := definition.Pages[0].Sections[0].Blocks[0].ID
			bundle.Scope = &operation.Scope{PageID: &pageID, SectionID: &sectionID, BlockID: &blockID}
			bundle.Operations[0].TargetID = pageID
		},
		"forbidden": func(bundle *operation.Bundle) {
			bundle.Operations[0] = operation.Operation{Op: operation.PageDelete, TargetID: pageID, Payload: &operation.PageDeletePayload{}}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			bundle := base
			bundle.Operations = append([]operation.Operation(nil), base.Operations...)
			mutate(&bundle)
			_, err := PreviewScopedEdit(context.Background(), scopedGeneratorFunc(func(context.Context, ScopedContext) (operation.Bundle, error) {
				return bundle, nil
			}), definition, definition.Metadata.ID, scope, 7, "edit", []string{"month"}, components, runID)
			if err == nil {
				t.Fatal("unsafe AI bundle was accepted")
			}
		})
	}
}

func validReportPlan() Plan {
	return Plan{ReportTemplateVersion: "1.0.0", Sections: []PlanSection{{
		Title: "Trend", Purpose: "Explain trend", Blocks: []PlanBlock{{
			Purpose: "Monthly sales", RecommendedComponent: "line-trend",
			DataRoles:       PlanDataRoles{Dimensions: []string{"month"}, Measures: []string{"sales_amount"}},
			AnalysisMethods: []insight.AnalysisMethod{insight.AnalysisTrend},
			DesktopHint:     DesktopHint{W: 12, H: 6}, MobileHint: MobileHint{Order: 1},
		}},
	}}}
}

func reportAIBaseDefinition(t *testing.T) report.ReportDefinition {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "..", "..", "..", "api", "examples", "report-definition", "simple-report.json"))
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

func reportAIComponents(t *testing.T) *template.Registry {
	t.Helper()
	registry, err := template.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
