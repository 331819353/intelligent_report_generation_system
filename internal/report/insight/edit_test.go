package insight

import (
	"reflect"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

func TestHumanEditAndAllRegenerationModes(t *testing.T) {
	bundle := validEvidenceBundle(t, SourceDatasetQuery)
	previous := validInsightArtifact(t, bundle)
	editor := askdata.ID("00000000-0000-4000-8000-000000000099")
	edited, err := ApplyHumanEdit(previous, editor, time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC), InsightContent{
		Summary: "人工摘要", Findings: []string{"人工发现"}, Risks: []string{}, Actions: []string{},
	})
	if err != nil || !edited.HumanEdited || edited.HumanEditedBy == nil || *edited.HumanEditedBy != editor || len(edited.Citations) != 0 {
		t.Fatalf("ApplyHumanEdit() = %#v, %v", edited, err)
	}

	generated := validInsightArtifact(t, bundle)
	generated.Content.Findings = []string{"机器发现"}
	generated = generated.Normalize()
	if generated.Validate() != nil {
		t.Fatal("generated fixture is invalid")
	}
	preserved, err := Regenerate(edited, generated, RegeneratePreserve)
	if err != nil || !reflect.DeepEqual(preserved, edited) {
		t.Fatalf("preserve = %#v, %v", preserved, err)
	}
	replaced, err := Regenerate(edited, generated, RegenerateReplace)
	if err != nil || !reflect.DeepEqual(replaced, generated) || replaced.HumanEdited {
		t.Fatalf("replace = %#v, %v", replaced, err)
	}
	merged, err := Regenerate(edited, generated, RegenerateMerge)
	if err != nil || !merged.HumanEdited || len(merged.Content.Findings) != 2 ||
		merged.Content.Findings[0] != "人工发现" || merged.Content.Findings[1] != "机器发现" {
		t.Fatalf("merge = %#v, %v", merged, err)
	}
	if _, err := Regenerate(previous, generated, RegenerateMerge); err == nil {
		t.Fatal("merge without a human-edited artifact was accepted")
	}
}
