package datarequest

import (
	"errors"
	"sort"
	"strings"
)

var ErrSecurityCosignRequired = errors.New("data request security cosign is required")

type SensitivityFact struct {
	SourceID    string
	Sensitivity Sensitivity
}

// DeriveSensitivity returns the strict maximum. The caller must provide a
// fact for every requested field and semantic dimension; an empty or unknown
// fact set fails closed instead of silently defaulting to INTERNAL.
func DeriveSensitivity(facts []SensitivityFact) (Sensitivity, error) {
	if len(facts) == 0 || len(facts) > 256 {
		return "", ErrInvalidRequest
	}
	seen := map[string]struct{}{}
	maximum := SensitivityPublic
	for _, fact := range facts {
		fact.SourceID = strings.TrimSpace(fact.SourceID)
		if !boundedText(fact.SourceID, 1, 256) || sensitivityRank(fact.Sensitivity) < 0 {
			return "", ErrInvalidRequest
		}
		if _, duplicate := seen[fact.SourceID]; duplicate {
			return "", ErrInvalidRequest
		}
		seen[fact.SourceID] = struct{}{}
		if sensitivityRank(fact.Sensitivity) > sensitivityRank(maximum) {
			maximum = fact.Sensitivity
		}
	}
	return maximum, nil
}

func RequiresSecurityCosign(sensitivity Sensitivity) bool {
	return sensitivity == SensitivityConfidential || sensitivity == SensitivityRestricted
}

type ApprovalPolicyInput struct {
	Sensitivity      Sensitivity
	RequesterUserID  string
	ApproverUserID   string
	SecurityCosignID string
	ActiveMemberIDs  []string
}

// ValidateApproval enforces independent security review for high-sensitivity
// detail delivery. The cosigner must be an active domain member and cannot be
// the requester or the approver occupying the workflow approval seat.
func ValidateApproval(input ApprovalPolicyInput) error {
	if sensitivityRank(input.Sensitivity) < 0 || strings.TrimSpace(input.RequesterUserID) == "" ||
		strings.TrimSpace(input.ApproverUserID) == "" {
		return ErrInvalidRequest
	}
	if !RequiresSecurityCosign(input.Sensitivity) {
		if strings.TrimSpace(input.SecurityCosignID) == "" {
			return nil
		}
	}
	cosigner := strings.TrimSpace(input.SecurityCosignID)
	if cosigner == "" || cosigner == input.RequesterUserID || cosigner == input.ApproverUserID {
		return ErrSecurityCosignRequired
	}
	members := append([]string(nil), input.ActiveMemberIDs...)
	sort.Strings(members)
	index := sort.SearchStrings(members, cosigner)
	if index >= len(members) || members[index] != cosigner {
		return ErrSecurityCosignRequired
	}
	return nil
}

func sensitivityRank(value Sensitivity) int {
	switch value {
	case SensitivityPublic:
		return 0
	case SensitivityInternal:
		return 1
	case SensitivityConfidential:
		return 2
	case SensitivityRestricted:
		return 3
	default:
		return -1
	}
}
