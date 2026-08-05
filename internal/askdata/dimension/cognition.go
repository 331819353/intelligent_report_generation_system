package dimension

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type ProposalSource string
type AnomalyType string
type MergeRisk string

const (
	ProposalDeterministic ProposalSource = "DETERMINISTIC_EQUIVALENCE"
	ProposalLLM           ProposalSource = "LLM_REVIEW"

	AnomalyAliasCandidate   AnomalyType = "ALIAS_CANDIDATE"
	AnomalyClusterCandidate AnomalyType = "CLUSTER_CANDIDATE"
	AnomalyHierarchy        AnomalyType = "HIERARCHY_ANOMALY"
	AnomalySentinel         AnomalyType = "SENTINEL_CANDIDATE"

	RiskLow    MergeRisk = "LOW"
	RiskMedium MergeRisk = "MEDIUM"
	RiskHigh   MergeRisk = "HIGH"
)

// AnomalyProposal contains only stable member IDs and evidence references;
// sensitive raw values never need to enter an LLM context.
type AnomalyProposal struct {
	Source               ProposalSource        `json:"source"`
	Type                 AnomalyType           `json:"type"`
	DimensionVersionID   askdata.ID            `json:"dimensionVersionId"`
	MemberVersionIDs     []askdata.ID          `json:"memberVersionIds"`
	SuggestedCanonicalID askdata.ID            `json:"suggestedCanonicalId"`
	Risk                 MergeRisk             `json:"risk"`
	Summary              string                `json:"summary"`
	EvidenceRefs         []askdata.EvidenceRef `json:"evidenceRefs"`
	Sensitivity          registry.Sensitivity  `json:"sensitivity"`
}

func (proposal AnomalyProposal) Validate() error {
	if proposal.Source != ProposalDeterministic && proposal.Source != ProposalLLM {
		return errors.New("proposal source is invalid")
	}
	if proposal.Type != AnomalyAliasCandidate && proposal.Type != AnomalyClusterCandidate &&
		proposal.Type != AnomalyHierarchy && proposal.Type != AnomalySentinel {
		return errors.New("anomaly type is invalid")
	}
	if proposal.Risk != RiskLow && proposal.Risk != RiskMedium && proposal.Risk != RiskHigh {
		return errors.New("merge risk is invalid")
	}
	if err := proposal.DimensionVersionID.Validate(); err != nil {
		return fmt.Errorf("dimensionVersionId: %w", err)
	}
	if len(proposal.MemberVersionIDs) < 1 || len(proposal.MemberVersionIDs) > 64 {
		return errors.New("memberVersionIds count is invalid")
	}
	seen := map[askdata.ID]struct{}{}
	for index, memberID := range proposal.MemberVersionIDs {
		if err := memberID.Validate(); err != nil {
			return fmt.Errorf("memberVersionIds[%d]: %w", index, err)
		}
		if _, duplicate := seen[memberID]; duplicate {
			return fmt.Errorf("memberVersionIds[%d] is duplicated", index)
		}
		seen[memberID] = struct{}{}
	}
	if err := proposal.SuggestedCanonicalID.Validate(); err != nil {
		return fmt.Errorf("suggestedCanonicalId: %w", err)
	}
	if _, included := seen[proposal.SuggestedCanonicalID]; !included {
		return errors.New("suggestedCanonicalId must be one of memberVersionIds")
	}
	if strings.TrimSpace(proposal.Summary) == "" || !utf8.ValidString(proposal.Summary) || utf8.RuneCountInString(proposal.Summary) > 1_000 {
		return errors.New("summary is invalid")
	}
	if len(proposal.EvidenceRefs) < 1 || len(proposal.EvidenceRefs) > 64 {
		return errors.New("evidenceRefs count is invalid")
	}
	for index, evidence := range proposal.EvidenceRefs {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("evidenceRefs[%d]: %w", index, err)
		}
	}
	if !validSensitivity(proposal.Sensitivity) {
		return errors.New("sensitivity is invalid")
	}
	if proposal.Source == ProposalLLM &&
		(proposal.Sensitivity == registry.SensitivityConfidential || proposal.Sensitivity == registry.SensitivityRestricted) {
		return errors.New("sensitive member values cannot be reviewed by an LLM")
	}
	return nil
}

// CanAutoApply is deliberately narrow. LLM proposals, fuzzy clusters,
// hierarchy changes and every medium/high-risk merge always require a human
// review and a later semantic release.
func (proposal AnomalyProposal) CanAutoApply() bool {
	return proposal.Validate() == nil && proposal.Source == ProposalDeterministic &&
		proposal.Type == AnomalyAliasCandidate && proposal.Risk == RiskLow &&
		(proposal.Sensitivity == registry.SensitivityPublic || proposal.Sensitivity == registry.SensitivityInternal)
}
