package askdatahttp

import (
	"encoding/json"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
)

func TestSavedQuestionSemanticIRAcceptsExactAnsweredArtifact(t *testing.T) {
	release := askdata.ReleaseRef{
		ReleaseID:   askdata.ID("11111111-1111-4111-8111-111111111111"),
		ContentHash: askdata.HashBytes([]byte("release")),
	}
	ir := ircontract.SemanticIR{
		IRVersion: ircontract.Version, SemanticReleaseID: release.ReleaseID,
		SemanticContentHash: release.ContentHash,
		DomainID:            askdata.ID("22222222-2222-4222-8222-222222222222"),
		ModelVersionID:      askdata.ID("33333333-3333-4333-8333-333333333333"),
		Metrics: []ircontract.Metric{{
			MetricVersionID: askdata.ID("44444444-4444-4444-8444-444444444444"), Alias: "gross_margin",
		}},
		Sort: []ircontract.Sort{}, Limit: 100,
		OtherPolicy: ircontract.OtherNone, TieBreaking: ircontract.TieIncludeAll,
	}
	normalized, raw, hash, err := ircontract.Canonicalize(ir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := orchestrator.ReplaySnapshot{
		Run: orchestrator.Run{State: orchestrator.StateAnswered, Release: release,
			Hashes: orchestrator.RunHashes{SemanticIR: hash}},
		Artifacts: []orchestrator.Artifact{{Type: orchestrator.ArtifactSemanticIR, Payload: json.RawMessage(raw)}},
	}
	got, err := SavedQuestionSemanticIR(snapshot)
	if err != nil {
		t.Fatalf("SavedQuestionSemanticIR() error = %v", err)
	}
	_, _, gotHash, err := ircontract.Canonicalize(got)
	if err != nil || gotHash != hash || got.SemanticReleaseID != normalized.SemanticReleaseID {
		t.Fatalf("resolved IR hash=%q release=%q err=%v", gotHash, got.SemanticReleaseID, err)
	}
}

func TestSavedQuestionSemanticIRRejectsUntrustedRunShapes(t *testing.T) {
	release := askdata.ReleaseRef{
		ReleaseID:   askdata.ID("11111111-1111-4111-8111-111111111111"),
		ContentHash: askdata.HashBytes([]byte("release")),
	}
	ir := ircontract.SemanticIR{
		IRVersion: ircontract.Version, SemanticReleaseID: release.ReleaseID,
		SemanticContentHash: release.ContentHash,
		DomainID:            askdata.ID("22222222-2222-4222-8222-222222222222"),
		ModelVersionID:      askdata.ID("33333333-3333-4333-8333-333333333333"),
		Metrics: []ircontract.Metric{{
			MetricVersionID: askdata.ID("44444444-4444-4444-8444-444444444444"), Alias: "gross_margin",
		}},
		Sort: []ircontract.Sort{}, Limit: 100,
		OtherPolicy: ircontract.OtherNone, TieBreaking: ircontract.TieIncludeAll,
	}
	_, raw, hash, err := ircontract.Canonicalize(ir)
	if err != nil {
		t.Fatal(err)
	}
	validArtifact := orchestrator.Artifact{Type: orchestrator.ArtifactSemanticIR, Payload: json.RawMessage(raw)}
	tests := []struct {
		name     string
		snapshot orchestrator.ReplaySnapshot
	}{
		{"unfinished run", orchestrator.ReplaySnapshot{Run: orchestrator.Run{State: orchestrator.StateExecuting, Release: release, Hashes: orchestrator.RunHashes{SemanticIR: hash}}, Artifacts: []orchestrator.Artifact{validArtifact}}},
		{"duplicate artifact", orchestrator.ReplaySnapshot{Run: orchestrator.Run{State: orchestrator.StateAnswered, Release: release, Hashes: orchestrator.RunHashes{SemanticIR: hash}}, Artifacts: []orchestrator.Artifact{validArtifact, validArtifact}}},
		{"hash mismatch", orchestrator.ReplaySnapshot{Run: orchestrator.Run{State: orchestrator.StateAnswered, Release: release, Hashes: orchestrator.RunHashes{SemanticIR: askdata.HashBytes([]byte("forged"))}}, Artifacts: []orchestrator.Artifact{validArtifact}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := SavedQuestionSemanticIR(test.snapshot); !errors.Is(err, ErrAddToReportNotAccepted) {
				t.Fatalf("error=%v, want %v", err, ErrAddToReportNotAccepted)
			}
		})
	}
}
