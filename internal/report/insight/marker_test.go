package insight

import (
	"testing"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

func markerSources(t *testing.T) (EvidenceBundle, MarkerSources) {
	t.Helper()
	bundle := datasetEvidenceBundle(t)
	return bundle, MarkerSourcesFor(bundle, []MarkerObject{
		{ObjectID: askdata.ID("00000000-0000-4000-8000-0000000000c1"), Name: "营业收入"},
	})
}

func TestMarkersSubstituteVerifiedTextAndCiteIt(t *testing.T) {
	bundle, sources := markerSources(t)
	factID := bundle.Facts[0].ID

	content, citations, err := RenderMarkedContent(InsightContent{
		Summary: "最高渠道为 {{fact:" + string(factID) + "}}。",
	}, sources)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(citations) != 1 {
		t.Fatalf("expected one citation, got %d", len(citations))
	}
	// The substituted text is the fact's own value, so the model never chose it.
	runes := []rune(content.Summary)
	cited := string(runes[citations[0].TextSpan.Start:citations[0].TextSpan.End])
	if cited != bundle.Facts[0].CurrentValue+" "+bundle.Facts[0].Unit {
		t.Fatalf("citation must span the substituted value, got %q", cited)
	}
	if citations[0].Kind != shared.CitationResultCell {
		t.Fatalf("a fact citation must address a result cell, got %q", citations[0].Kind)
	}
}

func TestSpansAddressTheCanonicalJoinedText(t *testing.T) {
	bundle, sources := markerSources(t)
	factID := string(bundle.Facts[0].ID)

	content, citations, err := RenderMarkedContent(InsightContent{
		Summary:  "概览。",
		Findings: []string{"渠道 {{fact:" + factID + "}}。"},
		Actions:  []string{"关注 {{field:00000000-0000-4000-8000-0000000000c1}}。"},
	}, sources)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// CanonicalText joins parts with "\n"; spans must address that exact string
	// or every citation would silently point at the wrong words.
	canonical := []rune(content.CanonicalText())
	if len(citations) != 2 {
		t.Fatalf("expected two citations, got %d", len(citations))
	}
	first := string(canonical[citations[0].TextSpan.Start:citations[0].TextSpan.End])
	if first != bundle.Facts[0].CurrentValue+" "+bundle.Facts[0].Unit {
		t.Fatalf("finding citation is misaligned: %q", first)
	}
	second := string(canonical[citations[1].TextSpan.Start:citations[1].TextSpan.End])
	if second != "营业收入" {
		t.Fatalf("action citation is misaligned: %q", second)
	}
	if err := shared.ValidateCitations(content.CanonicalText(), citations); err != nil {
		t.Fatalf("rendered citations must be valid: %v", err)
	}
}

func TestSpansAreCodePointsNotBytes(t *testing.T) {
	bundle, sources := markerSources(t)
	// A multi-byte prefix would shift every byte-based offset.
	content, citations, err := RenderMarkedContent(InsightContent{
		Summary: "线上线下渠道对比：{{fact:" + string(bundle.Facts[0].ID) + "}}",
	}, sources)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	runes := []rune(content.Summary)
	if citations[0].TextSpan.End > len(runes) {
		t.Fatal("span exceeds the rune length, so it was computed in bytes")
	}
	cited := string(runes[citations[0].TextSpan.Start:citations[0].TextSpan.End])
	if cited != bundle.Facts[0].CurrentValue+" "+bundle.Facts[0].Unit {
		t.Fatalf("multi-byte prefix misaligned the span: %q", cited)
	}
	if utf8.RuneCountInString(content.Summary) != len(runes) {
		t.Fatal("rendered text is not valid UTF-8")
	}
}

// A marker naming something outside the evidence is refused outright: there is
// no verified text to substitute, so the narrative cannot be rendered at all.
func TestUnknownOrMalformedMarkersAreRefused(t *testing.T) {
	_, sources := markerSources(t)
	for name, text := range map[string]string{
		"unknown fact":  "值为 {{fact:NOT_A_FACT/001}}",
		"unknown field": "关注 {{field:00000000-0000-4000-8000-0000000000ff}}",
		"unknown kind":  "见 {{metric:revenue}}",
		"unclosed":      "值为 {{fact:x",
		"stray close":   "值为 }}",
	} {
		if _, _, err := RenderMarkedContent(InsightContent{Summary: text}, sources); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

// End to end: a rendered narrative passes the shared verifier unchanged.
func TestRenderedNarrativeVerifiesEndToEnd(t *testing.T) {
	bundle, sources := markerSources(t)
	content, citations, err := RenderMarkedContent(InsightContent{
		Summary: "最高渠道贡献 {{fact:" + string(bundle.Facts[0].ID) + "}}。",
	}, sources)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	hash, err := bundle.Hash()
	if err != nil {
		t.Fatal(err)
	}
	artifact := InsightArtifact{
		SchemaVersion: InsightSchemaVersion,
		ID:            askdata.ID("00000000-0000-4000-8000-0000000000a2"),
		EvidenceHash:  hash,
		PromptVersion: "report-insight-v1", ModelPolicy: "governed-default",
		VerifierVersion: answer.VerifierVersion, PolicyWordlistVersion: policyWordlistVersion,
		Content: content, Citations: citations, Status: InsightCurrent,
	}.Normalize()
	if err := artifact.ValidateAgainst(bundle); err != nil {
		t.Fatalf("rendered artifact must be valid: %v", err)
	}
	narrative, err := (VerifiableInsight{Artifact: artifact, Evidence: bundle}).VerificationNarrative()
	if err != nil {
		t.Fatalf("narrative: %v", err)
	}
	report := newVerifier(t).VerifyNarrative(
		narrative, datasetResultEvidence(bundle), datasetBindingEvidence(bundle), compiler.ResolvedTimeSpec{},
	)
	if !report.Passed {
		t.Fatalf("a marker-rendered narrative must verify, failures: %#v", report.Failures)
	}
}
