package answer

import "fmt"

// DegradedNarrativeHint is the stable, actionable presentation hint for an
// answer whose generated prose could not be verified. It deliberately avoids
// a generic apology: the governed L1 result remains available to the user.
const DegradedNarrativeHint = "本次未生成文字结论，请查看数据与口径。"

// ToStructured removes every byte of unverified prose while preserving the
// result-derived L1 layer and provenance. Callers must persist only the
// returned artifact; persisting the rejected candidate would cross the
// narrative trust boundary.
func ToStructured(candidate AnswerArtifact, attempts int) (AnswerArtifact, error) {
	if attempts < 0 || attempts > 2 {
		return AnswerArtifact{}, fmt.Errorf("attempts must be between 0 and 2")
	}
	result := candidate.Normalize()
	result.Layers.Narrative = NarrativeLayer{Findings: []string{}, Citations: nil}
	result.Verification.Attempts = attempts
	result.Verification.Passed = false
	result.Verification.Degraded = true
	result = result.Normalize()
	if err := result.Validate(); err != nil {
		return AnswerArtifact{}, fmt.Errorf("structured answer fallback: %w", err)
	}
	return result, nil
}
