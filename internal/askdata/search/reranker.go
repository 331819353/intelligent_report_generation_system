package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	askdatacognition "intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	RerankSchemaVersion        = "1.0"
	MaxRerankCandidates        = 30
	MaxRerankChoices           = 30
	MaxRerankNegativeExamples  = 4
	MaxRerankEvidencePerChoice = 16
	MaxRerankSummaryRunes      = 1_000
)

var (
	ErrInvalidRerankRequest  = errors.New("semantic candidate rerank request is invalid")
	ErrInvalidRerankProposal = errors.New("semantic candidate rerank proposal is invalid")
)

type GraphCompatibility string
type GateVerdict string
type RerankVerdict string
type RerankDecisionSource string

const (
	GraphCompatible   GraphCompatibility = "COMPATIBLE"
	GraphIncompatible GraphCompatibility = "INCOMPATIBLE"
	GraphNotRequired  GraphCompatibility = "NOT_REQUIRED"
	GraphUnknown      GraphCompatibility = "UNKNOWN"

	GateAllow GateVerdict = "ALLOW"
	GateBlock GateVerdict = "BLOCK"

	RerankRanked  RerankVerdict = "RANKED"
	RerankNoMatch RerankVerdict = "NO_MATCH"

	DecisionLLM                RerankDecisionSource = "LLM"
	DecisionDeterministicBlock RerankDecisionSource = "DETERMINISTIC_BLOCK"
)

type CandidateRef struct {
	ObjectType      ObjectType `json:"objectType"`
	ObjectVersionID askdata.ID `json:"objectVersionId"`
}

func (ref CandidateRef) Validate() error {
	if !ValidRetrievalObjectType(ref.ObjectType) {
		return errors.New("objectType is invalid")
	}
	if err := ref.ObjectVersionID.Validate(); err != nil {
		return fmt.Errorf("objectVersionId: %w", err)
	}
	return nil
}

type DeterministicGate struct {
	Verdict      GateVerdict           `json:"verdict"`
	ReasonCodes  []string              `json:"reasonCodes"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

func (gate DeterministicGate) Validate() error {
	if gate.Verdict != GateAllow && gate.Verdict != GateBlock {
		return errors.New("gate verdict is invalid")
	}
	if len(gate.ReasonCodes) < 1 || len(gate.ReasonCodes) > 16 {
		return errors.New("gate reasonCodes count is invalid")
	}
	if err := validateSortedCodes(gate.ReasonCodes); err != nil {
		return fmt.Errorf("gate reasonCodes: %w", err)
	}
	if len(gate.EvidenceRefs) < 1 || len(gate.EvidenceRefs) > 16 {
		return errors.New("gate evidenceRefs count is invalid")
	}
	return validateSortedEvidenceRefs(gate.EvidenceRefs)
}

// RerankCandidate is produced only after SQL/RLS/release filtering. Definition,
// hard negatives and graph compatibility are sanitized evidence, while Gate is
// an authoritative deterministic decision that the reviewer cannot override.
type RerankCandidate struct {
	Candidate          Candidate             `json:"candidate"`
	Definition         string                `json:"definition"`
	NegativeExamples   []string              `json:"negativeExamples"`
	Sensitivity        registry.Sensitivity  `json:"sensitivity"`
	GraphCompatibility GraphCompatibility    `json:"graphCompatibility"`
	GraphEvidenceRefs  []askdata.EvidenceRef `json:"graphEvidenceRefs"`
	Gate               DeterministicGate     `json:"gate"`
}

type RerankRequest struct {
	Scope                   askdata.PolicyScope `json:"scope"`
	Mention                 string              `json:"mention"`
	RetrievalDegraded       bool                `json:"retrievalDegraded"`
	RetrievalDegradedReason string              `json:"retrievalDegradedReason,omitempty"`
	Candidates              []RerankCandidate   `json:"candidates"`
}

type RerankCandidateEvidence struct {
	CandidateRef       CandidateRef          `json:"candidateRef"`
	BaselineRank       int                   `json:"baselineRank"`
	BaselineScore      float64               `json:"baselineScore"`
	RetrievalEvidence  []SourceEvidence      `json:"retrievalEvidence"`
	Definition         string                `json:"definition"`
	NegativeExamples   []string              `json:"negativeExamples"`
	Sensitivity        registry.Sensitivity  `json:"sensitivity"`
	GraphCompatibility GraphCompatibility    `json:"graphCompatibility"`
	GraphEvidenceRefs  []askdata.EvidenceRef `json:"graphEvidenceRefs"`
	Selectable         bool                  `json:"selectable"`
	BlockReasonCodes   []string              `json:"blockReasonCodes"`
	GateVerdict        GateVerdict           `json:"gateVerdict"`
	GateReasonCodes    []string              `json:"gateReasonCodes"`
	GateEvidenceRefs   []askdata.EvidenceRef `json:"gateEvidenceRefs"`
}

type RerankEvidence struct {
	SchemaVersion           string                    `json:"schemaVersion"`
	Scope                   askdata.PolicyScope       `json:"scope"`
	Mention                 string                    `json:"mention"`
	RetrievalDegraded       bool                      `json:"retrievalDegraded"`
	RetrievalDegradedReason string                    `json:"retrievalDegradedReason,omitempty"`
	Candidates              []RerankCandidateEvidence `json:"candidates"`
}

// NewRerankEvidence canonicalizes a bounded candidate set. Candidate ordering
// is rebuilt from deterministic RRF score/type/ID, so caller slice order cannot
// influence the cognition fact or its hash.
func NewRerankEvidence(request RerankRequest) (RerankEvidence, error) {
	if err := request.Scope.Validate(); err != nil {
		return RerankEvidence{}, fmt.Errorf("%w: scope: %v", ErrInvalidRerankRequest, err)
	}
	mention, err := normalizeText(request.Mention, 512)
	if err != nil || mention == "" || len(request.Candidates) < 1 || len(request.Candidates) > MaxRerankCandidates {
		return RerankEvidence{}, ErrInvalidRerankRequest
	}
	degradedReason := strings.TrimSpace(request.RetrievalDegradedReason)
	if request.RetrievalDegraded {
		if !stableRerankCode(degradedReason) {
			return RerankEvidence{}, fmt.Errorf("%w: degraded reason", ErrInvalidRerankRequest)
		}
	} else if degradedReason != "" {
		return RerankEvidence{}, fmt.Errorf("%w: degraded reason without degradation", ErrInvalidRerankRequest)
	}

	candidates := make([]RerankCandidateEvidence, 0, len(request.Candidates))
	seen := map[CandidateRef]struct{}{}
	for index, input := range request.Candidates {
		candidate, err := normalizeRerankCandidate(input)
		if err != nil {
			return RerankEvidence{}, fmt.Errorf("%w: candidates[%d]: %v", ErrInvalidRerankRequest, index, err)
		}
		ref := CandidateRef{candidate.Candidate.ObjectType, candidate.Candidate.ObjectVersionID}
		if _, duplicate := seen[ref]; duplicate {
			return RerankEvidence{}, fmt.Errorf("%w: duplicated candidate", ErrInvalidRerankRequest)
		}
		seen[ref] = struct{}{}
		blockReasons := []string{}
		if candidate.Gate.Verdict == GateBlock {
			blockReasons = append(blockReasons, candidate.Gate.ReasonCodes...)
		}
		if candidate.GraphCompatibility == GraphIncompatible {
			blockReasons = append(blockReasons, "GRAPH_INCOMPATIBLE")
		}
		blockReasons = normalizedCodes(blockReasons)
		candidates = append(candidates, RerankCandidateEvidence{
			CandidateRef:       ref,
			BaselineScore:      candidate.Candidate.Score,
			RetrievalEvidence:  append([]SourceEvidence(nil), candidate.Candidate.Evidence...),
			Definition:         candidate.Definition,
			NegativeExamples:   append([]string{}, candidate.NegativeExamples...),
			Sensitivity:        candidate.Sensitivity,
			GraphCompatibility: candidate.GraphCompatibility,
			GraphEvidenceRefs:  append([]askdata.EvidenceRef(nil), candidate.GraphEvidenceRefs...),
			Selectable:         len(blockReasons) == 0,
			BlockReasonCodes:   blockReasons,
			GateVerdict:        candidate.Gate.Verdict,
			GateReasonCodes:    append([]string{}, candidate.Gate.ReasonCodes...),
			GateEvidenceRefs:   append([]askdata.EvidenceRef(nil), candidate.Gate.EvidenceRefs...),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].BaselineScore != candidates[j].BaselineScore {
			return candidates[i].BaselineScore > candidates[j].BaselineScore
		}
		return candidateRefLess(candidates[i].CandidateRef, candidates[j].CandidateRef)
	})
	for index := range candidates {
		candidates[index].BaselineRank = index + 1
	}
	evidence := RerankEvidence{
		SchemaVersion:           RerankSchemaVersion,
		Scope:                   request.Scope,
		Mention:                 mention,
		RetrievalDegraded:       request.RetrievalDegraded,
		RetrievalDegradedReason: degradedReason,
		Candidates:              candidates,
	}
	if err := evidence.Validate(); err != nil {
		return RerankEvidence{}, err
	}
	return evidence, nil
}

func (evidence RerankEvidence) Validate() error {
	if evidence.SchemaVersion != RerankSchemaVersion || evidence.Scope.Validate() != nil ||
		len(evidence.Candidates) < 1 || len(evidence.Candidates) > MaxRerankCandidates {
		return ErrInvalidRerankRequest
	}
	mention, err := normalizeText(evidence.Mention, 512)
	if err != nil || mention == "" || mention != evidence.Mention {
		return ErrInvalidRerankRequest
	}
	if evidence.RetrievalDegraded {
		if !stableRerankCode(evidence.RetrievalDegradedReason) {
			return ErrInvalidRerankRequest
		}
	} else if evidence.RetrievalDegradedReason != "" {
		return ErrInvalidRerankRequest
	}
	seen := map[CandidateRef]struct{}{}
	for index, candidate := range evidence.Candidates {
		if candidate.BaselineRank != index+1 {
			return errors.New("rerank baseline ranks must be contiguous")
		}
		if err := validateRerankCandidateEvidence(candidate); err != nil {
			return fmt.Errorf("candidates[%d]: %w", index, err)
		}
		if _, duplicate := seen[candidate.CandidateRef]; duplicate {
			return errors.New("rerank candidate is duplicated")
		}
		seen[candidate.CandidateRef] = struct{}{}
		if index > 0 {
			previous := evidence.Candidates[index-1]
			if previous.BaselineScore < candidate.BaselineScore ||
				(previous.BaselineScore == candidate.BaselineScore &&
					!candidateRefLess(previous.CandidateRef, candidate.CandidateRef)) {
				return errors.New("rerank candidates are not in canonical baseline order")
			}
		}
	}
	return nil
}

func (evidence RerankEvidence) PromptFact() (askdatacognition.PromptFact, error) {
	if err := evidence.Validate(); err != nil {
		return askdatacognition.PromptFact{}, err
	}
	payload, err := json.Marshal(evidence)
	if err != nil {
		return askdatacognition.PromptFact{}, err
	}
	return askdatacognition.NewPromptFact(
		askdata.ID("rerank-candidates:"+string(askdata.HashBytes(payload))),
		askdatacognition.FactCandidateSet,
		payload,
	)
}

func (evidence RerankEvidence) EvidenceRef() (askdata.EvidenceRef, error) {
	fact, err := evidence.PromptFact()
	if err != nil {
		return askdata.EvidenceRef{}, err
	}
	ref := askdata.EvidenceRef{
		EvidenceID:  fact.EvidenceID,
		Kind:        askdata.EvidenceKindCandidateSet,
		SourceID:    evidence.Scope.Release.ReleaseID,
		ContentHash: fact.ContentHash,
	}
	return ref, ref.Validate()
}

type RerankChoice struct {
	CandidateRef CandidateRef          `json:"candidateRef"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

type RerankProposal struct {
	SchemaVersion        string              `json:"schemaVersion"`
	Verdict              RerankVerdict       `json:"verdict"`
	Choices              []RerankChoice      `json:"choices"`
	Summary              string              `json:"summary"`
	CandidateSetEvidence askdata.EvidenceRef `json:"candidateSetEvidence"`
}

func DecodeRerankProposal(raw []byte) (RerankProposal, error) {
	var proposal RerankProposal
	if err := askdata.DecodeStrictJSON(raw, &proposal); err != nil {
		return RerankProposal{}, err
	}
	if err := proposal.Validate(); err != nil {
		return RerankProposal{}, err
	}
	return proposal, nil
}

func (proposal RerankProposal) Validate() error {
	if proposal.SchemaVersion != RerankSchemaVersion ||
		(proposal.Verdict != RerankRanked && proposal.Verdict != RerankNoMatch) {
		return ErrInvalidRerankProposal
	}
	if strings.TrimSpace(proposal.Summary) == "" || !utf8.ValidString(proposal.Summary) ||
		utf8.RuneCountInString(proposal.Summary) > MaxRerankSummaryRunes {
		return fmt.Errorf("%w: summary", ErrInvalidRerankProposal)
	}
	if err := proposal.CandidateSetEvidence.Validate(); err != nil ||
		proposal.CandidateSetEvidence.Kind != askdata.EvidenceKindCandidateSet {
		return fmt.Errorf("%w: candidateSetEvidence", ErrInvalidRerankProposal)
	}
	if proposal.Verdict == RerankRanked {
		if len(proposal.Choices) < 1 || len(proposal.Choices) > MaxRerankChoices {
			return fmt.Errorf("%w: choices count", ErrInvalidRerankProposal)
		}
	} else if len(proposal.Choices) != 0 {
		return fmt.Errorf("%w: NO_MATCH cannot contain choices", ErrInvalidRerankProposal)
	}
	seen := map[CandidateRef]struct{}{}
	for index, choice := range proposal.Choices {
		if err := choice.CandidateRef.Validate(); err != nil {
			return fmt.Errorf("%w: choices[%d]: %v", ErrInvalidRerankProposal, index, err)
		}
		if _, duplicate := seen[choice.CandidateRef]; duplicate {
			return fmt.Errorf("%w: choices[%d] is duplicated", ErrInvalidRerankProposal, index)
		}
		seen[choice.CandidateRef] = struct{}{}
		if len(choice.EvidenceRefs) < 1 || len(choice.EvidenceRefs) > MaxRerankEvidencePerChoice ||
			validateSortedEvidenceRefs(choice.EvidenceRefs) != nil {
			return fmt.Errorf("%w: choices[%d].evidenceRefs", ErrInvalidRerankProposal, index)
		}
	}
	return nil
}

// ValidateAgainstEvidence is the local authority after model output. It
// rejects invented IDs, blocked candidates, cross-candidate evidence and a
// candidate-set hash from any other policy scope or release.
func (proposal RerankProposal) ValidateAgainstEvidence(evidence RerankEvidence) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	if err := proposal.Validate(); err != nil {
		return err
	}
	ref, err := evidence.EvidenceRef()
	if err != nil {
		return err
	}
	if proposal.CandidateSetEvidence != ref {
		return fmt.Errorf("%w: candidate set evidence mismatch", ErrInvalidRerankProposal)
	}
	candidates := make(map[CandidateRef]RerankCandidateEvidence, len(evidence.Candidates))
	for _, candidate := range evidence.Candidates {
		candidates[candidate.CandidateRef] = candidate
	}
	for index, choice := range proposal.Choices {
		candidate, exists := candidates[choice.CandidateRef]
		if !exists {
			return fmt.Errorf("%w: choices[%d] invented a candidate", ErrInvalidRerankProposal, index)
		}
		if !candidate.Selectable {
			return fmt.Errorf("%w: choices[%d] attempted to override deterministic block", ErrInvalidRerankProposal, index)
		}
		allowedEvidence := candidateEvidenceSet(candidate)
		for _, evidenceRef := range choice.EvidenceRefs {
			if _, allowed := allowedEvidence[evidenceRef]; !allowed {
				return fmt.Errorf("%w: choices[%d] cited evidence from another candidate", ErrInvalidRerankProposal, index)
			}
		}
	}
	return nil
}

type BlockedCandidate struct {
	CandidateRef CandidateRef          `json:"candidateRef"`
	ReasonCodes  []string              `json:"reasonCodes"`
	EvidenceRefs []askdata.EvidenceRef `json:"evidenceRefs"`
}

type RerankResult struct {
	CandidateSetEvidence askdata.EvidenceRef  `json:"candidateSetEvidence"`
	DecisionSource       RerankDecisionSource `json:"decisionSource"`
	RankedCandidates     []Candidate          `json:"rankedCandidates"`
	UnselectedCandidates []CandidateRef       `json:"unselectedCandidates"`
	BlockedCandidates    []BlockedCandidate   `json:"blockedCandidates"`
	NoMatch              bool                 `json:"noMatch"`
	ProposalHash         askdata.ContentHash  `json:"proposalHash,omitempty"`
	ResultHash           askdata.ContentHash  `json:"resultHash"`
}

type RerankReviewer interface {
	ReviewCandidates(context.Context, askdatacognition.PromptFact) (RerankProposal, error)
}

type Reranker struct{ reviewer RerankReviewer }

func NewReranker(reviewer RerankReviewer) (*Reranker, error) {
	if reviewer == nil {
		return nil, errors.New("rerank reviewer is required")
	}
	return &Reranker{reviewer: reviewer}, nil
}

func (reranker *Reranker) Rerank(ctx context.Context, request RerankRequest) (RerankResult, error) {
	if reranker == nil || reranker.reviewer == nil {
		return RerankResult{}, ErrInvalidRerankRequest
	}
	evidence, err := NewRerankEvidence(request)
	if err != nil {
		return RerankResult{}, err
	}
	setRef, err := evidence.EvidenceRef()
	if err != nil {
		return RerankResult{}, err
	}
	result := baseRerankResult(evidence, setRef)
	selectable := 0
	for _, candidate := range evidence.Candidates {
		if candidate.Selectable {
			selectable++
		}
	}
	if selectable == 0 {
		result.DecisionSource = DecisionDeterministicBlock
		result.NoMatch = true
		return finalizeRerankResult(result)
	}
	fact, err := evidence.PromptFact()
	if err != nil {
		return RerankResult{}, err
	}
	proposal, err := reranker.reviewer.ReviewCandidates(ctx, fact)
	if err != nil {
		return RerankResult{}, err
	}
	proposal = normalizeRerankProposal(proposal)
	if err := proposal.ValidateAgainstEvidence(evidence); err != nil {
		return RerankResult{}, err
	}
	proposalPayload, err := json.Marshal(proposal)
	if err != nil {
		return RerankResult{}, err
	}
	result.DecisionSource = DecisionLLM
	result.ProposalHash = askdata.HashBytes(proposalPayload)
	result.NoMatch = proposal.Verdict == RerankNoMatch
	candidateMap := make(map[CandidateRef]Candidate, len(evidence.Candidates))
	for _, candidate := range evidence.Candidates {
		candidateMap[candidate.CandidateRef] = Candidate{
			ObjectType:      candidate.CandidateRef.ObjectType,
			ObjectVersionID: candidate.CandidateRef.ObjectVersionID,
			Score:           candidate.BaselineScore,
			Evidence:        append([]SourceEvidence(nil), candidate.RetrievalEvidence...),
		}
	}
	selected := map[CandidateRef]struct{}{}
	for _, choice := range proposal.Choices {
		result.RankedCandidates = append(result.RankedCandidates, candidateMap[choice.CandidateRef])
		selected[choice.CandidateRef] = struct{}{}
	}
	result.UnselectedCandidates = result.UnselectedCandidates[:0]
	for _, candidate := range evidence.Candidates {
		if candidate.Selectable {
			if _, chosen := selected[candidate.CandidateRef]; !chosen {
				result.UnselectedCandidates = append(result.UnselectedCandidates, candidate.CandidateRef)
			}
		}
	}
	return finalizeRerankResult(result)
}

func normalizeRerankCandidate(input RerankCandidate) (RerankCandidate, error) {
	ref := CandidateRef{input.Candidate.ObjectType, input.Candidate.ObjectVersionID}
	if err := ref.Validate(); err != nil || math.IsNaN(input.Candidate.Score) || math.IsInf(input.Candidate.Score, 0) ||
		input.Candidate.Score < 0 || len(input.Candidate.Evidence) < 1 || len(input.Candidate.Evidence) > 3 {
		return RerankCandidate{}, errors.New("candidate identity, score or retrieval evidence is invalid")
	}
	// MEMBER definitions contain raw labels. The public request shape cannot
	// prove that a caller loaded one from the release-pinned authoritative
	// store under the recorded policy, so fail closed before building a
	// PromptFact. Member-aware reranking can be enabled only after that proof is
	// represented by an unforgeable capability.
	if ref.ObjectType == ObjectMember {
		return RerankCandidate{}, errors.New("member candidate cannot enter LLM reranking without authoritative evidence")
	}
	definition, err := normalizeText(input.Definition, 512)
	if err != nil || definition == "" || unsafeRerankText(definition) {
		return RerankCandidate{}, errors.New("candidate definition is invalid")
	}
	negativeExamples, err := normalizeRerankExamples(input.NegativeExamples)
	if err != nil {
		return RerankCandidate{}, err
	}
	if input.Sensitivity != registry.SensitivityPublic && input.Sensitivity != registry.SensitivityInternal {
		return RerankCandidate{}, errors.New("sensitive candidate cannot enter LLM reranking")
	}
	if !validGraphCompatibility(input.GraphCompatibility) ||
		len(input.GraphEvidenceRefs) < 1 || len(input.GraphEvidenceRefs) > 16 {
		return RerankCandidate{}, errors.New("graph compatibility evidence is invalid")
	}
	result := input
	result.Definition = definition
	result.NegativeExamples = negativeExamples
	result.Candidate.Evidence = append([]SourceEvidence(nil), input.Candidate.Evidence...)
	sort.Slice(result.Candidate.Evidence, func(i, j int) bool {
		return result.Candidate.Evidence[i].Source < result.Candidate.Evidence[j].Source
	})
	if err := validateCandidateSourceEvidence(result.Candidate); err != nil {
		return RerankCandidate{}, err
	}
	result.GraphEvidenceRefs = normalizedEvidenceRefs(input.GraphEvidenceRefs)
	if err := validateSortedEvidenceRefs(result.GraphEvidenceRefs); err != nil {
		return RerankCandidate{}, err
	}
	result.Gate.ReasonCodes = normalizedCodes(input.Gate.ReasonCodes)
	result.Gate.EvidenceRefs = normalizedEvidenceRefs(input.Gate.EvidenceRefs)
	if err := result.Gate.Validate(); err != nil {
		return RerankCandidate{}, err
	}
	return result, nil
}

func validateRerankCandidateEvidence(candidate RerankCandidateEvidence) error {
	if err := candidate.CandidateRef.Validate(); err != nil || candidate.CandidateRef.ObjectType == ObjectMember ||
		candidate.BaselineRank < 1 ||
		math.IsNaN(candidate.BaselineScore) || math.IsInf(candidate.BaselineScore, 0) || candidate.BaselineScore < 0 {
		return errors.New("candidate baseline is invalid")
	}
	if normalized, err := normalizeText(candidate.Definition, 512); err != nil || normalized == "" ||
		normalized != candidate.Definition || unsafeRerankText(candidate.Definition) {
		return errors.New("candidate definition is invalid")
	}
	if candidate.Sensitivity != registry.SensitivityPublic && candidate.Sensitivity != registry.SensitivityInternal {
		return errors.New("candidate sensitivity is invalid")
	}
	if !validGraphCompatibility(candidate.GraphCompatibility) ||
		validateSortedEvidenceRefs(candidate.GraphEvidenceRefs) != nil || len(candidate.GraphEvidenceRefs) < 1 {
		return errors.New("candidate graph evidence is invalid")
	}
	if candidate.GateVerdict != GateAllow && candidate.GateVerdict != GateBlock {
		return errors.New("candidate deterministic gate verdict is invalid")
	}
	if validateSortedCodes(candidate.GateReasonCodes) != nil || len(candidate.GateReasonCodes) < 1 ||
		validateSortedEvidenceRefs(candidate.GateEvidenceRefs) != nil || len(candidate.GateEvidenceRefs) < 1 ||
		validateCandidateSourceEvidence(Candidate{
			ObjectType:      candidate.CandidateRef.ObjectType,
			ObjectVersionID: candidate.CandidateRef.ObjectVersionID,
			Score:           candidate.BaselineScore,
			Evidence:        candidate.RetrievalEvidence,
		}) != nil {
		return errors.New("candidate evidence is invalid")
	}
	normalizedExamples, err := normalizeRerankExamples(candidate.NegativeExamples)
	if err != nil {
		return err
	}
	if !equalStrings(candidate.NegativeExamples, normalizedExamples) {
		return errors.New("candidate negative examples are not canonical")
	}
	expectedBlockReasons := []string{}
	if candidate.GateVerdict == GateBlock {
		expectedBlockReasons = append(expectedBlockReasons, candidate.GateReasonCodes...)
	}
	if candidate.GraphCompatibility == GraphIncompatible {
		expectedBlockReasons = append(expectedBlockReasons, "GRAPH_INCOMPATIBLE")
	}
	expectedBlockReasons = normalizedCodes(expectedBlockReasons)
	expectedSelectable := len(expectedBlockReasons) == 0
	if candidate.Selectable != expectedSelectable || validateSortedCodes(candidate.BlockReasonCodes) != nil ||
		!equalStrings(candidate.BlockReasonCodes, expectedBlockReasons) {
		return errors.New("candidate deterministic block shape is invalid")
	}
	return nil
}

func validateCandidateSourceEvidence(candidate Candidate) error {
	if len(candidate.Evidence) < 1 || len(candidate.Evidence) > 3 {
		return errors.New("retrieval evidence count is invalid")
	}
	previous := RetrievalSource("")
	seen := map[RetrievalSource]struct{}{}
	for _, evidence := range candidate.Evidence {
		if evidence.Source != SourceExact && evidence.Source != SourceLexical && evidence.Source != SourceVector {
			return errors.New("retrieval evidence source is invalid")
		}
		if _, duplicate := seen[evidence.Source]; duplicate {
			return errors.New("retrieval evidence source is duplicated")
		}
		if previous != "" && evidence.Source < previous {
			return errors.New("retrieval evidence must be sorted")
		}
		seen[evidence.Source] = struct{}{}
		previous = evidence.Source
		if evidence.Rank < 1 || math.IsNaN(evidence.SourceScore) || math.IsInf(evidence.SourceScore, 0) ||
			evidence.SourceScore < 0 || evidence.Evidence.Validate() != nil ||
			evidence.Evidence.SourceID != candidate.ObjectVersionID ||
			evidence.Evidence.Kind != sourceEvidenceKind(evidence.Source) {
			return errors.New("retrieval source evidence is invalid")
		}
	}
	return nil
}

func normalizeRerankExamples(values []string) ([]string, error) {
	if len(values) > MaxRerankNegativeExamples {
		return nil, errors.New("negative examples exceed the bounded prompt limit")
	}
	result := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized, err := normalizeText(value, 256)
		if err != nil || unsafeRerankText(normalized) {
			return nil, errors.New("negative example is invalid")
		}
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, normalized)
	}
	sort.Slice(result, func(i, j int) bool { return strings.ToLower(result[i]) < strings.ToLower(result[j]) })
	return result, nil
}

func unsafeRerankText(value string) bool {
	return ai.ContainsSensitiveText(value) || physicalQueryPattern.MatchString(value)
}

func validGraphCompatibility(value GraphCompatibility) bool {
	return value == GraphCompatible || value == GraphIncompatible ||
		value == GraphNotRequired || value == GraphUnknown
}

func normalizeRerankProposal(proposal RerankProposal) RerankProposal {
	proposal.Choices = append([]RerankChoice(nil), proposal.Choices...)
	for index := range proposal.Choices {
		proposal.Choices[index].EvidenceRefs = normalizedEvidenceRefs(proposal.Choices[index].EvidenceRefs)
	}
	return proposal
}

func candidateEvidenceSet(candidate RerankCandidateEvidence) map[askdata.EvidenceRef]struct{} {
	result := map[askdata.EvidenceRef]struct{}{}
	for _, evidence := range candidate.RetrievalEvidence {
		result[evidence.Evidence] = struct{}{}
	}
	for _, evidence := range candidate.GraphEvidenceRefs {
		result[evidence] = struct{}{}
	}
	for _, evidence := range candidate.GateEvidenceRefs {
		result[evidence] = struct{}{}
	}
	return result
}

func baseRerankResult(evidence RerankEvidence, setRef askdata.EvidenceRef) RerankResult {
	result := RerankResult{CandidateSetEvidence: setRef}
	for _, candidate := range evidence.Candidates {
		if candidate.Selectable {
			result.UnselectedCandidates = append(result.UnselectedCandidates, candidate.CandidateRef)
			continue
		}
		refs := append([]askdata.EvidenceRef(nil), candidate.GateEvidenceRefs...)
		refs = append(refs, candidate.GraphEvidenceRefs...)
		refs = normalizedEvidenceRefs(refs)
		result.BlockedCandidates = append(result.BlockedCandidates, BlockedCandidate{
			CandidateRef: candidate.CandidateRef,
			ReasonCodes:  append([]string(nil), candidate.BlockReasonCodes...),
			EvidenceRefs: refs,
		})
	}
	return result
}

func finalizeRerankResult(result RerankResult) (RerankResult, error) {
	result.ResultHash = ""
	payload, err := json.Marshal(result)
	if err != nil {
		return RerankResult{}, err
	}
	result.ResultHash = askdata.HashBytes(payload)
	return result, nil
}

func normalizedCodes(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) == 0 {
		return []string{}
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func validateSortedCodes(values []string) error {
	previous := ""
	for _, value := range values {
		if !stableRerankCode(value) || (previous != "" && value <= previous) {
			return errors.New("codes must be stable, sorted and unique")
		}
		previous = value
	}
	return nil
}

func stableRerankCode(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}

func normalizedEvidenceRefs(values []askdata.EvidenceRef) []askdata.EvidenceRef {
	result := append([]askdata.EvidenceRef(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].EvidenceID != result[j].EvidenceID {
			return result[i].EvidenceID < result[j].EvidenceID
		}
		return result[i].ContentHash < result[j].ContentHash
	})
	if len(result) == 0 {
		return []askdata.EvidenceRef{}
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func validateSortedEvidenceRefs(values []askdata.EvidenceRef) error {
	var previous askdata.EvidenceRef
	for index, evidence := range values {
		if err := evidence.Validate(); err != nil {
			return fmt.Errorf("evidenceRefs[%d]: %w", index, err)
		}
		if index > 0 && evidence.EvidenceID <= previous.EvidenceID {
			return errors.New("evidenceRefs must be sorted and unique")
		}
		previous = evidence
	}
	return nil
}

func candidateRefLess(left, right CandidateRef) bool {
	if left.ObjectType != right.ObjectType {
		return left.ObjectType < right.ObjectType
	}
	return left.ObjectVersionID < right.ObjectVersionID
}

func equalStrings(left, right []string) bool {
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
