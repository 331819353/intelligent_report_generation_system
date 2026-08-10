package ir_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

func TestSemanticIRCanonicalHashIsStable(t *testing.T) {
	month := ir.TimeGrainMonth
	base := ir.SemanticIR{
		IRVersion:           ir.Version,
		SemanticReleaseID:   "release-2026-08",
		SemanticContentHash: askdata.HashBytes([]byte("release")),
		DomainID:            "sales",
		ModelVersionID:      "sales-model@v4",
		Metrics: []ir.Metric{
			{MetricVersionID: "order_count@v2", Alias: "order_count"},
			{MetricVersionID: "net_sales@v3", Alias: "net_sales"},
		},
		GroupBy: []ir.GroupBy{{DimensionVersionID: "stat_month@v2", Grain: &month}},
		Filters: []ir.Filter{
			{DimensionVersionID: "channel@v2", Operator: ir.FilterIn, MemberVersionIDs: []askdata.ID{"offline@v1", "online@v1"}},
			{DimensionVersionID: "sales_region@v5", Operator: ir.FilterIn, MemberVersionIDs: []askdata.ID{"north@v2", "east_china@v2"}},
		},
		TimeRange:  &ir.TimeRange{DimensionVersionID: "order_date@v2", Start: "2026-01-01", EndExclusive: "2026-08-06", Timezone: "Asia/Shanghai"},
		Comparison: &ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1},
		Sort:       []ir.Sort{{TargetType: ir.SortTargetMetric, TargetVersionID: "net_sales@v3", Direction: ir.SortDescending, Nulls: ir.NullsLast, RankBy: ir.RankByCurrentValue}},
		Limit:      500,
	}
	permuted := base
	permuted.Metrics = []ir.Metric{base.Metrics[1], base.Metrics[0]}
	permuted.Filters = []ir.Filter{base.Filters[1], base.Filters[0]}
	permuted.Filters[0].MemberVersionIDs = []askdata.ID{"east_china@v2", "north@v2"}

	_, rawA, hashA, err := ir.Canonicalize(base)
	if err != nil {
		t.Fatalf("Canonicalize(base) error = %v", err)
	}
	_, rawB, hashB, err := ir.Canonicalize(permuted)
	if err != nil {
		t.Fatalf("Canonicalize(permuted) error = %v", err)
	}
	if hashA != hashB || string(rawA) != string(rawB) {
		t.Fatalf("canonical forms differ:\n%s\n%s\n%s != %s", rawA, rawB, hashA, hashB)
	}

	schemaRaw, err := os.ReadFile("../../../api/schemas/semantic-ir-v1.schema.json")
	if err != nil {
		t.Fatalf("os.ReadFile(schema) error = %v", err)
	}
	if _, err := ai.ValidateStructuredOutput(ai.JSONSchema{Name: "semantic_ir_v1", Schema: schemaRaw}, rawA); err != nil {
		t.Fatalf("schema validation error = %v", err)
	}
}

func TestSemanticIRDecodeRejectsPhysicalFields(t *testing.T) {
	document := validIR()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	unsafe := strings.Replace(string(raw), `"modelVersionId":"sales-model@v4"`, `"modelVersionId":"sales-model@v4","tableName":"orders"`, 1)
	if _, err := ir.Decode([]byte(unsafe)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Decode() error = %v, want unknown physical field rejection", err)
	}
}

func TestSemanticIRRequiresOneSelectedDomainID(t *testing.T) {
	document := validIR()
	document.DomainID = ""
	if err := document.Validate(); err == nil || !strings.Contains(err.Error(), "domainId") {
		t.Fatalf("Validate() error = %v, want required domainId", err)
	}
	raw, err := json.Marshal(validIR())
	if err != nil {
		t.Fatal(err)
	}
	multiple := strings.Replace(string(raw), `"domainId":"sales"`, `"domainId":["sales","finance"]`, 1)
	if _, err := ir.Decode([]byte(multiple)); err == nil {
		t.Fatal("Decode() accepted a multi-valued domainId")
	}
}

func TestSemanticIRRejectsUnboundAndInvalidReferences(t *testing.T) {
	document := validIR()
	document.Filters[0].MemberVersionIDs = nil
	if err := document.Validate(); err == nil || !strings.Contains(err.Error(), "memberVersionIds is required") {
		t.Fatalf("Validate() error = %v, want unbound member rejection", err)
	}
	document = validIR()
	document.Sort[0].TargetVersionID = "unknown_metric@v1"
	if err := document.Validate(); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("Validate() error = %v, want unknown sort target rejection", err)
	}
}

func validIR() ir.SemanticIR {
	return ir.SemanticIR{
		IRVersion:           ir.Version,
		SemanticReleaseID:   "release-2026-08",
		SemanticContentHash: askdata.HashBytes([]byte("release")),
		DomainID:            "sales",
		ModelVersionID:      "sales-model@v4",
		Metrics:             []ir.Metric{{MetricVersionID: "net_sales@v3", Alias: "net_sales"}},
		GroupBy:             []ir.GroupBy{},
		Filters:             []ir.Filter{{DimensionVersionID: "sales_region@v5", Operator: ir.FilterIn, MemberVersionIDs: []askdata.ID{"east_china@v2"}}},
		TimeRange:           nil,
		Comparison:          nil,
		Sort:                []ir.Sort{{TargetType: ir.SortTargetMetric, TargetVersionID: "net_sales@v3", Direction: ir.SortDescending, Nulls: ir.NullsLast, RankBy: ir.RankByCurrentValue}},
		Limit:               500,
		OtherPolicy:         ir.OtherNone,
		TieBreaking:         ir.TieIncludeAll,
	}
}
