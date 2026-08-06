package ircontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
)

// Normalize returns a deep-enough copy with deterministic set ordering. The
// order of Sort is preserved because it defines multi-column sort precedence.
func Normalize(value SemanticIR) SemanticIR {
	normalized := value
	normalized.Metrics = append([]Metric(nil), value.Metrics...)
	normalized.GroupBy = append([]GroupBy(nil), value.GroupBy...)
	normalized.Filters = make([]Filter, len(value.Filters))
	for index, filter := range value.Filters {
		normalized.Filters[index] = filter
		normalized.Filters[index].MemberVersionIDs = append([]askdata.ID(nil), filter.MemberVersionIDs...)
		sort.Slice(normalized.Filters[index].MemberVersionIDs, func(left, right int) bool {
			return strings.Compare(string(normalized.Filters[index].MemberVersionIDs[left]), string(normalized.Filters[index].MemberVersionIDs[right])) < 0
		})
	}
	normalized.Sort = append([]Sort(nil), value.Sort...)
	if normalized.Metrics == nil {
		normalized.Metrics = []Metric{}
	}
	if normalized.GroupBy == nil {
		normalized.GroupBy = []GroupBy{}
	}
	if normalized.Filters == nil {
		normalized.Filters = []Filter{}
	}
	if normalized.Sort == nil {
		normalized.Sort = []Sort{}
	}
	sort.Slice(normalized.Metrics, func(left, right int) bool {
		if normalized.Metrics[left].MetricVersionID == normalized.Metrics[right].MetricVersionID {
			return normalized.Metrics[left].Alias < normalized.Metrics[right].Alias
		}
		return normalized.Metrics[left].MetricVersionID < normalized.Metrics[right].MetricVersionID
	})
	sort.Slice(normalized.GroupBy, func(left, right int) bool {
		if normalized.GroupBy[left].DimensionVersionID == normalized.GroupBy[right].DimensionVersionID {
			return grainValue(normalized.GroupBy[left].Grain) < grainValue(normalized.GroupBy[right].Grain)
		}
		return normalized.GroupBy[left].DimensionVersionID < normalized.GroupBy[right].DimensionVersionID
	})
	sort.Slice(normalized.Filters, func(left, right int) bool {
		if normalized.Filters[left].DimensionVersionID == normalized.Filters[right].DimensionVersionID {
			return normalized.Filters[left].Operator < normalized.Filters[right].Operator
		}
		return normalized.Filters[left].DimensionVersionID < normalized.Filters[right].DimensionVersionID
	})
	return normalized
}

// Canonicalize validates the normalized IR and returns stable JSON and hash.
func Canonicalize(value SemanticIR) (SemanticIR, []byte, askdata.ContentHash, error) {
	normalized := Normalize(value)
	if err := normalized.Validate(); err != nil {
		return SemanticIR{}, nil, "", err
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return SemanticIR{}, nil, "", fmt.Errorf("marshal semantic IR: %w", err)
	}
	sum := sha256.Sum256(raw)
	return normalized, raw, askdata.ContentHash(hex.EncodeToString(sum[:])), nil
}

func grainValue(value *TimeGrain) string {
	if value == nil {
		return ""
	}
	return string(*value)
}
