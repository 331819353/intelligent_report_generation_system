package metriccandidate

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPublicationGeneratorUsesReservedPublishedVersionIdentity(t *testing.T) {
	version := publishedDatasetVersion(t, candidateDatasetDocument())
	version.DraftVersionID = "33333333-3333-4333-8333-333333333333"
	version.DraftRecordVersion = 2
	version.DatasetRecordVersion = 7

	preparation, err := NewPublicationGenerator(nil).GeneratePublicationCandidates(
		context.Background(), testTenantID, testActorID, version,
	)
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Total == 0 || preparation.Ready+preparation.Review+preparation.Blocked != preparation.Total {
		t.Fatalf("preparation=%#v", preparation)
	}
	var result ExtractionResult
	if err := json.Unmarshal(preparation.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.DatasetVersionID != version.ID {
		t.Fatalf("dataset version id=%q want=%q", result.DatasetVersionID, version.ID)
	}
	for _, candidate := range result.Candidates {
		if candidate.Definition.DatasetVersionID != version.ID {
			t.Fatalf("candidate definition=%#v", candidate.Definition)
		}
	}
}
