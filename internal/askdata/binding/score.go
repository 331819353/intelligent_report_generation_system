package binding

import (
	"math"

	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/search"
)

const (
	retrievalWeight = 0.30
	exactWeight     = 0.10
	lexicalWeight   = 0.05
	vectorWeight    = 0.05
	reviewerWeight  = 0.10
	ruleWeight      = 0.10
	qualityWeight   = 0.10
	graphWeight     = 0.15
	costWeight      = 0.05
)

// Score contains only deterministic system features. It deliberately has no
// LLM-reported confidence; NLU-006 calibrates probability from these values
// and held-out labels.
type Score struct {
	Retrieval   float64 `json:"retrieval"`
	Exact       float64 `json:"exact"`
	Lexical     float64 `json:"lexical"`
	Vector      float64 `json:"vector"`
	Reviewer    float64 `json:"reviewer"`
	Rule        float64 `json:"rule"`
	Quality     float64 `json:"quality"`
	Graph       float64 `json:"graph"`
	Cost        float64 `json:"cost"`
	RiskPenalty float64 `json:"riskPenalty"`
	Total       float64 `json:"total"`
}

type candidateScore struct {
	retrieval, exact, lexical, vector, reviewer, rule, quality, cost float64
}

func scoreCandidate(option CandidateOption) candidateScore {
	reviewer := 0.0
	if option.SelectionSource == SelectionLLMRerank && option.ReviewerRank > 0 {
		reviewer = 1 / float64(option.ReviewerRank)
	}
	exact := 0.0
	if hasRetrievalSource(option.Candidate, search.SourceExact) {
		exact = 1
	}
	lexical := retrievalSourceScore(option.Candidate, search.SourceLexical)
	vector := retrievalSourceScore(option.Candidate, search.SourceVector)
	return candidateScore{
		retrieval: saturatingScore(option.Candidate.Score), exact: exact,
		lexical: lexical, vector: vector,
		reviewer: reviewer, rule: option.RuleScore, quality: option.QualityScore,
		cost: option.CostScore,
	}
}

func scoreSelections(selections []selectedCandidate, path *graph.JoinPath) Score {
	result := Score{}
	for _, selection := range selections {
		value := scoreCandidate(selection.option)
		result.Retrieval += value.retrieval
		result.Exact += value.exact
		result.Lexical += value.lexical
		result.Vector += value.vector
		result.Reviewer += value.reviewer
		result.Rule += value.rule
		result.Quality += value.quality
		result.Cost += value.cost
	}
	if len(selections) > 0 {
		count := float64(len(selections))
		result.Retrieval /= count
		result.Exact /= count
		result.Lexical /= count
		result.Vector /= count
		result.Reviewer /= count
		result.Rule /= count
		result.Quality /= count
		result.Cost /= count
	}
	result.Graph = 1
	if path != nil {
		result.Graph = math.Max(0, 1-0.05*float64(len(path.Steps)-1))
		for _, risk := range path.RiskCodes {
			switch risk {
			case graph.JoinRiskOneToMany:
				result.RiskPenalty += 0.04
			case graph.JoinRiskManyToMany:
				result.RiskPenalty += 0.10
			case graph.JoinRiskPreaggregation:
				result.RiskPenalty += 0.03
			case graph.JoinRiskFanoutBlocked:
				result.RiskPenalty += 1
			}
		}
	}
	result.Total = retrievalWeight*result.Retrieval + exactWeight*result.Exact +
		lexicalWeight*result.Lexical + vectorWeight*result.Vector +
		reviewerWeight*result.Reviewer + ruleWeight*result.Rule +
		qualityWeight*result.Quality + graphWeight*result.Graph +
		costWeight*result.Cost - result.RiskPenalty
	result.Total = roundScore(math.Max(0, math.Min(1, result.Total)))
	result.Retrieval = roundScore(result.Retrieval)
	result.Exact = roundScore(result.Exact)
	result.Lexical = roundScore(result.Lexical)
	result.Vector = roundScore(result.Vector)
	result.Reviewer = roundScore(result.Reviewer)
	result.Rule = roundScore(result.Rule)
	result.Quality = roundScore(result.Quality)
	result.Graph = roundScore(result.Graph)
	result.Cost = roundScore(result.Cost)
	result.RiskPenalty = roundScore(result.RiskPenalty)
	return result
}

func partialSelectionScore(selections []selectedCandidate) float64 {
	if len(selections) == 0 {
		return 0
	}
	total := 0.0
	for _, selection := range selections {
		value := scoreCandidate(selection.option)
		total += retrievalWeight*value.retrieval + exactWeight*value.exact +
			lexicalWeight*value.lexical + vectorWeight*value.vector +
			reviewerWeight*value.reviewer + ruleWeight*value.rule +
			qualityWeight*value.quality + costWeight*value.cost
	}
	return roundScore(total / float64(len(selections)))
}

func retrievalSourceScore(candidate search.Candidate, source search.RetrievalSource) float64 {
	for _, evidence := range candidate.Evidence {
		if evidence.Source == source {
			return saturatingScore(evidence.SourceScore)
		}
	}
	return 0
}

func saturatingScore(value float64) float64 {
	if value <= 0 {
		return 0
	}
	return value / (1 + value)
}

func roundScore(value float64) float64 {
	return math.Round(value*1_000_000_000) / 1_000_000_000
}
