package evaluation

import (
	"context"
	"errors"
	"math/rand"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrInvalidFaultInjection = errors.New("fault injection measurement is invalid")

type FaultType string

const (
	FaultWrongMetric    FaultType = "WRONG_METRIC"
	FaultWrongDimension FaultType = "WRONG_DIMENSION"
	FaultWrongTime      FaultType = "WRONG_TIME"
	FaultWrongJoin      FaultType = "WRONG_JOIN"
)

var governedFaultTypes = map[FaultType]struct{}{
	FaultWrongMetric: {}, FaultWrongDimension: {}, FaultWrongTime: {}, FaultWrongJoin: {},
}

type FaultInjectionCase struct {
	CaseID         askdata.ID
	EligibleFaults []FaultType
}

type FaultTrial struct {
	CaseID askdata.ID
	Fault  FaultType
	Seed   int64
	Index  int
}

type FaultDetector interface {
	Detect(context.Context, FaultTrial) (bool, error)
}

// MeasureRecovery samples otherwise-correct cases using a stable seed and
// measures whether downstream controls catch the injected error. It accepts
// stable case IDs only and therefore cannot leak sealed questions or answers.
func MeasureRecovery(
	ctx context.Context,
	cases []FaultInjectionCase,
	fault FaultType,
	seed int64,
	sampleCount int,
	detector FaultDetector,
) (RecoveryMeasurement, error) {
	if ctx == nil || detector == nil || sampleCount < 1 || sampleCount > MaxFaultInjectionCases {
		return RecoveryMeasurement{}, ErrInvalidFaultInjection
	}
	if _, ok := governedFaultTypes[fault]; !ok {
		return RecoveryMeasurement{}, ErrInvalidFaultInjection
	}
	eligible := make([]FaultInjectionCase, 0, len(cases))
	seen := map[askdata.ID]struct{}{}
	for _, evaluationCase := range cases {
		if evaluationCase.CaseID.Validate() != nil {
			return RecoveryMeasurement{}, ErrInvalidFaultInjection
		}
		if _, duplicate := seen[evaluationCase.CaseID]; duplicate {
			return RecoveryMeasurement{}, ErrInvalidFaultInjection
		}
		seen[evaluationCase.CaseID] = struct{}{}
		for _, candidate := range evaluationCase.EligibleFaults {
			if candidate == fault {
				eligible = append(eligible, evaluationCase)
				break
			}
		}
	}
	if sampleCount > len(eligible) {
		return RecoveryMeasurement{}, ErrInvalidFaultInjection
	}
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].CaseID < eligible[j].CaseID })
	permutation := rand.New(rand.NewSource(seed)).Perm(len(eligible)) // #nosec G404 -- reproducibility is required.
	detected := 0
	for index, selected := range permutation[:sampleCount] {
		caught, err := detector.Detect(ctx, FaultTrial{
			CaseID: eligible[selected].CaseID, Fault: fault, Seed: seed, Index: index,
		})
		if err != nil {
			return RecoveryMeasurement{}, err
		}
		if caught {
			detected++
		}
	}
	measurement := RecoveryMeasurement{Seed: seed, SampleCount: sampleCount, DetectedCount: detected}
	return measurement, measurement.Validate()
}
