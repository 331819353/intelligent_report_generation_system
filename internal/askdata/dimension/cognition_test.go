package dimension

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	askdatacognition "intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/registry"
)

type generationReviewerFixture struct {
	calls        int
	fact         askdatacognition.PromptFact
	unknownID    bool
	confidential bool
	err          error
}

func (reviewer *generationReviewerFixture) ReviewGeneration(
	_ context.Context,
	fact askdatacognition.PromptFact,
) ([]AnomalyProposal, error) {
	reviewer.calls++
	reviewer.fact = fact
	if reviewer.err != nil {
		return nil, reviewer.err
	}
	var evidence GenerationReviewEvidence
	if err := json.Unmarshal(fact.Payload, &evidence); err != nil {
		return nil, err
	}
	ref := askdata.EvidenceRef{
		EvidenceID: fact.EvidenceID, Kind: askdata.EvidenceKindDimensionProfile,
		SourceID: askdata.ID("profile:" + string(evidence.ProfileHash)), ContentHash: fact.ContentHash,
	}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	memberIDs := []askdata.ID{
		evidence.Members[0].MemberEvidenceID,
		evidence.Members[1].MemberEvidenceID,
	}
	if reviewer.unknownID {
		memberIDs[1] = "profile-member:not-observed"
	}
	sensitivity := evidence.Sensitivity
	if reviewer.confidential {
		sensitivity = registry.SensitivityConfidential
	}
	return []AnomalyProposal{{
		Source:                       ProposalLLM,
		Type:                         AnomalyClusterCandidate,
		DimensionVersionID:           evidence.DimensionVersionID,
		Generation:                   evidence.Generation,
		MemberEvidenceIDs:            memberIDs,
		SuggestedCanonicalEvidenceID: memberIDs[0],
		Risk:                         RiskHigh,
		Summary:                      "两个观测成员可能属于同一业务簇，需要人工复核。",
		EvidenceRefs:                 []askdata.EvidenceRef{ref},
		Sensitivity:                  sensitivity,
	}}, nil
}

func TestProfileGenerationBuildsStableCognitionEvidenceAndAliasCandidates(t *testing.T) {
	claim, profile, observations := generationReviewFixture(t, registry.SensitivityInternal, false, false)
	reversedClaim, reversedProfile, reversedObservations := generationReviewFixture(t, registry.SensitivityInternal, false, true)
	evidence, err := NewGenerationReviewEvidence(claim, profile, observations)
	if err != nil {
		t.Fatal(err)
	}
	reversedEvidence, err := NewGenerationReviewEvidence(reversedClaim, reversedProfile, reversedObservations)
	if err != nil {
		t.Fatal(err)
	}
	fact, err := evidence.PromptFact()
	if err != nil {
		t.Fatal(err)
	}
	reversedFact, err := reversedEvidence.PromptFact()
	if err != nil {
		t.Fatal(err)
	}
	if fact.Kind != askdatacognition.FactDimensionProfile || fact.ContentHash != reversedFact.ContentHash ||
		!reflect.DeepEqual(evidence, reversedEvidence) {
		t.Fatalf("generation evidence is not stable:\n%#v\n%#v", evidence, reversedEvidence)
	}
	if evidence.Generation != profile.Generation || evidence.CapturedDistinctCount != 3 ||
		len(evidence.Members) != 2 || len(evidence.ReservedValues) != 1 || !evidence.ScanComplete {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	if evidence.ReservedValues[0].Code != "UNKNOWN" ||
		evidence.ReservedValues[0].NormalizedValueHash == "" ||
		evidence.ReservedValues[0].CatalogVersion != "reserved-member-values-v1" {
		t.Fatalf("reserved evidence must contain only governed code/hash/count: %#v", evidence.ReservedValues)
	}

	reviewer := &generationReviewerFixture{}
	result, err := ReviewProfileGeneration(context.Background(), claim, profile, observations, reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.calls != 0 || len(result.DeterministicProposals) != 1 || len(result.LLMProposals) != 0 {
		t.Fatalf("unexpected review result: calls=%d result=%#v", reviewer.calls, result)
	}
	alias := result.DeterministicProposals[0]
	if alias.Type != AnomalyAliasCandidate || !reflect.DeepEqual(alias.SuggestedAliases, []string{"east"}) ||
		!alias.CanAutoApplyAgainst(result.Evidence) {
		t.Fatalf("unexpected deterministic alias candidate: %#v", alias)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("review result: %v", err)
	}
}

func TestProfileGenerationNeverInvokesMemberReviewerAndRejectsSensitiveReview(t *testing.T) {
	claim, profile, observations := generationReviewFixture(t, registry.SensitivityInternal, false, false)
	reviewer := &generationReviewerFixture{err: errors.New("must not be called")}
	if _, err := ReviewProfileGeneration(context.Background(), claim, profile, observations, reviewer); err != nil {
		t.Fatalf("local deterministic review: %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatal("member reviewer was invoked without a persisted-authority label loader")
	}

	sensitiveClaim, sensitiveProfile, sensitiveObservations := generationReviewFixture(
		t, registry.SensitivityConfidential, false, false,
	)
	reviewer = &generationReviewerFixture{}
	if _, err := ReviewProfileGeneration(
		context.Background(), sensitiveClaim, sensitiveProfile, sensitiveObservations, reviewer,
	); !errors.Is(err, ErrSensitiveGenerationReview) {
		t.Fatalf("sensitive generation error = %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatal("sensitive generation reached the LLM reviewer")
	}
}

func TestIncompleteHighCardinalityGenerationNeverReachesReview(t *testing.T) {
	claim, profile, observations := generationReviewFixture(t, registry.SensitivityInternal, true, false)
	if _, err := NewGenerationReviewEvidence(claim, profile, observations); !errors.Is(err, ErrSensitiveGenerationReview) {
		t.Fatalf("incomplete/high-cardinality generation error = %v", err)
	}
}

func TestGenerationProposalTypesStayBoundToObservedEvidence(t *testing.T) {
	claim, profile, observations := generationReviewFixture(t, registry.SensitivityInternal, false, false)
	evidence, err := NewGenerationReviewEvidence(claim, profile, observations)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := evidence.EvidenceRef()
	if err != nil {
		t.Fatal(err)
	}
	memberIDs := []askdata.ID{
		evidence.Members[0].MemberEvidenceID,
		evidence.Members[1].MemberEvidenceID,
	}
	for _, proposal := range []AnomalyProposal{
		{
			Source: ProposalLLM, Type: AnomalyAliasCandidate,
			DimensionVersionID: evidence.DimensionVersionID, Generation: evidence.Generation,
			MemberEvidenceIDs: memberIDs, SuggestedCanonicalEvidenceID: memberIDs[0],
			SuggestedAliases: []string{"east"}, Risk: RiskMedium,
			Summary: "观测值可能是别名，需要人工复核。", EvidenceRefs: []askdata.EvidenceRef{ref},
			Sensitivity: evidence.Sensitivity,
		},
		{
			Source: ProposalLLM, Type: AnomalyHierarchy,
			DimensionVersionID: evidence.DimensionVersionID, Generation: evidence.Generation,
			MemberEvidenceIDs: memberIDs, Risk: RiskHigh,
			Summary: "成员层级可能异常，需要人工复核。", EvidenceRefs: []askdata.EvidenceRef{ref},
			Sensitivity: evidence.Sensitivity,
		},
		{
			Source: ProposalLLM, Type: AnomalySentinel,
			DimensionVersionID: evidence.DimensionVersionID, Generation: evidence.Generation,
			MemberEvidenceIDs: memberIDs[:1], Risk: RiskHigh,
			Summary: "成员可能是未收录的哨兵值，需要人工复核。", EvidenceRefs: []askdata.EvidenceRef{ref},
			Sensitivity: evidence.Sensitivity,
		},
	} {
		proposal = normalizeProposal(proposal)
		if err := proposal.ValidateAgainstGeneration(evidence); err != nil {
			t.Fatalf("%s proposal: %v", proposal.Type, err)
		}
		if proposal.CanAutoApplyAgainst(evidence) {
			t.Fatalf("%s LLM proposal must remain review-only", proposal.Type)
		}
	}
}

func TestGenerationReviewPolicyGateRejectsDowngradedAndFabricatedInputs(t *testing.T) {
	for _, test := range []struct {
		name            string
		policy          registry.MemberIndexPolicy
		highCardinality bool
	}{
		{name: "internal exact only", policy: registry.MemberIndexExactOnly},
		{name: "internal on demand", policy: registry.MemberIndexOnDemand},
		{name: "internal none", policy: registry.MemberIndexNone},
		{name: "internal full high cardinality", policy: registry.MemberIndexFull, highCardinality: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			claim := validScanClaim()
			claim.MemberIndexPolicy = test.policy
			claim.HighCardinalityHint = test.highCardinality
			profile, _, observations, err := buildProfileResult(claim, ScanResult{
				RowCount: 1, RawDistinct: 1, SampleBytes: int64(len("客户A")),
				Members: []RawMember{{Value: "客户A", Count: 1}},
			}, DefaultPolicyConfig())
			if err != nil {
				t.Fatal(err)
			}
			reviewer := &generationReviewerFixture{}
			if _, err := ReviewProfileGeneration(
				context.Background(), claim, profile, observations, reviewer,
			); !errors.Is(err, ErrSensitiveGenerationReview) {
				t.Fatalf("policy gate error = %v", err)
			}
			if reviewer.calls != 0 {
				t.Fatal("ineligible member labels reached the LLM reviewer")
			}
		})
	}

	claim, profile, observations := generationReviewFixture(
		t, registry.SensitivityInternal, false, false,
	)
	forgedClaim := claim
	forgedClaim.MemberIndexPolicy = registry.MemberIndexExactOnly
	if _, err := NewGenerationReviewEvidence(forgedClaim, profile, observations); !errors.Is(err, ErrSensitiveGenerationReview) {
		t.Fatalf("caller-downgraded claim error = %v", err)
	}

	evidence, err := NewGenerationReviewEvidence(claim, profile, observations)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var decoded GenerationReviewEvidence
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	fact, err := decoded.PromptFact()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("EAST"), []byte("east"), []byte("华南"),
		[]byte("canonicalLabel"), []byte("normalizedValue"), []byte("memberKeyHash"),
		[]byte("memberEvidenceId"), []byte("observationContentHash"),
	} {
		if bytes.Contains(fact.Payload, forbidden) {
			t.Fatalf("prompt payload leaked member data %q: %s", forbidden, fact.Payload)
		}
	}
}

func generationReviewFixture(
	t *testing.T,
	sensitivity registry.Sensitivity,
	truncated bool,
	reverse bool,
) (ScanClaim, Profile, []MemberObservation) {
	t.Helper()
	claim := validScanClaim()
	claim.Sensitivity = sensitivity
	claim.MemberIndexPolicy = registry.MemberIndexFull
	members := []RawMember{
		{Value: "EAST", Count: 2},
		{Value: "east", Count: 1},
		{Value: "华南", Count: 2},
		{Value: "UNKNOWN", Count: 1},
	}
	if reverse {
		for left, right := 0, len(members)-1; left < right; left, right = left+1, right-1 {
			members[left], members[right] = members[right], members[left]
		}
	}
	profile, _, observations, err := buildProfileResult(claim, ScanResult{
		RowCount:    7,
		NullCount:   1,
		RawDistinct: 4,
		SampleBytes: int64(len("EAST") + len("east") + len("华南") + len("UNKNOWN")),
		Truncated:   truncated,
		Members:     members,
	}, DefaultPolicyConfig())
	if err != nil {
		t.Fatal(err)
	}
	return claim, profile, observations
}
