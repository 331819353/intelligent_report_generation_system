package semanticqa

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) GetQueryPlan(
	ctx context.Context,
	tenantID, id string,
) (plan QueryPlan, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return loadQueryPlan(ctx, tx, id, &plan)
	})
	return plan, err
}

func (store *PostgresStore) PrepareQueryPlanExecution(
	ctx context.Context,
	tenantID, id, expectedGenerationID, expectedPathHash string,
) (plan QueryPlan, dimensionFieldID, memberKey string, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if err := loadQueryPlan(ctx, tx, id, &plan); err != nil {
			return err
		}
		if plan.Status != "READY" ||
			plan.GraphGenerationID != expectedGenerationID ||
			plan.PathHash != expectedPathHash {
			return ErrConflict
		}
		err := tx.QueryRow(ctx, `SELECT COALESCE(dimension.field_id,''),
				COALESCE(selected_member.member_key,'')
			FROM platform.semantic_query_plans AS query_plan
			JOIN platform.semantic_graph_projection_state AS graph_state
			  ON graph_state.tenant_id=query_plan.tenant_id
			 AND graph_state.current_generation_id=query_plan.graph_generation_id
			 AND graph_state.status='READY'
			 AND graph_state.applied_event_version=graph_state.requested_event_version
			JOIN platform.metric_versions AS metric_version
			  ON metric_version.tenant_id=query_plan.tenant_id
			 AND metric_version.id=query_plan.selected_metric_version_id
			 AND metric_version.metric_id=query_plan.selected_metric_id
			 AND metric_version.dataset_version_id=
			   query_plan.selected_dataset_version_id
			 AND metric_version.status='PUBLISHED'
			JOIN platform.metrics AS metric
			  ON metric.tenant_id=metric_version.tenant_id
			 AND metric.id=metric_version.metric_id
			 AND metric.current_published_version_id=metric_version.id
			 AND metric.status='PUBLISHED' AND metric.deleted_at IS NULL
			JOIN platform.dataset_versions AS dataset_version
			  ON dataset_version.tenant_id=metric_version.tenant_id
			 AND dataset_version.id=metric_version.dataset_version_id
			 AND dataset_version.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=dataset_version.tenant_id
			 AND dataset.id=dataset_version.dataset_id
			 AND dataset.current_published_version_id=dataset_version.id
			 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.tenant_id=dataset_version.tenant_id
			 AND materialization.id=query_plan.selected_materialization_id
			 AND materialization.dataset_id=dataset.id
			 AND materialization.dataset_version_id=dataset_version.id
			 AND materialization.status='ACTIVE'
			 AND materialization.schema_hash=dataset_version.schema_hash
			LEFT JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=query_plan.tenant_id
			 AND dimension.id=query_plan.selected_dimension_id
			 AND dimension.dataset_version_id=dataset_version.id
			 AND dimension.status='PUBLISHED'
			LEFT JOIN LATERAL (
			  SELECT member.member_key
			  FROM platform.semantic_query_plan_evidence AS evidence
			  JOIN platform.dimension_members AS member
			    ON member.tenant_id=evidence.tenant_id
			   AND member.id::text=evidence.subject_ref
			   AND member.dimension_id=query_plan.selected_dimension_id
			   AND member.status='ACTIVE'
			   AND (member.valid_from IS NULL OR member.valid_from<=now())
			   AND (member.valid_to IS NULL OR member.valid_to>now())
			  WHERE evidence.tenant_id=query_plan.tenant_id
			    AND evidence.query_plan_id=query_plan.id
			    AND evidence.subject_type='MEMBER'
			  ORDER BY evidence.evidence_index
			  LIMIT 1
			) AS selected_member ON true
			WHERE query_plan.id=$1::uuid AND query_plan.status='READY'
			  AND query_plan.graph_generation_id=$2::uuid
			  AND query_plan.path_hash=$3
			  AND (
			    COALESCE(
			      (query_plan.normalized_request_json->>'hasMemberValue')::boolean,
			      false
			    )=(selected_member.member_key IS NOT NULL)
			  )
			  AND (
			    query_plan.selected_dimension_id IS NULL
			    OR (
			      dimension.id IS NOT NULL
			      AND EXISTS(
			        SELECT 1
			        FROM platform.dimension_metric_compatibility AS compatibility
			        WHERE compatibility.tenant_id=query_plan.tenant_id
			          AND compatibility.dimension_id=dimension.id
			          AND compatibility.metric_id=metric.id
			          AND compatibility.metric_version_id=metric_version.id
			          AND compatibility.metric_dataset_version_id=
			            dataset_version.id
			          AND compatibility.status='VERIFIED'
			          AND compatibility.fanout_policy<>'UNSAFE'
			      )
			    )
			  )`, id, expectedGenerationID, expectedPathHash).
			Scan(&dimensionFieldID, &memberKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUnprovenPath
		}
		return err
	})
	return plan, dimensionFieldID, memberKey, err
}

func (store *PostgresStore) FinishQueryPlanExecution(
	ctx context.Context,
	tenantID, id, queryID, errorCode string,
	success bool,
	expectedGenerationID string,
	durationMS int64,
	rowCount int,
) (QueryPlan, error) {
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if success {
			tag, err := tx.Exec(ctx, `UPDATE platform.semantic_query_plans AS query_plan
				SET status='EXECUTED',executed_query_id=$1,
					execution_error_code='',executed_at=now(),
					execution_duration_ms=$4,execution_row_count=$5
				WHERE query_plan.id=$2::uuid AND query_plan.status='READY'
				  AND query_plan.graph_generation_id=$3::uuid
				  AND EXISTS(
				    SELECT 1
				    FROM platform.semantic_graph_projection_state AS graph_state
				    WHERE graph_state.tenant_id=query_plan.tenant_id
				      AND graph_state.current_generation_id=
				        query_plan.graph_generation_id
				      AND graph_state.status='READY'
				      AND graph_state.applied_event_version=
				        graph_state.requested_event_version
				  )`, queryID, id, expectedGenerationID, durationMS, rowCount)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_query_plans
			SET status='FAILED',executed_query_id=$1,
				execution_error_code=$2,executed_at=now(),
				execution_duration_ms=NULL,execution_row_count=NULL
			WHERE id=$3::uuid AND status='READY'
			  AND graph_generation_id=$4::uuid`,
			queryID, errorCode, id, expectedGenerationID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return QueryPlan{}, err
	}
	return store.GetQueryPlan(ctx, tenantID, id)
}

func loadQueryPlan(
	ctx context.Context,
	tx pgx.Tx,
	id string,
	plan *QueryPlan,
) error {
	var createdAt time.Time
	err := tx.QueryRow(ctx, `SELECT query_plan.id::text,
			query_plan.graph_generation_id::text,generation.generation,
			query_plan.question_hash,query_plan.intent,query_plan.status,
			query_plan.confidence::float8,
			COALESCE(query_plan.selected_metric_id::text,''),
			COALESCE(query_plan.selected_metric_version_id::text,''),
			COALESCE(query_plan.selected_dimension_id::text,''),
			COALESCE(query_plan.selected_dataset_version_id::text,''),
			COALESCE(query_plan.selected_materialization_id::text,''),
			query_plan.path_hash,query_plan.failure_code,
			query_plan.executed_query_id,query_plan.execution_error_code,
			query_plan.execution_duration_ms,query_plan.execution_row_count,
			query_plan.created_at
		FROM platform.semantic_query_plans AS query_plan
		JOIN platform.semantic_graph_generations AS generation
		  ON generation.tenant_id=query_plan.tenant_id
		 AND generation.id=query_plan.graph_generation_id
		WHERE query_plan.id=$1::uuid`, id).Scan(
		&plan.ID, &plan.GraphGenerationID, &plan.GraphGeneration,
		&plan.QuestionHash, &plan.Intent, &plan.Status, &plan.Confidence,
		&plan.SelectedMetricID, &plan.SelectedMetricVersionID,
		&plan.SelectedDimensionID, &plan.SelectedDatasetVersionID,
		&plan.SelectedMaterializationID, &plan.PathHash, &plan.FailureCode,
		&plan.ExecutedQueryID, &plan.ExecutionErrorCode,
		&plan.ExecutionDurationMS, &plan.ExecutionRowCount, &createdAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	plan.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	plan.Evidence = []QueryEvidence{}
	rows, err := tx.Query(ctx, `SELECT evidence.evidence_index,
			evidence.node_key,evidence.relation_type,evidence.subject_type,
			evidence.subject_ref,
			CASE WHEN evidence.subject_type='MEMBER'
			  THEN '成员命中（值已脱敏）'
			  ELSE COALESCE(node.label,evidence.subject_type)
			END,
			evidence.authority,evidence.confidence::float8,evidence.evidence_hash
		FROM platform.semantic_query_plan_evidence AS evidence
		LEFT JOIN platform.semantic_graph_nodes AS node
		  ON node.tenant_id=evidence.tenant_id
		 AND node.generation_id=$2::uuid
		 AND node.node_key=evidence.node_key
		WHERE evidence.query_plan_id=$1::uuid
		ORDER BY evidence.evidence_index`, id, plan.GraphGenerationID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var evidence QueryEvidence
		if err := rows.Scan(
			&evidence.Index, &evidence.NodeKey, &evidence.RelationType,
			&evidence.SubjectType, &evidence.SubjectRef, &evidence.Label,
			&evidence.Authority, &evidence.Confidence, &evidence.EvidenceHash,
		); err != nil {
			return err
		}
		plan.Evidence = append(plan.Evidence, evidence)
	}
	return rows.Err()
}
