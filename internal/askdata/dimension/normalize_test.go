package dimension

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestMemberLookupKeyHashMatchesCanonicalNormalization(t *testing.T) {
	dimensionID := askdata.ID("dimension-region@v1")
	member, _, err := NormalizeMember(
		dimensionID, "ＡＣＭＥ　华东", nil,
		registry.SensitivityConfidential, registry.MemberIndexExactOnly, false,
		DefaultReservedValueCatalog(),
	)
	if err != nil {
		t.Fatal(err)
	}
	lookupHash, err := MemberLookupKeyHash(dimensionID, "  acme\t华东  ")
	if err != nil {
		t.Fatal(err)
	}
	if lookupHash != member.MemberKeyHash {
		t.Fatalf("lookup hash = %s, want %s", lookupHash, member.MemberKeyHash)
	}
	if _, err := MemberLookupKeyHash(dimensionID, "unknown"); !errors.Is(err, ErrInvalidMemberLookup) {
		t.Fatalf("reserved lookup error = %v", err)
	}
}

func TestNormalizeMemberSeparatesCanonicalAliasesAndDimensionKey(t *testing.T) {
	member, reserved, err := NormalizeMember(
		"dimension-region-v1", "　华东区 ", []string{"华东区", " East  China ", "ＥＡＳＴ　ＣＨＩＮＡ"},
		registry.SensitivityInternal, registry.MemberIndexFull, false, DefaultReservedValueCatalog(),
	)
	if err != nil || reserved != nil {
		t.Fatalf("NormalizeMember() = %#v, %#v, %v", member, reserved, err)
	}
	if member.CanonicalValue != "华东区" || member.NormalizedValue != "华东区" || len(member.Aliases) != 1 ||
		member.Aliases[0].NormalizedAlias != "east china" || !member.EligibleForLLM {
		t.Fatalf("member = %#v", member)
	}
	other, _, err := NormalizeMember(
		"dimension-service-level-v1", "华东区", nil, registry.SensitivityInternal,
		registry.MemberIndexFull, false, DefaultReservedValueCatalog(),
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
		"dimension-region-v1", " 未知 ", nil, registry.SensitivityInternal,
		registry.MemberIndexFull, false, DefaultReservedValueCatalog(),
	)
	if !errors.Is(err, ErrReservedMemberValue) || reserved == nil || reserved.Code != "UNKNOWN" {
		t.Fatalf("reserved result = %#v, %v", reserved, err)
	}
	member, _, err := NormalizeMember(
		"dimension-customer-v1", "客户A", nil, registry.SensitivityConfidential,
		registry.MemberIndexFull, false, DefaultReservedValueCatalog(),
	)
	if err != nil || member.EligibleForLLM {
		t.Fatalf("confidential member = %#v, %v", member, err)
	}
}

func TestNormalizeMemberRequiresFullLowCardinalityPolicyForLLM(t *testing.T) {
	for _, test := range []struct {
		name            string
		sensitivity     registry.Sensitivity
		policy          registry.MemberIndexPolicy
		highCardinality bool
		wantEligible    bool
	}{
		{name: "public full low cardinality", sensitivity: registry.SensitivityPublic, policy: registry.MemberIndexFull, wantEligible: true},
		{name: "internal full low cardinality", sensitivity: registry.SensitivityInternal, policy: registry.MemberIndexFull, wantEligible: true},
		{name: "internal exact only", sensitivity: registry.SensitivityInternal, policy: registry.MemberIndexExactOnly},
		{name: "internal on demand", sensitivity: registry.SensitivityInternal, policy: registry.MemberIndexOnDemand},
		{name: "internal none", sensitivity: registry.SensitivityInternal, policy: registry.MemberIndexNone},
		{name: "internal high cardinality", sensitivity: registry.SensitivityInternal, policy: registry.MemberIndexFull, highCardinality: true},
		{name: "confidential full", sensitivity: registry.SensitivityConfidential, policy: registry.MemberIndexFull},
	} {
		t.Run(test.name, func(t *testing.T) {
			member, _, err := NormalizeMember(
				"dimension-policy-v1", "华东", nil, test.sensitivity,
				test.policy, test.highCardinality, DefaultReservedValueCatalog(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if member.EligibleForLLM != test.wantEligible {
				t.Fatalf("eligibleForLLM=%v, want %v", member.EligibleForLLM, test.wantEligible)
			}
		})
	}
}

func TestLLMAnomalyProposalNeverAutoMergesAndSensitiveValuesAreRejected(t *testing.T) {
	evidence := askdata.EvidenceRef{
		EvidenceID: "evidence-cluster-1", Kind: askdata.EvidenceKindDimensionProfile,
		SourceID: "profile-1", ContentHash: askdata.HashBytes([]byte("profile")),
	}
	proposal := AnomalyProposal{
		Source: ProposalLLM, Type: AnomalyClusterCandidate,
		DimensionVersionID: "dimension-region-v1", Generation: 1,
		MemberEvidenceIDs:            []askdata.ID{"member-east-cn-v1", "member-east-v1"},
		SuggestedCanonicalEvidenceID: "member-east-v1", Risk: RiskHigh,
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
	proposal.MemberEvidenceIDs = []askdata.ID{"member-east-v1"}
	proposal.SuggestedCanonicalEvidenceID = "member-east-v1"
	proposal.SuggestedAliases = []string{"华东"}
	proposal.Sensitivity = registry.SensitivityInternal
	if !proposal.CanAutoApply() {
		t.Fatal("low-risk deterministic alias equivalence should be auto-applicable")
	}
}
