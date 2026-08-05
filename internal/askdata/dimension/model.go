// Package dimension defines auditable profiling, member indexing and
// normalization contracts. It never stores warehouse credentials or arbitrary
// business rows.
package dimension

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const ProfileSchemaVersion = "1.0"

type ScanBudget struct {
	MaxRows            int64 `json:"maxRows"`
	MaxDistinctValues  int64 `json:"maxDistinctValues"`
	MaxSampleBytes     int64 `json:"maxSampleBytes"`
	StatementTimeoutMS int   `json:"statementTimeoutMs"`
}

func (budget ScanBudget) Validate() error {
	if budget.MaxRows < 1 || budget.MaxRows > 100_000_000 ||
		budget.MaxDistinctValues < 1 || budget.MaxDistinctValues > 1_000_000 ||
		budget.MaxSampleBytes < 1_024 || budget.MaxSampleBytes > 256<<20 ||
		budget.StatementTimeoutMS < 100 || budget.StatementTimeoutMS > 120_000 {
		return errors.New("scan budget exceeds safe bounds")
	}
	return nil
}

type ScanUsage struct {
	RowsScanned      int64 `json:"rowsScanned"`
	DistinctCaptured int64 `json:"distinctCaptured"`
	SampleBytes      int64 `json:"sampleBytes"`
	TimedOut         bool  `json:"timedOut"`
	Truncated        bool  `json:"truncated"`
}

type ReservedValueObservation struct {
	Code                string              `json:"code"`
	NormalizedValueHash askdata.ContentHash `json:"normalizedValueHash"`
	Count               int64               `json:"count"`
}

// Profile records bounded aggregate evidence. Reserved/default observations
// contain only normalized hashes and counts, never raw sensitive member values.
type Profile struct {
	SchemaVersion         string                     `json:"schemaVersion"`
	TenantID              askdata.ID                 `json:"tenantId"`
	DomainID              askdata.ID                 `json:"domainId"`
	DimensionVersionID    askdata.ID                 `json:"dimensionVersionId"`
	Generation            int64                      `json:"generation"`
	SourceSnapshotHash    askdata.ContentHash        `json:"sourceSnapshotHash"`
	Sensitivity           registry.Sensitivity       `json:"sensitivity"`
	HighCardinalityHint   bool                       `json:"highCardinalityHint"`
	RowCount              int64                      `json:"rowCount"`
	NullCount             int64                      `json:"nullCount"`
	DistinctCount         int64                      `json:"distinctCount"`
	PreviousDistinctCount *int64                     `json:"previousDistinctCount"`
	AddedDistinctCount    int64                      `json:"addedDistinctCount"`
	RemovedDistinctCount  int64                      `json:"removedDistinctCount"`
	ReservedValues        []ReservedValueObservation `json:"reservedValues"`
	Budget                ScanBudget                 `json:"budget"`
	Usage                 ScanUsage                  `json:"usage"`
	ProfileHash           askdata.ContentHash        `json:"profileHash"`
}

func NewProfile(input Profile) (Profile, error) {
	input.SchemaVersion = ProfileSchemaVersion
	input.ProfileHash = ""
	input.ReservedValues = append([]ReservedValueObservation(nil), input.ReservedValues...)
	sort.Slice(input.ReservedValues, func(i, j int) bool {
		if input.ReservedValues[i].Code == input.ReservedValues[j].Code {
			return input.ReservedValues[i].NormalizedValueHash < input.ReservedValues[j].NormalizedValueHash
		}
		return input.ReservedValues[i].Code < input.ReservedValues[j].Code
	})
	if err := input.validate(false); err != nil {
		return Profile{}, err
	}
	payload, err := json.Marshal(profileHashPayload(input))
	if err != nil {
		return Profile{}, err
	}
	input.ProfileHash = askdata.HashBytes(payload)
	return input, input.Validate()
}

func (profile Profile) Validate() error { return profile.validate(true) }

func (profile Profile) validate(requireHash bool) error {
	if profile.SchemaVersion != ProfileSchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", ProfileSchemaVersion)
	}
	for name, id := range map[string]askdata.ID{
		"tenantId": profile.TenantID, "domainId": profile.DomainID,
		"dimensionVersionId": profile.DimensionVersionID,
	} {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if err := profile.SourceSnapshotHash.Validate(); err != nil {
		return fmt.Errorf("sourceSnapshotHash: %w", err)
	}
	if profile.Generation < 1 || !validSensitivity(profile.Sensitivity) {
		return errors.New("generation or sensitivity is invalid")
	}
	if profile.RowCount < 0 || profile.NullCount < 0 || profile.NullCount > profile.RowCount ||
		profile.DistinctCount < 0 || profile.DistinctCount > profile.RowCount-profile.NullCount {
		return errors.New("row, NULL and distinct counts are inconsistent")
	}
	if err := profile.Budget.Validate(); err != nil {
		return err
	}
	if profile.Usage.RowsScanned < 0 || profile.Usage.RowsScanned > profile.Budget.MaxRows ||
		profile.Usage.DistinctCaptured < 0 || profile.Usage.DistinctCaptured > profile.Budget.MaxDistinctValues ||
		profile.Usage.SampleBytes < 0 || profile.Usage.SampleBytes > profile.Budget.MaxSampleBytes ||
		profile.Usage.DistinctCaptured > profile.DistinctCount {
		return errors.New("scan usage exceeds its recorded budget or profile counts")
	}
	if profile.Usage.TimedOut && !profile.Usage.Truncated {
		return errors.New("a timed-out scan must be marked truncated")
	}
	if profile.PreviousDistinctCount == nil {
		if profile.AddedDistinctCount != 0 || profile.RemovedDistinctCount != 0 {
			return errors.New("change counts require a previous profile")
		}
	} else {
		previous := *profile.PreviousDistinctCount
		if previous < 0 || profile.AddedDistinctCount < 0 || profile.RemovedDistinctCount < 0 ||
			profile.RemovedDistinctCount > previous ||
			previous+profile.AddedDistinctCount-profile.RemovedDistinctCount != profile.DistinctCount {
			return errors.New("member change counts are inconsistent")
		}
	}
	seenReserved := map[string]struct{}{}
	var reservedCount int64
	for index, observation := range profile.ReservedValues {
		if !stableCode(observation.Code) || observation.Count < 1 {
			return fmt.Errorf("reservedValues[%d] code or count is invalid", index)
		}
		if err := observation.NormalizedValueHash.Validate(); err != nil {
			return fmt.Errorf("reservedValues[%d].normalizedValueHash: %w", index, err)
		}
		identity := observation.Code + "\x00" + string(observation.NormalizedValueHash)
		if _, duplicate := seenReserved[identity]; duplicate {
			return fmt.Errorf("reservedValues[%d] is duplicated", index)
		}
		seenReserved[identity] = struct{}{}
		if reservedCount > math.MaxInt64-observation.Count {
			return errors.New("reserved value count overflows")
		}
		reservedCount += observation.Count
	}
	if reservedCount > profile.RowCount-profile.NullCount {
		return errors.New("reserved value count exceeds non-NULL rows")
	}
	if requireHash {
		if err := profile.ProfileHash.Validate(); err != nil {
			return fmt.Errorf("profileHash: %w", err)
		}
		payload, err := json.Marshal(profileHashPayload(profile))
		if err != nil {
			return err
		}
		if askdata.HashBytes(payload) != profile.ProfileHash {
			return errors.New("profileHash does not match profile content")
		}
	}
	return nil
}

func (profile Profile) NullRatio() float64 {
	if profile.RowCount == 0 {
		return 0
	}
	return float64(profile.NullCount) / float64(profile.RowCount)
}

func (profile Profile) ReservedRatio() float64 {
	if profile.RowCount-profile.NullCount == 0 {
		return 0
	}
	var count int64
	for _, observation := range profile.ReservedValues {
		count += observation.Count
	}
	return float64(count) / float64(profile.RowCount-profile.NullCount)
}

// ChangeRate is a bounded set-change ratio: (added+removed)/(previous+added).
// A first generation has no comparable rate and returns (0,false).
func (profile Profile) ChangeRate() (float64, bool) {
	if profile.PreviousDistinctCount == nil {
		return 0, false
	}
	union := *profile.PreviousDistinctCount + profile.AddedDistinctCount
	if union == 0 {
		return 0, true
	}
	return float64(profile.AddedDistinctCount+profile.RemovedDistinctCount) / float64(union), true
}

func profileHashPayload(profile Profile) any {
	type withoutHash Profile
	payload := withoutHash(profile)
	payload.ProfileHash = ""
	return payload
}

func validSensitivity(value registry.Sensitivity) bool {
	return value == registry.SensitivityPublic || value == registry.SensitivityInternal ||
		value == registry.SensitivityConfidential || value == registry.SensitivityRestricted
}

func stableCode(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 64 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}
