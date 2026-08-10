package datarequest

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	ClusterWindow    = 30 * 24 * time.Hour
	ClusterThreshold = 3
)

// ClusterObservation is the privacy-safe request evidence used to decide
// whether repeated detail requests should become a governed semantic asset.
// It deliberately excludes result rows, member labels and SQL.
type ClusterObservation struct {
	RequestID       string
	TenantID        string
	DomainID        string
	RequesterUserID string
	MetricIDs       []string
	DimensionIDs    []string
	Grain           string
	BusinessPurpose string
	CreatedAt       time.Time
}

type ClusterCandidate struct {
	TenantID               string              `json:"tenantId"`
	DomainID               string              `json:"domainId"`
	KeyHash                askdata.ContentHash `json:"keyHash"`
	MetricIDs              []string            `json:"metricIds"`
	DimensionIDs           []string            `json:"dimensionIds"`
	Grain                  string              `json:"grain"`
	RequestCount           int                 `json:"requestCount"`
	DistinctRequesterCount int                 `json:"distinctRequesterCount"`
	TypicalPurposes        []string            `json:"typicalBusinessPurposes"`
	FirstSeenAt            time.Time           `json:"firstSeenAt"`
	LastSeenAt             time.Time           `json:"lastSeenAt"`
}

type clusterKey struct {
	TenantID     string   `json:"tenantId"`
	DomainID     string   `json:"domainId"`
	MetricIDs    []string `json:"metricIds"`
	DimensionIDs []string `json:"dimensionIds"`
	Grain        string   `json:"grain"`
}

type clusterAccumulator struct {
	key        clusterKey
	hash       askdata.ContentHash
	requesters map[string]struct{}
	purposes   map[string]int
	count      int
	first      time.Time
	last       time.Time
}

// ClusterRequests returns only newly actionable clusters. A caller passes
// hashes already present in the active-learning queue so repeated runs are
// idempotent; the database uniqueness key remains the final concurrency guard.
func ClusterRequests(
	now time.Time,
	observations []ClusterObservation,
	existing map[askdata.ContentHash]struct{},
) ([]ClusterCandidate, error) {
	if now.IsZero() || len(observations) > 1_000_000 {
		return nil, ErrInvalidRequest
	}
	cutoff := now.UTC().Add(-ClusterWindow)
	groups := map[askdata.ContentHash]*clusterAccumulator{}
	for _, observation := range observations {
		if observation.CreatedAt.Before(cutoff) || observation.CreatedAt.After(now.UTC()) {
			continue
		}
		key, hash, purpose, err := normalizeClusterObservation(observation)
		if err != nil {
			return nil, err
		}
		group := groups[hash]
		if group == nil {
			group = &clusterAccumulator{
				key: key, hash: hash, requesters: map[string]struct{}{},
				purposes: map[string]int{}, first: observation.CreatedAt.UTC(),
			}
			groups[hash] = group
		}
		group.count++
		group.requesters[observation.RequesterUserID] = struct{}{}
		group.purposes[purpose]++
		createdAt := observation.CreatedAt.UTC()
		if createdAt.Before(group.first) {
			group.first = createdAt
		}
		if createdAt.After(group.last) {
			group.last = createdAt
		}
	}

	result := make([]ClusterCandidate, 0, len(groups))
	for hash, group := range groups {
		if group.count < ClusterThreshold {
			continue
		}
		if _, duplicate := existing[hash]; duplicate {
			continue
		}
		result = append(result, ClusterCandidate{
			TenantID: group.key.TenantID, DomainID: group.key.DomainID, KeyHash: hash,
			MetricIDs:    append([]string(nil), group.key.MetricIDs...),
			DimensionIDs: append([]string(nil), group.key.DimensionIDs...), Grain: group.key.Grain,
			RequestCount: group.count, DistinctRequesterCount: len(group.requesters),
			TypicalPurposes: typicalPurposes(group.purposes, 5),
			FirstSeenAt:     group.first, LastSeenAt: group.last,
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].KeyHash < result[right].KeyHash })
	return result, nil
}

func normalizeClusterObservation(
	observation ClusterObservation,
) (clusterKey, askdata.ContentHash, string, error) {
	if uuid.Validate(observation.RequestID) != nil || uuid.Validate(observation.TenantID) != nil ||
		uuid.Validate(observation.DomainID) != nil || uuid.Validate(observation.RequesterUserID) != nil ||
		observation.CreatedAt.IsZero() {
		return clusterKey{}, "", "", ErrInvalidRequest
	}
	metrics, err := normalizedUUIDs(observation.MetricIDs, 20)
	if err != nil {
		return clusterKey{}, "", "", err
	}
	dimensions, err := normalizedUUIDs(observation.DimensionIDs, 30)
	if err != nil {
		return clusterKey{}, "", "", err
	}
	grain := strings.ToUpper(strings.TrimSpace(observation.Grain))
	if !allowedGrain(grain) {
		return clusterKey{}, "", "", ErrInvalidRequest
	}
	purpose := strings.TrimSpace(observation.BusinessPurpose)
	if !boundedText(purpose, 1, 2000) {
		return clusterKey{}, "", "", ErrInvalidRequest
	}
	key := clusterKey{
		TenantID: observation.TenantID, DomainID: observation.DomainID,
		MetricIDs: metrics, DimensionIDs: dimensions, Grain: grain,
	}
	payload, err := json.Marshal(key)
	if err != nil {
		return clusterKey{}, "", "", err
	}
	return key, askdata.HashBytes(payload), purpose, nil
}

func typicalPurposes(counts map[string]int, limit int) []string {
	items := make([]string, 0, len(counts))
	for purpose := range counts {
		items = append(items, purpose)
	}
	sort.Slice(items, func(left, right int) bool {
		if counts[items[left]] != counts[items[right]] {
			return counts[items[left]] > counts[items[right]]
		}
		return items[left] < items[right]
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}
