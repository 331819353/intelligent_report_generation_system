package semanticqa

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) UpsertQueryFeedback(
	ctx context.Context,
	tenantID, actorID, queryPlanID string,
	input SubmitQueryFeedbackInput,
) (item QueryFeedback, err error) {
	if store == nil {
		return item, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO platform.semantic_query_feedback(
				tenant_id,query_plan_id,actor_id,rating,issue_type,comment
			)
			SELECT platform.current_tenant_id(),plan.id,$2::uuid,$3,$4,$5
			FROM platform.semantic_query_plans AS plan
			WHERE plan.tenant_id=platform.current_tenant_id()
			  AND plan.id=$1::uuid
			  AND plan.status='EXECUTED'
			ON CONFLICT(tenant_id,query_plan_id,actor_id) DO UPDATE SET
				rating=EXCLUDED.rating,issue_type=EXCLUDED.issue_type,
				comment=EXCLUDED.comment
			RETURNING id::text,query_plan_id::text,rating,issue_type,comment,
				created_at::text,updated_at::text`,
			queryPlanID, actorID, input.Rating, input.IssueType, input.Comment,
		)
		if scanErr := row.Scan(
			&item.ID, &item.QueryPlanID, &item.Rating, &item.IssueType, &item.Comment,
			&item.CreatedAt, &item.UpdatedAt,
		); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return scanErr
		}
		return nil
	})
	return item, err
}
