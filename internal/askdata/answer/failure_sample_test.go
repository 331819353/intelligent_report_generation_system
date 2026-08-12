package answer

import (
	"encoding/json"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

func TestFailureSamplesContainOnlyHashSpanCodeAndReleasedIDs(t *testing.T) {
	secret := "未经校验的客户原文 999 元"
	binding := BindingEvidence{Source: BindingSourceSemanticRelease, Objects: []ObjectEvidence{
		{ObjectID: "metric:revenue@v1", Kind: ObjectMetric, Bound: true},
		{ObjectID: "dimension:region@v1", Kind: ObjectDimension, Bound: true},
		{ObjectID: "member:east@v1", Kind: ObjectMember, Bound: true},
	}}
	report := VerifyReport{Failures: []VerifyFailure{{
		Reason: AnswerNumberUnverified, Span: shared.TextSpan{Start: 10, End: 13}, Text: "999",
	}}}
	samples := failureSamplesFor(report, NarrativeLayer{Summary: secret}, binding, 2)
	if len(samples) != 1 || samples[0].RejectedTextHash != askdata.HashBytes([]byte(secret)) ||
		len(samples[0].MetricVersionIDs) != 1 || len(samples[0].DimensionVersionIDs) != 1 ||
		samples[0].FailureCode != AnswerNumberUnverified || samples[0].Attempt != 2 {
		t.Fatalf("samples=%#v", samples)
	}
	raw, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "999") ||
		strings.Contains(string(raw), `"text"`) {
		t.Fatalf("failure sample leaked rejected text: %s", raw)
	}
}
