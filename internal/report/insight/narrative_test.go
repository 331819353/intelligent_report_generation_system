package insight

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/shared"
)

type narrativeModelFunc func(context.Context, NarrativePrompt) (NarrativeDraft, error)

func (function narrativeModelFunc) GenerateInsightNarrative(ctx context.Context, prompt NarrativePrompt) (NarrativeDraft, error) {
	return function(ctx, prompt)
}

func TestNarrativeGeneratorRequiresSharedVerifierBeforeReturningArtifact(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	policy := answer.DefaultReleaseVerifierPolicy(false)
	verifier, err := answer.NewVerifier(policy)
	if err != nil {
		t.Fatal(err)
	}
	result, binding := reportVerificationEvidence(t, bundle)
	valid := validInsightArtifact(t, bundle)
	valid.VerifierVersion = policy.VerifierVersion
	valid.PolicyWordlistVersion = policy.PolicyWordlistVersion
	valid.Citations = append(valid.Citations, shared.NewContractCitation(
		insightSpanFor(t, valid.Content.CanonicalText(), "销售额"), bundle.Facts[0].MetricVersionID,
	))

	seenPrompt := false
	generator := NarrativeGenerator{
		Verifier: verifier,
		Model: narrativeModelFunc(func(_ context.Context, prompt NarrativePrompt) (NarrativeDraft, error) {
			seenPrompt = prompt.Evidence.DatasetVersionID == bundle.DatasetVersionID && prompt.PromptVersion == valid.PromptVersion
			return NarrativeDraft{Content: valid.Content, Citations: valid.Citations}, nil
		}),
	}
	artifact, report, err := generator.Generate(context.Background(), NarrativeGenerationRequest{
		ArtifactID: "insight_generated", Evidence: bundle, ResultEvidence: result, BindingEvidence: binding,
		PromptVersion: valid.PromptVersion, ModelPolicy: valid.ModelPolicy,
		VerifierVersion: policy.VerifierVersion, PolicyWordlistVersion: policy.PolicyWordlistVersion,
	})
	if err != nil || !report.Passed || !seenPrompt || artifact.ID != "insight_generated" {
		t.Fatalf("Generate() = %#v, %#v, %v", artifact, report, err)
	}
}

func TestNarrativeGeneratorBlocksHallucinatedNumber(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	policy := answer.DefaultReleaseVerifierPolicy(false)
	verifier, err := answer.NewVerifier(policy)
	if err != nil {
		t.Fatal(err)
	}
	result, binding := reportVerificationEvidence(t, bundle)
	content := InsightContent{Summary: "销售额较上期增长999%。", Findings: []string{}, Risks: []string{}, Actions: []string{}}
	draft := NarrativeDraft{Content: content, Citations: []shared.Citation{
		shared.NewContractCitation(insightSpanFor(t, content.CanonicalText(), "销售额"), bundle.Facts[0].MetricVersionID),
		shared.NewResultCellCitation(insightSpanFor(t, content.CanonicalText(), "999%"), bundle.Facts[0].CellRefs[0]),
	}}
	generator := NarrativeGenerator{Verifier: verifier, Model: narrativeModelFunc(func(context.Context, NarrativePrompt) (NarrativeDraft, error) {
		return draft, nil
	})}
	artifact, report, err := generator.Generate(context.Background(), NarrativeGenerationRequest{
		ArtifactID: "insight_hallucinated", Evidence: bundle, ResultEvidence: result, BindingEvidence: binding,
		PromptVersion: "insight-v1", ModelPolicy: "evidence-only",
		VerifierVersion: policy.VerifierVersion, PolicyWordlistVersion: policy.PolicyWordlistVersion,
	})
	var verificationErr *NarrativeVerificationError
	if !errors.As(err, &verificationErr) || report.Passed || artifact.ID != "" {
		t.Fatalf("hallucinated Generate() = %#v, %#v, %v", artifact, report, err)
	}
}

func reportVerificationEvidence(t *testing.T, bundle EvidenceBundle) (answer.ResultEvidence, answer.BindingEvidence) {
	t.Helper()
	hash, err := bundle.Hash()
	if err != nil {
		t.Fatal(err)
	}
	currentRef := bundle.Facts[0].CellRefs[0]
	baselineRowKey, err := shared.FormatRowKey([]shared.RowKeyPart{
		{Key: "region", Value: "east"}, {Key: "month", Value: "2025-08"},
	})
	if err != nil {
		t.Fatal(err)
	}
	baselineRef := shared.CellRef{RowKey: baselineRowKey, ColumnKey: currentRef.ColumnKey}
	result := answer.ResultEvidence{
		Version: answer.ResultEvidenceVersion, ReferenceHash: hash,
		Cells: []answer.ResultCell{
			{Ref: currentRef, MetricVersionID: bundle.Facts[0].MetricVersionID, Value: bundle.Facts[0].CurrentValue, ValueKind: answer.ValueNumber, Unit: bundle.Facts[0].Unit, DisplayPrecision: 4},
			{Ref: baselineRef, MetricVersionID: bundle.Facts[0].MetricVersionID, Value: *bundle.Facts[0].PreviousValue, ValueKind: answer.ValueNumber, Unit: bundle.Facts[0].Unit, DisplayPrecision: 4},
		},
		Derivations: []answer.DerivationEvidence{{
			ID: "derivation:report-yoy", Left: currentRef, Right: baselineRef,
			AllowedRules: []answer.DerivationName{answer.DerivationYoYGrowth},
		}},
	}.Normalize()
	binding := answer.BindingEvidence{
		Version: answer.BindingEvidenceVersion, SemanticReleaseID: *bundle.SemanticReleaseID,
		Objects: []answer.ObjectEvidence{{
			ObjectID: bundle.Facts[0].MetricVersionID, Kind: answer.ObjectMetric,
			Bound: true, Names: []string{"销售额"},
		}},
	}.Normalize()
	return result, binding
}
