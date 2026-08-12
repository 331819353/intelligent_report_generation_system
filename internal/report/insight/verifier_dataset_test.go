package insight

import (
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

// policyWordlistVersion is the version the verifier loads from its embedded
// wordlist. NewVerifier rejects any other value.
const policyWordlistVersion = "1.0.0"

// A dataset-sourced report insight declares the dataset version as its object
// catalog, so the shared verifier can check it the same way it checks an
// Ask Data answer bound to a semantic release.
func TestDatasetSourcedEvidenceDeclaresItsDatasetCatalog(t *testing.T) {
	bundle := datasetEvidenceBundle(t)
	source, catalogID := bundle.BindingCatalog()
	if source != answer.BindingSourceDatasetVersion {
		t.Fatalf("expected a dataset catalog, got %q", source)
	}
	if catalogID != bundle.DatasetVersionID {
		t.Fatalf("catalog must be the dataset version, got %q", catalogID)
	}
}

func TestDatasetSourcedNarrativePassesTheSharedVerifier(t *testing.T) {
	bundle := datasetEvidenceBundle(t)
	artifact := datasetArtifact(t, bundle)

	narrative, err := (VerifiableInsight{Artifact: artifact, Evidence: bundle}).VerificationNarrative()
	if err != nil {
		t.Fatalf("narrative adaptation: %v", err)
	}
	report := newVerifier(t).VerifyNarrative(
		narrative, datasetResultEvidence(bundle), datasetBindingEvidence(bundle), compiler.ResolvedTimeSpec{},
	)
	if !report.Passed {
		t.Fatalf("a cited dataset-sourced narrative must verify, failures: %#v", report.Failures)
	}
	// The grade is recorded so a dataset-verified conclusion is never presented
	// as one backed by a certified semantic metric.
	if report.Source != answer.BindingSourceDatasetVersion {
		t.Fatalf("verification grade must be recorded, got %q", report.Source)
	}
}

// An uncited number is still a hallucination risk regardless of catalog source.
func TestDatasetSourcedNarrativeStillRequiresCitations(t *testing.T) {
	bundle := datasetEvidenceBundle(t)
	artifact := datasetArtifact(t, bundle)
	artifact.Content.Summary = "120 与 999"
	artifact = artifact.Normalize()

	narrative, err := (VerifiableInsight{Artifact: artifact, Evidence: bundle}).VerificationNarrative()
	if err != nil {
		t.Fatalf("narrative adaptation: %v", err)
	}
	report := newVerifier(t).VerifyNarrative(
		narrative, datasetResultEvidence(bundle), datasetBindingEvidence(bundle), compiler.ResolvedTimeSpec{},
	)
	if report.Passed {
		t.Fatal("a number with no matching evidence must not verify")
	}
}

// The catalog a narrative was written against must be the one it is checked
// against, so a dataset narrative cannot borrow a semantic release's authority.
func TestNarrativeCannotBeVerifiedAgainstADifferentCatalog(t *testing.T) {
	bundle := datasetEvidenceBundle(t)
	artifact := datasetArtifact(t, bundle)
	narrative, err := (VerifiableInsight{Artifact: artifact, Evidence: bundle}).VerificationNarrative()
	if err != nil {
		t.Fatalf("narrative adaptation: %v", err)
	}
	report := newVerifier(t).VerifyNarrative(narrative, datasetResultEvidence(bundle), answer.BindingEvidence{
		Version:           answer.BindingEvidenceVersion,
		Source:            answer.BindingSourceSemanticRelease,
		SemanticReleaseID: askdata.ID("00000000-0000-4000-8000-0000000000b2"),
		Objects:           []answer.ObjectEvidence{},
	}.Normalize(), compiler.ResolvedTimeSpec{})
	if report.Passed {
		t.Fatal("a dataset narrative must not verify against a semantic catalog")
	}
}

// Both identity fields set, or neither, leaves object names unanchored.
func TestBindingEvidenceRequiresExactlyOneCatalogIdentity(t *testing.T) {
	base := answer.BindingEvidence{Version: answer.BindingEvidenceVersion, Objects: []answer.ObjectEvidence{}}
	for name, mutate := range map[string]func(answer.BindingEvidence) answer.BindingEvidence{
		"no source": func(value answer.BindingEvidence) answer.BindingEvidence { return value },
		"dataset source without id": func(value answer.BindingEvidence) answer.BindingEvidence {
			value.Source = answer.BindingSourceDatasetVersion
			return value
		},
		"semantic source without id": func(value answer.BindingEvidence) answer.BindingEvidence {
			value.Source = answer.BindingSourceSemanticRelease
			return value
		},
		"both identities": func(value answer.BindingEvidence) answer.BindingEvidence {
			value.Source = answer.BindingSourceDatasetVersion
			value.DatasetVersionID = askdata.ID("00000000-0000-4000-8000-000000000132")
			value.SemanticReleaseID = askdata.ID("00000000-0000-4000-8000-0000000000b2")
			return value
		},
	} {
		if mutate(base).Validate() == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

func newVerifier(t *testing.T) *answer.Verifier {
	t.Helper()
	verifier, err := answer.NewVerifier(answer.ReleaseVerifierPolicy{
		VerifierVersion: answer.VerifierVersion, PolicyWordlistVersion: policyWordlistVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

// datasetArtifact writes the smallest defensible narrative: one number, cited
// to the exact cell it came from.
func datasetArtifact(t *testing.T, bundle EvidenceBundle) InsightArtifact {
	t.Helper()
	hash, err := bundle.Hash()
	if err != nil {
		t.Fatal(err)
	}
	value := bundle.Facts[0].CurrentValue
	artifact := InsightArtifact{
		SchemaVersion: InsightSchemaVersion,
		ID:            askdata.ID("00000000-0000-4000-8000-0000000000a1"),
		EvidenceHash:  hash,
		PromptVersion: "report-insight-v1", ModelPolicy: "governed-default",
		VerifierVersion: answer.VerifierVersion, PolicyWordlistVersion: policyWordlistVersion,
		Content: InsightContent{Summary: value},
		Citations: []shared.Citation{shared.NewResultCellCitation(
			shared.TextSpan{Start: 0, End: len([]rune(value))}, bundle.Facts[0].CellRefs[0],
		)},
		Status: InsightCurrent,
	}.Normalize()
	if err := artifact.ValidateAgainst(bundle); err != nil {
		t.Fatalf("test artifact is invalid: %v", err)
	}
	return artifact
}

// datasetResultEvidence adapts a bundle to the verifier's result contract.
func datasetResultEvidence(bundle EvidenceBundle) answer.ResultEvidence {
	cells := make([]answer.ResultCell, 0, len(bundle.Facts))
	seen := map[string]bool{}
	for _, fact := range bundle.Facts {
		for _, ref := range fact.CellRefs {
			key := ref.RowKey + "\x00" + ref.ColumnKey
			if seen[key] {
				continue
			}
			seen[key] = true
			cells = append(cells, answer.ResultCell{
				Ref: ref, MetricVersionID: fact.MetricVersionID, Value: fact.CurrentValue,
				ValueKind: answer.ValueNumber, Unit: fact.Unit,
			})
		}
	}
	hash, _ := bundle.Hash()
	return answer.ResultEvidence{
		Version: answer.ResultEvidenceVersion, ReferenceHash: hash,
		Cells: cells, Derivations: []answer.DerivationEvidence{},
	}.Normalize()
}

func datasetBindingEvidence(bundle EvidenceBundle) answer.BindingEvidence {
	source, catalogID := bundle.BindingCatalog()
	return answer.BindingEvidence{
		Version: answer.BindingEvidenceVersion, Source: source, DatasetVersionID: catalogID,
		Objects: []answer.ObjectEvidence{},
	}.Normalize()
}

func datasetEvidenceBundle(t *testing.T) EvidenceBundle {
	t.Helper()
	input, err := BuildMethodInput(ResultTable{
		Columns: []string{"channel", "revenue"},
		Rows:    [][]any{{"线上", "120"}, {"线下", "80"}},
	}, MethodRoles{Dimension: "channel", Value: "revenue"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := BuildEvidence(NewRegistry(), EvidenceRequest{
		SourceType:               SourceDatasetQuery,
		DatasetVersionID:         askdata.ID("00000000-0000-4000-8000-000000000132"),
		DataSnapshotVersion:      "snapshot-1",
		QueryPlanHash:            askdata.ContentHash(strings.Repeat("a", 64)),
		FilterHash:               askdata.ContentHash(strings.Repeat("b", 64)),
		AsOf:                     mustTime(t, "2026-08-12T00:00:00Z"),
		ResolvedTimeRange:        ResolvedTimeRange{Start: "2025-08-12T00:00:00Z", EndExclusive: "2026-08-12T00:00:00Z", Timezone: "UTC"},
		MetricVersionID:          askdata.ID("00000000-0000-4000-8000-000000000132"),
		Unit:                     "CNY",
		Method:                   AnalysisTopN,
		EvidenceAlgorithmVersion: "report-evidence-derive-1.0.0",
		Input:                    input,
	}, mustTime(t, "2026-08-12T00:00:01Z"))
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}
