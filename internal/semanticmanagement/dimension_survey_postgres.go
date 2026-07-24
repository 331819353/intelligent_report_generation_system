package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const dimensionSurveyCandidateColumns = `candidate.id::text,
	candidate.survey_run_id::text,candidate.dataset_id::text,
	candidate.dataset_version_id::text,candidate.schema_hash,
	candidate.materialization_id::text,candidate.materialization_snapshot_hash,
	run.materialization_row_count,candidate.field_id,candidate.field_code,
	candidate.field_role,candidate.canonical_type,candidate.semantic_type,
	(candidate.risk_high_cardinality OR
	  COALESCE(profile.risk_high_cardinality,false)),
	candidate.risk_sensitive,
	candidate.evidence_json,candidate.proposed_code,candidate.proposed_name,
	candidate.proposed_description,candidate.proposed_dimension_type,
	candidate.proposed_member_index_policy,candidate.proposed_high_cardinality,
	candidate.proposed_sensitive,candidate.status,candidate.version,
	COALESCE(candidate.accepted_dimension_id::text,''),candidate.decision_reason,
	candidate.generated_by::text,candidate.updated_by::text,
	COALESCE(candidate.reviewed_by::text,''),candidate.reviewed_at,
	candidate.created_at,candidate.updated_at,
	COALESCE(profile.id::text,''),COALESCE(profile.status,'NOT_QUEUED'),
	COALESCE(profile.profile_version,''),COALESCE(profile.policy_version,''),
	COALESCE(profile.materialization_id::text,candidate.materialization_id::text),
	COALESCE(
	  profile.materialization_snapshot_hash,
	  candidate.materialization_snapshot_hash
	),
	COALESCE(profile.expected_row_count,run.materialization_row_count),
	profile.row_count,profile.non_null_count,profile.null_count,
	profile.distinct_count,COALESCE(profile.distinct_overflow,false),
	COALESCE(profile.distinct_cap,0),profile.distinct_ratio::float8,
	COALESCE(profile.risk_high_cardinality,false),
	COALESCE(profile.recommended_member_index_policy,''),
	COALESCE(profile.result_code,''),COALESCE(profile.evidence_hash,''),
	COALESCE(profile.attempt,0),COALESCE(profile.max_attempts,0),
	COALESCE(profile.created_at,'epoch'::timestamptz),
	COALESCE(profile.updated_at,'epoch'::timestamptz),
	profile.started_at,profile.completed_at`

const dimensionSurveyProfileJoin = ` LEFT JOIN platform.dimension_profile_jobs AS profile
	  ON profile.tenant_id=candidate.tenant_id
	 AND profile.materialization_id=candidate.materialization_id
	 AND profile.field_id=candidate.field_id
	 AND profile.profile_version='dws-dimension-profile-v1'
	 AND profile.policy_version='dimension-member-policy-v1'`

func (s *PostgresStore) ListDimensionSurveyCandidates(
	ctx context.Context,
	tenantID string,
	filter DimensionSurveyFilter,
) (items []DimensionSurveyCandidate, total int, err error) {
	items = []DimensionSurveyCandidate{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+
			dimensionSurveyCandidateColumns+`,count(*) OVER()::int
			FROM platform.dimension_survey_candidates AS candidate
			JOIN platform.dimension_survey_runs AS run
			  ON run.id=candidate.survey_run_id
			 AND run.tenant_id=candidate.tenant_id
			`+dimensionSurveyProfileJoin+`
			WHERE candidate.tenant_id=platform.current_tenant_id()
			  AND ($1='' OR candidate.dataset_id::text=$1)
			  AND ($2='' OR candidate.dataset_version_id::text=$2)
			  AND ($3='' OR candidate.status=$3)
			  AND ($4='' OR candidate.field_role=$4)
			ORDER BY candidate.updated_at DESC,candidate.id
			LIMIT $5 OFFSET $6`,
			filter.DatasetID, filter.DatasetVersionID, filter.Status,
			filter.FieldRole, filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DimensionSurveyCandidate
			if err := scanDimensionSurveyCandidate(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) GetDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, id string,
) (item DimensionSurveyCandidate, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return getDimensionSurveyCandidateTx(ctx, tx, id, false, &item)
	})
	return item, err
}

func (s *PostgresStore) UpdateDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	prepared PreparedDimension,
) (item DimensionSurveyCandidate, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var identity DimensionSurveyCandidate
		if err := getDimensionSurveyCandidateTx(ctx, tx, id, false, &identity); err != nil {
			return err
		}
		if err := lockDimensionSurveyGovernanceScope(ctx, tx, identity); err != nil {
			return err
		}
		var current DimensionSurveyCandidate
		if err := getDimensionSurveyCandidateTx(ctx, tx, id, true, &current); err != nil {
			return err
		}
		if current.Status != "SUGGESTED" || current.Version != expectedVersion ||
			!surveyPreparedMatchesIdentity(current, prepared) ||
			(current.ProposedHighCardinality && !prepared.HighCardinality) ||
			(current.ProposedSensitive && !prepared.Sensitive) ||
			indexPolicyRank(prepared.MemberIndexPolicy) <
				indexPolicyRank(current.ProposedMemberIndexPolicy) {
			return ErrConflict
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dimension_survey_candidates SET
				proposed_code=$1,proposed_name=$2,proposed_description=$3,
				proposed_dimension_type=$4,proposed_member_index_policy=$5,
				proposed_high_cardinality=$6,proposed_sensitive=$7,
				version=version+1,updated_by=$8,updated_at=clock_timestamp()
			WHERE id=$9::uuid AND version=$10 AND status='SUGGESTED'`,
			prepared.Code, prepared.Name, prepared.Description,
			prepared.DimensionType, prepared.MemberIndexPolicy,
			prepared.HighCardinality, prepared.Sensitive,
			actorID, id, expectedVersion)
		if err != nil {
			return mapWriteError(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if err := auditMutation(
			ctx, tx, actorID,
			"DIMENSION_SURVEY_CANDIDATE_UPDATE",
			"DIMENSION_SURVEY_CANDIDATE", id,
			map[string]any{
				"datasetVersionId": current.DatasetVersionID,
				"fieldId":          current.FieldID,
				"previousVersion":  expectedVersion,
			},
		); err != nil {
			return err
		}
		return getDimensionSurveyCandidateTx(ctx, tx, id, false, &item)
	})
	return item, err
}

func (s *PostgresStore) AcceptDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	prepared PreparedDimension,
) (result DimensionSurveyAcceptance, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var identity DimensionSurveyCandidate
		if err := getDimensionSurveyCandidateTx(ctx, tx, id, false, &identity); err != nil {
			return err
		}
		if err := lockDimensionSurveyGovernanceScope(ctx, tx, identity); err != nil {
			return err
		}
		var candidate DimensionSurveyCandidate
		if err := getDimensionSurveyCandidateTx(
			ctx, tx, id, true, &candidate,
		); err != nil {
			return err
		}
		if candidate.Status != "SUGGESTED" ||
			candidate.Version != expectedVersion ||
			!surveyPreparedMatches(candidate, prepared) {
			return ErrConflict
		}
		if err := validateSurveyAcceptanceTx(ctx, tx, candidate); err != nil {
			return err
		}

		var dimensionID string
		err := tx.QueryRow(ctx, `INSERT INTO platform.semantic_dimensions(
				tenant_id,dataset_id,dataset_version_id,field_id,
				code,name,description,dimension_type,member_index_policy,
				high_cardinality,sensitive,status,definition_hash,
				created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,
				$9,$10,'PUBLISHED',$11,$12,$12
			)
			RETURNING id::text`,
			prepared.DatasetID, prepared.DatasetVersionID, prepared.FieldID,
			prepared.Code, prepared.Name, prepared.Description,
			prepared.DimensionType, prepared.MemberIndexPolicy,
			prepared.HighCardinality, prepared.Sensitive,
			prepared.DefinitionHash, actorID).Scan(&dimensionID)
		if err != nil {
			return mapWriteError(err)
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dimension_survey_candidates SET
				status='ACCEPTED',accepted_dimension_id=$1,
				version=version+1,decision_reason='',
				updated_by=$2,reviewed_by=$2,reviewed_at=now(),
				updated_at=clock_timestamp()
			WHERE id=$3::uuid AND version=$4 AND status='SUGGESTED'`,
			dimensionID, actorID, id, expectedVersion)
		if err != nil {
			return mapWriteError(err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if err := auditMutation(
			ctx, tx, actorID,
			"DIMENSION_SURVEY_CANDIDATE_ACCEPT",
			"DIMENSION_SURVEY_CANDIDATE", id,
			map[string]any{
				"datasetVersionId":  candidate.DatasetVersionID,
				"materializationId": candidate.MaterializationID,
				"fieldId":           candidate.FieldID,
				"dimensionId":       dimensionID,
				"schemaHash":        candidate.SchemaHash,
			},
		); err != nil {
			return err
		}
		if err := getDimensionSurveyCandidateTx(
			ctx, tx, id, false, &result.Candidate,
		); err != nil {
			return err
		}
		if err := scanDimension(tx.QueryRow(ctx, `SELECT `+dimensionColumns+`
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=dimension.tenant_id
			 AND field.dataset_version_id=dimension.dataset_version_id
			 AND field.field_id=dimension.field_id
			WHERE dimension.id=$1::uuid`, dimensionID), &result.Dimension); err != nil {
			return err
		}
		switch result.Dimension.MemberIndexPolicy {
		case "FULL":
			job, err := enqueueSurveyDimensionRefreshTx(
				ctx, tx, actorID, candidate, result.Dimension,
			)
			if err != nil {
				return err
			}
			result.MemberRefreshJob = &job
			result.MemberSearchReady = false
			result.NextAction = "WAIT_FOR_MEMBER_REFRESH"
		case "EXACT_ONLY":
			result.MemberSearchReady = false
			result.NextAction = "USE_EXACT_MATCH_ONLY"
		case "NONE":
			result.MemberSearchReady = false
			result.NextAction = "MEMBER_INDEX_DISABLED"
		default:
			return ErrConflict
		}
		return nil
	})
	return result, err
}

func enqueueSurveyDimensionRefreshTx(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	candidate DimensionSurveyCandidate,
	dimension Dimension,
) (RefreshJob, error) {
	payload, err := json.Marshal(struct {
		Source              string
		CandidateID         string
		DimensionID         string
		DimensionVersion    int64
		MaterializationID   string
		MaterializationHash string
		MaxMembers          int
		TimeoutSeconds      int
	}{
		Source:              DimensionSurveyVersion,
		CandidateID:         candidate.ID,
		DimensionID:         dimension.ID,
		DimensionVersion:    dimension.Version,
		MaterializationID:   candidate.MaterializationID,
		MaterializationHash: candidate.MaterializationSnapshotHash,
		MaxMembers:          defaultRefreshMaxMembers,
		TimeoutSeconds:      defaultRefreshTimeout,
	})
	if err != nil {
		return RefreshJob{}, err
	}
	requestHash := hashBytes(payload)
	idempotencyKey := hashBytes([]byte(
		DimensionSurveyVersion + ":accept:" + candidate.ID,
	))
	var jobID string
	err = tx.QueryRow(ctx, `INSERT INTO platform.dimension_member_refresh_jobs(
			tenant_id,dimension_id,dimension_version,dataset_id,dataset_version_id,
			field_id,field_code,member_index_policy,materialization_id,status,
			max_members,timeout_seconds,request_hash,idempotency_key,requested_by
		) VALUES(
			platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,'FULL',$7,'QUEUED',
			$8,$9,$10,$11,$12
		)
		RETURNING id::text`,
		dimension.ID, dimension.Version, candidate.DatasetID,
		candidate.DatasetVersionID, candidate.FieldID, candidate.FieldCode,
		candidate.MaterializationID, defaultRefreshMaxMembers,
		defaultRefreshTimeout, requestHash, idempotencyKey, actorID,
	).Scan(&jobID)
	if err != nil {
		return RefreshJob{}, mapWriteError(err)
	}
	if err := auditMutation(
		ctx, tx, actorID,
		"DIMENSION_MEMBER_REFRESH_REQUEST",
		"DIMENSION_MEMBER_REFRESH_JOB", jobID,
		map[string]any{
			"dimensionId":       dimension.ID,
			"candidateId":       candidate.ID,
			"materializationId": candidate.MaterializationID,
			"status":            "QUEUED",
			"source":            DimensionSurveyVersion,
		},
	); err != nil {
		return RefreshJob{}, err
	}
	return getRefreshJob(ctx, tx, jobID)
}

func (s *PostgresStore) RejectDimensionSurveyCandidate(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	reason string,
) (item DimensionSurveyCandidate, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.dimension_survey_candidates SET
				status='REJECTED',version=version+1,decision_reason=$1,
				updated_by=$2,reviewed_by=$2,reviewed_at=now(),
				updated_at=clock_timestamp()
			WHERE id=$3::uuid AND version=$4 AND status='SUGGESTED'`,
			reason, actorID, id, expectedVersion)
		if err != nil {
			return mapWriteError(err)
		}
		if tag.RowsAffected() != 1 {
			return classifyMissingOrConflict(
				ctx, tx, "platform.dimension_survey_candidates", id,
			)
		}
		if err := auditMutation(
			ctx, tx, actorID,
			"DIMENSION_SURVEY_CANDIDATE_REJECT",
			"DIMENSION_SURVEY_CANDIDATE", id,
			map[string]any{
				"previousVersion": expectedVersion,
				"reason":          reason,
			},
		); err != nil {
			return err
		}
		return getDimensionSurveyCandidateTx(ctx, tx, id, false, &item)
	})
	return item, err
}

func getDimensionSurveyCandidateTx(
	ctx context.Context,
	tx pgx.Tx,
	id string,
	forUpdate bool,
	item *DimensionSurveyCandidate,
) error {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE OF candidate"
	}
	err := scanDimensionSurveyCandidate(tx.QueryRow(ctx, `SELECT `+
		dimensionSurveyCandidateColumns+`
		FROM platform.dimension_survey_candidates AS candidate
		JOIN platform.dimension_survey_runs AS run
		  ON run.id=candidate.survey_run_id
		 AND run.tenant_id=candidate.tenant_id
		`+dimensionSurveyProfileJoin+`
		WHERE candidate.id=$1::uuid`+lock, id), item)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func lockDimensionSurveyGovernanceScope(
	ctx context.Context,
	tx pgx.Tx,
	candidate DimensionSurveyCandidate,
) error {
	if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
		return err
	}
	var locked int
	err := tx.QueryRow(ctx, `SELECT 1
		FROM platform.dataset_materializations AS materialization
		WHERE materialization.id=$1::uuid
		  AND materialization.tenant_id=platform.current_tenant_id()
		  AND materialization.dataset_id=$2::uuid
		  AND materialization.dataset_version_id=$3::uuid
		FOR SHARE OF materialization`,
		candidate.MaterializationID, candidate.DatasetID,
		candidate.DatasetVersionID,
	).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended(
		  'dimension-dataset-profile:'||platform.current_tenant_id()::text||
		  ':'||$1::text,
		  0
		)
	)`, candidate.DatasetID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended(
		  'dimension-field-risk:'||platform.current_tenant_id()::text||
		  ':'||$1::text||':'||$2::text,
		  0
		)
	)`, candidate.DatasetVersionID, candidate.FieldID)
	return err
}

func validateSurveyAcceptanceTx(
	ctx context.Context,
	tx pgx.Tx,
	candidate DimensionSurveyCandidate,
) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.dimension_survey_runs AS run
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.id=run.materialization_id
		 AND materialization.tenant_id=run.tenant_id
		 AND materialization.dataset_id=run.dataset_id
		 AND materialization.dataset_version_id=run.dataset_version_id
		JOIN platform.dataset_versions AS version
		  ON version.id=run.dataset_version_id
		 AND version.dataset_id=run.dataset_id
		 AND version.tenant_id=run.tenant_id
		JOIN platform.datasets AS dataset
		  ON dataset.id=version.dataset_id
		 AND dataset.tenant_id=version.tenant_id
		JOIN platform.dataset_fields AS field
		  ON field.tenant_id=version.tenant_id
		 AND field.dataset_version_id=version.id
		WHERE run.id=$1::uuid
		  AND run.status='SUCCEEDED'
		  AND run.schema_hash=$2
		  AND run.materialization_id=$3::uuid
		  AND run.materialization_snapshot_hash=$4
		  AND materialization.status='ACTIVE'
		  AND materialization.layer='DWS'
		  AND materialization.schema_hash=$2
		  AND materialization.snapshot_hash=$4
		  AND version.status='PUBLISHED'
		  AND version.layer='DWS'
		  AND version.schema_hash=$2
		  AND dataset.status='PUBLISHED'
		  AND dataset.layer='DWS'
		  AND dataset.current_published_version_id=version.id
		  AND dataset.deleted_at IS NULL
		  AND field.field_id=$5
		  AND field.field_code::text=$6
		  AND field.field_role=$7
		  AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')
		  AND (
		    NOT EXISTS(
		      SELECT 1
		      FROM platform.asset_tag_bindings AS sensitivity_binding
		      JOIN platform.semantic_tags AS sensitivity_tag
		        ON sensitivity_tag.id=sensitivity_binding.tag_id
		       AND sensitivity_tag.tenant_id=sensitivity_binding.tenant_id
		       AND sensitivity_tag.status='ACTIVE'
		       AND sensitivity_tag.category='SENSITIVITY'
		      WHERE sensitivity_binding.tenant_id=version.tenant_id
		        AND sensitivity_binding.asset_type='DATASET_FIELD'
		        AND sensitivity_binding.dataset_id=version.dataset_id
		        AND sensitivity_binding.dataset_version_id=version.id
		        AND sensitivity_binding.dataset_field_id=field.field_id
		        AND sensitivity_binding.status='APPROVED'
		    )
		    OR ($10 AND $8='NONE')
		  )
		  AND (
		    $8='NONE'
		    OR EXISTS(
		      SELECT 1
		      FROM platform.dimension_profile_jobs AS profile
		      WHERE profile.tenant_id=version.tenant_id
		        AND profile.dataset_id=version.dataset_id
		        AND profile.dataset_version_id=version.id
		        AND profile.materialization_id=materialization.id
		        AND profile.schema_hash=materialization.schema_hash
		        AND profile.materialization_snapshot_hash=materialization.snapshot_hash
		        AND profile.field_id=field.field_id
		        AND profile.profile_version='dws-dimension-profile-v1'
		        AND profile.policy_version='dimension-member-policy-v1'
		        AND profile.status IN ('SUCCEEDED','SKIPPED_POLICY')
		        AND (
		          ($8='FULL'
		            AND profile.status='SUCCEEDED'
		            AND profile.recommended_member_index_policy='FULL')
		          OR
		          ($8='EXACT_ONLY'
		            AND profile.recommended_member_index_policy
		              IN ('FULL','EXACT_ONLY')
		            AND (
		              profile.status='SUCCEEDED'
		              OR (
		                profile.status='SKIPPED_POLICY'
		                AND profile.result_code=
		                  'IDENTIFIER_FIELD_PROFILE_SKIPPED'
		              )
		            ))
		        )
		        AND (NOT profile.risk_high_cardinality OR $9)
		    )
		  )
		  AND NOT EXISTS(
		    SELECT 1
		    FROM platform.semantic_dimensions AS dimension
		    WHERE dimension.tenant_id=version.tenant_id
		      AND dimension.dataset_version_id=version.id
		      AND dimension.field_id=field.field_id
		      AND dimension.status<>'DEPRECATED'
		  )
		FOR SHARE OF run,materialization,version,dataset,field
	)`,
		candidate.SurveyRunID, candidate.SchemaHash,
		candidate.MaterializationID, candidate.MaterializationSnapshotHash,
		candidate.FieldID, candidate.FieldCode, candidate.FieldRole,
		candidate.ProposedMemberIndexPolicy,
		candidate.ProposedHighCardinality,
		candidate.ProposedSensitive).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

func surveyPreparedMatchesIdentity(
	candidate DimensionSurveyCandidate,
	prepared PreparedDimension,
) bool {
	return prepared.DatasetID == candidate.DatasetID &&
		prepared.DatasetVersionID == candidate.DatasetVersionID &&
		prepared.FieldID == candidate.FieldID &&
		(!candidate.RiskHighCardinality || prepared.HighCardinality) &&
		(!candidate.RiskSensitive || prepared.Sensitive) &&
		(!prepared.HighCardinality ||
			prepared.MemberIndexPolicy != "FULL") &&
		(!prepared.Sensitive || prepared.MemberIndexPolicy != "FULL")
}

func surveyPreparedMatches(
	candidate DimensionSurveyCandidate,
	prepared PreparedDimension,
) bool {
	return surveyPreparedMatchesIdentity(candidate, prepared) &&
		prepared.Code == candidate.ProposedCode &&
		prepared.Name == candidate.ProposedName &&
		prepared.Description == candidate.ProposedDescription &&
		prepared.DimensionType == candidate.ProposedDimensionType &&
		prepared.MemberIndexPolicy == candidate.ProposedMemberIndexPolicy &&
		prepared.HighCardinality == candidate.ProposedHighCardinality &&
		prepared.Sensitive == candidate.ProposedSensitive &&
		prepared.Status == "PUBLISHED"
}

func scanDimensionSurveyCandidate(
	row scanRow,
	item *DimensionSurveyCandidate,
	extra ...any,
) error {
	targets := []any{
		&item.ID, &item.SurveyRunID, &item.DatasetID,
		&item.DatasetVersionID, &item.SchemaHash,
		&item.MaterializationID, &item.MaterializationSnapshotHash,
		&item.MaterializationRowCount, &item.FieldID, &item.FieldCode,
		&item.FieldRole, &item.CanonicalType, &item.SemanticType,
		&item.RiskHighCardinality, &item.RiskSensitive, &item.Evidence,
		&item.ProposedCode, &item.ProposedName, &item.ProposedDescription,
		&item.ProposedDimensionType, &item.ProposedMemberIndexPolicy,
		&item.ProposedHighCardinality, &item.ProposedSensitive,
		&item.Status, &item.Version, &item.AcceptedDimensionID,
		&item.DecisionReason, &item.GeneratedBy, &item.UpdatedBy,
		&item.ReviewedBy, &item.ReviewedAt, &item.CreatedAt, &item.UpdatedAt,
		&item.Profile.ID, &item.Profile.Status,
		&item.Profile.ProfileVersion, &item.Profile.PolicyVersion,
		&item.Profile.MaterializationID,
		&item.Profile.MaterializationSnapshotHash,
		&item.Profile.ExpectedRowCount, &item.Profile.RowCount,
		&item.Profile.NonNullCount, &item.Profile.NullCount,
		&item.Profile.DistinctCount, &item.Profile.DistinctOverflow,
		&item.Profile.DistinctCap, &item.Profile.DistinctRatio,
		&item.Profile.RiskHighCardinality,
		&item.Profile.RecommendedMemberIndexPolicy,
		&item.Profile.ResultCode, &item.Profile.EvidenceHash,
		&item.Profile.Attempt, &item.Profile.MaxAttempts,
		&item.Profile.CreatedAt, &item.Profile.UpdatedAt,
		&item.Profile.StartedAt, &item.Profile.CompletedAt,
	}
	return row.Scan(append(targets, extra...)...)
}

var _ DimensionSurveyStore = (*PostgresStore)(nil)
