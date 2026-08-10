package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	E2ERunnerSchemaVersion = "askdata-e2e-runner-v1"
	MaxE2EBatchCases       = 100_000
)

var ErrInvalidE2ERun = errors.New("end-to-end evaluation run is invalid")

type E2EDisposition string

const (
	E2EDirect  E2EDisposition = "DIRECT"
	E2EClarify E2EDisposition = "CLARIFY"
	E2ERefuse  E2EDisposition = "REFUSE"
	E2EError   E2EDisposition = "ERROR"
)

type E2ECase struct {
	CaseID              askdata.ID
	CaseContentHash     askdata.ContentHash
	ExpectedDisposition E2EDisposition
	ExpectedReasonCode  string
	ExpectedIRHash      askdata.ContentHash
	ExpectedPathHash    askdata.ContentHash
	ExpectedResultHash  askdata.ContentHash
	Priority            string
	SecurityExpected    bool
}

type E2EBatch struct {
	TenantID              askdata.ID
	DomainID              askdata.ID
	EvaluationSetID       askdata.ID
	EvaluationSetHash     askdata.ContentHash
	EvaluationBatchID     askdata.ID
	ReleaseID             askdata.ID
	SemanticVersion       string
	ReleaseContentHash    askdata.ContentHash
	WarehouseSnapshotHash askdata.ContentHash
	WarehouseFreshnessAt  time.Time
	Cases                 []E2ECase
}

// E2EExecutionRequest contains only stable handles and immutable pins. The
// production orchestrator loads sealed question content through its privileged
// evaluation path; question text never crosses into the runner/report.
type E2EExecutionRequest struct {
	TenantID              askdata.ID
	DomainID              askdata.ID
	CaseID                askdata.ID
	CaseContentHash       askdata.ContentHash
	ReleaseID             askdata.ID
	SemanticVersion       string
	ReleaseContentHash    askdata.ContentHash
	WarehouseSnapshotHash askdata.ContentHash
	WarehouseFreshnessAt  time.Time
}

type E2EOutcome struct {
	Disposition           E2EDisposition
	ReasonCode            string
	IRHash                askdata.ContentHash
	PathHash              askdata.ContentHash
	ResultHash            askdata.ContentHash
	ComparisonHash        askdata.ContentHash
	SecurityPassed        bool
	SensitiveLeak         bool
	FailureStage          FailureStage
	FailureCode           string
	NarrativePassed       bool
	NarrativeEvidenceHash askdata.ContentHash
	Duration              time.Duration
}

type ProductionEvaluationOrchestrator interface {
	ExecuteEvaluationCase(context.Context, E2EExecutionRequest) (E2EOutcome, error)
}

type E2ECaseRecord struct {
	SchemaVersion         string              `json:"schemaVersion"`
	TenantID              askdata.ID          `json:"tenantId"`
	DomainID              askdata.ID          `json:"domainId"`
	EvaluationBatchID     askdata.ID          `json:"evaluationBatchId"`
	EvaluationSetID       askdata.ID          `json:"evaluationSetId"`
	EvaluationSetHash     askdata.ContentHash `json:"evaluationSetHash"`
	CaseID                askdata.ID          `json:"caseId"`
	CaseContentHash       askdata.ContentHash `json:"caseContentHash"`
	ReleaseID             askdata.ID          `json:"releaseId"`
	SemanticVersion       string              `json:"semanticVersion"`
	ReleaseContentHash    askdata.ContentHash `json:"releaseContentHash"`
	WarehouseSnapshotHash askdata.ContentHash `json:"warehouseSnapshotHash"`
	WarehouseFreshnessAt  time.Time           `json:"warehouseFreshnessAt"`
	ExpectedDisposition   E2EDisposition      `json:"expectedDisposition"`
	ActualDisposition     E2EDisposition      `json:"actualDisposition"`
	ExpectedReasonCode    string              `json:"expectedReasonCode,omitempty"`
	ActualReasonCode      string              `json:"actualReasonCode,omitempty"`
	ExpectedIRHash        askdata.ContentHash `json:"expectedIrHash,omitempty"`
	ActualIRHash          askdata.ContentHash `json:"actualIrHash,omitempty"`
	ExpectedPathHash      askdata.ContentHash `json:"expectedPathHash,omitempty"`
	ActualPathHash        askdata.ContentHash `json:"actualPathHash,omitempty"`
	ExpectedResultHash    askdata.ContentHash `json:"expectedResultHash,omitempty"`
	ActualResultHash      askdata.ContentHash `json:"actualResultHash,omitempty"`
	ComparisonHash        askdata.ContentHash `json:"comparisonHash,omitempty"`
	StrictCorrect         bool                `json:"strictCorrect"`
	SecurityPassed        bool                `json:"securityPassed"`
	SensitiveLeak         bool                `json:"sensitiveLeak"`
	FailureStage          FailureStage        `json:"failureStage,omitempty"`
	FailureCode           string              `json:"failureCode,omitempty"`
	NarrativePassed       bool                `json:"narrativePassed"`
	NarrativeEvidenceHash askdata.ContentHash `json:"narrativeEvidenceHash"`
	DurationMS            int64               `json:"durationMs"`
	RecordHash            askdata.ContentHash `json:"recordHash"`
}

type E2ERunStore interface {
	AppendE2ECaseRecord(context.Context, E2ECaseRecord) error
}

type E2EBatchReceipt struct {
	SchemaVersion         string                `json:"schemaVersion"`
	EvaluationBatchID     askdata.ID            `json:"evaluationBatchId"`
	EvaluationSetID       askdata.ID            `json:"evaluationSetId"`
	EvaluationSetHash     askdata.ContentHash   `json:"evaluationSetHash"`
	ReleaseID             askdata.ID            `json:"releaseId"`
	ReleaseContentHash    askdata.ContentHash   `json:"releaseContentHash"`
	WarehouseSnapshotHash askdata.ContentHash   `json:"warehouseSnapshotHash"`
	CaseCount             int                   `json:"caseCount"`
	StrictCorrectCount    int                   `json:"strictCorrectCount"`
	DirectCount           int                   `json:"directCount"`
	ClarificationCount    int                   `json:"clarificationCount"`
	RefusalCount          int                   `json:"refusalCount"`
	SecurityFailureCount  int                   `json:"securityFailureCount"`
	SensitiveLeakCount    int                   `json:"sensitiveLeakCount"`
	NarrativeFailureCount int                   `json:"narrativeFailureCount"`
	RecordHashes          []askdata.ContentHash `json:"recordHashes"`
	ReceiptHash           askdata.ContentHash   `json:"receiptHash"`
}

type E2ERunner struct {
	orchestrator ProductionEvaluationOrchestrator
	store        E2ERunStore
}

func NewE2ERunner(orchestrator ProductionEvaluationOrchestrator, store E2ERunStore) (*E2ERunner, error) {
	if orchestrator == nil || store == nil {
		return nil, ErrInvalidE2ERun
	}
	return &E2ERunner{orchestrator: orchestrator, store: store}, nil
}

func (runner *E2ERunner) Run(ctx context.Context, batch E2EBatch) (E2EBatchReceipt, error) {
	if runner == nil || runner.orchestrator == nil || runner.store == nil || ctx == nil {
		return E2EBatchReceipt{}, ErrInvalidE2ERun
	}
	if err := validateE2EBatch(batch); err != nil {
		return E2EBatchReceipt{}, err
	}
	cases := append([]E2ECase(nil), batch.Cases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].CaseID < cases[j].CaseID })
	receipt := E2EBatchReceipt{
		SchemaVersion: E2ERunnerSchemaVersion, EvaluationBatchID: batch.EvaluationBatchID,
		EvaluationSetID: batch.EvaluationSetID, EvaluationSetHash: batch.EvaluationSetHash,
		ReleaseID: batch.ReleaseID, ReleaseContentHash: batch.ReleaseContentHash,
		WarehouseSnapshotHash: batch.WarehouseSnapshotHash, CaseCount: len(cases),
		RecordHashes: make([]askdata.ContentHash, 0, len(cases)),
	}
	for _, evaluationCase := range cases {
		if err := ctx.Err(); err != nil {
			return E2EBatchReceipt{}, err
		}
		started := time.Now()
		outcome, executionErr := runner.orchestrator.ExecuteEvaluationCase(ctx, E2EExecutionRequest{
			TenantID: batch.TenantID, DomainID: batch.DomainID, CaseID: evaluationCase.CaseID,
			CaseContentHash: evaluationCase.CaseContentHash, ReleaseID: batch.ReleaseID,
			SemanticVersion: batch.SemanticVersion, ReleaseContentHash: batch.ReleaseContentHash,
			WarehouseSnapshotHash: batch.WarehouseSnapshotHash, WarehouseFreshnessAt: batch.WarehouseFreshnessAt,
		})
		if ctx.Err() != nil {
			return E2EBatchReceipt{}, ctx.Err()
		}
		if executionErr != nil {
			outcome = E2EOutcome{
				Disposition: E2EError, FailureStage: FailureStageExecution,
				FailureCode: "E2E_ORCHESTRATOR_ERROR", Duration: time.Since(started),
				NarrativeEvidenceHash: askdata.HashBytes([]byte("E2E_ORCHESTRATOR_ERROR:" + string(evaluationCase.CaseID))),
			}
		}
		record, err := buildE2ECaseRecord(batch, evaluationCase, outcome)
		if err != nil {
			return E2EBatchReceipt{}, err
		}
		if err := runner.store.AppendE2ECaseRecord(ctx, record); err != nil {
			return E2EBatchReceipt{}, err
		}
		receipt.RecordHashes = append(receipt.RecordHashes, record.RecordHash)
		if record.StrictCorrect {
			receipt.StrictCorrectCount++
		}
		switch record.ActualDisposition {
		case E2EDirect:
			receipt.DirectCount++
		case E2EClarify:
			receipt.ClarificationCount++
		case E2ERefuse:
			receipt.RefusalCount++
		}
		if !record.SecurityPassed {
			receipt.SecurityFailureCount++
		}
		if record.SensitiveLeak {
			receipt.SensitiveLeakCount++
		}
		if !record.NarrativePassed {
			receipt.NarrativeFailureCount++
		}
	}
	receipt.ReceiptHash = hashE2EBatchReceipt(receipt)
	return receipt, receipt.Validate()
}

func buildE2ECaseRecord(batch E2EBatch, evaluationCase E2ECase, outcome E2EOutcome) (E2ECaseRecord, error) {
	if err := validateE2EOutcome(outcome); err != nil {
		return E2ECaseRecord{}, err
	}
	strict := outcome.Disposition == evaluationCase.ExpectedDisposition && outcome.SecurityPassed && !outcome.SensitiveLeak
	if evaluationCase.ExpectedReasonCode != "" {
		strict = strict && outcome.ReasonCode == evaluationCase.ExpectedReasonCode
	}
	if evaluationCase.ExpectedDisposition == E2EDirect {
		strict = strict && outcome.IRHash == evaluationCase.ExpectedIRHash && outcome.ResultHash == evaluationCase.ExpectedResultHash
		if evaluationCase.ExpectedPathHash != "" {
			strict = strict && outcome.PathHash == evaluationCase.ExpectedPathHash
		}
	}
	record := E2ECaseRecord{
		SchemaVersion: E2ERunnerSchemaVersion, TenantID: batch.TenantID, DomainID: batch.DomainID,
		EvaluationBatchID: batch.EvaluationBatchID,
		EvaluationSetID:   batch.EvaluationSetID, EvaluationSetHash: batch.EvaluationSetHash,
		CaseID: evaluationCase.CaseID, CaseContentHash: evaluationCase.CaseContentHash,
		ReleaseID: batch.ReleaseID, SemanticVersion: batch.SemanticVersion,
		ReleaseContentHash: batch.ReleaseContentHash, WarehouseSnapshotHash: batch.WarehouseSnapshotHash,
		WarehouseFreshnessAt: batch.WarehouseFreshnessAt,
		ExpectedDisposition:  evaluationCase.ExpectedDisposition, ActualDisposition: outcome.Disposition,
		ExpectedReasonCode: evaluationCase.ExpectedReasonCode, ActualReasonCode: outcome.ReasonCode,
		ExpectedIRHash: evaluationCase.ExpectedIRHash, ActualIRHash: outcome.IRHash,
		ExpectedPathHash: evaluationCase.ExpectedPathHash, ActualPathHash: outcome.PathHash,
		ExpectedResultHash: evaluationCase.ExpectedResultHash, ActualResultHash: outcome.ResultHash,
		ComparisonHash: outcome.ComparisonHash, StrictCorrect: strict,
		SecurityPassed: outcome.SecurityPassed, SensitiveLeak: outcome.SensitiveLeak,
		FailureStage: outcome.FailureStage, FailureCode: outcome.FailureCode,
		NarrativePassed: outcome.NarrativePassed, NarrativeEvidenceHash: outcome.NarrativeEvidenceHash,
		DurationMS: outcome.Duration.Milliseconds(),
	}
	if strict {
		record.FailureStage, record.FailureCode = "", ""
	} else if record.FailureStage == "" {
		record.FailureStage, record.FailureCode = classifyE2EFailure(evaluationCase, outcome)
	}
	record.RecordHash = hashE2ECaseRecord(record)
	return record, record.Validate()
}

func (record E2ECaseRecord) Validate() error {
	if record.SchemaVersion != E2ERunnerSchemaVersion || record.TenantID.Validate() != nil || record.DomainID.Validate() != nil ||
		record.EvaluationBatchID.Validate() != nil ||
		record.EvaluationSetID.Validate() != nil || record.EvaluationSetHash.Validate() != nil ||
		record.CaseID.Validate() != nil || record.CaseContentHash.Validate() != nil ||
		record.ReleaseID.Validate() != nil || len(record.SemanticVersion) < 3 || len(record.SemanticVersion) > 128 ||
		record.ReleaseContentHash.Validate() != nil || record.WarehouseSnapshotHash.Validate() != nil ||
		record.WarehouseFreshnessAt.IsZero() || !validE2EDisposition(record.ExpectedDisposition, false) ||
		!validE2EDisposition(record.ActualDisposition, true) || record.DurationMS < 0 || record.DurationMS > 600_000 ||
		record.RecordHash.Validate() != nil {
		return ErrInvalidE2ERun
	}
	if record.NarrativeEvidenceHash.Validate() != nil {
		return ErrInvalidE2ERun
	}
	if record.StrictCorrect != (record.FailureStage == "" && record.FailureCode == "") {
		return ErrInvalidE2ERun
	}
	if !record.StrictCorrect && (!validFailureStage(record.FailureStage) || !regressionCodePattern.MatchString(record.FailureCode)) {
		return ErrInvalidE2ERun
	}
	if hashE2ECaseRecord(record) != record.RecordHash {
		return ErrInvalidE2ERun
	}
	return nil
}

func (receipt E2EBatchReceipt) Validate() error {
	if receipt.SchemaVersion != E2ERunnerSchemaVersion || receipt.EvaluationBatchID.Validate() != nil ||
		receipt.EvaluationSetID.Validate() != nil || receipt.EvaluationSetHash.Validate() != nil ||
		receipt.ReleaseID.Validate() != nil || receipt.ReleaseContentHash.Validate() != nil ||
		receipt.WarehouseSnapshotHash.Validate() != nil || receipt.CaseCount < 1 ||
		len(receipt.RecordHashes) != receipt.CaseCount || receipt.StrictCorrectCount < 0 ||
		receipt.StrictCorrectCount > receipt.CaseCount || receipt.DirectCount+receipt.ClarificationCount+receipt.RefusalCount > receipt.CaseCount ||
		receipt.SecurityFailureCount < 0 || receipt.SecurityFailureCount > receipt.CaseCount ||
		receipt.SensitiveLeakCount < 0 || receipt.SensitiveLeakCount > receipt.SecurityFailureCount ||
		receipt.NarrativeFailureCount < 0 || receipt.NarrativeFailureCount > receipt.CaseCount ||
		receipt.ReceiptHash.Validate() != nil {
		return ErrInvalidE2ERun
	}
	for _, hash := range receipt.RecordHashes {
		if hash.Validate() != nil {
			return ErrInvalidE2ERun
		}
	}
	if hashE2EBatchReceipt(receipt) != receipt.ReceiptHash {
		return ErrInvalidE2ERun
	}
	return nil
}

func validateE2EBatch(batch E2EBatch) error {
	if batch.TenantID.Validate() != nil || batch.DomainID.Validate() != nil || batch.EvaluationSetID.Validate() != nil ||
		batch.EvaluationSetHash.Validate() != nil || batch.EvaluationBatchID.Validate() != nil ||
		batch.ReleaseID.Validate() != nil || batch.ReleaseContentHash.Validate() != nil ||
		batch.WarehouseSnapshotHash.Validate() != nil || batch.WarehouseFreshnessAt.IsZero() ||
		len(batch.SemanticVersion) < 3 || len(batch.SemanticVersion) > 128 || len(batch.Cases) < 1 || len(batch.Cases) > MaxE2EBatchCases {
		return ErrInvalidE2ERun
	}
	seen := map[askdata.ID]struct{}{}
	for _, evaluationCase := range batch.Cases {
		if evaluationCase.CaseID.Validate() != nil || evaluationCase.CaseContentHash.Validate() != nil ||
			!validE2EDisposition(evaluationCase.ExpectedDisposition, false) ||
			(evaluationCase.ExpectedDisposition == E2EDirect && (evaluationCase.ExpectedIRHash.Validate() != nil || evaluationCase.ExpectedResultHash.Validate() != nil)) ||
			(evaluationCase.ExpectedPathHash != "" && evaluationCase.ExpectedPathHash.Validate() != nil) ||
			(evaluationCase.Priority != "P0" && evaluationCase.Priority != "P1" && evaluationCase.Priority != "P2") {
			return ErrInvalidE2ERun
		}
		if _, duplicate := seen[evaluationCase.CaseID]; duplicate {
			return ErrInvalidE2ERun
		}
		seen[evaluationCase.CaseID] = struct{}{}
	}
	return nil
}

func validateE2EOutcome(outcome E2EOutcome) error {
	if !validE2EDisposition(outcome.Disposition, true) || outcome.Duration < 0 || outcome.Duration > 10*time.Minute ||
		(outcome.IRHash != "" && outcome.IRHash.Validate() != nil) || (outcome.PathHash != "" && outcome.PathHash.Validate() != nil) ||
		(outcome.ResultHash != "" && outcome.ResultHash.Validate() != nil) || (outcome.ComparisonHash != "" && outcome.ComparisonHash.Validate() != nil) ||
		(outcome.FailureStage != "" && !validFailureStage(outcome.FailureStage)) ||
		(outcome.FailureCode != "" && !regressionCodePattern.MatchString(outcome.FailureCode)) ||
		outcome.NarrativeEvidenceHash.Validate() != nil ||
		outcome.SensitiveLeak && outcome.SecurityPassed {
		return ErrInvalidE2ERun
	}
	return nil
}

func classifyE2EFailure(expected E2ECase, actual E2EOutcome) (FailureStage, string) {
	if actual.SensitiveLeak || !actual.SecurityPassed {
		return FailureStageSecurity, "E2E_SECURITY_FAILED"
	}
	if actual.Disposition != expected.ExpectedDisposition || actual.ReasonCode != expected.ExpectedReasonCode {
		return FailureStageIntent, "E2E_DISPOSITION_MISMATCH"
	}
	if actual.IRHash != expected.ExpectedIRHash {
		return FailureStageIR, "E2E_IR_MISMATCH"
	}
	if expected.ExpectedPathHash != "" && actual.PathHash != expected.ExpectedPathHash {
		return FailureStageGraph, "E2E_PATH_MISMATCH"
	}
	return FailureStageValidation, "E2E_RESULT_MISMATCH"
}

func validE2EDisposition(value E2EDisposition, allowError bool) bool {
	return value == E2EDirect || value == E2EClarify || value == E2ERefuse || allowError && value == E2EError
}

func hashE2ECaseRecord(record E2ECaseRecord) askdata.ContentHash {
	record.RecordHash = ""
	payload, _ := json.Marshal(record)
	return askdata.HashBytes(payload)
}

func hashE2EBatchReceipt(receipt E2EBatchReceipt) askdata.ContentHash {
	receipt.ReceiptHash = ""
	payload, _ := json.Marshal(receipt)
	return askdata.HashBytes(payload)
}
