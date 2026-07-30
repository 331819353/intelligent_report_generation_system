package semanticqa

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

type recordingDWSInvoker struct {
	invocations []aiplatform.Invocation
}

func TestBuildADSCandidatePinsOnePublishedDWS(t *testing.T) {
	sourceVersionID := "2470617f-c71d-493d-93d5-9d67ac327e79"
	argument := dataset.Expression{
		Type: "FIELD_REF", NodeID: "fact", Field: "amount",
	}
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dws_sales_trend", Name: "销售趋势",
			Domain: "销售", Subject: "趋势",
			Type: "CROSS_SOURCE", Layer: dataset.LayerDWS,
		},
		Nodes: []dataset.Node{{
			ID: "fact", Type: "DATASET",
			DatasetVersionID: "f0206e2a-a8d7-45c6-9e98-ec8ca53c0565",
			Alias:            "fact", Projection: []string{"stat_month", "amount"},
			SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{},
		Fields: []dataset.Field{
			{
				ID: "field_stat_month", Code: "stat_month", Name: "统计月份",
				Role: "TIME", CanonicalType: "DATE",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: "stat_month",
				},
			},
			{
				ID: "field_amount", Code: "amount", Name: "销售额",
				Role: "MEASURE", CanonicalType: "DECIMAL",
				Expression: dataset.Expression{
					Type: "AGGREGATE", Function: "SUM", Argument: &argument,
				},
			},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{"field_stat_month"},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一个统计月份", KeyFields: []string{"stat_month"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal source DWS: %v", err)
	}
	preparedSource, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare source DWS: %#v", err)
	}
	prepared, err := buildADSCandidate(dataset.Record{
		ID: "source", Code: "dws_sales_trend", Name: "销售趋势",
		Layer: dataset.LayerDWS, DSL: preparedSource.DSLJSON,
	}, sourceVersionID)
	if err != nil {
		t.Fatalf("build ADS: %v", err)
	}
	if prepared.Document.Dataset.Layer != dataset.LayerADS ||
		prepared.Document.Dataset.Code != "ads_sales_trend" ||
		prepared.Document.Dataset.Type != "CROSS_SOURCE" {
		t.Fatalf("unexpected ADS descriptor: %#v", prepared.Document.Dataset)
	}
	if len(prepared.Document.Nodes) != 1 ||
		prepared.Document.Nodes[0].DatasetVersionID != sourceVersionID {
		t.Fatalf("unexpected ADS inputs: %#v", prepared.Document.Nodes)
	}
	if prepared.Document.Fields[1].Expression.Type != "FIELD_REF" {
		t.Fatalf("ADS must project the DWS field: %#v", prepared.Document.Fields[1])
	}
}

func (invoker *recordingDWSInvoker) Configured() bool {
	return true
}

func (invoker *recordingDWSInvoker) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	invoker.invocations = append(invoker.invocations, invocation)
	return aiplatform.InvocationResult{
		RequestID: "request",
		ProviderResult: aiplatform.ProviderResult{
			Content: []byte(`{"selections":[{
				"templateCode":"TREND",
				"dimensionCodes":[],
				"metricCodes":[]
			}]}`),
		},
	}, nil
}

func TestDWSSelectorUsesPerFactPromptForSingleDWD(t *testing.T) {
	invoker := &recordingDWSInvoker{}
	selector := NewOrchestratedDWSAnalysisSelector(invoker)
	fact := dwsPlanningAsset{
		Record: dataset.Record{
			ID: "fact_dataset", Code: "dwd_order", Name: "订单明细",
		},
		VersionID: "fact_version",
		Document: dataset.Document{
			Fields: []dataset.Field{{
				Code: "order_date", Role: "TIME", CanonicalType: "DATE",
			}},
			FactContract: &dataset.FactContract{
				BusinessAction: "下单", EventTimeField: "order_date",
			},
		},
	}
	selected, _, err := selector.Select(
		context.Background(), "tenant", "actor", "scope",
		dwsModelingScope{
			GroupKey:   "single-dwd:fact_dataset",
			DomainCode: "operations", SubjectCode: "order",
			SubjectName: "订单分析",
		},
		[]dwsPlanningAsset{fact}, nil, []string{"TREND"},
	)
	if err != nil {
		t.Fatalf("select single DWD: %v", err)
	}
	if len(selected) != 1 || selected[0].TemplateCode != "TREND" {
		t.Fatalf("selected templates = %#v", selected)
	}
	if len(invoker.invocations) != 1 ||
		invoker.invocations[0].PromptVersion != dwsSingleFactPlanningVersion {
		t.Fatalf("invocations = %#v", invoker.invocations)
	}
}

func TestBuildDimensionCountDWSCandidateHasOneMetric(t *testing.T) {
	sourceVersionID := "2470617f-c71d-493d-93d5-9d67ac327e79"
	visible := true
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dim_employee", Name: "员工",
			Domain: "人力资源", Subject: "员工",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDIM,
		},
		Nodes: []dataset.Node{{
			ID: "source", Type: "DATASET",
			DatasetVersionID: "f0206e2a-a8d7-45c6-9e98-ec8ca53c0565",
			Alias:            "source", Projection: []string{"employee_id", "department"},
			SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{},
		Fields: []dataset.Field{
			{
				ID: "field_employee_id", Code: "employee_id", Name: "员工标识",
				Role: "IDENTIFIER", CanonicalType: "STRING", Visible: &visible,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source", Field: "employee_id",
				},
			},
			{
				ID: "field_department", Code: "department", Name: "部门",
				Role: "ATTRIBUTE", CanonicalType: "STRING", Visible: &visible,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source", Field: "department",
				},
			},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一个员工",
			KeyFields:   []string{"employee_id"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal DIM: %v", err)
	}
	preparedSource, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare DIM: %#v", err)
	}
	prepared, err := buildDimensionCountDWSCandidate(dataset.Record{
		ID: "source", Code: "dim_employee", Name: "员工",
		Layer: dataset.LayerDIM, DSL: preparedSource.DSLJSON,
	}, sourceVersionID, []string{"department"})
	if err != nil {
		t.Fatalf("build DIM count DWS: %#v", err)
	}
	if prepared.Document.Dataset.Layer != dataset.LayerDWS ||
		prepared.Document.AnalysisContract == nil ||
		prepared.Document.AnalysisContract.Intent != "ENTITY_COUNT" {
		t.Fatalf("unexpected factless DWS: %#v", prepared.Document)
	}
	measureCount := 0
	for _, field := range prepared.Document.Fields {
		if field.Role == "MEASURE" {
			measureCount++
			if field.Code != "entity_count" ||
				field.Expression.Function != "COUNT_DISTINCT" {
				t.Fatalf("unexpected entity count metric: %#v", field)
			}
		}
	}
	if measureCount != 1 {
		t.Fatalf("measure count = %d", measureCount)
	}
}

func TestDWSSelectorKeepsExplicitMultiFactPrompt(t *testing.T) {
	invoker := &recordingDWSInvoker{}
	selector := NewOrchestratedDWSAnalysisSelector(invoker)
	facts := []dwsPlanningAsset{
		{
			Record:    dataset.Record{ID: "fact_a", Code: "dwd_a", Name: "事实 A"},
			VersionID: "version_a",
		},
		{
			Record:    dataset.Record{ID: "fact_b", Code: "dwd_b", Name: "事实 B"},
			VersionID: "version_b",
		},
	}
	_, _, err := selector.Select(
		context.Background(), "tenant", "actor", "scope",
		dwsModelingScope{GroupKey: "explicit-multi-fact"},
		facts, nil, []string{"MULTI_FACT_COMPARISON"},
	)
	if err != nil {
		t.Fatalf("select explicit multi-fact DWS: %v", err)
	}
	if len(invoker.invocations) != 1 ||
		invoker.invocations[0].PromptVersion !=
			dwsGroupedFactPlanningVersion {
		t.Fatalf("invocations = %#v", invoker.invocations)
	}
}

func TestSingleFactWithoutTimeKeepsDimensionTemplates(t *testing.T) {
	document := dataset.Document{
		Dataset: dataset.Descriptor{Layer: dataset.LayerDWD},
		Fields: []dataset.Field{
			{Code: "item_id", Role: "IDENTIFIER", CanonicalType: "INTEGER"},
			{Code: "category", Role: "DIMENSION", CanonicalType: "STRING"},
			{Code: "amount", Role: "MEASURE", CanonicalType: "DECIMAL"},
		},
		OutputGrain: dataset.OutputGrain{KeyFields: []string{"item_id"}},
		FactContract: &dataset.FactContract{
			GrainKeyFields: []string{"item_id"},
			AtomicMeasures: []dataset.AtomicMeasureContract{{
				Field: "amount", Additivity: "ADDITIVE",
			}},
		},
	}
	_, eligible := eligibleDWSAnalysisScope(
		[]dwsPlanningAsset{{Document: document}}, false,
	)
	want := []string{"DISTRIBUTION", "RANKING", "DRILLDOWN"}
	if !slices.Equal(eligible, want) {
		t.Fatalf("eligible templates = %#v, want %#v", eligible, want)
	}
}

func TestBuildSingleFactEventFallsBackToSafeRecordCount(t *testing.T) {
	sourceVersionID := "2470617f-c71d-493d-93d5-9d67ac327e79"
	visible := true
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dwd_delivery_event", Name: "配送事件明细",
			Domain: "企业经营", Subject: "经营分析",
			Type: "CROSS_SOURCE", Layer: dataset.LayerDWD,
			SemanticContractVersion: "1.0",
		},
		Nodes: []dataset.Node{{
			ID: "source", Type: "DATASET",
			DatasetVersionID: "f0206e2a-a8d7-45c6-9e98-ec8ca53c0565",
			Alias:            "source", Projection: []string{
				"event_id", "event_date", "event_type",
			},
			SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{},
		Fields: []dataset.Field{
			{
				ID: "field_event_id", Code: "event_id", Name: "事件 ID",
				Role: "IDENTIFIER", CanonicalType: "INTEGER", Visible: &visible,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source", Field: "event_id",
				},
			},
			{
				ID: "field_event_date", Code: "event_date", Name: "事件日期",
				Role: "TIME", CanonicalType: "DATE", Visible: &visible,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source", Field: "event_date",
				},
			},
			{
				ID: "field_event_type", Code: "event_type", Name: "事件类型",
				Role: "DIMENSION", CanonicalType: "STRING", Visible: &visible,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: "source", Field: "event_type",
				},
			},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一个配送事件",
			KeyFields:   []string{"event_id"}, TimeField: "event_date",
			DefaultTimeGrain: "DAY",
		},
		FactContract: &dataset.FactContract{
			BusinessAction: "配送事件",
			GrainKeyFields: []string{"event_id"},
			EventTimeField: "event_date", AtomicMeasures: []dataset.AtomicMeasureContract{},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal event fact: %v", err)
	}
	preparedSource, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare event fact: %#v", err)
	}
	prepared, err := buildSingleFactDWSCandidate(
		dataset.Record{
			ID: "source", Code: document.Dataset.Code, Name: document.Dataset.Name,
			Layer: dataset.LayerDWD, DSL: preparedSource.DSLJSON,
		},
		sourceVersionID, "TREND",
	)
	if err != nil {
		t.Fatalf("build event DWS: %#v", err)
	}
	if prepared.Document.AnalysisContract == nil ||
		len(prepared.Document.AnalysisContract.Measures) != 1 ||
		prepared.Document.AnalysisContract.Measures[0].Field !=
			dwsRecordCountMetricCode {
		t.Fatalf(
			"unexpected event measures: %#v",
			prepared.Document.AnalysisContract,
		)
	}
	if prepared.Document.Dataset.Type != "CROSS_SOURCE" {
		t.Fatalf("event DWS type = %q", prepared.Document.Dataset.Type)
	}
	var countField *dataset.Field
	for index := range prepared.Document.Fields {
		if prepared.Document.Fields[index].Code == dwsRecordCountMetricCode {
			countField = &prepared.Document.Fields[index]
			break
		}
	}
	if countField == nil || countField.Expression.Function != "COUNT" ||
		countField.Expression.Argument != nil {
		t.Fatalf("unexpected record count field: %#v", countField)
	}
}

func TestExplicitMultiFactScopeNeverDropsAnIneligibleFact(t *testing.T) {
	eligibleDocument := dataset.Document{
		Dataset: dataset.Descriptor{Layer: dataset.LayerDWD},
		Fields: []dataset.Field{
			{Code: "event_id", Role: "IDENTIFIER", CanonicalType: "INTEGER"},
			{Code: "event_date", Role: "TIME", CanonicalType: "DATE"},
		},
		OutputGrain: dataset.OutputGrain{
			KeyFields: []string{"event_id"}, TimeField: "event_date",
		},
		FactContract: &dataset.FactContract{
			GrainKeyFields: []string{"event_id"}, EventTimeField: "event_date",
		},
	}
	ineligibleDocument := eligibleDocument
	ineligibleDocument.Fields = []dataset.Field{{
		Code: "snapshot_id", Role: "IDENTIFIER", CanonicalType: "INTEGER",
	}}
	ineligibleDocument.OutputGrain = dataset.OutputGrain{
		KeyFields: []string{"snapshot_id"},
	}
	ineligibleDocument.FactContract = &dataset.FactContract{
		GrainKeyFields: []string{"snapshot_id"},
	}
	modelingFacts, eligible := eligibleDWSAnalysisScope(
		[]dwsPlanningAsset{
			{Document: eligibleDocument},
			{Document: ineligibleDocument},
		},
		false,
	)
	if len(modelingFacts) != 0 || len(eligible) != 0 {
		t.Fatalf(
			"partial multi-fact scope must be rejected, facts=%d templates=%#v",
			len(modelingFacts), eligible,
		)
	}
}

func TestUnchangedDWSOutputsRemainSuccessful(t *testing.T) {
	if status := dwsModelingCompletionStatus(0, 0, 3, 3); status != "SUCCEEDED" {
		t.Fatalf("unchanged status = %q", status)
	}
	if status := dwsModelingCompletionStatus(1, 0, 2, 1); status != "PARTIAL" {
		t.Fatalf("mixed status = %q", status)
	}
	if status := dwsModelingCompletionStatus(0, 0, 2, 0); status != "SKIPPED" {
		t.Fatalf("failed status = %q", status)
	}
}

func TestSelectedDWSScopeGetsStableUniqueCode(t *testing.T) {
	base := "dws_enterprise_selected_multi_fact_summary"
	first := scopedDWSCode(base, "selected-dws:first")
	second := scopedDWSCode(base, "selected-dws:second")
	if first == base || first == second || len(first) > 63 || len(second) > 63 {
		t.Fatalf("scoped codes = %q, %q", first, second)
	}
	if repeated := scopedDWSCode(base, "selected-dws:first"); repeated != first {
		t.Fatalf("scoped code is unstable: %q != %q", repeated, first)
	}
}

func TestGeneratedDWSFieldCodesFitPostgreSQLIdentifiers(t *testing.T) {
	long := "general_business_analysis_merchant_daily_ops_" +
		"completed_order_count"
	first := boundedDWSFieldCode(long)
	second := boundedDWSFieldCode(long + "_different")
	if len(first) > 63 || len(second) > 63 ||
		first == second || first != boundedDWSFieldCode(long) {
		t.Fatalf("bounded field codes = %q, %q", first, second)
	}
}

func TestDefaultDWSSelectionConsolidatesDimensionsAtOnePhysicalGrain(t *testing.T) {
	document := dataset.Document{
		Dataset: dataset.Descriptor{Layer: dataset.LayerDWD},
		Fields: []dataset.Field{
			{Code: "region_code", Role: "DIMENSION"},
			{Code: "channel_code", Role: "DIMENSION"},
			{Code: "amount", Role: "MEASURE"},
		},
		OutputGrain: dataset.OutputGrain{KeyFields: []string{"order_id"}},
		FactContract: &dataset.FactContract{
			GrainKeyFields: []string{"order_id"},
			AtomicMeasures: []dataset.AtomicMeasureContract{{
				Field: "amount", Additivity: "ADDITIVE",
				ValueBehavior: "FLOW", DefaultAggregation: "SUM",
				TimeAggregation: "SUM", NullPolicy: "PRESERVE",
			}},
		},
	}
	selections := consolidatedDWSSelection(
		[]string{"DISTRIBUTION", "RANKING", "DRILLDOWN"}, document,
	)
	if len(selections) != 1 ||
		selections[0].TemplateCode != "DRILLDOWN" ||
		selections[0].GroupingMode != "STANDARD" ||
		!slices.Equal(
			selections[0].DimensionCodes,
			[]string{"region_code", "channel_code"},
		) {
		t.Fatalf("consolidated selection = %#v", selections)
	}
}

func TestLegacyAdditiveMeasureIsCorrectedAtDWSBoundary(t *testing.T) {
	contract := effectiveDWSAtomicMeasure(
		dataset.AtomicMeasureContract{
			Field: "sales_ytd", Additivity: "ADDITIVE",
			NullPolicy: "PRESERVE",
		},
		dataset.Field{
			Code: "sales_ytd", Name: "本年累计销售额",
			Role: "MEASURE", CanonicalType: "DECIMAL",
		},
	)
	if contract.ValueBehavior != "CUMULATIVE" ||
		contract.Additivity != "SEMI_ADDITIVE" ||
		contract.TimeAggregation != "LAST" {
		t.Fatalf("legacy cumulative contract = %#v", contract)
	}
}

func TestPointInTimeDWSUsesOnePhysicalGrain(t *testing.T) {
	sourceVersionID := "2470617f-c71d-493d-93d5-9d67ac327e79"
	upstreamVersionID := "f0206e2a-a8d7-45c6-9e98-ec8ca53c0565"
	visible := true
	fields := []dataset.Field{
		dwsTestSourceField(
			"snapshot_id", "快照标识", "IDENTIFIER", "INTEGER",
			upstreamVersionID, &visible,
		),
		dwsTestSourceField(
			"snapshot_date", "快照日期", "TIME", "DATE",
			upstreamVersionID, &visible,
		),
		dwsTestSourceField(
			"region_code", "区域", "DIMENSION", "STRING",
			upstreamVersionID, &visible,
		),
		dwsTestSourceField(
			"channel_code", "渠道", "DIMENSION", "STRING",
			upstreamVersionID, &visible,
		),
		dwsTestSourceField(
			"ending_balance", "期末余额", "MEASURE", "DECIMAL",
			upstreamVersionID, &visible,
		),
	}
	projection := []string{
		"snapshot_id", "snapshot_date", "region_code", "channel_code",
		"ending_balance",
	}
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dwd_account_snapshot", Name: "账户快照明细",
			Domain: "财务", Subject: "账户",
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDWD,
			SemanticContractVersion: "1.0",
		},
		Nodes: []dataset.Node{{
			ID: "source", Type: "DATASET", DatasetVersionID: upstreamVersionID,
			Alias: "source", Projection: projection,
			SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{}, Fields: fields,
		Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一个账户日快照",
			KeyFields:   []string{"snapshot_id"}, TimeField: "snapshot_date",
			DefaultTimeGrain: "DAY",
		},
		FactContract: &dataset.FactContract{
			BusinessAction: "账户日快照",
			GrainKeyFields: []string{"snapshot_id"},
			EventTimeField: "snapshot_date",
			AtomicMeasures: []dataset.AtomicMeasureContract{{
				Field: "ending_balance", Additivity: "SEMI_ADDITIVE",
				ValueBehavior: "POINT_IN_TIME", DefaultAggregation: "SUM",
				TimeAggregation: "LAST", NullPolicy: "PRESERVE",
			}},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 1000,
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	preparedSource, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare point-in-time DWD: %#v", err)
	}
	prepared, err := buildSingleFactDWSCandidateWithSelection(
		dataset.Record{
			ID: "source", Code: document.Dataset.Code, Name: document.Dataset.Name,
			Layer: dataset.LayerDWD, DSL: preparedSource.DSLJSON,
		},
		sourceVersionID, "DRILLDOWN",
		[]string{"region_code", "channel_code"},
		[]string{"ending_balance"}, "CUBE", nil,
	)
	if err != nil {
		t.Fatalf("build point-in-time DWS: %#v", err)
	}
	if prepared.Document.GroupByMode != dataset.GroupByModeStandard ||
		len(prepared.Document.GroupingSets) != 0 ||
		prepared.Document.AnalysisContract.TimeField != "stat_date" ||
		prepared.Document.AnalysisContract.TimeGrain != "DAY" {
		t.Fatalf("point-in-time grouping contract = %#v", prepared.Document)
	}
	if !slices.Contains(prepared.Document.GroupBy, "field_stat_date") {
		t.Fatalf("point-in-time DWS drops snapshot date: %#v", prepared.Document.GroupBy)
	}
	measure := prepared.Document.AnalysisContract.Measures[0]
	if measure.Aggregation != "SUM" ||
		measure.ValueBehavior != "POINT_IN_TIME" ||
		measure.TimeAggregation != "LAST" {
		t.Fatalf("point-in-time measure = %#v", measure)
	}
}

func dwsTestSourceField(
	code, name, role, canonicalType, _ string,
	visible *bool,
) dataset.Field {
	return dataset.Field{
		ID: "field_" + code, Code: code, Name: name, Description: name,
		Role: role, CanonicalType: canonicalType, Visible: visible,
		Expression: dataset.Expression{
			Type: "FIELD_REF", NodeID: "source", Field: code,
		},
	}
}
