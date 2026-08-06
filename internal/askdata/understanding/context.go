package understanding

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	ContextSnapshotVersion = "conversation-snapshot-v1"
	ContextMergeVersion    = "conversation-merge-v1"
	MaxContextDecisions    = 16
)

const (
	ReasonPreviousContextRequired = "CONTEXT_PREVIOUS_TURN_REQUIRED"
)

type ContextContinuation string

const (
	ContextAuto        ContextContinuation = "AUTO"
	ContextFollowUp    ContextContinuation = "FOLLOW_UP"
	ContextIndependent ContextContinuation = "INDEPENDENT"
)

type ContextMergeMode string

const (
	ContextModeIndependent ContextMergeMode = "INDEPENDENT"
	ContextModeFollowUp    ContextMergeMode = "FOLLOW_UP"
	ContextModeClearAll    ContextMergeMode = "CLEAR_ALL"
	ContextModeScopeReset  ContextMergeMode = "SCOPE_RESET"
	ContextModeMissing     ContextMergeMode = "MISSING_CONTEXT"
)

type ContextPrecedence string

const ContextCurrentTurnWins ContextPrecedence = "CURRENT_TURN_WINS"

type ContextSlot string

const (
	ContextSlotAll        ContextSlot = "ALL"
	ContextSlotDomain     ContextSlot = "DOMAIN"
	ContextSlotMetric     ContextSlot = "METRIC"
	ContextSlotGrouping   ContextSlot = "GROUPING"
	ContextSlotFilter     ContextSlot = "FILTER"
	ContextSlotTime       ContextSlot = "TIME"
	ContextSlotComparison ContextSlot = "COMPARISON"
	ContextSlotOrdering   ContextSlot = "ORDERING"
	ContextSlotLimit      ContextSlot = "LIMIT"
)

var orderedContextSlots = []ContextSlot{
	ContextSlotDomain, ContextSlotMetric, ContextSlotGrouping, ContextSlotFilter,
	ContextSlotTime, ContextSlotComparison, ContextSlotOrdering, ContextSlotLimit,
}

type ContextAction string

const (
	ContextActionInherit  ContextAction = "INHERIT"
	ContextActionOverride ContextAction = "OVERRIDE"
	ContextActionClear    ContextAction = "CLEAR"
	ContextActionReset    ContextAction = "RESET"
)

type ContextReason string

const (
	ContextReasonFollowUp            ContextReason = "FOLLOW_UP_INHERIT"
	ContextReasonCurrentTime         ContextReason = "CURRENT_TIME"
	ContextReasonCurrentGrouping     ContextReason = "CURRENT_GROUPING"
	ContextReasonCurrentComparison   ContextReason = "CURRENT_COMPARISON"
	ContextReasonCurrentRanking      ContextReason = "CURRENT_RANKING"
	ContextReasonCurrentSort         ContextReason = "CURRENT_SORT"
	ContextReasonExplicitClear       ContextReason = "EXPLICIT_CLEAR"
	ContextReasonScopeChanged        ContextReason = "POLICY_OR_RELEASE_CHANGED"
	ContextReasonConversationChanged ContextReason = "CONVERSATION_CHANGED"
)

// ContextDecision describes slot precedence without relocating a mention from
// its source question. Trigger spans, when present, always address the current
// original question.
type ContextDecision struct {
	Slot        ContextSlot   `json:"slot"`
	Action      ContextAction `json:"action"`
	Reason      ContextReason `json:"reason"`
	TriggerText string        `json:"triggerText,omitempty"`
	TriggerSpan *Span         `json:"triggerSpan,omitempty"`
}

// ContextSnapshot stores one finalized understanding. Unresolved turns cannot
// become inheritable context.
type ContextSnapshot struct {
	Version        string                `json:"version"`
	ConversationID askdata.ID            `json:"conversationId"`
	TurnID         askdata.ID            `json:"turnId"`
	Scope          askdata.PolicyScope   `json:"scope"`
	Understanding  QuestionUnderstanding `json:"understanding"`
	SnapshotHash   askdata.ContentHash   `json:"snapshotHash"`
}

func NewContextSnapshot(
	conversationID, turnID askdata.ID,
	scope askdata.PolicyScope,
	understanding QuestionUnderstanding,
) (ContextSnapshot, error) {
	if err := conversationID.Validate(); err != nil {
		return ContextSnapshot{}, fmt.Errorf("conversationId: %w", err)
	}
	if err := turnID.Validate(); err != nil {
		return ContextSnapshot{}, fmt.Errorf("turnId: %w", err)
	}
	if err := scope.Validate(); err != nil {
		return ContextSnapshot{}, fmt.Errorf("scope: %w", err)
	}
	if err := understanding.Validate(); err != nil {
		return ContextSnapshot{}, fmt.Errorf("understanding: %w", err)
	}
	if len(understanding.UnresolvedSpans) != 0 {
		return ContextSnapshot{}, errors.New("only a finalized understanding without unresolved spans can be inherited")
	}
	cloned, err := cloneUnderstanding(understanding)
	if err != nil {
		return ContextSnapshot{}, err
	}
	snapshot := ContextSnapshot{
		Version: ContextSnapshotVersion, ConversationID: conversationID,
		TurnID: turnID, Scope: scope, Understanding: cloned,
	}
	snapshot.SnapshotHash, err = contextSnapshotHash(snapshot)
	if err != nil {
		return ContextSnapshot{}, err
	}
	return snapshot, nil
}

func (snapshot ContextSnapshot) Validate() error {
	if err := snapshot.validateHeader(); err != nil {
		return err
	}
	if err := snapshot.Understanding.Validate(); err != nil {
		return fmt.Errorf("understanding: %w", err)
	}
	if len(snapshot.Understanding.UnresolvedSpans) != 0 {
		return errors.New("snapshot understanding must not contain unresolved spans")
	}
	expected, err := contextSnapshotHash(snapshot)
	if err != nil {
		return err
	}
	if expected != snapshot.SnapshotHash {
		return errors.New("snapshotHash does not match the context snapshot")
	}
	return nil
}

func (snapshot ContextSnapshot) validateHeader() error {
	if snapshot.Version != ContextSnapshotVersion {
		return fmt.Errorf("snapshot version must be %q", ContextSnapshotVersion)
	}
	if err := snapshot.ConversationID.Validate(); err != nil {
		return fmt.Errorf("conversationId: %w", err)
	}
	if err := snapshot.TurnID.Validate(); err != nil {
		return fmt.Errorf("turnId: %w", err)
	}
	if err := snapshot.Scope.Validate(); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	if err := snapshot.SnapshotHash.Validate(); err != nil {
		return fmt.Errorf("snapshotHash: %w", err)
	}
	return nil
}

type ContextMergeRequest struct {
	ConversationID askdata.ID          `json:"conversationId"`
	TurnID         askdata.ID          `json:"turnId"`
	Scope          askdata.PolicyScope `json:"scope"`
	Question       NormalizedQuestion  `json:"question"`
	Rules          RuleParseResult     `json:"rules"`
	Continuation   ContextContinuation `json:"continuation"`
	Previous       *ContextSnapshot    `json:"previous"`
}

// ContextMergeResult keeps inherited mentions attached to their source
// question. The current question remains separate and always has precedence.
type ContextMergeResult struct {
	Version              string                 `json:"version"`
	ConversationID       askdata.ID             `json:"conversationId"`
	TurnID               askdata.ID             `json:"turnId"`
	PolicyHash           askdata.ContentHash    `json:"policyHash"`
	Release              askdata.ReleaseRef     `json:"release"`
	CurrentQuestionHash  askdata.ContentHash    `json:"currentQuestionHash"`
	PreviousSnapshotHash *askdata.ContentHash   `json:"previousSnapshotHash"`
	Mode                 ContextMergeMode       `json:"mode"`
	Precedence           ContextPrecedence      `json:"precedence"`
	Inherited            *QuestionUnderstanding `json:"inherited"`
	Decisions            []ContextDecision      `json:"decisions"`
	UnresolvedSpans      []UnresolvedSpan       `json:"unresolvedSpans"`
	ContentHash          askdata.ContentHash    `json:"contentHash"`
}

func MergeContext(request ContextMergeRequest) (ContextMergeResult, error) {
	if err := request.validate(); err != nil {
		return ContextMergeResult{}, err
	}
	result, err := mergeContextUnchecked(request)
	if err != nil {
		return ContextMergeResult{}, err
	}
	if err := result.validateFields(request.Question); err != nil {
		return ContextMergeResult{}, fmt.Errorf("context merge result: %w", err)
	}
	return result, nil
}

func (request ContextMergeRequest) validate() error {
	if err := request.ConversationID.Validate(); err != nil {
		return fmt.Errorf("conversationId: %w", err)
	}
	if err := request.TurnID.Validate(); err != nil {
		return fmt.Errorf("turnId: %w", err)
	}
	if err := request.Scope.Validate(); err != nil {
		return fmt.Errorf("scope: %w", err)
	}
	if err := request.Question.Validate(); err != nil {
		return fmt.Errorf("question: %w", err)
	}
	if err := request.Rules.Validate(request.Question); err != nil {
		return fmt.Errorf("rules: %w", err)
	}
	switch request.Continuation {
	case ContextAuto, ContextFollowUp, ContextIndependent:
	default:
		return fmt.Errorf("unsupported continuation %q", request.Continuation)
	}
	return nil
}

func mergeContextUnchecked(request ContextMergeRequest) (ContextMergeResult, error) {
	base := ContextMergeResult{
		Version: ContextMergeVersion, ConversationID: request.ConversationID, TurnID: request.TurnID,
		PolicyHash: request.Scope.PolicyHash, Release: request.Scope.Release,
		CurrentQuestionHash: askdata.HashBytes([]byte(request.Question.Original)),
		Precedence:          ContextCurrentTurnWins,
		Decisions:           []ContextDecision{}, UnresolvedSpans: []UnresolvedSpan{},
	}
	directives, globalClear, err := detectClearDirectives(request.Question)
	if err != nil {
		return ContextMergeResult{}, err
	}
	if globalClear {
		base.Mode = ContextModeClearAll
		base.Decisions = []ContextDecision{{
			Slot: ContextSlotAll, Action: ContextActionClear, Reason: ContextReasonExplicitClear,
			TriggerText: directives[ContextSlotAll].text, TriggerSpan: spanPointer(directives[ContextSlotAll].span),
		}}
		return finalizeContextResult(base)
	}

	followUp := request.Continuation == ContextFollowUp ||
		(request.Continuation == ContextAuto && autoFollowUp(request.Question, request.Rules, len(directives) != 0))
	if request.Continuation == ContextIndependent || !followUp {
		base.Mode = ContextModeIndependent
		return finalizeContextResult(base)
	}
	if request.Previous == nil {
		base.Mode = ContextModeMissing
		span := boundedNormalizedSpan(request.Question)
		unresolved, mapErr := unresolvedFromNormalized(
			request.Question, span, ReasonPreviousContextRequired, NeededConversationContext,
		)
		if mapErr != nil {
			return ContextMergeResult{}, mapErr
		}
		base.UnresolvedSpans = append(base.UnresolvedSpans, unresolved)
		return finalizeContextResult(base)
	}

	if request.Previous != nil {
		if err := request.Previous.validateHeader(); err != nil {
			return ContextMergeResult{}, fmt.Errorf("previous: %w", err)
		}
		if request.Previous.Scope.PolicyHash != request.Scope.PolicyHash {
			base.Mode = ContextModeScopeReset
			base.Decisions = []ContextDecision{{
				Slot: ContextSlotAll, Action: ContextActionReset, Reason: ContextReasonScopeChanged,
			}}
			return finalizeContextResult(base)
		}
		if request.Previous.ConversationID != request.ConversationID {
			base.Mode = ContextModeScopeReset
			base.Decisions = []ContextDecision{{
				Slot: ContextSlotAll, Action: ContextActionReset, Reason: ContextReasonConversationChanged,
			}}
			return finalizeContextResult(base)
		}
		if request.Previous.TurnID == request.TurnID {
			return ContextMergeResult{}, errors.New("current turnId must differ from the previous turnId")
		}
		if err := request.Previous.Validate(); err != nil {
			return ContextMergeResult{}, fmt.Errorf("previous: %w", err)
		}
	}

	base.Mode = ContextModeFollowUp
	previousHash := request.Previous.SnapshotHash
	base.PreviousSnapshotHash = &previousHash
	overrides, err := currentSlotSignals(request.Rules)
	if err != nil {
		return ContextMergeResult{}, err
	}
	present := contextSlotPresence(request.Previous.Understanding)
	inherit := make(map[ContextSlot]bool, len(orderedContextSlots))
	for _, slot := range orderedContextSlots {
		if signal, cleared := directives[slot]; cleared {
			base.Decisions = append(base.Decisions, ContextDecision{
				Slot: slot, Action: ContextActionClear, Reason: ContextReasonExplicitClear,
				TriggerText: signal.text, TriggerSpan: spanPointer(signal.span),
			})
			continue
		}
		if signal, overridden := overrides[slot]; overridden {
			base.Decisions = append(base.Decisions, ContextDecision{
				Slot: slot, Action: ContextActionOverride, Reason: signal.reason,
				TriggerText: signal.text, TriggerSpan: spanPointer(signal.span),
			})
			continue
		}
		if present[slot] {
			inherit[slot] = true
			base.Decisions = append(base.Decisions, ContextDecision{
				Slot: slot, Action: ContextActionInherit, Reason: ContextReasonFollowUp,
			})
		}
	}
	if len(base.Decisions) > MaxContextDecisions {
		return ContextMergeResult{}, fmt.Errorf("context decisions exceed %d items", MaxContextDecisions)
	}
	if len(inherit) != 0 {
		value := inheritedUnderstanding(request.Previous.Understanding, inherit)
		cloned, cloneErr := cloneUnderstanding(value)
		if cloneErr != nil {
			return ContextMergeResult{}, cloneErr
		}
		base.Inherited = &cloned
	}
	return finalizeContextResult(base)
}

func (result ContextMergeResult) Validate(request ContextMergeRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	if err := result.validateFields(request.Question); err != nil {
		return err
	}
	expected, err := mergeContextUnchecked(request)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(result, expected) {
		return errors.New("context merge result does not match its request")
	}
	return nil
}

func (result ContextMergeResult) EvidenceRef(evidenceID askdata.ID) (askdata.EvidenceRef, error) {
	if err := evidenceID.Validate(); err != nil {
		return askdata.EvidenceRef{}, fmt.Errorf("evidenceId: %w", err)
	}
	if err := result.ContentHash.Validate(); err != nil {
		return askdata.EvidenceRef{}, fmt.Errorf("contentHash: %w", err)
	}
	expected, err := contextResultHash(result)
	if err != nil {
		return askdata.EvidenceRef{}, err
	}
	if expected != result.ContentHash {
		return askdata.EvidenceRef{}, errors.New("contentHash does not match the context result")
	}
	if err := result.TurnID.Validate(); err != nil {
		return askdata.EvidenceRef{}, fmt.Errorf("turnId: %w", err)
	}
	return askdata.EvidenceRef{
		EvidenceID: evidenceID, Kind: askdata.EvidenceKindConversation,
		SourceID: result.TurnID, ContentHash: result.ContentHash,
	}, nil
}

func (result ContextMergeResult) validateFields(question NormalizedQuestion) error {
	if result.Version != ContextMergeVersion {
		return fmt.Errorf("version must be %q", ContextMergeVersion)
	}
	if err := result.ConversationID.Validate(); err != nil {
		return fmt.Errorf("conversationId: %w", err)
	}
	if err := result.TurnID.Validate(); err != nil {
		return fmt.Errorf("turnId: %w", err)
	}
	if err := result.PolicyHash.Validate(); err != nil {
		return fmt.Errorf("policyHash: %w", err)
	}
	if err := result.Release.Validate(); err != nil {
		return fmt.Errorf("release: %w", err)
	}
	if result.CurrentQuestionHash != askdata.HashBytes([]byte(question.Original)) {
		return errors.New("currentQuestionHash does not match the current question")
	}
	if result.PreviousSnapshotHash != nil {
		if err := result.PreviousSnapshotHash.Validate(); err != nil {
			return fmt.Errorf("previousSnapshotHash: %w", err)
		}
	}
	if !validContextMode(result.Mode) {
		return fmt.Errorf("unsupported context merge mode %q", result.Mode)
	}
	if result.Precedence != ContextCurrentTurnWins {
		return fmt.Errorf("precedence must be %q", ContextCurrentTurnWins)
	}
	if result.Decisions == nil || result.UnresolvedSpans == nil || len(result.Decisions) > MaxContextDecisions {
		return errors.New("context result collections are invalid")
	}
	seenSlots := make(map[ContextSlot]struct{}, len(result.Decisions))
	for index, decision := range result.Decisions {
		if !validContextSlot(decision.Slot) || !validContextAction(decision.Action) || !validContextReason(decision.Reason) {
			return fmt.Errorf("decisions[%d] has an invalid vocabulary value", index)
		}
		if _, duplicate := seenSlots[decision.Slot]; duplicate {
			return fmt.Errorf("decisions[%d] duplicates slot %q", index, decision.Slot)
		}
		seenSlots[decision.Slot] = struct{}{}
		if (decision.TriggerText == "") != (decision.TriggerSpan == nil) {
			return fmt.Errorf("decisions[%d] trigger text and span must appear together", index)
		}
		if decision.TriggerSpan != nil {
			if err := validateMention(question.Original, decision.TriggerText, *decision.TriggerSpan); err != nil {
				return fmt.Errorf("decisions[%d] trigger: %w", index, err)
			}
		}
	}
	if result.Inherited != nil {
		if err := result.Inherited.Validate(); err != nil {
			return fmt.Errorf("inherited: %w", err)
		}
		if len(result.Inherited.UnresolvedSpans) != 0 {
			return errors.New("inherited context must not contain unresolved spans")
		}
	}
	for index, unresolved := range result.UnresolvedSpans {
		if err := validateMention(question.Original, unresolved.Text, unresolved.Span); err != nil {
			return fmt.Errorf("unresolvedSpans[%d]: %w", index, err)
		}
		if unresolved.Reason != ReasonPreviousContextRequired || len(unresolved.NeededEvidence) != 1 || unresolved.NeededEvidence[0] != NeededConversationContext {
			return fmt.Errorf("unresolvedSpans[%d] is not a supported context gap", index)
		}
	}
	if err := validateContextModeShape(result); err != nil {
		return err
	}
	if err := result.ContentHash.Validate(); err != nil {
		return fmt.Errorf("contentHash: %w", err)
	}
	expected, err := contextResultHash(result)
	if err != nil {
		return err
	}
	if expected != result.ContentHash {
		return errors.New("contentHash does not match the context result")
	}
	return nil
}

func validateContextModeShape(result ContextMergeResult) error {
	switch result.Mode {
	case ContextModeFollowUp:
		if result.PreviousSnapshotHash == nil || len(result.UnresolvedSpans) != 0 {
			return errors.New("follow-up context requires a previous snapshot and no context gap")
		}
	case ContextModeMissing:
		if result.PreviousSnapshotHash != nil || result.Inherited != nil || len(result.UnresolvedSpans) != 1 {
			return errors.New("missing context mode has an invalid shape")
		}
	case ContextModeIndependent, ContextModeClearAll, ContextModeScopeReset:
		if result.PreviousSnapshotHash != nil || result.Inherited != nil || len(result.UnresolvedSpans) != 0 {
			return errors.New("non-inheriting context mode has an invalid shape")
		}
	}
	return nil
}

type contextSignal struct {
	text   string
	span   Span
	reason ContextReason
}

type contextClearRule struct {
	phrase string
	slots  []ContextSlot
}

var contextClearRules = []contextClearRule{
	{"清除全部条件", []ContextSlot{ContextSlotAll}}, {"清空上下文", []ContextSlot{ContextSlotAll}},
	{"全部清除", []ContextSlot{ContextSlotAll}}, {"重新开始", []ContextSlot{ContextSlotAll}},
	{"重新提问", []ContextSlot{ContextSlotAll}}, {"新问题", []ContextSlot{ContextSlotAll}},
	{"不要时间条件", []ContextSlot{ContextSlotTime}}, {"不限定时间", []ContextSlot{ContextSlotTime}},
	{"不限时间", []ContextSlot{ContextSlotTime}}, {"取消时间", []ContextSlot{ContextSlotTime}},
	{"清除时间", []ContextSlot{ContextSlotTime}},
	{"取消分组", []ContextSlot{ContextSlotGrouping}}, {"清除分组", []ContextSlot{ContextSlotGrouping}},
	{"不要分组", []ContextSlot{ContextSlotGrouping}}, {"不分组", []ContextSlot{ContextSlotGrouping}},
	{"不按", []ContextSlot{ContextSlotGrouping}},
	{"取消筛选", []ContextSlot{ContextSlotFilter}}, {"清除筛选", []ContextSlot{ContextSlotFilter}},
	{"不要筛选", []ContextSlot{ContextSlotFilter}}, {"不限条件", []ContextSlot{ContextSlotFilter}},
	{"取消比较", []ContextSlot{ContextSlotComparison}}, {"清除比较", []ContextSlot{ContextSlotComparison}},
	{"不要比较", []ContextSlot{ContextSlotComparison}}, {"不比较", []ContextSlot{ContextSlotComparison}},
	{"取消同比", []ContextSlot{ContextSlotComparison}}, {"不要同比", []ContextSlot{ContextSlotComparison}},
	{"取消环比", []ContextSlot{ContextSlotComparison}}, {"不要环比", []ContextSlot{ContextSlotComparison}},
	{"取消排序", []ContextSlot{ContextSlotOrdering}}, {"清除排序", []ContextSlot{ContextSlotOrdering}},
	{"不要排序", []ContextSlot{ContextSlotOrdering}}, {"不排序", []ContextSlot{ContextSlotOrdering}},
	{"取消排名", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"清除排名", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"不限排名", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"不要排名", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"取消前", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"不要前", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"取消top", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"不要top", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"取消bottom", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"不要bottom", []ContextSlot{ContextSlotLimit, ContextSlotOrdering}},
	{"取消指标", []ContextSlot{ContextSlotMetric}}, {"清除指标", []ContextSlot{ContextSlotMetric}},
}

func detectClearDirectives(question NormalizedQuestion) (map[ContextSlot]contextSignal, bool, error) {
	result := make(map[ContextSlot]contextSignal)
	for _, rule := range contextClearRules {
		for _, span := range findLiteralSpans(question.Normalized, rule.phrase) {
			original, text, err := originalMatch(question, span)
			if err != nil {
				return nil, false, err
			}
			signal := contextSignal{text: text, span: original, reason: ContextReasonExplicitClear}
			for _, slot := range rule.slots {
				current, exists := result[slot]
				if !exists || earlierSignal(signal, current) {
					result[slot] = signal
				}
			}
		}
	}
	_, global := result[ContextSlotAll]
	return result, global, nil
}

func currentSlotSignals(rules RuleParseResult) (map[ContextSlot]contextSignal, error) {
	result := make(map[ContextSlot]contextSignal)
	add := func(slot ContextSlot, text string, span Span, reason ContextReason) {
		signal := contextSignal{text: text, span: span, reason: reason}
		if current, exists := result[slot]; !exists || earlierSignal(signal, current) {
			result[slot] = signal
		}
	}
	if rules.Time != nil {
		add(ContextSlotTime, rules.Time.Text, rules.Time.Span, ContextReasonCurrentTime)
	}
	if len(rules.Groupings) != 0 {
		add(ContextSlotGrouping, rules.Groupings[0].Text, rules.Groupings[0].Span, ContextReasonCurrentGrouping)
	}
	if len(rules.Comparisons) != 0 {
		add(ContextSlotComparison, rules.Comparisons[0].Text, rules.Comparisons[0].Span, ContextReasonCurrentComparison)
	}
	if rules.Ranking != nil {
		add(ContextSlotLimit, rules.Ranking.Text, rules.Ranking.Span, ContextReasonCurrentRanking)
		add(ContextSlotOrdering, rules.Ranking.Text, rules.Ranking.Span, ContextReasonCurrentRanking)
	}
	if len(rules.Sorts) != 0 {
		add(ContextSlotOrdering, rules.Sorts[0].Text, rules.Sorts[0].Span, ContextReasonCurrentSort)
	}
	for _, unresolved := range rules.UnresolvedSpans {
		switch {
		case strings.HasPrefix(unresolved.Reason, "TIME_"):
			add(ContextSlotTime, unresolved.Text, unresolved.Span, ContextReasonCurrentTime)
		case unresolved.Reason == ReasonMultipleComparisons:
			add(ContextSlotComparison, unresolved.Text, unresolved.Span, ContextReasonCurrentComparison)
		case unresolved.Reason == ReasonLimitOutOfRange || unresolved.Reason == ReasonUnsupportedRankRatio || unresolved.Reason == ReasonMultipleRankings:
			add(ContextSlotLimit, unresolved.Text, unresolved.Span, ContextReasonCurrentRanking)
			add(ContextSlotOrdering, unresolved.Text, unresolved.Span, ContextReasonCurrentRanking)
		case unresolved.Reason == ReasonConflictingOrder:
			add(ContextSlotOrdering, unresolved.Text, unresolved.Span, ContextReasonCurrentSort)
		}
	}
	return result, nil
}

func earlierSignal(left, right contextSignal) bool {
	if left.span.Start != right.span.Start {
		return left.span.Start < right.span.Start
	}
	return left.span.End-left.span.Start > right.span.End-right.span.Start
}

func autoFollowUp(question NormalizedQuestion, rules RuleParseResult, hasClear bool) bool {
	if hasClear {
		return true
	}
	text := strings.TrimSpace(question.Normalized)
	for _, prefix := range []string{
		"那么", "然后", "换成", "换为", "改成", "改为", "改看", "换看", "接着", "继续",
		"那", "再", "还", "只看", "只要", "按", "每个",
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return questionCoveredByRules(question, rules)
}

func questionCoveredByRules(question NormalizedQuestion, rules RuleParseResult) bool {
	runes := []rune(question.Normalized)
	covered := make([]bool, len(runes))
	mark := func(original Span) {
		normalized, err := question.NormalizedSpan(original)
		if err != nil {
			return
		}
		for index := normalized.Start; index < normalized.End; index++ {
			covered[index] = true
		}
	}
	matchCount := 0
	if rules.Time != nil {
		mark(rules.Time.Span)
		matchCount++
	}
	for _, value := range rules.Comparisons {
		mark(value.Span)
		matchCount++
	}
	if rules.Ranking != nil {
		mark(rules.Ranking.Span)
		matchCount++
	}
	for _, value := range rules.Sorts {
		mark(value.Span)
		matchCount++
	}
	for _, value := range rules.Groupings {
		mark(value.Span)
		matchCount++
	}
	for _, value := range rules.UnresolvedSpans {
		mark(value.Span)
		matchCount++
	}
	if matchCount == 0 {
		return false
	}
	for index, value := range runes {
		if covered[index] || unicode.IsSpace(value) || strings.ContainsRune(",.;:!?，。；：！？", value) {
			continue
		}
		return false
	}
	return true
}

func contextSlotPresence(value QuestionUnderstanding) map[ContextSlot]bool {
	result := make(map[ContextSlot]bool)
	result[ContextSlotDomain] = len(value.DomainHypotheses) != 0
	result[ContextSlotMetric] = len(value.MetricMentions) != 0
	for _, dimension := range value.DimensionMentions {
		result[slotForDimension(dimension.Role)] = true
	}
	result[ContextSlotFilter] = result[ContextSlotFilter] || len(value.ValueMentions) != 0
	result[ContextSlotTime] = result[ContextSlotTime] || value.Time != nil
	result[ContextSlotComparison] = len(value.Comparisons) != 0
	result[ContextSlotOrdering] = result[ContextSlotOrdering] || len(value.Ordering) != 0
	result[ContextSlotLimit] = value.Limit != nil
	return result
}

func slotForDimension(role DimensionRole) ContextSlot {
	switch role {
	case DimensionRoleGroupBy:
		return ContextSlotGrouping
	case DimensionRoleFilter:
		return ContextSlotFilter
	case DimensionRoleTime:
		return ContextSlotTime
	default:
		return ContextSlotOrdering
	}
}

func inheritedUnderstanding(previous QuestionUnderstanding, inherit map[ContextSlot]bool) QuestionUnderstanding {
	result := QuestionUnderstanding{
		SchemaVersion: SchemaVersion, Question: previous.Question,
		DomainHypotheses: []DomainHypothesis{}, MetricMentions: []MetricMention{},
		DimensionMentions: []DimensionMention{}, ValueMentions: []ValueMention{},
		Comparisons: []ComparisonMention{}, Ordering: []OrderingMention{},
		UnresolvedSpans: []UnresolvedSpan{},
	}
	if inherit[ContextSlotDomain] {
		result.DomainHypotheses = append(result.DomainHypotheses, previous.DomainHypotheses...)
	}
	if inherit[ContextSlotMetric] {
		result.MetricMentions = append(result.MetricMentions, previous.MetricMentions...)
	}
	for _, dimension := range previous.DimensionMentions {
		if inherit[slotForDimension(dimension.Role)] {
			result.DimensionMentions = append(result.DimensionMentions, dimension)
		}
	}
	if inherit[ContextSlotFilter] {
		result.ValueMentions = append(result.ValueMentions, previous.ValueMentions...)
	}
	if inherit[ContextSlotTime] && previous.Time != nil {
		value := *previous.Time
		result.Time = &value
	}
	if inherit[ContextSlotComparison] {
		result.Comparisons = append(result.Comparisons, previous.Comparisons...)
	}
	if inherit[ContextSlotOrdering] {
		result.Ordering = append(result.Ordering, previous.Ordering...)
	}
	if inherit[ContextSlotLimit] && previous.Limit != nil {
		value := *previous.Limit
		result.Limit = &value
	}
	return result
}

func cloneUnderstanding(value QuestionUnderstanding) (QuestionUnderstanding, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return QuestionUnderstanding{}, fmt.Errorf("marshal understanding: %w", err)
	}
	cloned, err := Decode(raw)
	if err != nil {
		return QuestionUnderstanding{}, fmt.Errorf("clone understanding: %w", err)
	}
	return cloned, nil
}

func boundedNormalizedSpan(question NormalizedQuestion) Span {
	length := utf8.RuneCountInString(question.Normalized)
	if length > 512 {
		length = 512
	}
	return Span{Start: 0, End: length}
}

func spanPointer(value Span) *Span {
	copy := value
	return &copy
}

func validContextMode(value ContextMergeMode) bool {
	switch value {
	case ContextModeIndependent, ContextModeFollowUp, ContextModeClearAll, ContextModeScopeReset, ContextModeMissing:
		return true
	default:
		return false
	}
}

func validContextSlot(value ContextSlot) bool {
	if value == ContextSlotAll {
		return true
	}
	for _, slot := range orderedContextSlots {
		if value == slot {
			return true
		}
	}
	return false
}

func validContextAction(value ContextAction) bool {
	switch value {
	case ContextActionInherit, ContextActionOverride, ContextActionClear, ContextActionReset:
		return true
	default:
		return false
	}
}

func validContextReason(value ContextReason) bool {
	switch value {
	case ContextReasonFollowUp, ContextReasonCurrentTime, ContextReasonCurrentGrouping,
		ContextReasonCurrentComparison, ContextReasonCurrentRanking, ContextReasonCurrentSort,
		ContextReasonExplicitClear, ContextReasonScopeChanged, ContextReasonConversationChanged:
		return true
	default:
		return false
	}
}

func finalizeContextResult(result ContextMergeResult) (ContextMergeResult, error) {
	var err error
	result.ContentHash, err = contextResultHash(result)
	return result, err
}

func contextResultHash(result ContextMergeResult) (askdata.ContentHash, error) {
	result.ContentHash = ""
	raw, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal context result: %w", err)
	}
	return askdata.HashBytes(raw), nil
}

func contextSnapshotHash(snapshot ContextSnapshot) (askdata.ContentHash, error) {
	snapshot.SnapshotHash = ""
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return "", fmt.Errorf("marshal context snapshot: %w", err)
	}
	return askdata.HashBytes(raw), nil
}
