package semanticmanagement

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const dimensionWherePolicyColumns = `policy.id::text,
	policy.dimension_id::text,policy.dimension_field_name,
	policy.dimension_description,policy.metric_code,policy.metric_field_id,
	policy.table_schema,policy.table_name,policy.actor_id::text,
	policy.sample_values,policy.attempt,
	policy.tenant_id::text,policy.lease_owner,policy.lease_token::text,
	policy.lease_expires_at`

const dimensionWherePolicyMaxAttempts = 10

func (s *PostgresStore) ListDimensionDecisionTenantIDs(
	ctx context.Context,
) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (s *PostgresStore) ReconcileDimensionWherePolicies(
	ctx context.Context,
	tenantID string,
) (reconciled int64, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `WITH eligible AS (
				SELECT dimension.id AS dimension_id,
					compatibility.metric_id,
					compatibility.metric_version_id,
					compatibility.metric_dataset_version_id
					  AS dataset_version_id,
					materialization.id AS materialization_id,
					dimension.code::text AS dimension_field_name,
					btrim(dimension.description) AS dimension_description,
					metric.code::text AS metric_code,
					metric.name AS metric_name,
					version.published_by AS actor_id,
					version.definition_json#>>'{expression,fieldId}'
					  AS metric_field_id,
					materialization.published_schema AS table_schema,
					materialization.published_name AS table_name,
					materialization.snapshot_hash,
					samples.values AS sample_values
				FROM platform.dimension_metric_compatibility AS compatibility
				JOIN platform.semantic_dimensions AS dimension
				  ON dimension.tenant_id=compatibility.tenant_id
				 AND dimension.id=compatibility.dimension_id
				 AND dimension.status='PUBLISHED'
				 AND dimension.member_index_policy='FULL'
				 AND NOT dimension.sensitive
				 AND NOT dimension.high_cardinality
				JOIN platform.dataset_fields AS field
				  ON field.tenant_id=dimension.tenant_id
				 AND field.dataset_version_id=dimension.dataset_version_id
				 AND field.field_id=dimension.field_id
				JOIN platform.metric_versions AS version
				  ON version.tenant_id=compatibility.tenant_id
				 AND version.id=compatibility.metric_version_id
				 AND version.metric_id=compatibility.metric_id
				 AND version.dataset_version_id=
				     compatibility.metric_dataset_version_id
				 AND version.status='PUBLISHED'
				JOIN platform.metrics AS metric
				  ON metric.tenant_id=version.tenant_id
				 AND metric.id=version.metric_id
				 AND metric.status='PUBLISHED'
				 AND metric.current_published_version_id=version.id
				 AND metric.deleted_at IS NULL
				JOIN platform.dataset_materializations AS materialization
				  ON materialization.tenant_id=version.tenant_id
				 AND materialization.dataset_id=version.dataset_id
				 AND materialization.dataset_version_id=version.dataset_version_id
				 AND materialization.layer='DWS'
				 AND materialization.status='ACTIVE'
				CROSS JOIN LATERAL (
				  SELECT COALESCE(array_agg(sample.canonical_label
				    ORDER BY sample.canonical_label),'{}') AS values
				  FROM (
				    SELECT member.canonical_label
				    FROM platform.dimension_members AS member
				    WHERE member.tenant_id=dimension.tenant_id
				      AND member.dimension_id=dimension.id
				      AND member.status='ACTIVE'
				      AND btrim(member.canonical_label)<>''
				    ORDER BY member.normalized_value,member.id
				    LIMIT 24
				  ) AS sample
				) AS samples
				WHERE compatibility.tenant_id=platform.current_tenant_id()
				  AND compatibility.status='VERIFIED'
				  AND compatibility.compatibility_type='DIRECT'
				  AND compatibility.fanout_policy='SAFE'
				  AND cardinality(samples.values)>0
				  AND btrim(dimension.description)<>''
				  AND btrim(COALESCE(
				    version.definition_json#>>'{expression,fieldId}',''
				  ))<>''
			), prepared AS (
				SELECT eligible.*,
					encode(public.digest(convert_to(concat_ws(E'\x1f',
					  eligible.dimension_id::text,
					  eligible.metric_version_id::text,
					  eligible.materialization_id::text,
					  eligible.dimension_field_name,
					  eligible.dimension_description,
					  eligible.metric_code,eligible.metric_field_id,
					  eligible.table_schema,eligible.table_name,
					  eligible.snapshot_hash,
					  array_to_string(eligible.sample_values,E'\x1e')
					),'UTF8'),'sha256'),'hex') AS input_hash
				FROM eligible
			)
			INSERT INTO platform.dimension_where_design_policies(
				tenant_id,dimension_id,metric_id,metric_version_id,
				dataset_version_id,materialization_id,input_hash,
				dimension_field_name,dimension_description,metric_code,
				metric_field_id,table_schema,table_name,actor_id,sample_values
			)
			SELECT platform.current_tenant_id(),prepared.dimension_id,
				prepared.metric_id,prepared.metric_version_id,
				prepared.dataset_version_id,prepared.materialization_id,
				prepared.input_hash,prepared.dimension_field_name,
				prepared.dimension_description,prepared.metric_code,
				prepared.metric_field_id,prepared.table_schema,
				prepared.table_name,prepared.actor_id,prepared.sample_values
			FROM prepared
			ON CONFLICT(
				tenant_id,dimension_id,metric_version_id,materialization_id
			) DO UPDATE SET
				input_hash=EXCLUDED.input_hash,
				dimension_field_name=EXCLUDED.dimension_field_name,
				dimension_description=EXCLUDED.dimension_description,
				metric_code=EXCLUDED.metric_code,
				metric_field_id=EXCLUDED.metric_field_id,
				table_schema=EXCLUDED.table_schema,
				table_name=EXCLUDED.table_name,
				actor_id=EXCLUDED.actor_id,
				sample_values=EXCLUDED.sample_values,
				status='PENDING',predicate_operator='',llm_model='',
				llm_reason='',confidence=NULL,attempt=0,
				next_attempt_at=now(),lease_owner='',lease_token=NULL,
				lease_expires_at=NULL,error_code='',completed_at=NULL,
				updated_at=now()
			WHERE platform.dimension_where_design_policies.input_hash
			  IS DISTINCT FROM EXCLUDED.input_hash`)
		if execErr != nil {
			return execErr
		}
		reconciled = tag.RowsAffected()
		return nil
	})
	return reconciled, err
}

func (s *PostgresStore) ClaimDimensionWherePolicy(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *DimensionWherePolicyClaim, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if _, execErr := tx.Exec(ctx, `UPDATE
				platform.dimension_where_design_policies
			SET status='FAILED',error_code='LEASE_EXPIRED',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=$1`, dimensionWherePolicyMaxAttempts); execErr != nil {
			return execErr
		}
		row := tx.QueryRow(ctx, `WITH candidate AS (
				SELECT id
				FROM platform.dimension_where_design_policies
				WHERE attempt<$3
				  AND (
				    (status='PENDING' AND next_attempt_at<=now())
				    OR (status='RUNNING' AND lease_expires_at<=now())
				  )
				ORDER BY created_at,id
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE platform.dimension_where_design_policies AS policy
			SET status='RUNNING',attempt=policy.attempt+1,
				predicate_operator='',llm_model='',llm_reason='',
				confidence=NULL,error_code='',completed_at=NULL,
				lease_owner=$1,lease_token=public.gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 second'),
				updated_at=now()
			FROM candidate
			WHERE policy.id=candidate.id
			RETURNING `+dimensionWherePolicyColumns,
			workerID, int64(lease/time.Second),
			dimensionWherePolicyMaxAttempts)
		item := DimensionWherePolicyClaim{}
		if scanErr := row.Scan(
			&item.ID, &item.DimensionID, &item.DimensionFieldName,
			&item.DimensionDescription, &item.MetricCode,
			&item.MetricFieldID, &item.TableSchema, &item.TableName,
			&item.ActorID, &item.SampleValues, &item.Attempt, &item.TenantID,
			&item.LeaseOwner, &item.LeaseToken, &item.LeaseExpiresAt,
		); errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		} else if scanErr != nil {
			return scanErr
		}
		claim = &item
		return nil
	})
	return claim, err
}

func (s *PostgresStore) CompleteDimensionWherePolicy(
	ctx context.Context,
	claim DimensionWherePolicyClaim,
	decision DimensionWherePolicyDecision,
) error {
	return database.WithTenantTx(
		ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE
					platform.dimension_where_design_policies
				SET status='SUCCEEDED',predicate_operator=$1,
					llm_model=$2,llm_reason=$3,confidence=$4,
					lease_owner='',lease_token=NULL,lease_expires_at=NULL,
					error_code='',completed_at=now(),updated_at=now()
				WHERE id=$5::uuid AND status='RUNNING'
				  AND attempt=$6 AND lease_owner=$7
				  AND lease_token=$8::uuid AND lease_expires_at>now()`,
				decision.PredicateOperator, decision.LLMModel,
				decision.LLMReason, decision.Confidence,
				claim.ID, claim.Attempt, claim.LeaseOwner, claim.LeaseToken,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrRefreshLeaseLost
			}
			return nil
		},
	)
}

func (s *PostgresStore) FailDimensionWherePolicy(
	ctx context.Context,
	claim DimensionWherePolicyClaim,
	code string,
) error {
	return database.WithTenantTx(
		ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
			terminal := claim.Attempt >= dimensionWherePolicyMaxAttempts
			targetStatus := "PENDING"
			errorCode := ""
			var completedAt any
			if terminal {
				targetStatus = "FAILED"
				errorCode = strings.TrimSpace(code)
				completedAt = time.Now().UTC()
			}
			tag, err := tx.Exec(ctx, `UPDATE
					platform.dimension_where_design_policies
				SET status=$1,error_code=$2,
					next_attempt_at=CASE WHEN $1='PENDING'
					  THEN now()+(attempt*interval '30 seconds')
					  ELSE next_attempt_at END,
					lease_owner='',lease_token=NULL,lease_expires_at=NULL,
					completed_at=$3,updated_at=now()
				WHERE id=$4::uuid AND status='RUNNING'
				  AND attempt=$5 AND lease_owner=$6
				  AND lease_token=$7::uuid`,
				targetStatus, errorCode, completedAt, claim.ID,
				claim.Attempt, claim.LeaseOwner, claim.LeaseToken,
			)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrRefreshLeaseLost
			}
			return nil
		},
	)
}

func (s *PostgresStore) MaterializeDimensionWhereDecisions(
	ctx context.Context,
	tenantID string,
	limit int,
) (materialized int64, err error) {
	if limit < 1 || limit > 5000 {
		return 0, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var locked bool
		if lockErr := tx.QueryRow(ctx, `SELECT
				pg_try_advisory_xact_lock(hashtextextended(
				  'dimension-where-materialization:'||
				  platform.current_tenant_id()::text,0
				))`).Scan(&locked); lockErr != nil {
			return lockErr
		}
		if !locked {
			return nil
		}
		tag, execErr := tx.Exec(ctx, `WITH eligible AS (
				SELECT policy.id AS policy_id,policy.input_hash,
					policy.predicate_operator,policy.llm_model,
					policy.llm_prompt_version,policy.llm_reason,
					policy.dimension_id,policy.metric_id,
					policy.metric_version_id,policy.dataset_version_id,
					policy.materialization_id,
					dimension.field_id AS dimension_field_id,
					policy.dimension_field_name,
					policy.dimension_description,policy.metric_code,
					policy.metric_field_id,policy.table_schema,
					policy.table_name,
					dimension.code::text AS dimension_code,
					metric.name AS metric_name,
					member.id AS dimension_member_id,
					member.normalized_value,member.canonical_label,
					document.id AS embedding_document_id,
					document.document AS vector_key,
					document.input_hash AS vector_key_hash,
					document.embedding_model
				FROM platform.dimension_where_design_policies AS policy
				JOIN platform.semantic_dimensions AS dimension
				  ON dimension.tenant_id=policy.tenant_id
				 AND dimension.id=policy.dimension_id
				 AND dimension.status='PUBLISHED'
				JOIN platform.dimension_members AS member
				  ON member.tenant_id=dimension.tenant_id
				 AND member.dimension_id=dimension.id
				 AND member.status='ACTIVE'
				JOIN platform.dimension_member_semantic_documents AS document
				  ON document.tenant_id=member.tenant_id
				 AND document.dimension_member_id=member.id
				 AND document.dimension_id=member.dimension_id
				 AND document.embedding_status='SUCCEEDED'
				JOIN platform.metrics AS metric
				  ON metric.tenant_id=policy.tenant_id
				 AND metric.id=policy.metric_id
				 AND metric.status='PUBLISHED'
				WHERE policy.tenant_id=platform.current_tenant_id()
				  AND policy.status='SUCCEEDED'
			), pending_base AS MATERIALIZED (
				SELECT eligible.*
				FROM eligible
				LEFT JOIN platform.dimension_where_decisions AS decision
				  ON decision.tenant_id=platform.current_tenant_id()
				 AND decision.dimension_member_id=
				     eligible.dimension_member_id
				 AND decision.metric_version_id=eligible.metric_version_id
				 AND decision.materialization_id=eligible.materialization_id
				WHERE decision.id IS NULL
				   OR (
				     decision.source_type='DWS_PRECOMPUTED'
				     AND decision.source_input_hash<>
				         encode(public.digest(convert_to(
				           concat_ws(E'\x1f',eligible.input_hash,
				             eligible.vector_key_hash,
				             eligible.predicate_operator,
				             eligible.normalized_value
				           ),'UTF8'
				         ),'sha256'),'hex')
				   )
				ORDER BY eligible.dimension_id,
					eligible.normalized_value,eligible.dimension_member_id
				LIMIT $1
			), pending AS (
				SELECT pending_base.*,
					COALESCE((
					  SELECT array_agg(alias.alias ORDER BY alias.alias)
					  FROM (
					    SELECT DISTINCT member_alias.alias
					    FROM platform.dimension_member_aliases AS member_alias
					    WHERE member_alias.tenant_id=
					          platform.current_tenant_id()
					      AND member_alias.dimension_id=
					          pending_base.dimension_id
					      AND member_alias.dimension_member_id=
					          pending_base.dimension_member_id
					      AND btrim(member_alias.alias)<>''
					    ORDER BY member_alias.alias
					    LIMIT 64
					  ) AS alias
					),'{}') AS aliases,
					encode(public.digest(convert_to(
					  pending_base.normalized_value,'UTF8'
					),'sha256'),'hex') AS member_set_hash,
					encode(public.digest(convert_to(concat_ws(E'\x1f',
					  pending_base.input_hash,
					  pending_base.vector_key_hash,
					  pending_base.predicate_operator,
					  pending_base.normalized_value
					),'UTF8'),'sha256'),'hex') AS source_input_hash
				FROM pending_base
			)
			INSERT INTO platform.dimension_where_decisions(
				tenant_id,vector_key,vector_key_hash,embedding,
				embedding_model,dimension_id,dimension_field_id,
				dimension_field_name,dimension_description,canonical_value,
				aliases,selected_member_set_hash,selected_member_count,
				metric_id,metric_version_id,dataset_version_id,metric_code,
				metric_name,metric_field_id,materialization_id,table_schema,
				table_name,predicate_operator,where_condition,
				compiled_condition,llm_model,llm_prompt_version,llm_reason,
				latest_query_plan_id,dimension_member_id,
				embedding_document_id,source_type,source_input_hash
			)
			SELECT platform.current_tenant_id(),pending.vector_key,
				pending.vector_key_hash,NULL,pending.embedding_model,
				pending.dimension_id,pending.dimension_field_id,
				pending.dimension_field_name,pending.dimension_description,
				pending.canonical_label,pending.aliases,
				pending.member_set_hash,1,pending.metric_id,
				pending.metric_version_id,pending.dataset_version_id,
				pending.metric_code,pending.metric_name,
				pending.metric_field_id,pending.materialization_id,
				pending.table_schema,pending.table_name,
				pending.predicate_operator,
				CASE pending.predicate_operator
				  WHEN 'CONTAINS' THEN format(
				    '%s LIKE %L ESCAPE ''\\''',
				    pending.dimension_field_name,
				    '%'||
				    replace(replace(replace(
				      pending.canonical_label,
				      E'\\',E'\\\\'
				    ),'%',E'\\%'),'_',E'\\_')||
				    '%'
				  )
				  ELSE format(
				    '%s = %L',pending.dimension_field_name,
				    pending.canonical_label
				  )
				END,
					CASE pending.predicate_operator
					  WHEN 'CONTAINS' THEN format(
					    '%s LIKE :%s_1 ESCAPE ''\\''',
					    pending.dimension_field_id,pending.dimension_code
					  )
					  ELSE format(
					    '%s = :%s_1',
					    pending.dimension_field_id,pending.dimension_code
					  )
					END,
				pending.llm_model,pending.llm_prompt_version,
				pending.llm_reason,NULL,pending.dimension_member_id,
				pending.embedding_document_id,'DWS_PRECOMPUTED',
				pending.source_input_hash
			FROM pending
			ON CONFLICT(
				tenant_id,dimension_id,selected_member_set_hash,
				metric_version_id,materialization_id
			) DO UPDATE SET
				vector_key=EXCLUDED.vector_key,
				vector_key_hash=EXCLUDED.vector_key_hash,
				embedding=NULL,
				embedding_model=EXCLUDED.embedding_model,
				dimension_field_id=EXCLUDED.dimension_field_id,
				dimension_field_name=EXCLUDED.dimension_field_name,
				dimension_description=EXCLUDED.dimension_description,
				canonical_value=EXCLUDED.canonical_value,
				aliases=EXCLUDED.aliases,
				metric_code=EXCLUDED.metric_code,
				metric_name=EXCLUDED.metric_name,
				metric_field_id=EXCLUDED.metric_field_id,
				table_schema=EXCLUDED.table_schema,
				table_name=EXCLUDED.table_name,
				predicate_operator=EXCLUDED.predicate_operator,
				where_condition=EXCLUDED.where_condition,
				compiled_condition=EXCLUDED.compiled_condition,
				llm_model=EXCLUDED.llm_model,
				llm_prompt_version=EXCLUDED.llm_prompt_version,
				llm_reason=EXCLUDED.llm_reason,
				dimension_member_id=EXCLUDED.dimension_member_id,
				embedding_document_id=EXCLUDED.embedding_document_id,
				source_input_hash=EXCLUDED.source_input_hash,
				last_seen_at=now()
			WHERE platform.dimension_where_decisions.source_type=
			  'DWS_PRECOMPUTED'`,
			limit,
		)
		if execErr != nil {
			return execErr
		}
		materialized = tag.RowsAffected()
		return nil
	})
	return materialized, err
}

func (s *PostgresStore) CleanupDimensionWhereDecisions(
	ctx context.Context,
	tenantID string,
) (removed int64, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `DELETE
			FROM platform.dimension_where_decisions AS decision
			WHERE decision.source_type='DWS_PRECOMPUTED'
			  AND NOT EXISTS(
			    SELECT 1
			    FROM platform.dimension_members AS member
			    JOIN platform.semantic_dimensions AS dimension
			      ON dimension.tenant_id=member.tenant_id
			     AND dimension.id=member.dimension_id
			    WHERE member.tenant_id=decision.tenant_id
			      AND member.id=decision.dimension_member_id
			      AND member.dimension_id=decision.dimension_id
			      AND member.status='ACTIVE'
			      AND dimension.status='PUBLISHED'
			  )`)
		if execErr != nil {
			return execErr
		}
		removed = tag.RowsAffected()
		return nil
	})
	return removed, err
}

func (s *PostgresStore) DimensionWhereDecisionBuildProgress(
	ctx context.Context,
	tenantID string,
) (progress DimensionWhereDecisionBuildProgress, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM
				  platform.dimension_where_design_policies),
				(SELECT count(*) FROM
				  platform.dimension_where_design_policies
				  WHERE status='SUCCEEDED'),
				(SELECT count(*) FROM
				  platform.dimension_member_semantic_documents),
				(SELECT count(*) FROM platform.dimension_where_decisions
				  WHERE source_type='DWS_PRECOMPUTED'),
				(SELECT count(*) FROM
				  platform.dimension_member_semantic_documents
				  WHERE embedding_status<>'SUCCEEDED')`,
		).Scan(
			&progress.PoliciesQueued, &progress.PoliciesSucceeded,
			&progress.EligibleMembers, &progress.MaterializedMembers,
			&progress.PendingVectors,
		)
	})
	return progress, err
}

func validDimensionWherePolicyDecision(
	claim DimensionWherePolicyClaim,
	decision DimensionWherePolicyDecision,
) error {
	operator := strings.ToUpper(strings.TrimSpace(
		decision.PredicateOperator,
	))
	if operator != "EQUALS" && operator != "CONTAINS" {
		return fmt.Errorf("%w: invalid predicate operator", ErrInvalidRequest)
	}
	if strings.TrimSpace(decision.LLMModel) == "" ||
		strings.TrimSpace(decision.LLMReason) == "" ||
		decision.Confidence < 0.80 || decision.Confidence > 1 {
		return fmt.Errorf("%w: incomplete LLM evidence", ErrInvalidRequest)
	}
	if operator == "CONTAINS" {
		delimited := false
		for _, sample := range claim.SampleValues {
			if strings.ContainsAny(sample, ",，;；|") {
				delimited = true
				break
			}
		}
		if !delimited {
			return fmt.Errorf(
				"%w: contains requires delimited sample evidence",
				ErrInvalidRequest,
			)
		}
	}
	return nil
}
