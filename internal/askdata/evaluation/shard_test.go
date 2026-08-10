package evaluation

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

func TestAssignSealedShardsIsBalancedAndDeterministic(t *testing.T) {
	cases := sealedSamplingCases()
	left, err := AssignSealedShards(cases, 42)
	if err != nil {
		t.Fatal(err)
	}
	right, err := AssignSealedShards(append([]StratifiedCase(nil), cases...), 42)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatal("same sampling seed produced different assignments")
	}
	pValue, err := ValidateShardDistribution(left)
	if err != nil || pValue <= .05 {
		t.Fatalf("p-value = %v, err = %v", pValue, err)
	}
}

func TestSealedShardRotationAndFourShardClaim(t *testing.T) {
	state := ShardRotationState{NextShardID: 1}
	for _, expected := range []int16{1, 2, 3, 4, 1} {
		plan, err := PlanSealedRun(state, []int16{1, 2, 3, 4}, SealedRunRegular)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.ShardIDs) != 1 || plan.ShardIDs[0] != expected || plan.CanIssue95Percent {
			t.Fatalf("plan = %#v", plan)
		}
		state = plan.NextState
	}
	if _, err := PlanSealedRun(state, []int16{1, 2, 3}, SealedRunFirst95); err == nil {
		t.Fatal("95 percent claim accepted with fewer than four shards")
	}
	full, err := PlanSealedRun(state, []int16{1, 2, 3, 4}, SealedRunFirst95)
	if err != nil || !full.CanIssue95Percent || len(full.ShardIDs) != 4 {
		t.Fatalf("full claim plan = %#v, %v", full, err)
	}
}

func TestUsageSevenAndDetailExposureRetireShard(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	state := SealedShardState{ShardID: 1}
	var err error
	for count := 1; count <= 7; count++ {
		state, err = RecordShardUse(state, now.Add(time.Duration(count)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	if state.RetiredAt == nil || state.RetireReason != ShardRetiredUsageLimit {
		t.Fatalf("usage retirement = %#v", state)
	}
	state = SealedShardState{ShardID: 2}
	state, err = RecordSealedRead(state, SealedReadEvaluator, now)
	if err != nil || state.RetiredAt != nil {
		t.Fatalf("runner read exposed shard: %#v, %v", state, err)
	}
	state, err = RecordSealedRead(state, SealedReadDetails, now.Add(time.Minute))
	if err != nil || state.ExposedAt == nil || state.RetireReason != ShardRetiredExposed {
		t.Fatalf("detail exposure = %#v, %v", state, err)
	}
}

func TestKLDivergenceAlertsOnQuarterlyDistributionDrift(t *testing.T) {
	reference := map[string]float64{"SIMPLE": .5, "RELATIONAL": .3, "SECURITY": .2}
	candidate := map[string]float64{"SIMPLE": .2, "RELATIONAL": .3, "SECURITY": .5}
	alert, divergence, err := DistributionDriftAlert(reference, candidate, .1)
	if err != nil || !alert || divergence <= .1 {
		t.Fatalf("drift = %v, %v, %v", alert, divergence, err)
	}
	if _, _, err := DistributionDriftAlert(reference, map[string]float64{"SIMPLE": 1}, .1); !errors.Is(err, ErrInvalidSealedSampling) {
		t.Fatalf("invalid distribution error = %v", err)
	}
}

func sealedSamplingCases() []StratifiedCase {
	result := make([]StratifiedCase, 0, SealedRequiredCaseCount)
	for index := 0; index < SealedRequiredCaseCount; index++ {
		result = append(result, StratifiedCase{
			CaseID:  askdata.ID(fmt.Sprintf("sealed-case-%04d", index)),
			Stratum: fmt.Sprintf("TYPE_%d", index%5),
		})
	}
	return result
}
