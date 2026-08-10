package search

import "errors"

const ExactVectorCandidateThreshold = 1_000

type VectorSearchRoute string

const (
	VectorRouteANN   VectorSearchRoute = "ANN"
	VectorRouteExact VectorSearchRoute = "EXACT"
)

var ErrInvalidVectorCandidateEstimate = errors.New("vector candidate estimate is invalid")

// RouteVectorSearch keeps HNSW as a performance optimization. Once release,
// tenant, domain and object-type filters leave fewer than 1,000 candidates,
// exact KNN is both bounded and the safer route.
func RouteVectorSearch(candidateEstimate int) (VectorSearchRoute, error) {
	if candidateEstimate < 0 {
		return "", ErrInvalidVectorCandidateEstimate
	}
	if candidateEstimate < ExactVectorCandidateThreshold {
		return VectorRouteExact, nil
	}
	return VectorRouteANN, nil
}
