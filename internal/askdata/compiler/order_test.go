package compiler

import (
	"errors"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

func TestCompileSortRejectsUnselectedTargetWithStableCode(t *testing.T) {
	query := topNQueryFixture()
	query.Sort[0].TargetVersionID = "metric-unselected-v1"
	_, err := CompileSort(SortCompileRequest{
		Query: query,
		Columns: []SortColumnBinding{{
			TargetType: ir.SortTargetMetric, TargetVersionID: "metric-unselected-v1",
			CurrentColumn: "sales",
		}},
		StableGroupColumns: []string{"region"},
	})
	var failure *SortPlanError
	if !errors.As(err, &failure) || failure.Code != PlanInvalidSortTargetCode ||
		failure.TargetVersionID != "metric-unselected-v1" {
		t.Fatalf("CompileSort() error = %#v", err)
	}
}

func TestCompileLimitDefaultsAndBoundsAreIndependentFromResultCap(t *testing.T) {
	query := topNQueryFixture()
	compiledSort := mustCompileSort(t, query)
	compiled, err := CompileLimit(LimitCompileRequest{
		SourceRelation: "rank_input", OutputColumns: []string{"region", "sales"}, Sort: compiledSort,
	})
	if err != nil {
		t.Fatal(err)
	}
	if compiled.Limit != 10 || !strings.Contains(compiled.SQL, `"__row_rank" <= 10`) ||
		ir.MaxResultRows != 10000 || ir.MaxTopN != 1000 {
		t.Fatalf("unexpected bounded limit: %#v\n%s", compiled, compiled.SQL)
	}
	for _, invalid := range []int{0, 1001} {
		if _, err := CompileLimit(LimitCompileRequest{
			SourceRelation: "rank_input", OutputColumns: []string{"region", "sales"},
			Sort: compiledSort, Limit: &invalid,
		}); !errors.Is(err, ErrInvalidLimitPlan) {
			t.Fatalf("limit %d error = %v", invalid, err)
		}
	}
}

func TestCompileSortUsesDeltaAndBottomNDirection(t *testing.T) {
	query := topNQueryFixture()
	query.Comparison = &ir.Comparison{Type: ir.ComparisonYearOverYear, Periods: 1}
	query.TimeRange = &ir.TimeRange{
		DimensionVersionID: "dimension-date-v1", Start: "2026-01-01", EndExclusive: "2027-01-01",
		Timezone: "Asia/Shanghai",
	}
	query.Sort[0].RankBy = ir.RankByDelta
	query.Sort[0].Direction = ir.SortAscending
	compiled := mustCompileSort(t, query)
	if !strings.Contains(compiled.RankOrderBy, `"rank_source"."sales_delta" ASC NULLS LAST`) ||
		strings.Contains(compiled.RankOrderBy, `"sales_current"`) {
		t.Fatalf("DELTA BottomN order = %s", compiled.RankOrderBy)
	}
}

func TestTieStrategiesCompileRankAndStableRowNumber(t *testing.T) {
	query := topNQueryFixture()
	include := mustCompileSort(t, query)
	limit := 2
	compiled, err := CompileLimit(LimitCompileRequest{
		SourceRelation: "rank_input", OutputColumns: []string{"region", "sales"}, Sort: include, Limit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.CTE, `RANK() OVER`) || strings.Contains(compiled.CTE, `ROW_NUMBER() OVER`) ||
		!strings.Contains(compiled.MetadataSQL, `"ties_included"`) {
		t.Fatalf("INCLUDE_ALL SQL = %s", compiled.SQL)
	}

	query.TieBreaking = ir.TieDeterministicCut
	cut := mustCompileSort(t, query)
	compiled, err = CompileLimit(LimitCompileRequest{
		SourceRelation: "rank_input", OutputColumns: []string{"region", "sales"}, Sort: cut, Limit: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.CTE, `ROW_NUMBER() OVER`) ||
		!strings.Contains(cut.RowOrderBy, `"rank_source"."region" ASC NULLS LAST`) ||
		!strings.Contains(compiled.MetadataSQL, `"ties_cut"`) {
		t.Fatalf("DETERMINISTIC_CUT SQL = %s", compiled.SQL)
	}
}

func topNQueryFixture() ir.SemanticIR {
	return ir.SemanticIR{
		IRVersion: ir.Version, SemanticReleaseID: "release-query007-v1",
		SemanticContentHash: askdata.HashBytes([]byte("release-query007-v1")),
		DomainID:            "sales", ModelVersionID: "model-sales-v1",
		Metrics: []ir.Metric{{MetricVersionID: "metric-sales-v1", Alias: "sales"}},
		GroupBy: []ir.GroupBy{{DimensionVersionID: "dimension-region-v1"}},
		Filters: []ir.Filter{},
		Sort: []ir.Sort{{
			TargetType: ir.SortTargetMetric, TargetVersionID: "metric-sales-v1",
			Direction: ir.SortDescending, Nulls: ir.NullsLast, RankBy: ir.RankByCurrentValue,
		}},
		Limit: 2, OtherPolicy: ir.OtherNone, TieBreaking: ir.TieIncludeAll,
	}
}

func mustCompileSort(t *testing.T, query ir.SemanticIR) CompiledSort {
	t.Helper()
	compiled, err := CompileSort(SortCompileRequest{
		Query: query,
		Columns: []SortColumnBinding{{
			TargetType: ir.SortTargetMetric, TargetVersionID: "metric-sales-v1",
			CurrentColumn: "sales_current", DeltaColumn: "sales_delta", RatioColumn: "sales_ratio",
		}},
		StableGroupColumns: []string{"region"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}
