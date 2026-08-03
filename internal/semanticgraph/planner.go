package semanticgraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// RankJoinPaths applies the documented risk cost outside nGQL so planning is
// deterministic and remains stable across NebulaGraph upgrades.
func RankJoinPaths(candidates []JoinPath, limit int) []JoinPath {
	if limit < 1 || limit > MaximumJoinPaths {
		limit = MaximumJoinPaths
	}
	unique := make(map[string]JoinPath, len(candidates))
	for _, candidate := range candidates {
		if len(candidate.Edges) < 1 || len(candidate.Edges) > MaximumOnlineHops ||
			len(candidate.VIDs) != len(candidate.Edges)+1 {
			continue
		}
		valid := true
		candidate.Cost = 0
		for _, edge := range candidate.Edges {
			if !edge.Certified || !edge.AllowedForQuery ||
				strings.TrimSpace(edge.Cardinality) == "" ||
				strings.EqualFold(edge.Cardinality, "unknown") {
				valid = false
				break
			}
			candidate.Cost += edge.BaseCost + edge.FanoutPenalty +
				edge.StalePenalty + edge.CrossSourcePenalty + edge.PolicyPenalty
		}
		if !valid {
			continue
		}
		candidate.PathHash = joinPathHash(candidate)
		if previous, exists := unique[candidate.PathHash]; !exists || candidate.Cost < previous.Cost {
			unique[candidate.PathHash] = candidate
		}
	}
	result := make([]JoinPath, 0, len(unique))
	for _, candidate := range unique {
		result = append(result, candidate)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Cost != result[right].Cost {
			return result[left].Cost < result[right].Cost
		}
		if len(result[left].Edges) != len(result[right].Edges) {
			return len(result[left].Edges) < len(result[right].Edges)
		}
		return result[left].PathHash < result[right].PathHash
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func joinPathHash(path JoinPath) string {
	payload := struct {
		VIDs      []string `json:"vids"`
		Relations []string `json:"relations"`
	}{VIDs: path.VIDs, Relations: make([]string, 0, len(path.Edges))}
	for _, edge := range path.Edges {
		payload.Relations = append(payload.Relations, edge.RelationID)
	}
	encoded, _ := json.Marshal(payload)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}
