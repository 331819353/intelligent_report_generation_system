package askdatahttp

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	reportmodel "intelligent-report-generation-system/internal/report"
)

func TestVerifyReportSeedContextReleaseBranches(t *testing.T) {
	fixture := reportSeedFixture(t)
	tests := []struct {
		name     string
		selected askdata.ReleaseRef
		status   string
		wantErr  bool
	}{
		{"same active release", fixture.sourceRelease, "ACTIVE", false},
		{"same retained release", fixture.sourceRelease, "RETAINED", false},
		{"different current active release", fixture.currentRelease, "ACTIVE", false},
		{"different non-active release", fixture.currentRelease, "RETAINED", true},
		{"same release hash mismatch", askdata.ReleaseRef{ReleaseID: fixture.sourceRelease.ReleaseID, ContentHash: fixture.currentRelease.ContentHash}, "ACTIVE", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := fixture.input
			input.PinnedReleaseID = test.selected.ReleaseID
			seed, err := verifyReportSeedContext(fixture.definition, input, test.selected, test.status,
				fixture.normalized, fixture.canonical, fixture.irHash)
			if test.wantErr != errors.Is(err, ErrReportSeedInvalid) {
				t.Fatalf("err=%v, want invalid=%v", err, test.wantErr)
			}
			if !test.wantErr && (seed.PinnedReleaseID != test.selected.ReleaseID || seed.SemanticIRHash != fixture.irHash) {
				t.Fatalf("seed=%+v", seed)
			}
		})
	}
}

func TestVerifyReportSeedContextRejectsForgeryAndDatasetBinding(t *testing.T) {
	fixture := reportSeedFixture(t)
	forged := fixture.input
	forged.SemanticIR.Limit++
	forgedNormalized, forgedCanonical, forgedHash, err := ircontract.Canonicalize(forged.SemanticIR)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyReportSeedContext(fixture.definition, forged, fixture.sourceRelease, "ACTIVE",
		forgedNormalized, forgedCanonical, forgedHash); !errors.Is(err, ErrReportSeedInvalid) {
		t.Fatalf("forged IR error=%v", err)
	}

	datasetDefinition := fixture.definition
	datasetDefinition.Components[0].DataBinding = &reportmodel.DataBinding{BindingMode: reportmodel.BindingDatasetField}
	if _, err := verifyReportSeedContext(datasetDefinition, fixture.input, fixture.sourceRelease, "ACTIVE",
		fixture.normalized, fixture.canonical, fixture.irHash); !errors.Is(err, ErrReportSeedInvalid) {
		t.Fatalf("DATASET_FIELD error=%v", err)
	}
}

type reportSeedTestFixture struct {
	definition                    reportmodel.ReportDefinition
	input                         ReportSeedContextInput
	sourceRelease, currentRelease askdata.ReleaseRef
	normalized                    ircontract.SemanticIR
	canonical                     []byte
	irHash                        askdata.ContentHash
}

func reportSeedFixture(t *testing.T) reportSeedTestFixture {
	t.Helper()
	id := func(value string) askdata.ID { return askdata.ID(value) }
	sourceRelease := askdata.ReleaseRef{ReleaseID: id("11111111-1111-4111-8111-111111111111"), ContentHash: askdata.HashBytes([]byte("source"))}
	currentRelease := askdata.ReleaseRef{ReleaseID: id("22222222-2222-4222-8222-222222222222"), ContentHash: askdata.HashBytes([]byte("current"))}
	ir := ircontract.SemanticIR{
		IRVersion: ircontract.Version, SemanticReleaseID: sourceRelease.ReleaseID,
		SemanticContentHash: sourceRelease.ContentHash,
		DomainID:            id("33333333-3333-4333-8333-333333333333"),
		ModelVersionID:      id("44444444-4444-4444-8444-444444444444"),
		Metrics:             []ircontract.Metric{{MetricVersionID: id("55555555-5555-4555-8555-555555555555"), Alias: "sales"}},
		Sort:                []ircontract.Sort{}, Limit: 100, OtherPolicy: ircontract.OtherNone, TieBreaking: ircontract.TieIncludeAll,
	}
	normalized, canonical, hash, err := ircontract.Canonicalize(ir)
	if err != nil {
		t.Fatal(err)
	}
	componentID := id("component-1")
	versionID := id("66666666-6666-4666-8666-666666666666")
	definition := reportmodel.ReportDefinition{Components: []reportmodel.Component{{
		ID: componentID,
		DataBinding: &reportmodel.DataBinding{BindingMode: reportmodel.BindingSemanticIR,
			SemanticQueryRef: &reportmodel.SemanticQueryRef{SemanticReleaseID: sourceRelease.ReleaseID,
				SemanticContentHash: sourceRelease.ContentHash, SemanticIR: normalized}},
	}}}
	return reportSeedTestFixture{definition: definition, input: ReportSeedContextInput{
		ReportVersionID: versionID, ComponentID: componentID, SemanticIR: normalized,
		PinnedReleaseID: sourceRelease.ReleaseID,
	}, sourceRelease: sourceRelease, currentRelease: currentRelease, normalized: normalized,
		canonical: canonical, irHash: hash}
}
