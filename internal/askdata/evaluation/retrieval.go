package evaluation

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	RetrievalEvaluationVersion = "retrieval-recall-evaluation-v1"
	MaxRetrievalCases          = 100_000
)

type RetrievalMode string

const (
	RetrievalModeANN   RetrievalMode = "ANN"
	RetrievalModeExact RetrievalMode = "EXACT"
)

type RetrievalObjectType string

const (
	RetrievalMetric    RetrievalObjectType = "METRIC"
	RetrievalDimension RetrievalObjectType = "DIMENSION"
	RetrievalMember    RetrievalObjectType = "MEMBER"
)

var ErrInvalidRetrievalEvaluation = errors.New("retrieval recall evaluation is invalid")

type RetrievalCandidate struct {
	ObjectType RetrievalObjectType `json:"objectType"`
	VersionID  askdata.ID          `json:"versionId"`
	Rank       int                 `json:"rank"`
}

type RetrievalGold struct {
	MetricVersionIDs    []askdata.ID `json:"metricVersionIds"`
	DimensionVersionIDs []askdata.ID `json:"dimensionVersionIds"`
	MemberVersionIDs    []askdata.ID `json:"memberVersionIds"`
}

// RetrievalEvaluationCase intentionally has no question-text field. CaseID is
// the only sample identifier emitted by reports, including sensitive cases.
type RetrievalEvaluationCase struct {
	SchemaVersion string               `json:"schemaVersion"`
	CaseID        askdata.ID           `json:"caseId"`
	DomainID      askdata.ID           `json:"domainId"`
	Complexity    ComplexityClass      `json:"complexity"`
	Sensitive     bool                 `json:"sensitive"`
	Gold          RetrievalGold        `json:"gold"`
	ANN           []RetrievalCandidate `json:"ann"`
	Exact         []RetrievalCandidate `json:"exact"`
}

type RetrievalCaseSet struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Cases         []RetrievalEvaluationCase `json:"cases"`
}

type RecallScore struct {
	K      int     `json:"k"`
	Gold   int     `json:"gold"`
	Hit    int     `json:"hit"`
	Recall float64 `json:"recall"`
	Passed bool    `json:"passed"`
}

type RetrievalSummary struct {
	CaseCount int         `json:"caseCount"`
	Metric    RecallScore `json:"metric"`
	Dimension RecallScore `json:"dimension"`
	Member    RecallScore `json:"member"`
}

type RetrievalSlice struct {
	Key     string           `json:"key"`
	Summary RetrievalSummary `json:"summary"`
}

type RetrievalFailure struct {
	CaseID     askdata.ID          `json:"caseId"`
	DomainID   askdata.ID          `json:"domainId"`
	Complexity ComplexityClass     `json:"complexity"`
	ObjectType RetrievalObjectType `json:"objectType"`
}

type RetrievalEvaluationReport struct {
	SchemaVersion string              `json:"schemaVersion"`
	Mode          RetrievalMode       `json:"mode"`
	CaseCount     int                 `json:"caseCount"`
	Overall       RetrievalSummary    `json:"overall"`
	ByDomain      []RetrievalSlice    `json:"byDomain"`
	ByComplexity  []RetrievalSlice    `json:"byComplexity"`
	Failures      []RetrievalFailure  `json:"failures"`
	Passed        bool                `json:"passed"`
	SourceHash    askdata.ContentHash `json:"sourceHash"`
}

type recallAccumulator struct {
	cases                       map[askdata.ID]struct{}
	metricGold, metricHit       int
	dimensionGold, dimensionHit int
	memberGold, memberHit       int
}

func EvaluateRetrievalRecall(
	cases []RetrievalEvaluationCase,
	mode RetrievalMode,
) (RetrievalEvaluationReport, error) {
	if mode != RetrievalModeANN && mode != RetrievalModeExact {
		return RetrievalEvaluationReport{}, fmt.Errorf("%w: mode must be ANN or EXACT", ErrInvalidRetrievalEvaluation)
	}
	if len(cases) < 1 || len(cases) > MaxRetrievalCases {
		return RetrievalEvaluationReport{}, fmt.Errorf("%w: case count must be between 1 and %d", ErrInvalidRetrievalEvaluation, MaxRetrievalCases)
	}
	normalized := append([]RetrievalEvaluationCase(nil), cases...)
	sort.Slice(normalized, func(left, right int) bool { return normalized[left].CaseID < normalized[right].CaseID })
	overall := newRecallAccumulator()
	byDomain := map[string]*recallAccumulator{}
	byComplexity := map[string]*recallAccumulator{}
	failures := make([]RetrievalFailure, 0)
	for index, evaluationCase := range normalized {
		if err := evaluationCase.Validate(); err != nil {
			return RetrievalEvaluationReport{}, fmt.Errorf("%w: cases[%d]: %v", ErrInvalidRetrievalEvaluation, index, err)
		}
		if index > 0 && normalized[index-1].CaseID == evaluationCase.CaseID {
			return RetrievalEvaluationReport{}, fmt.Errorf("%w: duplicate case %s", ErrInvalidRetrievalEvaluation, evaluationCase.CaseID)
		}
		candidates := evaluationCase.ANN
		if mode == RetrievalModeExact {
			candidates = evaluationCase.Exact
		}
		caseCounts, caseFailures := evaluateRetrievalCase(evaluationCase, candidates)
		mergeRecall(overall, evaluationCase.CaseID, caseCounts)
		mergeRecall(accumulatorForString(byDomain, string(evaluationCase.DomainID)), evaluationCase.CaseID, caseCounts)
		mergeRecall(accumulatorForString(byComplexity, string(evaluationCase.Complexity)), evaluationCase.CaseID, caseCounts)
		failures = append(failures, caseFailures...)
	}
	report := RetrievalEvaluationReport{
		SchemaVersion: RetrievalEvaluationVersion, Mode: mode, CaseCount: len(normalized),
		Overall: summarizeRecall(overall), ByDomain: recallSlices(byDomain),
		ByComplexity: recallSlices(byComplexity), Failures: failures,
	}
	if report.Overall.Metric.Gold == 0 || report.Overall.Dimension.Gold == 0 || report.Overall.Member.Gold == 0 {
		return RetrievalEvaluationReport{}, fmt.Errorf("%w: gold set must cover metric, dimension and member", ErrInvalidRetrievalEvaluation)
	}
	report.Passed = report.Overall.Metric.Passed && report.Overall.Dimension.Passed && report.Overall.Member.Passed
	payload, err := json.Marshal(struct {
		SchemaVersion string                    `json:"schemaVersion"`
		Cases         []RetrievalEvaluationCase `json:"cases"`
	}{RetrievalEvaluationVersion, normalized})
	if err != nil {
		return RetrievalEvaluationReport{}, err
	}
	report.SourceHash = askdata.HashBytes(payload)
	return report, nil
}

func (evaluationCase RetrievalEvaluationCase) Validate() error {
	if evaluationCase.SchemaVersion != RetrievalEvaluationVersion {
		return fmt.Errorf("schemaVersion must be %q", RetrievalEvaluationVersion)
	}
	if err := evaluationCase.CaseID.Validate(); err != nil {
		return fmt.Errorf("caseId: %v", err)
	}
	if err := evaluationCase.DomainID.Validate(); err != nil {
		return fmt.Errorf("domainId: %v", err)
	}
	if !validComplexity(evaluationCase.Complexity) {
		return errors.New("complexity is invalid")
	}
	for label, ids := range map[string][]askdata.ID{
		"metricVersionIds":    evaluationCase.Gold.MetricVersionIDs,
		"dimensionVersionIds": evaluationCase.Gold.DimensionVersionIDs,
		"memberVersionIds":    evaluationCase.Gold.MemberVersionIDs,
	} {
		if err := validateRetrievalIDs(ids); err != nil {
			return fmt.Errorf("gold.%s: %v", label, err)
		}
	}
	if err := validateRetrievalCandidates(evaluationCase.ANN); err != nil {
		return fmt.Errorf("ann: %v", err)
	}
	if err := validateRetrievalCandidates(evaluationCase.Exact); err != nil {
		return fmt.Errorf("exact: %v", err)
	}
	return nil
}

func evaluateRetrievalCase(
	evaluationCase RetrievalEvaluationCase,
	candidates []RetrievalCandidate,
) (recallAccumulator, []RetrievalFailure) {
	counts := *newRecallAccumulator()
	type goldSpec struct {
		kind RetrievalObjectType
		ids  []askdata.ID
		k    int
	}
	specs := []goldSpec{
		{RetrievalMetric, evaluationCase.Gold.MetricVersionIDs, 10},
		{RetrievalDimension, evaluationCase.Gold.DimensionVersionIDs, 10},
		{RetrievalMember, evaluationCase.Gold.MemberVersionIDs, 20},
	}
	failures := make([]RetrievalFailure, 0)
	for _, spec := range specs {
		gold := idSet(spec.ids)
		hits := 0
		for _, candidate := range candidates {
			if candidate.ObjectType == spec.kind && candidate.Rank <= spec.k {
				if _, ok := gold[candidate.VersionID]; ok {
					hits++
					delete(gold, candidate.VersionID)
				}
			}
		}
		switch spec.kind {
		case RetrievalMetric:
			counts.metricGold, counts.metricHit = len(spec.ids), hits
		case RetrievalDimension:
			counts.dimensionGold, counts.dimensionHit = len(spec.ids), hits
		case RetrievalMember:
			counts.memberGold, counts.memberHit = len(spec.ids), hits
		}
		if len(gold) > 0 {
			failures = append(failures, RetrievalFailure{
				CaseID: evaluationCase.CaseID, DomainID: evaluationCase.DomainID,
				Complexity: evaluationCase.Complexity, ObjectType: spec.kind,
			})
		}
	}
	return counts, failures
}

func newRecallAccumulator() *recallAccumulator {
	return &recallAccumulator{cases: map[askdata.ID]struct{}{}}
}

func mergeRecall(target *recallAccumulator, caseID askdata.ID, value recallAccumulator) {
	target.cases[caseID] = struct{}{}
	target.metricGold += value.metricGold
	target.metricHit += value.metricHit
	target.dimensionGold += value.dimensionGold
	target.dimensionHit += value.dimensionHit
	target.memberGold += value.memberGold
	target.memberHit += value.memberHit
}

func summarizeRecall(value *recallAccumulator) RetrievalSummary {
	return RetrievalSummary{
		CaseCount: len(value.cases),
		Metric:    recallScore(10, value.metricGold, value.metricHit, .99),
		Dimension: recallScore(10, value.dimensionGold, value.dimensionHit, .99),
		Member:    recallScore(20, value.memberGold, value.memberHit, .99),
	}
}

func recallScore(k, gold, hit int, threshold float64) RecallScore {
	recall := 1.0
	if gold > 0 {
		recall = float64(hit) / float64(gold)
	}
	return RecallScore{K: k, Gold: gold, Hit: hit, Recall: recall, Passed: gold == 0 || recall >= threshold}
}

func accumulatorForString(values map[string]*recallAccumulator, key string) *recallAccumulator {
	if values[key] == nil {
		values[key] = newRecallAccumulator()
	}
	return values[key]
}

func recallSlices(values map[string]*recallAccumulator) []RetrievalSlice {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]RetrievalSlice, len(keys))
	for index, key := range keys {
		result[index] = RetrievalSlice{Key: key, Summary: summarizeRecall(values[key])}
	}
	return result
}

func validateRetrievalIDs(ids []askdata.ID) error {
	seen := map[askdata.ID]struct{}{}
	for _, id := range ids {
		if err := id.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("duplicate stable version ID")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateRetrievalCandidates(candidates []RetrievalCandidate) error {
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if candidate.ObjectType != RetrievalMetric && candidate.ObjectType != RetrievalDimension && candidate.ObjectType != RetrievalMember {
			return errors.New("objectType is invalid")
		}
		if err := candidate.VersionID.Validate(); err != nil {
			return err
		}
		if candidate.Rank < 1 || candidate.Rank > 100_000 {
			return errors.New("rank is out of bounds")
		}
		key := string(candidate.ObjectType) + "\x00" + string(candidate.VersionID)
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate candidate")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func idSet(ids []askdata.ID) map[askdata.ID]struct{} {
	result := make(map[askdata.ID]struct{}, len(ids))
	for _, id := range ids {
		result[id] = struct{}{}
	}
	return result
}

func ParseRetrievalMode(raw string) (RetrievalMode, error) {
	mode := RetrievalMode(strings.ToUpper(strings.TrimSpace(raw)))
	if mode != RetrievalModeANN && mode != RetrievalModeExact {
		return "", fmt.Errorf("%w: mode must be ANN or EXACT", ErrInvalidRetrievalEvaluation)
	}
	return mode, nil
}
