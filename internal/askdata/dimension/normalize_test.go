package dimension

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestNormalizeMemberSeparatesCanonicalAliasesAndDimensionKey(t *testing.T) {
	member, reserved, err := NormalizeMember(
		"dimension-region-v1", "　华东区 ", []string{"华东区", " East  China ", "ＥＡＳＴ　ＣＨＩＮＡ"},
		registry.SensitivityInternal, DefaultReservedValueCatalog(),
	)
	if err != nil || reserved != nil {
		t.Fatalf("NormalizeMember() = %#v, %#v, %v", member, reserved, err)
	}
	if member.CanonicalValue != "华东区" || member.NormalizedValue != "华东区" || len(member.Aliases) != 1 ||
		member.Aliases[0].NormalizedAlias != "east china" || !member.EligibleForLLM {
		t.Fatalf("member = %#v", member)
	}
	other, _, err := NormalizeMember(
		"dimension-service-level-v1", "华东区", nil, registry.SensitivityInternal, DefaultReservedValueCatalog(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if member.MemberKeyHash == other.MemberKeyHash {
		t.Fatal("identical values in different dimensions must have different keys")
	}
}

func TestNormalizeMemberExcludesReservedValuesAndProtectsSensitiveLLMUse(t *testing.T) {
	_, reserved, err := NormalizeMember(
		"dimension-region-v1", " 未知 ", nil, registry.SensitivityInternal, DefaultReservedValueCatalog(),
	)
	if !errors.Is(err, ErrReservedMemberValue) || reserved == nil || reserved.Code != "UNKNOWN" {
		t.Fatalf("reserved result = %#v, %v", reserved, err)
	}
	member, _, err := NormalizeMember(
		"dimension-customer-v1", "客户A", nil, registry.SensitivityConfidential, DefaultReservedValueCatalog(),
	)
	if err != nil || member.EligibleForLLM {
		t.Fatalf("confidential member = %#v, %v", member, err)
	}
}

func TestLLMAnomalyProposalNeverAutoMergesAndSensitiveValuesAreRejected(t *testing.T) {
	evidence := askdata.EvidenceRef{
		EvidenceID: "evidence-cluster-1", Kind: askdata.EvidenceKindDimensionProfile,
		SourceID: "profile-1", ContentHash: askdata.HashBytes([]byte("profile")),
	}
	proposal := AnomalyProposal{
		Source: ProposalLLM, Type: AnomalyClusterCandidate,
		DimensionVersionID:   "dimension-region-v1",
		MemberVersionIDs:     []askdata.ID{"member-east-v1", "member-east-cn-v1"},
		SuggestedCanonicalID: "member-east-v1", Risk: RiskHigh,
		Summary: "两个成员可能是同义值，但需要人工复核。", EvidenceRefs: []askdata.EvidenceRef{evidence},
		Sensitivity: registry.SensitivityInternal,
	}
	if err := proposal.Validate(); err != nil {
		t.Fatal(err)
	}
	if proposal.CanAutoApply() {
		t.Fatal("LLM high-risk proposal must never auto-merge")
	}
	proposal.Sensitivity = registry.SensitivityConfidential
	if err := proposal.Validate(); err == nil {
		t.Fatal("confidential member proposal was exposed to LLM review")
	}

	proposal.Source = ProposalDeterministic
	proposal.Type = AnomalyAliasCandidate
	proposal.Risk = RiskLow
	proposal.Sensitivity = registry.SensitivityInternal
	if !proposal.CanAutoApply() {
		t.Fatal("low-risk deterministic alias equivalence should be auto-applicable")
	}
}
