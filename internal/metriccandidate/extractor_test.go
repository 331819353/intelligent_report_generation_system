package metriccandidate

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

const (
	testDatasetID        = "11111111-1111-4111-8111-111111111111"
	testDatasetVersionID = "22222222-2222-4222-8222-222222222222"
)

func TestExtractDerivesDeterministicCandidatesFromExplicitFacts(t *testing.T) {
	document := candidateDatasetDocument()
	version := publishedDatasetVersion(t, document)

	first, err := Extract(version)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	second, err := Extract(version)
	if err != nil {
		t.Fatalf("Extract() second error = %v", err)
	}
	if first.Status != TaskStatusPartial {
		t.Fatalf("task status = %s, want %s", first.Status, TaskStatusPartial)
	}
	if len(first.Candidates) != 6 {
		t.Fatalf("candidates = %#v", first.Candidates)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("the same immutable version produced different candidates")
	}

	byDefinition := map[string]CandidateDraft{}
	for _, candidate := range first.Candidates {
		byDefinition[candidate.SourceFieldID+":"+candidate.Definition.Aggregation] = candidate
		raw, marshalErr := json.Marshal(candidate.Definition)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		prepared, prepareErr := metric.Prepare(raw)
		if prepareErr != nil {
			t.Fatalf("candidate %s has an invalid definition: %v", candidate.SourceFieldID, prepareErr)
		}
		if prepared.DefinitionHash != candidate.DefinitionHash || len(candidate.Fingerprint) != 64 || len(candidate.Evidence) == 0 {
			t.Fatalf("candidate %s lacks stable derivation metadata: %#v", candidate.SourceFieldID, candidate)
		}
	}

	assertCandidate(t, byDefinition["field_id:COUNT"], "COUNT", ConfidenceHigh, CandidateStatusReady, "ADDITIVE")
	assertCandidate(t, byDefinition["field_id:COUNT_DISTINCT"], "COUNT_DISTINCT", ConfidenceHigh, CandidateStatusReady, "NON_ADDITIVE")
	assertCandidate(t, byDefinition["field_amount:SUM"], "SUM", ConfidenceHigh, CandidateStatusReady, "ADDITIVE")
	assertCandidate(t, byDefinition["field_quantity:SUM"], "SUM", ConfidenceMedium, CandidateStatusReady, "ADDITIVE")
	assertCandidate(t, byDefinition["field_rate:AVG"], "AVG", ConfidenceMedium, CandidateStatusReady, "NON_ADDITIVE")
	assertCandidate(t, byDefinition["field_score:SUM"], "SUM", ConfidenceLow, CandidateStatusNeedsReview, "ADDITIVE")

	amount := byDefinition["field_amount:SUM"]
	if amount.Definition.TimeFieldID != "field_order_date" || amount.Definition.TimeGrain != "MONTH" {
		t.Fatalf("exact time semantics were not extracted: %#v", amount.Definition)
	}
	gotDimensions := dimensionFieldIDs(amount.Definition.AllowedDimensions)
	wantDimensions := []string{"field_region", "field_order_date"}
	if !reflect.DeepEqual(gotDimensions, wantDimensions) {
		t.Fatalf("dimensions = %v, want %v", gotDimensions, wantDimensions)
	}
	if got := byDefinition["field_id:COUNT_DISTINCT"].Definition.Metric.Name; got != "订单数" {
		t.Fatalf("identifier count name = %q, want 订单数", got)
	}
}

func TestExtractBlocksAggregatedDatasetsAndAggregateExpressions(t *testing.T) {
	document := candidateDatasetDocument()
	document.GroupBy = []string{"field_region"}
	source := document.Fields[0].Expression
	document.Fields[0].Expression = dataset.Expression{Type: "AGGREGATE", Function: "SUM", Argument: &source}
	version := publishedDatasetVersion(t, document)

	result, err := Extract(version)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if result.Status != TaskStatusPartial || len(result.Candidates) == 0 {
		t.Fatalf("unexpected aggregate extraction result: %#v", result)
	}
	for _, candidate := range result.Candidates {
		if candidate.Status != CandidateStatusBlocked {
			t.Fatalf("candidate %s status = %s", candidate.SourceFieldID, candidate.Status)
		}
		if !containsString(candidate.BlockReasons, BlockReasonAggregatedDataset) ||
			!containsString(candidate.BlockReasons, BlockReasonAggregateExpression) {
			t.Fatalf("candidate %s block reasons = %v", candidate.SourceFieldID, candidate.BlockReasons)
		}
	}
}

func TestExtractBlocksPreAggregatedDataset(t *testing.T) {
	document := candidateDatasetDocument()
	document.Nodes = append(document.Nodes, dataset.Node{
		ID: "targets", Type: "TABLE", DataSourceID: "33333333-3333-4333-8333-333333333333",
		TableID: "44444444-4444-4444-8444-444444444444", Alias: "t",
		Projection: []string{"region", "target"}, SourceFilters: []dataset.SourceFilter{},
	})
	document.Joins = []dataset.Join{{
		ID: "join_targets", LeftNodeID: "orders", RightNodeID: "targets", JoinType: "LEFT",
		Cardinality: "MANY_TO_ONE", ManualConfirmed: true,
		Conditions: []dataset.JoinCondition{{
			LeftExpression:  dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "region"},
			Operator:        "EQUALS",
			RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "targets", Field: "region"},
		}},
	}}
	document.PreAggregations = []dataset.PreAggregation{{
		ID: "group_orders", NodeID: "orders", JoinID: "join_targets", JoinSide: "LEFT",
		GroupBy: []dataset.PreAggregationGroup{{Field: "order_id"}, {Field: "region"}, {Field: "order_date"}, {Field: "channel"}},
		Metrics: []dataset.PreAggregationMetric{
			{Field: "amount", Function: "SUM"},
			{Field: "quantity", Function: "SUM"},
			{Field: "rate", Function: "AVG"},
			{Field: "score", Function: "AVG"},
		},
	}}
	version := publishedDatasetVersion(t, document)

	result, err := Extract(version)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	for _, candidate := range result.Candidates {
		if candidate.Status != CandidateStatusBlocked || !containsString(candidate.BlockReasons, BlockReasonPreAggregation) {
			t.Fatalf("pre-aggregation candidate was not blocked: %#v", candidate)
		}
	}
}

func TestExtractRejectsAnythingOtherThanTheExactPublishedEnvelope(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	tests := map[string]func(*dataset.VersionRecord){
		"非发布状态":    func(value *dataset.VersionRecord) { value.Status = "STALE" },
		"非规范数据集标识": func(value *dataset.VersionRecord) { value.DatasetID = "current-dataset" },
		"DSL 摘要漂移": func(value *dataset.VersionRecord) { value.DSLHash = "a" + value.DSLHash[1:] },
		"计划摘要缺失":   func(value *dataset.VersionRecord) { value.PlanHash = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := version
			mutate(&changed)
			result, err := Extract(changed)
			if !errors.Is(err, ErrInvalidDatasetVersion) || result.Status != TaskStatusFailed {
				t.Fatalf("Extract() result=%#v err=%v", result, err)
			}
		})
	}
}

func assertCandidate(t *testing.T, candidate CandidateDraft, aggregation string, confidence Confidence, status CandidateStatus, additivity string) {
	t.Helper()
	if candidate.Definition.Aggregation != aggregation || candidate.Confidence != confidence || candidate.Status != status || candidate.Definition.Additivity != additivity {
		t.Fatalf("candidate %s = aggregation %s, confidence %s, status %s, additivity %s", candidate.SourceFieldID,
			candidate.Definition.Aggregation, candidate.Confidence, candidate.Status, candidate.Definition.Additivity)
	}
}

func candidateDatasetDocument() dataset.Document {
	visible := true
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "orders_detail", Name: "订单明细", Description: "订单明细数据集", Type: "SINGLE_SOURCE",
		},
		Nodes: []dataset.Node{{
			ID: "orders", Type: "TABLE", DataSourceID: "33333333-3333-4333-8333-333333333333",
			TableID: "44444444-4444-4444-8444-444444444444", Alias: "o",
			Projection:    []string{"order_id", "amount", "quantity", "rate", "score", "region", "order_date", "channel"},
			SourceFilters: []dataset.SourceFilter{},
		}},
		Joins:           []dataset.Join{},
		PreAggregations: []dataset.PreAggregation{},
		Fields: []dataset.Field{
			{ID: "field_amount", Code: "amount", Name: "订单金额", Role: "MEASURE", Aggregation: "SUM", Unit: "元", Format: "#,##0.00", SemanticType: "AMOUNT", Expression: fieldRef("amount"), CanonicalType: "DECIMAL", Nullable: false, Visible: &visible},
			{ID: "field_quantity", Code: "quantity", Name: "商品数量", Role: "ATTRIBUTE", SemanticType: "QUANTITY", Expression: fieldRef("quantity"), CanonicalType: "INTEGER", Nullable: false, Visible: &visible},
			{ID: "field_rate", Code: "rate", Name: "转化率", Role: "ATTRIBUTE", SemanticType: "PERCENTAGE", Expression: fieldRef("rate"), CanonicalType: "DECIMAL", Nullable: true, Visible: &visible},
			{ID: "field_score", Code: "score", Name: "评分", Role: "ATTRIBUTE", Expression: fieldRef("score"), CanonicalType: "DECIMAL", Nullable: true, Visible: &visible},
			{ID: "field_region", Code: "region", Name: "地区", Role: "DIMENSION", Expression: fieldRef("region"), CanonicalType: "STRING", Nullable: true, Visible: &visible},
			{ID: "field_order_date", Code: "order_date", Name: "下单日期", Role: "TIME", Expression: fieldRef("order_date"), CanonicalType: "DATE", Nullable: false, Visible: &visible},
			{ID: "field_channel", Code: "channel", Name: "渠道", Role: "ATTRIBUTE", Expression: fieldRef("channel"), CanonicalType: "STRING", Nullable: true, Visible: &visible},
			{ID: "field_id", Code: "order_id", Name: "订单编号", Role: "IDENTIFIER", Expression: fieldRef("order_id"), CanonicalType: "STRING", Nullable: false, Visible: &visible},
		},
		Filters:    []dataset.Filter{},
		GroupBy:    []string{},
		Having:     []dataset.Filter{},
		Sorts:      []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一笔订单", KeyFields: []string{"order_id"},
			TimeField: "order_date", DefaultTimeGrain: "MONTH",
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 200, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{Enabled: false},
		},
	}
}

func fieldRef(field string) dataset.Expression {
	return dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: field}
}

func publishedDatasetVersion(t *testing.T, document dataset.Document) dataset.VersionRecord {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare dataset fixture: %v", err)
	}
	return dataset.VersionRecord{
		ID: testDatasetVersionID, DatasetID: testDatasetID, Status: "PUBLISHED", VersionNo: 1,
		DSLVersion: dataset.DSLVersion, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
		DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON,
	}
}
