package search

import (
	"fmt"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

type RetrievalSource string

const (
	SourceExact   RetrievalSource = "EXACT"
	SourceLexical RetrievalSource = "LEXICAL"
	SourceVector  RetrievalSource = "VECTOR"
)

type RawHit struct {
	ObjectType      ObjectType
	ObjectVersionID askdata.ID
	InputHash       askdata.ContentHash
	Score           float64
}

type SourceEvidence struct {
	Source      RetrievalSource     `json:"source"`
	Rank        int                 `json:"rank"`
	SourceScore float64             `json:"sourceScore"`
	Evidence    askdata.EvidenceRef `json:"evidence"`
}

type Candidate struct {
	ObjectType      ObjectType       `json:"objectType"`
	ObjectVersionID askdata.ID       `json:"objectVersionId"`
	Score           float64          `json:"score"`
	Evidence        []SourceEvidence `json:"evidence"`
}

type RankConfig struct {
	RRFConstant   float64
	ExactWeight   float64
	LexicalWeight float64
	VectorWeight  float64
	TopKPerType   int
}

func DefaultRankConfig() RankConfig {
	return RankConfig{RRFConstant: 60, ExactWeight: 4, LexicalWeight: 1, VectorWeight: 1, TopKPerType: 10}
}

func (config RankConfig) Validate() error {
	if config.RRFConstant < 1 || config.RRFConstant > 1_000 ||
		config.ExactWeight <= 0 || config.ExactWeight > 10 ||
		config.LexicalWeight <= 0 || config.LexicalWeight > 10 ||
		config.VectorWeight <= 0 || config.VectorWeight > 10 ||
		config.TopKPerType < 1 || config.TopKPerType > 100 {
		return fmt.Errorf("retrieval rank configuration is invalid")
	}
	return nil
}

func MergeRRF(exact, lexical, vector []RawHit, config RankConfig) ([]Candidate, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	type candidateKey struct {
		objectType ObjectType
		versionID  askdata.ID
	}
	merged := map[candidateKey]*Candidate{}
	for _, source := range []struct {
		name   RetrievalSource
		weight float64
		hits   []RawHit
	}{
		{SourceExact, config.ExactWeight, exact},
		{SourceLexical, config.LexicalWeight, lexical},
		{SourceVector, config.VectorWeight, vector},
	} {
		perTypeRank := map[ObjectType]int{}
		seen := map[candidateKey]struct{}{}
		for _, hit := range source.hits {
			if !validRetrievalObjectType(hit.ObjectType) || hit.Score < 0 {
				return nil, fmt.Errorf("%s hit is invalid", source.name)
			}
			if err := hit.ObjectVersionID.Validate(); err != nil {
				return nil, fmt.Errorf("%s objectVersionId: %w", source.name, err)
			}
			if err := hit.InputHash.Validate(); err != nil {
				return nil, fmt.Errorf("%s inputHash: %w", source.name, err)
			}
			key := candidateKey{hit.ObjectType, hit.ObjectVersionID}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			perTypeRank[hit.ObjectType]++
			rank := perTypeRank[hit.ObjectType]
			candidate := merged[key]
			if candidate == nil {
				candidate = &Candidate{ObjectType: hit.ObjectType, ObjectVersionID: hit.ObjectVersionID}
				merged[key] = candidate
			}
			candidate.Score += source.weight / (config.RRFConstant + float64(rank))
			candidate.Evidence = append(candidate.Evidence, SourceEvidence{
				Source: source.name, Rank: rank, SourceScore: hit.Score,
				Evidence: askdata.EvidenceRef{
					EvidenceID: askdata.ID(fmt.Sprintf("retrieval:%s:%s", source.name, hit.InputHash)),
					Kind:       sourceEvidenceKind(source.name), SourceID: hit.ObjectVersionID,
					ContentHash: hit.InputHash,
				},
			})
		}
	}
	all := make([]Candidate, 0, len(merged))
	for _, candidate := range merged {
		sort.Slice(candidate.Evidence, func(i, j int) bool { return candidate.Evidence[i].Source < candidate.Evidence[j].Source })
		all = append(all, *candidate)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		if all[i].ObjectType != all[j].ObjectType {
			return all[i].ObjectType < all[j].ObjectType
		}
		return all[i].ObjectVersionID < all[j].ObjectVersionID
	})
	counts := map[ObjectType]int{}
	result := make([]Candidate, 0, len(all))
	for _, candidate := range all {
		if counts[candidate.ObjectType] >= config.TopKPerType {
			continue
		}
		counts[candidate.ObjectType]++
		result = append(result, candidate)
	}
	return result, nil
}

func sourceEvidenceKind(source RetrievalSource) askdata.EvidenceKind {
	switch source {
	case SourceExact:
		return askdata.EvidenceKindExactAlias
	case SourceLexical:
		return askdata.EvidenceKindLexicalMatch
	default:
		return askdata.EvidenceKindVectorMatch
	}
}

func validRetrievalObjectType(value ObjectType) bool {
	return value == ObjectMetric || value == ObjectDimension || value == ObjectMember ||
		value == ObjectBusinessTerm || value == ObjectCertifiedExample || value == ObjectReportAsset
}
