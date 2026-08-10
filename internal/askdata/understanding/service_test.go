package understanding

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/security"
)

type understandingReviewerFunc func(context.Context, UnderstandingReviewInput) (UnderstandingProposal, error)

func (reviewer understandingReviewerFunc) ReviewUnderstanding(ctx context.Context, input UnderstandingReviewInput) (UnderstandingProposal, error) {
	return reviewer(ctx, input)
}

func TestUnderstandingServiceBuildsBoundedFactsAndAcceptsCompleteIntent(t *testing.T) {
	t.Parallel()
	request := completeUnderstandingRequestForTest(t)
	var received UnderstandingReviewInput
	reviewer := understandingReviewerFunc(func(_ context.Context, input UnderstandingReviewInput) (UnderstandingProposal, error) {
		received = input
		return completeProposalForTest(t, request, input), nil
	})
	service, err := NewUnderstandingService(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Understand(context.Background(), request)
	if err != nil {
		t.Fatalf("understand: %v", err)
	}
	if received.Stage != cognition.StageUnderstanding || len(received.Facts) != 4 {
		t.Fatalf("unexpected review input: %+v", received)
	}
	wantKinds := []cognition.FactKind{
		cognition.FactConversation, cognition.FactExactMatches,
		cognition.FactRuleParse, cognition.FactPolicyEvidence,
	}
	for index, want := range wantKinds {
		if received.Facts[index].Kind != want {
			t.Fatalf("facts[%d].kind = %s, want %s", index, received.Facts[index].Kind, want)
		}
	}
	var exactFact struct {
		Matches  []ExactMatch   `json:"matches"`
		Residual []ResidualSpan `json:"residualSpans"`
	}
	if err := json.Unmarshal(received.Facts[1].Payload, &exactFact); err != nil {
		t.Fatal(err)
	}
	if len(exactFact.Matches) != 2 ||
		!reflect.DeepEqual(exactFact.Residual, []ResidualSpan{{
			Text: "华东", Span: mustContextSpan(request.ContextRequest.Question.Original, "华东"),
		}}) {
		t.Fatalf("unexpected exact/residual evidence: %+v", exactFact)
	}
	if result.Current.Time == nil || result.Current.Time.Text != "今年" || len(result.Current.MetricMentions) != 1 ||
		len(result.Current.DimensionMentions) != 1 || len(result.Current.ValueMentions) != 1 ||
		len(result.EvidenceRequests) != 3 || len(result.Conflicts) != 0 {
		t.Fatalf("unexpected understanding result: %+v", result)
	}
	if err := result.Validate(request); err != nil {
		t.Fatalf("validate result: %v", err)
	}
	if result.ProposalHash.Validate() != nil || result.ResultHash.Validate() != nil {
		t.Fatalf("result hashes are invalid: %+v", result)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\"sql\"", "\"tableName\"", "\"columnName\"", "\"objectVersionId\""} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("result leaked forbidden physical field %s: %s", forbidden, payload)
		}
	}
}

func TestUnderstandingServicePinsDomainFromSelectedSessionScope(t *testing.T) {
	t.Parallel()
	request := completeUnderstandingRequestForTest(t)
	reviewer := understandingReviewerFunc(func(_ context.Context, input UnderstandingReviewInput) (UnderstandingProposal, error) {
		proposal := completeProposalForTest(t, request, input)
		proposal.Understanding.DomainHypotheses = []DomainHypothesis{}
		return proposal, nil
	})
	service, err := NewUnderstandingService(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Understand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	policy := evidenceByKindForTest(t, result.EvidenceRefs, askdata.EvidenceKindPolicy)
	want := []DomainHypothesis{{
		DomainID: request.ContextRequest.Scope.DomainIDs[0], Score: 1,
		EvidenceRefs: []askdata.EvidenceRef{policy},
	}}
	if !reflect.DeepEqual(result.Current.DomainHypotheses, want) {
		t.Fatalf("domain hypotheses = %#v, want policy-pinned %#v", result.Current.DomainHypotheses, want)
	}
	var policyFact struct {
		DomainID askdata.ID `json:"domainId"`
	}
	input, err := BuildUnderstandingReviewInput(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(input.Facts[3].Payload, &policyFact); err != nil || policyFact.DomainID != want[0].DomainID {
		t.Fatalf("policy fact = %#v, %v", policyFact, err)
	}
	if strings.Contains(string(input.Facts[3].Payload), "domainIds") {
		t.Fatalf("policy fact exposed a domain-routing collection: %s", input.Facts[3].Payload)
	}
}

func TestUnderstandingServiceKeepsInheritedMentionsOnTheirSourceQuestion(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	contextRequest := contextRequestForTest(t, scope, "那按地区呢", ContextAuto, &snapshot)
	request := UnderstandingRequest{
		ContextRequest: contextRequest,
		Context:        mergeContextForTest(t, contextRequest),
		ExactMatches: []ExactMatch{
			exactMatchForTest(contextRequest.Question.Original, "地区", search.ObjectDimension, "地区", "dimension-region-v1"),
		},
		SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	reviewer := understandingReviewerFunc(func(_ context.Context, input UnderstandingReviewInput) (UnderstandingProposal, error) {
		policy := evidenceByKindForTest(t, input.AllowedEvidenceRefs, askdata.EvidenceKindPolicy)
		currentRef := evidenceByKindForTest(t, input.AllowedEvidenceRefs, askdata.EvidenceKindConversation)
		regionSpan := mustContextSpan(contextRequest.Question.Original, "地区")
		current := emptyUnderstandingForTest(contextRequest.Question.Original)
		current.DomainHypotheses = []DomainHypothesis{{DomainID: "sales", Score: 0.9, EvidenceRefs: []askdata.EvidenceRef{policy}}}
		current.DimensionMentions = []DimensionMention{{Text: "地区", Span: regionSpan, Role: DimensionRoleGroupBy}}
		inherited := request.Context.Inherited
		if inherited == nil {
			t.Fatal("expected inherited context")
		}
		requests := []UnderstandingEvidenceRequest{
			evidenceRequestForTest(EvidenceOriginCurrent, NeededDimensionCandidates, "地区", regionSpan, currentRef),
			evidenceRequestForTest(EvidenceOriginInherited, NeededMetricCandidates, inherited.MetricMentions[0].Text, inherited.MetricMentions[0].Span, currentRef),
			evidenceRequestForTest(EvidenceOriginInherited, NeededMemberCandidates, inherited.ValueMentions[0].Text, inherited.ValueMentions[0].Span, currentRef),
		}
		return UnderstandingProposal{
			SchemaVersion: UnderstandingReviewSchemaVersion, Understanding: current,
			Conflicts: []UnderstandingConflict{}, EvidenceRequests: requests,
			EvidenceRefs: input.AllowedEvidenceRefs,
		}, nil
	})
	service, err := NewUnderstandingService(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Understand(context.Background(), request)
	if err != nil {
		t.Fatalf("understand follow-up: %v", err)
	}
	if len(result.Current.MetricMentions) != 0 || result.Context.Inherited == nil || len(result.Context.Inherited.MetricMentions) != 1 {
		t.Fatalf("current and inherited mentions were conflated: %+v", result)
	}
	for _, evidenceRequest := range result.EvidenceRequests {
		question := result.Current.Question
		if evidenceRequest.Origin == EvidenceOriginInherited {
			question = result.Context.Inherited.Question
		}
		if err := validateMention(question, evidenceRequest.Text, evidenceRequest.Span); err != nil {
			t.Fatalf("request lost its source span: %+v: %v", evidenceRequest, err)
		}
	}
}

func TestUnderstandingServicePreservesRuleConflictsAndPlansEvidence(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	contextRequest := contextRequestForTest(t, scope, "销售额同比环比", ContextIndependent, nil)
	request := UnderstandingRequest{
		ContextRequest: contextRequest,
		Context:        mergeContextForTest(t, contextRequest),
		ExactMatches: []ExactMatch{
			exactMatchForTest(contextRequest.Question.Original, "销售额", search.ObjectMetric, "销售额", "metric-sales-v3"),
		},
		SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	if len(contextRequest.Rules.UnresolvedSpans) != 1 {
		t.Fatalf("fixture did not produce a grammar conflict: %+v", contextRequest.Rules)
	}
	reviewer := understandingReviewerFunc(func(_ context.Context, input UnderstandingReviewInput) (UnderstandingProposal, error) {
		refs := input.AllowedEvidenceRefs
		policy := evidenceByKindForTest(t, refs, askdata.EvidenceKindPolicy)
		rule := evidenceByKindForTest(t, refs, askdata.EvidenceKindRule)
		current := emptyUnderstandingForTest(contextRequest.Question.Original)
		current.DomainHypotheses = []DomainHypothesis{{DomainID: "sales", Score: 0.8, EvidenceRefs: []askdata.EvidenceRef{policy, rule}}}
		current.MetricMentions = []MetricMention{{Text: "销售额", Span: mustContextSpan(current.Question, "销售额"), AggregationHint: AggregationDefault}}
		target := "销售额"
		current.Comparisons = append([]ComparisonMention(nil), contextRequest.Rules.Comparisons...)
		for index := range current.Comparisons {
			current.Comparisons[index].TargetText = &target
		}
		unresolved := contextRequest.Rules.UnresolvedSpans[0]
		current.UnresolvedSpans = []UnresolvedSpan{unresolved}
		return UnderstandingProposal{
			SchemaVersion: UnderstandingReviewSchemaVersion,
			Understanding: current,
			Conflicts: []UnderstandingConflict{{
				Code: unresolved.Reason, Text: unresolved.Text, Span: unresolved.Span,
				Summary: "同比与环比同时出现，需要确认比较口径。", EvidenceRefs: []askdata.EvidenceRef{rule},
			}},
			EvidenceRequests: []UnderstandingEvidenceRequest{
				evidenceRequestForTest(EvidenceOriginCurrent, NeededMetricCandidates, "销售额", mustContextSpan(current.Question, "销售额"), rule),
				evidenceRequestForTest(EvidenceOriginCurrent, NeededConversationContext, unresolved.Text, unresolved.Span, rule),
			},
			EvidenceRefs: refs,
		}, nil
	})
	service, err := NewUnderstandingService(reviewer)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Understand(context.Background(), request)
	if err != nil {
		t.Fatalf("understand conflict: %v", err)
	}
	if len(result.Conflicts) != 1 || result.Conflicts[0].Code != ReasonMultipleComparisons || len(result.EvidenceRequests) != 2 {
		t.Fatalf("conflict/evidence plan was lost: %+v", result)
	}
}

func TestUnderstandingServiceFailsClosedOnModelAuthorityViolations(t *testing.T) {
	t.Parallel()
	request := completeUnderstandingRequestForTest(t)
	tests := []struct {
		name   string
		mutate func(*UnderstandingProposal)
		want   string
	}{
		{
			name: "rule time override",
			mutate: func(proposal *UnderstandingProposal) {
				proposal.Understanding.Time.Grain = TimeGrainMonth
			},
			want: "deterministic time",
		},
		{
			name: "unauthorized domain",
			mutate: func(proposal *UnderstandingProposal) {
				proposal.Understanding.DomainHypotheses[0].DomainID = "finance"
			},
			want: "outside the selected domain",
		},
		{
			name: "invented evidence",
			mutate: func(proposal *UnderstandingProposal) {
				proposal.EvidenceRefs = append(proposal.EvidenceRefs, askdata.EvidenceRef{
					EvidenceID: "invented-evidence", Kind: askdata.EvidenceKindRule,
					SourceID: "invented-source", ContentHash: askdata.HashBytes([]byte("invented")),
				})
			},
			want: "invented or stale evidence",
		},
		{
			name: "missing retrieval plan",
			mutate: func(proposal *UnderstandingProposal) {
				proposal.EvidenceRequests = proposal.EvidenceRequests[1:]
			},
			want: "metric mention has no candidate request",
		},
		{
			name: "physical query in authored text",
			mutate: func(proposal *UnderstandingProposal) {
				proposal.EvidenceRequests[0].Reason = "select amount from dws_orders"
			},
			want: "evidenceRequests",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reviewer := understandingReviewerFunc(func(_ context.Context, input UnderstandingReviewInput) (UnderstandingProposal, error) {
				proposal := completeProposalForTest(t, request, input)
				test.mutate(&proposal)
				return proposal, nil
			})
			service, err := NewUnderstandingService(reviewer)
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Understand(context.Background(), request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDecodeUnderstandingProposalRejectsUnknownPhysicalField(t *testing.T) {
	base := `{"schemaVersion":"question-understanding-review-v1","understanding":{"schemaVersion":"1.0","question":"销售额","domainHypotheses":[],"metricMentions":[],"dimensionMentions":[],"valueMentions":[],"time":null,"comparisons":[],"ordering":[],"limit":null,"unresolvedSpans":[]},"conflicts":[],"evidenceRequests":[],"evidenceRefs":[]}`
	unsafe := strings.Replace(base, `"understanding":{`, `"sql":"select * from orders","understanding":{`, 1)
	if _, err := DecodeUnderstandingProposal([]byte(unsafe)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decode error = %v, want unknown field rejection", err)
	}
}

func TestUnderstandingResultRejectsTampering(t *testing.T) {
	t.Parallel()
	request := completeUnderstandingRequestForTest(t)
	service, err := NewUnderstandingService(understandingReviewerFunc(func(_ context.Context, input UnderstandingReviewInput) (UnderstandingProposal, error) {
		return completeProposalForTest(t, request, input), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Understand(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	tampered := result
	tampered.Current.MetricMentions = append([]MetricMention(nil), result.Current.MetricMentions...)
	tampered.Current.MetricMentions[0].AggregationHint = AggregationAverage
	if err := tampered.Validate(request); err == nil {
		t.Fatal("expected result tampering rejection")
	}
}

func completeUnderstandingRequestForTest(t *testing.T) UnderstandingRequest {
	t.Helper()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	contextRequest := contextRequestForTest(t, scope, "今年华东销售额按地区同比top 10降序", ContextIndependent, nil)
	question := contextRequest.Question.Original
	return UnderstandingRequest{
		ContextRequest: contextRequest,
		Context:        mergeContextForTest(t, contextRequest),
		ExactMatches: []ExactMatch{
			exactMatchForTest(question, "销售额", search.ObjectMetric, "销售额", "metric-sales-v3"),
			exactMatchForTest(question, "地区", search.ObjectDimension, "地区", "dimension-region-v1"),
		},
		SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
}

func completeProposalForTest(t *testing.T, request UnderstandingRequest, input UnderstandingReviewInput) UnderstandingProposal {
	t.Helper()
	question := request.ContextRequest.Question.Original
	policy := evidenceByKindForTest(t, input.AllowedEvidenceRefs, askdata.EvidenceKindPolicy)
	rule := evidenceByKindForTest(t, input.AllowedEvidenceRefs, askdata.EvidenceKindRule)
	exact := evidenceByKindForTest(t, input.AllowedEvidenceRefs, askdata.EvidenceKindExactAlias)
	dimensionHint := "地区"
	target := "销售额"
	current := emptyUnderstandingForTest(question)
	current.DomainHypotheses = []DomainHypothesis{{DomainID: "sales", Score: 0.96, EvidenceRefs: []askdata.EvidenceRef{policy, exact}}}
	current.MetricMentions = []MetricMention{{Text: "销售额", Span: mustContextSpan(question, "销售额"), AggregationHint: AggregationDefault}}
	current.DimensionMentions = []DimensionMention{{Text: "地区", Span: mustContextSpan(question, "地区"), Role: DimensionRoleGroupBy}}
	current.ValueMentions = []ValueMention{{Text: "华东", Span: mustContextSpan(question, "华东"), DimensionHint: &dimensionHint, OperatorHint: ValueOperatorDefault}}
	timeUnderstanding := request.ContextRequest.Rules.Time.Understanding()
	current.Time = &timeUnderstanding
	current.Comparisons = append([]ComparisonMention(nil), request.ContextRequest.Rules.Comparisons...)
	for index := range current.Comparisons {
		current.Comparisons[index].TargetText = &target
	}
	current.Limit = new(int)
	*current.Limit = request.ContextRequest.Rules.Ranking.Limit
	current.Ordering = []OrderingMention{
		{Text: request.ContextRequest.Rules.Ranking.Text, Span: request.ContextRequest.Rules.Ranking.Span, TargetText: target, Direction: request.ContextRequest.Rules.Ranking.Direction},
		{Text: request.ContextRequest.Rules.Sorts[0].Text, Span: request.ContextRequest.Rules.Sorts[0].Span, TargetText: target, Direction: request.ContextRequest.Rules.Sorts[0].Direction},
	}
	return UnderstandingProposal{
		SchemaVersion: UnderstandingReviewSchemaVersion,
		Understanding: current,
		Conflicts:     []UnderstandingConflict{},
		EvidenceRequests: []UnderstandingEvidenceRequest{
			evidenceRequestForTest(EvidenceOriginCurrent, NeededMetricCandidates, "销售额", mustContextSpan(question, "销售额"), rule),
			evidenceRequestForTest(EvidenceOriginCurrent, NeededDimensionCandidates, "地区", mustContextSpan(question, "地区"), rule),
			evidenceRequestForTest(EvidenceOriginCurrent, NeededMemberCandidates, "华东", mustContextSpan(question, "华东"), rule),
		},
		EvidenceRefs: input.AllowedEvidenceRefs,
	}
}

func emptyUnderstandingForTest(question string) QuestionUnderstanding {
	return QuestionUnderstanding{
		SchemaVersion: SchemaVersion, Question: question,
		DomainHypotheses: []DomainHypothesis{}, MetricMentions: []MetricMention{},
		DimensionMentions: []DimensionMention{}, ValueMentions: []ValueMention{},
		Comparisons: []ComparisonMention{}, Ordering: []OrderingMention{},
		UnresolvedSpans: []UnresolvedSpan{},
	}
}

func exactMatchForTest(question, text string, objectType search.ObjectType, label string, sourceID askdata.ID) ExactMatch {
	return ExactMatch{
		ObjectType: objectType, CanonicalLabel: label, Text: text, Span: mustContextSpan(question, text),
		Evidence: askdata.EvidenceRef{
			EvidenceID: askdata.ID("exact:" + string(sourceID)), Kind: askdata.EvidenceKindExactAlias,
			SourceID: sourceID, ContentHash: askdata.HashBytes([]byte("exact:" + string(sourceID))),
		},
	}
}

func evidenceRequestForTest(origin EvidenceOrigin, needed NeededEvidence, text string, span Span, ref askdata.EvidenceRef) UnderstandingEvidenceRequest {
	return UnderstandingEvidenceRequest{
		Origin: origin, NeededEvidence: needed, Text: text, Span: span,
		Reason: "需要受权限与发布版本约束的候选证据。", EvidenceRefs: []askdata.EvidenceRef{ref},
	}
}

func evidenceByKindForTest(t *testing.T, values []askdata.EvidenceRef, kind askdata.EvidenceKind) askdata.EvidenceRef {
	t.Helper()
	for _, value := range values {
		if value.Kind == kind {
			return value
		}
	}
	t.Fatalf("evidence kind %s not found in %+v", kind, values)
	return askdata.EvidenceRef{}
}

func TestBuildUnderstandingReviewInputIsDeterministic(t *testing.T) {
	t.Parallel()
	request := completeUnderstandingRequestForTest(t)
	forward, err := BuildUnderstandingReviewInput(request)
	if err != nil {
		t.Fatal(err)
	}
	reversed := request
	reversed.ExactMatches = append([]ExactMatch(nil), request.ExactMatches...)
	for left, right := 0, len(reversed.ExactMatches)-1; left < right; left, right = left+1, right-1 {
		reversed.ExactMatches[left], reversed.ExactMatches[right] = reversed.ExactMatches[right], reversed.ExactMatches[left]
	}
	backward, err := BuildUnderstandingReviewInput(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(forward, backward) {
		t.Fatalf("prompt facts depend on exact-match input order:\nforward=%+v\nbackward=%+v", forward, backward)
	}
}

func TestBuildUnderstandingReviewInputExposesOnlyUnconsumedResidualText(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	contextRequest := contextRequestForTest(t, scope, "今年神秘口径销售额", ContextIndependent, nil)
	request := UnderstandingRequest{
		ContextRequest: contextRequest,
		Context:        mergeContextForTest(t, contextRequest),
		ExactMatches: []ExactMatch{
			exactMatchForTest(contextRequest.Question.Original, "销售额", search.ObjectMetric, "销售额", "metric-sales-v3"),
		},
		SensitiveMemberMatches: []security.ExactMemberMatch{},
	}
	input, err := BuildUnderstandingReviewInput(request)
	if err != nil {
		t.Fatal(err)
	}
	var exactFact struct {
		Residual []ResidualSpan `json:"residualSpans"`
	}
	if err := json.Unmarshal(input.Facts[1].Payload, &exactFact); err != nil {
		t.Fatal(err)
	}
	want := []ResidualSpan{{Text: "神秘口径", Span: mustContextSpan(contextRequest.Question.Original, "神秘口径")}}
	if !reflect.DeepEqual(exactFact.Residual, want) {
		t.Fatalf("residual = %+v, want %+v", exactFact.Residual, want)
	}
}

func TestMarshalSanitizedFactRedactsNestedCurrentAndInheritedText(t *testing.T) {
	t.Parallel()
	current := sensitiveBindingForTest(
		EvidenceOriginCurrent, "客户XA42Y销售额", "A42",
		"a42", "Ａ４２", "ａ４２",
	)
	inherited := sensitiveBindingForTest(
		EvidenceOriginInherited, "SecretX利润", "SecretX",
		"secretx", "ＳｅｃｒｅｔＸ", "ｓｅｃｒｅｔｘ",
	)
	bindings := []sensitiveMemberBinding{current, inherited}
	payload := map[string]any{
		"question": "客户XA42Y销售额",
		"context": map[string]any{
			"inherited": map[string]any{
				"question": "SecretX利润",
				"summary":  "secretx仍是上一轮条件",
			},
		},
		"rules": []any{
			map[string]any{"reason": "需要核对Ａ４２"},
		},
		"exactMatches": []any{
			map[string]any{"canonicalLabel": "客户Xa42Y"},
		},
	}

	redacted, err := marshalSanitizedFact(payload, bindings)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(redacted)
	for _, forbidden := range []string{
		"A42", "a42", "Ａ４２", "ａ４２",
		"SecretX", "secretx", "ＳｅｃｒｅｔＸ", "ｓｅｃｒｅｔｘ",
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("nested prompt fact leaked %q: %s", forbidden, serialized)
		}
	}
	if !strings.Contains(serialized, "客户X███Y销售额") ||
		!strings.Contains(serialized, "███████利润") {
		t.Fatalf("prompt fact did not redact adjacent/current and inherited values: %s", serialized)
	}
	safe, err := sanitizePromptString("客户XA42Y销售额", bindings)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(safe) != utf8.RuneCountInString("客户XA42Y销售额") {
		t.Fatalf("redaction changed rune length: %q", safe)
	}
}

func TestSensitiveQuestionViewPreservesOffsetsAfterRedaction(t *testing.T) {
	t.Parallel()
	raw := "A42之后销售额"
	binding := sensitiveBindingForTest(
		EvidenceOriginCurrent, raw, "A42", "a42", "Ａ４２", "ａ４２",
	)
	request := UnderstandingRequest{
		ContextRequest: ContextMergeRequest{Question: NormalizedQuestion{Original: raw}},
	}
	views, err := sensitiveQuestionViews(request, []sensitiveMemberBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	view := views[EvidenceOriginCurrent]
	if view.safe != "███之后销售额" || !reflect.DeepEqual(view.spans, []Span{{Start: 0, End: 3}}) {
		t.Fatalf("unexpected safe view: %+v", view)
	}
	if mustContextSpan(raw, "销售额") != mustContextSpan(view.safe, "销售额") {
		t.Fatalf("span after sensitive fragment drifted: raw=%q safe=%q", raw, view.safe)
	}
}

func TestRestoreSensitiveReviewerProposalRequiresSafeQuestionAndRestoresOriginal(t *testing.T) {
	t.Parallel()
	request, bindings, proposal, _ := sensitiveReviewerFixture()
	wantMetricSpan := proposal.Understanding.MetricMentions[0].Span

	restored, err := restoreSensitiveReviewerProposalWithBindings(proposal, request, bindings)
	if err != nil {
		t.Fatalf("restore safe proposal: %v", err)
	}
	if restored.Understanding.Question != request.ContextRequest.Question.Original {
		t.Fatalf("question was not restored: %q", restored.Understanding.Question)
	}
	if restored.Understanding.MetricMentions[0].Span != wantMetricSpan {
		t.Fatalf("metric span drifted: %+v", restored.Understanding.MetricMentions[0].Span)
	}

	unsafe := proposal
	unsafe.Understanding.Question = request.ContextRequest.Question.Original
	if _, err := restoreSensitiveReviewerProposalWithBindings(unsafe, request, bindings); err == nil ||
		!strings.Contains(err.Error(), "redacted current question") {
		t.Fatalf("unsafe question error = %v", err)
	}
}

func TestRestoreSensitiveReviewerProposalRejectsSensitiveOutputText(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(*UnderstandingProposal, askdata.EvidenceRef)
		wantEcho bool
	}{
		{
			name: "summary raw fragment",
			mutate: func(proposal *UnderstandingProposal, ref askdata.EvidenceRef) {
				span := mustContextSpan(proposal.Understanding.Question, "销售额")
				proposal.Understanding.UnresolvedSpans = []UnresolvedSpan{{
					Text: "销售额", Span: span, Reason: ReasonTooManyMatches,
					NeededEvidence: []NeededEvidence{NeededConversationContext},
				}}
				proposal.Conflicts = []UnderstandingConflict{{
					Code: ReasonTooManyMatches, Text: "销售额", Span: span,
					Summary: "客户XA42Y需要确认", EvidenceRefs: []askdata.EvidenceRef{ref},
				}}
			},
			wantEcho: true,
		},
		{
			name: "summary compatibility variant",
			mutate: func(proposal *UnderstandingProposal, ref askdata.EvidenceRef) {
				span := mustContextSpan(proposal.Understanding.Question, "销售额")
				proposal.Understanding.UnresolvedSpans = []UnresolvedSpan{{
					Text: "销售额", Span: span, Reason: ReasonTooManyMatches,
					NeededEvidence: []NeededEvidence{NeededConversationContext},
				}}
				proposal.Conflicts = []UnderstandingConflict{{
					Code: ReasonTooManyMatches, Text: "销售额", Span: span,
					Summary: "需要确认Ａ４２", EvidenceRefs: []askdata.EvidenceRef{ref},
				}}
			},
			wantEcho: true,
		},
		{
			name: "evidence reason case folded variant",
			mutate: func(proposal *UnderstandingProposal, ref askdata.EvidenceRef) {
				span := mustContextSpan(proposal.Understanding.Question, "销售额")
				proposal.EvidenceRequests = []UnderstandingEvidenceRequest{{
					Origin: EvidenceOriginCurrent, NeededEvidence: NeededMetricCandidates,
					Text: "销售额", Span: span, Reason: "需要核对a42",
					EvidenceRefs: []askdata.EvidenceRef{ref},
				}}
			},
			wantEcho: true,
		},
		{
			name: "evidence request text compatibility variant",
			mutate: func(proposal *UnderstandingProposal, ref askdata.EvidenceRef) {
				span := mustContextSpan(proposal.Understanding.Question, "销售额")
				proposal.EvidenceRequests = []UnderstandingEvidenceRequest{{
					Origin: EvidenceOriginCurrent, NeededEvidence: NeededMemberCandidates,
					Text: "ａ４２", Span: span, Reason: "需要候选证据",
					EvidenceRefs: []askdata.EvidenceRef{ref},
				}}
			},
			wantEcho: true,
		},
		{
			name: "mention metadata compatibility variant",
			mutate: func(proposal *UnderstandingProposal, _ askdata.EvidenceRef) {
				hint := "Ａ４２"
				span := mustContextSpan(proposal.Understanding.Question, "销售额")
				proposal.Understanding.ValueMentions = []ValueMention{{
					Text: "销售额", Span: span, DimensionHint: &hint,
					OperatorHint: ValueOperatorDefault,
				}}
			},
			wantEcho: true,
		},
		{
			name: "mention text raw fragment",
			mutate: func(proposal *UnderstandingProposal, _ askdata.EvidenceRef) {
				proposal.Understanding.MetricMentions = []MetricMention{{
					Text: "A42", Span: Span{Start: 0, End: 3},
					AggregationHint: AggregationDefault,
				}}
			},
		},
		{
			name: "conflict text case folded variant",
			mutate: func(proposal *UnderstandingProposal, ref askdata.EvidenceRef) {
				proposal.Conflicts = []UnderstandingConflict{{
					Code: ReasonTooManyMatches, Text: "a42", Span: Span{Start: 0, End: 3},
					Summary: "需要确认冲突", EvidenceRefs: []askdata.EvidenceRef{ref},
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, bindings, proposal, ref := sensitiveReviewerFixture()
			test.mutate(&proposal, ref)
			_, err := restoreSensitiveReviewerProposalWithBindings(proposal, request, bindings)
			if err == nil {
				t.Fatal("expected sensitive reviewer output rejection")
			}
			if test.wantEcho && !strings.Contains(err.Error(), "echoed sensitive member text") {
				t.Fatalf("error = %v, want sensitive echo rejection", err)
			}
		})
	}
}

func TestUnderstandingRequestRequiresExplicitSensitiveMatchesAndRejectsCallerMemberLabels(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*UnderstandingRequest)
	}{
		{
			name: "nil sensitive matches",
			mutate: func(request *UnderstandingRequest) {
				request.SensitiveMemberMatches = nil
			},
		},
		{
			name: "caller supplied member exact match",
			mutate: func(request *UnderstandingRequest) {
				question := request.ContextRequest.Question.Original
				request.ExactMatches = append(request.ExactMatches, ExactMatch{
					ObjectType: search.ObjectMember, CanonicalLabel: "华东",
					Text: "华东", Span: mustContextSpan(question, "华东"),
					Evidence: askdata.EvidenceRef{
						EvidenceID: "caller-member-evidence", Kind: askdata.EvidenceKindExactAlias,
						SourceID: "caller-member-source", ContentHash: askdata.HashBytes([]byte("caller-member")),
					},
				})
			},
		},
		{
			name: "caller labels an existing hit as member",
			mutate: func(request *UnderstandingRequest) {
				request.ExactMatches[0].ObjectType = search.ObjectMember
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := completeUnderstandingRequestForTest(t)
			test.mutate(&request)
			if _, err := BuildUnderstandingReviewInput(request); err == nil {
				t.Fatal("expected invalid understanding request")
			}
		})
	}
}

func TestSensitiveMemberCannotOverlapAuthoritativeCurrentSpans(t *testing.T) {
	t.Parallel()
	base := completeUnderstandingRequestForTest(t)
	memberSpan := mustContextSpan(base.ContextRequest.Question.Original, "华东")
	tests := []struct {
		name    string
		request UnderstandingRequest
		span    Span
	}{
		{"time", base, base.ContextRequest.Rules.Time.Span},
		{"comparison", base, base.ContextRequest.Rules.Comparisons[0].Span},
		{"ranking", base, base.ContextRequest.Rules.Ranking.Span},
		{"sort", base, base.ContextRequest.Rules.Sorts[0].Span},
		{"grouping", base, base.ContextRequest.Rules.Groupings[0].Span},
	}
	ruleUnresolved := base
	ruleUnresolved.ContextRequest.Rules.UnresolvedSpans = []UnresolvedSpan{{
		Text: "华东", Span: memberSpan, Reason: ReasonTooManyMatches,
		NeededEvidence: []NeededEvidence{NeededConversationContext},
	}}
	tests = append(tests, struct {
		name    string
		request UnderstandingRequest
		span    Span
	}{"rule unresolved", ruleUnresolved, memberSpan})
	contextUnresolved := base
	contextUnresolved.Context.UnresolvedSpans = []UnresolvedSpan{{
		Text: "华东", Span: memberSpan, Reason: ReasonTooManyMatches,
		NeededEvidence: []NeededEvidence{NeededConversationContext},
	}}
	tests = append(tests, struct {
		name    string
		request UnderstandingRequest
		span    Span
	}{"context unresolved", contextUnresolved, memberSpan})
	contextDecision := base
	contextDecision.Context.Decisions = append(
		append([]ContextDecision(nil), base.Context.Decisions...),
		ContextDecision{TriggerSpan: &memberSpan},
	)
	tests = append(tests, struct {
		name    string
		request UnderstandingRequest
		span    Span
	}{"context decision", contextDecision, memberSpan})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			bindings := []sensitiveMemberBinding{{
				Origin: EvidenceOriginCurrent, Span: test.span,
			}}
			if err := validateSensitiveAuthoritativeSpanConflicts(test.request, bindings); err == nil ||
				!strings.Contains(err.Error(), "overlaps authoritative") {
				t.Fatalf("error = %v, want authoritative overlap rejection", err)
			}
		})
	}
}

func sensitiveBindingForTest(
	origin EvidenceOrigin,
	question string,
	fragment string,
	variants ...string,
) sensitiveMemberBinding {
	all := append([]string{fragment}, variants...)
	return sensitiveMemberBinding{
		Origin: origin, Question: question, Span: mustContextSpan(question, fragment),
		redact: func(value string) (string, error) {
			result := value
			for _, variant := range all {
				result = strings.ReplaceAll(
					result, variant,
					strings.Repeat("█", utf8.RuneCountInString(variant)),
				)
			}
			return result, nil
		},
	}
}

func sensitiveReviewerFixture() (
	UnderstandingRequest,
	[]sensitiveMemberBinding,
	UnderstandingProposal,
	askdata.EvidenceRef,
) {
	raw, safe := "A42销售额", "███销售额"
	request := UnderstandingRequest{
		ContextRequest: ContextMergeRequest{Question: NormalizedQuestion{Original: raw}},
	}
	bindings := []sensitiveMemberBinding{sensitiveBindingForTest(
		EvidenceOriginCurrent, raw, "A42", "a42", "Ａ４２", "ａ４２",
	)}
	ref := askdata.EvidenceRef{
		EvidenceID: "reviewer-sensitive-evidence", Kind: askdata.EvidenceKindConversation,
		SourceID: "reviewer-sensitive-source", ContentHash: askdata.HashBytes([]byte("reviewer-sensitive")),
	}
	current := emptyUnderstandingForTest(safe)
	current.MetricMentions = []MetricMention{{
		Text: "销售额", Span: mustContextSpan(safe, "销售额"),
		AggregationHint: AggregationDefault,
	}}
	proposal := UnderstandingProposal{
		SchemaVersion: UnderstandingReviewSchemaVersion,
		Understanding: current, Conflicts: []UnderstandingConflict{},
		EvidenceRequests: []UnderstandingEvidenceRequest{},
		EvidenceRefs:     []askdata.EvidenceRef{ref},
	}
	return request, bindings, proposal, ref
}
