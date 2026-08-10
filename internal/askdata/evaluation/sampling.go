package evaluation

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	SealedShardCount        = 4
	SealedCasesPerShard     = 500
	SealedRequiredCaseCount = SealedShardCount * SealedCasesPerShard
)

var ErrInvalidSealedSampling = errors.New("sealed evaluation sampling is invalid")

type StratifiedCase struct {
	CaseID  askdata.ID
	Stratum string
}

type ShardAssignment struct {
	CaseID  askdata.ID `json:"caseId"`
	Stratum string     `json:"stratum"`
	ShardID int16      `json:"shardId"`
}

// AssignSealedShards performs deterministic stratified assignment while
// enforcing the governed 4x500 capacity. The seed is an audit input, not a
// source of secrecy.
func AssignSealedShards(cases []StratifiedCase, seed int64) ([]ShardAssignment, error) {
	if len(cases) != SealedRequiredCaseCount {
		return nil, ErrInvalidSealedSampling
	}
	groups := map[string][]StratifiedCase{}
	seen := map[askdata.ID]struct{}{}
	for _, evaluationCase := range cases {
		if evaluationCase.CaseID.Validate() != nil || !stableStratum(evaluationCase.Stratum) {
			return nil, ErrInvalidSealedSampling
		}
		if _, duplicate := seen[evaluationCase.CaseID]; duplicate {
			return nil, ErrInvalidSealedSampling
		}
		seen[evaluationCase.CaseID] = struct{}{}
		groups[evaluationCase.Stratum] = append(groups[evaluationCase.Stratum], evaluationCase)
	}
	strata := make([]string, 0, len(groups))
	for stratum := range groups {
		strata = append(strata, stratum)
	}
	sort.Strings(strata)
	loads := [SealedShardCount]int{}
	assignments := make([]ShardAssignment, 0, len(cases))
	for _, stratum := range strata {
		group := groups[stratum]
		sort.Slice(group, func(i, j int) bool {
			left := seededCaseOrder(seed, stratum, group[i].CaseID)
			right := seededCaseOrder(seed, stratum, group[j].CaseID)
			if left != right {
				return left < right
			}
			return group[i].CaseID < group[j].CaseID
		})
		start := int(seededCaseOrder(seed, stratum, "start") % SealedShardCount)
		for index, evaluationCase := range group {
			preferred := (start + index) % SealedShardCount
			shard := nextShardWithCapacity(loads, preferred)
			if shard < 0 {
				return nil, ErrInvalidSealedSampling
			}
			loads[shard]++
			assignments = append(assignments, ShardAssignment{
				CaseID: evaluationCase.CaseID, Stratum: stratum, ShardID: int16(shard + 1),
			})
		}
	}
	for _, count := range loads {
		if count != SealedCasesPerShard {
			return nil, ErrInvalidSealedSampling
		}
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].CaseID < assignments[j].CaseID })
	return assignments, nil
}

func ValidateShardDistribution(assignments []ShardAssignment) (float64, error) {
	if len(assignments) != SealedRequiredCaseCount {
		return 0, ErrInvalidSealedSampling
	}
	counts := map[string][SealedShardCount]int{}
	shardTotals := [SealedShardCount]int{}
	seen := map[askdata.ID]struct{}{}
	for _, assignment := range assignments {
		if assignment.CaseID.Validate() != nil || !stableStratum(assignment.Stratum) ||
			assignment.ShardID < 1 || assignment.ShardID > SealedShardCount {
			return 0, ErrInvalidSealedSampling
		}
		if _, duplicate := seen[assignment.CaseID]; duplicate {
			return 0, ErrInvalidSealedSampling
		}
		seen[assignment.CaseID] = struct{}{}
		row := counts[assignment.Stratum]
		row[assignment.ShardID-1]++
		counts[assignment.Stratum] = row
		shardTotals[assignment.ShardID-1]++
	}
	for _, count := range shardTotals {
		if count != SealedCasesPerShard {
			return 0, ErrInvalidSealedSampling
		}
	}
	if len(counts) < 2 {
		return 1, nil
	}
	chiSquare := 0.0
	for _, row := range counts {
		rowTotal := 0
		for _, count := range row {
			rowTotal += count
		}
		for shard, observed := range row {
			expected := float64(rowTotal*shardTotals[shard]) / float64(len(assignments))
			if expected == 0 {
				return 0, ErrInvalidSealedSampling
			}
			difference := float64(observed) - expected
			chiSquare += difference * difference / expected
		}
	}
	degrees := (len(counts) - 1) * (SealedShardCount - 1)
	return regularizedGammaQ(float64(degrees)/2, chiSquare/2), nil
}

func KLDivergence(reference, candidate map[string]float64) (float64, error) {
	if len(reference) == 0 || len(reference) != len(candidate) {
		return 0, ErrInvalidSealedSampling
	}
	totalReference, totalCandidate := 0.0, 0.0
	for key, referenceValue := range reference {
		candidateValue, exists := candidate[key]
		if !exists || referenceValue <= 0 || candidateValue <= 0 || math.IsNaN(referenceValue) || math.IsNaN(candidateValue) {
			return 0, ErrInvalidSealedSampling
		}
		totalReference += referenceValue
		totalCandidate += candidateValue
	}
	if math.Abs(totalReference-1) > 1e-9 || math.Abs(totalCandidate-1) > 1e-9 {
		return 0, ErrInvalidSealedSampling
	}
	divergence := 0.0
	for key, value := range candidate {
		divergence += value * math.Log(value/reference[key])
	}
	return divergence, nil
}

func DistributionDriftAlert(reference, candidate map[string]float64, threshold float64) (bool, float64, error) {
	if threshold <= 0 || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
		return false, 0, ErrInvalidSealedSampling
	}
	divergence, err := KLDivergence(reference, candidate)
	if err != nil {
		return false, 0, err
	}
	return divergence > threshold, divergence, nil
}

func nextShardWithCapacity(loads [SealedShardCount]int, preferred int) int {
	for offset := 0; offset < SealedShardCount; offset++ {
		candidate := (preferred + offset) % SealedShardCount
		if loads[candidate] < SealedCasesPerShard {
			return candidate
		}
	}
	return -1
}

func seededCaseOrder(seed int64, stratum string, id askdata.ID) uint64 {
	var seedBytes [8]byte
	binary.BigEndian.PutUint64(seedBytes[:], uint64(seed))
	digest := sha256.Sum256(append(append(seedBytes[:], []byte(stratum)...), []byte("|"+string(id))...))
	return binary.BigEndian.Uint64(digest[:8])
}

func stableStratum(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

// regularizedGammaQ evaluates the chi-square survival function without adding
// a heavyweight statistics dependency.
func regularizedGammaQ(a, x float64) float64 {
	if x < 0 || a <= 0 {
		return math.NaN()
	}
	if x == 0 {
		return 1
	}
	if x < a+1 {
		sum, term := 1/a, 1/a
		for n := 1; n <= 200; n++ {
			term *= x / (a + float64(n))
			sum += term
			if math.Abs(term) < math.Abs(sum)*1e-14 {
				break
			}
		}
		logGamma, _ := math.Lgamma(a)
		p := sum * math.Exp(-x+a*math.Log(x)-logGamma)
		return math.Max(0, math.Min(1, 1-p))
	}
	b := x + 1 - a
	c := 1 / 1e-300
	d := 1 / b
	h := d
	for n := 1; n <= 200; n++ {
		an := -float64(n) * (float64(n) - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < 1e-300 {
			d = 1e-300
		}
		c = b + an/c
		if math.Abs(c) < 1e-300 {
			c = 1e-300
		}
		d = 1 / d
		delta := d * c
		h *= delta
		if math.Abs(delta-1) < 1e-14 {
			break
		}
	}
	logGamma, _ := math.Lgamma(a)
	return math.Max(0, math.Min(1, math.Exp(-x+a*math.Log(x)-logGamma)*h))
}
