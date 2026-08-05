package understanding_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

func TestQuestionUnderstandingRoundTripAndSchema(t *testing.T) {
	question := "查询今年华东区按月销售额同比前10"
	dimensionHint := "地区"
	targetText := "销售额"
	limit := 10
	month := understanding.TimeGrainMonth
	evidence := askdata.EvidenceRef{
		EvidenceID:  "evidence-domain-sales",
		Kind:        askdata.EvidenceKindRule,
		SourceID:    "rule-domain-sales",
		ContentHash: askdata.HashBytes([]byte("domain sales rule")),
	}
	document := understanding.QuestionUnderstanding{
		SchemaVersion: understanding.SchemaVersion,
		Question:      question,
		DomainHypotheses: []understanding.DomainHypothesis{{
			DomainID: "sales", Score: 0.96, EvidenceRefs: []askdata.EvidenceRef{evidence},
		}},
		MetricMentions: []understanding.MetricMention{{
			Text: "销售额", Span: understanding.Span{Start: 9, End: 12}, AggregationHint: understanding.AggregationDefault,
		}},
		DimensionMentions: []understanding.DimensionMention{{
			Text: "月", Span: understanding.Span{Start: 8, End: 9}, Role: understanding.DimensionRoleGroupBy, Grain: &month,
		}},
		ValueMentions: []understanding.ValueMention{{
			Text: "华东区", Span: understanding.Span{Start: 4, End: 7}, DimensionHint: &dimensionHint, OperatorHint: understanding.ValueOperatorDefault,
		}},
		Time: &understanding.TimeUnderstanding{
			Text: "今年", Span: understanding.Span{Start: 2, End: 4}, Grain: understanding.TimeGrainMonth, Timezone: "Asia/Shanghai",
		},
		Comparisons: []understanding.ComparisonMention{{
			Text: "同比", Span: understanding.Span{Start: 12, End: 14}, Type: understanding.ComparisonYearOverYear, TargetText: &targetText,
		}},
		Ordering: []understanding.OrderingMention{{
			Text: "前", Span: understanding.Span{Start: 14, End: 15}, TargetText: "销售额", Direction: understanding.SortDescending,
		}},
		Limit:           &limit,
		UnresolvedSpans: []understanding.UnresolvedSpan{},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	decoded, err := understanding.Decode(raw)
	if err != nil {
		t.Fatalf("understanding.Decode() error = %v", err)
	}
	if decoded.Question != question || len(decoded.MetricMentions) != 1 {
		t.Fatalf("decoded document = %#v", decoded)
	}

	schemaRaw, err := os.ReadFile("../../../api/schemas/question-understanding-v1.schema.json")
	if err != nil {
		t.Fatalf("os.ReadFile(schema) error = %v", err)
	}
	if _, err := ai.ValidateStructuredOutput(ai.JSONSchema{Name: "question_understanding_v1", Schema: schemaRaw}, raw); err != nil {
		t.Fatalf("schema validation error = %v", err)
	}
}

func TestQuestionUnderstandingRejectsUnknownAndUnsafeFields(t *testing.T) {
	base := `{"schemaVersion":"1.0","question":"销售额","domainHypotheses":[],"metricMentions":[],"dimensionMentions":[],"valueMentions":[],"time":null,"comparisons":[],"ordering":[],"limit":null,"unresolvedSpans":[]}`
	unsafe := strings.Replace(base, `"question":"销售额"`, `"question":"销售额","sql":"select * from orders"`, 1)
	if _, err := understanding.Decode([]byte(unsafe)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Decode() error = %v, want unknown field rejection", err)
	}
}

func TestQuestionUnderstandingRejectsMismatchedUnicodeSpan(t *testing.T) {
	raw := `{"schemaVersion":"1.0","question":"华东销售额","domainHypotheses":[],"metricMentions":[{"text":"销售额","span":{"start":0,"end":3},"aggregationHint":"DEFAULT"}],"dimensionMentions":[],"valueMentions":[],"time":null,"comparisons":[],"ordering":[],"limit":null,"unresolvedSpans":[]}`
	if _, err := understanding.Decode([]byte(raw)); err == nil || !strings.Contains(err.Error(), "does not exactly match") {
		t.Fatalf("Decode() error = %v, want span mismatch", err)
	}
}
