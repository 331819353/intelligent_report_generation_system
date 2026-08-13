// Package disasterrecovery holds the governed contract for a restore drill.
//
// The scripts in scripts/ can already take a backup, refuse a non-empty restore
// target, compare the restored release manifest and re-prove the graph
// projection. What none of them could do is answer the question the drill
// exists to answer: was the recovery ACCEPTED. A backup that completes is not a
// recovery that works, and a restore that completes is not a recovery that was
// verified — so the receipt below is the artifact, and its verdict is always
// recomputed from the stages rather than read from the file.
package disasterrecovery

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const ReceiptSchemaVersion = "askdata-dr-drill-receipt-v1"

// SignabilityNotSigned mirrors the capacity contract: a drill that is missing
// any input its conclusion depends on reports one fixed code, never a partial
// pass. RTO and RPO are commitments about a specific environment; a drill on a
// laptop can prove the procedure and can never prove the commitment.
const (
	SignabilityNotSigned = "DRILL_NOT_SIGNED"
	SignabilitySignable  = "SIGNABLE"
)

var ErrInvalidDrillReceipt = errors.New("disaster recovery drill receipt is invalid")

// DrillStage is the recovery order from 04 §6.2. The order is part of the
// contract, not a suggestion: verifying the foreign-key closure before the
// migrations are checked proves nothing, and unfreezing writes before the
// business smoke test turns a drill into an outage.
type DrillStage string

const (
	StageFreezeWrites          DrillStage = "FREEZE_WRITES"
	StageRestoreControlData    DrillStage = "RESTORE_CONTROL_DATABASE"
	StageVerifyMigrationsRLS   DrillStage = "VERIFY_MIGRATIONS_AND_RLS"
	StageVerifyForeignKeys     DrillStage = "VERIFY_FOREIGN_KEY_CLOSURE"
	StageVerifyImmutableEvents DrillStage = "VERIFY_IMMUTABLE_EVENT_HASHES"
	StageRestoreObjectStorage  DrillStage = "RESTORE_OBJECT_STORAGE"
	StageVerifyWarehouse       DrillStage = "VERIFY_WAREHOUSE_SNAPSHOT_READONLY"
	StageRebuildProjections    DrillStage = "REBUILD_SEARCH_AND_GRAPH_PROJECTIONS"
	StageRestartWorkers        DrillStage = "RESTART_WORKERS_IN_TIERS"
	StageBusinessSmoke         DrillStage = "BUSINESS_SMOKE_AND_UNFREEZE"
)

// orderedDrillStages is the single source of truth for the recovery order.
var orderedDrillStages = []DrillStage{
	StageFreezeWrites, StageRestoreControlData, StageVerifyMigrationsRLS,
	StageVerifyForeignKeys, StageVerifyImmutableEvents, StageRestoreObjectStorage,
	StageVerifyWarehouse, StageRebuildProjections, StageRestartWorkers, StageBusinessSmoke,
}

func OrderedDrillStages() []DrillStage {
	return append([]DrillStage(nil), orderedDrillStages...)
}

// DrillStageResult records one stage. EvidenceReference points at the artifact
// that makes the stage checkable by someone who was not in the room — a restore
// script transcript, a graph rebuild proof, a smoke-test run ID.
type DrillStageResult struct {
	Stage             DrillStage `json:"stage"`
	Passed            bool       `json:"passed"`
	StartedAt         time.Time  `json:"startedAt"`
	CompletedAt       time.Time  `json:"completedAt"`
	EvidenceReference string     `json:"evidenceReference"`
	FailureCode       string     `json:"failureCode,omitempty"`
}

// DrillObjective is the commitment the drill is measured against. Without a
// declared target there is no such thing as a passing RTO — only a duration.
type DrillObjective struct {
	TargetEnvironment      string `json:"targetEnvironment"`
	RecoveryTimeBudgetSec  int64  `json:"recoveryTimeBudgetSeconds"`
	RecoveryPointBudgetSec int64  `json:"recoveryPointBudgetSeconds"`
}

type DrillSignoff struct {
	OwnerActorID string `json:"ownerActorId"`
	SignedAt     string `json:"signedAt"`
	Notes        string `json:"notes"`
}

type DrillSignability struct {
	Verdict       string   `json:"verdict"`
	MissingInputs []string `json:"missingInputs"`
}

// DrillReceipt is the whole record. BackupSHA256 ties it to one backup
// directory: a receipt that does not name what it restored cannot be audited.
type DrillReceipt struct {
	SchemaVersion string             `json:"schemaVersion"`
	DrillID       string             `json:"drillId"`
	BackupSHA256  string             `json:"backupSha256"`
	Objective     *DrillObjective    `json:"objective,omitempty"`
	Stages        []DrillStageResult `json:"stages"`
	// LastDurableWriteAt is the newest business fact the restored control plane
	// contains. The recovery point is measured from it, not from when the
	// backup file was written.
	LastDurableWriteAt time.Time     `json:"lastDurableWriteAt"`
	IncidentDetectedAt time.Time     `json:"incidentDetectedAt"`
	Signoff            *DrillSignoff `json:"signoff,omitempty"`
	// Measured values and the verdict are recomputed by Validate.
	RecoveryTimeSeconds  int64            `json:"recoveryTimeSeconds"`
	RecoveryPointSeconds int64            `json:"recoveryPointSeconds"`
	Signability          DrillSignability `json:"signability"`
}

// MeasuredRecovery returns the RTO and RPO the stages actually demonstrate.
// RTO runs from incident detection to the last stage that passed — measuring to
// the end of the last stage ATTEMPTED would credit recovery time to a step that
// failed.
func (receipt DrillReceipt) MeasuredRecovery() (recoveryTimeSeconds, recoveryPointSeconds int64) {
	completedAt := time.Time{}
	for _, stage := range receipt.Stages {
		if stage.Passed && stage.CompletedAt.After(completedAt) {
			completedAt = stage.CompletedAt
		}
	}
	if !receipt.IncidentDetectedAt.IsZero() && completedAt.After(receipt.IncidentDetectedAt) {
		recoveryTimeSeconds = int64(completedAt.Sub(receipt.IncidentDetectedAt).Seconds())
	}
	if !receipt.IncidentDetectedAt.IsZero() && !receipt.LastDurableWriteAt.IsZero() &&
		receipt.IncidentDetectedAt.After(receipt.LastDurableWriteAt) {
		recoveryPointSeconds = int64(receipt.IncidentDetectedAt.Sub(receipt.LastDurableWriteAt).Seconds())
	}
	return recoveryTimeSeconds, recoveryPointSeconds
}

// EvaluateDrillSignability decides whether the receipt may be presented as a
// recovery acceptance. Every stage must be present, in order, and passed;
// stopping early is a failed drill even when nothing errored.
func EvaluateDrillSignability(receipt DrillReceipt) DrillSignability {
	missing := make([]string, 0, 8)
	recorded := make(map[DrillStage]DrillStageResult, len(receipt.Stages))
	for _, stage := range receipt.Stages {
		recorded[stage.Stage] = stage
	}
	for _, stage := range orderedDrillStages {
		result, present := recorded[stage]
		switch {
		case !present:
			missing = append(missing, "stages."+string(stage))
		case !result.Passed:
			missing = append(missing, "stages."+string(stage)+".passed")
		case strings.TrimSpace(result.EvidenceReference) == "":
			missing = append(missing, "stages."+string(stage)+".evidenceReference")
		}
	}
	if strings.TrimSpace(receipt.BackupSHA256) == "" {
		missing = append(missing, "backupSha256")
	}
	if receipt.IncidentDetectedAt.IsZero() {
		missing = append(missing, "incidentDetectedAt")
	}
	if receipt.LastDurableWriteAt.IsZero() {
		missing = append(missing, "lastDurableWriteAt")
	}
	if receipt.Objective == nil || strings.TrimSpace(receipt.Objective.TargetEnvironment) == "" ||
		receipt.Objective.RecoveryTimeBudgetSec <= 0 || receipt.Objective.RecoveryPointBudgetSec <= 0 {
		missing = append(missing, "objective")
	} else {
		recoveryTime, recoveryPoint := receipt.MeasuredRecovery()
		if recoveryTime <= 0 || recoveryTime > receipt.Objective.RecoveryTimeBudgetSec {
			missing = append(missing, "objective.recoveryTimeBudgetSeconds")
		}
		if recoveryPoint > receipt.Objective.RecoveryPointBudgetSec {
			missing = append(missing, "objective.recoveryPointBudgetSeconds")
		}
	}
	if receipt.Signoff == nil || strings.TrimSpace(receipt.Signoff.OwnerActorID) == "" ||
		strings.TrimSpace(receipt.Signoff.SignedAt) == "" {
		missing = append(missing, "signoff")
	}
	sort.Strings(missing)
	verdict := SignabilitySignable
	if len(missing) != 0 {
		verdict = SignabilityNotSigned
	}
	return DrillSignability{Verdict: verdict, MissingInputs: missing}
}

// Validate checks the receipt's internal consistency and recomputes every
// derived value. It never trusts a supplied duration or verdict.
func (receipt *DrillReceipt) Validate() error {
	if receipt.SchemaVersion != ReceiptSchemaVersion {
		return fmt.Errorf("%w: schemaVersion must be %q", ErrInvalidDrillReceipt, ReceiptSchemaVersion)
	}
	if strings.TrimSpace(receipt.DrillID) == "" || len(receipt.DrillID) > 128 {
		return fmt.Errorf("%w: drillId is required", ErrInvalidDrillReceipt)
	}
	if len(receipt.Stages) == 0 || len(receipt.Stages) > len(orderedDrillStages) {
		return fmt.Errorf("%w: stage count", ErrInvalidDrillReceipt)
	}
	position := make(map[DrillStage]int, len(orderedDrillStages))
	for index, stage := range orderedDrillStages {
		position[stage] = index
	}
	previous := -1
	failed := false
	seen := make(map[DrillStage]struct{}, len(receipt.Stages))
	for _, result := range receipt.Stages {
		index, governed := position[result.Stage]
		if !governed {
			return fmt.Errorf("%w: stage %s is not governed", ErrInvalidDrillReceipt, result.Stage)
		}
		if _, duplicate := seen[result.Stage]; duplicate {
			return fmt.Errorf("%w: stage %s is duplicated", ErrInvalidDrillReceipt, result.Stage)
		}
		seen[result.Stage] = struct{}{}
		if index <= previous {
			return fmt.Errorf("%w: stage %s is out of recovery order", ErrInvalidDrillReceipt, result.Stage)
		}
		if index != previous+1 {
			return fmt.Errorf("%w: stage %s skipped an earlier stage", ErrInvalidDrillReceipt, result.Stage)
		}
		previous = index
		if result.StartedAt.IsZero() || result.CompletedAt.IsZero() ||
			result.CompletedAt.Before(result.StartedAt) {
			return fmt.Errorf("%w: stage %s timing", ErrInvalidDrillReceipt, result.Stage)
		}
		// A stage that ran after a failure is not evidence of recovery: the
		// ordering exists precisely so a later check cannot be read as covering
		// an earlier one that did not hold.
		if failed && result.Passed {
			return fmt.Errorf(
				"%w: stage %s reports success after an earlier stage failed", ErrInvalidDrillReceipt, result.Stage,
			)
		}
		if !result.Passed {
			failed = true
			if strings.TrimSpace(result.FailureCode) == "" {
				return fmt.Errorf("%w: stage %s failed without a code", ErrInvalidDrillReceipt, result.Stage)
			}
		}
	}
	receipt.RecoveryTimeSeconds, receipt.RecoveryPointSeconds = receipt.MeasuredRecovery()
	receipt.Signability = EvaluateDrillSignability(*receipt)
	return nil
}
