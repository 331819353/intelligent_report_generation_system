package dimension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const DefaultDimensionProfileLease = 2 * time.Minute

var (
	ErrInvalidProfileWork = errors.New("askdata dimension profile work is invalid")
	ErrWarehouseTimeout   = errors.New("dimension warehouse scan timed out")
	ErrWarehouseDrift     = errors.New("dimension warehouse snapshot drifted")
	ErrProfileSourceStale = errors.New("dimension profile source is no longer current")
)

type WorkerOptions struct {
	Budget      ScanBudget
	Policy      PolicyConfig
	MaxAttempts int
}

func DefaultWorkerOptions() WorkerOptions {
	return WorkerOptions{
		Budget: ScanBudget{
			MaxRows: 100_000, MaxDistinctValues: 10_000,
			MaxSampleBytes: 8 << 20, StatementTimeoutMS: 30_000,
		},
		Policy: DefaultPolicyConfig(), MaxAttempts: 3,
	}
}

func (options WorkerOptions) Validate() error {
	if err := options.Budget.Validate(); err != nil {
		return err
	}
	if err := options.Policy.Validate(); err != nil {
		return err
	}
	if options.MaxAttempts < 1 || options.MaxAttempts > 10 {
		return errors.New("dimension profile max attempts must be between 1 and 10")
	}
	return nil
}

type ScanClaim struct {
	ID, TenantID, DomainID, DimensionVersionID string
	SemanticModelVersionID, DatasetID          string
	DatasetVersionID, MaterializationID        string
	SourceSnapshotHash, DatasetSchemaHash      string
	PublishedSchema, PublishedName, FieldCode  string
	InputHash, LeaseToken                      string
	Generation, ExpectedRowCount               int64
	Sensitivity                                registry.Sensitivity
	MemberIndexPolicy                          registry.MemberIndexPolicy
	HighCardinalityHint                        bool
	Budget                                     ScanBudget
	Attempt, MaxAttempts                       int
	PreviousDistinctCount                      *int64
	PreviousMemberKeyHashes                    []askdata.ContentHash
	PreviousComplete                           bool
}

type RawMember struct {
	Value string
	Count int64
}

type ScanResult struct {
	RowCount    int64
	NullCount   int64
	SampleBytes int64
	RawDistinct int64
	Truncated   bool
	Members     []RawMember
}

type MemberObservation struct {
	DimensionVersionID askdata.ID
	Generation         int64
	MemberKeyHash      askdata.ContentHash
	CanonicalLabel     string
	NormalizedValue    string
	ObservedAliases    []string
	ObservedCount      int64
	Sensitivity        registry.Sensitivity
	EligibleForLLM     bool
	ContentHash        askdata.ContentHash
}

type ProfileStore interface {
	ListTenantIDs(context.Context) ([]string, error)
	Claim(context.Context, string, string, time.Duration, WorkerOptions) (*ScanClaim, error)
	Complete(context.Context, ScanClaim, string, Profile, PolicyDecision, []MemberObservation) error
	Fail(context.Context, ScanClaim, string, string) error
}

type WarehouseScanner interface {
	Scan(context.Context, ScanClaim) (ScanResult, error)
}

type Worker struct {
	store   ProfileStore
	scanner WarehouseScanner
	options WorkerOptions
}

func NewWorker(store ProfileStore, scanner WarehouseScanner, options WorkerOptions) (*Worker, error) {
	if store == nil || scanner == nil {
		return nil, ErrInvalidProfileWork
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}
	return &Worker{store: store, scanner: scanner, options: options}, nil
}

func (worker *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, ErrInvalidProfileWork
	}
	return worker.store.ListTenantIDs(ctx)
}

func (worker *Worker) ProcessNext(
	ctx context.Context, tenantID, workerID string, lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.scanner == nil ||
		uuid.Validate(tenantID) != nil || strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		lease < time.Second || lease > 10*time.Minute {
		return false, ErrInvalidProfileWork
	}
	claim, err := worker.store.Claim(ctx, tenantID, workerID, lease, worker.options)
	if err != nil || claim == nil {
		return false, err
	}
	if err := validateScanClaim(*claim, workerID); err != nil {
		return true, errors.Join(err, worker.store.Fail(ctx, *claim, workerID, "INVALID_SCAN_CLAIM"))
	}

	result, err := worker.scanner.Scan(ctx, *claim)
	if err != nil {
		return true, errors.Join(err, worker.store.Fail(ctx, *claim, workerID, scanErrorCode(err)))
	}
	profile, decision, observations, err := buildProfileResult(*claim, result, worker.options.Policy)
	if err != nil {
		return true, errors.Join(err, worker.store.Fail(ctx, *claim, workerID, "MEMBER_NORMALIZATION_FAILED"))
	}
	if err := worker.store.Complete(ctx, *claim, workerID, profile, decision, observations); err != nil {
		return true, err
	}
	return true, nil
}

func buildProfileResult(
	claim ScanClaim, result ScanResult, policyConfig PolicyConfig,
) (Profile, PolicyDecision, []MemberObservation, error) {
	if result.RowCount < 0 || result.NullCount < 0 || result.NullCount > result.RowCount ||
		result.SampleBytes < 0 || result.SampleBytes > claim.Budget.MaxSampleBytes ||
		result.RawDistinct < 0 || int64(len(result.Members)) > claim.Budget.MaxDistinctValues {
		return Profile{}, PolicyDecision{}, nil, ErrInvalidProfileWork
	}

	type accumulatedMember struct {
		normalized NormalizedMember
		aliases    map[string]struct{}
		count      int64
	}
	catalog := DefaultReservedValueCatalog()
	membersByHash := map[askdata.ContentHash]*accumulatedMember{}
	reservedByKey := map[string]ReservedValueObservation{}
	currentKeys := map[askdata.ContentHash]struct{}{}

	for _, raw := range result.Members {
		if raw.Count < 1 {
			return Profile{}, PolicyDecision{}, nil, ErrInvalidProfileWork
		}
		normalized, reserved, normalizeErr := NormalizeMember(
			askdata.ID(claim.DimensionVersionID), raw.Value, nil, claim.Sensitivity, catalog,
		)
		if errors.Is(normalizeErr, ErrReservedMemberValue) && reserved != nil {
			key := reserved.Code + "\x00" + string(reserved.NormalizedValueHash)
			observation := reservedByKey[key]
			observation.Code = reserved.Code
			observation.NormalizedValueHash = reserved.NormalizedValueHash
			observation.Count += raw.Count
			reservedByKey[key] = observation
			currentKeys[reserved.NormalizedValueHash] = struct{}{}
			continue
		}
		if normalizeErr != nil {
			return Profile{}, PolicyDecision{}, nil, normalizeErr
		}
		currentKeys[normalized.MemberKeyHash] = struct{}{}
		accumulated := membersByHash[normalized.MemberKeyHash]
		if accumulated == nil {
			accumulated = &accumulatedMember{normalized: normalized, aliases: map[string]struct{}{}}
			membersByHash[normalized.MemberKeyHash] = accumulated
		} else if normalized.CanonicalValue != accumulated.normalized.CanonicalValue &&
			len(accumulated.aliases) < 64 {
			accumulated.aliases[normalized.CanonicalValue] = struct{}{}
		}
		accumulated.count += raw.Count
	}

	reserved := make([]ReservedValueObservation, 0, len(reservedByKey))
	for _, observation := range reservedByKey {
		reserved = append(reserved, observation)
	}
	observations := make([]MemberObservation, 0, len(membersByHash))
	for _, accumulated := range membersByHash {
		aliases := make([]string, 0, len(accumulated.aliases))
		for alias := range accumulated.aliases {
			aliases = append(aliases, alias)
		}
		sort.Strings(aliases)
		observation := MemberObservation{
			DimensionVersionID: askdata.ID(claim.DimensionVersionID), Generation: claim.Generation,
			MemberKeyHash:   accumulated.normalized.MemberKeyHash,
			CanonicalLabel:  accumulated.normalized.CanonicalValue,
			NormalizedValue: accumulated.normalized.NormalizedValue,
			ObservedAliases: aliases, ObservedCount: accumulated.count,
			Sensitivity: claim.Sensitivity, EligibleForLLM: accumulated.normalized.EligibleForLLM,
		}
		payload, marshalErr := json.Marshal(struct {
			DimensionVersionID askdata.ID           `json:"dimensionVersionId"`
			Generation         int64                `json:"generation"`
			MemberKeyHash      askdata.ContentHash  `json:"memberKeyHash"`
			CanonicalLabel     string               `json:"canonicalLabel"`
			NormalizedValue    string               `json:"normalizedValue"`
			ObservedAliases    []string             `json:"observedAliases"`
			ObservedCount      int64                `json:"observedCount"`
			Sensitivity        registry.Sensitivity `json:"sensitivity"`
			EligibleForLLM     bool                 `json:"eligibleForLlm"`
		}{
			observation.DimensionVersionID, observation.Generation,
			observation.MemberKeyHash, observation.CanonicalLabel,
			observation.NormalizedValue, observation.ObservedAliases,
			observation.ObservedCount, observation.Sensitivity, observation.EligibleForLLM,
		})
		if marshalErr != nil {
			return Profile{}, PolicyDecision{}, nil, marshalErr
		}
		observation.ContentHash = askdata.HashBytes(payload)
		observations = append(observations, observation)
	}
	sort.Slice(reserved, func(i, j int) bool {
		if reserved[i].Code == reserved[j].Code {
			return reserved[i].NormalizedValueHash < reserved[j].NormalizedValueHash
		}
		return reserved[i].Code < reserved[j].Code
	})
	sort.Slice(observations, func(i, j int) bool { return observations[i].MemberKeyHash < observations[j].MemberKeyHash })

	distinctCount := int64(len(currentKeys))
	var previousDistinctCount *int64
	var added, removed int64
	if !result.Truncated && claim.PreviousComplete && claim.PreviousDistinctCount != nil {
		previousKeys := make(map[askdata.ContentHash]struct{}, len(claim.PreviousMemberKeyHashes))
		for _, key := range claim.PreviousMemberKeyHashes {
			previousKeys[key] = struct{}{}
		}
		if int64(len(previousKeys)) == *claim.PreviousDistinctCount {
			previous := *claim.PreviousDistinctCount
			previousDistinctCount = &previous
			for key := range currentKeys {
				if _, exists := previousKeys[key]; !exists {
					added++
				}
			}
			for key := range previousKeys {
				if _, exists := currentKeys[key]; !exists {
					removed++
				}
			}
		}
	}
	profile, err := NewProfile(Profile{
		TenantID: askdata.ID(claim.TenantID), DomainID: askdata.ID(claim.DomainID),
		DimensionVersionID: askdata.ID(claim.DimensionVersionID), Generation: claim.Generation,
		SourceSnapshotHash: askdata.ContentHash(claim.SourceSnapshotHash), Sensitivity: claim.Sensitivity,
		HighCardinalityHint: claim.HighCardinalityHint || result.Truncated ||
			result.RawDistinct > claim.Budget.MaxDistinctValues,
		RowCount: result.RowCount, NullCount: result.NullCount, DistinctCount: distinctCount,
		PreviousDistinctCount: previousDistinctCount,
		AddedDistinctCount:    added, RemovedDistinctCount: removed,
		ReservedValues: reserved, Budget: claim.Budget,
		Usage: ScanUsage{
			RowsScanned: result.RowCount, DistinctCaptured: distinctCount,
			SampleBytes: result.SampleBytes, Truncated: result.Truncated,
		},
	})
	if err != nil {
		return Profile{}, PolicyDecision{}, nil, err
	}
	decision, err := DecidePolicy(profile, policyConfig)
	if err != nil {
		return Profile{}, PolicyDecision{}, nil, err
	}
	return profile, decision, observations, nil
}

func validateScanClaim(claim ScanClaim, workerID string) error {
	for _, id := range []string{
		claim.ID, claim.TenantID, claim.DomainID, claim.DimensionVersionID,
		claim.SemanticModelVersionID, claim.DatasetID, claim.DatasetVersionID,
		claim.MaterializationID, claim.LeaseToken,
	} {
		if uuid.Validate(id) != nil {
			return ErrInvalidProfileWork
		}
	}
	for _, hash := range []string{claim.SourceSnapshotHash, claim.DatasetSchemaHash, claim.InputHash} {
		if err := askdata.ContentHash(hash).Validate(); err != nil {
			return ErrInvalidProfileWork
		}
	}
	if claim.Generation < 1 || claim.ExpectedRowCount < 0 || claim.Attempt < 1 ||
		claim.Attempt > claim.MaxAttempts || strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		claim.PublishedSchema != "warehouse_published" || strings.TrimSpace(claim.PublishedName) == "" ||
		strings.TrimSpace(claim.FieldCode) == "" || !validSensitivity(claim.Sensitivity) ||
		(claim.MemberIndexPolicy != registry.MemberIndexFull &&
			claim.MemberIndexPolicy != registry.MemberIndexExactOnly &&
			claim.MemberIndexPolicy != registry.MemberIndexOnDemand) {
		return ErrInvalidProfileWork
	}
	return claim.Budget.Validate()
}

func scanErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrWarehouseTimeout):
		return "WAREHOUSE_STATEMENT_TIMEOUT"
	case errors.Is(err, ErrWarehouseDrift):
		return "WAREHOUSE_SNAPSHOT_DRIFT"
	default:
		return "WAREHOUSE_SCAN_FAILED"
	}
}

func validateMemberObservations(claim ScanClaim, observations []MemberObservation) error {
	seen := map[askdata.ContentHash]struct{}{}
	for _, observation := range observations {
		if observation.DimensionVersionID != askdata.ID(claim.DimensionVersionID) ||
			observation.Generation != claim.Generation || observation.ObservedCount < 1 ||
			strings.TrimSpace(observation.CanonicalLabel) == "" || strings.TrimSpace(observation.NormalizedValue) == "" ||
			observation.Sensitivity != claim.Sensitivity || len(observation.ObservedAliases) > 64 {
			return ErrInvalidProfileWork
		}
		if err := observation.MemberKeyHash.Validate(); err != nil {
			return ErrInvalidProfileWork
		}
		if err := observation.ContentHash.Validate(); err != nil {
			return ErrInvalidProfileWork
		}
		if _, duplicate := seen[observation.MemberKeyHash]; duplicate {
			return ErrInvalidProfileWork
		}
		seen[observation.MemberKeyHash] = struct{}{}
		if observation.EligibleForLLM &&
			(observation.Sensitivity == registry.SensitivityConfidential ||
				observation.Sensitivity == registry.SensitivityRestricted) {
			return ErrInvalidProfileWork
		}
	}
	return nil
}

func profileResultMatchesClaim(claim ScanClaim, profile Profile, decision PolicyDecision) error {
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("profile: %w", err)
	}
	if err := decision.Validate(); err != nil {
		return fmt.Errorf("policy decision: %w", err)
	}
	if profile.TenantID != askdata.ID(claim.TenantID) || profile.DomainID != askdata.ID(claim.DomainID) ||
		profile.DimensionVersionID != askdata.ID(claim.DimensionVersionID) ||
		profile.Generation != claim.Generation || profile.SourceSnapshotHash != askdata.ContentHash(claim.SourceSnapshotHash) ||
		profile.Sensitivity != claim.Sensitivity || profile.Budget != claim.Budget ||
		decision.ProfileHash != profile.ProfileHash {
		return ErrInvalidProfileWork
	}
	return nil
}
