package understanding

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

func TestMergeContextReplacesGroupingAndInheritsOtherConfirmedSlots(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	request := contextRequestForTest(t, scope, "那按地区呢", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)

	if result.Mode != ContextModeFollowUp || result.Precedence != ContextCurrentTurnWins {
		t.Fatalf("unexpected merge mode: %+v", result)
	}
	if result.PreviousSnapshotHash == nil || *result.PreviousSnapshotHash != snapshot.SnapshotHash {
		t.Fatalf("previous snapshot was not pinned: %+v", result.PreviousSnapshotHash)
	}
	if result.Inherited == nil {
		t.Fatal("expected inherited context")
	}
	if len(result.Inherited.MetricMentions) != 1 || len(result.Inherited.ValueMentions) != 1 ||
		result.Inherited.Time == nil || len(result.Inherited.Comparisons) != 1 ||
		len(result.Inherited.Ordering) != 1 || result.Inherited.Limit == nil {
		t.Fatalf("expected non-grouping slots to be inherited: %+v", result.Inherited)
	}
	if len(result.Inherited.DimensionMentions) != 0 {
		t.Fatalf("prior grouping must be replaced: %+v", result.Inherited.DimensionMentions)
	}
	grouping := contextDecisionForTest(t, result, ContextSlotGrouping)
	if grouping.Action != ContextActionOverride || grouping.Reason != ContextReasonCurrentGrouping || grouping.TriggerText != "按" {
		t.Fatalf("unexpected grouping decision: %+v", grouping)
	}
	assertExactOriginalSpan(t, request.Question.Original, grouping.TriggerText, *grouping.TriggerSpan)
	metric := contextDecisionForTest(t, result, ContextSlotMetric)
	if metric.Action != ContextActionInherit {
		t.Fatalf("metric decision = %+v", metric)
	}
}

func TestMergeContextReplacesOnlyTimeForSwitchPhrase(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	request := contextRequestForTest(t, scope, "换成去年", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)

	if result.Inherited == nil || result.Inherited.Time != nil {
		t.Fatalf("prior time must not be inherited: %+v", result.Inherited)
	}
	if len(result.Inherited.MetricMentions) != 1 || len(result.Inherited.DimensionMentions) != 1 {
		t.Fatalf("metric and grouping should remain inherited: %+v", result.Inherited)
	}
	decision := contextDecisionForTest(t, result, ContextSlotTime)
	if decision.Action != ContextActionOverride || decision.Reason != ContextReasonCurrentTime || decision.TriggerText != "去年" {
		t.Fatalf("unexpected time decision: %+v", decision)
	}
}

func TestMergeContextExplicitClearRulesWinOverRuleMatches(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	tests := []struct {
		question string
		slots    []ContextSlot
	}{
		{"取消分组", []ContextSlot{ContextSlotGrouping}},
		{"不要同比", []ContextSlot{ContextSlotComparison}},
		{"取消排名", []ContextSlot{ContextSlotOrdering, ContextSlotLimit}},
		{"不要前10", []ContextSlot{ContextSlotOrdering, ContextSlotLimit}},
		{"不限时间", []ContextSlot{ContextSlotTime}},
		{"清除筛选", []ContextSlot{ContextSlotFilter}},
	}
	for _, test := range tests {
		result := mergeContextForTest(t, contextRequestForTest(t, scope, test.question, ContextAuto, &snapshot))
		if result.Mode != ContextModeFollowUp {
			t.Fatalf("%q: mode = %s", test.question, result.Mode)
		}
		for _, slot := range test.slots {
			decision := contextDecisionForTest(t, result, slot)
			if decision.Action != ContextActionClear || decision.Reason != ContextReasonExplicitClear {
				t.Fatalf("%q %s: decision = %+v", test.question, slot, decision)
			}
		}
	}
}

func TestMergeContextClearAllDoesNotReadPreviousSnapshot(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	snapshot.Understanding.Question = "tampered but intentionally discarded"
	request := contextRequestForTest(t, scope, "清空上下文", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)
	if result.Mode != ContextModeClearAll || result.Inherited != nil || result.PreviousSnapshotHash != nil {
		t.Fatalf("clear all retained context: %+v", result)
	}
	if decision := contextDecisionForTest(t, result, ContextSlotAll); decision.Action != ContextActionClear {
		t.Fatalf("clear decision = %+v", decision)
	}
}

func TestMergeContextTreatsCompleteQuestionAsIndependent(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	request := contextRequestForTest(t, scope, "今年利润按月", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)
	if result.Mode != ContextModeIndependent || result.Inherited != nil || result.PreviousSnapshotHash != nil || len(result.Decisions) != 0 {
		t.Fatalf("complete question inherited stale context: %+v", result)
	}
}

func TestIndependentQuestionDoesNotReadStalePreviousContent(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	snapshot.Understanding.Question = "tampered but irrelevant to an independent turn"
	request := contextRequestForTest(t, scope, "今年利润按月", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)
	if result.Mode != ContextModeIndependent || result.Inherited != nil {
		t.Fatalf("independent turn read stale context: %+v", result)
	}
}

func TestMergeContextContinuationOverrideIsExplicit(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)

	forced := mergeContextForTest(t, contextRequestForTest(t, scope, "地区", ContextFollowUp, &snapshot))
	if forced.Mode != ContextModeFollowUp || forced.Inherited == nil {
		t.Fatalf("forced follow-up was not inherited: %+v", forced)
	}
	independent := mergeContextForTest(t, contextRequestForTest(t, scope, "那按地区呢", ContextIndependent, &snapshot))
	if independent.Mode != ContextModeIndependent || independent.Inherited != nil {
		t.Fatalf("forced independent question inherited context: %+v", independent)
	}
}

func TestMergeContextNeverInheritsAcrossPolicyOrRelease(t *testing.T) {
	t.Parallel()
	baseScope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, baseScope)
	snapshot.Understanding.Question = "tampered and must never be read across scope"
	tests := []askdata.PolicyScope{
		contextScopeForTest(t, "actor-2", "release-v1", "analyst"),
		contextScopeForTest(t, "actor-1", "release-v2", "analyst"),
		contextScopeForTest(t, "actor-1", "release-v1", "viewer"),
	}
	for _, scope := range tests {
		request := contextRequestForTest(t, scope, "那按地区呢", ContextAuto, &snapshot)
		result := mergeContextForTest(t, request)
		if result.Mode != ContextModeScopeReset || result.Inherited != nil || result.PreviousSnapshotHash != nil {
			t.Fatalf("scope %s inherited prior content: %+v", scope.PolicyHash, result)
		}
		if decision := contextDecisionForTest(t, result, ContextSlotAll); decision.Reason != ContextReasonScopeChanged {
			t.Fatalf("scope reset decision = %+v", decision)
		}
	}
}

func TestMergeContextNeverInheritsAcrossConversation(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	request := contextRequestForTest(t, scope, "那按地区呢", ContextAuto, &snapshot)
	request.ConversationID = "conversation-other"
	result := mergeContextForTest(t, request)
	if result.Mode != ContextModeScopeReset || contextDecisionForTest(t, result, ContextSlotAll).Reason != ContextReasonConversationChanged {
		t.Fatalf("cross-conversation result = %+v", result)
	}
}

func TestMergeContextReturnsTargetedGapWithoutPreviousTurn(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	request := contextRequestForTest(t, scope, "那按地区呢", ContextAuto, nil)
	result := mergeContextForTest(t, request)
	if result.Mode != ContextModeMissing || result.Inherited != nil || len(result.UnresolvedSpans) != 1 {
		t.Fatalf("unexpected missing context result: %+v", result)
	}
	unresolved := result.UnresolvedSpans[0]
	if unresolved.Reason != ReasonPreviousContextRequired || unresolved.NeededEvidence[0] != NeededConversationContext {
		t.Fatalf("unexpected context gap: %+v", unresolved)
	}
	assertExactOriginalSpan(t, request.Question.Original, unresolved.Text, unresolved.Span)
}

func TestMergeContextDoesNotInheritTimeWhenCurrentTimeIsAmbiguous(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	request := contextRequestForTest(t, scope, "3月5日呢", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)
	if result.Mode != ContextModeFollowUp || result.Inherited == nil || result.Inherited.Time != nil {
		t.Fatalf("ambiguous current time inherited stale time: %+v", result)
	}
	if decision := contextDecisionForTest(t, result, ContextSlotTime); decision.Action != ContextActionOverride || decision.Reason != ContextReasonCurrentTime {
		t.Fatalf("ambiguous time decision = %+v", decision)
	}
}

func TestContextSnapshotAndMergeResultRejectTampering(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	tamperedSnapshot := snapshot
	tamperedSnapshot.Understanding.MetricMentions[0].Text = "利润"
	if err := tamperedSnapshot.Validate(); err == nil {
		t.Fatal("expected snapshot tampering rejection")
	}

	snapshot = contextSnapshotForTest(t, scope)
	request := contextRequestForTest(t, scope, "换成去年", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)
	result.Decisions[0].Action = ContextActionClear
	if err := result.Validate(request); err == nil {
		t.Fatal("expected result tampering rejection")
	}
}

func TestContextSnapshotRejectsUnresolvedUnderstanding(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	value := confirmedUnderstandingForTest()
	value.UnresolvedSpans = []UnresolvedSpan{{
		Text: "今年", Span: contextSpanForTest(t, value.Question, "今年"),
		Reason: "TEST_UNRESOLVED", NeededEvidence: []NeededEvidence{NeededTimeResolution},
	}}
	if _, err := NewContextSnapshot("conversation-1", "turn-1", scope, value); err == nil || !strings.Contains(err.Error(), "finalized") {
		t.Fatalf("NewContextSnapshot() error = %v", err)
	}
}

func TestContextSnapshotClonesCallerUnderstanding(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	value := confirmedUnderstandingForTest()
	snapshot, err := NewContextSnapshot("conversation-1", "turn-1", scope, value)
	if err != nil {
		t.Fatal(err)
	}
	value.MetricMentions[0].Text = "利润"
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot shares caller memory: %v", err)
	}
}

func TestContextResultProducesConversationEvidence(t *testing.T) {
	t.Parallel()
	scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
	snapshot := contextSnapshotForTest(t, scope)
	request := contextRequestForTest(t, scope, "换成去年", ContextAuto, &snapshot)
	result := mergeContextForTest(t, request)
	evidence, err := result.EvidenceRef("evidence-conversation-turn-2")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Kind != askdata.EvidenceKindConversation || evidence.SourceID != request.TurnID || evidence.ContentHash != result.ContentHash {
		t.Fatalf("evidence = %+v", evidence)
	}
	result.Mode = ContextModeIndependent
	if _, err := result.EvidenceRef("evidence-conversation-tampered"); err == nil {
		t.Fatal("expected evidence generation to reject a tampered result")
	}
}

func FuzzMergeContextReplayAndSpanSafety(f *testing.F) {
	for _, seed := range []string{
		"那按地区呢", "换成去年", "不要同比", "取消排名", "清空上下文", "今年利润按月",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		question, err := NormalizeQuestion(source)
		if err != nil {
			return
		}
		parser, err := NewRuleParser(referenceForTest(), time.April)
		if err != nil {
			t.Fatal(err)
		}
		rules, err := parser.Parse(question)
		if err != nil {
			return
		}
		scope := contextScopeForTest(t, "actor-1", "release-v1", "analyst")
		snapshot := contextSnapshotForTest(t, scope)
		request := ContextMergeRequest{
			ConversationID: "conversation-1", TurnID: "turn-2", Scope: scope,
			Question: question, Rules: rules, Continuation: ContextAuto, Previous: &snapshot,
		}
		result, err := MergeContext(request)
		if err != nil {
			return
		}
		if err := result.Validate(request); err != nil {
			t.Fatalf("replay validation: %v", err)
		}
		for _, decision := range result.Decisions {
			if decision.TriggerSpan != nil {
				assertExactOriginalSpan(t, source, decision.TriggerText, *decision.TriggerSpan)
			}
		}
		for _, unresolved := range result.UnresolvedSpans {
			assertExactOriginalSpan(t, source, unresolved.Text, unresolved.Span)
		}
	})
}

func contextScopeForTest(t *testing.T, actor, releaseID, role string) askdata.PolicyScope {
	t.Helper()
	release := askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.HashBytes([]byte(releaseID))}
	scope, err := askdata.NewPolicyScope("tenant-1", askdata.ID(actor), []askdata.ID{"sales"}, []askdata.ID{askdata.ID(role)}, release)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func contextSnapshotForTest(t *testing.T, scope askdata.PolicyScope) ContextSnapshot {
	t.Helper()
	snapshot, err := NewContextSnapshot("conversation-1", "turn-1", scope, confirmedUnderstandingForTest())
	if err != nil {
		t.Fatalf("new snapshot: %v", err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("validate snapshot: %v", err)
	}
	return snapshot
}

func contextRequestForTest(
	t *testing.T, scope askdata.PolicyScope, source string,
	continuation ContextContinuation, previous *ContextSnapshot,
) ContextMergeRequest {
	t.Helper()
	question, err := NormalizeQuestion(source)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	parser, err := NewRuleParser(referenceForTest(), time.April)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := parser.Parse(question)
	if err != nil {
		t.Fatalf("parse rules: %v", err)
	}
	return ContextMergeRequest{
		ConversationID: "conversation-1", TurnID: "turn-2", Scope: scope,
		Question: question, Rules: rules, Continuation: continuation, Previous: previous,
	}
}

func mergeContextForTest(t *testing.T, request ContextMergeRequest) ContextMergeResult {
	t.Helper()
	result, err := MergeContext(request)
	if err != nil {
		t.Fatalf("merge context: %v", err)
	}
	if err := result.Validate(request); err != nil {
		t.Fatalf("validate context result: %v", err)
	}
	return result
}

func confirmedUnderstandingForTest() QuestionUnderstanding {
	question := "今年华东区销售额按月同比前10降序"
	dimensionHint := "地区"
	target := "销售额"
	month := TimeGrainMonth
	limit := 10
	return QuestionUnderstanding{
		SchemaVersion: SchemaVersion, Question: question,
		DomainHypotheses: []DomainHypothesis{},
		MetricMentions: []MetricMention{{
			Text: "销售额", Span: mustContextSpan(question, "销售额"), AggregationHint: AggregationDefault,
		}},
		DimensionMentions: []DimensionMention{{
			Text: "月", Span: mustContextSpan(question, "月"), Role: DimensionRoleGroupBy, Grain: &month,
		}},
		ValueMentions: []ValueMention{{
			Text: "华东区", Span: mustContextSpan(question, "华东区"), DimensionHint: &dimensionHint, OperatorHint: ValueOperatorDefault,
		}},
		Time: &TimeUnderstanding{
			Text: "今年", Span: mustContextSpan(question, "今年"), Grain: TimeGrainMonth, Timezone: QuestionTimezone,
		},
		Comparisons: []ComparisonMention{{
			Text: "同比", Span: mustContextSpan(question, "同比"), Type: ComparisonYearOverYear, TargetText: &target,
		}},
		Ordering: []OrderingMention{{
			Text: "降序", Span: mustContextSpan(question, "降序"), TargetText: "销售额", Direction: SortDescending,
		}},
		Limit: &limit, UnresolvedSpans: []UnresolvedSpan{},
	}
}

func contextDecisionForTest(t *testing.T, result ContextMergeResult, slot ContextSlot) ContextDecision {
	t.Helper()
	for _, decision := range result.Decisions {
		if decision.Slot == slot {
			return decision
		}
	}
	t.Fatalf("missing decision for slot %s in %+v", slot, result.Decisions)
	return ContextDecision{}
}

func contextSpanForTest(t *testing.T, question, text string) Span {
	t.Helper()
	span := mustContextSpan(question, text)
	assertExactOriginalSpan(t, question, text, span)
	return span
}

func mustContextSpan(question, text string) Span {
	start := strings.Index(question, text)
	if start < 0 {
		panic("context test text not found")
	}
	return Span{
		Start: utf8.RuneCountInString(question[:start]),
		End:   utf8.RuneCountInString(question[:start+len(text)]),
	}
}
