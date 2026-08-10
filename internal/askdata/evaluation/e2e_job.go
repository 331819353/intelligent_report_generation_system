package evaluation

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type E2EBatchSelection struct {
	TenantID              askdata.ID
	DomainID              askdata.ID
	EvaluationSetID       askdata.ID
	EvaluationBatchID     askdata.ID
	ReleaseID             askdata.ID
	WarehouseSnapshotHash askdata.ContentHash
	WarehouseFreshnessAt  time.Time
}

type E2EBatchLoader interface {
	LoadE2EBatch(context.Context, E2EBatchSelection) (E2EBatch, error)
}

type PostgresE2EBatchLoader struct{ pool *pgxpool.Pool }

func NewPostgresE2EBatchLoader(pool *pgxpool.Pool) *PostgresE2EBatchLoader {
	return &PostgresE2EBatchLoader{pool: pool}
}

func (loader *PostgresE2EBatchLoader) LoadE2EBatch(
	ctx context.Context,
	selection E2EBatchSelection,
) (E2EBatch, error) {
	if loader == nil || loader.pool == nil || ctx == nil || validateE2ESelection(selection) != nil {
		return E2EBatch{}, ErrInvalidE2ERun
	}
	batch := E2EBatch{
		TenantID: selection.TenantID, DomainID: selection.DomainID,
		EvaluationSetID: selection.EvaluationSetID, EvaluationBatchID: selection.EvaluationBatchID,
		ReleaseID: selection.ReleaseID, WarehouseSnapshotHash: selection.WarehouseSnapshotHash,
		WarehouseFreshnessAt: selection.WarehouseFreshnessAt.UTC(), Cases: []E2ECase{},
	}
	err := database.WithTenantTx(ctx, loader.pool, string(selection.TenantID), func(tx pgx.Tx) error {
		var status, split, mode string
		var canIssue bool
		if err := tx.QueryRow(ctx, `SELECT evaluation_set.sealed_content_hash,
			release.semantic_version,release.content_hash,evaluation_set.status,
			evaluation_set.dataset_split,evaluation_set.evaluation_mode,plan.can_issue_95_percent
		FROM askdata.evaluation_sets AS evaluation_set
		JOIN askdata.releases AS release
		  ON release.id=evaluation_set.target_release_id
		 AND release.content_hash=evaluation_set.target_release_content_hash
		 AND release.domain_id=evaluation_set.domain_id
		 AND release.tenant_id=evaluation_set.tenant_id
		JOIN askdata.evaluation_batch_plans AS plan
		  ON plan.tenant_id=evaluation_set.tenant_id
		 AND plan.evaluation_set_id=evaluation_set.id
		WHERE evaluation_set.id=$1 AND evaluation_set.domain_id=$2
		  AND release.id=$3 AND release.status IN ('READY','SUPERSEDED')
		  AND plan.evaluation_batch_id=$4`, selection.EvaluationSetID, selection.DomainID,
			selection.ReleaseID, selection.EvaluationBatchID).Scan(
			&batch.EvaluationSetHash, &batch.SemanticVersion, &batch.ReleaseContentHash,
			&status, &split, &mode, &canIssue); err != nil {
			return err
		}
		if status != "SEALED" || split != "SEALED" || mode != "END_TO_END_RESULT_EQUIVALENCE" || !canIssue {
			return ErrInvalidE2ERun
		}
		rows, err := tx.Query(ctx, `SELECT evaluation_case.id::text,evaluation_case.content_hash,
			evaluation_case.expected_disposition,evaluation_case.expected_ir_hash,
			evaluation_case.expected_path_hash,evaluation_case.expected_result_hash,
			evaluation_case.priority,evaluation_case.security_expectation
		FROM askdata.evaluation_cases AS evaluation_case
		JOIN askdata.evaluation_batch_plans AS plan
		  ON plan.tenant_id=evaluation_case.tenant_id
		 AND plan.evaluation_set_id=evaluation_case.evaluation_set_id
		 AND plan.evaluation_batch_id=$2
		WHERE evaluation_case.evaluation_set_id=$1
		  AND evaluation_case.shard_id=ANY(plan.shard_ids)
		ORDER BY evaluation_case.id`, selection.EvaluationSetID, selection.EvaluationBatchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var evaluationCase E2ECase
			var expectedIR, expectedPath, expectedResult *string
			var securityExpectation string
			if err := rows.Scan(&evaluationCase.CaseID, &evaluationCase.CaseContentHash,
				&evaluationCase.ExpectedDisposition, &expectedIR, &expectedPath, &expectedResult,
				&evaluationCase.Priority, &securityExpectation); err != nil {
				return err
			}
			if expectedIR != nil {
				evaluationCase.ExpectedIRHash = askdata.ContentHash(*expectedIR)
			}
			if expectedPath != nil {
				evaluationCase.ExpectedPathHash = askdata.ContentHash(*expectedPath)
			}
			if expectedResult != nil {
				evaluationCase.ExpectedResultHash = askdata.ContentHash(*expectedResult)
			}
			evaluationCase.SecurityExpected = securityExpectation != "NONE"
			batch.Cases = append(batch.Cases, evaluationCase)
		}
		return rows.Err()
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return E2EBatch{}, ErrInvalidE2ERun
	}
	if err != nil {
		return E2EBatch{}, err
	}
	if err := validateE2EBatch(batch); err != nil {
		return E2EBatch{}, err
	}
	return batch, nil
}

type E2EJob struct {
	loader       E2EBatchLoader
	orchestrator ProductionEvaluationOrchestrator
	store        E2ERunStore
}

func NewE2EJob(
	loader E2EBatchLoader,
	orchestrator ProductionEvaluationOrchestrator,
	store E2ERunStore,
) (*E2EJob, error) {
	if loader == nil || orchestrator == nil || store == nil {
		return nil, ErrInvalidE2ERun
	}
	return &E2EJob{loader: loader, orchestrator: orchestrator, store: store}, nil
}

func (job *E2EJob) Run(
	ctx context.Context,
	selection E2EBatchSelection,
) (E2EBatchReceipt, error) {
	if job == nil || job.loader == nil || job.orchestrator == nil || job.store == nil {
		return E2EBatchReceipt{}, ErrInvalidE2ERun
	}
	batch, err := job.loader.LoadE2EBatch(ctx, selection)
	if err != nil {
		return E2EBatchReceipt{}, err
	}
	runner, err := NewE2ERunner(job.orchestrator, job.store)
	if err != nil {
		return E2EBatchReceipt{}, err
	}
	return runner.Run(ctx, batch)
}

func validateE2ESelection(selection E2EBatchSelection) error {
	for _, id := range []askdata.ID{
		selection.TenantID, selection.DomainID, selection.EvaluationSetID,
		selection.EvaluationBatchID, selection.ReleaseID,
	} {
		parsed, err := uuid.Parse(string(id))
		if err != nil || parsed.String() != strings.ToLower(string(id)) {
			return ErrInvalidE2ERun
		}
	}
	if selection.WarehouseSnapshotHash.Validate() != nil || selection.WarehouseFreshnessAt.IsZero() {
		return ErrInvalidE2ERun
	}
	return nil
}

var _ E2EBatchLoader = (*PostgresE2EBatchLoader)(nil)
