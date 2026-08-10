package understanding

import (
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrInvalidSelectedDomain = errors.New("question selected domain is invalid")

// selectedDomainFromScope returns the business domain chosen before the
// question flow starts. Ask Data intentionally accepts a single-domain policy
// scope: domain selection belongs to the authenticated session, not to the
// language model or a probabilistic router.
func selectedDomainFromScope(scope askdata.PolicyScope) (askdata.ID, error) {
	if err := scope.Validate(); err != nil || len(scope.DomainIDs) != 1 {
		return "", ErrInvalidSelectedDomain
	}
	return scope.DomainIDs[0], nil
}

func selectedDomainPolicyEvidence(scope askdata.PolicyScope) (askdata.EvidenceRef, error) {
	domainID, err := selectedDomainFromScope(scope)
	if err != nil {
		return askdata.EvidenceRef{}, err
	}
	ref := askdata.EvidenceRef{
		EvidenceID:  askdata.ID("nlu-policy:" + string(scope.PolicyHash)),
		Kind:        askdata.EvidenceKindPolicy,
		SourceID:    scope.Release.ReleaseID,
		ContentHash: scope.PolicyHash,
	}
	if err := ref.Validate(); err != nil || domainID.Validate() != nil {
		return askdata.EvidenceRef{}, ErrInvalidSelectedDomain
	}
	return ref, nil
}

// pinProposalToSelectedDomain removes domain routing from the model boundary.
// An omitted or matching hypothesis is replaced by a deterministic score-1
// policy fact. A foreign hypothesis is rejected instead of being silently
// accepted or exposed as a user-facing domain choice.
func pinProposalToSelectedDomain(
	proposal UnderstandingProposal,
	scope askdata.PolicyScope,
) (UnderstandingProposal, error) {
	domainID, err := selectedDomainFromScope(scope)
	if err != nil {
		return UnderstandingProposal{}, err
	}
	for index, hypothesis := range proposal.Understanding.DomainHypotheses {
		if hypothesis.DomainID != domainID {
			return UnderstandingProposal{}, fmt.Errorf(
				"%w: domainHypotheses[%d] is outside the selected domain",
				ErrInvalidSelectedDomain, index,
			)
		}
	}
	policy, err := selectedDomainPolicyEvidence(scope)
	if err != nil {
		return UnderstandingProposal{}, err
	}
	proposal.Understanding.DomainHypotheses = []DomainHypothesis{{
		DomainID: domainID, Score: 1, EvidenceRefs: []askdata.EvidenceRef{policy},
	}}
	return proposal, nil
}

func validatePinnedSelectedDomain(
	understanding QuestionUnderstanding,
	scope askdata.PolicyScope,
) error {
	domainID, err := selectedDomainFromScope(scope)
	if err != nil {
		return err
	}
	policy, err := selectedDomainPolicyEvidence(scope)
	if err != nil {
		return err
	}
	if len(understanding.DomainHypotheses) != 1 {
		return fmt.Errorf("%w: exactly one selected-domain hypothesis is required", ErrInvalidSelectedDomain)
	}
	hypothesis := understanding.DomainHypotheses[0]
	if hypothesis.DomainID != domainID || hypothesis.Score != 1 ||
		len(hypothesis.EvidenceRefs) != 1 || hypothesis.EvidenceRefs[0] != policy {
		return fmt.Errorf("%w: domain hypothesis is not pinned to policy", ErrInvalidSelectedDomain)
	}
	return nil
}
