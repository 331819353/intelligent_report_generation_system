package evaluation

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

type e2eBatchLoaderFixture struct {
	batch     E2EBatch
	selection E2EBatchSelection
	err       error
}

func (fixture *e2eBatchLoaderFixture) LoadE2EBatch(_ context.Context, selection E2EBatchSelection) (E2EBatch, error) {
	fixture.selection = selection
	return fixture.batch, fixture.err
}

func TestE2EJobLoadsPinnedBatchAndRunsProductionContract(t *testing.T) {
	batch := e2eBatchFixture()
	selection := E2EBatchSelection{
		TenantID: batch.TenantID, DomainID: batch.DomainID,
		EvaluationSetID: batch.EvaluationSetID, EvaluationBatchID: batch.EvaluationBatchID,
		ReleaseID: batch.ReleaseID, WarehouseSnapshotHash: batch.WarehouseSnapshotHash,
		WarehouseFreshnessAt: batch.WarehouseFreshnessAt,
	}
	loader := &e2eBatchLoaderFixture{batch: batch}
	orchestrator := &e2eOrchestratorFixture{outcomes: map[askdata.ID]E2EOutcome{
		"case-direct": {Disposition: E2EDirect, IRHash: batch.Cases[0].ExpectedIRHash,
			ResultHash: batch.Cases[0].ExpectedResultHash, SecurityPassed: true,
			NarrativePassed: true, NarrativeEvidenceHash: hashOf("job-direct")},
		"case-refuse": {Disposition: E2ERefuse, ReasonCode: "UNAUTHORIZED", SecurityPassed: true,
			NarrativePassed: true, NarrativeEvidenceHash: hashOf("job-refuse")},
	}}
	store := &e2eStoreFixture{}
	job, err := NewE2EJob(loader, orchestrator, store)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := job.Run(context.Background(), selection)
	if err != nil {
		t.Fatal(err)
	}
	if loader.selection != selection || receipt.CaseCount != 2 || receipt.StrictCorrectCount != 2 || len(store.records) != 2 {
		t.Fatalf("job receipt=%#v selection=%#v records=%d", receipt, loader.selection, len(store.records))
	}
}

func TestE2EJobFailsClosedWhenSealedBatchCannotLoad(t *testing.T) {
	job, err := NewE2EJob(&e2eBatchLoaderFixture{err: ErrInvalidE2ERun}, &e2eOrchestratorFixture{}, &e2eStoreFixture{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := job.Run(context.Background(), E2EBatchSelection{}); !errors.Is(err, ErrInvalidE2ERun) {
		t.Fatalf("job error = %v", err)
	}
}
