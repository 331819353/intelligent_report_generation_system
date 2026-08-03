package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) CreateQuestionTemplate(
	ctx context.Context,
	tenantID, actorID string,
	input CreateQuestionTemplateInput,
) (item QuestionTemplate, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var requiredSlots []byte
		var createdAt, updatedAt time.Time
		err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_question_templates(
				tenant_id,code,name,intent,required_slots_json,created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5::uuid,$5::uuid
			)
			RETURNING id::text,code,name,intent,required_slots_json,status,
				created_at,updated_at`,
			input.Code, input.Name, input.Intent, input.RequiredSlots, actorID,
		).Scan(
			&item.ID, &item.Code, &item.Name, &item.Intent, &requiredSlots,
			&item.Status, &createdAt, &updatedAt,
		)
		if err != nil {
			return mapPostgresError(err)
		}
		item.RequiredSlots = append(json.RawMessage(nil), requiredSlots...)
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return nil
	})
	return item, err
}

func (store *PostgresStore) ListQuestionTemplates(
	ctx context.Context,
	tenantID string,
) (items []QuestionTemplate, err error) {
	items = []QuestionTemplate{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,code,name,intent,
				required_slots_json,status,created_at,updated_at
			FROM platform.semantic_question_templates
			ORDER BY code,id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item QuestionTemplate
			var requiredSlots []byte
			var createdAt, updatedAt time.Time
			if err := rows.Scan(
				&item.ID, &item.Code, &item.Name, &item.Intent,
				&requiredSlots, &item.Status, &createdAt, &updatedAt,
			); err != nil {
				return err
			}
			item.RequiredSlots = append(json.RawMessage(nil), requiredSlots...)
			item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
			item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (store *PostgresStore) CreateGoldenQuestionSet(
	ctx context.Context,
	tenantID, actorID string,
	input CreateGoldenQuestionSetInput,
) (item GoldenQuestionSet, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var createdAt, updatedAt time.Time
		var sealedAt *time.Time
		err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_golden_question_sets(
				tenant_id,code,name,business_domain,version,
				correctness_threshold,safety_threshold,dataset_split,evaluation_mode,
				created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$9::uuid,$9::uuid
			)
			RETURNING id::text,code,name,business_domain,version,
				correctness_threshold::float8,safety_threshold::float8,
				dataset_split,evaluation_mode,sealed_content_hash,sealed_at,
				status,record_version,created_at,updated_at`,
			input.Code, input.Name, input.BusinessDomain, input.Version,
			input.CorrectnessThreshold, input.SafetyThreshold,
			input.DatasetSplit, input.EvaluationMode, actorID,
		).Scan(
			&item.ID, &item.Code, &item.Name, &item.BusinessDomain, &item.Version,
			&item.CorrectnessThreshold, &item.SafetyThreshold,
			&item.DatasetSplit, &item.EvaluationMode, &item.SealedContentHash,
			&sealedAt, &item.Status, &item.RecordVersion, &createdAt, &updatedAt,
		)
		if err != nil {
			return mapPostgresError(err)
		}
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		if sealedAt != nil {
			item.SealedAt = sealedAt.UTC().Format(time.RFC3339Nano)
		}
		return nil
	})
	return item, err
}

func (store *PostgresStore) ListGoldenQuestionSets(
	ctx context.Context,
	tenantID string,
) (items []GoldenQuestionSet, err error) {
	items = []GoldenQuestionSet{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,code,name,business_domain,
				version,correctness_threshold::float8,safety_threshold::float8,
				dataset_split,evaluation_mode,sealed_content_hash,sealed_at,
				status,record_version,created_at,updated_at
			FROM platform.semantic_golden_question_sets
			ORDER BY code,version DESC,id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item GoldenQuestionSet
			var createdAt, updatedAt time.Time
			var sealedAt *time.Time
			if err := rows.Scan(
				&item.ID, &item.Code, &item.Name, &item.BusinessDomain,
				&item.Version, &item.CorrectnessThreshold,
				&item.SafetyThreshold, &item.DatasetSplit, &item.EvaluationMode,
				&item.SealedContentHash, &sealedAt,
				&item.Status, &item.RecordVersion,
				&createdAt, &updatedAt,
			); err != nil {
				return err
			}
			item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
			item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
			if sealedAt != nil {
				item.SealedAt = sealedAt.UTC().Format(time.RFC3339Nano)
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (store *PostgresStore) ActivateGoldenQuestionSet(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedRecordVersion int64,
) (GoldenQuestionSet, error) {
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var code, status, datasetSplit, evaluationMode string
		var recordVersion int64
		if err := tx.QueryRow(ctx, `SELECT code,status,record_version,
				dataset_split,evaluation_mode
			FROM platform.semantic_golden_question_sets
			WHERE id=$1::uuid FOR UPDATE`, id).
			Scan(&code, &status, &recordVersion, &datasetSplit, &evaluationMode); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != "DRAFT" || recordVersion != expectedRecordVersion {
			return ErrConflict
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.semantic_golden_questions
			WHERE set_id=$1::uuid AND status='ACTIVE'`, id).Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			return ErrInvalidState
		}
		sealedContentHash := ""
		if datasetSplit == "SEALED" {
			if evaluationMode != "END_TO_END_RESULT_EQUIVALENCE" || count < 2000 {
				return ErrInvalidState
			}
			var eligibleCount int
			if err := tx.QueryRow(ctx, `SELECT count(*)::int
				FROM platform.semantic_golden_questions
				WHERE set_id=$1::uuid AND status='ACTIVE'
				  AND independent_review_count=2
				  AND approved_question<>''
				  AND (
				    expected_status<>'READY'
				    OR fixture_json->>'expectedResultHash' ~ '^[0-9a-f]{64}$'
				  )`, id).Scan(&eligibleCount); err != nil {
				return err
			}
			if eligibleCount != count {
				return ErrInvalidState
			}
			if err := tx.QueryRow(ctx, `SELECT encode(digest(string_agg(
				question_hash||':'||expected_path_hash||':'||expected_status||':'||
				COALESCE(fixture_json->>'expectedResultHash','')||':'||priority||':'||
				answerable::text||':'||security_expectation,
				'|' ORDER BY question_hash,id
			), 'sha256'),'hex')
				FROM platform.semantic_golden_questions
				WHERE set_id=$1::uuid AND status='ACTIVE'`, id).
				Scan(&sealedContentHash); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.semantic_golden_question_sets
			SET status='RETIRED',record_version=record_version+1,
				updated_by=$1::uuid,updated_at=now()
			WHERE code=$2 AND status='ACTIVE'`, actorID, code); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_golden_question_sets
			SET status='ACTIVE',record_version=record_version+1,
				updated_by=$1::uuid,activated_at=now(),updated_at=now(),
				sealed_content_hash=CASE WHEN dataset_split='SEALED' THEN $4 ELSE '' END,
				sealed_at=CASE WHEN dataset_split='SEALED' THEN now() ELSE NULL END
			WHERE id=$2::uuid AND status='DRAFT' AND record_version=$3`,
			actorID, id, expectedRecordVersion, sealedContentHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return GoldenQuestionSet{}, err
	}
	items, err := store.ListGoldenQuestionSets(ctx, tenantID)
	if err != nil {
		return GoldenQuestionSet{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return GoldenQuestionSet{}, ErrNotFound
}

func (store *PostgresStore) CreateGoldenQuestion(
	ctx context.Context,
	tenantID, actorID string,
	input CreateGoldenQuestionInput,
	questionHash string,
) (item GoldenQuestion, err error) {
	fixture, err := json.Marshal(input.Fixture)
	if err != nil {
		return item, err
	}
	answerable := true
	if input.Answerable != nil {
		answerable = *input.Answerable
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var createdAt, updatedAt time.Time
		var fixtureJSON []byte
		err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_golden_questions(
				tenant_id,set_id,question_hash,template_id,expected_path_hash,
				expected_status,fixture_json,approved_question,priority,answerable,
				security_expectation,independent_review_count,created_by,updated_by
			)
			SELECT platform.current_tenant_id(),question_set.id,$2,
				NULLIF($3::text,'')::uuid,$4,$5,$6::jsonb,
				CASE WHEN question_set.evaluation_mode='END_TO_END_RESULT_EQUIVALENCE'
				  THEN $7::text ELSE ''::text END,
				$8,$9,$10,$11,$12::uuid,$12::uuid
			FROM platform.semantic_golden_question_sets AS question_set
			WHERE question_set.id=$1::uuid AND question_set.status='DRAFT'
			  AND (
			    $3::text=''
			    OR EXISTS(
			      SELECT 1
			      FROM platform.semantic_question_templates AS template
			      WHERE template.id=$3::uuid
			        AND template.status='ACTIVE'
			        AND template.intent=($6::jsonb)->'queryPlan'->>'intent'
			    )
			  )
			RETURNING id::text,set_id::text,question_hash,
				COALESCE(template_id::text,''),expected_path_hash,
				expected_status,priority,answerable,security_expectation,
				independent_review_count,fixture_json,status,created_by::text,
				created_at,updated_at,approved_question`,
			input.SetID, questionHash, input.TemplateID, input.ExpectedPathHash,
			input.ExpectedStatus, fixture, input.Question, input.Priority, answerable,
			input.SecurityExpectation, input.IndependentReviewCount, actorID,
		).Scan(
			&item.ID, &item.SetID, &item.QuestionHash, &item.TemplateID,
			&item.ExpectedPathHash, &item.ExpectedStatus, &item.Priority,
			&item.Answerable, &item.SecurityExpectation,
			&item.IndependentReviewCount, &fixtureJSON,
			&item.Status, &item.createdBy, &createdAt, &updatedAt,
			&item.approvedQuestion,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidState
		}
		if err != nil {
			return mapPostgresError(err)
		}
		if err := json.Unmarshal(fixtureJSON, &item.Fixture); err != nil {
			return err
		}
		item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return nil
	})
	return item, err
}

const goldenQuestionColumns = `question.id::text,
	COALESCE(question.set_id::text,''),question.question_hash,
	COALESCE(question.template_id::text,''),question.expected_path_hash,
	question.expected_status,question.priority,question.answerable,
	question.security_expectation,question.independent_review_count,
	question.fixture_json,question.status,
	question.created_by::text,question.created_at,question.updated_at,
	question.approved_question,question_set.evaluation_mode,
	question_set.dataset_split`

func (store *PostgresStore) ListGoldenQuestions(
	ctx context.Context,
	tenantID, setID string,
) (items []GoldenQuestion, err error) {
	items = []GoldenQuestion{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+goldenQuestionColumns+`
			FROM platform.semantic_golden_questions AS question
			JOIN platform.semantic_golden_question_sets AS question_set
			  ON question_set.id=question.set_id
			 AND question_set.tenant_id=question.tenant_id
			WHERE question.set_id=$1::uuid
			ORDER BY question.question_hash,question.id`, setID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanGoldenQuestion(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (store *PostgresStore) GetGoldenQuestion(
	ctx context.Context,
	tenantID, id string,
) (item GoldenQuestion, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+goldenQuestionColumns+`
			FROM platform.semantic_golden_questions AS question
			JOIN platform.semantic_golden_question_sets AS question_set
			  ON question_set.tenant_id=question.tenant_id
			 AND question_set.id=question.set_id
			WHERE question.id=$1::uuid AND question.status='ACTIVE'
			  AND question_set.status IN ('DRAFT','ACTIVE')`, id)
		var scanErr error
		item, scanErr = scanGoldenQuestion(row)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return scanErr
	})
	return item, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanGoldenQuestion(row rowScanner) (item GoldenQuestion, err error) {
	var fixtureJSON []byte
	var createdAt, updatedAt time.Time
	err = row.Scan(
		&item.ID, &item.SetID, &item.QuestionHash, &item.TemplateID,
		&item.ExpectedPathHash, &item.ExpectedStatus, &item.Priority,
		&item.Answerable, &item.SecurityExpectation,
		&item.IndependentReviewCount, &fixtureJSON,
		&item.Status, &item.createdBy, &createdAt, &updatedAt,
		&item.approvedQuestion, &item.evaluationMode, &item.datasetSplit,
	)
	if err != nil {
		return item, err
	}
	if err := json.Unmarshal(fixtureJSON, &item.Fixture); err != nil {
		return item, err
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return item, nil
}

func (store *PostgresStore) RecordGoldenQuestionReplay(
	ctx context.Context,
	tenantID, actorID string,
	item GoldenQuestion,
	plan QueryPlan,
	failureStage, failureCode string,
) (result GoldenQuestionReplay, err error) {
	return store.RecordGoldenQuestionReplayV2(
		ctx, tenantID, actorID, item,
		GoldenQuestionReplayObservation{
			Plan: plan, FailureStage: failureStage, FailureCode: failureCode,
			EvaluationMode: "FIXTURE_REGRESSION",
		},
	)
}

func (store *PostgresStore) RecordGoldenQuestionReplayV2(
	ctx context.Context,
	tenantID, actorID string,
	item GoldenQuestion,
	observation GoldenQuestionReplayObservation,
) (result GoldenQuestionReplay, err error) {
	status := "PASSED"
	if observation.FailureCode != "" {
		status = "FAILED"
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var createdAt time.Time
		err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_golden_question_runs(
				tenant_id,golden_question_id,graph_generation_id,query_plan_id,
				status,expected_status,actual_status,expected_path_hash,
				actual_path_hash,failure_stage,failure_code,evaluation_mode,
				semantic_version,semantic_content_hash,expected_result_hash,
				actual_result_hash,direct_answer,refusal,unauthorized_blocked,
				sensitive_leak_detected,executed_by
			) VALUES(
				platform.current_tenant_id(),$1::uuid,NULLIF($2,'')::uuid,
				NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,$9,$10,$11,
				$12,$13,$14,$15,$16,$17,$18,$19,$20::uuid
			)
			RETURNING id::text,created_at`,
			item.ID, observation.Plan.GraphGenerationID, observation.Plan.ID, status,
			item.ExpectedStatus, observation.Plan.Status, item.ExpectedPathHash,
			observation.Plan.PathHash, observation.FailureStage,
			observation.FailureCode, observation.EvaluationMode,
			observation.SemanticVersion, observation.SemanticContentHash,
			observation.ExpectedResultHash, observation.ActualResultHash,
			observation.DirectAnswer, observation.Refusal,
			observation.UnauthorizedBlocked,
			observation.SensitiveLeakDetected, actorID,
		).Scan(&result.ID, &createdAt)
		if err != nil {
			return err
		}
		result.GoldenQuestionID = item.ID
		result.Status = status
		result.FailureStage = observation.FailureStage
		result.FailureCode = observation.FailureCode
		result.EvaluationMode = observation.EvaluationMode
		result.SemanticVersion = observation.SemanticVersion
		result.SemanticContentHash = observation.SemanticContentHash
		result.ExpectedResultHash = observation.ExpectedResultHash
		result.ActualResultHash = observation.ActualResultHash
		result.DirectAnswer = observation.DirectAnswer
		result.Refusal = observation.Refusal
		result.UnauthorizedBlocked = observation.UnauthorizedBlocked
		result.SensitiveLeakDetected = observation.SensitiveLeakDetected
		result.QueryPlan = observation.Plan
		result.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		return nil
	})
	return result, err
}

func (store *PostgresStore) ListMaterializationRecommendations(
	ctx context.Context,
	tenantID string,
	lookbackDays, minimumHits int,
) (items []MaterializationRecommendation, err error) {
	items = []MaterializationRecommendation{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH usage AS (
				SELECT selected_dataset_version_id AS version_id,
					count(*)::bigint AS hits,
					count(DISTINCT question_hash)::bigint AS questions,
					COALESCE(
					  round(avg(execution_duration_ms)
					    FILTER (WHERE execution_duration_ms IS NOT NULL)),
					  0
					)::bigint AS average_duration_ms,
					COALESCE(max(execution_duration_ms),0)::bigint
					  AS maximum_duration_ms
				FROM platform.semantic_query_plans
				WHERE created_at>=now()-make_interval(days=>$1)
				  AND status IN ('READY','EXECUTED')
				  AND selected_dataset_version_id IS NOT NULL
				GROUP BY selected_dataset_version_id
			)
			SELECT dataset.id::text,version.id::text,dataset.code::text,
				dataset.name,usage.hits,usage.questions,
				usage.average_duration_ms,usage.maximum_duration_ms,
				EXISTS(
				  SELECT 1 FROM platform.dataset_materializations AS materialization
				  WHERE materialization.dataset_id=dataset.id
				    AND materialization.dataset_version_id=version.id
				    AND materialization.status='ACTIVE'
				) AS active_materialized
			FROM usage
			JOIN platform.dataset_versions AS version
			  ON version.id=usage.version_id AND version.layer='DWS'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			 AND dataset.id=version.dataset_id
			 AND dataset.current_published_version_id=version.id
			 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
			ORDER BY usage.hits DESC,version.id`, lookbackDays)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item MaterializationRecommendation
			if err := rows.Scan(
				&item.DatasetID, &item.DatasetVersionID,
				&item.DatasetCode, &item.DatasetName,
				&item.QueryPlanHits, &item.DistinctQuestions,
				&item.AverageDurationMS, &item.MaximumDurationMS,
				&item.ActiveMaterialized,
			); err != nil {
				return err
			}
			switch {
			case item.ActiveMaterialized:
				item.Recommendation = "KEEP"
				item.ReasonCode = "ACTIVE_MATERIALIZATION_REUSED"
			case item.QueryPlanHits >= int64(minimumHits):
				item.Recommendation = "PROPOSE_MATERIALIZATION_CHANGE_SET"
				item.ReasonCode = "QUERY_PLAN_REUSE_THRESHOLD_REACHED"
			default:
				item.Recommendation = "LOGICAL_ONLY"
				item.ReasonCode = "BELOW_REUSE_THRESHOLD"
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}
