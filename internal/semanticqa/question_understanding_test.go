package semanticqa

import (
	"strings"
	"testing"
	"time"
)

func TestUnderstandQuestionPreservesOriginalUTF8ByteSpans(t *testing.T) {
	snapshot := QuestionSemanticSnapshot{
		TenantID: "tenant-a", SemanticVersion: "release-1",
		ContentHash: strings.Repeat("a", 64), EffectiveAt: time.Now().UTC(),
		Objects: []QuestionSemanticObject{
			{
				ObjectType: "METRIC", ObjectID: "paid_gmv", ObjectVersion: "1",
				Contract: map[string]any{
					"code": "paid_gmv", "title": "支付GMV",
					"aliases": []any{"支付gmv"},
				},
			},
			{
				ObjectType: "CERTIFIED_EXAMPLE", ObjectID: "paid_gmv_top", ObjectVersion: "1",
				Contract: map[string]any{
					"question": "支付GMV上月前10", "objectIds": []any{"paid_gmv"},
				},
			},
		},
	}
	original := "  支付ＧＭＶ   上月前10  "
	understanding := understandQuestion(original, &snapshot)
	if understanding.NormalizedText != "支付gmv 上月前10" {
		t.Fatalf("unexpected normalized text: %q", understanding.NormalizedText)
	}
	foundMetric, foundTime, foundLimit := false, false, false
	for _, mention := range understanding.Mentions {
		if mention.StartByte < 0 || mention.EndByte > len(original) || mention.StartByte >= mention.EndByte {
			t.Fatalf("invalid original byte span: %+v", mention)
		}
		switch mention.Type {
		case "METRIC":
			foundMetric = true
			if original[mention.StartByte:mention.EndByte] != "支付ＧＭＶ" ||
				len(mention.Candidates) != 1 || mention.Candidates[0].ObjectID != "paid_gmv" {
				t.Fatalf("governed metric span did not map to original UTF-8 bytes: %+v", mention)
			}
		case "TIME_RANGE":
			foundTime = true
		case "LIMIT":
			foundLimit = true
		}
	}
	if !foundMetric || !foundTime || !foundLimit {
		t.Fatalf("missing deterministic mentions: %+v", understanding.Mentions)
	}
	if len(understanding.CertifiedExamples) != 1 ||
		understanding.CertifiedExamples[0].ObjectID != "paid_gmv_top" ||
		!strings.HasPrefix(understanding.CertifiedExamples[0].EvidenceID, "example:") {
		t.Fatalf("certified example recall is missing: %+v", understanding.CertifiedExamples)
	}
}

func TestNormalizeQuestionUsesUnicodeCaseFoldingWithAlignment(t *testing.T) {
	result := normalizeQuestion("Straße")
	if result.NormalizedText != "strasse" {
		t.Fatalf("expected full Unicode case folding, got %q", result.NormalizedText)
	}
	if len(result.AlignmentMap) != 7 ||
		result.AlignmentMap[4].OriginalStart != result.AlignmentMap[5].OriginalStart {
		t.Fatalf("expanded case fold must retain original byte evidence: %+v", result.AlignmentMap)
	}
}

func TestGovernedAliasAmbiguityRequiresStableMetricConfirmation(t *testing.T) {
	understanding := QuestionUnderstanding{Mentions: []QuestionMention{{
		Type: "METRIC", MentionText: "销售额",
		Candidates: []QuestionMentionCandidate{
			{Code: "gross_sales", Label: "销售额（含税）"},
			{Code: "net_sales", Label: "销售额（净额）"},
		},
	}}}
	clarification := governedMentionClarification(understanding, nil)
	if clarification == nil || clarification.Type != "METRIC" || len(clarification.MetricCandidates) != 2 {
		t.Fatalf("ambiguous governed alias must clarify: %+v", clarification)
	}
	if governedMentionClarification(understanding, []string{"net_sales"}) != nil {
		t.Fatal("one stable governed metric confirmation must resolve the alias")
	}
}

func TestGovernedSimpleQuestionHintsUseExactMetricWithoutLegacyModel(t *testing.T) {
	snapshot := QuestionSemanticSnapshot{
		TenantID: "tenant-a", SemanticVersion: "release-1",
		ContentHash: strings.Repeat("a", 64), EffectiveAt: time.Now().UTC(),
		Objects: []QuestionSemanticObject{{
			ObjectType: "METRIC", ObjectID: "metric-1", ObjectVersion: "v1",
			Contract: map[string]any{"code": "net_revenue", "title": "净收入"},
		}},
	}
	understanding := understandQuestion("请问本月净收入是多少？", &snapshot)
	codes, hints, complete := governedSimpleQuestionHints(
		"请问本月净收入是多少？", understanding,
	)
	if !complete || len(codes) != 1 || codes[0] != "net_revenue" || hints.Intent != "METRIC" {
		t.Fatalf("simple governed question did not use deterministic hints: codes=%v hints=%+v mentions=%+v", codes, hints, understanding.Mentions)
	}
}

func TestGovernedSimpleQuestionHintsCarryExplicitDateRange(t *testing.T) {
	snapshot := QuestionSemanticSnapshot{
		TenantID: "tenant-a", SemanticVersion: "release-1",
		ContentHash: strings.Repeat("a", 64), EffectiveAt: time.Now().UTC(),
		Objects: []QuestionSemanticObject{{
			ObjectType: "METRIC", ObjectID: "metric-1", ObjectVersion: "v1",
			Contract: map[string]any{"code": "net_revenue", "title": "净收入"},
		}},
	}
	question := "2026年6月1日的净收入是多少？"
	understanding := understandQuestion(question, &snapshot)
	_, hints, complete := governedSimpleQuestionHints(question, understanding)
	if !complete || len(hints.DimensionValues) != 1 ||
		hints.DimensionValues[0].TimeRange == nil ||
		hints.DimensionValues[0].TimeRange.Start != "2026-06-01" ||
		hints.DimensionValues[0].TimeRange.EndExclusive != "2026-06-02" {
		t.Fatalf("explicit date range was not preserved: %+v", hints)
	}
	month, ok := inferExplicitQueryTimeRange("2026-06")
	if !ok || month.Start != "2026-06-01" || month.EndExclusive != "2026-07-01" {
		t.Fatalf("explicit month range = %+v ok=%v", month, ok)
	}
	if _, ok := inferExplicitQueryTimeRange("2026年2月30日"); ok {
		t.Fatal("invalid calendar date must not be normalized into a different day")
	}
	for _, date := range []string{
		"2026-06-09", "2026-06-10", "2026-06-19", "2026-06-20",
		"2026-06-30",
	} {
		question := date + "的净收入是多少？"
		understanding := understandQuestion(question, &snapshot)
		_, hints, complete := governedSimpleQuestionHints(question, understanding)
		if !complete || len(hints.DimensionValues) != 1 ||
			hints.DimensionValues[0].TimeRange == nil ||
			hints.DimensionValues[0].TimeRange.Start != date {
			t.Fatalf("two-digit date %q was truncated: %+v", date, hints)
		}
	}
}

func TestGovernedSimpleQuestionHintsRejectUnaccountedBusinessText(t *testing.T) {
	snapshot := QuestionSemanticSnapshot{
		TenantID: "tenant-a", SemanticVersion: "release-1",
		ContentHash: strings.Repeat("a", 64), EffectiveAt: time.Now().UTC(),
		Objects: []QuestionSemanticObject{{
			ObjectType: "METRIC", ObjectID: "metric-1", ObjectVersion: "v1",
			Contract: map[string]any{"code": "net_revenue", "title": "净收入"},
		}},
	}
	understanding := understandQuestion("本月净收入按渠道拆分", &snapshot)
	if _, _, complete := governedSimpleQuestionHints("本月净收入按渠道拆分", understanding); complete {
		t.Fatal("unresolved business text must fall back to bounded interpretation")
	}
}
