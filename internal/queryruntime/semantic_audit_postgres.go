package queryruntime

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresSemanticQuestionAuditStore persists summary-only execution facts.
// It intentionally has no API capable of accepting SQL, parameters or rows.
type PostgresSemanticQuestionAuditStore struct{ pool *pgxpool.Pool }

func NewPostgresSemanticQuestionAuditStore(pool *pgxpool.Pool) *PostgresSemanticQuestionAuditStore {
	return &PostgresSemanticQuestionAuditStore{pool: pool}
}

func (store *PostgresSemanticQuestionAuditStore) StartSemanticQuestion(
	ctx context.Context,
	run SemanticQuestionRun,
) error {
	access, ok := database.AccessContextFromContext(ctx)
	if store == nil || store.pool == nil || run.Validate() != nil || !ok ||
		access.UserID != run.ActorID || access.DomainID != run.DomainID {
		return errors.New("semantic execution audit request is invalid")
	}
	return database.WithTenantTx(ctx, store.pool, run.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.semantic_query_execution_runs(
			id,tenant_id,domain_id,actor_id,run_type,query_plan_hash,
			validation_hash,plan_count,max_rows,timeout_ms,max_explain_cost
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			run.RunID, run.TenantID, run.DomainID, run.ActorID, run.RunType,
			run.QueryPlanHash, run.ValidationHash, run.PlanCount, run.MaxRows,
			run.TimeoutMS, run.MaxExplainCost)
		return err
	})
}

func (store *PostgresSemanticQuestionAuditStore) FinishSemanticQuestion(
	ctx context.Context,
	completion SemanticQuestionCompletion,
) error {
	access, ok := database.AccessContextFromContext(ctx)
	if store == nil || store.pool == nil || completion.Validate() != nil || !ok {
		return errors.New("semantic execution audit completion is invalid")
	}
	return database.WithTenantTx(ctx, store.pool, completion.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_query_execution_runs
			SET status=$3,result_hash=$4,row_count=$5,duration_ms=$6,error_code=$7
			WHERE id=$1 AND tenant_id=$2 AND actor_id=$8 AND domain_id=$9 AND status='RUNNING'`,
			completion.RunID, completion.TenantID, completion.Status,
			completion.ResultHash, completion.RowCount, completion.DurationMS,
			completion.ErrorCode, access.UserID, access.DomainID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("semantic execution audit run was not found")
		}
		return nil
	})
}
