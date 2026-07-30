package semanticqa

import (
	"encoding/json"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestSelectADSContainmentTargetUsesLargestStrictSubset(t *testing.T) {
	source := adsTestDWSDocument(
		[]string{"stat_month", "region_code", "channel_code"},
	)
	candidates := []adsDWSAsset{
		{
			Record: dataset.Record{Code: "dws_region"},
			Document: adsTestDWSDocument(
				[]string{"region_code"},
			),
		},
		{
			Record: dataset.Record{Code: "dws_month_region"},
			Document: adsTestDWSDocument(
				[]string{"stat_month", "region_code"},
			),
		},
		{
			Record: dataset.Record{Code: "dws_product"},
			Document: adsTestDWSDocument(
				[]string{"product_code"},
			),
		},
	}
	selected := selectADSContainmentTarget(source, candidates)
	if selected == nil || selected.Record.Code != "dws_month_region" {
		t.Fatalf("selected target = %#v", selected)
	}
}

func TestBuildContainedDWSADSCandidateAggregatesAdditiveMeasures(t *testing.T) {
	sourceDocument := adsTestDWSDocument(
		[]string{"stat_month", "region_code", "channel_code"},
	)
	raw, err := json.Marshal(sourceDocument)
	if err != nil {
		t.Fatal(err)
	}
	target := adsDWSAsset{
		Record: dataset.Record{
			ID:   "20000000-0000-4000-8000-000000000001",
			Code: "dws_month_region", Name: "月度区域主题",
		},
		VersionID: "20000000-0000-4000-8000-000000000002",
		Document: adsTestDWSDocument(
			[]string{"stat_month", "region_code"},
		),
	}
	prepared, err := buildContainedDWSADSCandidate(
		dataset.Record{
			Code: "dws_month_region_channel", Name: "月度区域渠道主题",
			Layer: dataset.LayerDWS, DSL: raw,
		},
		"10000000-0000-4000-8000-000000000001",
		target,
	)
	if err != nil {
		t.Fatalf("build containment ADS: %v", err)
	}
	if len(prepared.Document.PreAggregations) != 1 ||
		len(prepared.Document.PreAggregations[0].GroupBy) != 2 ||
		len(prepared.Document.Joins) != 1 {
		t.Fatalf(
			"containment topology = preAgg %#v joins %#v",
			prepared.Document.PreAggregations, prepared.Document.Joins,
		)
	}
	if len(prepared.Document.PreAggregations[0].Metrics) != 1 ||
		prepared.Document.PreAggregations[0].Metrics[0].Function != "SUM" {
		t.Fatalf(
			"source rollup metrics = %#v",
			prepared.Document.PreAggregations[0].Metrics,
		)
	}
	foundSource, foundTarget := false, false
	for _, field := range prepared.Document.Fields {
		foundSource = foundSource || field.Code == "source_revenue"
		foundTarget = foundTarget || field.Code == "target_revenue"
	}
	if !foundSource || !foundTarget {
		t.Fatalf(
			"associated metrics missing: source=%t target=%t",
			foundSource, foundTarget,
		)
	}
}

func TestADSContainmentRejectsMultiGrainAndPointInTimeSources(t *testing.T) {
	target := adsDWSAsset{
		Record: dataset.Record{Code: "dws_month_region"},
		Document: adsTestDWSDocument(
			[]string{"stat_month", "region_code"},
		),
	}
	multiGrain := adsTestDWSDocument(
		[]string{"stat_month", "region_code", "channel_code"},
	)
	multiGrain.GroupByMode = dataset.GroupByModeCube
	if selected := selectADSContainmentTarget(
		multiGrain, []adsDWSAsset{target},
	); selected != nil {
		t.Fatalf("multi-grain DWS must not be reaggregated: %#v", selected)
	}

	pointInTime := adsTestDWSDocument(
		[]string{"stat_month", "region_code", "channel_code"},
	)
	pointInTime.AnalysisContract.Measures[0].Additivity = "SEMI_ADDITIVE"
	pointInTime.AnalysisContract.Measures[0].ValueBehavior = "POINT_IN_TIME"
	pointInTime.AnalysisContract.Measures[0].TimeAggregation = "LAST"
	if selected := selectADSContainmentTarget(
		pointInTime, []adsDWSAsset{target},
	); selected != nil {
		t.Fatalf("point-in-time DWS must not be summed across time: %#v", selected)
	}
}

func adsTestDWSDocument(dimensions []string) dataset.Document {
	fields := []dataset.Field{}
	for index, code := range dimensions {
		role, canonicalType := "DIMENSION", "STRING"
		if code == "stat_month" {
			role, canonicalType = "TIME", "DATE"
		}
		fields = append(fields, dataset.Field{
			ID:   "field_dimension_" + string(rune('a'+index)),
			Code: code, Name: code, Description: code,
			Role: role, CanonicalType: canonicalType,
		})
	}
	fields = append(fields, dataset.Field{
		ID: "field_revenue", Code: "revenue", Name: "收入",
		Description: "收入", Role: "MEASURE", CanonicalType: "DECIMAL",
	})
	return dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: "dws_test", Name: "测试主题", Domain: "销售",
			Subject: "销售", Type: "SINGLE_SOURCE", Layer: dataset.LayerDWS,
		},
		Fields: fields,
		OutputGrain: dataset.OutputGrain{
			KeyFields: append([]string(nil), dimensions...),
		},
		AnalysisContract: &dataset.AnalysisContract{
			Intent: "TREND", InputMode: "SINGLE_FACT",
			CommonGrainFields:   append([]string(nil), dimensions...),
			ConformedDimensions: append([]string(nil), dimensions...),
			Measures: []dataset.AnalysisMeasureContract{{
				Field: "revenue", Aggregation: "SUM", Additivity: "ADDITIVE",
				SourceNodeIDs: []string{"fact"},
			}},
		},
	}
}
