package semanticasset

import (
	"testing"
	"time"
)

func TestBuildLegacySemanticReleaseCandidatePinsNativeAssets(t *testing.T) {
	now := time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC)
	snapshot := legacyReleaseSnapshot{
		Datasets: []legacyDataset{{
			ID: "00000000-0000-4000-8000-000000000101", VersionID: "00000000-0000-4000-8000-000000000102",
			Code: "sales_daily", Name: "销售日汇总", DomainID: "domain-sales",
			OwnerID: "00000000-0000-4000-8000-000000000001", PublishedAt: now,
			PhysicalSchema: "warehouse_dws", PhysicalName: "sales_daily_v1",
			DSL: map[string]any{
				"outputGrain": map[string]any{"keyFields": []any{"stat_date", "region"}},
				"fields":      []any{map[string]any{"id": "stat_date", "name": "统计日期"}},
			},
		}},
		Metrics: []legacyMetric{{
			ID: "00000000-0000-4000-8000-000000000201", VersionID: "00000000-0000-4000-8000-000000000202",
			DatasetID: "00000000-0000-4000-8000-000000000101", DatasetVersionID: "00000000-0000-4000-8000-000000000102",
			Code: "paid_gmv", Name: "支付金额", MetricType: "ATOMIC", DomainID: "domain-sales",
			OwnerID: "00000000-0000-4000-8000-000000000001", PublishedAt: now,
			Definition: map[string]any{"timeFieldId": "stat_date", "expression": map[string]any{"type": "FIELD_REF", "fieldId": "paid_amount"}},
		}},
		Dimensions: []legacyDimension{{
			ID: "00000000-0000-4000-8000-000000000301", DatasetID: "00000000-0000-4000-8000-000000000101",
			DatasetVersionID: "00000000-0000-4000-8000-000000000102", FieldID: "region",
			Code: "sales_region", Name: "销售区域", DimensionType: "GEOGRAPHY",
			DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DomainID:       "domain-sales", OwnerID: "00000000-0000-4000-8000-000000000001",
			MemberIndexPolicy: "FULL", UpdatedAt: now,
		}},
		Compatibilities: []legacyCompatibility{{
			MetricVersionID: "00000000-0000-4000-8000-000000000202",
			DimensionID:     "00000000-0000-4000-8000-000000000301",
			Cardinality:     "NOT_APPLICABLE", FanoutPolicy: "SAFE",
		}},
		Members: []legacyDimensionMember{{
			ID:          "00000000-0000-4000-8000-000000000401",
			DimensionID: "00000000-0000-4000-8000-000000000301",
			MemberKey:   "east", CanonicalLabel: "华东", Aliases: []string{"东区"},
		}},
		RoleCodes: []string{"analyst"},
	}
	preview := buildLegacySemanticReleaseCandidate(
		"00000000-0000-4000-8000-000000000001",
		BootstrapSemanticReleaseInput{
			SemanticVersion: "legacy-2026.08.03", DefaultTimezone: "Asia/Shanghai",
			DefaultCalendar: "GREGORIAN", CompletePeriodPolicy: "EXCLUDE_INCOMPLETE",
		}, snapshot,
	)
	if !preview.Eligible || preview.Candidate == nil || len(preview.Issues) != 0 {
		t.Fatalf("expected eligible candidate, got %+v", preview)
	}
	if preview.SourceCounts["metrics"] != 1 || preview.CandidateCount < 8 {
		t.Fatalf("unexpected migration summary: %+v", preview)
	}
	draft, err := normalizeSemanticRelease(*preview.Candidate)
	if err != nil {
		t.Fatalf("generated candidate must pass release validation: %v", err)
	}
	if draft.ContentHash == "" {
		t.Fatal("generated candidate must have a content hash")
	}
}

func TestBuildLegacySemanticReleaseCandidateDoesNotPublishSensitiveValues(t *testing.T) {
	now := time.Now().UTC()
	snapshot := legacyReleaseSnapshot{
		Datasets: []legacyDataset{{
			ID: "00000000-0000-4000-8000-000000000101", VersionID: "00000000-0000-4000-8000-000000000102",
			Code: "people", Name: "人员", OwnerID: "00000000-0000-4000-8000-000000000001",
			PublishedAt: now, PhysicalSchema: "warehouse_dws", PhysicalName: "people_v1",
			DSL: map[string]any{"outputGrain": map[string]any{"keyFields": []any{"person_id"}}},
		}},
		Metrics: []legacyMetric{{
			ID: "00000000-0000-4000-8000-000000000201", VersionID: "00000000-0000-4000-8000-000000000202",
			DatasetID: "00000000-0000-4000-8000-000000000101", DatasetVersionID: "00000000-0000-4000-8000-000000000102",
			Code: "people_count", Name: "人数", OwnerID: "00000000-0000-4000-8000-000000000001",
			PublishedAt: now, Definition: map[string]any{"expression": map[string]any{"type": "COUNT"}},
		}},
		Dimensions: []legacyDimension{{
			ID: "00000000-0000-4000-8000-000000000301", DatasetID: "00000000-0000-4000-8000-000000000101",
			DatasetVersionID: "00000000-0000-4000-8000-000000000102", FieldID: "person_name",
			Code: "person_name", Name: "姓名", DimensionType: "OTHER", Sensitive: true,
			DefinitionHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			OwnerID:        "00000000-0000-4000-8000-000000000001", MemberIndexPolicy: "FULL", UpdatedAt: now,
		}},
		Compatibilities: []legacyCompatibility{{
			MetricVersionID: "00000000-0000-4000-8000-000000000202",
			DimensionID:     "00000000-0000-4000-8000-000000000301", FanoutPolicy: "SAFE",
		}},
		Members: []legacyDimensionMember{{
			ID:          "00000000-0000-4000-8000-000000000401",
			DimensionID: "00000000-0000-4000-8000-000000000301", MemberKey: "alice", CanonicalLabel: "张三",
		}},
		RoleCodes: []string{"analyst"},
	}
	preview := buildLegacySemanticReleaseCandidate(
		"00000000-0000-4000-8000-000000000001",
		BootstrapSemanticReleaseInput{
			SemanticVersion: "legacy-sensitive", DefaultTimezone: "Asia/Shanghai",
			DefaultCalendar: "GREGORIAN", CompletePeriodPolicy: "EXCLUDE_INCOMPLETE",
		}, snapshot,
	)
	if !preview.Eligible || preview.Candidate == nil {
		t.Fatalf("unexpected blocked preview: %+v", preview)
	}
	for _, object := range preview.Candidate.Objects {
		if object.ObjectType == "DIMENSION_VALUE" {
			t.Fatal("sensitive dimension members must not enter release search or graph projections")
		}
	}
}
