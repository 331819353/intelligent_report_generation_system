package insight

import (
	"errors"

	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/shared"
)

// VerifiableInsight adapts the report artifact and its immutable Evidence
// Bundle to the same ANS-002 Verifier used by Ask Data. It contains no report
// runtime or storage behavior and cannot bypass Evidence Bundle validation.
type VerifiableInsight struct {
	Artifact InsightArtifact
	Evidence EvidenceBundle
}

func (value VerifiableInsight) VerificationNarrative() (answer.VerificationNarrative, error) {
	if value.Artifact.ValidateAgainst(value.Evidence) != nil || value.Artifact.HumanEdited ||
		value.Artifact.Status != InsightCurrent {
		return answer.VerificationNarrative{}, errors.New("insight is not eligible for generated narrative verification")
	}
	hash, err := value.Evidence.Hash()
	if err != nil || hash != value.Artifact.EvidenceHash {
		return answer.VerificationNarrative{}, errors.New("insight evidence hash is invalid")
	}
	source, catalogID := value.Evidence.BindingCatalog()
	return answer.VerificationNarrative{
		Text:                  value.Artifact.Content.CanonicalText(),
		Citations:             shared.NormalizeCitations(value.Artifact.Citations),
		VerifierVersion:       value.Artifact.VerifierVersion,
		PolicyWordlistVersion: value.Artifact.PolicyWordlistVersion,
		ReferenceHash:         hash,
		Source:                source,
		CatalogID:             catalogID,
	}, nil
}

// Verify enters the exact ANS-002 implementation used by Ask Data after the
// report artifact has been bound to its immutable Evidence Bundle.
func (value VerifiableInsight) Verify(
	verifier *answer.Verifier,
	result answer.ResultEvidence,
	binding answer.BindingEvidence,
	timeSpec compiler.ResolvedTimeSpec,
) (answer.VerifyReport, error) {
	narrative, err := value.VerificationNarrative()
	if err != nil {
		return answer.VerifyReport{}, err
	}
	return verifier.VerifyNarrative(narrative, result, binding, timeSpec), nil
}
