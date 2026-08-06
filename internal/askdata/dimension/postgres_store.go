package dimension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

var (
	profileErrorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
	warehouseNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
)

type PostgresProfileStore struct{ pool *pgxpool.Pool }

func NewPostgresProfileStore(pool *pgxpool.Pool) *PostgresProfileStore {
	return &PostgresProfileStore{pool: pool}
}

func (store *PostgresProfileStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidProfileWork
	}
	rows, err := store.pool.Query(ctx, `SELECT id::text FROM platform.tenants
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

func (store *PostgresProfileStore) Claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
	options WorkerOptions,
) (claim *ScanClaim, err error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		lease < time.Second || lease > 10*time.Minute || options.Validate() != nil {
		return nil, ErrInvalidProfileWork
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
			hashtextextended('askdata-dimension-profile-sync:'||$1::text,0)
		)`, tenantID); err != nil {
			return err
		}
		if err := synchronizeProfileJobs(ctx, tx, tenantID, options); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.dimension_profile_jobs SET
			status='STALE',error_code='CONFIG_SUPERSEDED',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,completed_at=now(),updated_at=now()
			WHERE status IN ('PENDING','RUNNING') AND (
				max_rows<>$1 OR max_distinct_values<>$2 OR max_sample_bytes<>$3
				OR statement_timeout_ms<>$4 OR policy_version<>$5
			)`, options.Budget.MaxRows, options.Budget.MaxDistinctValues,
			options.Budget.MaxSampleBytes, options.Budget.StatementTimeoutMS,
			options.Policy.Version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.dimension_profile_jobs AS job SET
			status='STALE',error_code='SOURCE_SUPERSEDED',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,completed_at=now(),updated_at=now()
			WHERE job.status IN ('PENDING','RUNNING') AND NOT EXISTS(
				SELECT 1
				FROM askdata.dimensions AS dimension
				JOIN askdata.semantic_models AS model
				  ON model.id=dimension.semantic_model_version_id
				 AND model.domain_id=dimension.domain_id AND model.tenant_id=dimension.tenant_id
				JOIN platform.datasets AS dataset
				  ON dataset.id=model.dataset_id AND dataset.tenant_id=model.tenant_id
				 AND dataset.current_published_version_id=model.dataset_version_id
				JOIN platform.dataset_versions AS version
				  ON version.id=model.dataset_version_id AND version.dataset_id=dataset.id
				 AND version.tenant_id=dataset.tenant_id
				JOIN platform.dataset_materializations AS materialization
				  ON materialization.id=model.materialization_id
				 AND materialization.dataset_id=dataset.id
				 AND materialization.dataset_version_id=version.id
				 AND materialization.tenant_id=dataset.tenant_id
				JOIN platform.dataset_fields AS field
				  ON field.tenant_id=version.tenant_id AND field.dataset_version_id=version.id
				 AND field.field_id=dimension.logical_field_id
				WHERE dimension.id=job.dimension_version_id
				  AND dimension.domain_id=job.domain_id AND dimension.tenant_id=job.tenant_id
				  AND dimension.status IN ('DRAFT','CERTIFIED')
				  AND dimension.sensitivity=job.sensitivity
				  AND dimension.member_index_policy=job.member_index_policy
				  AND model.id=job.semantic_model_version_id
				  AND model.status IN ('DRAFT','CERTIFIED')
				  AND model.dataset_id=job.dataset_id AND model.dataset_version_id=job.dataset_version_id
				  AND model.materialization_id=job.materialization_id
				  AND model.dataset_schema_hash=job.dataset_schema_hash
				  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
				  AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
				  AND materialization.status='ACTIVE' AND materialization.layer=version.layer
				  AND materialization.snapshot_hash=job.source_snapshot_hash
				  AND materialization.schema_hash=job.dataset_schema_hash
				  AND materialization.published_schema=job.published_schema
				  AND materialization.published_name=job.published_name
				  AND field.field_code::text=job.field_code
			)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE askdata.dimension_profile_jobs SET
			status='FAILED',error_code='LEASE_EXPIRED',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now() AND attempt>=max_attempts`); err != nil {
			return err
		}

		row := tx.QueryRow(ctx, `WITH picked AS (
			SELECT id FROM askdata.dimension_profile_jobs
			WHERE attempt<max_attempts AND (
				(status='PENDING' AND next_attempt_at<=now())
				OR (status='RUNNING' AND lease_expires_at<=now())
			)
			ORDER BY next_attempt_at,created_at,id
			FOR UPDATE SKIP LOCKED LIMIT 1
		), claimed AS (
			UPDATE askdata.dimension_profile_jobs AS job SET
				status='RUNNING',attempt=job.attempt+1,error_code='',lease_owner=$1,
				lease_token=gen_random_uuid(),lease_expires_at=now()+($2*interval '1 second'),
				started_at=COALESCE(job.started_at,now()),completed_at=NULL,updated_at=now()
			FROM picked WHERE job.id=picked.id
			RETURNING job.*
		) SELECT id::text,tenant_id::text,domain_id::text,dimension_version_id::text,
			semantic_model_version_id::text,dataset_id::text,dataset_version_id::text,
			materialization_id::text,source_snapshot_hash,dataset_schema_hash,
			published_schema,published_name,field_code,input_hash,lease_token::text,
			generation,expected_row_count,sensitivity,member_index_policy,
			high_cardinality_hint,max_rows,max_distinct_values,max_sample_bytes,
			statement_timeout_ms,attempt,max_attempts
		FROM claimed`, workerID, int64(lease/time.Second))
		var claimed ScanClaim
		var sensitivity, memberIndexPolicy string
		if err := row.Scan(
			&claimed.ID, &claimed.TenantID, &claimed.DomainID, &claimed.DimensionVersionID,
			&claimed.SemanticModelVersionID, &claimed.DatasetID, &claimed.DatasetVersionID,
			&claimed.MaterializationID, &claimed.SourceSnapshotHash, &claimed.DatasetSchemaHash,
			&claimed.PublishedSchema, &claimed.PublishedName, &claimed.FieldCode,
			&claimed.InputHash, &claimed.LeaseToken, &claimed.Generation,
			&claimed.ExpectedRowCount, &sensitivity, &memberIndexPolicy,
			&claimed.HighCardinalityHint, &claimed.Budget.MaxRows,
			&claimed.Budget.MaxDistinctValues, &claimed.Budget.MaxSampleBytes,
			&claimed.Budget.StatementTimeoutMS, &claimed.Attempt, &claimed.MaxAttempts,
		); errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		claimed.Sensitivity = registry.Sensitivity(sensitivity)
		claimed.MemberIndexPolicy = registry.MemberIndexPolicy(memberIndexPolicy)
		if err := loadPreviousProfileKeys(ctx, tx, &claimed); err != nil {
			return err
		}
		claim = &claimed
		return nil
	})
	return claim, err
}

func synchronizeProfileJobs(ctx context.Context, tx pgx.Tx, tenantID string, options WorkerOptions) error {
	_, err := tx.Exec(ctx, `WITH candidates AS (
		SELECT dimension.tenant_id,dimension.domain_id,dimension.id AS dimension_version_id,
			dimension.semantic_model_version_id,model.dataset_id,model.dataset_version_id,
			model.materialization_id,materialization.snapshot_hash,
			model.dataset_schema_hash,materialization.published_schema,
			materialization.published_name,field.field_code::text AS field_code,
			materialization.row_count AS expected_row_count,dimension.sensitivity,
			dimension.member_index_policy,dimension.high_cardinality,
			encode(public.digest(convert_to(concat_ws(E'\\x1f',
				'dimension-profile-v2',dimension.content_hash,model.content_hash,
				materialization.snapshot_hash,model.dataset_schema_hash,field.field_code::text,
				dimension.sensitivity,dimension.member_index_policy,dimension.high_cardinality::text,
				($2::bigint)::text,($3::bigint)::text,($4::bigint)::text,
				($5::integer)::text,$6::text
			),'UTF8'),'sha256'),'hex') AS input_hash
		FROM askdata.dimensions AS dimension
		JOIN askdata.semantic_models AS model
		  ON model.id=dimension.semantic_model_version_id
		 AND model.domain_id=dimension.domain_id AND model.tenant_id=dimension.tenant_id
		JOIN platform.datasets AS dataset
		  ON dataset.id=model.dataset_id AND dataset.tenant_id=model.tenant_id
		 AND dataset.current_published_version_id=model.dataset_version_id
		JOIN platform.dataset_versions AS version
		  ON version.id=model.dataset_version_id AND version.dataset_id=dataset.id
		 AND version.tenant_id=dataset.tenant_id
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.id=model.materialization_id
		 AND materialization.dataset_id=dataset.id
		 AND materialization.dataset_version_id=version.id
		 AND materialization.tenant_id=dataset.tenant_id
		JOIN platform.dataset_fields AS field
		  ON field.tenant_id=version.tenant_id AND field.dataset_version_id=version.id
		 AND field.field_id=dimension.logical_field_id
		WHERE dimension.tenant_id=$1 AND dimension.status IN ('DRAFT','CERTIFIED')
		  AND model.status IN ('DRAFT','CERTIFIED')
		  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
		  AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
		  AND model.layer=version.layer AND model.dataset_schema_hash=version.schema_hash
		  AND materialization.status='ACTIVE' AND materialization.layer=version.layer
		  AND materialization.schema_hash=version.schema_hash
		  AND materialization.published_schema='warehouse_published'
		  AND materialization.row_count IS NOT NULL
	), missing AS (
		SELECT candidate.*,
			COALESCE((SELECT max(job.generation)+1
				FROM askdata.dimension_profile_jobs AS job
				WHERE job.tenant_id=candidate.tenant_id
				  AND job.dimension_version_id=candidate.dimension_version_id),1) AS generation
		FROM candidates AS candidate
		WHERE NOT EXISTS(
			SELECT 1 FROM askdata.dimension_profile_jobs AS job
			WHERE job.tenant_id=candidate.tenant_id
			  AND job.dimension_version_id=candidate.dimension_version_id
			  AND job.input_hash=candidate.input_hash
		)
	) INSERT INTO askdata.dimension_profile_jobs(
		tenant_id,domain_id,dimension_version_id,generation,semantic_model_version_id,
		dataset_id,dataset_version_id,materialization_id,source_snapshot_hash,
		dataset_schema_hash,published_schema,published_name,field_code,
		expected_row_count,sensitivity,member_index_policy,high_cardinality_hint,
		max_rows,max_distinct_values,max_sample_bytes,statement_timeout_ms,
		policy_version,input_hash,status,max_attempts,error_code,completed_at
	) SELECT tenant_id,domain_id,dimension_version_id,generation,semantic_model_version_id,
		dataset_id,dataset_version_id,materialization_id,snapshot_hash,
		dataset_schema_hash,published_schema,published_name,field_code,
		expected_row_count,sensitivity,member_index_policy,high_cardinality,
		$2::bigint,$3::bigint,$4::bigint,$5::integer,$6::text,input_hash,
		CASE WHEN sensitivity='RESTRICTED' OR member_index_policy='NONE'
			THEN 'SKIPPED' ELSE 'PENDING' END,
		$7::integer,
		CASE WHEN sensitivity='RESTRICTED' THEN 'RESTRICTED_DIMENSION'
			WHEN member_index_policy='NONE' THEN 'MEMBER_SCAN_DISABLED' ELSE '' END,
		CASE WHEN sensitivity='RESTRICTED' OR member_index_policy='NONE'
			THEN now() ELSE NULL END
	FROM missing
	ON CONFLICT(tenant_id,dimension_version_id,input_hash) DO NOTHING`,
		tenantID, options.Budget.MaxRows, options.Budget.MaxDistinctValues,
		options.Budget.MaxSampleBytes, options.Budget.StatementTimeoutMS,
		options.Policy.Version, options.MaxAttempts)
	return err
}

func loadPreviousProfileKeys(ctx context.Context, tx pgx.Tx, claim *ScanClaim) error {
	var profileID string
	var distinctCount int64
	var truncated, timedOut bool
	err := tx.QueryRow(ctx, `SELECT id::text,
		(profile_json->>'distinctCount')::bigint,
		COALESCE((profile_json#>>'{usage,truncated}')::boolean,false),
		COALESCE((profile_json#>>'{usage,timedOut}')::boolean,false)
	FROM askdata.dimension_profiles
	WHERE dimension_version_id=$1 AND generation<$2
	ORDER BY generation DESC,id DESC LIMIT 1`, claim.DimensionVersionID, claim.Generation).
		Scan(&profileID, &distinctCount, &truncated, &timedOut)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT member_key_hash
		FROM askdata.dimension_profile_members WHERE profile_id=$1
		UNION
		SELECT reserved->>'normalizedValueHash'
		FROM askdata.dimension_profiles AS profile,
		LATERAL jsonb_array_elements(profile.profile_json->'reservedValues') AS reserved
		WHERE profile.id=$1
		ORDER BY 1`, profileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	keys := []askdata.ContentHash{}
	for rows.Next() {
		var key askdata.ContentHash
		if err := rows.Scan(&key); err != nil {
			return err
		}
		if err := key.Validate(); err != nil {
			return err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	claim.PreviousDistinctCount = &distinctCount
	claim.PreviousMemberKeyHashes = keys
	claim.PreviousComplete = !truncated && !timedOut && int64(len(keys)) == distinctCount
	return nil
}

func (store *PostgresProfileStore) Complete(
	ctx context.Context,
	claim ScanClaim,
	workerID string,
	profile Profile,
	decision PolicyDecision,
	observations []MemberObservation,
) error {
	if store == nil || store.pool == nil || validateScanClaim(claim, workerID) != nil ||
		profileResultMatchesClaim(claim, profile, decision) != nil ||
		validateMemberObservations(claim, profile, observations) != nil {
		return ErrInvalidProfileWork
	}
	profileJSON, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	decisionJSON, err := json.Marshal(decision)
	if err != nil {
		return err
	}
	stale := false
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var current bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM askdata.dimension_profile_jobs AS job
			JOIN askdata.dimensions AS dimension
			  ON dimension.id=job.dimension_version_id AND dimension.domain_id=job.domain_id
			 AND dimension.tenant_id=job.tenant_id
			JOIN askdata.semantic_models AS model
			  ON model.id=job.semantic_model_version_id AND model.domain_id=job.domain_id
			 AND model.tenant_id=job.tenant_id
			JOIN platform.datasets AS dataset
			  ON dataset.id=job.dataset_id AND dataset.tenant_id=job.tenant_id
			 AND dataset.current_published_version_id=job.dataset_version_id
			JOIN platform.dataset_versions AS version
			  ON version.id=job.dataset_version_id AND version.dataset_id=job.dataset_id
			 AND version.tenant_id=job.tenant_id
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.id=job.materialization_id
			 AND materialization.dataset_id=job.dataset_id
			 AND materialization.dataset_version_id=job.dataset_version_id
			 AND materialization.tenant_id=job.tenant_id
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=job.tenant_id AND field.dataset_version_id=job.dataset_version_id
			 AND field.field_id=dimension.logical_field_id
			WHERE job.id=$1 AND job.input_hash=$2 AND job.status='RUNNING'
			  AND job.lease_owner=$3 AND job.lease_token=$4
			  AND dimension.status IN ('DRAFT','CERTIFIED')
			  AND dimension.sensitivity=job.sensitivity
			  AND dimension.member_index_policy=job.member_index_policy
			  AND model.status IN ('DRAFT','CERTIFIED')
			  AND model.dataset_id=job.dataset_id AND model.dataset_version_id=job.dataset_version_id
			  AND model.materialization_id=job.materialization_id
			  AND model.dataset_schema_hash=job.dataset_schema_hash
			  AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
			  AND version.status='PUBLISHED' AND version.layer IN ('DWS','ADS')
			  AND version.schema_hash=job.dataset_schema_hash
			  AND materialization.status='ACTIVE' AND materialization.layer=version.layer
			  AND materialization.snapshot_hash=job.source_snapshot_hash
			  AND materialization.schema_hash=job.dataset_schema_hash
			  AND materialization.published_schema=job.published_schema
			  AND materialization.published_name=job.published_name
			  AND field.field_code::text=job.field_code
		)`, claim.ID, claim.InputHash, workerID, claim.LeaseToken).Scan(&current); err != nil {
			return err
		}
		if !current {
			if _, err := tx.Exec(ctx, `UPDATE askdata.dimension_profile_jobs SET
				status='STALE',error_code='SOURCE_SUPERSEDED',lease_owner='',lease_token=NULL,
				lease_expires_at=NULL,completed_at=now(),updated_at=now()
				WHERE id=$1 AND input_hash=$2 AND status='RUNNING'
				  AND lease_owner=$3 AND lease_token=$4`,
				claim.ID, claim.InputHash, workerID, claim.LeaseToken); err != nil {
				return err
			}
			stale = true
			return nil
		}
		var profileID string
		if err := tx.QueryRow(ctx, `INSERT INTO askdata.dimension_profiles(
			tenant_id,domain_id,dimension_version_id,job_id,generation,source_snapshot_hash,
			profile_json,profile_hash,policy_decision_json,policy_decision_hash
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id::text`, claim.TenantID, claim.DomainID, claim.DimensionVersionID,
			claim.ID, claim.Generation, claim.SourceSnapshotHash, profileJSON,
			profile.ProfileHash, decisionJSON, decision.DecisionHash).Scan(&profileID); err != nil {
			return err
		}
		for _, observation := range observations {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.dimension_profile_members(
				tenant_id,domain_id,dimension_version_id,profile_id,generation,
				member_key_hash,canonical_label,normalized_value,observed_aliases,
				observed_count,sensitivity,eligible_for_llm,content_hash
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
				claim.TenantID, claim.DomainID, claim.DimensionVersionID, profileID,
				claim.Generation, observation.MemberKeyHash, observation.CanonicalLabel,
				observation.NormalizedValue, observation.ObservedAliases,
				observation.ObservedCount, observation.Sensitivity,
				observation.EligibleForLLM, observation.ContentHash); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.dimension_profile_jobs SET
			status='SUCCEEDED',error_code='',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,completed_at=now(),updated_at=now()
			WHERE id=$1 AND input_hash=$2 AND status='RUNNING'
			  AND lease_owner=$3 AND lease_token=$4`,
			claim.ID, claim.InputHash, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("dimension profile lease was lost before completion")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if stale {
		return ErrProfileSourceStale
	}
	return nil
}

func (store *PostgresProfileStore) Fail(
	ctx context.Context, claim ScanClaim, workerID, code string,
) error {
	if store == nil || store.pool == nil || validateScanClaim(claim, workerID) != nil ||
		!profileErrorCodePattern.MatchString(code) {
		return ErrInvalidProfileWork
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var attempt, maximum int
		if err := tx.QueryRow(ctx, `SELECT attempt,max_attempts
			FROM askdata.dimension_profile_jobs
			WHERE id=$1 AND input_hash=$2 AND status='RUNNING'
			  AND lease_owner=$3 AND lease_token=$4 FOR UPDATE`,
			claim.ID, claim.InputHash, workerID, claim.LeaseToken).Scan(&attempt, &maximum); err != nil {
			return err
		}
		status := "PENDING"
		completed := false
		if attempt >= maximum {
			status = "FAILED"
			completed = true
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.dimension_profile_jobs SET
			status=$1,error_code=CASE WHEN $1='FAILED' THEN $2 ELSE '' END,
			next_attempt_at=CASE WHEN $1='PENDING'
			  THEN now()+(LEAST(300,power(2,attempt)::integer)*interval '1 second')
			  ELSE next_attempt_at END,
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			completed_at=CASE WHEN $5 THEN now() ELSE NULL END,updated_at=now()
			WHERE id=$3 AND input_hash=$4 AND status='RUNNING'
			  AND lease_owner=$6 AND lease_token=$7`,
			status, code, claim.ID, claim.InputHash, completed, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("dimension profile lease was lost before failure recording")
		}
		return nil
	})
}

type PostgresWarehouseScanner struct{ pool *pgxpool.Pool }

func NewPostgresWarehouseScanner(pool *pgxpool.Pool) *PostgresWarehouseScanner {
	return &PostgresWarehouseScanner{pool: pool}
}

func (scanner *PostgresWarehouseScanner) Scan(ctx context.Context, claim ScanClaim) (ScanResult, error) {
	if scanner == nil || scanner.pool == nil || claim.PublishedSchema != "warehouse_published" ||
		!warehouseNamePattern.MatchString(claim.PublishedName) ||
		strings.TrimSpace(claim.FieldCode) == "" || len(claim.FieldCode) > 128 ||
		claim.ExpectedRowCount < 0 || claim.Budget.Validate() != nil {
		return ScanResult{}, ErrInvalidProfileWork
	}
	tx, err := scanner.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return ScanResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('statement_timeout',$1,true),
		set_config('lock_timeout',$2,true)`,
		fmt.Sprintf("%dms", claim.Budget.StatementTimeoutMS),
		fmt.Sprintf("%dms", min(claim.Budget.StatementTimeoutMS, 5_000))); err != nil {
		return ScanResult{}, classifyWarehouseError(err)
	}
	qualifiedRelation := pgx.Identifier{claim.PublishedSchema, claim.PublishedName}.Sanitize()
	column := pgx.Identifier{claim.FieldCode}.Sanitize()
	query := fmt.Sprintf(`WITH bounded AS MATERIALIZED (
		SELECT %s::text AS value FROM %s LIMIT $1
	), totals AS (
		SELECT count(*)::bigint AS row_count,
		       count(*) FILTER(WHERE value IS NULL)::bigint AS null_count
		FROM bounded
	), member_values AS (
		SELECT value,count(*)::bigint AS observed_count
		FROM bounded WHERE value IS NOT NULL GROUP BY value
	), selected AS (
		SELECT value,observed_count FROM member_values
		ORDER BY value COLLATE "C" LIMIT $2
	)
	SELECT totals.row_count,totals.null_count,
	       (SELECT count(*)::bigint FROM member_values) AS raw_distinct,
	       selected.value,selected.observed_count
	FROM totals LEFT JOIN selected ON true
	ORDER BY selected.value COLLATE "C"`, column, qualifiedRelation)
	rows, err := tx.Query(ctx, query, claim.Budget.MaxRows, claim.Budget.MaxDistinctValues)
	if err != nil {
		return ScanResult{}, classifyWarehouseError(err)
	}
	result := ScanResult{Members: []RawMember{}}
	first := true
	for rows.Next() {
		var rowCount, nullCount, rawDistinct int64
		var value *string
		var observedCount *int64
		if err := rows.Scan(&rowCount, &nullCount, &rawDistinct, &value, &observedCount); err != nil {
			rows.Close()
			return ScanResult{}, classifyWarehouseError(err)
		}
		if first {
			result.RowCount, result.NullCount, result.RawDistinct = rowCount, nullCount, rawDistinct
			first = false
		}
		if value == nil || observedCount == nil {
			continue
		}
		valueBytes := int64(len(*value))
		if result.SampleBytes > claim.Budget.MaxSampleBytes-valueBytes {
			result.Truncated = true
			continue
		}
		result.SampleBytes += valueBytes
		result.Members = append(result.Members, RawMember{Value: *value, Count: *observedCount})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ScanResult{}, classifyWarehouseError(err)
	}
	rows.Close()
	expectedScanned := claim.ExpectedRowCount
	if expectedScanned > claim.Budget.MaxRows {
		expectedScanned = claim.Budget.MaxRows
		result.Truncated = true
	}
	if result.RowCount != expectedScanned {
		return ScanResult{}, ErrWarehouseDrift
	}
	if result.RawDistinct > claim.Budget.MaxDistinctValues ||
		int64(len(result.Members)) < min(result.RawDistinct, claim.Budget.MaxDistinctValues) {
		result.Truncated = true
	}
	if err := tx.Commit(ctx); err != nil {
		return ScanResult{}, classifyWarehouseError(err)
	}
	return result, nil
}

func classifyWarehouseError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "57014" {
		return fmt.Errorf("%w: %v", ErrWarehouseTimeout, err)
	}
	return err
}

var (
	_ ProfileStore     = (*PostgresProfileStore)(nil)
	_ WarehouseScanner = (*PostgresWarehouseScanner)(nil)
)
