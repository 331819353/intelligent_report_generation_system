package answer

import (
	"fmt"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

type verifierFixture struct {
	artifact AnswerArtifact
	result   ResultEvidence
	binding  BindingEvidence
	timeSpec compiler.ResolvedTimeSpec
	cellRef  shared.CellRef
}

type citationFixture struct {
	kind       shared.CitationKind
	fragment   string
	ref        shared.CellRef
	contractID askdata.ID
}

func TestVerifierAcceptsChineseArabicAndDeclaredDerivedFacts(t *testing.T) {
	verifier := mustVerifier(t, false)
	fixture := baseVerifierFixture(t)
	tests := []struct {
		name      string
		text      string
		citations []citationFixture
	}{
		{
			name: "Chinese numeral", text: "一百二十八万元",
			citations: []citationFixture{{kind: shared.CitationResultCell, fragment: "一百二十八万元", ref: fixture.cellRef}},
		},
		{
			name: "scaled Arabic", text: "128万元",
			citations: []citationFixture{{kind: shared.CitationResultCell, fragment: "128万元", ref: fixture.cellRef}},
		},
		{
			name: "grouped Arabic", text: "1,280,000元",
			citations: []citationFixture{{kind: shared.CitationResultCell, fragment: "1,280,000元", ref: fixture.cellRef}},
		},
		{
			name: "time object value and YoY", text: "2026年8月销售额为128万元，同比上升28%。",
			citations: []citationFixture{
				{kind: shared.CitationTimeSpec, fragment: "2026年8月"},
				{kind: shared.CitationContract, fragment: "销售额", contractID: "metric:sales@v5"},
				{kind: shared.CitationResultCell, fragment: "128万元", ref: fixture.cellRef},
				{kind: shared.CitationResultCell, fragment: "28%", ref: fixture.cellRef},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact := narrativeArtifact(t, fixture, test.text, test.citations)
			report := verifier.Verify(artifact, fixture.result, fixture.binding, fixture.timeSpec)
			if !report.Passed || len(report.Failures) != 0 {
				t.Fatalf("Verify() = %#v", report)
			}
		})
	}
}

func TestVerifierDistinguishesPercentageFromPercentagePoints(t *testing.T) {
	verifier := mustVerifier(t, false)
	fixture := baseVerifierFixture(t)
	ratioRef := mustCellRef(t, "measure", "ratio", "ratio_value")
	pointRef := mustCellRef(t, "measure", "point", "point_value")
	fixture.result.Cells = append(fixture.result.Cells,
		ResultCell{Ref: ratioRef, MetricVersionID: "metric:ratio@v1", Value: "0.03", ValueKind: ValueRatio, Unit: "PERCENT", DisplayPrecision: 2},
		ResultCell{Ref: pointRef, MetricVersionID: "metric:ratio@v1", Value: "0.03", ValueKind: ValuePercentagePoint, Unit: "PERCENTAGE_POINT", DisplayPrecision: 2},
	)
	fixture.result = fixture.result.Normalize()
	if err := fixture.result.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		text string
		ref  shared.CellRef
		pass bool
	}{
		{name: "percent matches ratio", text: "3%", ref: ratioRef, pass: true},
		{name: "points match point cell", text: "3个百分点", ref: pointRef, pass: true},
		{name: "points are not percent", text: "3个百分点", ref: ratioRef, pass: false},
		{name: "percent is not points", text: "3%", ref: pointRef, pass: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := narrativeArtifact(t, fixture, test.text, []citationFixture{{
				kind: shared.CitationResultCell, fragment: test.text, ref: test.ref,
			}})
			report := verifier.Verify(artifact, fixture.result, fixture.binding, fixture.timeSpec)
			if report.Passed != test.pass {
				t.Fatalf("Verify() = %#v, want passed=%t", report, test.pass)
			}
			if !test.pass && !hasVerifyCode(report, AnswerNumberUnverified) {
				t.Fatalf("failure code = %#v", report.Failures)
			}
		})
	}
}

func TestVerifierUsesExactDisplayPrecisionTolerance(t *testing.T) {
	verifier := mustVerifier(t, false)
	fixture := baseVerifierFixture(t)
	mutateResultCell(t, &fixture, fixture.cellRef, func(cell *ResultCell) {
		cell.Value = "1.234"
		cell.DisplayPrecision = 2
	})
	for _, test := range []struct {
		value string
		pass  bool
	}{{value: "1.239", pass: true}, {value: "1.2391", pass: false}} {
		t.Run(test.value, func(t *testing.T) {
			artifact := narrativeArtifact(t, fixture, test.value, []citationFixture{{
				kind: shared.CitationResultCell, fragment: test.value, ref: fixture.cellRef,
			}})
			report := verifier.Verify(artifact, fixture.result, fixture.binding, fixture.timeSpec)
			if report.Passed != test.pass {
				t.Fatalf("Verify() = %#v, want passed=%t", report, test.pass)
			}
		})
	}
}

func TestVerifierRejectsCoincidentalCellCombinationWithoutDeclaration(t *testing.T) {
	verifier := mustVerifier(t, false)
	fixture := baseVerifierFixture(t)
	fixture.result.Derivations = []DerivationEvidence{}
	artifact := narrativeArtifact(t, fixture, "同比上升28%", []citationFixture{{
		kind: shared.CitationResultCell, fragment: "28%", ref: fixture.cellRef,
	}})
	report := verifier.Verify(artifact, fixture.result, fixture.binding, fixture.timeSpec)
	if report.Passed || !hasVerifyCode(report, AnswerNumberUnverified) {
		t.Fatalf("Verify() = %#v", report)
	}
}

func TestVerifierRequiredFailureCodesHaveAtLeastThreeNegativeShapes(t *testing.T) {
	verifier := mustVerifier(t, false)
	tests := []struct {
		name      string
		code      VerifyCode
		text      string
		citations func(verifierFixture) []citationFixture
		mutate    func(*verifierFixture)
	}{
		{name: "number wrong", code: AnswerNumberUnverified, text: "1200000", citations: resultCitation("1200000")},
		{name: "number uncited", code: AnswerNumberUnverified, text: "1280000", citations: noCitations},
		{name: "number undeclared derivation", code: AnswerNumberUnverified, text: "28%", citations: resultCitation("28%"), mutate: func(value *verifierFixture) { value.result.Derivations = []DerivationEvidence{} }},

		{name: "time old date", code: AnswerTimeMismatch, text: "2024-01-01", citations: timeCitation("2024-01-01")},
		{name: "time wrong quarter", code: AnswerTimeMismatch, text: "上季度", citations: timeCitation("上季度")},
		{name: "time wrong fiscal year", code: AnswerTimeMismatch, text: "本财年", citations: timeCitation("本财年")},

		{name: "unit count", code: AnswerUnitMismatch, text: "1280000件", citations: resultCitation("1280000件")},
		{name: "unit usd", code: AnswerUnitMismatch, text: "1280000USD", citations: resultCitation("1280000USD")},
		{name: "unit currency on count", code: AnswerUnitMismatch, text: "1280000元", citations: resultCitation("1280000元"), mutate: func(value *verifierFixture) {
			mutateResultCell(t, value, value.cellRef, func(cell *ResultCell) {
				cell.Unit, cell.Currency = "件", ""
			})
		}},

		{name: "object member", code: AnswerObjectHallucinated, text: "华南", citations: noCitations, mutate: addUnboundObject("member:south@v1", ObjectMember, "华南")},
		{name: "object metric", code: AnswerObjectHallucinated, text: "利润率", citations: noCitations, mutate: addUnboundObject("metric:margin@v1", ObjectMetric, "利润率")},
		{name: "object dimension", code: AnswerObjectHallucinated, text: "门店类型", citations: noCitations, mutate: addUnboundObject("dimension:store-type@v1", ObjectDimension, "门店类型")},

		{name: "forbidden causal", code: AnswerForbiddenAssertion, text: "由于促销，销售额增长。", citations: noCitations},
		{name: "forbidden prediction", code: AnswerForbiddenAssertion, text: "预计销售额增长。", citations: noCitations},
		{name: "forbidden advice", code: AnswerForbiddenAssertion, text: "应当扩大投放。", citations: noCitations},

		{name: "external benchmark", code: AnswerExternalFact, text: "高于行业平均。", citations: noCitations},
		{name: "external competitor", code: AnswerExternalFact, text: "领先竞品。", citations: noCitations},
		{name: "external experience", code: AnswerExternalFact, text: "根据经验会继续增长。", citations: noCitations},
	}
	counts := map[VerifyCode]int{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := baseVerifierFixture(t)
			if test.mutate != nil {
				test.mutate(&fixture)
			}
			artifact := narrativeArtifact(t, fixture, test.text, test.citations(fixture))
			report := verifier.Verify(artifact, fixture.result, fixture.binding, fixture.timeSpec)
			if report.Passed || !hasVerifyCode(report, test.code) {
				t.Fatalf("Verify() = %#v, want %s", report, test.code)
			}
			counts[test.code]++
		})
	}
	for _, code := range []VerifyCode{
		AnswerNumberUnverified, AnswerTimeMismatch, AnswerUnitMismatch,
		AnswerObjectHallucinated, AnswerForbiddenAssertion, AnswerExternalFact,
	} {
		if counts[code] < 3 {
			t.Fatalf("%s negative count = %d", code, counts[code])
		}
	}
}

func TestVerifierContributionPolicyAllowsOnlyGovernedWeakPhrase(t *testing.T) {
	fixture := baseVerifierFixture(t)
	weak := narrativeArtifact(t, fixture, "数值上贡献最大的是华东。", nil)
	blocked := mustVerifier(t, false).Verify(weak, fixture.result, fixture.binding, fixture.timeSpec)
	if !hasVerifyCode(blocked, AnswerForbiddenAssertion) {
		t.Fatalf("default report = %#v", blocked)
	}
	allowed := mustVerifier(t, true).Verify(weak, fixture.result, fixture.binding, fixture.timeSpec)
	if !allowed.Passed {
		t.Fatalf("contribution report = %#v", allowed)
	}
	strong := narrativeArtifact(t, fixture, "由于华东增长导致整体增长。", nil)
	strongReport := mustVerifier(t, true).Verify(strong, fixture.result, fixture.binding, fixture.timeSpec)
	if !hasVerifyCode(strongReport, AnswerForbiddenAssertion) {
		t.Fatalf("strong causal report = %#v", strongReport)
	}
}

func TestVerifierPinsImplementationAndWordlistVersions(t *testing.T) {
	policy := DefaultReleaseVerifierPolicy(false)
	policy.VerifierVersion = "answer-fact-verifier-v2"
	if _, err := NewVerifier(policy); err == nil {
		t.Fatal("unknown verifier version was accepted")
	}
	policy = DefaultReleaseVerifierPolicy(false)
	policy.PolicyWordlistVersion = "2.0.0"
	if _, err := NewVerifier(policy); err == nil {
		t.Fatal("unknown policy wordlist version was accepted")
	}
}

func baseVerifierFixture(t *testing.T) verifierFixture {
	t.Helper()
	cellRef := mustCellRef(t, "region", "east", "sales_amount")
	baselineRef := mustCellRef(t, "region", "east-baseline", "sales_amount")
	referenceHash := askdata.HashBytes([]byte("verified result"))
	result := ResultEvidence{
		Version: ResultEvidenceVersion, ReferenceHash: referenceHash,
		Cells: []ResultCell{
			{Ref: cellRef, MetricVersionID: "metric:sales@v5", Value: "1280000", ValueKind: ValueNumber, Unit: "CNY", Currency: "CNY", DisplayPrecision: 2},
			{Ref: baselineRef, MetricVersionID: "metric:sales@v5", Value: "1000000", ValueKind: ValueNumber, Unit: "CNY", Currency: "CNY", DisplayPrecision: 2},
		},
		Derivations: []DerivationEvidence{{
			ID: "derivation:yoy", Left: cellRef, Right: baselineRef,
			AllowedRules: []DerivationName{DerivationYoYGrowth},
		}},
	}.Normalize()
	binding := BindingEvidence{
		Version: BindingEvidenceVersion, SemanticReleaseID: "release:v1",
		Objects: []ObjectEvidence{{ObjectID: "metric:sales@v5", Kind: ObjectMetric, Bound: true, Names: []string{"销售额"}}},
	}.Normalize()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	timeSpec := compiler.ResolvedTimeSpec{
		RequestedPeriod: "CURRENT_MONTH", Grain: "MONTH", PolicyApplied: "MTD", PolicySource: "TIME_CONTRACT",
		ResolvedStart:               time.Date(2026, 8, 1, 0, 0, 0, 0, location),
		ResolvedEndExclusive:        time.Date(2026, 8, 7, 0, 0, 0, 0, location),
		DataAvailableThrough:        time.Date(2026, 8, 6, 10, 30, 0, 0, location),
		TruncatedByDataAvailability: true, Timezone: location.String(),
		Comparison: &compiler.ResolvedComparison{
			Type: "YEAR_OVER_YEAR", Periods: 1, Alignment: "SAME_DAY_COUNT",
			ResolvedStart:        time.Date(2025, 8, 1, 0, 0, 0, 0, location),
			ResolvedEndExclusive: time.Date(2025, 8, 7, 0, 0, 0, 0, location),
		},
	}
	artifact := validAnswerArtifact(t)
	policy := DefaultReleaseVerifierPolicy(false)
	artifact.Verification.VerifierVersion = policy.VerifierVersion
	artifact.Verification.PolicyWordlistVersion = policy.PolicyWordlistVersion
	artifact.Provenance.ResultHash = referenceHash
	artifact.Provenance.SemanticReleaseID = binding.SemanticReleaseID
	return verifierFixture{artifact: artifact, result: result, binding: binding, timeSpec: timeSpec, cellRef: cellRef}
}

func narrativeArtifact(t *testing.T, fixture verifierFixture, text string, citations []citationFixture) AnswerArtifact {
	t.Helper()
	artifact := fixture.artifact
	artifact.Layers.Narrative = NarrativeLayer{Summary: text, Findings: []string{}, Citations: []shared.Citation{}}
	for _, citation := range citations {
		span := spanFor(t, text, citation.fragment)
		switch citation.kind {
		case shared.CitationResultCell:
			artifact.Layers.Narrative.Citations = append(artifact.Layers.Narrative.Citations, shared.NewResultCellCitation(span, citation.ref))
		case shared.CitationContract:
			artifact.Layers.Narrative.Citations = append(artifact.Layers.Narrative.Citations, shared.NewContractCitation(span, citation.contractID))
		case shared.CitationTimeSpec:
			artifact.Layers.Narrative.Citations = append(artifact.Layers.Narrative.Citations, shared.NewTimeSpecCitation(span))
		default:
			t.Fatalf("unsupported citation kind %s", citation.kind)
		}
	}
	artifact.Layers.Narrative.Citations = shared.NormalizeCitations(artifact.Layers.Narrative.Citations)
	if err := artifact.Validate(); err != nil {
		t.Fatalf("artifact fixture: %v", err)
	}
	return artifact
}

func mustVerifier(t *testing.T, contributionMode bool) *Verifier {
	t.Helper()
	verifier, err := NewVerifier(DefaultReleaseVerifierPolicy(contributionMode))
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func mustCellRef(t *testing.T, key, value, column string) shared.CellRef {
	t.Helper()
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: key, Value: value}})
	if err != nil {
		t.Fatal(err)
	}
	return shared.CellRef{RowKey: rowKey, ColumnKey: column}
}

func hasVerifyCode(report VerifyReport, code VerifyCode) bool {
	for _, failure := range report.Failures {
		if failure.Reason == code {
			return true
		}
	}
	return false
}

func resultCitation(fragment string) func(verifierFixture) []citationFixture {
	return func(value verifierFixture) []citationFixture {
		return []citationFixture{{kind: shared.CitationResultCell, fragment: fragment, ref: value.cellRef}}
	}
}

func timeCitation(fragment string) func(verifierFixture) []citationFixture {
	return func(verifierFixture) []citationFixture {
		return []citationFixture{{kind: shared.CitationTimeSpec, fragment: fragment}}
	}
}

func noCitations(verifierFixture) []citationFixture { return nil }

func addUnboundObject(id string, kind ObjectKind, name string) func(*verifierFixture) {
	return func(value *verifierFixture) {
		value.binding.Objects = append(value.binding.Objects, ObjectEvidence{
			ObjectID: askdata.ID(id), Kind: kind, Bound: false, Names: []string{name},
		})
		value.binding = value.binding.Normalize()
	}
}

func mutateResultCell(t *testing.T, fixture *verifierFixture, ref shared.CellRef, mutate func(*ResultCell)) {
	t.Helper()
	for index := range fixture.result.Cells {
		if fixture.result.Cells[index].Ref == ref {
			mutate(&fixture.result.Cells[index])
			return
		}
	}
	t.Fatalf("result cell %#v not found", ref)
}

func ExampleVerifyFailure() {
	failure := VerifyFailure{
		Element: ElementAssertion, Text: "预计", Span: shared.TextSpan{Start: 0, End: 2},
		Reason: AnswerForbiddenAssertion, Expected: []string{"evidence-limited descriptive wording"},
	}
	fmt.Println(failure.Reason)
	// Output: ANSWER_FORBIDDEN_ASSERTION
}
