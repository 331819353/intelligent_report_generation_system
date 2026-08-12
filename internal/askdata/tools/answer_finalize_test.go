package tools

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

func TestExactNarrativePassesSharedVerifier(t *testing.T) {
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: "row", Value: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	cell := answer.ResultCell{
		Ref:             shared.CellRef{RowKey: rowKey, ColumnKey: "sales_amount"},
		MetricVersionID: "metric:sales@v1",
		Value:           "128.5", ValueKind: answer.ValueNumber,
		Unit: "CNY", Currency: "CNY", DisplayPrecision: 1,
	}
	assertNarrativePasses(t, exactNarrative(cell, false), cell)
}

func TestEmptyResultNarrativePassesSharedVerifier(t *testing.T) {
	cells, cell, _, err := emptyResultCell()
	if err != nil || len(cells) != 1 {
		t.Fatalf("emptyResultCell() = %#v, %v", cells, err)
	}
	assertNarrativePasses(t, exactNarrative(cell, true), cell)
}

func assertNarrativePasses(t *testing.T, narrative answer.NarrativeLayer, cell answer.ResultCell) {
	t.Helper()
	policy := answer.DefaultReleaseVerifierPolicy(false)
	verifier, err := answer.NewVerifier(policy)
	if err != nil {
		t.Fatal(err)
	}
	referenceHash := askdata.HashBytes([]byte("result"))
	releaseID := askdata.ID("release:test@v1")
	report := verifier.VerifyNarrative(answer.VerificationNarrative{
		Text: narrative.CanonicalText(), Citations: narrative.Citations,
		VerifierVersion:       policy.VerifierVersion,
		PolicyWordlistVersion: policy.PolicyWordlistVersion,
		ReferenceHash:         referenceHash,
		Source:                answer.BindingSourceSemanticRelease,
		CatalogID:             releaseID,
	}, answer.ResultEvidence{
		Version: answer.ResultEvidenceVersion, ReferenceHash: referenceHash,
		Cells: []answer.ResultCell{cell}, Derivations: []answer.DerivationEvidence{},
	}.Normalize(), answer.BindingEvidence{
		Source:  answer.BindingSourceSemanticRelease,
		Version: answer.BindingEvidenceVersion, SemanticReleaseID: releaseID,
		Objects: []answer.ObjectEvidence{},
	}.Normalize(), compiler.ResolvedTimeSpec{})
	if !report.Passed {
		t.Fatalf("narrative failed verification: %#v", report)
	}
}
