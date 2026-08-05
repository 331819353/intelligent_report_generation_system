package dimension

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const DefaultPolicyVersion = "dimension-index-policy-v1"

const (
	ReasonRestrictedSensitivity    = "RESTRICTED_SENSITIVITY"
	ReasonConfidentialSensitivity  = "CONFIDENTIAL_SENSITIVITY"
	ReasonSensitiveHighCardinality = "SENSITIVE_HIGH_CARDINALITY"
	ReasonNoMembers                = "NO_MEMBERS"
	ReasonHighCardinality          = "HIGH_CARDINALITY"
	ReasonIncompleteScan           = "INCOMPLETE_SCAN"
	ReasonHighChangeRate           = "HIGH_CHANGE_RATE"
	ReasonHighNullRatio            = "HIGH_NULL_RATIO"
	ReasonHighReservedRatio        = "HIGH_RESERVED_RATIO"
	ReasonLowStableCardinality     = "LOW_STABLE_CARDINALITY"
	ReasonMediumCardinality        = "MEDIUM_CARDINALITY"
	ReasonOnDemandDisabled         = "ON_DEMAND_DISABLED"
)

type PolicyConfig struct {
	Version              string  `json:"version"`
	FullMaxDistinct      int64   `json:"fullMaxDistinct"`
	HighCardinalityAt    int64   `json:"highCardinalityAt"`
	MaxFullChangeRate    float64 `json:"maxFullChangeRate"`
	MaxFullNullRatio     float64 `json:"maxFullNullRatio"`
	MaxFullReservedRatio float64 `json:"maxFullReservedRatio"`
	AllowOnDemand        bool    `json:"allowOnDemand"`
}

func DefaultPolicyConfig() PolicyConfig {
	return PolicyConfig{
		Version: DefaultPolicyVersion, FullMaxDistinct: 10_000,
		HighCardinalityAt: 100_000, MaxFullChangeRate: 0.20,
		MaxFullNullRatio: 0.95, MaxFullReservedRatio: 0.50,
		AllowOnDemand: true,
	}
}

func (config PolicyConfig) Validate() error {
	if config.Version == "" || len(config.Version) > 64 ||
		config.FullMaxDistinct < 1 || config.HighCardinalityAt <= config.FullMaxDistinct ||
		config.HighCardinalityAt > 10_000_000 ||
		config.MaxFullChangeRate < 0 || config.MaxFullChangeRate > 1 ||
		config.MaxFullNullRatio < 0 || config.MaxFullNullRatio > 1 ||
		config.MaxFullReservedRatio < 0 || config.MaxFullReservedRatio > 1 {
		return errors.New("dimension index policy configuration is invalid")
	}
	return nil
}

type PolicyDecision struct {
	PolicyVersion        string                     `json:"policyVersion"`
	Config               PolicyConfig               `json:"config"`
	ProfileHash          askdata.ContentHash        `json:"profileHash"`
	RecommendedPolicy    registry.MemberIndexPolicy `json:"recommendedPolicy"`
	HighCardinality      bool                       `json:"highCardinality"`
	EligibleForEmbedding bool                       `json:"eligibleForEmbedding"`
	ReasonCodes          []string                   `json:"reasonCodes"`
	DecisionHash         askdata.ContentHash        `json:"decisionHash"`
}

func DecidePolicy(profile Profile, config PolicyConfig) (PolicyDecision, error) {
	if err := profile.Validate(); err != nil {
		return PolicyDecision{}, fmt.Errorf("profile: %w", err)
	}
	if err := config.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	highCardinality := profile.HighCardinalityHint || profile.DistinctCount >= config.HighCardinalityAt
	policy := registry.MemberIndexExactOnly
	reasons := []string{}

	switch {
	case profile.Sensitivity == registry.SensitivityRestricted:
		policy = registry.MemberIndexNone
		reasons = append(reasons, ReasonRestrictedSensitivity)
	case profile.Sensitivity == registry.SensitivityConfidential && highCardinality:
		policy = registry.MemberIndexNone
		reasons = append(reasons, ReasonConfidentialSensitivity, ReasonSensitiveHighCardinality)
	case profile.Sensitivity == registry.SensitivityConfidential:
		policy = registry.MemberIndexExactOnly
		reasons = append(reasons, ReasonConfidentialSensitivity)
	case profile.DistinctCount == 0:
		policy = registry.MemberIndexNone
		reasons = append(reasons, ReasonNoMembers)
	case highCardinality:
		if config.AllowOnDemand {
			policy = registry.MemberIndexOnDemand
			reasons = append(reasons, ReasonHighCardinality)
		} else {
			policy = registry.MemberIndexNone
			reasons = append(reasons, ReasonHighCardinality, ReasonOnDemandDisabled)
		}
	case profile.Usage.Truncated || profile.Usage.TimedOut || profile.Usage.DistinctCaptured < profile.DistinctCount:
		policy = registry.MemberIndexExactOnly
		reasons = append(reasons, ReasonIncompleteScan)
	default:
		changeRate, comparable := profile.ChangeRate()
		if comparable && changeRate > config.MaxFullChangeRate {
			reasons = append(reasons, ReasonHighChangeRate)
		}
		if profile.NullRatio() > config.MaxFullNullRatio {
			reasons = append(reasons, ReasonHighNullRatio)
		}
		if profile.ReservedRatio() > config.MaxFullReservedRatio {
			reasons = append(reasons, ReasonHighReservedRatio)
		}
		if profile.DistinctCount <= config.FullMaxDistinct && len(reasons) == 0 {
			policy = registry.MemberIndexFull
			reasons = append(reasons, ReasonLowStableCardinality)
		} else {
			policy = registry.MemberIndexExactOnly
			if len(reasons) == 0 {
				reasons = append(reasons, ReasonMediumCardinality)
			}
		}
	}

	decision := PolicyDecision{
		PolicyVersion: config.Version, Config: config, ProfileHash: profile.ProfileHash,
		RecommendedPolicy: policy, HighCardinality: highCardinality,
		EligibleForEmbedding: policy == registry.MemberIndexFull &&
			(profile.Sensitivity == registry.SensitivityPublic || profile.Sensitivity == registry.SensitivityInternal),
		ReasonCodes: append([]string(nil), reasons...),
	}
	sort.Strings(decision.ReasonCodes)
	payload, err := json.Marshal(decisionHashPayload(decision))
	if err != nil {
		return PolicyDecision{}, err
	}
	decision.DecisionHash = askdata.HashBytes(payload)
	return decision, decision.Validate()
}

func (decision PolicyDecision) Validate() error {
	if decision.PolicyVersion == "" || len(decision.PolicyVersion) > 64 {
		return errors.New("policyVersion is invalid")
	}
	if err := decision.Config.Validate(); err != nil || decision.Config.Version != decision.PolicyVersion {
		return errors.New("decision config is invalid or version-mismatched")
	}
	if err := decision.ProfileHash.Validate(); err != nil {
		return fmt.Errorf("profileHash: %w", err)
	}
	if err := decision.DecisionHash.Validate(); err != nil {
		return fmt.Errorf("decisionHash: %w", err)
	}
	if decision.RecommendedPolicy != registry.MemberIndexFull &&
		decision.RecommendedPolicy != registry.MemberIndexExactOnly &&
		decision.RecommendedPolicy != registry.MemberIndexOnDemand &&
		decision.RecommendedPolicy != registry.MemberIndexNone {
		return errors.New("recommendedPolicy is invalid")
	}
	if decision.EligibleForEmbedding && decision.RecommendedPolicy != registry.MemberIndexFull {
		return errors.New("only FULL decisions can be eligible for embedding")
	}
	if len(decision.ReasonCodes) == 0 || len(decision.ReasonCodes) > 16 {
		return errors.New("reasonCodes count is invalid")
	}
	for index, reason := range decision.ReasonCodes {
		if !stableCode(reason) || index > 0 && decision.ReasonCodes[index-1] >= reason {
			return errors.New("reasonCodes must be unique and sorted stable codes")
		}
	}
	payload, err := json.Marshal(decisionHashPayload(decision))
	if err != nil {
		return err
	}
	if askdata.HashBytes(payload) != decision.DecisionHash {
		return errors.New("decisionHash does not match decision content")
	}
	return nil
}

func decisionHashPayload(decision PolicyDecision) any {
	type withoutHash PolicyDecision
	payload := withoutHash(decision)
	payload.DecisionHash = ""
	return payload
}
