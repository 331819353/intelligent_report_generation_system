package ir

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/binding"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/security"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

type irReviewerFunc func(context.Context, understanding.UnderstandingReviewInput) (understanding.UnderstandingProposal, error)

func (reviewer irReviewerFunc) ReviewUnderstanding(
	ctx context.Context,
	input understanding.UnderstandingReviewInput,
) (understanding.UnderstandingProposal, error) {
	return reviewer(ctx, input)
}

func TestBuildSemanticIRFromReplayValidatedBinding(t *testing.T) {
	request := semanticIRBuildFixture(t)
	artifact, err := Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.IR.SemanticReleaseID != request.BindingResult.Scope.Release.ReleaseID ||
		artifact.IR.SemanticContentHash != request.BindingResult.Scope.Release.ContentHash ||
		artifact.IR.ModelVersionID != "model-sales-v1" || len(artifact.IR.Metrics) != 1 ||
		len(artifact.IR.GroupBy) != 1 || len(artifact.IR.Filters) != 1 {
		t.Fatalf("unexpected semantic IR: %#v", artifact.IR)
	}
	if artifact.IR.GroupBy[0].DimensionVersionID != "dimension-region-v1" ||
		artifact.IR.Filters[0].DimensionVersionID != "dimension-region-v1" ||
		artifact.IR.Filters[0].Operator != FilterEquals ||
		!reflect.DeepEqual(artifact.IR.Filters[0].MemberVersionIDs, []askdata.ID{"member-east-v1"}) {
		t.Fatalf("unexpected dimension or member projection: %#v", artifact.IR)
	}
	if artifact.IR.TimeRange == nil || artifact.IR.TimeRange.DimensionVersionID != "dimension-order-date-v1" ||
		artifact.IR.TimeRange.Start != "2026-01-01" || artifact.IR.TimeRange.EndExclusive != "2027-01-01" ||
		artifact.IR.Comparison == nil || artifact.IR.Comparison.Type != ComparisonYearOverYear ||
		artifact.IR.Limit != 5 {
		t.Fatalf("unexpected time/comparison/limit: %#v", artifact.IR)
	}
	if len(artifact.IR.Sort) != 1 || artifact.IR.Sort[0].TargetType != SortTargetMetric ||
		artifact.IR.Sort[0].TargetVersionID != "metric-sales-v1" ||
		artifact.IR.Sort[0].Direction != SortDescending {
		t.Fatalf("unexpected sort: %#v", artifact.IR.Sort)
	}
	if !strings.HasPrefix(artifact.IR.Metrics[0].Alias, "metric_") ||
		len(artifact.IR.Metrics[0].Alias) > 64 || artifact.IRHash.Validate() != nil ||
		artifact.ArtifactHash.Validate() != nil {
		t.Fatalf("invalid aliases or hashes: %#v", artifact)
	}
	if err := artifact.ValidateAgainst(request); err != nil {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
	permuted := request
	permuted.BindingRequest.CandidateSets = append([]binding.MentionCandidateSet(nil), request.BindingRequest.CandidateSets...)
	for left, right := 0, len(permuted.BindingRequest.CandidateSets)-1; left < right; left, right = left+1, right-1 {
		permuted.BindingRequest.CandidateSets[left], permuted.BindingRequest.CandidateSets[right] =
			permuted.BindingRequest.CandidateSets[right], permuted.BindingRequest.CandidateSets[left]
	}
	permutedArtifact, err := Build(permuted)
	if err != nil || !reflect.DeepEqual(permutedArtifact, artifact) {
		t.Fatalf("permuted Build() = %#v, %v", permutedArtifact, err)
	}

	raw, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBuildArtifact(raw, request)
	if err != nil || !reflect.DeepEqual(decoded, artifact) {
		t.Fatalf("DecodeBuildArtifact() = %#v, %v", decoded, err)
	}
	unsafe := strings.Replace(string(raw), `"artifactHash":`, `"sql":"select * from secret","artifactHash":`, 1)
	if _, err := DecodeBuildArtifact([]byte(unsafe), request); err == nil {
		t.Fatal("unknown physical SQL field must be rejected")
	}
	tampered := artifact
	tampered.IR.Limit++
	tamperedRaw, _ := json.Marshal(tampered)
	if _, err := DecodeBuildArtifact(tamperedRaw, request); !errors.Is(err, ErrInvalidBuildArtifact) {
		t.Fatalf("tampered artifact error = %v", err)
	}
}

func TestBuildRejectsBindingReplayAndUnrepresentableModelShape(t *testing.T) {
	request := semanticIRBuildFixture(t)
	tampered := request
	tampered.BindingResult.ResultHash = askdata.HashBytes([]byte("tampered"))
	if _, err := Build(tampered); !errors.Is(err, ErrInvalidBuildRequest) {
		t.Fatalf("tampered binding error = %v", err)
	}

	bundle := request.BindingResult.Bundles[0]
	bundle.ModelVersionIDs = []askdata.ID{"model-a-v1", "model-b-v1"}
	if _, err := validateIRModelShape(bundle); err == nil || !strings.Contains(err.Error(), "cannot represent") {
		t.Fatalf("unexpected model shape error = %v", err)
	}
	plan := request.BindingRequest.GraphResolution.Plan
	plan.MemberOwnerships[0].Status = graph.MemberStatusExpired
	if err := validateBundleSemantics(request.BindingResult.Bundles[0], plan, "model-sales-v1"); err == nil || !strings.Contains(err.Error(), "ACTIVE") {
		t.Fatalf("expired member error = %v", err)
	}
}

func TestFilterFamiliesAreBoundedAndNeverStoreRawValues(t *testing.T) {
	dimensions := map[askdata.ID]struct{}{"dimension-region-v1": {}}
	filters, err := buildFilters([]binding.MemberBinding{
		{MemberVersionID: "east-v1", DimensionVersionID: "dimension-region-v1", OperatorHint: understanding.ValueOperatorDefault},
		{MemberVersionID: "west-v1", DimensionVersionID: "dimension-region-v1", OperatorHint: understanding.ValueOperatorEquals},
		{MemberVersionID: "blocked-v1", DimensionVersionID: "dimension-region-v1", OperatorHint: understanding.ValueOperatorNotIn},
	}, dimensions)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 2 || filters[0].Operator != FilterIn || filters[1].Operator != FilterNotIn {
		t.Fatalf("unexpected filters: %#v", filters)
	}
	if _, err := buildFilters(nil, dimensions); err == nil {
		t.Fatal("unbound FILTER dimension must be rejected")
	}
}

func TestSortDimensionIsProjectedAtNaturalGrain(t *testing.T) {
	mention := binding.MentionRef{Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionDimension, Index: 0}
	groups, _, _, sorts, err := buildDimensions([]binding.DimensionBinding{{
		Mention: &mention, DimensionVersionID: "dimension-region-v1", Role: understanding.DimensionRoleSort,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].DimensionVersionID != "dimension-region-v1" || groups[0].Grain != nil || len(sorts) != 1 {
		t.Fatalf("unexpected projected sort dimension: groups=%#v sorts=%#v", groups, sorts)
	}
}

func TestInheritedTimeRequiresSnapshotBoundResolutionProof(t *testing.T) {
	question := "今年"
	normalized, err := understanding.NormalizeQuestion(question)
	if err != nil {
		t.Fatal(err)
	}
	parser, err := understanding.NewRuleParser(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), time.January)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parser.Parse(normalized)
	if err != nil || rules.Time == nil {
		t.Fatalf("Parse() = %#v, %v", rules, err)
	}
	snapshotHash := askdata.HashBytes([]byte("previous-snapshot"))
	proof, err := NewInheritedTimeResolution(snapshotHash, *rules.Time)
	if err != nil {
		t.Fatal(err)
	}
	request := BuildRequest{
		BindingRequest: binding.Request{UnderstandingResult: understanding.UnderstandingResult{
			Context: understanding.ContextMergeResult{
				PreviousSnapshotHash: &snapshotHash,
				Inherited:            &understanding.QuestionUnderstanding{Question: question},
			},
		}},
		InheritedTimeResolution: &proof,
	}
	bundle := binding.Bundle{Time: &binding.TimeBinding{
		Origin: understanding.EvidenceOriginInherited, Value: rules.Time.Understanding(),
	}}
	if err := validateTimeResolution(request, bundle); err != nil {
		t.Fatalf("validateTimeResolution() error = %v", err)
	}
	tampered := proof
	tampered.Resolved.Start = "2025-01-01"
	request.InheritedTimeResolution = &tampered
	if err := validateTimeResolution(request, bundle); err == nil {
		t.Fatal("tampered inherited time proof must be rejected")
	}
}

func semanticIRBuildFixture(t *testing.T) BuildRequest {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		"tenant-ir", "actor-ir", []askdata.ID{"sales"}, []askdata.ID{"analyst"},
		askdata.ReleaseRef{ReleaseID: "release-ir-v1", ContentHash: askdata.HashBytes([]byte("release-ir-v1"))},
	)
	if err != nil {
		t.Fatal(err)
	}
	question := "今年华东销售额按地区同比前5名"
	normalized, err := understanding.NormalizeQuestion(question)
	if err != nil {
		t.Fatal(err)
	}
	parser, err := understanding.NewRuleParser(time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC), time.January)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parser.Parse(normalized)
	if err != nil || rules.Time == nil || rules.Ranking == nil || len(rules.Comparisons) != 1 {
		t.Fatalf("unexpected deterministic rules: %#v, %v", rules, err)
	}
	contextRequest := understanding.ContextMergeRequest{
		ConversationID: "conversation-ir", TurnID: "turn-ir", Scope: scope,
		Question: normalized, Rules: rules, Continuation: understanding.ContextIndependent,
	}
	contextResult, err := understanding.MergeContext(contextRequest)
	if err != nil {
		t.Fatal(err)
	}
	understandingRequest := understanding.UnderstandingRequest{
		ContextRequest: contextRequest, Context: contextResult,
		ExactMatches: []understanding.ExactMatch{}, SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	reviewer := irReviewerFunc(func(
		_ context.Context,
		input understanding.UnderstandingReviewInput,
	) (understanding.UnderstandingProposal, error) {
		policy := irEvidenceForKind(t, input.AllowedEvidenceRefs, askdata.EvidenceKindPolicy)
		conversation := irEvidenceForKind(t, input.AllowedEvidenceRefs, askdata.EvidenceKindConversation)
		metricSpan := irSpanForText(t, question, "销售额")
		regionSpan := irSpanForText(t, question, "地区")
		timeSpan := irSpanForText(t, question, "今年")
		memberSpan := irSpanForText(t, question, "华东")
		current := understanding.QuestionUnderstanding{
			SchemaVersion: understanding.SchemaVersion, Question: question,
			DomainHypotheses: []understanding.DomainHypothesis{{
				DomainID: "sales", Score: 1, EvidenceRefs: []askdata.EvidenceRef{policy},
			}},
			MetricMentions: []understanding.MetricMention{{
				Text: "销售额", Span: metricSpan, AggregationHint: understanding.AggregationDefault,
			}},
			DimensionMentions: []understanding.DimensionMention{
				{Text: "地区", Span: regionSpan, Role: understanding.DimensionRoleGroupBy},
				{Text: "今年", Span: timeSpan, Role: understanding.DimensionRoleTime, Grain: irUnderstandingGrain(understanding.TimeGrainYear)},
			},
			ValueMentions: []understanding.ValueMention{{
				Text: "华东", Span: memberSpan, OperatorHint: understanding.ValueOperatorDefault,
			}},
			Time: irTimeValue(rules), Comparisons: rules.Comparisons,
			Ordering: []understanding.OrderingMention{{
				Text: rules.Ranking.Text, Span: rules.Ranking.Span,
				TargetText: "销售额", Direction: rules.Ranking.Direction,
			}},
			Limit: irIntPointer(rules.Ranking.Limit), UnresolvedSpans: []understanding.UnresolvedSpan{},
		}
		requests := []understanding.UnderstandingEvidenceRequest{
			irEvidenceRequest(understanding.NeededMetricCandidates, "销售额", metricSpan, conversation),
			irEvidenceRequest(understanding.NeededDimensionCandidates, "地区", regionSpan, conversation),
			irEvidenceRequest(understanding.NeededDimensionCandidates, "今年", timeSpan, conversation),
			irEvidenceRequest(understanding.NeededMemberCandidates, "华东", memberSpan, conversation),
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
	understandingResult, err := service.Understand(context.Background(), understandingRequest)
	if err != nil {
		t.Fatal(err)
	}
	graphRequest, err := (graph.PlanRequest{
		Scope: scope, DomainID: "sales",
		MetricRefs: []graph.ObjectVersionRef{{ObjectID: "metric-sales", VersionID: "metric-sales-v1", Version: 1}},
		ModelRefs:  []graph.ObjectVersionRef{{ObjectID: "model-sales", VersionID: "model-sales-v1", Version: 1}},
		DimensionRefs: []graph.ObjectVersionRef{
			{ObjectID: "dimension-region", VersionID: "dimension-region-v1", Version: 1},
			{ObjectID: "dimension-order-date", VersionID: "dimension-order-date-v1", Version: 1},
		},
		MemberRefs: []graph.ObjectVersionRef{{ObjectID: "member-east", VersionID: "member-east-v1", Version: 1}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := graph.NewGraphPlan(
		graphRequest, graphRequest.ModelRefs,
		[]graph.MetricModelBinding{{MetricVersionID: "metric-sales-v1", ModelVersionID: "model-sales-v1"}},
		[]graph.DimensionCompatibility{
			{ModelVersionID: "model-sales-v1", DimensionVersionID: "dimension-region-v1"},
			{ModelVersionID: "model-sales-v1", DimensionVersionID: "dimension-order-date-v1"},
		},
		[]graph.MemberOwnership{{
			MemberVersionID: "member-east-v1", DimensionVersionID: "dimension-region-v1", Status: graph.MemberStatusActive,
		}}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	bindingRequest := binding.Request{
		UnderstandingRequest: understandingRequest, UnderstandingResult: understandingResult,
		GraphRequest: graphRequest, GraphResolution: graph.Resolution{Plan: plan, Source: graph.ResolutionSourceNebula},
		CandidateSets: []binding.MentionCandidateSet{
			irCandidateSet(scope.Release, binding.MentionRef{Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionMetric, Index: 0},
				irLLMCandidate(scope.Release, search.ObjectMetric, "metric-sales-v1", nil)),
			irCandidateSet(scope.Release, binding.MentionRef{Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionDimension, Index: 0},
				irLLMCandidate(scope.Release, search.ObjectDimension, "dimension-region-v1", nil)),
			irCandidateSet(scope.Release, binding.MentionRef{Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionDimension, Index: 1},
				irLLMCandidate(scope.Release, search.ObjectDimension, "dimension-order-date-v1", nil)),
			irCandidateSet(scope.Release, binding.MentionRef{Origin: understanding.EvidenceOriginCurrent, Kind: binding.MentionMember, Index: 0},
				irExactCandidate(scope.Release, "member-east-v1", irIDPointer("dimension-region-v1"))),
		},
	}
	bindingResult, err := binding.Bind(bindingRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(bindingResult.Bundles) != 1 {
		t.Fatalf("unexpected bundles: %#v", bindingResult.Bundles)
	}
	return BuildRequest{
		BindingRequest: bindingRequest, BindingResult: bindingResult,
		BundleHash: bindingResult.Bundles[0].BundleHash,
	}
}

func irCandidateSet(
	release askdata.ReleaseRef,
	mention binding.MentionRef,
	option binding.CandidateOption,
) binding.MentionCandidateSet {
	hash := askdata.HashBytes([]byte("candidate-set:" + string(mention.Kind) + string(rune(mention.Index))))
	return binding.MentionCandidateSet{
		Mention: mention,
		Evidence: askdata.EvidenceRef{
			EvidenceID: "candidate-set:" + askdata.ID(hash), Kind: askdata.EvidenceKindCandidateSet,
			SourceID: release.ReleaseID, ContentHash: hash,
		},
		Candidates: []binding.CandidateOption{option},
	}
}

func irLLMCandidate(
	release askdata.ReleaseRef,
	objectType search.ObjectType,
	versionID askdata.ID,
	parent *askdata.ID,
) binding.CandidateOption {
	return irCandidate(release, objectType, versionID, parent, binding.SelectionLLMRerank, search.SourceLexical)
}

func irExactCandidate(release askdata.ReleaseRef, versionID askdata.ID, parent *askdata.ID) binding.CandidateOption {
	return irCandidate(release, search.ObjectMember, versionID, parent, binding.SelectionDeterministicExact, search.SourceExact)
}

func irCandidate(
	release askdata.ReleaseRef,
	objectType search.ObjectType,
	versionID askdata.ID,
	parent *askdata.ID,
	selection binding.CandidateSelectionSource,
	source search.RetrievalSource,
) binding.CandidateOption {
	retrievalHash := askdata.HashBytes([]byte("retrieval:" + string(versionID)))
	retrievalKind := askdata.EvidenceKindLexicalMatch
	if source == search.SourceExact {
		retrievalKind = askdata.EvidenceKindExactAlias
	}
	reviewerRank := 1
	if selection == binding.SelectionDeterministicExact {
		reviewerRank = 0
	}
	return binding.CandidateOption{
		Candidate: search.Candidate{
			ObjectType: objectType, ObjectVersionID: versionID, Score: 1,
			Evidence: []search.SourceEvidence{{
				Source: source, Rank: 1, SourceScore: 1,
				Evidence: askdata.EvidenceRef{
					EvidenceID: "retrieval:" + versionID, Kind: retrievalKind,
					SourceID: versionID, ContentHash: retrievalHash,
				},
			}},
		},
		ParentDimensionVersionID: parent, SelectionSource: selection, ReviewerRank: reviewerRank,
		Gate: search.DeterministicGate{
			Verdict: search.GateAllow, ReasonCodes: []string{"RULE_ALLOW"},
			EvidenceRefs: []askdata.EvidenceRef{{
				EvidenceID: "gate:" + versionID, Kind: askdata.EvidenceKindRule,
				SourceID: release.ReleaseID, ContentHash: askdata.HashBytes([]byte("gate:" + string(versionID))),
			}},
		},
		RuleScore: 1, QualityScore: 1, CostScore: 1,
		FeatureEvidenceRefs: []askdata.EvidenceRef{
			{EvidenceID: "contract:" + versionID, Kind: askdata.EvidenceKindSemanticContract, SourceID: versionID, ContentHash: askdata.HashBytes([]byte("contract:" + string(versionID)))},
			{EvidenceID: "quality:" + versionID, Kind: askdata.EvidenceKindDataQuality, SourceID: versionID, ContentHash: askdata.HashBytes([]byte("quality:" + string(versionID)))},
		},
	}
}

func irEvidenceRequest(
	kind understanding.NeededEvidence,
	text string,
	span understanding.Span,
	ref askdata.EvidenceRef,
) understanding.UnderstandingEvidenceRequest {
	return understanding.UnderstandingEvidenceRequest{
		Origin: understanding.EvidenceOriginCurrent, NeededEvidence: kind, Text: text, Span: span,
		Reason: "需要稳定候选构造 Semantic IR。", EvidenceRefs: []askdata.EvidenceRef{ref},
	}
}

func irEvidenceForKind(t *testing.T, values []askdata.EvidenceRef, kind askdata.EvidenceKind) askdata.EvidenceRef {
	t.Helper()
	for _, value := range values {
		if value.Kind == kind {
			return value
		}
	}
	t.Fatalf("missing evidence kind %s", kind)
	return askdata.EvidenceRef{}
}

func irSpanForText(t *testing.T, question, fragment string) understanding.Span {
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

func irIDPointer(value askdata.ID) *askdata.ID { return &value }

func irIntPointer(value int) *int { return &value }

func irUnderstandingGrain(value understanding.TimeGrain) *understanding.TimeGrain { return &value }

func irTimeValue(result understanding.RuleParseResult) *understanding.TimeUnderstanding {
	if result.Time == nil {
		return nil
	}
	value := result.Time.Understanding()
	return &value
}
