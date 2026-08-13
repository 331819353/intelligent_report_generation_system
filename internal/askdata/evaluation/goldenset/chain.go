package goldenset

import (
	"context"
	"errors"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/binding"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/security"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

// The additivity suite asserts what the compiler does with a governed metric
// contract. To be worth anything it has to reach the compiler the way a real
// question does: understanding -> binding -> Semantic IR -> contract resolution
// -> Adapt. Every one of those stages validates the previous one, so a chain
// built here is a chain production would accept — and a shortcut past any of
// them would mean the suite exercises a plan no resolver ever produced.

var ErrGoldenChain = errors.New("golden set chain is invalid")

// chainReferenceTime anchors the deterministic rule parser. It is fixed so a
// relative phrase like 本月 resolves to the same interval on every run.
var chainReferenceTime = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)

type chainDimension struct {
	ObjectID  askdata.ID
	VersionID askdata.ID
	Text      string
}

type chainMetric struct {
	ObjectID  askdata.ID
	VersionID askdata.ID
	Text      string
}

// chainSpec is the shape of one synthetic question. Question must contain each
// mention text exactly once; spans are derived from it rather than declared,
// so a spec whose text and mentions disagree cannot be built at all.
type chainSpec struct {
	Key      string
	Question string
	Metrics  []chainMetric
	GroupBy  []chainDimension
	WithTime bool
}

type chain struct {
	scope          askdata.PolicyScope
	resolveRequest compiler.ResolveRequest
}

type understandingReviewer func(
	context.Context, understanding.UnderstandingReviewInput,
) (understanding.UnderstandingProposal, error)

func (reviewer understandingReviewer) ReviewUnderstanding(
	ctx context.Context,
	input understanding.UnderstandingReviewInput,
) (understanding.UnderstandingProposal, error) {
	return reviewer(ctx, input)
}

// runeSpan locates a mention in the question. It refuses an ambiguous or absent
// substring: a mention the reader cannot point at is not a mention.
func runeSpan(question, text string) (understanding.Span, error) {
	runes := []rune(question)
	target := []rune(text)
	found := -1
	for index := 0; index+len(target) <= len(runes); index++ {
		if string(runes[index:index+len(target)]) != text {
			continue
		}
		if found >= 0 {
			return understanding.Span{}, fmt.Errorf("%w: %q appears twice in %q", ErrGoldenChain, text, question)
		}
		found = index
	}
	if found < 0 {
		return understanding.Span{}, fmt.Errorf("%w: %q is not in %q", ErrGoldenChain, text, question)
	}
	return understanding.Span{Start: found, End: found + len(target)}, nil
}

func buildChain(spec chainSpec) (chain, error) {
	if len(spec.Metrics) == 0 {
		return chain{}, fmt.Errorf("%w: %s selects no metric", ErrGoldenChain, spec.Key)
	}
	release := askdata.ReleaseRef{
		ReleaseID: goldenReleaseID, ContentHash: askdata.HashBytes([]byte(goldenReleaseID)),
	}
	scope, err := askdata.NewPolicyScope(
		goldenTenantID, goldenActorID, []askdata.ID{goldenDomainID}, []askdata.ID{goldenRoleID}, release,
	)
	if err != nil {
		return chain{}, err
	}
	normalized, err := understanding.NormalizeQuestion(spec.Question)
	if err != nil {
		return chain{}, err
	}
	parser, err := understanding.NewRuleParser(chainReferenceTime, time.January)
	if err != nil {
		return chain{}, err
	}
	rules, err := parser.Parse(normalized)
	if err != nil {
		return chain{}, err
	}
	if spec.WithTime != (rules.Time != nil) {
		return chain{}, fmt.Errorf(
			"%w: %s declares withTime=%t but the deterministic parser found time=%t",
			ErrGoldenChain, spec.Key, spec.WithTime, rules.Time != nil,
		)
	}
	if len(rules.Groupings) != 0 {
		// A grouping phrase makes the parser authoritative over the GROUP_BY
		// roles, which would make the suite depend on grammar rather than on the
		// contract under test. The specs avoid the trigger words on purpose.
		return chain{}, fmt.Errorf("%w: %s triggered a deterministic grouping rule", ErrGoldenChain, spec.Key)
	}
	contextRequest := understanding.ContextMergeRequest{
		ConversationID: askdata.ID("conversation-golden-" + spec.Key),
		TurnID:         askdata.ID("turn-golden-" + spec.Key),
		Scope:          scope, Question: normalized, Rules: rules,
		Continuation: understanding.ContextIndependent,
	}
	contextResult, err := understanding.MergeContext(contextRequest)
	if err != nil {
		return chain{}, err
	}
	understandingRequest := understanding.UnderstandingRequest{
		ContextRequest: contextRequest, Context: contextResult,
		ExactMatches: []understanding.ExactMatch{}, SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	proposal, err := chainProposal(spec, rules)
	if err != nil {
		return chain{}, err
	}
	service, err := understanding.NewUnderstandingService(understandingReviewer(
		func(
			_ context.Context, input understanding.UnderstandingReviewInput,
		) (understanding.UnderstandingProposal, error) {
			return proposal(input)
		},
	))
	if err != nil {
		return chain{}, err
	}
	understandingResult, err := service.Understand(context.Background(), understandingRequest)
	if err != nil {
		return chain{}, fmt.Errorf("%w: %s understanding: %v", ErrGoldenChain, spec.Key, err)
	}
	graphRequest, graphPlan, err := chainGraphPlan(spec, scope)
	if err != nil {
		return chain{}, err
	}
	bindingRequest := binding.Request{
		UnderstandingRequest: understandingRequest, UnderstandingResult: understandingResult,
		GraphRequest:    graphRequest,
		GraphResolution: graph.Resolution{Plan: graphPlan, Source: graph.ResolutionSourceNebula},
		CandidateSets:   chainCandidateSets(spec, release),
	}
	bindingResult, err := binding.Bind(bindingRequest)
	if err != nil {
		return chain{}, fmt.Errorf("%w: %s binding: %v", ErrGoldenChain, spec.Key, err)
	}
	if len(bindingResult.Bundles) == 0 {
		return chain{}, fmt.Errorf("%w: %s produced no bundle", ErrGoldenChain, spec.Key)
	}
	buildRequest := ir.BuildRequest{
		BindingRequest: bindingRequest, BindingResult: bindingResult,
		BundleHash: bindingResult.Bundles[0].BundleHash,
	}
	buildArtifact, err := ir.Build(buildRequest)
	if err != nil {
		return chain{}, fmt.Errorf("%w: %s IR build: %v", ErrGoldenChain, spec.Key, err)
	}
	if err := assertChainShape(spec, buildArtifact.IR); err != nil {
		return chain{}, err
	}
	return chain{
		scope:          scope,
		resolveRequest: compiler.ResolveRequest{BuildRequest: buildRequest, BuildArtifact: buildArtifact},
	}, nil
}

// assertChainShape refuses a chain that did not produce the query the spec
// describes. Silently compiling a different shape would move a case into a
// category it does not belong to and the suite would still report a pass.
func assertChainShape(spec chainSpec, semanticIR ir.SemanticIR) error {
	if len(semanticIR.Metrics) != len(spec.Metrics) {
		return fmt.Errorf(
			"%w: %s expected %d metrics, IR has %d",
			ErrGoldenChain, spec.Key, len(spec.Metrics), len(semanticIR.Metrics),
		)
	}
	if len(semanticIR.GroupBy) != len(spec.GroupBy) {
		return fmt.Errorf(
			"%w: %s expected %d group-by dimensions, IR has %d",
			ErrGoldenChain, spec.Key, len(spec.GroupBy), len(semanticIR.GroupBy),
		)
	}
	if spec.WithTime != (semanticIR.TimeRange != nil) {
		return fmt.Errorf("%w: %s time range presence does not match the spec", ErrGoldenChain, spec.Key)
	}
	return nil
}

func chainProposal(
	spec chainSpec,
	rules understanding.RuleParseResult,
) (func(understanding.UnderstandingReviewInput) (understanding.UnderstandingProposal, error), error) {
	metricMentions := make([]understanding.MetricMention, 0, len(spec.Metrics))
	metricSpans := make([]understanding.Span, 0, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		span, err := runeSpan(spec.Question, metric.Text)
		if err != nil {
			return nil, err
		}
		metricSpans = append(metricSpans, span)
		metricMentions = append(metricMentions, understanding.MetricMention{
			Text: metric.Text, Span: span, AggregationHint: understanding.AggregationDefault,
		})
	}
	dimensionMentions := make([]understanding.DimensionMention, 0, len(spec.GroupBy)+1)
	dimensionTexts := make([]string, 0, len(spec.GroupBy)+1)
	dimensionSpans := make([]understanding.Span, 0, len(spec.GroupBy)+1)
	for _, dimension := range spec.GroupBy {
		span, err := runeSpan(spec.Question, dimension.Text)
		if err != nil {
			return nil, err
		}
		dimensionSpans = append(dimensionSpans, span)
		dimensionTexts = append(dimensionTexts, dimension.Text)
		dimensionMentions = append(dimensionMentions, understanding.DimensionMention{
			Text: dimension.Text, Span: span, Role: understanding.DimensionRoleGroupBy,
		})
	}
	var timeUnderstanding *understanding.TimeUnderstanding
	if rules.Time != nil {
		resolved := rules.Time.Understanding()
		timeUnderstanding = &resolved
		// A resolved time is only representable in Semantic IR alongside exactly
		// one TIME-role dimension, which is what pins the interval to a column.
		// The mention therefore sits on the phrase the deterministic parser
		// matched, not on a span the reviewer invented.
		dimensionSpans = append(dimensionSpans, resolved.Span)
		dimensionTexts = append(dimensionTexts, resolved.Text)
		dimensionMentions = append(dimensionMentions, understanding.DimensionMention{
			Text: resolved.Text, Span: resolved.Span, Role: understanding.DimensionRoleTime,
		})
	}
	return func(
		input understanding.UnderstandingReviewInput,
	) (understanding.UnderstandingProposal, error) {
		policy, ok := chainEvidence(input.AllowedEvidenceRefs, askdata.EvidenceKindPolicy)
		if !ok {
			return understanding.UnderstandingProposal{}, errors.New("policy evidence is missing")
		}
		conversation, ok := chainEvidence(input.AllowedEvidenceRefs, askdata.EvidenceKindConversation)
		if !ok {
			return understanding.UnderstandingProposal{}, errors.New("conversation evidence is missing")
		}
		requests := make([]understanding.UnderstandingEvidenceRequest, 0, len(spec.Metrics)+len(spec.GroupBy))
		for index, metric := range spec.Metrics {
			requests = append(requests, understanding.UnderstandingEvidenceRequest{
				Origin: understanding.EvidenceOriginCurrent, NeededEvidence: understanding.NeededMetricCandidates,
				Text: metric.Text, Span: metricSpans[index],
				Reason:       "需要稳定指标候选构造 Semantic IR。",
				EvidenceRefs: []askdata.EvidenceRef{conversation},
			})
		}
		for index, text := range dimensionTexts {
			requests = append(requests, understanding.UnderstandingEvidenceRequest{
				Origin: understanding.EvidenceOriginCurrent, NeededEvidence: understanding.NeededDimensionCandidates,
				Text: text, Span: dimensionSpans[index],
				Reason:       "需要稳定维度候选构造 Semantic IR。",
				EvidenceRefs: []askdata.EvidenceRef{conversation},
			})
		}
		return understanding.UnderstandingProposal{
			SchemaVersion: understanding.UnderstandingReviewSchemaVersion,
			Understanding: understanding.QuestionUnderstanding{
				SchemaVersion: understanding.SchemaVersion, Question: spec.Question,
				DomainHypotheses: []understanding.DomainHypothesis{{
					DomainID: goldenDomainID, Score: 1, EvidenceRefs: []askdata.EvidenceRef{policy},
				}},
				MetricMentions: metricMentions, DimensionMentions: dimensionMentions,
				ValueMentions: []understanding.ValueMention{}, Time: timeUnderstanding,
				Comparisons:     []understanding.ComparisonMention{},
				Ordering:        []understanding.OrderingMention{},
				UnresolvedSpans: []understanding.UnresolvedSpan{},
			},
			Conflicts: []understanding.UnderstandingConflict{}, EvidenceRequests: requests,
			EvidenceRefs: input.AllowedEvidenceRefs,
		}, nil
	}, nil
}

func chainGraphPlan(spec chainSpec, scope askdata.PolicyScope) (graph.PlanRequest, graph.GraphPlan, error) {
	metricRefs := make([]graph.ObjectVersionRef, 0, len(spec.Metrics))
	metricModels := make([]graph.MetricModelBinding, 0, len(spec.Metrics))
	for _, metric := range spec.Metrics {
		metricRefs = append(metricRefs, graph.ObjectVersionRef{
			ObjectID: metric.ObjectID, VersionID: metric.VersionID, Version: 1,
		})
		metricModels = append(metricModels, graph.MetricModelBinding{
			MetricVersionID: metric.VersionID, ModelVersionID: goldenModelVersionID,
		})
	}
	dimensionRefs := make([]graph.ObjectVersionRef, 0, len(spec.GroupBy)+1)
	compatibilities := make([]graph.DimensionCompatibility, 0, len(spec.GroupBy)+1)
	for _, dimension := range chainDimensions(spec) {
		dimensionRefs = append(dimensionRefs, graph.ObjectVersionRef{
			ObjectID: dimension.ObjectID, VersionID: dimension.VersionID, Version: 1,
		})
		compatibilities = append(compatibilities, graph.DimensionCompatibility{
			ModelVersionID: goldenModelVersionID, DimensionVersionID: dimension.VersionID,
		})
	}
	modelRefs := []graph.ObjectVersionRef{
		{ObjectID: goldenModelObjectID, VersionID: goldenModelVersionID, Version: 1},
	}
	request, err := (graph.PlanRequest{
		Scope: scope, DomainID: goldenDomainID, MetricRefs: metricRefs,
		ModelRefs: modelRefs, DimensionRefs: dimensionRefs,
	}).Normalize()
	if err != nil {
		return graph.PlanRequest{}, graph.GraphPlan{}, err
	}
	plan, err := graph.NewGraphPlan(request, request.ModelRefs, metricModels, compatibilities, nil, nil)
	if err != nil {
		return graph.PlanRequest{}, graph.GraphPlan{}, err
	}
	return request, plan, nil
}

func chainCandidateSets(spec chainSpec, release askdata.ReleaseRef) []binding.MentionCandidateSet {
	sets := make([]binding.MentionCandidateSet, 0, len(spec.Metrics)+len(spec.GroupBy))
	for index, metric := range spec.Metrics {
		sets = append(sets, chainCandidateSet(
			binding.MentionRef{
				Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionMetric, Index: index,
			},
			search.ObjectMetric, metric.VersionID, release,
		))
	}
	for index, dimension := range chainDimensions(spec) {
		sets = append(sets, chainCandidateSet(
			binding.MentionRef{
				Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionDimension, Index: index,
			},
			search.ObjectDimension, dimension.VersionID, release,
		))
	}
	return sets
}

func chainCandidateSet(
	mention binding.MentionRef,
	objectType search.ObjectType,
	objectVersionID askdata.ID,
	release askdata.ReleaseRef,
) binding.MentionCandidateSet {
	candidateSetHash := askdata.HashBytes([]byte("candidate-set:" + objectVersionID))
	retrievalHash := askdata.HashBytes([]byte("retrieval:" + objectVersionID))
	return binding.MentionCandidateSet{
		Mention: mention,
		Evidence: askdata.EvidenceRef{
			EvidenceID:  askdata.ID("candidate-set:" + string(candidateSetHash)),
			Kind:        askdata.EvidenceKindCandidateSet,
			SourceID:    release.ReleaseID,
			ContentHash: candidateSetHash,
		},
		Candidates: []binding.CandidateOption{{
			Candidate: search.Candidate{
				ObjectType: objectType, ObjectVersionID: objectVersionID, Score: 1,
				Evidence: []search.SourceEvidence{{
					Source: search.SourceLexical, Rank: 1, SourceScore: 1,
					Evidence: askdata.EvidenceRef{
						EvidenceID:  askdata.ID("retrieval:" + objectVersionID),
						Kind:        askdata.EvidenceKindLexicalMatch,
						SourceID:    objectVersionID,
						ContentHash: retrievalHash,
					},
				}},
			},
			SelectionSource: binding.SelectionLLMRerank, ReviewerRank: 1,
			Gate: search.DeterministicGate{
				Verdict: search.GateAllow, ReasonCodes: []string{"RULE_ALLOW"},
				EvidenceRefs: []askdata.EvidenceRef{{
					EvidenceID:  askdata.ID("gate:" + objectVersionID),
					Kind:        askdata.EvidenceKindRule,
					SourceID:    release.ReleaseID,
					ContentHash: askdata.HashBytes([]byte("gate:" + objectVersionID)),
				}},
			},
			RuleScore: 1, QualityScore: 1, CostScore: 1,
			FeatureEvidenceRefs: []askdata.EvidenceRef{
				{
					EvidenceID: askdata.ID("contract:" + objectVersionID), Kind: askdata.EvidenceKindSemanticContract,
					SourceID: objectVersionID, ContentHash: askdata.HashBytes([]byte("contract:" + objectVersionID)),
				},
				{
					EvidenceID: askdata.ID("quality:" + objectVersionID), Kind: askdata.EvidenceKindDataQuality,
					SourceID: objectVersionID, ContentHash: askdata.HashBytes([]byte("quality:" + objectVersionID)),
				},
			},
		}},
	}
}

// chainDimensions returns the dimensions in the same order as the proposal's
// dimension mentions. A binding candidate set is addressed by mention index, so
// the two orders diverging would silently bind a dimension to the wrong span.
func chainDimensions(spec chainSpec) []chainDimension {
	dimensions := append([]chainDimension(nil), spec.GroupBy...)
	if spec.WithTime {
		dimensions = append(dimensions, chainDimension{
			ObjectID: dimensionOrderDateObject, VersionID: dimensionOrderDate,
		})
	}
	return dimensions
}

func chainEvidence(values []askdata.EvidenceRef, kind askdata.EvidenceKind) (askdata.EvidenceRef, bool) {
	for _, value := range values {
		if value.Kind == kind {
			return value, true
		}
	}
	return askdata.EvidenceRef{}, false
}
