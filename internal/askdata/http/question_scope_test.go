package askdatahttp

import (
	"encoding/json"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/datarequest"
)

func TestPublicScopeVerdictAllowsOnlyStrictRowFreeDataRequestContext(t *testing.T) {
	originalQuestion := "导出本月订单明细"
	_, verdict := understanding.Classify(understanding.QuestionUnderstanding{
		SchemaVersion: understanding.SchemaVersion, Question: originalQuestion,
	})
	var err error
	verdict, err = understanding.WithParsedContext(verdict, datarequest.ParsedContext{
		MetricIDs: []string{"00000000-0000-4000-8000-000000000101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(verdict)
	if err != nil {
		t.Fatal(err)
	}
	public := parsePublicScopeVerdict(payload)
	if public == nil || public.Reason != understanding.ScopeReasonDetailList || public.ParsedContext == nil ||
		len(public.NextActions) != 1 || public.NextActions[0].Kind != understanding.NextActionDataRequest {
		t.Fatalf("public scope verdict = %#v", public)
	}
	if strings.Contains(string(payload), originalQuestion) || strings.Contains(string(payload), "requestText") {
		t.Fatalf("scope artifact persisted raw question: %s", payload)
	}

	var unsafe map[string]any
	if err := json.Unmarshal(payload, &unsafe); err != nil {
		t.Fatal(err)
	}
	unsafe["parsedContext"].(map[string]any)["rows"] = []any{map[string]any{"orderId": "secret"}}
	unsafePayload, _ := json.Marshal(unsafe)
	if parsed := parsePublicScopeVerdict(unsafePayload); parsed != nil {
		t.Fatalf("scope verdict accepted result rows: %#v", parsed)
	}
}

func TestPublicScopeVerdictRejectsTamperedActionAndOutcome(t *testing.T) {
	_, verdict := understanding.Classify(understanding.QuestionUnderstanding{
		SchemaVersion: understanding.SchemaVersion, Question: "预测下月销售额",
	})
	verdict.NextActions[0].Payload.Target = understanding.NextActionTargetDataRequestDialog
	payload, _ := json.Marshal(verdict)
	if parsed := parsePublicScopeVerdict(payload); parsed != nil {
		t.Fatalf("scope verdict accepted kind/target mismatch: %#v", parsed)
	}

	_, supported := understanding.Classify(understanding.QuestionUnderstanding{
		SchemaVersion: understanding.SchemaVersion, Question: "销售额是多少",
		MetricMentions: []understanding.MetricMention{{
			Text: "销售额", Span: understanding.Span{Start: 0, End: 3}, AggregationHint: understanding.AggregationDefault,
		}},
	})
	supported.Outcome = understanding.ScopeOutcomeOutOfScope
	payload, _ = json.Marshal(supported)
	if parsed := parsePublicScopeVerdict(payload); parsed != nil {
		t.Fatalf("scope verdict accepted type/outcome mismatch: %#v", parsed)
	}
}
