package search_test

import (
	"context"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	askdatacognition "intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
)

type externalMemberReviewer struct{ calls int }

func (reviewer *externalMemberReviewer) ReviewCandidates(
	context.Context,
	askdatacognition.PromptFact,
) (search.RerankProposal, error) {
	reviewer.calls++
	return search.RerankProposal{}, nil
}

func TestExternalCallerCannotSendManufacturedMemberLabelToReranker(t *testing.T) {
	const (
		memberID    = askdata.ID("66666666-6666-4666-8666-666666666666")
		dimensionID = askdata.ID("77777777-7777-4777-8777-777777777777")
		rawLabel    = "CUSTOMER_SECRET_A42"
	)
	document, err := search.BuildMemberDocument(search.MemberDocumentInput{
		ObjectVersionID:      memberID,
		DimensionVersionID:   dimensionID,
		DimensionName:        "客户",
		DimensionDescription: "客户主数据",
		CanonicalValue:       rawLabel,
		Sensitivity:          registry.SensitivityInternal,
		MemberIndexPolicy:    registry.MemberIndexFull,
	})
	if err != nil {
		t.Fatal(err)
	}

	retrievalHash := askdata.HashBytes([]byte("external-member-retrieval"))
	graphHash := askdata.HashBytes([]byte("external-member-graph"))
	gateHash := askdata.HashBytes([]byte("external-member-gate"))
	request := search.RerankRequest{
		Scope:   externalMemberScope(t),
		Mention: rawLabel,
		Candidates: []search.RerankCandidate{{
			Candidate: search.Candidate{
				ObjectType:      search.ObjectMember,
				ObjectVersionID: memberID,
				Score:           1,
				Evidence: []search.SourceEvidence{{
					Source: search.SourceLexical, Rank: 1, SourceScore: 1,
					Evidence: askdata.EvidenceRef{
						EvidenceID: "external-member-retrieval", Kind: askdata.EvidenceKindLexicalMatch,
						SourceID: memberID, ContentHash: retrievalHash,
					},
				}},
			},
			Definition:         document.Text,
			NegativeExamples:   []string{},
			Sensitivity:        registry.SensitivityInternal,
			GraphCompatibility: search.GraphCompatible,
			GraphEvidenceRefs: []askdata.EvidenceRef{{
				EvidenceID: "external-member-graph", Kind: askdata.EvidenceKindGraphPath,
				SourceID: memberID, ContentHash: graphHash,
			}},
			Gate: search.DeterministicGate{
				Verdict: search.GateAllow, ReasonCodes: []string{"AUTHORIZED"},
				EvidenceRefs: []askdata.EvidenceRef{{
					EvidenceID: "external-member-gate", Kind: askdata.EvidenceKindPolicy,
					SourceID: memberID, ContentHash: gateHash,
				}},
			},
		}},
	}

	if _, err := search.NewRerankEvidence(request); err == nil {
		t.Fatal("manufactured MEMBER candidate produced a model-visible fact")
	}
	reviewer := &externalMemberReviewer{}
	reranker, err := search.NewReranker(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reranker.Rerank(context.Background(), request); err == nil {
		t.Fatal("manufactured MEMBER candidate reached reranking")
	}
	if reviewer.calls != 0 {
		t.Fatalf("member label reached reviewer: calls=%d", reviewer.calls)
	}
}

func externalMemberScope(t *testing.T) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		[]askdata.ID{"33333333-3333-4333-8333-333333333333"},
		[]askdata.ID{"44444444-4444-4444-8444-444444444444"},
		askdata.ReleaseRef{
			ReleaseID:   "55555555-5555-4555-8555-555555555555",
			ContentHash: askdata.HashBytes([]byte("external-member-release")),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}
