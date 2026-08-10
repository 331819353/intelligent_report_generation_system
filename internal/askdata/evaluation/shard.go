package evaluation

import (
	"errors"
	"sort"
	"time"
)

var ErrInvalidShardRotation = errors.New("sealed shard rotation is invalid")

type SealedRunKind string

const (
	SealedRunRegular      SealedRunKind = "REGULAR_RELEASE"
	SealedRunFirst95      SealedRunKind = "FIRST_95_CLAIM"
	SealedRunArchitecture SealedRunKind = "MAJOR_ARCHITECTURE_CHANGE"
	SealedRunAnnual       SealedRunKind = "ANNUAL_REVIEW"
)

type ShardRotationState struct {
	NextShardID int16
	RunCount    int64
}

type SealedRunPlan struct {
	Kind              SealedRunKind
	ShardIDs          []int16
	CanIssue95Percent bool
	NextState         ShardRotationState
}

func PlanSealedRun(state ShardRotationState, available []int16, kind SealedRunKind) (SealedRunPlan, error) {
	if state.NextShardID < 1 || state.NextShardID > SealedShardCount || state.RunCount < 0 {
		return SealedRunPlan{}, ErrInvalidShardRotation
	}
	set := map[int16]struct{}{}
	for _, shardID := range available {
		if shardID < 1 || shardID > SealedShardCount {
			return SealedRunPlan{}, ErrInvalidShardRotation
		}
		set[shardID] = struct{}{}
	}
	full := len(set) == SealedShardCount
	plan := SealedRunPlan{Kind: kind, ShardIDs: []int16{}, NextState: state}
	switch kind {
	case SealedRunRegular:
		if !full {
			return SealedRunPlan{}, ErrInvalidShardRotation
		}
		plan.ShardIDs = []int16{state.NextShardID}
		plan.NextState.NextShardID = state.NextShardID%SealedShardCount + 1
	case SealedRunFirst95, SealedRunArchitecture, SealedRunAnnual:
		if !full {
			return SealedRunPlan{}, errors.New("all four sealed shards are required for a 95 percent conclusion")
		}
		plan.ShardIDs = []int16{1, 2, 3, 4}
		plan.CanIssue95Percent = true
	default:
		return SealedRunPlan{}, ErrInvalidShardRotation
	}
	plan.NextState.RunCount++
	return plan, nil
}

type ShardRetireReason string

const (
	ShardRetiredUsageLimit ShardRetireReason = "USAGE_LIMIT"
	ShardRetiredExposed    ShardRetireReason = "EXPOSED"
	ShardRetiredSuperseded ShardRetireReason = "SUPERSEDED"
)

type SealedShardState struct {
	ShardID      int16
	UsageCount   int
	ExposedAt    *time.Time
	RetiredAt    *time.Time
	RetireReason ShardRetireReason
}

func RecordShardUse(state SealedShardState, usedAt time.Time) (SealedShardState, error) {
	if err := validateSealedShardState(state); err != nil || usedAt.IsZero() || state.RetiredAt != nil {
		return SealedShardState{}, ErrInvalidShardRotation
	}
	state.UsageCount++
	if state.UsageCount > 6 {
		retired := usedAt.UTC()
		state.RetiredAt = &retired
		state.RetireReason = ShardRetiredUsageLimit
	}
	return state, nil
}

func AvailableShardIDs(states []SealedShardState) ([]int16, error) {
	result := make([]int16, 0, len(states))
	seen := map[int16]struct{}{}
	for _, state := range states {
		if err := validateSealedShardState(state); err != nil {
			return nil, err
		}
		if _, duplicate := seen[state.ShardID]; duplicate {
			return nil, ErrInvalidShardRotation
		}
		seen[state.ShardID] = struct{}{}
		if state.RetiredAt == nil {
			result = append(result, state.ShardID)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func validateSealedShardState(state SealedShardState) error {
	if state.ShardID < 1 || state.ShardID > SealedShardCount || state.UsageCount < 0 {
		return ErrInvalidShardRotation
	}
	if state.RetiredAt == nil && state.RetireReason != "" || state.RetiredAt != nil && state.RetireReason == "" {
		return ErrInvalidShardRotation
	}
	if state.RetireReason != "" && state.RetireReason != ShardRetiredUsageLimit &&
		state.RetireReason != ShardRetiredExposed && state.RetireReason != ShardRetiredSuperseded {
		return ErrInvalidShardRotation
	}
	return nil
}
