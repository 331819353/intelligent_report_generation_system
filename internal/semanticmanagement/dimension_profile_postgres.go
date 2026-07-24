package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/platform/database"
)

const dimensionProfileJobColumns = `profile.id::text,profile.status,
	profile.profile_version,profile.policy_version,
	profile.materialization_id::text,profile.materialization_snapshot_hash,
	profile.expected_row_count,profile.row_count,profile.non_null_count,
	profile.null_count,profile.distinct_count,profile.distinct_overflow,
	profile.distinct_cap,profile.distinct_ratio::float8,
	profile.risk_high_cardinality,profile.recommended_member_index_policy,
	profile.result_code,profile.evidence_hash,profile.attempt,profile.max_attempts,
	profile.created_at,profile.updated_at,profile.started_at,profile.completed_at,
	profile.tenant_id::text,profile.dataset_id::text,
	profile.dataset_version_id::text,profile.schema_hash,
	profile.field_id,profile.field_code,profile.field_role,
	profile.canonical_type,profile.semantic_type,
	profile.high_ratio_threshold::float8,profile.high_ratio_min_non_null,
	profile.timeout_seconds,profile.work_mem_kb,profile.temp_file_limit_kb,
	profile.requested_by::text,profile.next_attempt_at,
	profile.lease_owner,COALESCE(profile.lease_token::text,''),
	COALESCE(profile.lease_expires_at,'epoch'::timestamptz)`

func (s *PostgresStore) ListProfileTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
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

func (s *PostgresStore) ClaimDimensionProfile(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *DimensionProfileJob, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.dimension_profile_jobs
			SET status='FAILED',result_code='PROFILE_LEASE_EXPIRED',
				recommended_member_index_policy='NONE',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts`); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `WITH candidate AS (
				SELECT id
				FROM platform.dimension_profile_jobs
				WHERE attempt<max_attempts
				  AND (
				    (status='QUEUED' AND next_attempt_at<=now())
				    OR (status='RUNNING' AND lease_expires_at<=now())
				  )
				ORDER BY created_at,id
				FOR UPDATE SKIP LOCKED
				LIMIT 1
			)
			UPDATE platform.dimension_profile_jobs AS profile SET
				status='RUNNING',attempt=profile.attempt+1,
				started_at=COALESCE(profile.started_at,now()),completed_at=NULL,
				lease_owner=$1,lease_token=gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 second'),
				row_count=NULL,non_null_count=NULL,null_count=NULL,
				distinct_count=NULL,distinct_overflow=false,distinct_ratio=NULL,
				risk_high_cardinality=false,
				recommended_member_index_policy='',evidence_hash='',result_code=''
			FROM candidate
			WHERE profile.id=candidate.id
			RETURNING `+dimensionProfileJobColumns,
			workerID, int64(lease/time.Second))
		item := DimensionProfileJob{}
		if err := scanDimensionProfileJob(row, &item); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		claim = &item
		return nil
	})
	return claim, err
}

func (s *PostgresStore) HeartbeatDimensionProfile(
	ctx context.Context,
	claim DimensionProfileJob,
	lease time.Duration,
) (updated DimensionProfileJob, err error) {
	err = database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE platform.dimension_profile_jobs AS profile
			SET lease_expires_at=now()+($1*interval '1 second')
			WHERE profile.id=$2::uuid AND profile.status='RUNNING'
			  AND profile.attempt=$3 AND profile.lease_owner=$4
			  AND profile.lease_token=$5::uuid AND profile.lease_expires_at>now()
			RETURNING `+dimensionProfileJobColumns,
			int64(lease/time.Second), claim.ID, claim.Attempt,
			claim.LeaseOwner, claim.LeaseToken)
		if err := scanDimensionProfileJob(row, &updated); errors.Is(err, pgx.ErrNoRows) {
			return ErrProfileLeaseLost
		} else {
			return err
		}
	})
	return updated, err
}

func (s *PostgresStore) MeasureDimensionProfile(
	ctx context.Context,
	claim DimensionProfileJob,
) (observation DimensionProfileObservation, err error) {
	err = database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := lockDimensionProfileGovernanceScope(ctx, tx, claim); err != nil {
			return err
		}
		if err := verifyDimensionProfileLease(ctx, tx, claim, false); err != nil {
			return err
		}
		var buildRunID, layer, status, schemaHash, snapshotHash string
		var expectedRows int64
		var sensitive bool
		var physical materialization.PhysicalIdentifier
		err := tx.QueryRow(ctx, `SELECT
				materialization.build_run_id::text,materialization.layer,
				materialization.status,materialization.physical_schema,
				materialization.physical_name,materialization.published_schema,
				materialization.published_name,materialization.schema_hash,
				materialization.snapshot_hash,materialization.row_count,
				(
				  EXISTS(
				    SELECT 1
				    FROM platform.asset_tag_bindings AS binding
				    JOIN platform.semantic_tags AS tag
				      ON tag.id=binding.tag_id
				     AND tag.tenant_id=binding.tenant_id
				     AND tag.category='SENSITIVITY'
				    WHERE binding.tenant_id=materialization.tenant_id
				      AND binding.asset_type='DATASET_FIELD'
				      AND binding.dataset_id=materialization.dataset_id
				      AND binding.dataset_version_id=
				        materialization.dataset_version_id
				      AND binding.dataset_field_id=field.field_id
				      AND binding.status='APPROVED'
				  )
				  OR EXISTS(
				    SELECT 1
				    FROM platform.semantic_dimensions AS prior_dimension
				    WHERE prior_dimension.tenant_id=materialization.tenant_id
				      AND prior_dimension.dataset_id=materialization.dataset_id
				      AND prior_dimension.dataset_version_id=
				        materialization.dataset_version_id
				      AND prior_dimension.field_id=field.field_id
				      AND prior_dimension.status<>'DEPRECATED'
				      AND prior_dimension.sensitive
				  )
				  OR EXISTS(
				    SELECT 1
				    FROM platform.dimension_survey_candidates AS prior_candidate
				    WHERE prior_candidate.tenant_id=materialization.tenant_id
				      AND prior_candidate.dataset_id=materialization.dataset_id
				      AND prior_candidate.dataset_version_id=
				        materialization.dataset_version_id
				      AND prior_candidate.field_id=field.field_id
				      AND (
				        prior_candidate.risk_sensitive
				        OR prior_candidate.proposed_sensitive
				      )
				  )
				  OR EXISTS(
				    SELECT 1
				    FROM platform.dimension_profile_jobs AS prior_profile
				    WHERE prior_profile.tenant_id=materialization.tenant_id
				      AND prior_profile.dataset_id=materialization.dataset_id
				      AND prior_profile.dataset_version_id=
				        materialization.dataset_version_id
				      AND prior_profile.field_id=field.field_id
				      AND prior_profile.result_code=
				        'SENSITIVE_FIELD_PROFILE_SKIPPED'
				  )
				)
			FROM platform.dataset_materializations AS materialization
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=materialization.tenant_id
			 AND version.dataset_id=materialization.dataset_id
			 AND version.id=materialization.dataset_version_id
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			 AND dataset.id=version.dataset_id
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=version.tenant_id
			 AND field.dataset_version_id=version.id
			WHERE materialization.id=$1::uuid
			  AND materialization.dataset_id=$2::uuid
			  AND materialization.dataset_version_id=$3::uuid
			  AND version.layer='DWS' AND version.status='PUBLISHED'
			  AND version.schema_hash=$4
			  AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
			  AND dataset.current_published_version_id=version.id
			  AND dataset.deleted_at IS NULL
			  AND field.field_id=$5
			  AND field.field_code::text=$6
			  AND field.field_role=$7
			  AND field.canonical_type=$8
			  AND field.semantic_type=$9
			FOR SHARE OF materialization,version,dataset,field`,
			claim.MaterializationID, claim.DatasetID, claim.DatasetVersionID,
			claim.SchemaHash, claim.FieldID, claim.FieldCode, claim.FieldRole,
			claim.CanonicalType, claim.SemanticType).
			Scan(
				&buildRunID, &layer, &status,
				&physical.Schema, &physical.Name,
				&physical.PublishedSchema, &physical.PublishedName,
				&schemaHash, &snapshotHash, &expectedRows, &sensitive,
			)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProfileSourceChanged
		}
		if err != nil {
			return err
		}
		if layer != "DWS" || status != "ACTIVE" ||
			schemaHash != claim.SchemaHash ||
			snapshotHash != claim.MaterializationSnapshotHash ||
			expectedRows != claim.ExpectedRowCount ||
			materialization.ValidatePhysicalIdentifier(
				physical, claim.TenantID, claim.DatasetID,
				buildRunID, materialization.LayerDWS,
			) != nil {
			return ErrProfileSourceChanged
		}
		physicalTx := tx
		if s.warehousePool != nil && s.warehousePool != s.pool {
			warehouseTx, beginErr := s.warehousePool.BeginTx(
				ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly},
			)
			if beginErr != nil {
				return beginErr
			}
			defer warehouseTx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
			physicalTx = warehouseTx
		}
		if err := lockAndVerifyPublishedView(ctx, physicalTx, physical); err != nil {
			return ErrProfileUnsafeView
		}
		if err := requirePublishedColumn(ctx, physicalTx, physical, claim.FieldCode); err != nil {
			return ErrProfileSourceChanged
		}
		if sensitive {
			observation.PolicySkipped = true
			observation.PolicySkipCode = "SENSITIVE_FIELD_PROFILE_SKIPPED"
			return nil
		}
		if claim.FieldRole == "IDENTIFIER" ||
			strings.EqualFold(claim.SemanticType, "IDENTIFIER") {
			observation.PolicySkipped = true
			observation.PolicySkipCode = "IDENTIFIER_FIELD_PROFILE_SKIPPED"
			observation.RiskHighCardinality = true
			return nil
		}
		if err := lockDimensionProfilePhysical(ctx, physicalTx, physical); err != nil {
			return err
		}
		if physicalTx == tx {
			if err := setDimensionProfileLimits(ctx, tx, claim); err != nil {
				return err
			}
		} else if err := setDimensionProfileWarehouseLimits(ctx, physicalTx, claim); err != nil {
			return err
		}

		qualified := quoteTrustedIdentifier(physical.PublishedSchema) + "." +
			quoteTrustedIdentifier(physical.PublishedName)
		quotedField := quoteTrustedIdentifier(claim.FieldCode)
		if err := physicalTx.QueryRow(ctx, `SELECT count(*)::bigint,
				count(`+quotedField+`)::bigint
			FROM `+qualified).Scan(
			&observation.RowCount, &observation.NonNullCount,
		); err != nil {
			return classifyDimensionProfileDatabaseError(err)
		}
		observation.NullCount = observation.RowCount - observation.NonNullCount
		if observation.RowCount != claim.ExpectedRowCount {
			return ErrProfileSourceChanged
		}

		var boundedDistinct int64
		distinctLimit := int64(claim.DistinctCap) + 1
		groupExpression := quotedField
		if textualDimensionProfileType(claim.CanonicalType) {
			groupExpression += ` COLLATE "C"`
		}
		distinctSQL := `SELECT count(*)::bigint FROM (
			SELECT ` + groupExpression + ` AS dimension_value
			FROM ` + qualified + `
			WHERE ` + quotedField + ` IS NOT NULL
			GROUP BY 1
			LIMIT $1
		) AS bounded_dimension_values`
		if err := physicalTx.QueryRow(ctx, distinctSQL, distinctLimit).Scan(
			&boundedDistinct,
		); err != nil {
			return classifyDimensionProfileDatabaseError(err)
		}
		observation.DistinctOverflow = boundedDistinct > int64(claim.DistinctCap)
		observation.DistinctCount = boundedDistinct
		if observation.DistinctOverflow {
			observation.DistinctCount = int64(claim.DistinctCap)
		}
		if observation.NonNullCount > 0 {
			observation.DistinctRatio = float64(observation.DistinctCount) /
				float64(observation.NonNullCount)
		}
		identifierRisk := claim.FieldRole == "IDENTIFIER" ||
			strings.EqualFold(claim.SemanticType, "IDENTIFIER")
		ratioRisk := observation.NonNullCount >= claim.HighRatioMinNonNull &&
			observation.DistinctRatio >= claim.HighRatioThreshold
		observation.RiskHighCardinality =
			identifierRisk || observation.DistinctOverflow || ratioRisk
		document, marshalErr := json.Marshal(struct {
			Version                     string  `json:"version"`
			PolicyVersion               string  `json:"policyVersion"`
			MaterializationID           string  `json:"materializationId"`
			MaterializationSnapshotHash string  `json:"materializationSnapshotHash"`
			SchemaHash                  string  `json:"schemaHash"`
			FieldID                     string  `json:"fieldId"`
			RowCount                    int64   `json:"rowCount"`
			NonNullCount                int64   `json:"nonNullCount"`
			NullCount                   int64   `json:"nullCount"`
			DistinctCount               int64   `json:"distinctCount"`
			DistinctOverflow            bool    `json:"distinctOverflow"`
			DistinctCap                 int     `json:"distinctCap"`
			HighRatioThreshold          float64 `json:"highRatioThreshold"`
			HighRatioMinNonNull         int64   `json:"highRatioMinNonNull"`
			RiskHighCardinality         bool    `json:"riskHighCardinality"`
		}{
			Version:                     claim.ProfileVersion,
			PolicyVersion:               claim.PolicyVersion,
			MaterializationID:           claim.MaterializationID,
			MaterializationSnapshotHash: claim.MaterializationSnapshotHash,
			SchemaHash:                  claim.SchemaHash,
			FieldID:                     claim.FieldID,
			RowCount:                    observation.RowCount,
			NonNullCount:                observation.NonNullCount,
			NullCount:                   observation.NullCount,
			DistinctCount:               observation.DistinctCount,
			DistinctOverflow:            observation.DistinctOverflow,
			DistinctCap:                 claim.DistinctCap,
			HighRatioThreshold:          claim.HighRatioThreshold,
			HighRatioMinNonNull:         claim.HighRatioMinNonNull,
			RiskHighCardinality:         observation.RiskHighCardinality,
		})
		if marshalErr != nil {
			return marshalErr
		}
		observation.EvidenceHash = hashBytes(document)
		return nil
	})
	return observation, classifyDimensionProfileDatabaseError(err)
}

func textualDimensionProfileType(canonicalType string) bool {
	switch strings.ToUpper(strings.TrimSpace(canonicalType)) {
	case "STRING", "TEXT", "VARCHAR", "CHAR":
		return true
	default:
		return false
	}
}

func (s *PostgresStore) CompleteDimensionProfile(
	ctx context.Context,
	claim DimensionProfileJob,
	observation DimensionProfileObservation,
) error {
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := lockDimensionProfileGovernanceScope(ctx, tx, claim); err != nil {
			return err
		}
		if err := verifyDimensionProfileLease(ctx, tx, claim, true); err != nil {
			return err
		}
		var sourceValid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.dataset_materializations AS materialization
			JOIN platform.dataset_versions AS version
			  ON version.id=materialization.dataset_version_id
			 AND version.dataset_id=materialization.dataset_id
			 AND version.tenant_id=materialization.tenant_id
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id
			 AND dataset.tenant_id=version.tenant_id
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=version.tenant_id
			 AND field.dataset_version_id=version.id
			WHERE materialization.id=$1::uuid
			  AND materialization.dataset_id=$2::uuid
			  AND materialization.dataset_version_id=$3::uuid
			  AND materialization.layer='DWS'
			  AND materialization.status='ACTIVE'
			  AND materialization.schema_hash=$4
			  AND materialization.snapshot_hash=$5
			  AND materialization.row_count=$6
			  AND version.status='PUBLISHED' AND version.layer='DWS'
			  AND version.schema_hash=$4
			  AND dataset.status='PUBLISHED' AND dataset.layer='DWS'
			  AND dataset.current_published_version_id=version.id
			  AND dataset.deleted_at IS NULL
			  AND field.field_id=$7 AND field.field_code::text=$8
			  AND field.field_role=$9
			  AND field.canonical_type=$10
			  AND field.semantic_type=$11
			FOR SHARE OF materialization,version,dataset,field
		)`,
			claim.MaterializationID, claim.DatasetID, claim.DatasetVersionID,
			claim.SchemaHash, claim.MaterializationSnapshotHash,
			claim.ExpectedRowCount, claim.FieldID, claim.FieldCode,
			claim.FieldRole, claim.CanonicalType, claim.SemanticType).
			Scan(&sourceValid); err != nil {
			return err
		}
		if !sourceValid {
			return ErrProfileSourceChanged
		}

		var sensitive bool
		if err := tx.QueryRow(ctx, `SELECT
			EXISTS(
			  SELECT 1
			  FROM platform.asset_tag_bindings AS binding
			  JOIN platform.semantic_tags AS tag
			    ON tag.id=binding.tag_id
			   AND tag.tenant_id=binding.tenant_id
			   AND tag.category='SENSITIVITY'
			  WHERE binding.asset_type='DATASET_FIELD'
			    AND binding.dataset_id=$1::uuid
			    AND binding.dataset_version_id=$2::uuid
			    AND binding.dataset_field_id=$3
			    AND binding.status='APPROVED'
			)
			OR EXISTS(
			  SELECT 1
			  FROM platform.semantic_dimensions AS prior_dimension
			  WHERE prior_dimension.dataset_id=$1::uuid
			    AND prior_dimension.dataset_version_id=$2::uuid
			    AND prior_dimension.field_id=$3
			    AND prior_dimension.status<>'DEPRECATED'
			    AND prior_dimension.sensitive
			)
			OR EXISTS(
			  SELECT 1
			  FROM platform.dimension_survey_candidates AS prior_candidate
			  WHERE prior_candidate.dataset_id=$1::uuid
			    AND prior_candidate.dataset_version_id=$2::uuid
			    AND prior_candidate.field_id=$3
			    AND (
			      prior_candidate.risk_sensitive
			      OR prior_candidate.proposed_sensitive
			    )
			)
			OR EXISTS(
			  SELECT 1
			  FROM platform.dimension_profile_jobs AS prior_profile
			  WHERE prior_profile.dataset_id=$1::uuid
			    AND prior_profile.dataset_version_id=$2::uuid
			    AND prior_profile.field_id=$3
			    AND prior_profile.id<>$4::uuid
			    AND prior_profile.result_code=
			      'SENSITIVE_FIELD_PROFILE_SKIPPED'
			)`,
			claim.DatasetID, claim.DatasetVersionID, claim.FieldID, claim.ID).
			Scan(&sensitive); err != nil {
			return err
		}
		identifier := claim.FieldRole == "IDENTIFIER" ||
			strings.EqualFold(claim.SemanticType, "IDENTIFIER")
		policySkipCode := ""
		recommended := "FULL"
		if sensitive {
			policySkipCode = "SENSITIVE_FIELD_PROFILE_SKIPPED"
			recommended = "NONE"
			observation.RiskHighCardinality = false
		} else if identifier {
			policySkipCode = "IDENTIFIER_FIELD_PROFILE_SKIPPED"
			recommended = "EXACT_ONLY"
			observation.RiskHighCardinality = true
		} else {
			if observation.PolicySkipped ||
				observation.RowCount != claim.ExpectedRowCount {
				return ErrProfileSourceChanged
			}
			if observation.RiskHighCardinality {
				recommended = "EXACT_ONLY"
			}
		}

		var evidenceHash string
		var err error
		if policySkipCode == "" {
			evidenceHash, err = dimensionProfileEvidenceHash(
				claim, observation, false, recommended,
			)
		} else {
			evidenceHash, err = dimensionProfilePolicySkipEvidenceHash(
				claim, policySkipCode, sensitive,
				observation.RiskHighCardinality, recommended,
			)
		}
		if err != nil {
			return err
		}

		var tag pgconn.CommandTag
		if policySkipCode == "" {
			tag, err = tx.Exec(ctx, `UPDATE platform.dimension_profile_jobs
				SET status='SUCCEEDED',row_count=$1,non_null_count=$2,
					null_count=$3,distinct_count=$4,distinct_overflow=$5,
					distinct_ratio=$6,risk_high_cardinality=$7,
					recommended_member_index_policy=$8,evidence_hash=$9,
					result_code='',lease_owner='',lease_token=NULL,
					lease_expires_at=NULL,completed_at=now()
				WHERE id=$10::uuid AND status='RUNNING' AND attempt=$11
				  AND lease_owner=$12 AND lease_token=$13::uuid
				  AND lease_expires_at>now()`,
				observation.RowCount, observation.NonNullCount,
				observation.NullCount, observation.DistinctCount,
				observation.DistinctOverflow, observation.DistinctRatio,
				observation.RiskHighCardinality, recommended, evidenceHash,
				claim.ID, claim.Attempt, claim.LeaseOwner, claim.LeaseToken)
		} else {
			tag, err = tx.Exec(ctx, `UPDATE platform.dimension_profile_jobs
				SET status='SKIPPED_POLICY',row_count=NULL,
					non_null_count=NULL,null_count=NULL,distinct_count=NULL,
					distinct_overflow=false,distinct_ratio=NULL,
					risk_high_cardinality=$1,
					recommended_member_index_policy=$2,evidence_hash=$3,
					result_code=$4,lease_owner='',lease_token=NULL,
					lease_expires_at=NULL,completed_at=now()
				WHERE id=$5::uuid AND status='RUNNING' AND attempt=$6
				  AND lease_owner=$7 AND lease_token=$8::uuid
				  AND lease_expires_at>now()`,
				observation.RiskHighCardinality, recommended, evidenceHash,
				policySkipCode, claim.ID, claim.Attempt,
				claim.LeaseOwner, claim.LeaseToken)
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProfileLeaseLost
		}

		candidateRows, err := tx.Query(ctx, `UPDATE platform.dimension_survey_candidates
			SET proposed_high_cardinality=
			      proposed_high_cardinality OR $1,
			    proposed_sensitive=proposed_sensitive OR $2,
			    proposed_member_index_policy=CASE
			      WHEN $2 THEN 'NONE'
			      WHEN $1 AND proposed_member_index_policy='FULL' THEN 'EXACT_ONLY'
			      ELSE proposed_member_index_policy
			    END,
			    version=version+1,updated_by=$3,updated_at=clock_timestamp()
			WHERE materialization_id=$4::uuid AND field_id=$5
			  AND status='SUGGESTED'
			  AND (
			    ($1 AND (
			      NOT proposed_high_cardinality
			      OR proposed_member_index_policy='FULL'
			    ))
			    OR ($2 AND (
			      NOT proposed_sensitive
			      OR proposed_member_index_policy<>'NONE'
			    ))
			  )
			RETURNING id::text,version-1`,
			observation.RiskHighCardinality, sensitive, claim.RequestedBy,
			claim.MaterializationID, claim.FieldID)
		if err != nil {
			return err
		}
		type changedCandidate struct {
			id              string
			previousVersion int64
		}
		changedCandidates := []changedCandidate{}
		for candidateRows.Next() {
			var item changedCandidate
			if err := candidateRows.Scan(&item.id, &item.previousVersion); err != nil {
				candidateRows.Close()
				return err
			}
			changedCandidates = append(changedCandidates, item)
		}
		if err := candidateRows.Err(); err != nil {
			candidateRows.Close()
			return err
		}
		candidateRows.Close()

		dimensionRows, err := tx.Query(ctx, `UPDATE platform.semantic_dimensions AS dimension
			SET high_cardinality=dimension.high_cardinality OR $1,
			    sensitive=dimension.sensitive OR $2,
			    member_index_policy=CASE
			      WHEN $2 THEN 'NONE'
			      WHEN $1 AND dimension.member_index_policy='FULL' THEN 'EXACT_ONLY'
			      ELSE dimension.member_index_policy
			    END,
			    member_refresh_generation=NULL,
			    member_count=NULL,
			    member_refreshed_at=NULL,
			    last_member_refresh_job_id=NULL,
			    definition_hash=encode(public.digest(
			      convert_to(
			        concat_ws(E'\x1f',
			          dimension.dataset_id::text,
			          dimension.dataset_version_id::text,
			          dimension.field_id,
			          dimension.code::text,
			          dimension.name,
			          dimension.description,
			          dimension.dimension_type,
			          CASE
			            WHEN $2 THEN 'NONE'
			            WHEN $1 AND dimension.member_index_policy='FULL'
			              THEN 'EXACT_ONLY'
			            ELSE dimension.member_index_policy
			          END,
			          (dimension.high_cardinality OR $1)::text,
			          (dimension.sensitive OR $2)::text,
			          dimension.status
			        ),
			        'UTF8'
			      ),
			      'sha256'
			    ),'hex'),
			    version=dimension.version+1,updated_by=$3,
			    updated_at=clock_timestamp()
			WHERE dimension.dataset_id=$4::uuid
			  AND dimension.dataset_version_id=$5::uuid
			  AND dimension.field_id=$6
			  AND dimension.status='PUBLISHED'
			  AND (
			    ($1 AND (
			      NOT dimension.high_cardinality
			      OR dimension.member_index_policy='FULL'
			    ))
			    OR ($2 AND (
			      NOT dimension.sensitive
			      OR dimension.member_index_policy<>'NONE'
			    ))
			  )
			RETURNING dimension.id::text,dimension.version-1`,
			observation.RiskHighCardinality, sensitive, claim.RequestedBy,
			claim.DatasetID, claim.DatasetVersionID, claim.FieldID)
		if err != nil {
			return err
		}
		type changedDimension struct {
			id              string
			previousVersion int64
		}
		changedDimensions := []changedDimension{}
		for dimensionRows.Next() {
			var item changedDimension
			if err := dimensionRows.Scan(&item.id, &item.previousVersion); err != nil {
				dimensionRows.Close()
				return err
			}
			changedDimensions = append(changedDimensions, item)
		}
		if err := dimensionRows.Err(); err != nil {
			dimensionRows.Close()
			return err
		}
		dimensionRows.Close()

		profileDetail := map[string]any{
			"datasetVersionId":             claim.DatasetVersionID,
			"materializationId":            claim.MaterializationID,
			"fieldId":                      claim.FieldID,
			"policySkipped":                policySkipCode != "",
			"resultCode":                   policySkipCode,
			"riskHighCardinality":          observation.RiskHighCardinality,
			"recommendedMemberIndexPolicy": recommended,
			"evidenceHash":                 evidenceHash,
		}
		profileAction := "DIMENSION_PROFILE_COMPLETE"
		if policySkipCode == "" {
			profileDetail["rowCount"] = observation.RowCount
			profileDetail["nonNullCount"] = observation.NonNullCount
			profileDetail["nullCount"] = observation.NullCount
			profileDetail["distinctCount"] = observation.DistinctCount
			profileDetail["distinctOverflow"] = observation.DistinctOverflow
		} else {
			profileAction = "DIMENSION_PROFILE_POLICY_SKIPPED"
		}
		if err := auditMutation(
			ctx, tx, claim.RequestedBy,
			profileAction, "DIMENSION_PROFILE_JOB", claim.ID, profileDetail,
		); err != nil {
			return err
		}
		for _, candidate := range changedCandidates {
			if err := auditMutation(
				ctx, tx, claim.RequestedBy,
				"DIMENSION_PROFILE_CANDIDATE_TIGHTEN",
				"DIMENSION_SURVEY_CANDIDATE", candidate.id,
				map[string]any{
					"profileJobId":    claim.ID,
					"previousVersion": candidate.previousVersion,
					"policy":          recommended,
				},
			); err != nil {
				return err
			}
		}
		for _, dimension := range changedDimensions {
			if err := auditMutation(
				ctx, tx, claim.RequestedBy,
				"DIMENSION_PROFILE_POLICY_TIGHTEN",
				"SEMANTIC_DIMENSION", dimension.id,
				map[string]any{
					"profileJobId":    claim.ID,
					"previousVersion": dimension.previousVersion,
					"policy":          recommended,
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *PostgresStore) FailDimensionProfile(
	ctx context.Context,
	claim DimensionProfileJob,
	code string,
) error {
	retryable := code == "PROFILE_TIMEOUT" || code == "PROFILE_FAILED"
	stale := code == "PROFILE_SOURCE_CHANGED" || code == "PROFILE_VIEW_UNTRUSTED"
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		target := "FAILED"
		if stale {
			target = "STALE"
		} else if retryable && claim.Attempt < claim.MaxAttempts {
			target = "QUEUED"
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dimension_profile_jobs
			SET status=$1,
				next_attempt_at=CASE WHEN $1='QUEUED'
				  THEN now()+(LEAST(attempt,5)*interval '30 seconds')
				  ELSE next_attempt_at END,
				recommended_member_index_policy=CASE
				  WHEN $1 IN ('FAILED','STALE') THEN 'NONE' ELSE '' END,
				result_code=CASE WHEN $1='QUEUED' THEN '' ELSE $2 END,
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=CASE WHEN $1='QUEUED' THEN NULL ELSE now() END
			WHERE id=$3::uuid AND status='RUNNING' AND attempt=$4
			  AND lease_owner=$5 AND lease_token=$6::uuid
			  AND lease_expires_at>now()`,
			target, code, claim.ID, claim.Attempt,
			claim.LeaseOwner, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProfileLeaseLost
		}
		if target == "QUEUED" {
			return nil
		}
		return auditMutation(
			ctx, tx, claim.RequestedBy,
			"DIMENSION_PROFILE_"+target, "DIMENSION_PROFILE_JOB", claim.ID,
			map[string]any{
				"datasetVersionId":  claim.DatasetVersionID,
				"materializationId": claim.MaterializationID,
				"fieldId":           claim.FieldID,
				"resultCode":        code,
			},
		)
	})
}

func verifyDimensionProfileLease(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionProfileJob,
	lock bool,
) error {
	query := `SELECT 1
		FROM platform.dimension_profile_jobs
		WHERE id=$1::uuid AND status='RUNNING' AND attempt=$2
		  AND lease_owner=$3 AND lease_token=$4::uuid
		  AND lease_expires_at>now()`
	if lock {
		query += ` FOR UPDATE`
	}
	var one int
	err := tx.QueryRow(
		ctx, query, claim.ID, claim.Attempt, claim.LeaseOwner, claim.LeaseToken,
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProfileLeaseLost
	}
	return err
}

func lockDimensionProfileGovernanceScope(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionProfileJob,
) error {
	var locked int
	err := tx.QueryRow(ctx, `SELECT 1
		FROM platform.dataset_materializations AS materialization
		WHERE materialization.id=$1::uuid
		  AND materialization.dataset_id=$2::uuid
		  AND materialization.dataset_version_id=$3::uuid
		  AND materialization.tenant_id=platform.current_tenant_id()
		FOR SHARE OF materialization`,
		claim.MaterializationID, claim.DatasetID, claim.DatasetVersionID,
	).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProfileSourceChanged
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
	)`, claim.DatasetID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended(
		  'dimension-field-risk:'||platform.current_tenant_id()::text||
		  ':'||$1::text||':'||$2::text,
		  0
		)
	)`, claim.DatasetVersionID, claim.FieldID)
	return err
}

func setDimensionProfileLimits(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionProfileJob,
) error {
	_, err := tx.Exec(
		ctx,
		`SELECT platform.apply_dimension_profile_resource_limits($1,$2,$3,$4)`,
		claim.ID, claim.Attempt, claim.LeaseOwner, claim.LeaseToken,
	)
	return err
}

func setDimensionProfileWarehouseLimits(
	ctx context.Context,
	tx pgx.Tx,
	claim DimensionProfileJob,
) error {
	timeoutMS := fmt.Sprintf("%d", claim.TimeoutSeconds*1000)
	workMem := fmt.Sprintf("%dkB", claim.WorkMemKB)
	tempLimit := fmt.Sprintf("%dkB", claim.TempFileLimitKB)
	_, err := tx.Exec(ctx, `SELECT
		set_config('statement_timeout',$1,true),
		set_config('lock_timeout','5000',true),
		set_config('work_mem',$2,true),
		set_config('temp_file_limit',$3,true)`,
		timeoutMS, workMem, tempLimit)
	return err
}

func lockDimensionProfilePhysical(
	ctx context.Context,
	tx pgx.Tx,
	physical materialization.PhysicalIdentifier,
) error {
	qualified := quoteTrustedIdentifier(physical.Schema) + "." +
		quoteTrustedIdentifier(physical.Name)
	if _, err := tx.Exec(ctx, "LOCK TABLE "+qualified+" IN SHARE MODE"); err != nil {
		return classifyDimensionProfileDatabaseError(err)
	}
	return nil
}

func dimensionProfileEvidenceHash(
	claim DimensionProfileJob,
	observation DimensionProfileObservation,
	sensitive bool,
	recommended string,
) (string, error) {
	document, err := json.Marshal(struct {
		Version                     string  `json:"version"`
		PolicyVersion               string  `json:"policyVersion"`
		MaterializationID           string  `json:"materializationId"`
		MaterializationSnapshotHash string  `json:"materializationSnapshotHash"`
		SchemaHash                  string  `json:"schemaHash"`
		FieldID                     string  `json:"fieldId"`
		RowCount                    int64   `json:"rowCount"`
		NonNullCount                int64   `json:"nonNullCount"`
		NullCount                   int64   `json:"nullCount"`
		DistinctCount               int64   `json:"distinctCount"`
		DistinctOverflow            bool    `json:"distinctOverflow"`
		DistinctCap                 int     `json:"distinctCap"`
		DistinctRatio               float64 `json:"distinctRatio"`
		HighRatioThreshold          float64 `json:"highRatioThreshold"`
		HighRatioMinNonNull         int64   `json:"highRatioMinNonNull"`
		RiskHighCardinality         bool    `json:"riskHighCardinality"`
		Sensitive                   bool    `json:"sensitive"`
		RecommendedPolicy           string  `json:"recommendedPolicy"`
	}{
		Version:                     claim.ProfileVersion,
		PolicyVersion:               claim.PolicyVersion,
		MaterializationID:           claim.MaterializationID,
		MaterializationSnapshotHash: claim.MaterializationSnapshotHash,
		SchemaHash:                  claim.SchemaHash,
		FieldID:                     claim.FieldID,
		RowCount:                    observation.RowCount,
		NonNullCount:                observation.NonNullCount,
		NullCount:                   observation.NullCount,
		DistinctCount:               observation.DistinctCount,
		DistinctOverflow:            observation.DistinctOverflow,
		DistinctCap:                 claim.DistinctCap,
		DistinctRatio:               observation.DistinctRatio,
		HighRatioThreshold:          claim.HighRatioThreshold,
		HighRatioMinNonNull:         claim.HighRatioMinNonNull,
		RiskHighCardinality:         observation.RiskHighCardinality,
		Sensitive:                   sensitive,
		RecommendedPolicy:           recommended,
	})
	if err != nil {
		return "", err
	}
	return hashBytes(document), nil
}

func dimensionProfilePolicySkipEvidenceHash(
	claim DimensionProfileJob,
	resultCode string,
	sensitive, highCardinality bool,
	recommended string,
) (string, error) {
	document, err := json.Marshal(struct {
		Version                     string `json:"version"`
		PolicyVersion               string `json:"policyVersion"`
		MaterializationID           string `json:"materializationId"`
		MaterializationSnapshotHash string `json:"materializationSnapshotHash"`
		SchemaHash                  string `json:"schemaHash"`
		FieldID                     string `json:"fieldId"`
		ResultCode                  string `json:"resultCode"`
		Sensitive                   bool   `json:"sensitive"`
		RiskHighCardinality         bool   `json:"riskHighCardinality"`
		RecommendedPolicy           string `json:"recommendedPolicy"`
	}{
		Version:                     claim.ProfileVersion,
		PolicyVersion:               claim.PolicyVersion,
		MaterializationID:           claim.MaterializationID,
		MaterializationSnapshotHash: claim.MaterializationSnapshotHash,
		SchemaHash:                  claim.SchemaHash,
		FieldID:                     claim.FieldID,
		ResultCode:                  resultCode,
		Sensitive:                   sensitive,
		RiskHighCardinality:         highCardinality,
		RecommendedPolicy:           recommended,
	})
	if err != nil {
		return "", err
	}
	return hashBytes(document), nil
}

func classifyDimensionProfileDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrProfileTimeout
	}
	if errors.Is(err, ErrProfileLeaseLost) ||
		errors.Is(err, ErrProfileSourceChanged) ||
		errors.Is(err, ErrProfileUnsafeView) ||
		errors.Is(err, ErrProfileResourceLimit) {
		return err
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "57014":
			return ErrProfileTimeout
		case "53100", "53200", "53300", "53400":
			return ErrProfileResourceLimit
		case "42P01", "42703":
			return ErrProfileSourceChanged
		}
	}
	return fmt.Errorf("dimension profile database operation failed")
}

func scanDimensionProfileJob(
	row scanRow,
	item *DimensionProfileJob,
) error {
	return row.Scan(
		&item.ID, &item.Status, &item.ProfileVersion, &item.PolicyVersion,
		&item.MaterializationID, &item.MaterializationSnapshotHash,
		&item.ExpectedRowCount, &item.RowCount, &item.NonNullCount,
		&item.NullCount, &item.DistinctCount, &item.DistinctOverflow,
		&item.DistinctCap, &item.DistinctRatio,
		&item.RiskHighCardinality, &item.RecommendedMemberIndexPolicy,
		&item.ResultCode, &item.EvidenceHash, &item.Attempt, &item.MaxAttempts,
		&item.CreatedAt, &item.UpdatedAt, &item.StartedAt, &item.CompletedAt,
		&item.TenantID, &item.DatasetID, &item.DatasetVersionID,
		&item.SchemaHash, &item.FieldID, &item.FieldCode, &item.FieldRole,
		&item.CanonicalType, &item.SemanticType,
		&item.HighRatioThreshold, &item.HighRatioMinNonNull,
		&item.TimeoutSeconds, &item.WorkMemKB, &item.TempFileLimitKB,
		&item.RequestedBy, &item.NextAttemptAt,
		&item.LeaseOwner, &item.LeaseToken, &item.LeaseExpiresAt,
	)
}

var _ DimensionProfileStore = (*PostgresStore)(nil)
