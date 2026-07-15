package federation

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/filequery"
)

func TestAnalyzeJoinRisksReportsConfirmationCardinalityAndFanout(t *testing.T) {
	document := crossDocument()
	document.Joins[0].Cardinality = "ONE_TO_ONE"
	document.Joins[0].ManualConfirmed = false
	warnings, err := analyzeJoinRisks(context.Background(), document, map[string]filequery.NodeTableData{
		"orders":    {Columns: []string{"customer_id", "amount"}, Rows: [][]any{{int64(1), 10.0}, {int64(1), 20.0}, {int64(2), 30.0}}},
		"customers": {Columns: []string{"customer_id", "customer_name"}, Rows: [][]any{{int64(1), "A"}, {int64(1), "A2"}, {int64(2), "B"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	codes := map[string]bool{}
	for _, warning := range warnings {
		codes[warning.Code] = true
		if warning.JoinID != "orders_customers" || warning.EstimatedRows != 5 {
			t.Fatalf("warning=%#v", warning)
		}
	}
	for _, code := range []string{"JOIN_CONFIRMATION_REQUIRED", "JOIN_CARDINALITY_MISMATCH", "JOIN_FANOUT_RISK"} {
		if !codes[code] {
			t.Fatalf("warning codes=%#v", codes)
		}
	}
}

func TestAnalyzeJoinRisksReportsManyToMany(t *testing.T) {
	document := crossDocument()
	document.Joins[0].Cardinality = "MANY_TO_MANY"
	warnings, err := analyzeJoinRisks(context.Background(), document, map[string]filequery.NodeTableData{
		"orders":    {Columns: []string{"customer_id"}, Rows: [][]any{{int64(1)}, {int64(1)}}},
		"customers": {Columns: []string{"customer_id"}, Rows: [][]any{{int64(1)}, {int64(1)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 || warnings[0].Code != "JOIN_MANY_TO_MANY" || warnings[1].Code != "JOIN_FANOUT_RISK" {
		t.Fatalf("warnings=%#v", warnings)
	}
}

func TestAnalyzeJoinRisksRejectsLargeFanoutBeforeJoin(t *testing.T) {
	document := crossDocument()
	leftRows, rightRows := make([][]any, 500), make([][]any, 500)
	for index := range leftRows {
		leftRows[index], rightRows[index] = []any{int64(1)}, []any{int64(1)}
	}
	_, err := analyzeJoinRisks(context.Background(), document, map[string]filequery.NodeTableData{
		"orders": {Columns: []string{"customer_id"}, Rows: leftRows}, "customers": {Columns: []string{"customer_id"}, Rows: rightRows},
	})
	if !errors.Is(err, ErrJoinFanoutLimit) {
		t.Fatalf("analyze error=%v", err)
	}
}

func TestEstimateJoinPlanRowsDetectsMultiStageFanout(t *testing.T) {
	document := threeNodeRiskDocument()
	orders, customers, segments := make([][]any, 200), make([][]any, 200), make([][]any, 6)
	for index := range orders {
		orders[index] = []any{int64(1)}
	}
	for index := range customers {
		customers[index] = []any{int64(1)}
	}
	for index := range segments {
		segments[index] = []any{int64(1)}
	}
	_, err := estimateJoinPlanRows(context.Background(), document, map[string]filequery.NodeTableData{
		"orders": {Columns: []string{"customer_id"}, Rows: orders}, "customers": {Columns: []string{"customer_id"}, Rows: customers}, "segments": {Columns: []string{"customer_id"}, Rows: segments},
	})
	if !errors.Is(err, ErrJoinFanoutLimit) {
		t.Fatalf("plan estimate error=%v", err)
	}
}

func TestEstimateJoinPlanRowsTracksEveryJoinStage(t *testing.T) {
	document := threeNodeRiskDocument()
	estimates, err := estimateJoinPlanRows(context.Background(), document, map[string]filequery.NodeTableData{
		"orders":    {Columns: []string{"customer_id"}, Rows: [][]any{{int64(1)}, {int64(1)}}},
		"customers": {Columns: []string{"customer_id"}, Rows: [][]any{{int64(1)}, {int64(1)}}},
		"segments":  {Columns: []string{"customer_id"}, Rows: [][]any{{int64(1)}, {int64(1)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if estimates["orders_customers"] != 4 || estimates["customers_segments"] != 8 {
		t.Fatalf("estimates=%#v", estimates)
	}
}

func TestAnalyzeJoinRisksHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := analyzeJoinRisks(ctx, crossDocument(), map[string]filequery.NodeTableData{
		"orders": {Columns: []string{"customer_id"}}, "customers": {Columns: []string{"customer_id"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("analyze error=%v", err)
	}
}

func threeNodeRiskDocument() dataset.Document {
	document := crossDocument()
	document.Nodes = append(document.Nodes, dataset.Node{ID: "segments", Type: "TABLE", DataSourceID: "source-segments", TableID: "table-segments", Alias: "s", Projection: []string{"customer_id"}, SourceFilters: []dataset.SourceFilter{}})
	document.Joins = append(document.Joins, dataset.Join{
		ID: "customers_segments", LeftNodeID: "customers", RightNodeID: "segments", JoinType: "INNER", Cardinality: "MANY_TO_MANY", ManualConfirmed: true,
		Conditions: []dataset.JoinCondition{{
			LeftExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "customers", Field: "customer_id"}, Operator: "EQUALS",
			RightExpression: dataset.Expression{Type: "FIELD_REF", NodeID: "segments", Field: "customer_id"},
		}},
	})
	return document
}
