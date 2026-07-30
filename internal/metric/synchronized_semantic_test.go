package metric

import (
	"strings"
	"testing"
)

func TestSynchronizedMetricCaliberPreservesLockedFacts(t *testing.T) {
	definition := Definition{
		Metric:       Descriptor{Code: "entity_count", Name: "配送区域实体数量", Type: "DERIVED"},
		Aggregation:  "NONE",
		Unit:         "个",
		NumberFormat: "#,##0",
		DecimalScale: 0,
		TimeGrain:    "NONE",
		Additivity:   "NON_ADDITIVE",
		NullHandling: "IGNORE",
		SourceCalculation: &SourceCalculation{
			Stage:           "DATASET_DAG",
			Aggregation:     "COUNT_DISTINCT",
			Formula:         "COUNT_DISTINCT(dimension.zone_id)",
			ValueBehavior:   "NON_ADDITIVE",
			TimeAggregation: "NONE",
			EvidencePath:    "dsl.fields[3].expression",
		},
	}

	caliber := synchronizedMetricCaliber(definition, definition.SourceCalculation.Aggregation)
	for _, fact := range []string{
		"COUNT_DISTINCT(dimension.zone_id)",
		"COUNT_DISTINCT",
		"NON_ADDITIVE",
		"跨时间规则为 NONE",
		"不再二次聚合",
		"忽略空值",
		"#,##0",
		"保留 0 位小数",
		"单位为 个",
	} {
		if !strings.Contains(caliber, fact) {
			t.Fatalf("caliber %q does not contain locked fact %q", caliber, fact)
		}
	}
	if strings.Contains(caliber, "不聚合") {
		t.Fatalf("caliber must not describe a pre-aggregated metric as unaggregated: %q", caliber)
	}
}

func TestSynchronizedMetricTagsUseBusinessAggregation(t *testing.T) {
	definition := Definition{
		Metric:    Descriptor{Code: "entity_count", Name: "配送区域实体数量", Type: "DERIVED"},
		Unit:      "个",
		TimeGrain: "NONE",
	}
	tags := synchronizedMetricTags(
		definition,
		"COUNT_DISTINCT",
		[]string{"城市", "配送区域"},
	)
	joined := strings.Join(tags, "|")
	if !strings.Contains(joined, "COUNT_DISTINCT") || strings.Contains(joined, "|NONE|") {
		t.Fatalf("unexpected synchronized metric tags: %v", tags)
	}
}
