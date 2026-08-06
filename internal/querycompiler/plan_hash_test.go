package querycompiler

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestCompiledPlanHashExcludesValuesAndResultLimitIsExplicit(t *testing.T) {
	document := planHashDocument()
	input := Input{
		Document: document, Dialect: PostgreSQL,
		Tables: map[string]TableRef{"source": {
			NodeID: "source", Schema: "warehouse_published", Name: "dws_orders",
			Columns: map[string]bool{"region": true}, ColumnTypes: map[string]string{"region": "STRING"},
		}},
		Parameters: map[string]any{"region": "EAST"}, MaxRows: 8000, LimitKind: LimitResult,
	}
	first, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Parameters = map[string]any{"region": "WEST"}
	second, err := Compile(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanHash == "" || first.PlanHash != second.PlanHash {
		t.Fatalf("runtime values changed compiled plan hash: %q != %q", first.PlanHash, second.PlanHash)
	}
	input.LimitKind = LimitPreview
	if _, err := Compile(input); err == nil {
		t.Fatal("preview compilation exceeded previewLimit")
	}
	input.LimitKind = "OTHER"
	if _, err := Compile(input); err == nil {
		t.Fatal("unknown limit kind was accepted")
	}
}

func planHashDocument() dataset.Document {
	visible := true
	left := dataset.Expression{Type: "FIELD_REF", NodeID: "source", Field: "region"}
	right := dataset.Expression{Type: "PARAM_REF", Code: "region"}
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset:    dataset.Descriptor{Code: "askdata_plan_hash", Name: "plan", Type: "SINGLE_SOURCE"},
		Nodes: []dataset.Node{{
			ID: "source", Type: "DATASET", DatasetVersionID: "dataset-version",
			Alias: "source", Projection: []string{"region"}, SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{}, Transforms: []dataset.Transform{}, PreAggregations: []dataset.PreAggregation{},
		Fields: []dataset.Field{{
			ID: "field_region", Code: "region", Name: "region", Role: "DIMENSION",
			Expression: left, CanonicalType: "STRING", Visible: &visible,
		}},
		Filters: []dataset.Filter{{
			ID: "filter_region", Stage: "PRE_AGGREGATION",
			Expression: dataset.Expression{Type: "EQUALS", Left: &left, Right: &right},
		}},
		GroupBy: []string{}, Having: []dataset.Filter{}, Sorts: []dataset.Sort{},
		Parameters: []dataset.Parameter{{
			Code: "region", Name: "region", DataType: "STRING", Required: true,
		}},
		OutputGrain: dataset.OutputGrain{
			Description: "one row per region", KeyFields: []string{"region"},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 5000, ResultLimit: 10000,
		},
	}
}
