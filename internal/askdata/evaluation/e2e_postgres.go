package evaluation

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresE2EStore struct{ pool *pgxpool.Pool }

func NewPostgresE2EStore(pool *pgxpool.Pool) *PostgresE2EStore {
	return &PostgresE2EStore{pool: pool}
}

func (store *PostgresE2EStore) AppendE2ECaseRecord(ctx context.Context, record E2ECaseRecord) error {
	if store == nil || store.pool == nil || ctx == nil {
		return errors.New("end-to-end evaluation PostgreSQL store is not configured")
	}
	if err := record.Validate(); err != nil {
		return err
	}
	status := "FAILED"
	if record.StrictCorrect {
		status = "PASSED"
	} else if record.ActualDisposition == E2EError {
		status = "ERROR"
	}
	return database.WithTenantTx(ctx, store.pool, string(record.TenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO askdata.evaluation_runs(
			tenant_id,domain_id,evaluation_batch_id,evaluation_set_id,evaluation_case_id,
			evaluation_set_content_hash,case_content_hash,release_id,semantic_version,
			release_content_hash,evaluation_mode,runner_version,run_key_hash,
			warehouse_snapshot_hash,warehouse_freshness_at,status,expected_disposition,
			actual_disposition,expected_ir_hash,actual_ir_hash,expected_path_hash,
			actual_path_hash,expected_result_hash,actual_result_hash,ir_equivalent,
			path_equivalent,result_equivalent,strict_correct,security_passed,
			sensitive_leak_detected,failure_stage,failure_code,comparison_report_hash,duration_ms
		) VALUES(
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'END_TO_END_RESULT_EQUIVALENCE',$11,$12,
			$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33
		) ON CONFLICT(tenant_id,run_key_hash) DO NOTHING`,
			record.TenantID, record.DomainID, record.EvaluationBatchID, record.EvaluationSetID, record.CaseID,
			record.EvaluationSetHash, record.CaseContentHash, record.ReleaseID, record.SemanticVersion,
			record.ReleaseContentHash, E2ERunnerSchemaVersion, record.RecordHash,
			record.WarehouseSnapshotHash, record.WarehouseFreshnessAt, status,
			record.ExpectedDisposition, record.ActualDisposition,
			nullableHash(record.ExpectedIRHash), nullableHash(record.ActualIRHash),
			nullableHash(record.ExpectedPathHash), nullableHash(record.ActualPathHash),
			nullableHash(record.ExpectedResultHash), nullableHash(record.ActualResultHash),
			hashesEquivalent(record.ExpectedIRHash, record.ActualIRHash),
			hashesEquivalent(record.ExpectedPathHash, record.ActualPathHash),
			hashesEquivalent(record.ExpectedResultHash, record.ActualResultHash),
			record.StrictCorrect, record.SecurityPassed, record.SensitiveLeak,
			record.FailureStage, record.FailureCode, nullableHash(record.ComparisonHash), record.DurationMS,
		)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO askdata.evaluation_narrative_results(
			tenant_id,domain_id,evaluation_set_id,evaluation_batch_id,evaluation_case_id,
			release_id,release_content_hash,passed,evidence_hash
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT(tenant_id,evaluation_batch_id,evaluation_case_id) DO NOTHING`,
			record.TenantID, record.DomainID, record.EvaluationSetID, record.EvaluationBatchID,
			record.CaseID, record.ReleaseID, record.ReleaseContentHash,
			record.NarrativePassed, record.NarrativeEvidenceHash)
		return err
	})
}

func nullableHash(value askdata.ContentHash) any {
	if value == "" {
		return nil
	}
	return value
}

func hashesEquivalent(expected, actual askdata.ContentHash) bool {
	return expected != "" && actual != "" && expected == actual
}

var _ E2ERunStore = (*PostgresE2EStore)(nil)
