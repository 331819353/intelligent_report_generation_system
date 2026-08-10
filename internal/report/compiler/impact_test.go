package compiler

import (
	"context"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

type impactSourceStub struct{ items []ReportImpact }

func (stub impactSourceStub) FindReportImpacts(context.Context, askdata.ID, ChangeSpec) ([]ReportImpact, error) {
	return stub.items, nil
}

func TestImpactAnalyzerNormalizesAndClassifies(t *testing.T) {
	tenant := askdata.ID("11111111-1111-4111-8111-111111111111")
	reportID := askdata.ID("22222222-2222-4222-8222-222222222222")
	analyzer := NewImpactAnalyzer(impactSourceStub{items: []ReportImpact{
		{ReportID: reportID, ReportName: "R", OwnerID: tenant, ComponentIDs: []askdata.ID{"b", "a"}, DraftAffected: true},
		{ReportID: reportID, ReportName: "R", OwnerID: tenant, ComponentIDs: []askdata.ID{"a"}, PublishedImpact: true},
	}})
	items, severity, err := analyzer.AnalyzeImpact(context.Background(), tenant, ChangeSpec{
		Kind: ChangeMetricVersion, ObjectID: "33333333-3333-4333-8333-333333333333",
		ChangedFields: []string{"description", "formula_ast"},
	})
	if err != nil || severity != SeverityBreaking || len(items) != 1 || len(items[0].ComponentIDs) != 2 ||
		!items[0].DraftAffected || !items[0].PublishedImpact {
		t.Fatalf("AnalyzeImpact() = %#v, %s, %v", items, severity, err)
	}
}

func TestClassifyImpact(t *testing.T) {
	for _, test := range []struct {
		fields []string
		want   ImpactSeverity
	}{
		{[]string{"time_contract"}, SeverityBreaking},
		{[]string{"optionalDisplayProperty"}, SeverityCompatible},
		{[]string{"description", "aliases"}, SeverityInformational},
	} {
		if got := ClassifyImpact(test.fields); got != test.want {
			t.Fatalf("ClassifyImpact(%v) = %s", test.fields, got)
		}
	}
}

func TestReportImpactSQLDoesNotScanDefinitions(t *testing.T) {
	if strings.Contains(strings.ToLower(reportImpactSQL), "definition_json") {
		t.Fatal("impact query must not scan report definitions")
	}
	for _, table := range []string{"report_version_dependencies", "report_draft_dependencies"} {
		if !strings.Contains(reportImpactSQL, table) {
			t.Fatalf("impact query is missing %s", table)
		}
	}
}
