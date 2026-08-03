package semanticqa

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) StartQuestionRun(
	ctx context.Context,
	tenantID, actorID, runID, questionHash string,
	received QuestionStateEvent,
) error {
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		metadata, _ := ctx.Value(questionRuntimeMetadataKey{}).(questionRuntimeMetadata)
		var conversationID any
		if metadata.ConversationID != "" {
			conversationID = metadata.ConversationID
		}
		var parentQuestionID any
		if metadata.ParentQuestionID != "" {
			parentQuestionID = metadata.ParentQuestionID
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.semantic_question_runs(
				tenant_id,id,actor_id,conversation_id,parent_question_id,
				question_hash,current_state
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6
			)`,
			runID, actorID, conversationID, parentQuestionID,
			questionHash, received.State,
		); err != nil {
			return mapQuestionRunWriteError(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.semantic_question_run_events(
				tenant_id,question_run_id,event_index,state,stage,status,code,
				duration_ms,summary,occurred_at
			) VALUES(
				platform.current_tenant_id(),$1::uuid,1,$2,$3,$4,$5,$6,
				$7::jsonb,$8::timestamptz
			)`, runID, received.State, received.Stage, received.Status,
			received.Code, received.DurationMS, questionEventSummaryJSON(received),
			received.Timestamp)
		return err
	})
}

func (store *PostgresStore) ResolveConversationContext(
	ctx context.Context,
	tenantID, conversationID string,
) (items []string, err error) {
	items = []string{}
	if conversationID == "" {
		return items, nil
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT query_plan_ids::text[]
			FROM platform.semantic_question_runs
			WHERE tenant_id=platform.current_tenant_id()
			  AND conversation_id=$1::uuid
			  AND current_state='ANSWERED'
			  AND route='SEMANTIC_IR'
			  AND cardinality(query_plan_ids)>0
			ORDER BY completed_at DESC,id DESC
			LIMIT 1`, conversationID).Scan(&items)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return []string{}, nil
	}
	return items, err
}

func (store *PostgresStore) SaveQuestionOutcome(
	ctx context.Context,
	tenantID, runID string,
	outcome questionRunOutcome,
) error {
	budget, err := json.Marshal(outcome.Budgets)
	if err != nil {
		return err
	}
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		command, execErr := tx.Exec(ctx, `UPDATE platform.semantic_question_runs
			SET route=$1,decision=$2,semantic_version=$3,
				semantic_release_id=NULLIF($4,'')::uuid,
				semantic_content_hash=$5,understanding_hash=$6,
				graph_plan_hash=$7,intent_hash=$8,
				binding_bundle_hash=$9,query_plan_hash=$10,result_hash=$11,
				query_plan_ids=$12::uuid[],failure_code=$13,
				execution_budget=$14::jsonb,updated_at=now()
			WHERE tenant_id=platform.current_tenant_id() AND id=$15::uuid`,
			outcome.Route, outcome.Decision, outcome.SemanticVersion,
			outcome.SemanticReleaseID, outcome.SemanticContentHash,
			outcome.UnderstandingHash, outcome.GraphPlanHash,
			outcome.IntentHash, outcome.BindingBundleHash,
			outcome.QueryPlanHash, outcome.ResultHash, outcome.QueryPlanIDs,
			outcome.FailureCode, budget, runID,
		)
		if execErr != nil {
			return execErr
		}
		if command.RowsAffected() != 1 {
			return ErrNotFound
		}
		for _, artifact := range outcome.Artifacts {
			if !oneOf(artifact.Type, "UNDERSTANDING", "GRAPH_PLAN", "SEMANTIC_IR") ||
				!validHash(artifact.Hash) || len(artifact.Payload) == 0 {
				return ErrUnprovenPath
			}
			if _, artifactErr := tx.Exec(ctx, `INSERT INTO platform.semantic_question_artifacts(
					tenant_id,question_run_id,artifact_type,artifact_hash,payload
				) VALUES(platform.current_tenant_id(),$1::uuid,$2,$3,$4::jsonb)
				ON CONFLICT(tenant_id,question_run_id,artifact_type) DO UPDATE
				SET artifact_hash=EXCLUDED.artifact_hash,payload=EXCLUDED.payload,
					updated_at=now()`,
				runID, artifact.Type, artifact.Hash, artifact.Payload,
			); artifactErr != nil {
				return artifactErr
			}
		}
		return nil
	})
}

func (store *PostgresStore) GetQuestionRun(
	ctx context.Context,
	tenantID, runID string,
) (item QuestionRunSummary, err error) {
	item.QueryPlanIDs = []string{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		queryErr := tx.QueryRow(ctx, `SELECT id::text,
			COALESCE(conversation_id::text,''),COALESCE(parent_question_id::text,''),
			question_hash,current_state,COALESCE(route::text,''),decision,
			semantic_version,COALESCE(semantic_release_id::text,''),
			semantic_content_hash,understanding_hash,graph_plan_hash,
			intent_hash,query_plan_hash,result_hash,
			query_plan_ids::text[],failure_code,
			created_at::text,updated_at::text,COALESCE(completed_at::text,'')
			FROM platform.semantic_question_runs
			WHERE tenant_id=platform.current_tenant_id() AND id=$1::uuid`, runID).Scan(
			&item.QuestionID, &item.ConversationID, &item.ParentQuestionID,
			&item.QuestionHash, &item.State, &item.Route, &item.Decision,
			&item.SemanticVersion, &item.SemanticReleaseID,
			&item.SemanticContentHash, &item.UnderstandingHash,
			&item.GraphPlanHash, &item.IntentHash, &item.QueryPlanHash,
			&item.ResultHash, &item.QueryPlanIDs, &item.FailureCode,
			&item.CreatedAt, &item.UpdatedAt, &item.CompletedAt,
		)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return queryErr
	})
	return item, err
}

func (store *PostgresStore) ListQuestionRunEvents(
	ctx context.Context,
	tenantID, runID string,
) (items []QuestionStateEvent, err error) {
	items = []QuestionStateEvent{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT state,occurred_at::text,
			stage,status,code,duration_ms,summary
			FROM platform.semantic_question_run_events
			WHERE tenant_id=platform.current_tenant_id()
			  AND question_run_id=$1::uuid
			ORDER BY event_index`, runID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item QuestionStateEvent
			var summary []byte
			if scanErr := rows.Scan(
				&item.State, &item.Timestamp, &item.Stage, &item.Status,
				&item.Code, &item.DurationMS, &summary,
			); scanErr != nil {
				return scanErr
			}
			if len(summary) > 0 && string(summary) != "{}" {
				if decodeErr := json.Unmarshal(summary, &item.Summary); decodeErr != nil {
					return decodeErr
				}
			}
			items = append(items, item)
		}
		if rows.Err() != nil {
			return rows.Err()
		}
		if len(items) == 0 {
			return ErrNotFound
		}
		return nil
	})
	return items, err
}

func (store *PostgresStore) AppendQuestionState(
	ctx context.Context,
	tenantID, runID string,
	expected QuestionState,
	event QuestionStateEvent,
) error {
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var eventIndex int
		err := tx.QueryRow(ctx, `UPDATE platform.semantic_question_runs
			SET current_state=$1,record_version=record_version+1,
				updated_at=$2::timestamptz,
				completed_at=CASE WHEN $3 THEN $2::timestamptz ELSE NULL END
			WHERE tenant_id=platform.current_tenant_id()
			  AND id=$4::uuid AND current_state=$5
			RETURNING record_version::int`,
			event.State, event.Timestamp, terminalQuestionState(event.State),
			runID, expected,
		).Scan(&eventIndex)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.semantic_question_run_events(
				tenant_id,question_run_id,event_index,state,stage,status,code,
				duration_ms,summary,occurred_at
			) VALUES(
				platform.current_tenant_id(),$1::uuid,$2,$3,$4,$5,$6,$7,
				$8::jsonb,$9::timestamptz
			)`, runID, eventIndex, event.State, event.Stage, event.Status,
			event.Code, event.DurationMS, questionEventSummaryJSON(event),
			event.Timestamp)
		return err
	})
}

func mapQuestionRunWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}
