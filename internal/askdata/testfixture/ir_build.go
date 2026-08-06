package testfixture

import (
	"context"
	"errors"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/binding"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/security"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

type semanticIRReviewerFunc func(
	context.Context,
	understanding.UnderstandingReviewInput,
) (understanding.UnderstandingProposal, error)

func (reviewer semanticIRReviewerFunc) ReviewUnderstanding(
	ctx context.Context,
	input understanding.UnderstandingReviewInput,
) (understanding.UnderstandingProposal, error) {
	return reviewer(ctx, input)
}

// SemanticMetricBuildRequest returns a replay-valid, metric-only Binding -> IR
// fixture. More detailed resolver ownership tests construct their lookup and
// contract snapshot directly, keeping this shared replay fixture small.
func SemanticMetricBuildRequest() (ir.BuildRequest, error) {
	release := askdata.ReleaseRef{
		ReleaseID: "release-query-v1", ContentHash: askdata.HashBytes([]byte("release-query-v1")),
	}
	scope, err := askdata.NewPolicyScope(
		"tenant-query", "actor-query", []askdata.ID{"sales"}, []askdata.ID{"analyst"}, release,
	)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	question := "销售额"
	normalized, err := understanding.NormalizeQuestion(question)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	parser, err := understanding.NewRuleParser(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), time.January)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	rules, err := parser.Parse(normalized)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	contextRequest := understanding.ContextMergeRequest{
		ConversationID: "conversation-query", TurnID: "turn-query", Scope: scope,
		Question: normalized, Rules: rules, Continuation: understanding.ContextIndependent,
	}
	contextResult, err := understanding.MergeContext(contextRequest)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	understandingRequest := understanding.UnderstandingRequest{
		ContextRequest: contextRequest, Context: contextResult,
		ExactMatches: []understanding.ExactMatch{}, SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	reviewer := semanticIRReviewerFunc(func(
		_ context.Context,
		input understanding.UnderstandingReviewInput,
	) (understanding.UnderstandingProposal, error) {
		policy, ok := fixtureEvidenceForKind(input.AllowedEvidenceRefs, askdata.EvidenceKindPolicy)
		if !ok {
			return understanding.UnderstandingProposal{}, errors.New("policy evidence is missing")
		}
		conversation, ok := fixtureEvidenceForKind(input.AllowedEvidenceRefs, askdata.EvidenceKindConversation)
		if !ok {
			return understanding.UnderstandingProposal{}, errors.New("conversation evidence is missing")
		}
		span := understanding.Span{Start: 0, End: len([]rune(question))}
		return understanding.UnderstandingProposal{
			SchemaVersion: understanding.UnderstandingReviewSchemaVersion,
			Understanding: understanding.QuestionUnderstanding{
				SchemaVersion: understanding.SchemaVersion, Question: question,
				DomainHypotheses: []understanding.DomainHypothesis{{
					DomainID: "sales", Score: 1, EvidenceRefs: []askdata.EvidenceRef{policy},
				}},
				MetricMentions: []understanding.MetricMention{{
					Text: question, Span: span, AggregationHint: understanding.AggregationDefault,
				}},
				DimensionMentions: []understanding.DimensionMention{},
				ValueMentions:     []understanding.ValueMention{}, Comparisons: []understanding.ComparisonMention{},
				Ordering: []understanding.OrderingMention{}, UnresolvedSpans: []understanding.UnresolvedSpan{},
			},
			Conflicts: []understanding.UnderstandingConflict{},
			EvidenceRequests: []understanding.UnderstandingEvidenceRequest{{
				Origin: understanding.EvidenceOriginCurrent, NeededEvidence: understanding.NeededMetricCandidates,
				Text: question, Span: span, Reason: "需要稳定指标候选构造 Semantic IR。",
				EvidenceRefs: []askdata.EvidenceRef{conversation},
			}},
			EvidenceRefs: input.AllowedEvidenceRefs,
		}, nil
	})
	service, err := understanding.NewUnderstandingService(reviewer)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	understandingResult, err := service.Understand(context.Background(), understandingRequest)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	graphRequest, err := (graph.PlanRequest{
		Scope: scope, DomainID: "sales",
		MetricRefs: []graph.ObjectVersionRef{{ObjectID: "metric-sales", VersionID: "metric-sales-v1", Version: 1}},
		ModelRefs:  []graph.ObjectVersionRef{{ObjectID: "model-sales", VersionID: "model-sales-v1", Version: 1}},
	}).Normalize()
	if err != nil {
		return ir.BuildRequest{}, err
	}
	plan, err := graph.NewGraphPlan(
		graphRequest, graphRequest.ModelRefs,
		[]graph.MetricModelBinding{{MetricVersionID: "metric-sales-v1", ModelVersionID: "model-sales-v1"}},
		nil, nil, nil,
	)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	mention := binding.MentionRef{
		Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionMetric, Index: 0,
	}
	candidateSetHash := askdata.HashBytes([]byte("candidate-set:metric-sales-v1"))
	retrievalHash := askdata.HashBytes([]byte("retrieval:metric-sales-v1"))
	bindingRequest := binding.Request{
		UnderstandingRequest: understandingRequest, UnderstandingResult: understandingResult,
		GraphRequest: graphRequest, GraphResolution: graph.Resolution{Plan: plan, Source: graph.ResolutionSourceNebula},
		CandidateSets: []binding.MentionCandidateSet{{
			Mention: mention,
			Evidence: askdata.EvidenceRef{
				EvidenceID: "candidate-set:" + askdata.ID(candidateSetHash),
				Kind:       askdata.EvidenceKindCandidateSet, SourceID: release.ReleaseID, ContentHash: candidateSetHash,
			},
			Candidates: []binding.CandidateOption{{
				Candidate: search.Candidate{
					ObjectType: search.ObjectMetric, ObjectVersionID: "metric-sales-v1", Score: 1,
					Evidence: []search.SourceEvidence{{
						Source: search.SourceLexical, Rank: 1, SourceScore: 1,
						Evidence: askdata.EvidenceRef{
							EvidenceID: "retrieval:metric-sales-v1", Kind: askdata.EvidenceKindLexicalMatch,
							SourceID: "metric-sales-v1", ContentHash: retrievalHash,
						},
					}},
				},
				SelectionSource: binding.SelectionLLMRerank, ReviewerRank: 1,
				Gate: search.DeterministicGate{
					Verdict: search.GateAllow, ReasonCodes: []string{"RULE_ALLOW"},
					EvidenceRefs: []askdata.EvidenceRef{{
						EvidenceID: "gate:metric-sales-v1", Kind: askdata.EvidenceKindRule,
						SourceID: release.ReleaseID, ContentHash: askdata.HashBytes([]byte("gate:metric-sales-v1")),
					}},
				},
				RuleScore: 1, QualityScore: 1, CostScore: 1,
				FeatureEvidenceRefs: []askdata.EvidenceRef{
					{EvidenceID: "contract:metric-sales-v1", Kind: askdata.EvidenceKindSemanticContract, SourceID: "metric-sales-v1", ContentHash: askdata.HashBytes([]byte("contract:metric-sales-v1"))},
					{EvidenceID: "quality:metric-sales-v1", Kind: askdata.EvidenceKindDataQuality, SourceID: "metric-sales-v1", ContentHash: askdata.HashBytes([]byte("quality:metric-sales-v1"))},
				},
			}},
		}},
	}
	bindingResult, err := binding.Bind(bindingRequest)
	if err != nil {
		return ir.BuildRequest{}, err
	}
	if len(bindingResult.Bundles) != 1 {
		return ir.BuildRequest{}, errors.New("synthetic binding did not produce exactly one bundle")
	}
	return ir.BuildRequest{
		BindingRequest: bindingRequest, BindingResult: bindingResult,
		BundleHash: bindingResult.Bundles[0].BundleHash,
	}, nil
}

func fixtureEvidenceForKind(values []askdata.EvidenceRef, kind askdata.EvidenceKind) (askdata.EvidenceRef, bool) {
	for _, value := range values {
		if value.Kind == kind {
			return value, true
		}
	}
	return askdata.EvidenceRef{}, false
}
