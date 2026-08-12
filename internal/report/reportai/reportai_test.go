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

type contextSelectorFunc func(context.Context, DataContextSelectionRequest) (DataContextSelection, error)

func (function contextSelectorFunc) SelectDataContext(ctx context.Context, request DataContextSelectionRequest) (DataContextSelection, error) {
	return function(ctx, request)
}

type publishReviewerFunc func(context.Context, PublishReviewRequest) (PublishReview, error)

func (function publishReviewerFunc) ReviewPublication(ctx context.Context, request PublishReviewRequest) (PublishReview, error) {
	return function(ctx, request)
}

func TestPublicationReviewCannotOverrideDeterministicGates(t *testing.T) {
	request := PublishReviewRequest{
		ReportTitle: "月度经营报告", SourceRevisionNo: 24, TargetVersionNo: 13,
		DefinitionHash: strings.Repeat("a", 64), Gates: []PublishGateSummary{
			{ID: "SEMANTIC", Label: "口径与语义", Status: "PASSED"},
			{ID: "FRESHNESS", Label: "数据新鲜度", Status: "PASSED"},
			{ID: "PERMISSION", Label: "权限泄漏", Status: "PASSED"},
			{ID: "EXECUTION", Label: "组件可执行性", Status: "PASSED"},
			{ID: "RESPONSIVE", Label: "移动端适配", Status: "PASSED"},
			{ID: "FACT", Label: "事实与结论核验", Status: "WARNING", IssueCodes: []string{"REPORT_INSIGHT_STALE"}},
		}, WarningCodes: []string{"REPORT_INSIGHT_STALE"},
	}
	valid := PublishReview{Recommendation: "CONDITIONAL", Headline: "建议有条件发布", Summary: "存在一项需人工确认的风险。", Risks: []PublishRisk{{
		Code: "REPORT_INSIGHT_STALE", Title: "结论待确认", Explanation: "结论证据已过期。",
		Evidence: "确定性门禁返回 REPORT_INSIGHT_STALE。", SuggestedAction: "重新生成或显式确认。",
	}}}
	got, err := ReviewPublication(context.Background(), publishReviewerFunc(func(context.Context, PublishReviewRequest) (PublishReview, error) {
		return valid, nil
	}), request)
	if err != nil || got.Recommendation != "CONDITIONAL" {
		t.Fatalf("expected controlled conditional review, got %#v, %v", got, err)
	}
	_, err = ReviewPublication(context.Background(), publishReviewerFunc(func(context.Context, PublishReviewRequest) (PublishReview, error) {
		invalid := valid
		invalid.Recommendation = "ALLOW"
		return invalid, nil
	}), request)
	if err == nil {
		t.Fatal("expected deterministic warning to reject an AI ALLOW recommendation")
	}
}

// 未配置模型提供方时平台仍必须可发布：确定性评审要给出与门禁完全一致的裁决，
// 为每个问题码生成一条风险，并明确标注结论来源不是模型。
func TestDeterministicPublishReviewMatchesTheGatesWithoutAModel(t *testing.T) {
	gates := []PublishGateSummary{
		{ID: "SEMANTIC", Label: "口径与语义", Status: "PASSED"},
		{ID: "FRESHNESS", Label: "数据新鲜度", Status: "PASSED"},
		{ID: "PERMISSION", Label: "权限泄漏", Status: "PASSED"},
		{ID: "EXECUTION", Label: "组件可执行性", Status: "PASSED"},
		{ID: "RESPONSIVE", Label: "移动端适配", Status: "PASSED"},
		{ID: "FACT", Label: "事实与结论核验", Status: "WARNING",
			IssueCodes: []string{"REPORT_INSIGHT_STALE"}, Summary: "结论证据已过期。"},
	}
	base := PublishReviewRequest{
		ReportTitle: "月度经营报告", SourceRevisionNo: 24, TargetVersionNo: 13,
		DefinitionHash: strings.Repeat("a", 64), Gates: gates,
	}

	clean := DeterministicPublishReview(base)
	if clean.Recommendation != "ALLOW" || clean.Source != PublishReviewSourceDeterministic || len(clean.Risks) != 0 {
		t.Fatalf("expected a clean deterministic ALLOW review, got %#v", clean)
	}

	conditional := base
	conditional.WarningCodes = []string{"REPORT_INSIGHT_STALE"}
	warned := DeterministicPublishReview(conditional)
	if warned.Recommendation != "CONDITIONAL" || len(warned.Risks) != 1 ||
		warned.Risks[0].Code != "REPORT_INSIGHT_STALE" {
		t.Fatalf("expected one deterministic risk per warning code, got %#v", warned)
	}
	if !strings.Contains(warned.Risks[0].Explanation, "结论证据已过期") {
		t.Fatalf("expected the risk to quote the deterministic gate summary, got %q", warned.Risks[0].Explanation)
	}

	blocked := base
	blocked.BlockerCodes = []string{"REPORT_DEPENDENCY_INVALID"}
	blocked.WarningCodes = []string{"REPORT_INSIGHT_STALE"}
	stopped := DeterministicPublishReview(blocked)
	if stopped.Recommendation != "BLOCK" || len(stopped.Risks) != 2 {
		t.Fatalf("expected blockers to force BLOCK with a risk per code, got %#v", stopped)
	}

	// 确定性评审必须满足与模型评审同一套结构约束，二者对发布链路完全等价。
	if err := validatePublishReview(blocked, stopped); err != nil {
		t.Fatalf("deterministic review must satisfy the AI review contract: %v", err)
	}
}

func TestSelectDataContextCannotEscapeGovernedCandidates(t *testing.T) {
	definition := reportAIBaseDefinition(t)
	candidate := DataContextCandidate{DataContext: definition.DataContexts[0], Name: "Sales", Fields: []string{"month", "sales_amount"}}
	request := DataContextSelectionRequest{Intent: "sales report", Candidates: []DataContextCandidate{candidate}}
	valid := DataContextSelection{DataContextID: candidate.DataContext.ID, ReportName: "Sales report", Rationale: "Matches the requested subject", Confidence: "HIGH"}
	selection, err := SelectDataContext(context.Background(), contextSelectorFunc(func(context.Context, DataContextSelectionRequest) (DataContextSelection, error) {
		return valid, nil
	}), request)
	if err != nil || selection.DataContextID != candidate.DataContext.ID {
		t.Fatalf("valid selection = %#v, err=%v", selection, err)
	}
	_, err = SelectDataContext(context.Background(), contextSelectorFunc(func(context.Context, DataContextSelectionRequest) (DataContextSelection, error) {
		escaped := valid
		escaped.DataContextID = "secret_context"
		return escaped, nil
	}), request)
	if err == nil {
		t.Fatal("selector escaped the governed candidate allowlist")
	}
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

// A planned block that asks for analysis becomes a composite card: the chart in
// a content zone and its conclusion in an insight zone of the same card.
func TestAnalysisBlockBecomesACompositeCard(t *testing.T) {
	base := reportAIBaseDefinition(t)
	definition, err := Instantiate(validReportPlan(), InstantiateInput{
		Base: base, DataContextID: base.DataContexts[0].ID,
		AllowedFields: []string{"month", "sales_amount"},
	}, reportAIComponents(t), insight.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	var card *report.Block
	for pageIndex := range definition.Pages {
		for sectionIndex := range definition.Pages[pageIndex].Sections {
			for blockIndex := range definition.Pages[pageIndex].Sections[sectionIndex].Blocks {
				block := &definition.Pages[pageIndex].Sections[sectionIndex].Blocks[blockIndex]
				if len(block.Zones) > 1 {
					card = block
				}
			}
		}
	}
	if card == nil {
		t.Fatal("an analysed block must produce a card with more than one zone")
	}
	if card.Type != report.BlockAnalysisCard {
		t.Fatalf("a card carrying a conclusion is an analysis card, got %s", card.Type)
	}

	var content, conclusion *report.Zone
	for index := range card.Zones {
		switch card.Zones[index].Type {
		case report.ZoneContent:
			content = &card.Zones[index]
		case report.ZoneInsight:
			conclusion = &card.Zones[index]
		}
	}
	if content == nil || conclusion == nil {
		t.Fatalf("expected a content zone and an insight zone, got %#v", card.Zones)
	}
	// The content zone absorbs the card's remaining height; the conclusion keeps
	// only what its text needs.
	if content.Layout.HeightMode != report.ZoneHeightFR || content.Layout.FR == nil {
		t.Fatalf("content zone must share remaining height: %#v", content.Layout)
	}
	// Normalization reorders zones. Decoding canonical JSON into an already
	// populated definition used to leave this zone holding the content zone's
	// fr, which then failed validation. Each zone must keep its own layout.
	if conclusion.Layout.HeightMode != report.ZoneHeightAuto || conclusion.Layout.FR != nil {
		t.Fatalf("insight zone must not inherit another zone's height: %#v", conclusion.Layout)
	}

	// The conclusion is bound to the same query it describes, which is what
	// evidence derivation needs, and it ships with no prose until one is
	// generated and verified.
	componentID := conclusion.Slots[0].ComponentID
	var conclusionComponent *report.Component
	for index := range definition.Components {
		if definition.Components[index].ID == componentID {
			conclusionComponent = &definition.Components[index]
		}
	}
	if conclusionComponent == nil || conclusionComponent.DataBinding == nil ||
		len(conclusionComponent.DataBinding.Measures) == 0 {
		t.Fatalf("the conclusion must bind the measure it talks about: %#v", conclusionComponent)
	}
	if conclusionComponent.Options.RichText != "" {
		t.Fatal("the planner must not author prose that has not been verified")
	}
}
