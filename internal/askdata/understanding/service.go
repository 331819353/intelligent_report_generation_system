package understanding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/security"
)

const (
	UnderstandingReviewSchemaVersion = "question-understanding-review-v1"
	UnderstandingResultSchemaVersion = "question-understanding-result-v1"
	MaxUnderstandingConflicts        = 32
	MaxUnderstandingEvidenceRequests = 96
	MaxUnderstandingEvidenceRefs     = 64
	MaxUnderstandingSummaryRunes     = 1_000
)

var (
	ErrInvalidUnderstandingRequest  = errors.New("question understanding request is invalid")
	ErrInvalidUnderstandingProposal = errors.New("question understanding proposal is invalid")
	physicalQueryTextPattern        = regexp.MustCompile(`(?is)\b(select\s+.+\s+from|insert\s+into|update\s+.+\s+set|delete\s+from|copy\s+.+\s+(from|to)|create\s+(table|view)|alter\s+table|drop\s+(table|view)|match\s*\(.+\)\s*(return|where))\b`)
)

// EvidenceOrigin keeps inherited mentions attached to the question in which
// they were spoken. A follow-up never fabricates current-turn spans for a
// metric or filter inherited from an earlier turn.
type EvidenceOrigin string

const (
	EvidenceOriginCurrent   EvidenceOrigin = "CURRENT"
	EvidenceOriginInherited EvidenceOrigin = "INHERITED"
)

// ExactMatch is a bounded, already authorized dictionary hit supplied to the
// understanding model. SourceID is a stable semantic version ID, never a
// physical table or column identifier.
type ExactMatch struct {
	ObjectType     search.ObjectType   `json:"objectType"`
	CanonicalLabel string              `json:"canonicalLabel"`
	Text           string              `json:"text"`
	Span           Span                `json:"span"`
	Evidence       askdata.EvidenceRef `json:"evidence"`
}

// ResidualSpan is text not consumed by deterministic rules or exact matches.
// It is prompt evidence only; the reviewer must still return a validated
// mention or unresolved span for anything that affects the intent.
type ResidualSpan struct {
	Text string `json:"text"`
	Span Span   `json:"span"`
}

// UnderstandingConflict is a model-visible description of an unresolved
// current-turn ambiguity. The local validator requires a matching
// QuestionUnderstanding.unresolvedSpans entry and protects deterministic rule
// and context conflict codes from being removed or renamed.
type UnderstandingConflict struct {
	Code         string                `json:"code"`
	Text         string                `json:"text"`
	Span         Span                  `json:"span"`
	Summary      string                `json:"summary"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

// UnderstandingEvidenceRequest describes the next typed evidence needed by
// retrieval/binding. It deliberately contains no tool name, object ID, SQL or
// provider-specific arguments. The orchestrator maps this bounded vocabulary
// to trusted retrieval tools after NLU-004.
type UnderstandingEvidenceRequest struct {
	Origin         EvidenceOrigin        `json:"origin"`
	NeededEvidence NeededEvidence        `json:"neededEvidence"`
	Text           string                `json:"text"`
	Span           Span                  `json:"span"`
	Reason         string                `json:"reason"`
	EvidenceRefs   []askdata.EvidenceRef `json:"evidenceRefs"`
}

// UnderstandingProposal is the dedicated structured-output contract for the
// NLU reviewer. It does not extend the frozen Cognition Action vocabulary.
type UnderstandingProposal struct {
	SchemaVersion    string                         `json:"schemaVersion"`
	Understanding    QuestionUnderstanding          `json:"understanding"`
	Conflicts        []UnderstandingConflict        `json:"conflicts"`
	EvidenceRequests []UnderstandingEvidenceRequest `json:"evidenceRequests"`
	EvidenceRefs     []askdata.EvidenceRef          `json:"evidenceRefs"`
}

// UnderstandingReviewInput is the complete, sanitized reviewer boundary.
// Facts use AI-003's stage-specific fact kinds; AllowedEvidenceRefs is the
// closed citation set accepted from the model.
type UnderstandingReviewInput struct {
	Stage               cognition.Stage        `json:"stage"`
	Facts               []cognition.PromptFact `json:"facts"`
	AllowedEvidenceRefs []askdata.EvidenceRef  `json:"allowedEvidenceRefs"`
}

type UnderstandingReviewer interface {
	ReviewUnderstanding(context.Context, UnderstandingReviewInput) (UnderstandingProposal, error)
}

type UnderstandingRequest struct {
	ContextRequest         ContextMergeRequest         `json:"contextRequest"`
	Context                ContextMergeResult          `json:"context"`
	ExactMatches           []ExactMatch                `json:"exactMatches"`
	SensitiveMemberMatches []security.ExactMemberMatch `json:"sensitiveMemberMatches"`
}

type sensitiveMemberBinding struct {
	Match    security.ExactMemberMatch
	Origin   EvidenceOrigin
	Question string
	Span     Span
	redact   func(string) (string, error)
}

type SensitiveMemberFact struct {
	Origin             EvidenceOrigin      `json:"origin"`
	DimensionVersionID askdata.ID          `json:"dimensionVersionId"`
	Span               Span                `json:"span"`
	Evidence           askdata.EvidenceRef `json:"evidence"`
}

func bindSensitiveMemberMatches(request UnderstandingRequest) ([]sensitiveMemberBinding, error) {
	if request.SensitiveMemberMatches == nil || len(request.SensitiveMemberMatches) > 128 {
		return nil, fmt.Errorf("%w: sensitive member matches", ErrInvalidUnderstandingRequest)
	}
	bindings := make([]sensitiveMemberBinding, 0, len(request.SensitiveMemberMatches))
	for index, match := range request.SensitiveMemberMatches {
		if err := match.Validate(); err != nil {
			return nil, fmt.Errorf("%w: sensitiveMemberMatches[%d]", ErrInvalidUnderstandingRequest, index)
		}
		currentSpan, currentErr := match.SensitiveSpan(request.ContextRequest.Question.Original)
		var inheritedSpan security.RuneSpan
		inheritedErr := security.ErrInvalidMemberRedaction
		if request.Context.Inherited != nil {
			inheritedSpan, inheritedErr = match.SensitiveSpan(request.Context.Inherited.Question)
		}
		current := currentErr == nil
		inherited := inheritedErr == nil
		if current == inherited {
			return nil, fmt.Errorf("%w: sensitiveMemberMatches[%d] question binding", ErrInvalidUnderstandingRequest, index)
		}
		binding := sensitiveMemberBinding{Match: match}
		if current {
			binding.Origin = EvidenceOriginCurrent
			binding.Question = request.ContextRequest.Question.Original
			binding.Span = Span{Start: currentSpan.Start, End: currentSpan.End}
		} else {
			binding.Origin = EvidenceOriginInherited
			binding.Question = request.Context.Inherited.Question
			binding.Span = Span{Start: inheritedSpan.Start, End: inheritedSpan.End}
		}
		sourceMatch, sourceQuestion := match, binding.Question
		binding.redact = func(value string) (string, error) {
			return sourceMatch.RedactPromptText(sourceQuestion, value)
		}
		bindings = append(bindings, binding)
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].Origin != bindings[j].Origin {
			return bindings[i].Origin < bindings[j].Origin
		}
		if bindings[i].Span.Start != bindings[j].Span.Start {
			return bindings[i].Span.Start < bindings[j].Span.Start
		}
		if bindings[i].Span.End != bindings[j].Span.End {
			return bindings[i].Span.End < bindings[j].Span.End
		}
		return bindings[i].Match.EvidenceRef().SourceID < bindings[j].Match.EvidenceRef().SourceID
	})
	seen := map[string]struct{}{}
	for index, binding := range bindings {
		identity := fmt.Sprintf(
			"%s\x00%d\x00%d\x00%s", binding.Origin, binding.Span.Start,
			binding.Span.End, binding.Match.EvidenceRef().SourceID,
		)
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("%w: sensitive member match is duplicated", ErrInvalidUnderstandingRequest)
		}
		seen[identity] = struct{}{}
		if index > 0 && bindings[index-1].Origin == binding.Origin &&
			spansOverlap(bindings[index-1].Span, binding.Span) {
			return nil, fmt.Errorf("%w: sensitive member spans overlap", ErrInvalidUnderstandingRequest)
		}
	}
	return bindings, nil
}

// UnderstandingResult keeps current and inherited intent separate, records
// the accepted LLM candidate hash, and is replayable against the deterministic
// request. Current-turn precedence remains owned by ContextMergeResult.
type UnderstandingResult struct {
	SchemaVersion    string                         `json:"schemaVersion"`
	Context          ContextMergeResult             `json:"context"`
	Current          QuestionUnderstanding          `json:"current"`
	Conflicts        []UnderstandingConflict        `json:"conflicts"`
	EvidenceRequests []UnderstandingEvidenceRequest `json:"evidenceRequests"`
	EvidenceRefs     []askdata.EvidenceRef          `json:"evidenceRefs"`
	ProposalHash     askdata.ContentHash            `json:"proposalHash"`
	ResultHash       askdata.ContentHash            `json:"resultHash"`
}

type UnderstandingService struct{ reviewer UnderstandingReviewer }

func NewUnderstandingService(reviewer UnderstandingReviewer) (*UnderstandingService, error) {
	if reviewer == nil {
		return nil, errors.New("understanding reviewer is required")
	}
	return &UnderstandingService{reviewer: reviewer}, nil
}

// DecodeUnderstandingProposal is intended for provider adapters. Unknown or
// duplicate JSON fields fail before the proposal can reach the service.
func DecodeUnderstandingProposal(raw []byte) (UnderstandingProposal, error) {
	var proposal UnderstandingProposal
	if err := askdata.DecodeStrictJSON(raw, &proposal); err != nil {
		return UnderstandingProposal{}, err
	}
	proposal = normalizeUnderstandingProposal(proposal)
	if err := proposal.Validate(); err != nil {
		return UnderstandingProposal{}, err
	}
	return proposal, nil
}

func (service *UnderstandingService) Understand(ctx context.Context, request UnderstandingRequest) (UnderstandingResult, error) {
	if service == nil || service.reviewer == nil {
		return UnderstandingResult{}, ErrInvalidUnderstandingRequest
	}
	input, err := BuildUnderstandingReviewInput(request)
	if err != nil {
		return UnderstandingResult{}, err
	}
	proposal, err := service.reviewer.ReviewUnderstanding(ctx, cloneUnderstandingReviewInput(input))
	if err != nil {
		return UnderstandingResult{}, err
	}
	proposal = normalizeUnderstandingProposal(proposal)
	proposal, err = restoreSensitiveReviewerProposal(proposal, request)
	if err != nil {
		return UnderstandingResult{}, err
	}
	proposal, err = pinProposalToSelectedDomain(proposal, request.ContextRequest.Scope)
	if err != nil {
		return UnderstandingResult{}, fmt.Errorf("%w: %v", ErrInvalidUnderstandingProposal, err)
	}
	proposal = normalizeUnderstandingProposal(proposal)
	if err := proposal.validateAgainst(request, input.AllowedEvidenceRefs); err != nil {
		return UnderstandingResult{}, err
	}
	proposalPayload, err := json.Marshal(proposal)
	if err != nil {
		return UnderstandingResult{}, err
	}
	current, err := cloneUnderstanding(proposal.Understanding)
	if err != nil {
		return UnderstandingResult{}, err
	}
	result := UnderstandingResult{
		SchemaVersion:    UnderstandingResultSchemaVersion,
		Context:          request.Context,
		Current:          current,
		Conflicts:        cloneUnderstandingConflicts(proposal.Conflicts),
		EvidenceRequests: cloneUnderstandingEvidenceRequests(proposal.EvidenceRequests),
		EvidenceRefs:     append([]askdata.EvidenceRef(nil), proposal.EvidenceRefs...),
		ProposalHash:     askdata.HashBytes(proposalPayload),
	}
	resultPayload, err := understandingResultPayload(result)
	if err != nil {
		return UnderstandingResult{}, err
	}
	result.ResultHash = askdata.HashBytes(resultPayload)
	if err := result.Validate(request); err != nil {
		return UnderstandingResult{}, err
	}
	return result, nil
}

// BuildUnderstandingReviewInput validates all trusted upstream artifacts and
// creates exactly the four UNDERSTANDING-stage facts authorized by AI-003.
func BuildUnderstandingReviewInput(request UnderstandingRequest) (UnderstandingReviewInput, error) {
	if err := request.validate(); err != nil {
		return UnderstandingReviewInput{}, err
	}
	sensitiveBindings, err := bindSensitiveMemberMatches(request)
	if err != nil {
		return UnderstandingReviewInput{}, err
	}
	contextRef, err := request.Context.EvidenceRef(askdata.ID("nlu-context:" + string(request.Context.ContentHash)))
	if err != nil {
		return UnderstandingReviewInput{}, fmt.Errorf("%w: context evidence: %v", ErrInvalidUnderstandingRequest, err)
	}
	rulePayload, err := json.Marshal(request.ContextRequest.Rules)
	if err != nil {
		return UnderstandingReviewInput{}, err
	}
	ruleHash := askdata.HashBytes(rulePayload)
	ruleRef := askdata.EvidenceRef{
		EvidenceID: askdata.ID("nlu-rule:" + string(ruleHash)), Kind: askdata.EvidenceKindRule,
		SourceID: request.ContextRequest.TurnID, ContentHash: ruleHash,
	}
	policyRef, err := selectedDomainPolicyEvidence(request.ContextRequest.Scope)
	if err != nil {
		return UnderstandingReviewInput{}, fmt.Errorf("%w: selected domain", ErrInvalidUnderstandingRequest)
	}
	for _, ref := range []askdata.EvidenceRef{contextRef, ruleRef, policyRef} {
		if err := ref.Validate(); err != nil {
			return UnderstandingReviewInput{}, fmt.Errorf("%w: base evidence: %v", ErrInvalidUnderstandingRequest, err)
		}
	}

	matches := append([]ExactMatch(nil), request.ExactMatches...)
	sortExactMatches(matches)
	currentSensitiveSpans := []Span{}
	sensitiveFacts := make([]SensitiveMemberFact, 0, len(sensitiveBindings))
	for _, binding := range sensitiveBindings {
		if binding.Origin == EvidenceOriginCurrent {
			currentSensitiveSpans = append(currentSensitiveSpans, binding.Span)
		}
		sensitiveFacts = append(sensitiveFacts, SensitiveMemberFact{
			Origin: binding.Origin, DimensionVersionID: binding.Match.DimensionVersionID(),
			Span: binding.Span, Evidence: binding.Match.EvidenceRef(),
		})
	}
	residual := residualSpans(
		request.ContextRequest.Question.Original, request.ContextRequest.Rules,
		request.Context, matches, currentSensitiveSpans,
	)
	conversationPayload := struct {
		Question string              `json:"question"`
		Context  ContextMergeResult  `json:"context"`
		Evidence askdata.EvidenceRef `json:"evidence"`
	}{request.ContextRequest.Question.Original, request.Context, contextRef}
	exactPayload := struct {
		Matches          []ExactMatch          `json:"matches"`
		SensitiveMembers []SensitiveMemberFact `json:"sensitiveMembers"`
		Residual         []ResidualSpan        `json:"residualSpans"`
	}{matches, sensitiveFacts, residual}
	rulesPayload := struct {
		Rules    RuleParseResult     `json:"rules"`
		Evidence askdata.EvidenceRef `json:"evidence"`
	}{request.ContextRequest.Rules, ruleRef}
	selectedDomainID, err := selectedDomainFromScope(request.ContextRequest.Scope)
	if err != nil {
		return UnderstandingReviewInput{}, fmt.Errorf("%w: selected domain", ErrInvalidUnderstandingRequest)
	}
	policyPayload := struct {
		DomainID   askdata.ID          `json:"domainId"`
		Release    askdata.ReleaseRef  `json:"release"`
		PolicyHash askdata.ContentHash `json:"policyHash"`
		Evidence   askdata.EvidenceRef `json:"evidence"`
	}{selectedDomainID, request.ContextRequest.Scope.Release, request.ContextRequest.Scope.PolicyHash, policyRef}

	facts := make([]cognition.PromptFact, 0, 4)
	for _, item := range []struct {
		kind    cognition.FactKind
		payload any
	}{
		{cognition.FactConversation, conversationPayload},
		{cognition.FactExactMatches, exactPayload},
		{cognition.FactRuleParse, rulesPayload},
		{cognition.FactPolicyEvidence, policyPayload},
	} {
		payload, marshalErr := marshalSanitizedFact(
			item.payload, sensitiveBindings,
		)
		if marshalErr != nil {
			return UnderstandingReviewInput{}, marshalErr
		}
		factHash := askdata.HashBytes(payload)
		fact, factErr := cognition.NewPromptFact(askdata.ID("nlu-fact:"+string(factHash)), item.kind, payload)
		if factErr != nil {
			return UnderstandingReviewInput{}, fmt.Errorf("%w: %s fact: %v", ErrInvalidUnderstandingRequest, item.kind, factErr)
		}
		facts = append(facts, fact)
	}
	allowed := []askdata.EvidenceRef{contextRef, ruleRef, policyRef}
	for _, match := range matches {
		allowed = append(allowed, match.Evidence)
	}
	for _, binding := range sensitiveBindings {
		allowed = append(allowed, binding.Match.EvidenceRef())
	}
	allowed = normalizedUnderstandingEvidenceRefs(allowed)
	return UnderstandingReviewInput{Stage: cognition.StageUnderstanding, Facts: facts, AllowedEvidenceRefs: allowed}, nil
}

func marshalSanitizedFact(payload any, bindings []sensitiveMemberBinding) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	value, err = sanitizeJSONValue(value, orderedRedactionBindings(bindings))
	if err != nil {
		return nil, fmt.Errorf("%w: sensitive prompt redaction", ErrInvalidUnderstandingRequest)
	}
	return json.Marshal(value)
}

func sanitizeJSONValue(value any, bindings []sensitiveMemberBinding) (any, error) {
	switch typed := value.(type) {
	case string:
		return sanitizePromptString(typed, bindings)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			redacted, err := sanitizeJSONValue(item, bindings)
			if err != nil {
				return nil, err
			}
			result[index] = redacted
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			redacted, err := sanitizeJSONValue(item, bindings)
			if err != nil {
				return nil, err
			}
			result[key] = redacted
		}
		return result, nil
	default:
		return value, nil
	}
}

func sanitizePromptString(value string, bindings []sensitiveMemberBinding) (string, error) {
	result := value
	for _, binding := range bindings {
		if binding.redact == nil {
			return "", security.ErrInvalidMemberRedaction
		}
		redacted, err := binding.redact(result)
		if err != nil {
			return "", err
		}
		result = redacted
	}
	return result, nil
}

func orderedRedactionBindings(bindings []sensitiveMemberBinding) []sensitiveMemberBinding {
	result := append([]sensitiveMemberBinding(nil), bindings...)
	sort.Slice(result, func(i, j int) bool {
		leftLength := result[i].Span.End - result[i].Span.Start
		rightLength := result[j].Span.End - result[j].Span.Start
		if leftLength != rightLength {
			return leftLength > rightLength
		}
		if result[i].Origin != result[j].Origin {
			return result[i].Origin < result[j].Origin
		}
		if result[i].Span.Start != result[j].Span.Start {
			return result[i].Span.Start < result[j].Span.Start
		}
		return result[i].Match.EvidenceRef().SourceID < result[j].Match.EvidenceRef().SourceID
	})
	return result
}

func restoreSensitiveReviewerProposal(
	proposal UnderstandingProposal, request UnderstandingRequest,
) (UnderstandingProposal, error) {
	bindings, err := bindSensitiveMemberMatches(request)
	if err != nil {
		return UnderstandingProposal{}, err
	}
	return restoreSensitiveReviewerProposalWithBindings(proposal, request, bindings)
}

func restoreSensitiveReviewerProposalWithBindings(
	proposal UnderstandingProposal,
	request UnderstandingRequest,
	bindings []sensitiveMemberBinding,
) (UnderstandingProposal, error) {
	if len(bindings) == 0 {
		return proposal, nil
	}
	views, err := sensitiveQuestionViews(request, bindings)
	if err != nil {
		return UnderstandingProposal{}, err
	}
	current := views[EvidenceOriginCurrent]
	if proposal.Understanding.Question != current.safe {
		return UnderstandingProposal{}, fmt.Errorf("%w: reviewer question is not the redacted current question", ErrInvalidUnderstandingProposal)
	}
	// Validate the model response against the exact safe question it saw before
	// restoring the original question outside the LLM boundary.
	if err := proposal.Validate(); err != nil {
		return UnderstandingProposal{}, err
	}
	if err := rejectSensitiveProposalSpans(proposal, views); err != nil {
		return UnderstandingProposal{}, err
	}
	for _, value := range modelVisibleProposalStrings(proposal) {
		redacted, redactErr := sanitizePromptString(value, bindings)
		if redactErr != nil || redacted != value {
			return UnderstandingProposal{}, fmt.Errorf("%w: reviewer echoed sensitive member text", ErrInvalidUnderstandingProposal)
		}
	}
	proposal.Understanding.Question = request.ContextRequest.Question.Original
	return proposal, nil
}

type sensitiveQuestionView struct {
	raw   string
	safe  string
	spans []Span
}

func sensitiveQuestionViews(
	request UnderstandingRequest, bindings []sensitiveMemberBinding,
) (map[EvidenceOrigin]sensitiveQuestionView, error) {
	result := map[EvidenceOrigin]sensitiveQuestionView{}
	questions := map[EvidenceOrigin]string{
		EvidenceOriginCurrent: request.ContextRequest.Question.Original,
	}
	if request.Context.Inherited != nil {
		questions[EvidenceOriginInherited] = request.Context.Inherited.Question
	}
	for origin, raw := range questions {
		safe, err := sanitizePromptString(raw, bindings)
		if err != nil {
			return nil, fmt.Errorf("%w: sensitive question view", ErrInvalidUnderstandingRequest)
		}
		spans, err := changedRuneSpans(raw, safe)
		if err != nil {
			return nil, err
		}
		result[origin] = sensitiveQuestionView{raw: raw, safe: safe, spans: spans}
	}
	return result, nil
}

func changedRuneSpans(raw, safe string) ([]Span, error) {
	rawRunes, safeRunes := []rune(raw), []rune(safe)
	if len(rawRunes) != len(safeRunes) {
		return nil, fmt.Errorf("%w: prompt redaction changed rune offsets", ErrInvalidUnderstandingRequest)
	}
	spans := []Span{}
	for index := 0; index < len(rawRunes); {
		for index < len(rawRunes) && rawRunes[index] == safeRunes[index] {
			index++
		}
		start := index
		for index < len(rawRunes) && rawRunes[index] != safeRunes[index] {
			index++
		}
		if start < index {
			spans = append(spans, Span{Start: start, End: index})
		}
	}
	return spans, nil
}

func rejectSensitiveProposalSpans(
	proposal UnderstandingProposal, views map[EvidenceOrigin]sensitiveQuestionView,
) error {
	check := func(origin EvidenceOrigin, label string, span Span) error {
		for _, sensitive := range views[origin].spans {
			if spansOverlap(span, sensitive) {
				return fmt.Errorf("%w: %s overlaps a sensitive member span", ErrInvalidUnderstandingProposal, label)
			}
		}
		return nil
	}
	for index, mention := range proposal.Understanding.MetricMentions {
		if err := check(EvidenceOriginCurrent, fmt.Sprintf("metricMentions[%d]", index), mention.Span); err != nil {
			return err
		}
	}
	for index, mention := range proposal.Understanding.DimensionMentions {
		if err := check(EvidenceOriginCurrent, fmt.Sprintf("dimensionMentions[%d]", index), mention.Span); err != nil {
			return err
		}
	}
	for index, mention := range proposal.Understanding.ValueMentions {
		if err := check(EvidenceOriginCurrent, fmt.Sprintf("valueMentions[%d]", index), mention.Span); err != nil {
			return err
		}
	}
	if proposal.Understanding.Time != nil {
		if err := check(EvidenceOriginCurrent, "time", proposal.Understanding.Time.Span); err != nil {
			return err
		}
	}
	for index, comparison := range proposal.Understanding.Comparisons {
		if err := check(EvidenceOriginCurrent, fmt.Sprintf("comparisons[%d]", index), comparison.Span); err != nil {
			return err
		}
	}
	for index, ordering := range proposal.Understanding.Ordering {
		if err := check(EvidenceOriginCurrent, fmt.Sprintf("ordering[%d]", index), ordering.Span); err != nil {
			return err
		}
	}
	for index, unresolved := range proposal.Understanding.UnresolvedSpans {
		if err := check(EvidenceOriginCurrent, fmt.Sprintf("unresolvedSpans[%d]", index), unresolved.Span); err != nil {
			return err
		}
	}
	for index, conflict := range proposal.Conflicts {
		if err := check(EvidenceOriginCurrent, fmt.Sprintf("conflicts[%d]", index), conflict.Span); err != nil {
			return err
		}
	}
	for index, evidenceRequest := range proposal.EvidenceRequests {
		if err := check(evidenceRequest.Origin, fmt.Sprintf("evidenceRequests[%d]", index), evidenceRequest.Span); err != nil {
			return err
		}
	}
	return nil
}

func modelVisibleProposalStrings(proposal UnderstandingProposal) []string {
	values := []string{proposal.Understanding.Question}
	for _, mention := range proposal.Understanding.MetricMentions {
		values = append(values, mention.Text)
	}
	for _, mention := range proposal.Understanding.DimensionMentions {
		values = append(values, mention.Text)
	}
	for _, mention := range proposal.Understanding.ValueMentions {
		values = append(values, mention.Text)
		if mention.DimensionHint != nil {
			values = append(values, *mention.DimensionHint)
		}
	}
	if proposal.Understanding.Time != nil {
		values = append(values, proposal.Understanding.Time.Text)
	}
	for _, comparison := range proposal.Understanding.Comparisons {
		values = append(values, comparison.Text)
		if comparison.TargetText != nil {
			values = append(values, *comparison.TargetText)
		}
	}
	for _, ordering := range proposal.Understanding.Ordering {
		values = append(values, ordering.Text, ordering.TargetText)
	}
	for _, unresolved := range proposal.Understanding.UnresolvedSpans {
		values = append(values, unresolved.Text, unresolved.Reason)
	}
	for _, conflict := range proposal.Conflicts {
		values = append(values, conflict.Text, conflict.Summary)
	}
	for _, evidenceRequest := range proposal.EvidenceRequests {
		values = append(values, evidenceRequest.Text, evidenceRequest.Reason)
	}
	return values
}

func (request UnderstandingRequest) validate() error {
	if err := request.ContextRequest.validate(); err != nil {
		return fmt.Errorf("%w: context request: %v", ErrInvalidUnderstandingRequest, err)
	}
	if _, err := selectedDomainFromScope(request.ContextRequest.Scope); err != nil {
		return fmt.Errorf("%w: selected domain", ErrInvalidUnderstandingRequest)
	}
	if err := request.Context.Validate(request.ContextRequest); err != nil {
		return fmt.Errorf("%w: context: %v", ErrInvalidUnderstandingRequest, err)
	}
	if request.ExactMatches == nil || len(request.ExactMatches) > 128 ||
		request.SensitiveMemberMatches == nil || len(request.SensitiveMemberMatches) > 128 {
		return fmt.Errorf("%w: exact matches", ErrInvalidUnderstandingRequest)
	}
	sensitiveBindings, err := bindSensitiveMemberMatches(request)
	if err != nil {
		return err
	}
	if err := validateSensitiveAuthoritativeSpanConflicts(request, sensitiveBindings); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for index, match := range request.ExactMatches {
		if validateExactMatchBase(request.ContextRequest.Question.Original, match) != nil ||
			match.ObjectType == search.ObjectMember {
			return fmt.Errorf("%w: exactMatches[%d]", ErrInvalidUnderstandingRequest, index)
		}
		identity := fmt.Sprintf("%s\x00%d\x00%d\x00%s", match.ObjectType, match.Span.Start, match.Span.End, match.Evidence.SourceID)
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%w: exactMatches[%d] is duplicated", ErrInvalidUnderstandingRequest, index)
		}
		seen[identity] = struct{}{}
		for _, sensitive := range sensitiveBindings {
			if sensitive.Origin == EvidenceOriginCurrent && spansOverlap(match.Span, sensitive.Span) {
				return fmt.Errorf("%w: exactMatches[%d] overlaps a sensitive member", ErrInvalidUnderstandingRequest, index)
			}
		}
	}
	return nil
}

func validateSensitiveAuthoritativeSpanConflicts(
	request UnderstandingRequest, bindings []sensitiveMemberBinding,
) error {
	authoritative := []Span{}
	rules := request.ContextRequest.Rules
	if rules.Time != nil {
		authoritative = append(authoritative, rules.Time.Span)
	}
	for _, comparison := range rules.Comparisons {
		authoritative = append(authoritative, comparison.Span)
	}
	if rules.Ranking != nil {
		authoritative = append(authoritative, rules.Ranking.Span)
	}
	for _, rule := range rules.Sorts {
		authoritative = append(authoritative, rule.Span)
	}
	for _, rule := range rules.Groupings {
		authoritative = append(authoritative, rule.Span)
	}
	for _, unresolved := range rules.UnresolvedSpans {
		authoritative = append(authoritative, unresolved.Span)
	}
	for _, unresolved := range request.Context.UnresolvedSpans {
		authoritative = append(authoritative, unresolved.Span)
	}
	for _, decision := range request.Context.Decisions {
		if decision.TriggerSpan != nil {
			authoritative = append(authoritative, *decision.TriggerSpan)
		}
	}
	for _, binding := range bindings {
		if binding.Origin != EvidenceOriginCurrent {
			continue
		}
		for _, span := range authoritative {
			if spansOverlap(binding.Span, span) {
				return fmt.Errorf("%w: sensitive member overlaps authoritative rule or context span", ErrInvalidUnderstandingRequest)
			}
		}
	}
	return nil
}

func validateExactMatchBase(question string, match ExactMatch) error {
	if !validExactMatchObjectType(match.ObjectType) || strings.TrimSpace(match.CanonicalLabel) == "" ||
		!utf8.ValidString(match.CanonicalLabel) || utf8.RuneCountInString(match.CanonicalLabel) > 512 ||
		validateMention(question, match.Text, match.Span) != nil ||
		match.Evidence.Validate() != nil || match.Evidence.Kind != askdata.EvidenceKindExactAlias {
		return ErrInvalidUnderstandingRequest
	}
	return nil
}

func (proposal UnderstandingProposal) Validate() error {
	if proposal.SchemaVersion != UnderstandingReviewSchemaVersion {
		return fmt.Errorf("%w: schemaVersion", ErrInvalidUnderstandingProposal)
	}
	if err := proposal.Understanding.Validate(); err != nil {
		return fmt.Errorf("%w: understanding: %v", ErrInvalidUnderstandingProposal, err)
	}
	if proposal.Understanding.DomainHypotheses == nil || proposal.Understanding.MetricMentions == nil ||
		proposal.Understanding.DimensionMentions == nil || proposal.Understanding.ValueMentions == nil ||
		proposal.Understanding.Comparisons == nil || proposal.Understanding.Ordering == nil ||
		proposal.Understanding.UnresolvedSpans == nil || proposal.Conflicts == nil ||
		proposal.EvidenceRequests == nil || proposal.EvidenceRefs == nil {
		return fmt.Errorf("%w: collections must be non-null", ErrInvalidUnderstandingProposal)
	}
	if len(proposal.Conflicts) > MaxUnderstandingConflicts || len(proposal.EvidenceRequests) > MaxUnderstandingEvidenceRequests ||
		len(proposal.EvidenceRefs) < 1 || len(proposal.EvidenceRefs) > MaxUnderstandingEvidenceRefs {
		return fmt.Errorf("%w: collection bounds", ErrInvalidUnderstandingProposal)
	}
	if err := validateUnderstandingEvidenceRefs(proposal.EvidenceRefs); err != nil {
		return fmt.Errorf("%w: evidenceRefs: %v", ErrInvalidUnderstandingProposal, err)
	}
	seenConflicts := map[string]struct{}{}
	for index, conflict := range proposal.Conflicts {
		if !askdataReasonCode(conflict.Code) || validateMention(proposal.Understanding.Question, conflict.Text, conflict.Span) != nil ||
			invalidModelSummary(conflict.Summary) || len(conflict.EvidenceRefs) < 1 ||
			len(conflict.EvidenceRefs) > MaxUnderstandingEvidenceRefs || validateUnderstandingEvidenceRefs(conflict.EvidenceRefs) != nil {
			return fmt.Errorf("%w: conflicts[%d]", ErrInvalidUnderstandingProposal, index)
		}
		identity := fmt.Sprintf("%s\x00%d\x00%d", conflict.Code, conflict.Span.Start, conflict.Span.End)
		if _, duplicate := seenConflicts[identity]; duplicate {
			return fmt.Errorf("%w: conflicts[%d] is duplicated", ErrInvalidUnderstandingProposal, index)
		}
		seenConflicts[identity] = struct{}{}
	}
	seenRequests := map[string]struct{}{}
	for index, request := range proposal.EvidenceRequests {
		if request.Origin != EvidenceOriginCurrent && request.Origin != EvidenceOriginInherited ||
			!validNeededEvidence(request.NeededEvidence) || invalidModelSummary(request.Reason) ||
			len(request.EvidenceRefs) < 1 || len(request.EvidenceRefs) > MaxUnderstandingEvidenceRefs ||
			validateUnderstandingEvidenceRefs(request.EvidenceRefs) != nil {
			return fmt.Errorf("%w: evidenceRequests[%d]", ErrInvalidUnderstandingProposal, index)
		}
		identity := fmt.Sprintf("%s\x00%s\x00%d\x00%d", request.Origin, request.NeededEvidence, request.Span.Start, request.Span.End)
		if _, duplicate := seenRequests[identity]; duplicate {
			return fmt.Errorf("%w: evidenceRequests[%d] is duplicated", ErrInvalidUnderstandingProposal, index)
		}
		seenRequests[identity] = struct{}{}
	}
	for index, hypothesis := range proposal.Understanding.DomainHypotheses {
		if err := validateUnderstandingEvidenceRefs(hypothesis.EvidenceRefs); err != nil {
			return fmt.Errorf("%w: domainHypotheses[%d].evidenceRefs", ErrInvalidUnderstandingProposal, index)
		}
	}
	for _, value := range modelAuthoredUnderstandingText(proposal.Understanding) {
		if physicalQueryTextPattern.MatchString(value) {
			return fmt.Errorf("%w: physical query content is forbidden", ErrInvalidUnderstandingProposal)
		}
	}
	return nil
}

func (proposal UnderstandingProposal) validateAgainst(request UnderstandingRequest, allowed []askdata.EvidenceRef) error {
	if err := proposal.Validate(); err != nil {
		return err
	}
	if proposal.Understanding.Question != request.ContextRequest.Question.Original {
		return fmt.Errorf("%w: question mismatch", ErrInvalidUnderstandingProposal)
	}
	bindings, err := bindSensitiveMemberMatches(request)
	if err != nil {
		return err
	}
	if len(bindings) != 0 {
		views, viewErr := sensitiveQuestionViews(request, bindings)
		if viewErr != nil {
			return viewErr
		}
		if err := rejectSensitiveProposalSpans(proposal, views); err != nil {
			return err
		}
	}
	allowedSet := make(map[askdata.EvidenceRef]struct{}, len(allowed))
	for _, ref := range allowed {
		allowedSet[ref] = struct{}{}
	}
	if !containsEvidenceKind(proposal.EvidenceRefs, askdata.EvidenceKindConversation) ||
		!containsEvidenceKind(proposal.EvidenceRefs, askdata.EvidenceKindRule) ||
		!containsEvidenceKind(proposal.EvidenceRefs, askdata.EvidenceKindPolicy) {
		return fmt.Errorf("%w: conversation, rule and policy evidence are required", ErrInvalidUnderstandingProposal)
	}
	if err := validateCitations(proposal.EvidenceRefs, proposal.EvidenceRefs, allowedSet); err != nil {
		return err
	}
	for _, hypothesis := range proposal.Understanding.DomainHypotheses {
		if err := validateCitations(hypothesis.EvidenceRefs, proposal.EvidenceRefs, allowedSet); err != nil {
			return err
		}
	}
	if err := validatePinnedSelectedDomain(proposal.Understanding, request.ContextRequest.Scope); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidUnderstandingProposal, err)
	}
	for _, conflict := range proposal.Conflicts {
		if err := validateCitations(conflict.EvidenceRefs, proposal.EvidenceRefs, allowedSet); err != nil {
			return err
		}
	}
	for index, evidenceRequest := range proposal.EvidenceRequests {
		question := proposal.Understanding.Question
		if evidenceRequest.Origin == EvidenceOriginInherited {
			if request.Context.Inherited == nil {
				return fmt.Errorf("%w: evidenceRequests[%d] has no inherited source", ErrInvalidUnderstandingProposal, index)
			}
			question = request.Context.Inherited.Question
		}
		if err := validateMention(question, evidenceRequest.Text, evidenceRequest.Span); err != nil {
			return fmt.Errorf("%w: evidenceRequests[%d] span: %v", ErrInvalidUnderstandingProposal, index, err)
		}
		if err := validateCitations(evidenceRequest.EvidenceRefs, proposal.EvidenceRefs, allowedSet); err != nil {
			return err
		}
	}
	if err := validateAuthoritativeUnderstanding(proposal, request); err != nil {
		return err
	}
	if err := validateConflictCoverage(proposal); err != nil {
		return err
	}
	if err := validateEvidenceRequestCoverage(proposal, request.Context.Inherited); err != nil {
		return err
	}
	return nil
}

func (result UnderstandingResult) Validate(request UnderstandingRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	if result.SchemaVersion != UnderstandingResultSchemaVersion || !reflect.DeepEqual(result.Context, request.Context) {
		return errors.New("understanding result schema or context mismatch")
	}
	input, err := BuildUnderstandingReviewInput(request)
	if err != nil {
		return err
	}
	proposal := normalizeUnderstandingProposal(UnderstandingProposal{
		SchemaVersion: UnderstandingReviewSchemaVersion, Understanding: result.Current,
		Conflicts: result.Conflicts, EvidenceRequests: result.EvidenceRequests, EvidenceRefs: result.EvidenceRefs,
	})
	if err := proposal.validateAgainst(request, input.AllowedEvidenceRefs); err != nil {
		return err
	}
	payload, err := json.Marshal(proposal)
	if err != nil {
		return err
	}
	if result.ProposalHash != askdata.HashBytes(payload) {
		return errors.New("proposalHash does not match the accepted proposal")
	}
	resultPayload, err := understandingResultPayload(result)
	if err != nil {
		return err
	}
	if result.ResultHash != askdata.HashBytes(resultPayload) {
		return errors.New("resultHash does not match the understanding result")
	}
	return nil
}

func validateAuthoritativeUnderstanding(proposal UnderstandingProposal, request UnderstandingRequest) error {
	understanding := proposal.Understanding
	rules := request.ContextRequest.Rules
	if rules.Time != nil {
		expected := rules.Time.Understanding()
		if understanding.Time == nil || !reflect.DeepEqual(*understanding.Time, expected) {
			return fmt.Errorf("%w: deterministic time evidence was changed or omitted", ErrInvalidUnderstandingProposal)
		}
	}
	for _, expected := range rules.Comparisons {
		matched := false
		for _, actual := range understanding.Comparisons {
			if actual.Span == expected.Span {
				if actual.Text != expected.Text || actual.Type != expected.Type {
					return fmt.Errorf("%w: deterministic comparison evidence was changed", ErrInvalidUnderstandingProposal)
				}
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("%w: deterministic comparison evidence was omitted", ErrInvalidUnderstandingProposal)
		}
	}
	if rules.Ranking != nil {
		if understanding.Limit == nil || *understanding.Limit != rules.Ranking.Limit ||
			!hasOrderingRule(
				understanding.Ordering, rules.Ranking.Text, rules.Ranking.Span,
				rules.Ranking.Direction, rules.Ranking.RankBy,
			) {
			return fmt.Errorf("%w: deterministic ranking evidence was changed or omitted", ErrInvalidUnderstandingProposal)
		}
	}
	for _, rule := range rules.Sorts {
		if !hasOrderingRule(understanding.Ordering, rule.Text, rule.Span, rule.Direction, "") {
			return fmt.Errorf("%w: deterministic sort evidence was changed or omitted", ErrInvalidUnderstandingProposal)
		}
	}
	for _, rule := range rules.Groupings {
		if !hasGroupingRole(understanding.DimensionMentions, rule) &&
			!hasUnresolvedCoverage(understanding.UnresolvedSpans, rule.Span, NeededDimensionCandidates) {
			return fmt.Errorf("%w: deterministic grouping evidence has no GROUP_BY role or unresolved evidence request", ErrInvalidUnderstandingProposal)
		}
	}
	authoritative := append([]UnresolvedSpan(nil), rules.UnresolvedSpans...)
	authoritative = append(authoritative, request.Context.UnresolvedSpans...)
	for _, unresolved := range authoritative {
		if !containsUnresolved(understanding.UnresolvedSpans, unresolved) {
			return fmt.Errorf("%w: authoritative unresolved span %q was omitted", ErrInvalidUnderstandingProposal, unresolved.Reason)
		}
		if !containsConflict(proposal.Conflicts, unresolved.Reason, unresolved.Span) {
			return fmt.Errorf("%w: authoritative conflict %q was omitted", ErrInvalidUnderstandingProposal, unresolved.Reason)
		}
	}
	return nil
}

func validateConflictCoverage(proposal UnderstandingProposal) error {
	for _, conflict := range proposal.Conflicts {
		found := false
		for _, unresolved := range proposal.Understanding.UnresolvedSpans {
			if unresolved.Span == conflict.Span && unresolved.Text == conflict.Text {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: conflict %q has no unresolved span", ErrInvalidUnderstandingProposal, conflict.Code)
		}
	}
	for _, unresolved := range proposal.Understanding.UnresolvedSpans {
		found := false
		for _, conflict := range proposal.Conflicts {
			if conflict.Span == unresolved.Span && conflict.Text == unresolved.Text {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: unresolved span has no conflict", ErrInvalidUnderstandingProposal)
		}
	}
	return nil
}

func validateEvidenceRequestCoverage(proposal UnderstandingProposal, inherited *QuestionUnderstanding) error {
	for _, mention := range proposal.Understanding.MetricMentions {
		if !hasEvidenceRequest(proposal.EvidenceRequests, EvidenceOriginCurrent, NeededMetricCandidates, mention.Text, mention.Span) {
			return fmt.Errorf("%w: current metric mention has no candidate request", ErrInvalidUnderstandingProposal)
		}
	}
	for _, mention := range proposal.Understanding.DimensionMentions {
		if !hasEvidenceRequest(proposal.EvidenceRequests, EvidenceOriginCurrent, NeededDimensionCandidates, mention.Text, mention.Span) {
			return fmt.Errorf("%w: current dimension mention has no candidate request", ErrInvalidUnderstandingProposal)
		}
	}
	for _, mention := range proposal.Understanding.ValueMentions {
		if !hasEvidenceRequest(proposal.EvidenceRequests, EvidenceOriginCurrent, NeededMemberCandidates, mention.Text, mention.Span) {
			return fmt.Errorf("%w: current value mention has no candidate request", ErrInvalidUnderstandingProposal)
		}
	}
	for _, unresolved := range proposal.Understanding.UnresolvedSpans {
		for _, needed := range unresolved.NeededEvidence {
			if !hasEvidenceRequest(proposal.EvidenceRequests, EvidenceOriginCurrent, needed, unresolved.Text, unresolved.Span) {
				return fmt.Errorf("%w: unresolved span has no %s request", ErrInvalidUnderstandingProposal, needed)
			}
		}
	}
	if inherited == nil {
		return nil
	}
	for _, mention := range inherited.MetricMentions {
		if !hasEvidenceRequest(proposal.EvidenceRequests, EvidenceOriginInherited, NeededMetricCandidates, mention.Text, mention.Span) {
			return fmt.Errorf("%w: inherited metric mention has no candidate request", ErrInvalidUnderstandingProposal)
		}
	}
	for _, mention := range inherited.DimensionMentions {
		if !hasEvidenceRequest(proposal.EvidenceRequests, EvidenceOriginInherited, NeededDimensionCandidates, mention.Text, mention.Span) {
			return fmt.Errorf("%w: inherited dimension mention has no candidate request", ErrInvalidUnderstandingProposal)
		}
	}
	for _, mention := range inherited.ValueMentions {
		if !hasEvidenceRequest(proposal.EvidenceRequests, EvidenceOriginInherited, NeededMemberCandidates, mention.Text, mention.Span) {
			return fmt.Errorf("%w: inherited value mention has no candidate request", ErrInvalidUnderstandingProposal)
		}
	}
	return nil
}

func validateCitations(values, proposalRefs []askdata.EvidenceRef, allowed map[askdata.EvidenceRef]struct{}) error {
	proposalSet := make(map[askdata.EvidenceRef]struct{}, len(proposalRefs))
	for _, ref := range proposalRefs {
		proposalSet[ref] = struct{}{}
	}
	for _, ref := range values {
		if _, ok := allowed[ref]; !ok {
			return fmt.Errorf("%w: invented or stale evidence reference", ErrInvalidUnderstandingProposal)
		}
		if _, ok := proposalSet[ref]; !ok {
			return fmt.Errorf("%w: nested evidence is absent from proposal evidenceRefs", ErrInvalidUnderstandingProposal)
		}
	}
	return nil
}

func residualSpans(
	question string,
	rules RuleParseResult,
	context ContextMergeResult,
	matches []ExactMatch,
	sensitiveSpans []Span,
) []ResidualSpan {
	runes := []rune(question)
	covered := make([]bool, len(runes))
	mark := func(span Span) {
		if span.Start < 0 || span.End > len(covered) || span.End <= span.Start {
			return
		}
		for index := span.Start; index < span.End; index++ {
			covered[index] = true
		}
	}
	for _, match := range matches {
		mark(match.Span)
	}
	for _, span := range sensitiveSpans {
		mark(span)
	}
	if rules.Time != nil {
		mark(rules.Time.Span)
	}
	for _, comparison := range rules.Comparisons {
		mark(comparison.Span)
	}
	if rules.Ranking != nil {
		mark(rules.Ranking.Span)
	}
	for _, rule := range rules.Sorts {
		mark(rule.Span)
	}
	for _, rule := range rules.Groupings {
		mark(rule.Span)
	}
	for _, decision := range context.Decisions {
		if decision.TriggerSpan != nil {
			mark(*decision.TriggerSpan)
		}
	}
	result := []ResidualSpan{}
	for index := 0; index < len(runes); {
		for index < len(runes) && (covered[index] || !residualRune(runes[index])) {
			index++
		}
		start := index
		for index < len(runes) && !covered[index] && residualRune(runes[index]) {
			index++
		}
		if start < index {
			result = append(result, ResidualSpan{Text: string(runes[start:index]), Span: Span{Start: start, End: index}})
		}
	}
	return result
}

func residualRune(value rune) bool {
	return !unicode.IsSpace(value) && !unicode.IsPunct(value) && !unicode.IsSymbol(value) && !unicode.IsControl(value)
}

func hasOrderingRule(
	values []OrderingMention,
	text string,
	span Span,
	direction SortDirection,
	rankBy RankBy,
) bool {
	for _, value := range values {
		if value.Span == span {
			return value.Text == text && value.Direction == direction &&
				(rankBy == "" || value.RankBy == rankBy)
		}
	}
	return false
}

func hasGroupingRole(values []DimensionMention, rule GroupingRule) bool {
	for _, value := range values {
		near := spansOverlap(value.Span, rule.Span) || value.Span.Start >= rule.Span.End && value.Span.Start-rule.Span.End <= 4
		if !near || value.Role != DimensionRoleGroupBy {
			continue
		}
		if rule.Grain == nil || value.Grain != nil && *value.Grain == *rule.Grain {
			return true
		}
	}
	return false
}

func hasUnresolvedCoverage(values []UnresolvedSpan, span Span, needed NeededEvidence) bool {
	for _, value := range values {
		if spansOverlap(value.Span, span) {
			for _, item := range value.NeededEvidence {
				if item == needed {
					return true
				}
			}
		}
	}
	return false
}

func containsUnresolved(values []UnresolvedSpan, expected UnresolvedSpan) bool {
	for _, value := range values {
		if reflect.DeepEqual(value, expected) {
			return true
		}
	}
	return false
}

func containsConflict(values []UnderstandingConflict, code string, span Span) bool {
	for _, value := range values {
		if value.Code == code && value.Span == span {
			return true
		}
	}
	return false
}

func hasEvidenceRequest(values []UnderstandingEvidenceRequest, origin EvidenceOrigin, needed NeededEvidence, text string, span Span) bool {
	for _, value := range values {
		if value.Origin == origin && value.NeededEvidence == needed && value.Text == text && value.Span == span {
			return true
		}
	}
	return false
}

func spansOverlap(left, right Span) bool { return left.Start < right.End && right.Start < left.End }

func validExactMatchObjectType(value search.ObjectType) bool {
	switch value {
	case search.ObjectMetric, search.ObjectDimension, search.ObjectMember, search.ObjectBusinessTerm:
		return true
	default:
		return false
	}
}

func containsEvidenceKind(values []askdata.EvidenceRef, kind askdata.EvidenceKind) bool {
	for _, value := range values {
		if value.Kind == kind {
			return true
		}
	}
	return false
}

func askdataReasonCode(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func invalidModelSummary(value string) bool {
	return strings.TrimSpace(value) == "" || !utf8.ValidString(value) ||
		utf8.RuneCountInString(value) > MaxUnderstandingSummaryRunes || physicalQueryTextPattern.MatchString(value)
}

func modelAuthoredUnderstandingText(value QuestionUnderstanding) []string {
	result := []string{}
	for _, mention := range value.ValueMentions {
		if mention.DimensionHint != nil {
			result = append(result, *mention.DimensionHint)
		}
	}
	for _, comparison := range value.Comparisons {
		if comparison.TargetText != nil {
			result = append(result, *comparison.TargetText)
		}
	}
	for _, ordering := range value.Ordering {
		result = append(result, ordering.TargetText)
	}
	for _, unresolved := range value.UnresolvedSpans {
		result = append(result, unresolved.Reason)
	}
	return result
}

func validateUnderstandingEvidenceRefs(values []askdata.EvidenceRef) error {
	previous := askdata.EvidenceRef{}
	seen := map[askdata.EvidenceRef]struct{}{}
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("evidenceRefs[%d]: %w", index, err)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("evidenceRefs[%d] is duplicated", index)
		}
		if index > 0 && !understandingEvidenceRefLess(previous, value) {
			return errors.New("evidenceRefs must be sorted")
		}
		seen[value] = struct{}{}
		previous = value
	}
	return nil
}

func normalizedUnderstandingEvidenceRefs(values []askdata.EvidenceRef) []askdata.EvidenceRef {
	seen := map[askdata.EvidenceRef]struct{}{}
	result := make([]askdata.EvidenceRef, 0, len(values))
	for _, value := range values {
		if _, duplicate := seen[value]; !duplicate {
			seen[value] = struct{}{}
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return understandingEvidenceRefLess(result[i], result[j]) })
	return result
}

func understandingEvidenceRefLess(left, right askdata.EvidenceRef) bool {
	if left.EvidenceID != right.EvidenceID {
		return left.EvidenceID < right.EvidenceID
	}
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	if left.SourceID != right.SourceID {
		return left.SourceID < right.SourceID
	}
	return left.ContentHash < right.ContentHash
}

func normalizeUnderstandingProposal(proposal UnderstandingProposal) UnderstandingProposal {
	proposal.Conflicts = cloneUnderstandingConflicts(proposal.Conflicts)
	proposal.EvidenceRequests = cloneUnderstandingEvidenceRequests(proposal.EvidenceRequests)
	proposal.EvidenceRefs = normalizedUnderstandingEvidenceRefs(proposal.EvidenceRefs)
	for index := range proposal.Understanding.DomainHypotheses {
		proposal.Understanding.DomainHypotheses[index].EvidenceRefs = normalizedUnderstandingEvidenceRefs(proposal.Understanding.DomainHypotheses[index].EvidenceRefs)
	}
	for index := range proposal.Conflicts {
		proposal.Conflicts[index].EvidenceRefs = normalizedUnderstandingEvidenceRefs(proposal.Conflicts[index].EvidenceRefs)
	}
	for index := range proposal.EvidenceRequests {
		proposal.EvidenceRequests[index].EvidenceRefs = normalizedUnderstandingEvidenceRefs(proposal.EvidenceRequests[index].EvidenceRefs)
	}
	sort.Slice(proposal.Conflicts, func(i, j int) bool {
		left, right := proposal.Conflicts[i], proposal.Conflicts[j]
		if left.Span.Start != right.Span.Start {
			return left.Span.Start < right.Span.Start
		}
		if left.Span.End != right.Span.End {
			return left.Span.End < right.Span.End
		}
		return left.Code < right.Code
	})
	sort.Slice(proposal.EvidenceRequests, func(i, j int) bool {
		left, right := proposal.EvidenceRequests[i], proposal.EvidenceRequests[j]
		if left.Origin != right.Origin {
			return left.Origin < right.Origin
		}
		if left.Span.Start != right.Span.Start {
			return left.Span.Start < right.Span.Start
		}
		if left.Span.End != right.Span.End {
			return left.Span.End < right.Span.End
		}
		return left.NeededEvidence < right.NeededEvidence
	})
	return proposal
}

func cloneUnderstandingReviewInput(input UnderstandingReviewInput) UnderstandingReviewInput {
	result := UnderstandingReviewInput{Stage: input.Stage}
	result.Facts = make([]cognition.PromptFact, len(input.Facts))
	for index, fact := range input.Facts {
		result.Facts[index] = fact
		result.Facts[index].Payload = append(json.RawMessage(nil), fact.Payload...)
	}
	result.AllowedEvidenceRefs = append([]askdata.EvidenceRef(nil), input.AllowedEvidenceRefs...)
	return result
}

func cloneUnderstandingConflicts(values []UnderstandingConflict) []UnderstandingConflict {
	if values == nil {
		return nil
	}
	result := make([]UnderstandingConflict, len(values))
	copy(result, values)
	for index := range result {
		result[index].EvidenceRefs = append([]askdata.EvidenceRef(nil), result[index].EvidenceRefs...)
	}
	return result
}

func cloneUnderstandingEvidenceRequests(values []UnderstandingEvidenceRequest) []UnderstandingEvidenceRequest {
	if values == nil {
		return nil
	}
	result := make([]UnderstandingEvidenceRequest, len(values))
	copy(result, values)
	for index := range result {
		result[index].EvidenceRefs = append([]askdata.EvidenceRef(nil), result[index].EvidenceRefs...)
	}
	return result
}

func sortExactMatches(values []ExactMatch) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Span.Start != values[j].Span.Start {
			return values[i].Span.Start < values[j].Span.Start
		}
		if values[i].Span.End != values[j].Span.End {
			return values[i].Span.End > values[j].Span.End
		}
		if values[i].ObjectType != values[j].ObjectType {
			return values[i].ObjectType < values[j].ObjectType
		}
		return values[i].Evidence.SourceID < values[j].Evidence.SourceID
	})
}

func understandingResultPayload(result UnderstandingResult) ([]byte, error) {
	type resultWithoutHash UnderstandingResult
	payload := resultWithoutHash(result)
	payload.ResultHash = ""
	return json.Marshal(payload)
}
