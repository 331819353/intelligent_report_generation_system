package understanding

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	DictionaryMatchExact      = "EXACT"
	DictionaryMatchPrefix     = "PREFIX"
	DictionaryMatchSuffix     = "SUFFIX"
	DictionaryMatchRegexpSafe = "REGEX_SAFE"
	DictionaryMatchVector     = "VECTOR"

	DictionaryDropOutsideValidity = "OUTSIDE_VALIDITY"
	DictionaryDropRoleRestricted  = "ROLE_NOT_APPLICABLE"
	DictionaryDropNegativeContext = "NEGATIVE_CONTEXT"
	DictionaryDropOverlap         = "OVERLAPPED_BY_LONGER_OR_HIGHER_PRIORITY_MATCH"
	DictionaryDropResidualCovered = "RESIDUAL_SPAN_ALREADY_COVERED"
	DictionaryDropRegexpTimeout   = "REGEX_SAFE_TIMEOUT"

	DefaultNegativeContextWindow = 32
	MaxNegativeContextWindow     = 256
	MaxDictionaryTerms           = 20_000
	MaxDictionaryMatches         = 4_096
	dictionaryRegexpTimeout      = 10 * time.Millisecond
)

var (
	ErrInvalidDictionaryRequest = errors.New("invalid governed dictionary request")
	ErrInvalidDictionaryData    = errors.New("invalid governed dictionary data")
)

type DictionaryTerm struct {
	TenantID          askdata.ID
	DomainID          askdata.ID
	TermVersionID     askdata.ID
	TargetVersionID   askdata.ID
	TargetObjectType  string
	TargetCode        string
	Term              string
	TermType          string
	MatchMode         string
	MatchPattern      string
	Priority          int
	NegativeContexts  []string
	ApplicableRoleIDs []askdata.ID
	ValidFrom         *time.Time
	ValidTo           *time.Time
	ContentHash       askdata.ContentHash
}

type DictionarySnapshot struct {
	TenantID askdata.ID
	DomainID askdata.ID
	Release  askdata.ReleaseRef
	Terms    []DictionaryTerm
}

type DictionaryLoader interface {
	LoadDictionary(context.Context, askdata.PolicyScope, askdata.ID) (DictionarySnapshot, error)
}

type DictionaryMatchRequest struct {
	Scope                 askdata.PolicyScope
	Question              string
	Now                   time.Time
	NegativeContextWindow int
}

type DictionaryHit struct {
	DomainID         askdata.ID          `json:"domainId"`
	OriginalSpan     Span                `json:"originalSpan"`
	NormalizedSpan   Span                `json:"normalizedSpan"`
	OriginalText     string              `json:"originalText"`
	NormalizedText   string              `json:"normalizedText"`
	Term             string              `json:"term"`
	TermVersionID    askdata.ID          `json:"termVersionId"`
	TargetVersionID  askdata.ID          `json:"targetVersionId"`
	TargetObjectType string              `json:"targetObjectType"`
	TargetCode       string              `json:"targetCode"`
	MatchMode        string              `json:"matchMode"`
	Priority         int                 `json:"priority"`
	EvidenceHash     askdata.ContentHash `json:"evidenceHash"`
}

type DictionaryDrop struct {
	DomainID       askdata.ID `json:"domainId"`
	NormalizedSpan Span       `json:"normalizedSpan"`
	TermVersionID  askdata.ID `json:"termVersionId"`
	Reason         string     `json:"reason"`
}

type DictionaryMatchResult struct {
	Normalization NormalizedQuestion `json:"normalization"`
	Hits          []DictionaryHit    `json:"hits"`
	Dropped       []DictionaryDrop   `json:"dropped"`
}

type DictionaryMatcher struct {
	loader DictionaryLoader
	cache  *DictionaryCache
}

func NewDictionaryMatcher(loader DictionaryLoader, cache *DictionaryCache) (*DictionaryMatcher, error) {
	if loader == nil {
		return nil, ErrInvalidDictionaryRequest
	}
	if cache == nil {
		cache = NewDictionaryCache()
	}
	return &DictionaryMatcher{loader: loader, cache: cache}, nil
}

type dictionaryPattern struct {
	term       DictionaryTerm
	normalized string
	runes      []rune
	negatives  [][]rune
	roles      map[askdata.ID]struct{}
	regexp     *regexp.Regexp
}

type ahoNode struct {
	next    map[rune]int
	failure int
	outputs []int
}

type dictionaryIndex struct {
	exact   []dictionaryPattern
	special []dictionaryPattern
	nodes   []ahoNode
}

type dictionaryCandidate struct {
	pattern dictionaryPattern
	span    Span
}

func (matcher *DictionaryMatcher) Match(
	ctx context.Context,
	request DictionaryMatchRequest,
) (DictionaryMatchResult, error) {
	if matcher == nil || matcher.loader == nil || matcher.cache == nil || ctx == nil ||
		request.Scope.Validate() != nil || strings.TrimSpace(request.Question) == "" {
		return DictionaryMatchResult{}, ErrInvalidDictionaryRequest
	}
	window := request.NegativeContextWindow
	if window == 0 {
		window = DefaultNegativeContextWindow
	}
	if window < 1 || window > MaxNegativeContextWindow {
		return DictionaryMatchResult{}, ErrInvalidDictionaryRequest
	}
	now := request.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	normalized, err := NormalizeQuestion(request.Question)
	if err != nil {
		return DictionaryMatchResult{}, err
	}
	result := DictionaryMatchResult{
		Normalization: normalized, Hits: []DictionaryHit{}, Dropped: []DictionaryDrop{},
	}
	for _, domainID := range request.Scope.DomainIDs {
		if err := ctx.Err(); err != nil {
			return DictionaryMatchResult{}, err
		}
		index, err := matcher.indexFor(ctx, request.Scope, domainID)
		if err != nil {
			return DictionaryMatchResult{}, err
		}
		hits, dropped, err := index.match(ctx, normalized, request.Scope.RoleIDs, now, window)
		if err != nil {
			return DictionaryMatchResult{}, err
		}
		result.Hits = append(result.Hits, hits...)
		result.Dropped = append(result.Dropped, dropped...)
	}
	sortDictionaryHits(result.Hits)
	sortDictionaryDrops(result.Dropped)
	if len(result.Hits)+len(result.Dropped) > MaxDictionaryMatches {
		return DictionaryMatchResult{}, ErrInvalidDictionaryData
	}
	return result, nil
}

func (matcher *DictionaryMatcher) indexFor(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
) (*dictionaryIndex, error) {
	if index, exists := matcher.cache.get(scope.TenantID, domainID, scope.Release); exists {
		return index, nil
	}
	snapshot, err := matcher.loader.LoadDictionary(ctx, scope, domainID)
	if err != nil {
		return nil, err
	}
	if snapshot.TenantID != scope.TenantID || snapshot.DomainID != domainID ||
		snapshot.Release != scope.Release || len(snapshot.Terms) > MaxDictionaryTerms {
		return nil, ErrInvalidDictionaryData
	}
	index, err := buildDictionaryIndex(snapshot)
	if err != nil {
		return nil, err
	}
	matcher.cache.put(scope.TenantID, domainID, scope.Release, index)
	return index, nil
}

func buildDictionaryIndex(snapshot DictionarySnapshot) (*dictionaryIndex, error) {
	terms := append([]DictionaryTerm(nil), snapshot.Terms...)
	sort.Slice(terms, func(left, right int) bool {
		if terms[left].Term != terms[right].Term {
			return terms[left].Term < terms[right].Term
		}
		return terms[left].TermVersionID < terms[right].TermVersionID
	})
	index := &dictionaryIndex{nodes: []ahoNode{{next: map[rune]int{}}}}
	seen := map[askdata.ID]struct{}{}
	for _, term := range terms {
		if _, duplicate := seen[term.TermVersionID]; duplicate {
			return nil, ErrInvalidDictionaryData
		}
		seen[term.TermVersionID] = struct{}{}
		pattern, err := prepareDictionaryPattern(snapshot, term)
		if err != nil {
			return nil, err
		}
		switch term.MatchMode {
		case DictionaryMatchExact:
			patternIndex := len(index.exact)
			index.exact = append(index.exact, pattern)
			index.insert(pattern.runes, patternIndex)
		case DictionaryMatchPrefix, DictionaryMatchSuffix, DictionaryMatchRegexpSafe:
			index.special = append(index.special, pattern)
		case DictionaryMatchVector:
			// VECTOR terms intentionally remain outside deterministic matching.
		default:
			return nil, ErrInvalidDictionaryData
		}
	}
	index.buildFailures()
	return index, nil
}

func prepareDictionaryPattern(
	snapshot DictionarySnapshot,
	term DictionaryTerm,
) (dictionaryPattern, error) {
	if term.TenantID != snapshot.TenantID || term.DomainID != snapshot.DomainID ||
		term.TermVersionID.Validate() != nil || term.TargetVersionID.Validate() != nil ||
		term.ContentHash.Validate() != nil || term.Priority < 0 || term.Priority > 1000 ||
		!validDictionaryTargetType(term.TargetObjectType) || !validDictionaryTermType(term.TermType) ||
		!validDictionaryMatchMode(term.MatchMode) || strings.TrimSpace(term.TargetCode) != term.TargetCode ||
		len(term.TargetCode) > 512 || len(term.NegativeContexts) > 64 || len(term.ApplicableRoleIDs) > 64 ||
		term.ValidFrom != nil && term.ValidTo != nil && !term.ValidTo.After(*term.ValidFrom) {
		return dictionaryPattern{}, ErrInvalidDictionaryData
	}
	normalized, err := NormalizeQuestion(term.Term)
	if err != nil {
		return dictionaryPattern{}, ErrInvalidDictionaryData
	}
	pattern := dictionaryPattern{
		term: term, normalized: normalized.Normalized, runes: []rune(normalized.Normalized),
		roles: map[askdata.ID]struct{}{},
	}
	for _, roleID := range term.ApplicableRoleIDs {
		if roleID.Validate() != nil {
			return dictionaryPattern{}, ErrInvalidDictionaryData
		}
		if _, duplicate := pattern.roles[roleID]; duplicate {
			return dictionaryPattern{}, ErrInvalidDictionaryData
		}
		pattern.roles[roleID] = struct{}{}
	}
	for _, negative := range term.NegativeContexts {
		normalizedNegative, err := NormalizeQuestion(negative)
		if err != nil {
			return dictionaryPattern{}, ErrInvalidDictionaryData
		}
		pattern.negatives = append(pattern.negatives, []rune(normalizedNegative.Normalized))
	}
	if term.MatchMode == DictionaryMatchRegexpSafe {
		if term.MatchPattern == "" || len(term.MatchPattern) > 1024 {
			return dictionaryPattern{}, ErrInvalidDictionaryData
		}
		pattern.regexp, err = regexp.Compile(term.MatchPattern)
		if err != nil {
			return dictionaryPattern{}, ErrInvalidDictionaryData
		}
	}
	return pattern, nil
}

func (index *dictionaryIndex) insert(pattern []rune, output int) {
	state := 0
	for _, character := range pattern {
		next, exists := index.nodes[state].next[character]
		if !exists {
			next = len(index.nodes)
			index.nodes = append(index.nodes, ahoNode{next: map[rune]int{}})
			index.nodes[state].next[character] = next
		}
		state = next
	}
	index.nodes[state].outputs = append(index.nodes[state].outputs, output)
}

func (index *dictionaryIndex) buildFailures() {
	queue := []int{}
	for _, child := range index.nodes[0].next {
		queue = append(queue, child)
	}
	for len(queue) > 0 {
		state := queue[0]
		queue = queue[1:]
		for character, child := range index.nodes[state].next {
			queue = append(queue, child)
			failure := index.nodes[state].failure
			for failure != 0 {
				if target, exists := index.nodes[failure].next[character]; exists {
					failure = target
					break
				}
				failure = index.nodes[failure].failure
			}
			if failure == 0 {
				if target, exists := index.nodes[0].next[character]; exists && target != child {
					failure = target
				}
			}
			index.nodes[child].failure = failure
			index.nodes[child].outputs = append(
				index.nodes[child].outputs, index.nodes[failure].outputs...,
			)
		}
	}
}

func (index *dictionaryIndex) match(
	ctx context.Context,
	question NormalizedQuestion,
	roleIDs []askdata.ID,
	now time.Time,
	window int,
) ([]DictionaryHit, []DictionaryDrop, error) {
	questionRunes := []rune(question.Normalized)
	exact := index.exactCandidates(questionRunes)
	eligible, dropped := filterDictionaryCandidates(exact, questionRunes, roleIDs, now, window)
	selected, overlapDrops := resolveDictionaryOverlaps(eligible)
	dropped = append(dropped, overlapDrops...)
	coverage := make([]bool, len(questionRunes))
	for _, candidate := range selected {
		markDictionaryCoverage(coverage, candidate.span)
	}
	special, specialDrops, err := index.specialCandidates(ctx, question.Normalized, questionRunes, coverage)
	if err != nil {
		return nil, nil, err
	}
	dropped = append(dropped, specialDrops...)
	eligibleSpecial, policyDrops := filterDictionaryCandidates(special, questionRunes, roleIDs, now, window)
	dropped = append(dropped, policyDrops...)
	selectedSpecial, specialOverlapDrops := resolveDictionaryOverlaps(eligibleSpecial)
	dropped = append(dropped, specialOverlapDrops...)
	selected = append(selected, selectedSpecial...)
	hits := make([]DictionaryHit, 0, len(selected))
	for _, candidate := range selected {
		originalSpan, originalText, err := dictionaryOriginalProjection(question, candidate.span)
		if err != nil {
			return nil, nil, err
		}
		hits = append(hits, DictionaryHit{
			DomainID:     candidate.pattern.term.DomainID,
			OriginalSpan: originalSpan, NormalizedSpan: candidate.span,
			OriginalText:   originalText,
			NormalizedText: string(questionRunes[candidate.span.Start:candidate.span.End]),
			Term:           candidate.pattern.term.Term, TermVersionID: candidate.pattern.term.TermVersionID,
			TargetVersionID:  candidate.pattern.term.TargetVersionID,
			TargetObjectType: candidate.pattern.term.TargetObjectType,
			TargetCode:       candidate.pattern.term.TargetCode,
			MatchMode:        candidate.pattern.term.MatchMode, Priority: candidate.pattern.term.Priority,
			EvidenceHash: candidate.pattern.term.ContentHash,
		})
	}
	return hits, dropped, nil
}

func (index *dictionaryIndex) exactCandidates(question []rune) []dictionaryCandidate {
	if len(index.exact) == 0 || len(index.nodes) == 0 {
		return nil
	}
	state := 0
	result := []dictionaryCandidate{}
	for position, character := range question {
		for state != 0 {
			if _, exists := index.nodes[state].next[character]; exists {
				break
			}
			state = index.nodes[state].failure
		}
		if next, exists := index.nodes[state].next[character]; exists {
			state = next
		}
		for _, output := range index.nodes[state].outputs {
			pattern := index.exact[output]
			start := position + 1 - len(pattern.runes)
			if start >= 0 {
				result = append(result, dictionaryCandidate{pattern: pattern, span: Span{Start: start, End: position + 1}})
			}
		}
	}
	return result
}

func (index *dictionaryIndex) specialCandidates(
	ctx context.Context,
	questionText string,
	question []rune,
	coverage []bool,
) ([]dictionaryCandidate, []DictionaryDrop, error) {
	result := []dictionaryCandidate{}
	dropped := []DictionaryDrop{}
	for _, pattern := range index.special {
		var spans []Span
		var err error
		switch pattern.term.MatchMode {
		case DictionaryMatchPrefix, DictionaryMatchSuffix:
			spans = dictionaryLiteralSpans(question, pattern.runes, pattern.term.MatchMode)
		case DictionaryMatchRegexpSafe:
			spans, err = dictionaryRegexpSpans(ctx, pattern.regexp, questionText)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
				return nil, nil, err
			}
			if errors.Is(err, context.DeadlineExceeded) {
				dropped = append(dropped, DictionaryDrop{
					DomainID: pattern.term.DomainID, TermVersionID: pattern.term.TermVersionID,
					Reason: DictionaryDropRegexpTimeout,
				})
				continue
			}
			if err != nil {
				return nil, nil, err
			}
		}
		for _, span := range spans {
			if dictionaryOverlapsCoverage(coverage, span) {
				dropped = append(dropped, dictionaryDrop(pattern, span, DictionaryDropResidualCovered))
				continue
			}
			result = append(result, dictionaryCandidate{pattern: pattern, span: span})
		}
	}
	return result, dropped, nil
}

func dictionaryLiteralSpans(question, pattern []rune, mode string) []Span {
	if len(pattern) == 0 || len(pattern) > len(question) {
		return nil
	}
	result := []Span{}
	for start := 0; start+len(pattern) <= len(question); start++ {
		if !equalDictionaryRunes(question[start:start+len(pattern)], pattern) {
			continue
		}
		end := start + len(pattern)
		leftBoundary := start == 0 || dictionaryBoundary(question[start-1])
		rightBoundary := end == len(question) || dictionaryBoundary(question[end])
		prefixMatch := mode == DictionaryMatchPrefix && (leftBoundary || end < len(question) && !rightBoundary)
		suffixMatch := mode == DictionaryMatchSuffix && (rightBoundary || start > 0 && !leftBoundary)
		if prefixMatch || suffixMatch {
			result = append(result, Span{Start: start, End: end})
		}
	}
	return result
}

func dictionaryRegexpSpans(ctx context.Context, expression *regexp.Regexp, value string) ([]Span, error) {
	if expression == nil || ctx == nil || len(value) > MaxNormalizedQuestionRunes*4 {
		return nil, ErrInvalidDictionaryData
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	matchContext, cancel := context.WithTimeout(ctx, dictionaryRegexpTimeout)
	defer cancel()
	type regexpResult struct {
		indexes [][]int
	}
	channel := make(chan regexpResult, 1)
	go func() { channel <- regexpResult{indexes: expression.FindAllStringIndex(value, MaxDictionaryMatches)} }()
	select {
	case raw := <-channel:
		result := make([]Span, 0, len(raw.indexes))
		for _, indexes := range raw.indexes {
			start := utf8.RuneCountInString(value[:indexes[0]])
			end := start + utf8.RuneCountInString(value[indexes[0]:indexes[1]])
			if end > start {
				result = append(result, Span{Start: start, End: end})
			}
		}
		return result, nil
	case <-matchContext.Done():
		return nil, matchContext.Err()
	}
}

func filterDictionaryCandidates(
	candidates []dictionaryCandidate,
	question []rune,
	roleIDs []askdata.ID,
	now time.Time,
	window int,
) ([]dictionaryCandidate, []DictionaryDrop) {
	roles := make(map[askdata.ID]struct{}, len(roleIDs))
	for _, roleID := range roleIDs {
		roles[roleID] = struct{}{}
	}
	eligible := []dictionaryCandidate{}
	dropped := []DictionaryDrop{}
	for _, candidate := range candidates {
		reason := ""
		term := candidate.pattern.term
		if term.ValidFrom != nil && now.Before(*term.ValidFrom) || term.ValidTo != nil && !now.Before(*term.ValidTo) {
			reason = DictionaryDropOutsideValidity
		} else if len(candidate.pattern.roles) > 0 && !dictionaryRoleIntersects(candidate.pattern.roles, roles) {
			reason = DictionaryDropRoleRestricted
		} else if dictionaryHasNegativeContext(question, candidate.span, candidate.pattern.negatives, window) {
			reason = DictionaryDropNegativeContext
		}
		if reason != "" {
			dropped = append(dropped, dictionaryDrop(candidate.pattern, candidate.span, reason))
			continue
		}
		eligible = append(eligible, candidate)
	}
	return eligible, dropped
}

func resolveDictionaryOverlaps(candidates []dictionaryCandidate) ([]dictionaryCandidate, []DictionaryDrop) {
	sort.Slice(candidates, func(left, right int) bool {
		leftLength := candidates[left].span.End - candidates[left].span.Start
		rightLength := candidates[right].span.End - candidates[right].span.Start
		if leftLength != rightLength {
			return leftLength > rightLength
		}
		if candidates[left].pattern.term.Priority != candidates[right].pattern.term.Priority {
			return candidates[left].pattern.term.Priority > candidates[right].pattern.term.Priority
		}
		if candidates[left].pattern.normalized != candidates[right].pattern.normalized {
			return candidates[left].pattern.normalized < candidates[right].pattern.normalized
		}
		return candidates[left].pattern.term.TermVersionID < candidates[right].pattern.term.TermVersionID
	})
	selected := []dictionaryCandidate{}
	dropped := []DictionaryDrop{}
	for _, candidate := range candidates {
		overlapped := false
		for _, existing := range selected {
			if dictionarySpansOverlap(candidate.span, existing.span) {
				overlapped = true
				break
			}
		}
		if overlapped {
			dropped = append(dropped, dictionaryDrop(candidate.pattern, candidate.span, DictionaryDropOverlap))
			continue
		}
		selected = append(selected, candidate)
	}
	return selected, dropped
}

func dictionaryHasNegativeContext(question []rune, span Span, negatives [][]rune, window int) bool {
	start, end := max(0, span.Start-window), min(len(question), span.End+window)
	contextRunes := question[start:end]
	for _, negative := range negatives {
		if len(negative) > 0 && dictionaryContainsRunes(contextRunes, negative) {
			return true
		}
	}
	return false
}

func dictionaryOriginalProjection(question NormalizedQuestion, normalized Span) (Span, string, error) {
	if normalized.Start < 0 || normalized.End <= normalized.Start || normalized.End > len(question.origins) {
		return Span{}, "", ErrInvalidOffsetSpan
	}
	result := Span{Start: question.origins[normalized.Start].Start, End: question.origins[normalized.End-1].End}
	for _, origin := range question.origins[normalized.Start:normalized.End] {
		result.Start = min(result.Start, origin.Start)
		result.End = max(result.End, origin.End)
	}
	runes := []rune(question.Original)
	return result, string(runes[result.Start:result.End]), nil
}

func dictionaryDrop(pattern dictionaryPattern, span Span, reason string) DictionaryDrop {
	return DictionaryDrop{
		DomainID: pattern.term.DomainID, NormalizedSpan: span,
		TermVersionID: pattern.term.TermVersionID, Reason: reason,
	}
}

func dictionaryRoleIntersects(left, right map[askdata.ID]struct{}) bool {
	for roleID := range left {
		if _, exists := right[roleID]; exists {
			return true
		}
	}
	return false
}

func dictionaryBoundary(character rune) bool {
	return unicode.IsSpace(character) || unicode.IsPunct(character) || unicode.IsSymbol(character)
}

func dictionarySpansOverlap(left, right Span) bool {
	return left.Start < right.End && right.Start < left.End
}

func markDictionaryCoverage(coverage []bool, span Span) {
	for index := max(0, span.Start); index < min(len(coverage), span.End); index++ {
		coverage[index] = true
	}
}

func dictionaryOverlapsCoverage(coverage []bool, span Span) bool {
	for index := max(0, span.Start); index < min(len(coverage), span.End); index++ {
		if coverage[index] {
			return true
		}
	}
	return false
}

func equalDictionaryRunes(left, right []rune) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func dictionaryContainsRunes(haystack, needle []rune) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if equalDictionaryRunes(haystack[index:index+len(needle)], needle) {
			return true
		}
	}
	return false
}

func sortDictionaryHits(hits []DictionaryHit) {
	sort.Slice(hits, func(left, right int) bool {
		if hits[left].OriginalSpan.Start != hits[right].OriginalSpan.Start {
			return hits[left].OriginalSpan.Start < hits[right].OriginalSpan.Start
		}
		if hits[left].OriginalSpan.End != hits[right].OriginalSpan.End {
			return hits[left].OriginalSpan.End > hits[right].OriginalSpan.End
		}
		if hits[left].Priority != hits[right].Priority {
			return hits[left].Priority > hits[right].Priority
		}
		return hits[left].TermVersionID < hits[right].TermVersionID
	})
}

func sortDictionaryDrops(drops []DictionaryDrop) {
	sort.Slice(drops, func(left, right int) bool {
		if drops[left].NormalizedSpan.Start != drops[right].NormalizedSpan.Start {
			return drops[left].NormalizedSpan.Start < drops[right].NormalizedSpan.Start
		}
		if drops[left].Reason != drops[right].Reason {
			return drops[left].Reason < drops[right].Reason
		}
		return drops[left].TermVersionID < drops[right].TermVersionID
	})
}

func (hit DictionaryHit) Validate() error {
	if hit.DomainID.Validate() != nil || hit.TermVersionID.Validate() != nil ||
		hit.TargetVersionID.Validate() != nil || hit.EvidenceHash.Validate() != nil ||
		hit.OriginalSpan.End <= hit.OriginalSpan.Start || hit.NormalizedSpan.End <= hit.NormalizedSpan.Start ||
		strings.TrimSpace(hit.OriginalText) == "" || strings.TrimSpace(hit.NormalizedText) == "" ||
		strings.TrimSpace(hit.Term) == "" || len(hit.Term) > 512 ||
		strings.TrimSpace(hit.TargetCode) != hit.TargetCode || len(hit.TargetCode) > 512 ||
		!validDictionaryTargetType(hit.TargetObjectType) || !validDictionaryMatchMode(hit.MatchMode) ||
		hit.Priority < 0 || hit.Priority > 1000 {
		return fmt.Errorf("%w: dictionary hit", ErrInvalidDictionaryData)
	}
	return nil
}

func validDictionaryTargetType(value string) bool {
	switch value {
	case "METRIC", "DIMENSION", "MEMBER", "TIME_CONTRACT", "OPERATOR", "LEGACY":
		return true
	default:
		return false
	}
}

func validDictionaryTermType(value string) bool {
	switch value {
	case "METRIC", "DIMENSION", "MEMBER", "TIME", "OPERATOR":
		return true
	default:
		return false
	}
}

func validDictionaryMatchMode(value string) bool {
	switch value {
	case DictionaryMatchExact, DictionaryMatchPrefix, DictionaryMatchSuffix,
		DictionaryMatchRegexpSafe, DictionaryMatchVector:
		return true
	default:
		return false
	}
}
