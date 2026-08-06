package binding

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/security"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

type bindingReviewerFunc func(context.Context, understanding.UnderstandingReviewInput) (understanding.UnderstandingProposal, error)

func (reviewer bindingReviewerFunc) ReviewUnderstanding(
	ctx context.Context,
	input understanding.UnderstandingReviewInput,
) (understanding.UnderstandingProposal, error) {
	return reviewer(ctx, input)
}

func TestJointBinderEliminatesIndividuallyHighButIncompatibleCandidates(t *testing.T) {
	request := jointBindingFixture(t)
	result, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.NoMatch || len(result.Bundles) != 2 {
		t.Fatalf("unexpected bundle result: %#v", result)
	}
	if err := result.ValidateAgainst(request); err != nil {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
	for _, bundle := range result.Bundles {
		metric := bundle.MetricBindings[0]
		dimension := explicitDimension(bundle.DimensionBindings)
		member := bundle.MemberBindings[0]
		if metric.ModelVersionID == "model-a-v1" {
			if dimension.DimensionVersionID != "dimension-a-v1" || member.MemberVersionID != "member-a-v1" {
				t.Fatalf("binder kept an incompatible independent Top 1 combination: %#v", bundle)
			}
		} else if metric.ModelVersionID == "model-b-v1" {
			if dimension.DimensionVersionID != "dimension-b-v1" || member.MemberVersionID != "member-b-v1" {
				t.Fatalf("binder kept an incompatible independent Top 1 combination: %#v", bundle)
			}
		} else {
			t.Fatalf("unexpected model: %#v", bundle)
		}
		if len(bundle.EvidenceRefs) < 4 || bundle.BundleHash.Validate() != nil {
			t.Fatalf("bundle did not preserve stable evidence: %#v", bundle)
		}
	}
	// Metric A is individually first while dimension/member B are individually
	// first. Joint scoring should prefer the compatible all-B bundle.
	if result.Bundles[0].MetricBindings[0].MetricVersionID != "metric-b-v1" ||
		result.Bundles[0].MemberBindings[0].MemberVersionID != "member-b-v1" {
		t.Fatalf("unexpected top joint bundle: %#v", result.Bundles[0])
	}
}

func TestDeterministicBlockOverridesReviewerRank(t *testing.T) {
	request := jointBindingFixture(t)
	metricSet := candidateSetIndex(request.CandidateSets, MentionMetric)
	request.CandidateSets[metricSet].Candidates[0].Gate = gateForTest(
		request.GraphRequest.Scope.Release, search.GateBlock, "POLICY_BLOCK",
	)
	result, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, bundle := range result.Bundles {
		if bundle.MetricBindings[0].MetricVersionID == "metric-a-v1" {
			t.Fatalf("reviewer-selected blocked candidate survived: %#v", bundle)
		}
	}
	if len(result.BlockedCandidates) != 1 || result.BlockedCandidates[0].ObjectVersionID != "metric-a-v1" ||
		!reflect.DeepEqual(result.BlockedCandidates[0].ReasonCodes, []string{"POLICY_BLOCK"}) {
		t.Fatalf("blocked candidate evidence was not preserved: %#v", result.BlockedCandidates)
	}
}

func TestMemberMustBeActiveAndOwnedByCompatibleDimension(t *testing.T) {
	request := jointBindingFixture(t)
	planRequest := request.GraphRequest
	plan := request.GraphResolution.Plan
	for index := range plan.MemberOwnerships {
		plan.MemberOwnerships[index].Status = graph.MemberStatusExpired
	}
	plan, err := graph.NewGraphPlan(
		planRequest, plan.Models, plan.MetricModels, plan.CompatibleDimensions,
		plan.MemberOwnerships, plan.JoinPaths,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.GraphResolution.Plan = plan
	result, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoMatch || len(result.Bundles) != 0 {
		t.Fatalf("expired members produced an executable bundle: %#v", result)
	}
}

func TestCrossModelBundleRequiresAllowedCertifiedPath(t *testing.T) {
	request := relationalBindingFixture(t, registry.FanoutBlock)
	blocked, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked.NoMatch {
		t.Fatalf("fanout-blocked path produced a bundle: %#v", blocked)
	}

	request = relationalBindingFixture(t, registry.FanoutCertifiedPre)
	allowed, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	if allowed.NoMatch || len(allowed.Bundles) != 1 || allowed.Bundles[0].GraphPath == nil ||
		allowed.Bundles[0].GraphPath.PathID == "" || allowed.Bundles[0].Score.RiskPenalty <= 0 {
		t.Fatalf("certified relationship path was not retained with risk: %#v", allowed)
	}
}

func TestBindingIsStableAcrossCandidateOrderAndRejectsTampering(t *testing.T) {
	request := jointBindingFixture(t)
	first, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	permuted := request
	permuted.CandidateSets = append([]MentionCandidateSet(nil), request.CandidateSets...)
	for index := range permuted.CandidateSets {
		permuted.CandidateSets[index].Candidates = append(
			[]CandidateOption(nil), request.CandidateSets[index].Candidates...,
		)
		reverseOptions(permuted.CandidateSets[index].Candidates)
	}
	reverseCandidateSets(permuted.CandidateSets)
	second, err := Bind(permuted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("binding changed with caller order:\nfirst=%#v\nsecond=%#v", first, second)
	}
	tampered := first
	tampered.Bundles = append([]Bundle(nil), first.Bundles...)
	tampered.Bundles[0].Score.Total = 1
	if err := tampered.ValidateAgainst(request); !errors.Is(err, ErrInvalidBindingResult) {
		t.Fatalf("tampered result error = %v", err)
	}
}

func TestEmptyCandidateSetProducesAuditableNoMatch(t *testing.T) {
	request := jointBindingFixture(t)
	metricSet := candidateSetIndex(request.CandidateSets, MentionMetric)
	request.CandidateSets[metricSet].Candidates = []CandidateOption{}
	result, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoMatch || len(result.Bundles) != 0 || result.ResultHash.Validate() != nil {
		t.Fatalf("empty retrieval was not represented as a stable no-match: %#v", result)
	}
}

func TestBindingRejectsUnprovenOrReboundInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{
			name: "member via llm",
			mutate: func(request *Request) {
				index := candidateSetIndex(request.CandidateSets, MentionMember)
				request.CandidateSets[index].Candidates[0].SelectionSource = SelectionLLMRerank
				request.CandidateSets[index].Candidates[0].ReviewerRank = 1
			},
		},
		{
			name: "missing feature proof",
			mutate: func(request *Request) {
				request.CandidateSets[0].Candidates[0].FeatureEvidenceRefs = nil
			},
		},
		{
			name: "candidate outside graph request",
			mutate: func(request *Request) {
				request.CandidateSets[0].Candidates[0].Candidate.ObjectVersionID = "invented-version"
			},
		},
		{
			name: "candidate set from another release",
			mutate: func(request *Request) {
				request.CandidateSets[0].Evidence.SourceID = "other-release"
			},
		},
		{
			name: "tampered graph plan",
			mutate: func(request *Request) {
				request.GraphResolution.Plan.PlanHash = askdata.HashBytes([]byte("tampered"))
			},
		},
		{
			name: "tampered understanding",
			mutate: func(request *Request) {
				request.UnderstandingResult.ResultHash = askdata.HashBytes([]byte("tampered"))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := jointBindingFixture(t)
			test.mutate(&request)
			if _, err := Bind(request); !errors.Is(err, ErrInvalidBindingRequest) {
				t.Fatalf("Bind() error = %v", err)
			}
		})
	}
}

func TestBindingKeepsInheritedAndCurrentMentionOriginsSeparate(t *testing.T) {
	request := inheritedBindingFixture(t)
	result, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	if result.NoMatch || len(result.Bundles) != 1 {
		t.Fatalf("unexpected inherited binding result: %#v", result)
	}
	bundle := result.Bundles[0]
	if bundle.MetricBindings[0].Mention.Origin != understanding.EvidenceOriginInherited ||
		explicitDimension(bundle.DimensionBindings).Mention.Origin != understanding.EvidenceOriginCurrent {
		t.Fatalf("current and inherited mentions were conflated: %#v", bundle)
	}
}

func TestBindingResultStrictDecodeAndReplay(t *testing.T) {
	request := jointBindingFixture(t)
	result, err := Bind(request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeResult(raw, request)
	if err != nil || !reflect.DeepEqual(decoded, result) {
		t.Fatalf("DecodeResult() = %#v, %v", decoded, err)
	}
	unknown := strings.Replace(string(raw), `"version":`, `"sql":"select secret from table","version":`, 1)
	if _, err := DecodeResult([]byte(unknown), request); err == nil {
		t.Fatal("DecodeResult accepted an unknown physical field")
	}
}

func jointBindingFixture(t *testing.T) Request {
	t.Helper()
	understandingRequest, understandingResult, scope := understandingFixture(
		t, "销售额地区华东", []string{"销售额"}, []string{"地区"}, []string{"华东"},
	)
	graphRequest, err := (graph.PlanRequest{
		Scope: scope, DomainID: "sales",
		MetricRefs: []graph.ObjectVersionRef{
			{ObjectID: "metric-a", VersionID: "metric-a-v1", Version: 1},
			{ObjectID: "metric-b", VersionID: "metric-b-v1", Version: 1},
		},
		ModelRefs: []graph.ObjectVersionRef{
			{ObjectID: "model-a", VersionID: "model-a-v1", Version: 1},
			{ObjectID: "model-b", VersionID: "model-b-v1", Version: 1},
		},
		DimensionRefs: []graph.ObjectVersionRef{
			{ObjectID: "dimension-a", VersionID: "dimension-a-v1", Version: 1},
			{ObjectID: "dimension-b", VersionID: "dimension-b-v1", Version: 1},
		},
		MemberRefs: []graph.ObjectVersionRef{
			{ObjectID: "member-a", VersionID: "member-a-v1", Version: 1},
			{ObjectID: "member-b", VersionID: "member-b-v1", Version: 1},
		},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := graph.NewGraphPlan(
		graphRequest, graphRequest.ModelRefs,
		[]graph.MetricModelBinding{
			{MetricVersionID: "metric-a-v1", ModelVersionID: "model-a-v1"},
			{MetricVersionID: "metric-b-v1", ModelVersionID: "model-b-v1"},
		},
		[]graph.DimensionCompatibility{
			{ModelVersionID: "model-a-v1", DimensionVersionID: "dimension-a-v1"},
			{ModelVersionID: "model-b-v1", DimensionVersionID: "dimension-b-v1"},
		},
		[]graph.MemberOwnership{
			{MemberVersionID: "member-a-v1", DimensionVersionID: "dimension-a-v1", Status: graph.MemberStatusActive},
			{MemberVersionID: "member-b-v1", DimensionVersionID: "dimension-b-v1", Status: graph.MemberStatusActive},
		}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		UnderstandingRequest: understandingRequest, UnderstandingResult: understandingResult,
		GraphRequest:    graphRequest,
		GraphResolution: graph.Resolution{Plan: plan, Source: graph.ResolutionSourceNebula},
		CandidateSets: []MentionCandidateSet{
			candidateSetForTest(scope.Release, MentionRef{understanding.EvidenceOriginCurrent, MentionMetric, 0},
				llmCandidateForTest(scope.Release, search.ObjectMetric, "metric-a-v1", 9, 1, nil),
				llmCandidateForTest(scope.Release, search.ObjectMetric, "metric-b-v1", 0.2, 2, nil)),
			candidateSetForTest(scope.Release, MentionRef{understanding.EvidenceOriginCurrent, MentionDimension, 0},
				llmCandidateForTest(scope.Release, search.ObjectDimension, "dimension-b-v1", 9, 1, nil),
				llmCandidateForTest(scope.Release, search.ObjectDimension, "dimension-a-v1", 0.5, 2, nil)),
			candidateSetForTest(scope.Release, MentionRef{understanding.EvidenceOriginCurrent, MentionMember, 0},
				exactCandidateForTest(scope.Release, "member-b-v1", 9, idPointer("dimension-b-v1")),
				exactCandidateForTest(scope.Release, "member-a-v1", 0.5, idPointer("dimension-a-v1"))),
		}, Config: Config{TopBundles: 2},
	}
}

func relationalBindingFixture(t *testing.T, policy registry.FanoutPolicy) Request {
	t.Helper()
	understandingRequest, understandingResult, scope := understandingFixture(
		t, "销售额订单数", []string{"销售额", "订单数"}, nil, nil,
	)
	graphRequest, err := (graph.PlanRequest{
		Scope: scope, DomainID: "sales",
		MetricRefs: []graph.ObjectVersionRef{
			{ObjectID: "metric-sales", VersionID: "metric-sales-v1", Version: 1},
			{ObjectID: "metric-orders", VersionID: "metric-orders-v1", Version: 1},
		},
		ModelRefs: []graph.ObjectVersionRef{
			{ObjectID: "model-sales", VersionID: "model-sales-v1", Version: 1},
			{ObjectID: "model-orders", VersionID: "model-orders-v1", Version: 1},
		},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path, err := graph.NewJoinPath([]graph.JoinStep{{
		Hop: 1, RelationshipVersionID: "relationship-sales-orders-v1",
		FromModelVersionID: "model-orders-v1", ToModelVersionID: "model-sales-v1",
		Direction: graph.TraversalForward, JoinType: registry.JoinInner,
		Cardinality: registry.CardinalityOneToMany, FanoutPolicy: policy,
	}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := graph.NewGraphPlan(
		graphRequest, graphRequest.ModelRefs,
		[]graph.MetricModelBinding{
			{MetricVersionID: "metric-sales-v1", ModelVersionID: "model-sales-v1"},
			{MetricVersionID: "metric-orders-v1", ModelVersionID: "model-orders-v1"},
		}, nil, nil, []graph.JoinPath{path},
	)
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		UnderstandingRequest: understandingRequest, UnderstandingResult: understandingResult,
		GraphRequest:    graphRequest,
		GraphResolution: graph.Resolution{Plan: plan, Source: graph.ResolutionSourceNebula},
		CandidateSets: []MentionCandidateSet{
			candidateSetForTest(scope.Release, MentionRef{understanding.EvidenceOriginCurrent, MentionMetric, 0},
				llmCandidateForTest(scope.Release, search.ObjectMetric, "metric-sales-v1", 1, 1, nil)),
			candidateSetForTest(scope.Release, MentionRef{understanding.EvidenceOriginCurrent, MentionMetric, 1},
				llmCandidateForTest(scope.Release, search.ObjectMetric, "metric-orders-v1", 1, 1, nil)),
		}, Config: Config{TopBundles: 2},
	}
}

func inheritedBindingFixture(t *testing.T) Request {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		"tenant-binding", "actor-binding", []askdata.ID{"sales"}, []askdata.ID{"analyst"},
		askdata.ReleaseRef{ReleaseID: "release-binding-v1", ContentHash: askdata.HashBytes([]byte("release-binding-v1"))},
	)
	if err != nil {
		t.Fatal(err)
	}
	previousQuestion := "销售额"
	policyHash := askdata.HashBytes([]byte("previous-policy"))
	previous := understanding.QuestionUnderstanding{
		SchemaVersion: understanding.SchemaVersion, Question: previousQuestion,
		DomainHypotheses: []understanding.DomainHypothesis{{
			DomainID: "sales", Score: 1, EvidenceRefs: []askdata.EvidenceRef{{
				EvidenceID: "previous-policy", Kind: askdata.EvidenceKindPolicy,
				SourceID: scope.Release.ReleaseID, ContentHash: policyHash,
			}},
		}},
		MetricMentions: []understanding.MetricMention{{
			Text: previousQuestion, Span: understanding.Span{Start: 0, End: 3},
			AggregationHint: understanding.AggregationDefault,
		}},
		DimensionMentions: []understanding.DimensionMention{}, ValueMentions: []understanding.ValueMention{},
		Comparisons: []understanding.ComparisonMention{}, Ordering: []understanding.OrderingMention{},
		UnresolvedSpans: []understanding.UnresolvedSpan{},
	}
	snapshot, err := understanding.NewContextSnapshot("conversation-binding", "turn-previous", scope, previous)
	if err != nil {
		t.Fatal(err)
	}
	question := "那按地区呢"
	normalized, err := understanding.NormalizeQuestion(question)
	if err != nil {
		t.Fatal(err)
	}
	parser, err := understanding.NewRuleParser(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), time.January)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parser.Parse(normalized)
	if err != nil {
		t.Fatal(err)
	}
	contextRequest := understanding.ContextMergeRequest{
		ConversationID: "conversation-binding", TurnID: "turn-current", Scope: scope,
		Question: normalized, Rules: rules, Continuation: understanding.ContextFollowUp, Previous: &snapshot,
	}
	contextResult, err := understanding.MergeContext(contextRequest)
	if err != nil {
		t.Fatal(err)
	}
	understandingRequest := understanding.UnderstandingRequest{
		ContextRequest: contextRequest, Context: contextResult,
		ExactMatches: []understanding.ExactMatch{}, SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	reviewer := bindingReviewerFunc(func(
		_ context.Context,
		input understanding.UnderstandingReviewInput,
	) (understanding.UnderstandingProposal, error) {
		conversation := evidenceForKind(t, input.AllowedEvidenceRefs, askdata.EvidenceKindConversation)
		span := spanForText(t, question, "地区")
		current := understanding.QuestionUnderstanding{
			SchemaVersion: understanding.SchemaVersion, Question: question,
			DomainHypotheses: []understanding.DomainHypothesis{}, MetricMentions: []understanding.MetricMention{},
			DimensionMentions: []understanding.DimensionMention{{
				Text: "地区", Span: span, Role: understanding.DimensionRoleGroupBy,
			}},
			ValueMentions: []understanding.ValueMention{}, Comparisons: []understanding.ComparisonMention{},
			Ordering: []understanding.OrderingMention{}, UnresolvedSpans: []understanding.UnresolvedSpan{},
		}
		return understanding.UnderstandingProposal{
			SchemaVersion: understanding.UnderstandingReviewSchemaVersion,
			Understanding: current, Conflicts: []understanding.UnderstandingConflict{},
			EvidenceRequests: []understanding.UnderstandingEvidenceRequest{
				evidenceRequest(understanding.NeededDimensionCandidates, "地区", span, conversation),
				{
					Origin:         understanding.EvidenceOriginInherited,
					NeededEvidence: understanding.NeededMetricCandidates,
					Text:           "销售额", Span: understanding.Span{Start: 0, End: 3},
					Reason: "需要继承指标的稳定候选。", EvidenceRefs: []askdata.EvidenceRef{conversation},
				},
			},
			EvidenceRefs: input.AllowedEvidenceRefs,
		}, nil
	})
	service, err := understanding.NewUnderstandingService(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	understandingResult, err := service.Understand(context.Background(), understandingRequest)
	if err != nil {
		t.Fatal(err)
	}
	graphRequest, err := (graph.PlanRequest{
		Scope: scope, DomainID: "sales",
		MetricRefs:    []graph.ObjectVersionRef{{ObjectID: "metric-sales", VersionID: "metric-sales-v1", Version: 1}},
		ModelRefs:     []graph.ObjectVersionRef{{ObjectID: "model-sales", VersionID: "model-sales-v1", Version: 1}},
		DimensionRefs: []graph.ObjectVersionRef{{ObjectID: "dimension-region", VersionID: "dimension-region-v1", Version: 1}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := graph.NewGraphPlan(
		graphRequest, graphRequest.ModelRefs,
		[]graph.MetricModelBinding{{MetricVersionID: "metric-sales-v1", ModelVersionID: "model-sales-v1"}},
		[]graph.DimensionCompatibility{{ModelVersionID: "model-sales-v1", DimensionVersionID: "dimension-region-v1"}},
		nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return Request{
		UnderstandingRequest: understandingRequest, UnderstandingResult: understandingResult,
		GraphRequest: graphRequest, GraphResolution: graph.Resolution{Plan: plan, Source: graph.ResolutionSourceNebula},
		CandidateSets: []MentionCandidateSet{
			candidateSetForTest(scope.Release, MentionRef{understanding.EvidenceOriginInherited, MentionMetric, 0},
				llmCandidateForTest(scope.Release, search.ObjectMetric, "metric-sales-v1", 1, 1, nil)),
			candidateSetForTest(scope.Release, MentionRef{understanding.EvidenceOriginCurrent, MentionDimension, 0},
				llmCandidateForTest(scope.Release, search.ObjectDimension, "dimension-region-v1", 1, 1, nil)),
		},
	}
}

func understandingFixture(
	t *testing.T,
	question string,
	metrics, dimensions, members []string,
) (understanding.UnderstandingRequest, understanding.UnderstandingResult, askdata.PolicyScope) {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		"tenant-binding", "actor-binding", []askdata.ID{"sales"}, []askdata.ID{"analyst"},
		askdata.ReleaseRef{ReleaseID: "release-binding-v1", ContentHash: askdata.HashBytes([]byte("release-binding-v1"))},
	)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := understanding.NormalizeQuestion(question)
	if err != nil {
		t.Fatal(err)
	}
	parser, err := understanding.NewRuleParser(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), time.January)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parser.Parse(normalized)
	if err != nil {
		t.Fatal(err)
	}
	contextRequest := understanding.ContextMergeRequest{
		ConversationID: "conversation-binding", TurnID: "turn-binding", Scope: scope,
		Question: normalized, Rules: rules, Continuation: understanding.ContextIndependent,
	}
	contextResult, err := understanding.MergeContext(contextRequest)
	if err != nil {
		t.Fatal(err)
	}
	request := understanding.UnderstandingRequest{
		ContextRequest: contextRequest, Context: contextResult,
		ExactMatches: []understanding.ExactMatch{}, SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	reviewer := bindingReviewerFunc(func(
		_ context.Context,
		input understanding.UnderstandingReviewInput,
	) (understanding.UnderstandingProposal, error) {
		policy := evidenceForKind(t, input.AllowedEvidenceRefs, askdata.EvidenceKindPolicy)
		conversation := evidenceForKind(t, input.AllowedEvidenceRefs, askdata.EvidenceKindConversation)
		current := understanding.QuestionUnderstanding{
			SchemaVersion: understanding.SchemaVersion, Question: question,
			DomainHypotheses: []understanding.DomainHypothesis{{
				DomainID: "sales", Score: 1, EvidenceRefs: []askdata.EvidenceRef{policy},
			}},
			MetricMentions: []understanding.MetricMention{}, DimensionMentions: []understanding.DimensionMention{},
			ValueMentions: []understanding.ValueMention{}, Comparisons: []understanding.ComparisonMention{},
			Ordering: []understanding.OrderingMention{}, UnresolvedSpans: []understanding.UnresolvedSpan{},
		}
		requests := []understanding.UnderstandingEvidenceRequest{}
		for _, text := range metrics {
			span := spanForText(t, question, text)
			current.MetricMentions = append(current.MetricMentions, understanding.MetricMention{
				Text: text, Span: span, AggregationHint: understanding.AggregationDefault,
			})
			requests = append(requests, evidenceRequest(
				understanding.NeededMetricCandidates, text, span, conversation,
			))
		}
		for _, text := range dimensions {
			span := spanForText(t, question, text)
			current.DimensionMentions = append(current.DimensionMentions, understanding.DimensionMention{
				Text: text, Span: span, Role: understanding.DimensionRoleGroupBy,
			})
			requests = append(requests, evidenceRequest(
				understanding.NeededDimensionCandidates, text, span, conversation,
			))
		}
		for _, text := range members {
			span := spanForText(t, question, text)
			current.ValueMentions = append(current.ValueMentions, understanding.ValueMention{
				Text: text, Span: span, OperatorHint: understanding.ValueOperatorDefault,
			})
			requests = append(requests, evidenceRequest(
				understanding.NeededMemberCandidates, text, span, conversation,
			))
		}
		return understanding.UnderstandingProposal{
			SchemaVersion: understanding.UnderstandingReviewSchemaVersion,
			Understanding: current, Conflicts: []understanding.UnderstandingConflict{},
			EvidenceRequests: requests, EvidenceRefs: input.AllowedEvidenceRefs,
		}, nil
	})
	service, err := understanding.NewUnderstandingService(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Understand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return request, result, scope
}

func evidenceRequest(
	kind understanding.NeededEvidence,
	text string,
	span understanding.Span,
	ref askdata.EvidenceRef,
) understanding.UnderstandingEvidenceRequest {
	return understanding.UnderstandingEvidenceRequest{
		Origin: understanding.EvidenceOriginCurrent, NeededEvidence: kind, Text: text, Span: span,
		Reason: "需要稳定候选完成联合绑定。", EvidenceRefs: []askdata.EvidenceRef{ref},
	}
}

func candidateSetForTest(
	release askdata.ReleaseRef,
	mention MentionRef,
	options ...CandidateOption,
) MentionCandidateSet {
	hash := askdata.HashBytes([]byte("candidate-set:" + mentionIdentity(mention)))
	return MentionCandidateSet{
		Mention: mention,
		Evidence: askdata.EvidenceRef{
			EvidenceID: "candidate-set:" + askdata.ID(hash), Kind: askdata.EvidenceKindCandidateSet,
			SourceID: release.ReleaseID, ContentHash: hash,
		},
		Candidates: options,
	}
}

func llmCandidateForTest(
	release askdata.ReleaseRef,
	objectType search.ObjectType,
	versionID askdata.ID,
	score float64,
	rank int,
	parent *askdata.ID,
) CandidateOption {
	hash := askdata.HashBytes([]byte("retrieval:" + string(versionID)))
	return CandidateOption{
		Candidate: search.Candidate{
			ObjectType: objectType, ObjectVersionID: versionID, Score: score,
			Evidence: []search.SourceEvidence{{
				Source: search.SourceLexical, Rank: rank, SourceScore: score,
				Evidence: askdata.EvidenceRef{
					EvidenceID: "retrieval:" + versionID, Kind: askdata.EvidenceKindLexicalMatch,
					SourceID: versionID, ContentHash: hash,
				},
			}},
		},
		ParentDimensionVersionID: parent, SelectionSource: SelectionLLMRerank, ReviewerRank: rank,
		Gate:      gateForTest(release, search.GateAllow, "RULE_ALLOW"),
		RuleScore: 1, QualityScore: 1, CostScore: 1,
		FeatureEvidenceRefs: featureEvidenceForTest(versionID),
	}
}

func exactCandidateForTest(
	release askdata.ReleaseRef,
	versionID askdata.ID,
	score float64,
	parent *askdata.ID,
) CandidateOption {
	hash := askdata.HashBytes([]byte("exact:" + string(versionID)))
	return CandidateOption{
		Candidate: search.Candidate{
			ObjectType: search.ObjectMember, ObjectVersionID: versionID, Score: score,
			Evidence: []search.SourceEvidence{{
				Source: search.SourceExact, Rank: 1, SourceScore: score,
				Evidence: askdata.EvidenceRef{
					EvidenceID: "exact:" + versionID, Kind: askdata.EvidenceKindExactAlias,
					SourceID: versionID, ContentHash: hash,
				},
			}},
		},
		ParentDimensionVersionID: parent, SelectionSource: SelectionDeterministicExact,
		Gate:      gateForTest(release, search.GateAllow, "RULE_ALLOW"),
		RuleScore: 1, QualityScore: 1, CostScore: 1,
		FeatureEvidenceRefs: featureEvidenceForTest(versionID),
	}
}

func featureEvidenceForTest(versionID askdata.ID) []askdata.EvidenceRef {
	contractHash := askdata.HashBytes([]byte("contract:" + string(versionID)))
	qualityHash := askdata.HashBytes([]byte("quality:" + string(versionID)))
	return []askdata.EvidenceRef{
		{
			EvidenceID: "feature-contract:" + versionID, Kind: askdata.EvidenceKindSemanticContract,
			SourceID: versionID, ContentHash: contractHash,
		},
		{
			EvidenceID: "feature-quality:" + versionID, Kind: askdata.EvidenceKindDataQuality,
			SourceID: versionID, ContentHash: qualityHash,
		},
	}
}

func gateForTest(release askdata.ReleaseRef, verdict search.GateVerdict, reason string) search.DeterministicGate {
	hash := askdata.HashBytes([]byte("gate:" + reason))
	return search.DeterministicGate{
		Verdict: verdict, ReasonCodes: []string{reason},
		EvidenceRefs: []askdata.EvidenceRef{{
			EvidenceID: "gate:" + askdata.ID(hash), Kind: askdata.EvidenceKindRule,
			SourceID: release.ReleaseID, ContentHash: hash,
		}},
	}
}

func evidenceForKind(t *testing.T, values []askdata.EvidenceRef, kind askdata.EvidenceKind) askdata.EvidenceRef {
	t.Helper()
	for _, value := range values {
		if value.Kind == kind {
			return value
		}
	}
	t.Fatalf("missing evidence kind %s", kind)
	return askdata.EvidenceRef{}
}

func spanForText(t *testing.T, question, fragment string) understanding.Span {
	t.Helper()
	questionRunes, fragmentRunes := []rune(question), []rune(fragment)
	for start := 0; start+len(fragmentRunes) <= len(questionRunes); start++ {
		if string(questionRunes[start:start+len(fragmentRunes)]) == fragment {
			return understanding.Span{Start: start, End: start + len(fragmentRunes)}
		}
	}
	t.Fatalf("fragment %q not found in %q", fragment, question)
	return understanding.Span{}
}

func idPointer(value askdata.ID) *askdata.ID { return &value }

func explicitDimension(values []DimensionBinding) DimensionBinding {
	for _, value := range values {
		if value.Mention != nil {
			return value
		}
	}
	return DimensionBinding{}
}

func candidateSetIndex(values []MentionCandidateSet, kind MentionKind) int {
	for index, value := range values {
		if value.Mention.Kind == kind {
			return index
		}
	}
	return -1
}

func reverseOptions(values []CandidateOption) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseCandidateSets(values []MentionCandidateSet) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
