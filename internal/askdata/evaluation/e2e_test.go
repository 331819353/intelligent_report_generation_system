package evaluation

import (
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

type e2eOrchestratorFixture struct {
	outcomes map[askdata.ID]E2EOutcome
	requests []E2EExecutionRequest
}

func (fixture *e2eOrchestratorFixture) ExecuteEvaluationCase(_ context.Context, request E2EExecutionRequest) (E2EOutcome, error) {
	fixture.requests = append(fixture.requests, request)
	return fixture.outcomes[request.CaseID], nil
}

type e2eStoreFixture struct{ records []E2ECaseRecord }

func (fixture *e2eStoreFixture) AppendE2ECaseRecord(_ context.Context, record E2ECaseRecord) error {
	fixture.records = append(fixture.records, record)
	return nil
}

func TestE2ERunnerPinsReleaseWarehouseAndPersistsEveryCase(t *testing.T) {
	batch := e2eBatchFixture()
	orchestrator := &e2eOrchestratorFixture{outcomes: map[askdata.ID]E2EOutcome{
		"case-direct": {
			Disposition: E2EDirect, IRHash: batch.Cases[0].ExpectedIRHash,
			ResultHash: batch.Cases[0].ExpectedResultHash, SecurityPassed: true, NarrativePassed: true,
			NarrativeEvidenceHash: hashOf("narrative-direct"), Duration: time.Second,
		},
		"case-refuse": {Disposition: E2ERefuse, ReasonCode: "UNAUTHORIZED", SecurityPassed: true,
			NarrativePassed: true, NarrativeEvidenceHash: hashOf("narrative-refuse"), Duration: time.Second},
	}}
	store := &e2eStoreFixture{}
	runner, err := NewE2ERunner(orchestrator, store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := runner.Run(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.StrictCorrectCount != 2 || len(store.records) != 2 || len(orchestrator.requests) != 2 {
		t.Fatalf("receipt=%#v records=%d requests=%d", receipt, len(store.records), len(orchestrator.requests))
	}
	for _, request := range orchestrator.requests {
		if request.ReleaseID != batch.ReleaseID || request.ReleaseContentHash != batch.ReleaseContentHash ||
			request.WarehouseSnapshotHash != batch.WarehouseSnapshotHash || !request.WarehouseFreshnessAt.Equal(batch.WarehouseFreshnessAt) {
			t.Fatalf("pin changed: %#v", request)
		}
	}
}

func TestE2ERunnerRecordsStableFailureWithoutQuestionOrRows(t *testing.T) {
	batch := e2eBatchFixture()
	orchestrator := &e2eOrchestratorFixture{outcomes: map[askdata.ID]E2EOutcome{
		"case-direct": {Disposition: E2EDirect, IRHash: hashOf("wrong"), ResultHash: batch.Cases[0].ExpectedResultHash,
			SecurityPassed: true, NarrativePassed: true, NarrativeEvidenceHash: hashOf("narrative-direct")},
		"case-refuse": {Disposition: E2ERefuse, ReasonCode: "UNAUTHORIZED", SecurityPassed: true,
			NarrativePassed: true, NarrativeEvidenceHash: hashOf("narrative-refuse")},
	}}
	store := &e2eStoreFixture{}
	runner, _ := NewE2ERunner(orchestrator, store)
	receipt, err := runner.Run(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.StrictCorrectCount != 1 || store.records[0].FailureStage != FailureStageIR || store.records[0].FailureCode != "E2E_IR_MISMATCH" {
		t.Fatalf("failure record = %#v", store.records[0])
	}
	if store.records[0].RecordHash.Validate() != nil || store.records[0].ExpectedResultHash == "" {
		t.Fatal("bounded evidence hashes are missing")
	}
}

func TestE2ERunnerRejectsUnpinnedOrDuplicateBatch(t *testing.T) {
	batch := e2eBatchFixture()
	batch.WarehouseSnapshotHash = ""
	runner, _ := NewE2ERunner(&e2eOrchestratorFixture{}, &e2eStoreFixture{})
	if _, err := runner.Run(context.Background(), batch); !errors.Is(err, ErrInvalidE2ERun) {
		t.Fatalf("missing snapshot error = %v", err)
	}
	batch = e2eBatchFixture()
	batch.Cases = append(batch.Cases, batch.Cases[0])
	if _, err := runner.Run(context.Background(), batch); !errors.Is(err, ErrInvalidE2ERun) {
		t.Fatalf("duplicate case error = %v", err)
	}
}

func e2eBatchFixture() E2EBatch {
	return E2EBatch{
		TenantID: "tenant-a", DomainID: "domain-a", EvaluationSetID: "set-a", EvaluationSetHash: hashOf("set"),
		EvaluationBatchID: "batch-a", ReleaseID: "release-a", SemanticVersion: "v1.0.0",
		ReleaseContentHash: hashOf("release"), WarehouseSnapshotHash: hashOf("warehouse"),
		WarehouseFreshnessAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Cases: []E2ECase{
			{CaseID: "case-direct", CaseContentHash: hashOf("case-direct"), ExpectedDisposition: E2EDirect,
				ExpectedIRHash: hashOf("ir"), ExpectedResultHash: hashOf("result"), Priority: "P1"},
			{CaseID: "case-refuse", CaseContentHash: hashOf("case-refuse"), ExpectedDisposition: E2ERefuse,
				ExpectedReasonCode: "UNAUTHORIZED", Priority: "P0", SecurityExpected: true},
		},
	}
}
