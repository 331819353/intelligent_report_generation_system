package answer

import (
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/shared"
)

const (
	VerifierVersion        = "answer-fact-verifier-v1"
	DefaultVerifierVersion = VerifierVersion
)

//go:embed wordlist/v1.json
var embeddedPolicyWordlist []byte

type policyWordlist struct {
	Version               string   `json:"version"`
	Causal                []string `json:"causal"`
	Prediction            []string `json:"prediction"`
	External              []string `json:"external"`
	Advice                []string `json:"advice"`
	WeakContributionAllow []string `json:"weakContributionAllow"`
}

type PolicyCategory string

const (
	PolicyCausality    PolicyCategory = "CAUSALITY"
	PolicyForecast     PolicyCategory = "FORECAST"
	PolicyExternalFact PolicyCategory = "EXTERNAL_FACT"
	PolicyAdvice       PolicyCategory = "ADVICE"
	PolicyContribution PolicyCategory = "CONTRIBUTION"
)

type PolicyMatch struct {
	Category PolicyCategory
	Text     string
	Span     shared.TextSpan
}

func loadPolicyWordlist() (policyWordlist, error) {
	var wordlist policyWordlist
	if err := askdata.DecodeStrictJSON(embeddedPolicyWordlist, &wordlist); err != nil {
		return policyWordlist{}, fmt.Errorf("decode embedded answer policy: %w", err)
	}
	if strings.TrimSpace(wordlist.Version) == "" {
		return policyWordlist{}, errors.New("answer policy version is required")
	}
	groups := [][]string{wordlist.Causal, wordlist.Prediction, wordlist.External, wordlist.Advice, wordlist.WeakContributionAllow}
	for _, group := range groups {
		if len(group) == 0 {
			return policyWordlist{}, errors.New("answer policy word groups must be non-empty")
		}
		seen := map[string]struct{}{}
		for _, phrase := range group {
			if strings.TrimSpace(phrase) == "" || !utf8.ValidString(phrase) {
				return policyWordlist{}, errors.New("answer policy contains an invalid phrase")
			}
			if _, exists := seen[phrase]; exists {
				return policyWordlist{}, errors.New("answer policy contains a duplicate phrase")
			}
			seen[phrase] = struct{}{}
		}
	}
	return wordlist, nil
}

func (wordlist policyWordlist) matches(text string, contributionMode bool) []PolicyMatch {
	type entry struct {
		category PolicyCategory
		phrase   string
	}
	entries := make([]entry, 0)
	appendEntries := func(category PolicyCategory, phrases []string) {
		for _, phrase := range phrases {
			entries = append(entries, entry{category: category, phrase: phrase})
		}
	}
	appendEntries(PolicyCausality, wordlist.Causal)
	appendEntries(PolicyForecast, wordlist.Prediction)
	appendEntries(PolicyExternalFact, wordlist.External)
	appendEntries(PolicyAdvice, wordlist.Advice)
	sort.SliceStable(entries, func(left, right int) bool {
		return utf8.RuneCountInString(entries[left].phrase) > utf8.RuneCountInString(entries[right].phrase)
	})
	runes := []rune(text)
	occupied := make([]bool, len(runes))
	matches := make([]PolicyMatch, 0)
	for _, candidate := range entries {
		needle := []rune(candidate.phrase)
		for start := 0; start+len(needle) <= len(runes); start++ {
			if string(runes[start:start+len(needle)]) != candidate.phrase || spanOccupied(occupied, start, start+len(needle)) {
				continue
			}
			if contributionMode && candidate.category == PolicyCausality &&
				containedByAllowedContribution(runes, start, start+len(needle), wordlist.WeakContributionAllow) {
				continue
			}
			markSpan(occupied, start, start+len(needle))
			matches = append(matches, PolicyMatch{
				Category: candidate.category, Text: candidate.phrase,
				Span: shared.TextSpan{Start: start, End: start + len(needle)},
			})
		}
	}
	// “受……影响” is a structural strong-causality form rather than a finite phrase.
	for start, character := range runes {
		if character != '受' {
			continue
		}
		limit := start + 24
		if limit > len(runes) {
			limit = len(runes)
		}
		for end := start + 2; end+1 <= limit; end++ {
			if string(runes[end-1:end+1]) != "影响" || spanOccupied(occupied, start, end+1) {
				continue
			}
			markSpan(occupied, start, end+1)
			matches = append(matches, PolicyMatch{
				Category: PolicyCausality, Text: string(runes[start : end+1]),
				Span: shared.TextSpan{Start: start, End: end + 1},
			})
			break
		}
	}
	sort.SliceStable(matches, func(left, right int) bool { return matches[left].Span.Start < matches[right].Span.Start })
	return matches
}

func containedByAllowedContribution(runes []rune, start, end int, allowed []string) bool {
	for _, phrase := range allowed {
		needle := []rune(phrase)
		for offset := 0; offset+len(needle) <= len(runes); offset++ {
			if offset <= start && offset+len(needle) >= end && string(runes[offset:offset+len(needle)]) == phrase {
				return true
			}
		}
	}
	return false
}

func spanOccupied(occupied []bool, start, end int) bool {
	for index := start; index < end; index++ {
		if occupied[index] {
			return true
		}
	}
	return false
}

func markSpan(occupied []bool, start, end int) {
	for index := start; index < end; index++ {
		occupied[index] = true
	}
}
