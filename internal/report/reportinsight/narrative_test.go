package reportinsight

import (
	"context"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/report/insight"
	"intelligent-report-generation-system/internal/report/store"
)

const policyWordlistVersion = "1.0.0"

type modelFunc func(context.Context, insight.NarrativePrompt) (insight.NarrativeDraft, error)

func (function modelFunc) GenerateInsightNarrative(
	ctx context.Context, prompt insight.NarrativePrompt,
) (insight.NarrativeDraft, error) {
	return function(ctx, prompt)
}

type recordingWriter struct{ stored int }

func (writer *recordingWriter) AppendArtifact(
	_ context.Context, _ store.Identity, reportID, componentID, evidenceID askdata.ID,
	artifact insight.InsightArtifact,
) (insight.ArtifactRecord, error) {
	writer.stored++
	return insight.ArtifactRecord{
		ID: artifact.ID, ReportID: reportID, ComponentID: componentID,
		EvidenceID: evidenceID, Artifact: artifact,
	}, nil
}

func testEvidence(t *testing.T) insight.EvidenceRecord {
	t.Helper()
	input, err := insight.BuildMethodInput(insight.ResultTable{
		Columns: []string{"channel", "revenue"},
		Rows:    [][]any{{"线上", "120"}, {"线下", "80"}},
	}, insight.MethodRoles{Dimension: "channel", Value: "revenue"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	asOf, _ := time.Parse(time.RFC3339Nano, "2026-08-12T00:00:00Z")
	bundle, err := insight.BuildEvidence(insight.NewRegistry(), insight.EvidenceRequest{
		SourceType:               insight.SourceDatasetQuery,
		DatasetVersionID:         askdata.ID("00000000-0000-4000-8000-000000000132"),
		DataSnapshotVersion:      "snapshot-1",
		QueryPlanHash:            askdata.ContentHash(strings.Repeat("a", 64)),
		FilterHash:               askdata.ContentHash(strings.Repeat("b", 64)),
		AsOf:                     asOf,
		ResolvedTimeRange:        insight.ResolvedTimeRange{Start: "2025-08-12T00:00:00Z", EndExclusive: "2026-08-12T00:00:00Z", Timezone: "UTC"},
		MetricVersionID:          askdata.ID("00000000-0000-4000-8000-000000000132"),
		Unit:                     "CNY",
		Method:                   insight.AnalysisTopN,
		EvidenceAlgorithmVersion: EvidenceAlgorithmVersion,
		Input:                    input,
	}, asOf.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return insight.EvidenceRecord{
		ID: askdata.ID("00000000-0000-4000-8000-0000000000e1"), Bundle: bundle,
	}
}

func newService(t *testing.T, model insight.NarrativeModel, writer ArtifactWriter) NarrativeService {
	t.Helper()
	verifier, err := answer.NewVerifier(answer.ReleaseVerifierPolicy{
		VerifierVersion: answer.VerifierVersion, PolicyWordlistVersion: policyWordlistVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return NarrativeService{
		Model: model, Verifier: verifier, Artifacts: writer,
		VerifierVersion: answer.VerifierVersion, PolicyWordlistVersion: policyWordlistVersion,
		NewID: func() askdata.ID { return askdata.ID("00000000-0000-4000-8000-0000000000a9") },
	}
}

func TestMarkerNarrativeIsGeneratedVerifiedAndStored(t *testing.T) {
	evidence := testEvidence(t)
	writer := &recordingWriter{}
	service := newService(t, modelFunc(func(_ context.Context, prompt insight.NarrativePrompt) (insight.NarrativeDraft, error) {
		content, citations, err := insight.RenderMarkedContent(insight.InsightContent{
			Summary: "最高渠道贡献 {{fact:" + string(prompt.Evidence.Facts[0].ID) + "}}。",
		}, insight.MarkerSourcesFor(prompt.Evidence, nil))
		return insight.NarrativeDraft{Content: content, Citations: citations}, err
	}), writer)

	record, report, err := service.Generate(
		t.Context(), store.Identity{}, "00000000-0000-4000-8000-000000000101",
		"00000000-0000-4000-8000-000000000201", evidence, nil,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !report.Passed {
		t.Fatalf("a marker narrative must verify, failures: %#v", report.Failures)
	}
	if report.Source != answer.BindingSourceDatasetVersion {
		t.Fatalf("verification grade must be recorded, got %q", report.Source)
	}
	if writer.stored != 1 || record.Artifact.Status != insight.InsightCurrent {
		t.Fatalf("a verified conclusion must be stored, got %#v", record)
	}
}

// The point of the whole chain: prose the evidence does not support is refused,
// and nothing reaches storage.
func TestUnsupportedNarrativeIsRefusedAndNotStored(t *testing.T) {
	evidence := testEvidence(t)
	writer := &recordingWriter{}
	service := newService(t, modelFunc(func(context.Context, insight.NarrativePrompt) (insight.NarrativeDraft, error) {
		// A number the model invented, with no citation behind it.
		return insight.NarrativeDraft{Content: insight.InsightContent{Summary: "营收达到 999 CNY。"}}, nil
	}), writer)

	_, report, err := service.Generate(
		t.Context(), store.Identity{}, "00000000-0000-4000-8000-000000000101",
		"00000000-0000-4000-8000-000000000201", evidence, nil,
	)
	if err == nil {
		t.Fatal("an uncited figure must not produce an artifact")
	}
	if report.Passed {
		t.Fatal("verification must not pass")
	}
	if writer.stored != 0 {
		t.Fatal("nothing may be stored when verification fails")
	}
}

// A marker naming something outside the evidence fails before verification.
func TestMarkerOutsideTheEvidenceFailsTheDraft(t *testing.T) {
	evidence := testEvidence(t)
	writer := &recordingWriter{}
	service := newService(t, modelFunc(func(_ context.Context, prompt insight.NarrativePrompt) (insight.NarrativeDraft, error) {
		content, citations, err := insight.RenderMarkedContent(insight.InsightContent{
			Summary: "值为 {{fact:NOT_A_FACT/001}}。",
		}, insight.MarkerSourcesFor(prompt.Evidence, nil))
		return insight.NarrativeDraft{Content: content, Citations: citations}, err
	}), writer)

	if _, _, err := service.Generate(
		t.Context(), store.Identity{}, "00000000-0000-4000-8000-000000000101",
		"00000000-0000-4000-8000-000000000201", evidence, nil,
	); err == nil {
		t.Fatal("an unresolvable marker must fail the draft")
	}
	if writer.stored != 0 {
		t.Fatal("nothing may be stored")
	}
}

// With a catalog supplied, a named object is anchored to a real field identity
// and cited as a contract reference — so a name the component does not bind is
// no longer free text.
func TestNamedObjectIsAnchoredToTheCatalog(t *testing.T) {
	evidence := testEvidence(t)
	objects := []insight.MarkerObject{{
		ObjectID: askdata.ID("revenue_total"), Name: "营业收入", Measure: true,
	}}
	writer := &recordingWriter{}
	service := newService(t, modelFunc(func(_ context.Context, prompt insight.NarrativePrompt) (insight.NarrativeDraft, error) {
		content, citations, err := insight.RenderMarkedContent(insight.InsightContent{
			Summary: "{{field:revenue_total}} 最高渠道为 {{fact:" + string(prompt.Evidence.Facts[0].ID) + "}}。",
		}, insight.MarkerSourcesFor(prompt.Evidence, objects))
		return insight.NarrativeDraft{Content: content, Citations: citations}, err
	}), writer)

	record, report, err := service.Generate(
		t.Context(), store.Identity{}, "00000000-0000-4000-8000-000000000101",
		"00000000-0000-4000-8000-000000000201", evidence, objects,
	)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !report.Passed {
		t.Fatalf("a catalog-anchored name must verify, failures: %#v", report.Failures)
	}
	if !strings.Contains(record.Artifact.Content.Summary, "营业收入") {
		t.Fatalf("the object name must be substituted, got %q", record.Artifact.Content.Summary)
	}
}

// A duplicate display name would make the whole catalog invalid, so it is
// dropped rather than failing every narrative for that component.
func TestDuplicateObjectNamesDoNotInvalidateTheCatalog(t *testing.T) {
	evidence := testEvidence(t)
	binding := BindingEvidenceOf(evidence.Bundle, []insight.MarkerObject{
		{ObjectID: askdata.ID("a_field"), Name: "营业收入", Measure: true},
		{ObjectID: askdata.ID("b_field"), Name: "营业收入", Measure: true},
	})
	if err := binding.Validate(); err != nil {
		t.Fatalf("catalog must stay valid: %v", err)
	}
	if len(binding.Objects) != 1 {
		t.Fatalf("expected the duplicate to be dropped, got %d objects", len(binding.Objects))
	}
}
