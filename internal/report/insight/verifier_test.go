package insight

import (
	"reflect"
	"testing"

	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

func TestAskDataAndReportUseIdenticalVerifyReport(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceSemanticIR)
	artifact := validInsightArtifact(t, bundle)
	policy := answer.DefaultReleaseVerifierPolicy(false)
	artifact.VerifierVersion = policy.VerifierVersion
	artifact.PolicyWordlistVersion = policy.PolicyWordlistVersion
	artifact.Citations = append(artifact.Citations, shared.NewContractCitation(
		insightSpanFor(t, artifact.Content.CanonicalText(), "销售额"), bundle.Facts[0].MetricVersionID,
	))
	artifact = artifact.Normalize()
	verifiable := VerifiableInsight{Artifact: artifact, Evidence: bundle}

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
		Source:  answer.BindingSourceSemanticRelease,
		Version: answer.BindingEvidenceVersion, SemanticReleaseID: *bundle.SemanticReleaseID,
		Objects: []answer.ObjectEvidence{{
			ObjectID: bundle.Facts[0].MetricVersionID, Kind: answer.ObjectMetric,
			Bound: true, Names: []string{"销售额"},
		}},
	}.Normalize()
	verifier, err := answer.NewVerifier(policy)
	if err != nil {
		t.Fatal(err)
	}
	narrative, err := verifiable.VerificationNarrative()
	if err != nil {
		t.Fatal(err)
	}
	askDataReport := verifier.VerifyNarrative(narrative, result, binding, compiler.ResolvedTimeSpec{})
	reportRuntimeReport, err := verifiable.Verify(verifier, result, binding, compiler.ResolvedTimeSpec{})
	if err != nil {
		t.Fatal(err)
	}
	if !askDataReport.Passed || !reflect.DeepEqual(askDataReport, reportRuntimeReport) {
		t.Fatalf("verification reports differ:\nAsk Data: %#v\nReport:   %#v", askDataReport, reportRuntimeReport)
	}
}
