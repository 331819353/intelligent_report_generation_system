package insight

import (
	"errors"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

type RegenerationMode string

const (
	RegeneratePreserve RegenerationMode = "PRESERVE"
	RegenerateMerge    RegenerationMode = "MERGE"
	RegenerateReplace  RegenerationMode = "REPLACE"
)

func ApplyHumanEdit(artifact InsightArtifact, editor askdata.ID, editedAt time.Time, content InsightContent) (InsightArtifact, error) {
	if err := editor.Validate(); err != nil || content.Validate() != nil {
		return InsightArtifact{}, errors.New("human insight edit is invalid")
	}
	result := artifact.Normalize()
	timestamp := editedAt.Format(time.RFC3339Nano)
	result.Content = content
	result.Citations = nil
	result.HumanEdited = true
	result.HumanEditedBy = &editor
	result.HumanEditedAt = &timestamp
	result.Status = InsightCurrent
	return result.Normalize(), result.Validate()
}

func Regenerate(previous, generated InsightArtifact, mode RegenerationMode) (InsightArtifact, error) {
	if err := generated.Validate(); err != nil || generated.HumanEdited {
		return InsightArtifact{}, errors.New("generated insight is invalid")
	}
	switch mode {
	case RegeneratePreserve:
		return previous, previous.Validate()
	case RegenerateReplace:
		return generated, nil
	case RegenerateMerge:
		result := generated.Normalize()
		result.Content.Findings = append(append([]string(nil), previous.Content.Findings...), generated.Content.Findings...)
		result.Content.Risks = append(append([]string(nil), previous.Content.Risks...), generated.Content.Risks...)
		result.Content.Actions = append(append([]string(nil), previous.Content.Actions...), generated.Content.Actions...)
		// A merged artifact includes human prose, so it is explicitly marked as
		// human edited and no longer claims generated citation verification.
		result.Citations = nil
		result.HumanEdited = previous.HumanEdited
		result.HumanEditedBy = previous.HumanEditedBy
		result.HumanEditedAt = previous.HumanEditedAt
		if !result.HumanEdited {
			return InsightArtifact{}, errors.New("MERGE requires a human-edited previous artifact")
		}
		return result, result.Validate()
	default:
		return InsightArtifact{}, errors.New("unknown regeneration mode")
	}
}
