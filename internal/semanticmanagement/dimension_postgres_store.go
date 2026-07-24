package semanticmanagement

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const dimensionColumns = `dimension.id::text,dimension.dataset_id::text,
	dimension.dataset_version_id::text,dimension.field_id,field.field_code::text,
	dimension.code::text,dimension.name,dimension.description,dimension.dimension_type,
	dimension.member_index_policy,dimension.high_cardinality,dimension.sensitive,
	dimension.status,dimension.definition_hash,dimension.version,
	COALESCE(dimension.member_refresh_generation::text,''),dimension.member_count,
	dimension.member_refreshed_at,COALESCE(dimension.last_member_refresh_job_id::text,''),
	dimension.created_by::text,dimension.updated_by::text,
	dimension.created_at,dimension.updated_at`

func (s *PostgresStore) ListDimensions(ctx context.Context, tenantID string, filter DimensionFilter) (items []Dimension, total int, err error) {
	items = []Dimension{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+dimensionColumns+`,count(*) OVER()::int
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=dimension.tenant_id
			  AND field.dataset_version_id=dimension.dataset_version_id
			  AND field.field_id=dimension.field_id
			WHERE dimension.tenant_id=platform.current_tenant_id()
			  AND ($1='' OR dimension.code::text ILIKE '%'||$1||'%'
			    OR dimension.name ILIKE '%'||$1||'%')
			  AND ($2='' OR dimension.dataset_version_id::text=$2)
			  AND ($3='' OR dimension.dimension_type=$3)
			  AND ($4='' OR dimension.status=$4)
			ORDER BY dimension.updated_at DESC,dimension.id
			LIMIT $5 OFFSET $6`,
			filter.Query, filter.DatasetVersionID, filter.DimensionType, filter.Status,
			filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item Dimension
			if err := scanDimension(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) GetDimension(ctx context.Context, tenantID, id string) (item Dimension, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := scanDimension(tx.QueryRow(ctx, `SELECT `+dimensionColumns+`
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=dimension.tenant_id
			  AND field.dataset_version_id=dimension.dataset_version_id
			  AND field.field_id=dimension.field_id
			WHERE dimension.id=$1::uuid`, id), &item)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return item, err
}

func (s *PostgresStore) CreateDimension(ctx context.Context, tenantID, actorID string, prepared PreparedDimension) (item Dimension, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if _, err := requirePublishedDWSField(
			ctx, tx, prepared.DatasetID, prepared.DatasetVersionID, prepared.FieldID,
		); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.semantic_dimensions(
				tenant_id,dataset_id,dataset_version_id,field_id,code,name,description,
				dimension_type,member_index_policy,high_cardinality,sensitive,status,
				definition_hash,created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13
			) RETURNING id::text`, prepared.DatasetID, prepared.DatasetVersionID,
			prepared.FieldID, prepared.Code, prepared.Name, prepared.Description,
			prepared.DimensionType, prepared.MemberIndexPolicy, prepared.HighCardinality,
			prepared.Sensitive, prepared.Status, prepared.DefinitionHash, actorID)
		if err := row.Scan(&item.ID); err != nil {
			return mapWriteError(err)
		}
		if err := auditMutation(ctx, tx, actorID, "SEMANTIC_DIMENSION_CREATE", "SEMANTIC_DIMENSION", item.ID,
			map[string]any{"datasetVersionId": prepared.DatasetVersionID, "fieldId": prepared.FieldID}); err != nil {
			return err
		}
		return scanDimension(tx.QueryRow(ctx, `SELECT `+dimensionColumns+`
			FROM platform.semantic_dimensions AS dimension
			JOIN platform.dataset_fields AS field
			  ON field.tenant_id=dimension.tenant_id
			  AND field.dataset_version_id=dimension.dataset_version_id
			  AND field.field_id=dimension.field_id
			WHERE dimension.id=$1::uuid`, item.ID), &item)
	})
	return item, err
}

func (s *PostgresStore) UpdateDimension(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	prepared PreparedDimension,
) (item Dimension, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var currentVersion int64
		var currentStatus, currentPolicy, datasetID, datasetVersionID, fieldID string
		err := tx.QueryRow(ctx, `SELECT version,status,member_index_policy,dataset_id::text,
				dataset_version_id::text,field_id
			FROM platform.semantic_dimensions WHERE id=$1::uuid`, id).
			Scan(&currentVersion, &currentStatus, &currentPolicy, &datasetID, &datasetVersionID, &fieldID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := lockDimensionGovernanceScope(
			ctx, tx, datasetID, datasetVersionID, fieldID,
		); err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT version,status,member_index_policy,
				dataset_id::text,dataset_version_id::text,field_id
			FROM platform.semantic_dimensions
			WHERE id=$1::uuid
			FOR UPDATE`, id).Scan(
			&currentVersion, &currentStatus, &currentPolicy,
			&datasetID, &datasetVersionID, &fieldID,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentVersion != expectedVersion || currentStatus == "DEPRECATED" {
			return ErrConflict
		}
		if datasetID != prepared.DatasetID || datasetVersionID != prepared.DatasetVersionID ||
			fieldID != prepared.FieldID {
			return ErrConflict
		}
		if _, err := requirePublishedDWSField(ctx, tx, datasetID, datasetVersionID, fieldID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_dimensions SET
				code=$1,name=$2,description=$3,dimension_type=$4,member_index_policy=$5,
				high_cardinality=$6,sensitive=$7,status=$8,definition_hash=$9,
				member_refresh_generation=NULL,member_count=NULL,
				member_refreshed_at=NULL,last_member_refresh_job_id=NULL,
				version=version+1,updated_by=$10
			WHERE id=$11::uuid
			RETURNING `+bareDimensionColumns,
			prepared.Code, prepared.Name, prepared.Description, prepared.DimensionType,
			prepared.MemberIndexPolicy, prepared.HighCardinality, prepared.Sensitive,
			prepared.Status, prepared.DefinitionHash, actorID, id)
		if err := scanBareDimension(row, &item); err != nil {
			return mapWriteError(err)
		}
		restrictedExistingFullIndex := currentPolicy == "FULL" &&
			prepared.MemberIndexPolicy == "EXACT_ONLY"
		disabledMemberIndex := currentPolicy != "NONE" &&
			prepared.MemberIndexPolicy == "NONE"
		if restrictedExistingFullIndex || disabledMemberIndex {
			if _, err := tx.Exec(ctx, `UPDATE platform.dimension_members SET
				status='DEPRECATED',updated_at=now()
				WHERE dimension_id=$1::uuid AND status='ACTIVE'`, id); err != nil {
				return err
			}
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_DIMENSION_UPDATE", "SEMANTIC_DIMENSION", id,
			map[string]any{
				"previousVersion": expectedVersion, "version": item.Version,
				"previousMemberIndexPolicy": currentPolicy,
				"memberIndexPolicy":         prepared.MemberIndexPolicy,
			})
	})
	return item, err
}

func lockDimensionGovernanceScope(
	ctx context.Context,
	tx pgx.Tx,
	datasetID, datasetVersionID, fieldID string,
) error {
	if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT 1
		FROM platform.dataset_materializations AS materialization
		WHERE materialization.tenant_id=platform.current_tenant_id()
		  AND materialization.dataset_id=$1::uuid
		  AND materialization.dataset_version_id=$2::uuid
		  AND materialization.layer='DWS'
		  AND materialization.status='ACTIVE'
		FOR SHARE OF materialization`, datasetID, datasetVersionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended(
		  'dimension-dataset-profile:'||platform.current_tenant_id()::text||
		  ':'||$1::text,
		  0
		)
	)`, datasetID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended(
		  'dimension-field-risk:'||platform.current_tenant_id()::text||
		  ':'||$1::text||':'||$2::text,
		  0
		)
	)`, datasetVersionID, fieldID)
	return err
}

func lockSemanticGovernanceWriteGate(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
		hashtextextended(
		  'semantic-governance-write:'||platform.current_tenant_id()::text,
		  0
		)
	)`)
	return err
}

func (s *PostgresStore) DeprecateDimension(ctx context.Context, tenantID, actorID, id string, expectedVersion int64) (item Dimension, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_dimensions SET
				status='DEPRECATED',version=version+1,updated_by=$1
			WHERE id=$2::uuid AND version=$3 AND status<>'DEPRECATED'
			RETURNING `+bareDimensionColumns, actorID, id, expectedVersion)
		if err := scanBareDimension(row, &item); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return mapWriteError(err)
			}
			return classifyMissingOrConflict(ctx, tx, "platform.semantic_dimensions", id)
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_DIMENSION_DEPRECATE", "SEMANTIC_DIMENSION", id,
			map[string]any{"previousVersion": expectedVersion, "version": item.Version})
	})
	return item, err
}

// bareDimensionColumns avoids a join in UPDATE RETURNING. FieldCode is loaded
// by a scalar subquery tied to the exact immutable dataset version.
const bareDimensionColumns = `id::text,dataset_id::text,dataset_version_id::text,field_id,
	(SELECT field_code::text FROM platform.dataset_fields AS field
	  WHERE field.tenant_id=platform.current_tenant_id()
	    AND field.dataset_version_id=semantic_dimensions.dataset_version_id
	    AND field.field_id=semantic_dimensions.field_id),
	code::text,name,description,dimension_type,member_index_policy,high_cardinality,
	sensitive,status,definition_hash,version,COALESCE(member_refresh_generation::text,''),
	member_count,member_refreshed_at,COALESCE(last_member_refresh_job_id::text,''),
	created_by::text,updated_by::text,created_at,updated_at`

func requirePublishedDWSField(ctx context.Context, tx pgx.Tx, datasetID, datasetVersionID, fieldID string) (string, error) {
	var fieldCode string
	err := tx.QueryRow(ctx, `SELECT field.field_code::text
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id AND dataset.id=version.dataset_id
		JOIN platform.dataset_fields AS field
		  ON field.tenant_id=version.tenant_id
		  AND field.dataset_version_id=version.id
		WHERE version.id=$1::uuid AND version.dataset_id=$2::uuid
		  AND version.layer='DWS' AND version.status='PUBLISHED'
		  AND dataset.layer='DWS' AND dataset.deleted_at IS NULL
		  AND field.field_id=$3
		  AND field.field_role IN ('DIMENSION','ATTRIBUTE','TIME','IDENTIFIER')`,
		datasetVersionID, datasetID, fieldID).Scan(&fieldCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidRequest
	}
	return fieldCode, err
}

const memberColumns = `member.id::text,member.dimension_id::text,member.member_key,
	member.canonical_label,member.normalized_value,member.status,member.first_seen_at,
	member.last_seen_at,member.valid_from,member.valid_to,
	COALESCE(member.refresh_generation::text,''),COALESCE(member.last_refresh_job_id::text,''),
	member.updated_at`

const memberReadScopeCTE = `WITH subject_roles AS MATERIALIZED (
		SELECT user_role.role_id
		FROM platform.user_roles AS user_role
		JOIN platform.roles AS role
		  ON role.tenant_id=user_role.tenant_id
		  AND role.id=user_role.role_id
		  AND role.status='ACTIVE' AND role.deleted_at IS NULL
		WHERE user_role.user_id=$1::uuid
	), global_dataset_read AS MATERIALIZED (
		SELECT EXISTS(
			SELECT 1
			FROM subject_roles
			JOIN platform.role_permissions AS role_permission
			  ON role_permission.role_id=subject_roles.role_id
			JOIN platform.permissions AS permission
			  ON permission.tenant_id=role_permission.tenant_id
			  AND permission.id=role_permission.permission_id
			  AND permission.resource_type='DATASET'
			  AND permission.action='READ'
		) AS allowed
	)`

const authorizedDimensionMemberPredicate = `
	  AND (
	    (SELECT allowed FROM global_dataset_read)
	    OR EXISTS(
	      SELECT 1 FROM platform.object_permissions AS object_permission
	      WHERE object_permission.object_type='DATASET'
	        AND object_permission.object_id=dimension.dataset_id
	        AND object_permission.action='READ'
	        AND (
	          (
	            object_permission.subject_type='USER'
	            AND object_permission.subject_id=$1::uuid
	          )
	          OR (
	            object_permission.subject_type='ROLE'
	            AND object_permission.subject_id IN (
	              SELECT role_id FROM subject_roles
	            )
	          )
	        )
	    )
	  )
	  AND NOT EXISTS(
	    SELECT 1 FROM platform.data_row_policies AS row_policy
	    WHERE row_policy.object_type='DATASET'
	      AND row_policy.object_id=dimension.dataset_id
	      AND row_policy.enabled
	      AND (
	        (
	          cardinality(row_policy.applicable_user_ids)=0
	          AND cardinality(row_policy.applicable_role_ids)=0
	        )
	        OR $1::uuid=ANY(row_policy.applicable_user_ids)
	        OR row_policy.applicable_role_ids && ARRAY(
	          SELECT role_id FROM subject_roles
	        )
	      )
	  )
	  AND NOT EXISTS(
	    SELECT 1 FROM platform.data_column_policies AS column_policy
	    WHERE column_policy.object_type='DATASET'
	      AND column_policy.object_id=dimension.dataset_id
	      AND column_policy.field_code=dimension_field.field_code::text
	      AND column_policy.enabled
	      AND column_policy.policy_type<>'ALLOW'
	      AND (
	        (
	          cardinality(column_policy.applicable_user_ids)=0
	          AND cardinality(column_policy.applicable_role_ids)=0
	        )
	        OR $1::uuid=ANY(column_policy.applicable_user_ids)
	        OR column_policy.applicable_role_ids && ARRAY(
	          SELECT role_id FROM subject_roles
	        )
	      )
	  )`

const authorizedMetricDatasetPredicate = `
	  AND (
	    (SELECT allowed FROM global_dataset_read)
	    OR EXISTS(
	      SELECT 1 FROM platform.object_permissions AS metric_dataset_permission
	      WHERE metric_dataset_permission.object_type='DATASET'
	        AND metric_dataset_permission.object_id=dataset.id
	        AND metric_dataset_permission.action='READ'
	        AND (
	          (
	            metric_dataset_permission.subject_type='USER'
	            AND metric_dataset_permission.subject_id=$1::uuid
	          )
	          OR (
	            metric_dataset_permission.subject_type='ROLE'
	            AND metric_dataset_permission.subject_id IN (
	              SELECT role_id FROM subject_roles
	            )
	          )
	        )
	    )
	  )`

func (s *PostgresStore) ListDimensionMembers(ctx context.Context, tenantID, actorID string, filter DimensionMemberFilter) (items []DimensionMember, total int, err error) {
	items = []DimensionMember{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := requireDimensionMemberReadScope(
			ctx, tx, actorID, filter.DimensionID,
		); err != nil {
			return err
		}
		rows, queryErr := tx.Query(ctx, memberReadScopeCTE+` SELECT `+memberColumns+`,count(*) OVER()::int
			FROM platform.dimension_members AS member
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=member.tenant_id
			  AND dimension.id=member.dimension_id
			JOIN platform.dataset_fields AS dimension_field
			  ON dimension_field.tenant_id=dimension.tenant_id
			  AND dimension_field.dataset_version_id=dimension.dataset_version_id
			  AND dimension_field.field_id=dimension.field_id
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=dimension.tenant_id
			  AND version.id=dimension.dataset_version_id
			  AND version.dataset_id=dimension.dataset_id
			  AND version.layer='DWS' AND version.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			  AND dataset.id=version.dataset_id
			  AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
			  AND dataset.current_published_version_id=version.id
			  AND dataset.deleted_at IS NULL
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.tenant_id=dimension.tenant_id
			  AND materialization.dataset_id=dimension.dataset_id
			  AND materialization.dataset_version_id=dimension.dataset_version_id
			  AND materialization.layer='DWS'
			  AND materialization.status='ACTIVE'
			  AND materialization.schema_hash=version.schema_hash
			JOIN platform.dimension_profile_jobs AS profile
			  ON profile.tenant_id=materialization.tenant_id
			  AND profile.dataset_id=materialization.dataset_id
			  AND profile.dataset_version_id=materialization.dataset_version_id
			  AND profile.materialization_id=materialization.id
			  AND profile.schema_hash=materialization.schema_hash
			  AND profile.materialization_snapshot_hash=materialization.snapshot_hash
			  AND profile.field_id=dimension.field_id
			  AND profile.profile_version='dws-dimension-profile-v1'
			  AND profile.policy_version='dimension-member-policy-v1'
			  AND profile.status='SUCCEEDED'
			  AND profile.recommended_member_index_policy='FULL'
			JOIN platform.dimension_member_refresh_jobs AS refresh_job
			  ON refresh_job.tenant_id=dimension.tenant_id
			  AND refresh_job.dimension_id=dimension.id
			  AND refresh_job.id=dimension.last_member_refresh_job_id
			  AND refresh_job.materialization_id=materialization.id
			  AND refresh_job.refresh_generation=
			    dimension.member_refresh_generation
			  AND refresh_job.dimension_version=dimension.version
			  AND refresh_job.status='SUCCEEDED'
			WHERE member.dimension_id=$2::uuid
			  AND member.tenant_id=platform.current_tenant_id()
			  AND member.status='ACTIVE'
			  AND member.refresh_generation=dimension.member_refresh_generation
			  AND member.last_refresh_job_id=refresh_job.id
			  AND dimension.status='PUBLISHED'
			  AND dimension.member_index_policy='FULL'
			  AND NOT dimension.sensitive
			  AND ($3='' OR member.member_key ILIKE '%'||$3||'%'
			    OR member.canonical_label ILIKE '%'||$3||'%'
			    OR member.normalized_value=$3)
			  AND ($4='' OR $4='ACTIVE')
			`+authorizedDimensionMemberPredicate+`
			ORDER BY member.canonical_label,member.id
			LIMIT $5 OFFSET $6`,
			actorID, filter.DimensionID, filter.Query, filter.Status,
			filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DimensionMember
			if err := scanDimensionMember(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func requireDimensionMemberReadScope(
	ctx context.Context,
	tx pgx.Tx,
	actorID, dimensionID string,
) error {
	var allowed, rowRestricted, columnRestricted bool
	err := tx.QueryRow(ctx, memberReadScopeCTE+` SELECT
			(
			  (SELECT allowed FROM global_dataset_read)
			  OR EXISTS(
			    SELECT 1 FROM platform.object_permissions AS object_permission
			    WHERE object_permission.object_type='DATASET'
			      AND object_permission.object_id=dimension.dataset_id
			      AND object_permission.action='READ'
			      AND (
			        (
			          object_permission.subject_type='USER'
			          AND object_permission.subject_id=$1::uuid
			        )
			        OR (
			          object_permission.subject_type='ROLE'
			          AND object_permission.subject_id IN (
			            SELECT role_id FROM subject_roles
			          )
			        )
			      )
			  )
			),
			EXISTS(
			  SELECT 1 FROM platform.data_row_policies AS row_policy
			  WHERE row_policy.object_type='DATASET'
			    AND row_policy.object_id=dimension.dataset_id
			    AND row_policy.enabled
			    AND (
			      (
			        cardinality(row_policy.applicable_user_ids)=0
			        AND cardinality(row_policy.applicable_role_ids)=0
			      )
			      OR $1::uuid=ANY(row_policy.applicable_user_ids)
			      OR row_policy.applicable_role_ids && ARRAY(
			        SELECT role_id FROM subject_roles
			      )
			    )
			),
			EXISTS(
			  SELECT 1 FROM platform.data_column_policies AS column_policy
			  WHERE column_policy.object_type='DATASET'
			    AND column_policy.object_id=dimension.dataset_id
			    AND column_policy.field_code=dimension_field.field_code::text
			    AND column_policy.enabled
			    AND column_policy.policy_type<>'ALLOW'
			    AND (
			      (
			        cardinality(column_policy.applicable_user_ids)=0
			        AND cardinality(column_policy.applicable_role_ids)=0
			      )
			      OR $1::uuid=ANY(column_policy.applicable_user_ids)
			      OR column_policy.applicable_role_ids && ARRAY(
			        SELECT role_id FROM subject_roles
			      )
			    )
			)
		FROM platform.semantic_dimensions AS dimension
		JOIN platform.dataset_fields AS dimension_field
		  ON dimension_field.tenant_id=dimension.tenant_id
		  AND dimension_field.dataset_version_id=dimension.dataset_version_id
		  AND dimension_field.field_id=dimension.field_id
		WHERE dimension.id=$2::uuid`,
		actorID, dimensionID).Scan(&allowed, &rowRestricted, &columnRestricted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !allowed || rowRestricted || columnRestricted {
		return ErrMemberAccessDenied
	}
	return nil
}

const dimensionAliasColumns = `alias.id::text,alias.dimension_id::text,
	alias.dimension_member_id::text,alias.alias,alias.normalized_alias,alias.alias_type,
	alias.valid_from,alias.valid_to,alias.version,COALESCE(alias.created_by::text,''),
	COALESCE(alias.updated_by::text,''),alias.created_at,alias.updated_at`

func (s *PostgresStore) ListDimensionMemberAliases(ctx context.Context, tenantID, actorID string, filter DimensionMemberAliasFilter) (items []DimensionMemberAlias, total int, err error) {
	items = []DimensionMemberAlias{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if filter.DimensionID != "" {
			if err := requireDimensionMemberReadScope(
				ctx, tx, actorID, filter.DimensionID,
			); err != nil {
				return err
			}
		}
		rows, queryErr := tx.Query(ctx, memberReadScopeCTE+` SELECT `+dimensionAliasColumns+`,count(*) OVER()::int
			FROM platform.dimension_member_aliases AS alias
			JOIN platform.dimension_members AS member
			  ON member.tenant_id=alias.tenant_id
			  AND member.dimension_id=alias.dimension_id
			  AND member.id=alias.dimension_member_id
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=alias.tenant_id
			  AND dimension.id=alias.dimension_id
			JOIN platform.dataset_fields AS dimension_field
			  ON dimension_field.tenant_id=dimension.tenant_id
			  AND dimension_field.dataset_version_id=dimension.dataset_version_id
			  AND dimension_field.field_id=dimension.field_id
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=dimension.tenant_id
			  AND version.id=dimension.dataset_version_id
			  AND version.dataset_id=dimension.dataset_id
			  AND version.layer='DWS' AND version.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id
			  AND dataset.id=version.dataset_id
			  AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
			  AND dataset.current_published_version_id=version.id
			  AND dataset.deleted_at IS NULL
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.tenant_id=dimension.tenant_id
			  AND materialization.dataset_id=dimension.dataset_id
			  AND materialization.dataset_version_id=dimension.dataset_version_id
			  AND materialization.layer='DWS'
			  AND materialization.status='ACTIVE'
			  AND materialization.schema_hash=version.schema_hash
			JOIN platform.dimension_profile_jobs AS profile
			  ON profile.tenant_id=materialization.tenant_id
			  AND profile.dataset_id=materialization.dataset_id
			  AND profile.dataset_version_id=materialization.dataset_version_id
			  AND profile.materialization_id=materialization.id
			  AND profile.schema_hash=materialization.schema_hash
			  AND profile.materialization_snapshot_hash=materialization.snapshot_hash
			  AND profile.field_id=dimension.field_id
			  AND profile.profile_version='dws-dimension-profile-v1'
			  AND profile.policy_version='dimension-member-policy-v1'
			  AND profile.status='SUCCEEDED'
			  AND profile.recommended_member_index_policy='FULL'
			JOIN platform.dimension_member_refresh_jobs AS refresh_job
			  ON refresh_job.tenant_id=dimension.tenant_id
			  AND refresh_job.dimension_id=dimension.id
			  AND refresh_job.id=dimension.last_member_refresh_job_id
			  AND refresh_job.materialization_id=materialization.id
			  AND refresh_job.refresh_generation=
			    dimension.member_refresh_generation
			  AND refresh_job.dimension_version=dimension.version
			  AND refresh_job.status='SUCCEEDED'
			WHERE alias.tenant_id=platform.current_tenant_id()
			  AND member.status='ACTIVE'
			  AND member.refresh_generation=dimension.member_refresh_generation
			  AND member.last_refresh_job_id=refresh_job.id
			  AND dimension.status='PUBLISHED'
			  AND dimension.member_index_policy='FULL'
			  AND NOT dimension.sensitive
			  AND ($2='' OR alias.dimension_id::text=$2)
			  AND ($3='' OR alias.dimension_member_id::text=$3)
			  AND ($4='' OR alias.alias ILIKE '%'||$4||'%' OR alias.normalized_alias=$4)
			  AND ($5='' OR alias.alias_type=$5)
			`+authorizedDimensionMemberPredicate+`
			ORDER BY alias.alias,alias.id
			LIMIT $6 OFFSET $7`,
			actorID, filter.DimensionID, filter.DimensionMemberID, filter.Query,
			filter.AliasType, filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DimensionMemberAlias
			if err := scanDimensionMemberAlias(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) CreateDimensionMemberAlias(
	ctx context.Context,
	tenantID, actorID string,
	input CreateDimensionMemberAliasInput,
	normalized string,
) (item DimensionMemberAlias, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := requireActiveDimensionMember(ctx, tx, input.DimensionID, input.DimensionMemberID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.dimension_member_aliases(
				tenant_id,dimension_id,dimension_member_id,alias,normalized_alias,
				alias_type,valid_from,valid_to,created_by,updated_by
			) VALUES(platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$8)
			RETURNING `+bareDimensionAliasColumns,
			input.DimensionID, input.DimensionMemberID, input.Alias, normalized,
			input.AliasType, input.ValidFrom, input.ValidTo, actorID)
		if err := scanDimensionMemberAlias(row, &item); err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "DIMENSION_MEMBER_ALIAS_CREATE", "DIMENSION_MEMBER_ALIAS", item.ID,
			map[string]any{"dimensionId": item.DimensionID, "dimensionMemberId": item.DimensionMemberID})
	})
	return item, err
}

func (s *PostgresStore) UpdateDimensionMemberAlias(
	ctx context.Context,
	tenantID, actorID, id string,
	input UpdateDimensionMemberAliasInput,
	normalized string,
) (item DimensionMemberAlias, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		var dimensionID, memberID string
		var version int64
		err := tx.QueryRow(ctx, `SELECT dimension_id::text,dimension_member_id::text,version
			FROM platform.dimension_member_aliases WHERE id=$1::uuid FOR UPDATE`, id).
			Scan(&dimensionID, &memberID, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		if err := requireActiveDimensionMember(ctx, tx, dimensionID, memberID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `UPDATE platform.dimension_member_aliases SET
				alias=$1,normalized_alias=$2,alias_type=$3,valid_from=$4,valid_to=$5,
				version=version+1,updated_by=$6
			WHERE id=$7::uuid
			RETURNING `+bareDimensionAliasColumns,
			input.Alias, normalized, input.AliasType, input.ValidFrom, input.ValidTo,
			actorID, id)
		if err := scanDimensionMemberAlias(row, &item); err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "DIMENSION_MEMBER_ALIAS_UPDATE", "DIMENSION_MEMBER_ALIAS", item.ID,
			map[string]any{"previousVersion": input.ExpectedVersion, "version": item.Version})
	})
	return item, err
}

func (s *PostgresStore) DeleteDimensionMemberAlias(ctx context.Context, tenantID, actorID, id string, expectedVersion int64) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		var dimensionID, memberID string
		err := tx.QueryRow(ctx, `DELETE FROM platform.dimension_member_aliases
			WHERE id=$1::uuid AND version=$2
			RETURNING dimension_id::text,dimension_member_id::text`,
			id, expectedVersion).Scan(&dimensionID, &memberID)
		if errors.Is(err, pgx.ErrNoRows) {
			return classifyMissingOrConflict(ctx, tx, "platform.dimension_member_aliases", id)
		}
		if err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "DIMENSION_MEMBER_ALIAS_DELETE", "DIMENSION_MEMBER_ALIAS", id,
			map[string]any{"dimensionId": dimensionID, "dimensionMemberId": memberID})
	})
}

const bareDimensionAliasColumns = `id::text,dimension_id::text,dimension_member_id::text,
	alias,normalized_alias,alias_type,valid_from,valid_to,version,
	COALESCE(created_by::text,''),COALESCE(updated_by::text,''),created_at,updated_at`

func requireActiveDimensionMember(ctx context.Context, tx pgx.Tx, dimensionID, memberID string) error {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.dimension_members AS member
		JOIN platform.semantic_dimensions AS dimension
		  ON dimension.tenant_id=member.tenant_id AND dimension.id=member.dimension_id
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=dimension.tenant_id
		  AND version.id=dimension.dataset_version_id
		  AND version.dataset_id=dimension.dataset_id
		  AND version.layer='DWS' AND version.status='PUBLISHED'
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id
		  AND dataset.id=version.dataset_id
		  AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
		  AND dataset.current_published_version_id=version.id
		  AND dataset.deleted_at IS NULL
		JOIN platform.dataset_materializations AS materialization
		  ON materialization.tenant_id=dimension.tenant_id
		  AND materialization.dataset_id=dimension.dataset_id
		  AND materialization.dataset_version_id=dimension.dataset_version_id
		  AND materialization.layer='DWS' AND materialization.status='ACTIVE'
		  AND materialization.schema_hash=version.schema_hash
		JOIN platform.dimension_profile_jobs AS profile
		  ON profile.tenant_id=materialization.tenant_id
		  AND profile.dataset_id=materialization.dataset_id
		  AND profile.dataset_version_id=materialization.dataset_version_id
		  AND profile.materialization_id=materialization.id
		  AND profile.schema_hash=materialization.schema_hash
		  AND profile.materialization_snapshot_hash=materialization.snapshot_hash
		  AND profile.field_id=dimension.field_id
		  AND profile.profile_version='dws-dimension-profile-v1'
		  AND profile.policy_version='dimension-member-policy-v1'
		  AND profile.status='SUCCEEDED'
		  AND profile.recommended_member_index_policy='FULL'
		JOIN platform.dimension_member_refresh_jobs AS refresh_job
		  ON refresh_job.tenant_id=dimension.tenant_id
		  AND refresh_job.dimension_id=dimension.id
		  AND refresh_job.id=dimension.last_member_refresh_job_id
		  AND refresh_job.materialization_id=materialization.id
		  AND refresh_job.refresh_generation=dimension.member_refresh_generation
		  AND refresh_job.dimension_version=dimension.version
		  AND refresh_job.status='SUCCEEDED'
		WHERE member.id=$1::uuid AND member.dimension_id=$2::uuid
		  AND member.status='ACTIVE' AND dimension.status='PUBLISHED'
		  AND member.refresh_generation=dimension.member_refresh_generation
		  AND member.last_refresh_job_id=refresh_job.id
		  AND dimension.member_index_policy='FULL'
		  AND NOT dimension.sensitive
	)`, memberID, dimensionID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return ErrConflict
	}
	return nil
}

const compatibilityColumns = `compatibility.id::text,compatibility.dimension_id::text,
	compatibility.metric_id::text,compatibility.metric_version_id::text,
	compatibility.metric_dataset_version_id::text,compatibility.compatibility_type,
	compatibility.fanout_policy,compatibility.join_path_json,compatibility.evidence_source,
	compatibility.confidence,compatibility.status,compatibility.version,
	COALESCE(compatibility.verified_by::text,''),compatibility.verified_at,
	COALESCE(compatibility.created_by::text,''),COALESCE(compatibility.updated_by::text,''),
	compatibility.created_at,compatibility.updated_at`

func (s *PostgresStore) ListCompatibilities(ctx context.Context, tenantID string, filter CompatibilityFilter) (items []DimensionMetricCompatibility, total int, err error) {
	items = []DimensionMetricCompatibility{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+compatibilityColumns+`,count(*) OVER()::int
			FROM platform.dimension_metric_compatibility AS compatibility
			WHERE compatibility.tenant_id=platform.current_tenant_id()
			  AND ($1='' OR compatibility.dimension_id::text=$1)
			  AND ($2='' OR compatibility.metric_version_id::text=$2)
			  AND ($3='' OR compatibility.status=$3)
			ORDER BY compatibility.updated_at DESC,compatibility.id
			LIMIT $4 OFFSET $5`,
			filter.DimensionID, filter.MetricVersionID, filter.Status,
			filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DimensionMetricCompatibility
			if err := scanCompatibility(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) ProposeCompatibility(
	ctx context.Context,
	tenantID, actorID string,
	input ProposeCompatibilityInput,
) (item DimensionMetricCompatibility, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := requireCompatibilitySubjects(ctx, tx, input.DimensionID, input.MetricID,
			input.MetricVersionID, input.MetricDatasetVersionID, false); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.dimension_metric_compatibility(
				tenant_id,dimension_id,metric_id,metric_version_id,metric_dataset_version_id,
				compatibility_type,fanout_policy,join_path_json,evidence_source,confidence,
				status,created_by,updated_by
			) VALUES(platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$9,'PROPOSED',$10,$10)
			RETURNING `+bareCompatibilityColumns,
			input.DimensionID, input.MetricID, input.MetricVersionID,
			input.MetricDatasetVersionID, input.CompatibilityType, input.FanoutPolicy,
			input.JoinPath, input.EvidenceSource, input.Confidence, actorID)
		if err := scanCompatibility(row, &item); err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "DIMENSION_METRIC_COMPATIBILITY_PROPOSE", "DIMENSION_METRIC_COMPATIBILITY", item.ID,
			map[string]any{"dimensionId": item.DimensionID, "metricVersionId": item.MetricVersionID})
	})
	return item, err
}

func (s *PostgresStore) UpdateCompatibility(
	ctx context.Context,
	tenantID, actorID, id string,
	input UpdateCompatibilityInput,
) (item DimensionMetricCompatibility, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `UPDATE platform.dimension_metric_compatibility SET
				compatibility_type=$1,fanout_policy=$2,join_path_json=$3,
				evidence_source=$4,confidence=$5,version=version+1,updated_by=$6
			WHERE id=$7::uuid AND version=$8 AND status='PROPOSED'
			RETURNING `+bareCompatibilityColumns,
			input.CompatibilityType, input.FanoutPolicy, input.JoinPath,
			input.EvidenceSource, input.Confidence, actorID, id, input.ExpectedVersion)
		if err := scanCompatibility(row, &item); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return mapWriteError(err)
			}
			return classifyMissingOrConflict(ctx, tx, "platform.dimension_metric_compatibility", id)
		}
		return auditMutation(ctx, tx, actorID, "DIMENSION_METRIC_COMPATIBILITY_UPDATE", "DIMENSION_METRIC_COMPATIBILITY", item.ID,
			map[string]any{"previousVersion": input.ExpectedVersion, "version": item.Version})
	})
	return item, err
}

func (s *PostgresStore) DecideCompatibility(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
	decision string,
) (item DimensionMetricCompatibility, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		var dimensionID, metricID, metricVersionID, metricDatasetVersionID string
		var fanout, status string
		var version int64
		err := tx.QueryRow(ctx, `SELECT dimension_id::text,metric_id::text,
				metric_version_id::text,metric_dataset_version_id::text,
				fanout_policy,status,version
			FROM platform.dimension_metric_compatibility WHERE id=$1::uuid FOR UPDATE`, id).
			Scan(&dimensionID, &metricID, &metricVersionID, &metricDatasetVersionID,
				&fanout, &status, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status != "PROPOSED" || version != expectedVersion {
			return ErrConflict
		}
		if decision == "VERIFIED" {
			if fanout == "UNSAFE" {
				return ErrInvalidRequest
			}
			if err := requireCompatibilitySubjects(ctx, tx, dimensionID, metricID,
				metricVersionID, metricDatasetVersionID, true); err != nil {
				return err
			}
		}
		row := tx.QueryRow(ctx, `UPDATE platform.dimension_metric_compatibility SET
				status=$1,version=version+1,updated_by=$2,
				verified_by=CASE WHEN $1='VERIFIED' THEN $2::uuid ELSE NULL END,
				verified_at=CASE WHEN $1='VERIFIED' THEN now() ELSE NULL END
			WHERE id=$3::uuid
			RETURNING `+bareCompatibilityColumns, decision, actorID, id)
		if err := scanCompatibility(row, &item); err != nil {
			return mapWriteError(err)
		}
		action := "DIMENSION_METRIC_COMPATIBILITY_" + decision
		return auditMutation(ctx, tx, actorID, action, "DIMENSION_METRIC_COMPATIBILITY", item.ID,
			map[string]any{"previousVersion": expectedVersion, "version": item.Version})
	})
	return item, err
}

const bareCompatibilityColumns = `id::text,dimension_id::text,metric_id::text,
	metric_version_id::text,metric_dataset_version_id::text,compatibility_type,
	fanout_policy,join_path_json,evidence_source,confidence,status,version,
	COALESCE(verified_by::text,''),verified_at,COALESCE(created_by::text,''),
	COALESCE(updated_by::text,''),created_at,updated_at`

func requireCompatibilitySubjects(
	ctx context.Context,
	tx pgx.Tx,
	dimensionID, metricID, metricVersionID, metricDatasetVersionID string,
	requirePublishedDimensionAndMaterializations bool,
) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.semantic_dimensions AS dimension
		JOIN platform.dataset_versions AS dimension_version
		  ON dimension_version.tenant_id=dimension.tenant_id
		  AND dimension_version.id=dimension.dataset_version_id
		  AND dimension_version.dataset_id=dimension.dataset_id
		  AND dimension_version.layer='DWS'
		  AND dimension_version.status='PUBLISHED'
		JOIN platform.metric_versions AS version
		  ON version.tenant_id=dimension.tenant_id
		  AND version.id=$2::uuid AND version.metric_id=$3::uuid
		  AND version.dataset_version_id=$4::uuid
		JOIN platform.dataset_versions AS metric_dataset_version
		  ON metric_dataset_version.tenant_id=version.tenant_id
		  AND metric_dataset_version.id=version.dataset_version_id
		  AND metric_dataset_version.dataset_id=version.dataset_id
		  AND metric_dataset_version.layer='DWS'
		  AND metric_dataset_version.status='PUBLISHED'
		JOIN platform.metrics AS metric
		  ON metric.tenant_id=version.tenant_id AND metric.id=version.metric_id
		WHERE dimension.id=$1::uuid AND dimension.status<>'DEPRECATED'
		  AND version.status='PUBLISHED' AND metric.status='PUBLISHED'
		  AND metric.current_published_version_id=version.id
		  AND metric.deleted_at IS NULL
		  AND (
		    NOT $5
		    OR (
		      dimension.status='PUBLISHED'
		      AND EXISTS(
		        SELECT 1 FROM platform.dataset_materializations AS materialization
		        WHERE materialization.tenant_id=dimension.tenant_id
		          AND materialization.dataset_id=dimension.dataset_id
		          AND materialization.dataset_version_id=dimension.dataset_version_id
		          AND materialization.layer='DWS'
		          AND materialization.status='ACTIVE'
		      )
		      AND EXISTS(
		        SELECT 1 FROM platform.dataset_materializations AS materialization
		        WHERE materialization.tenant_id=version.tenant_id
		          AND materialization.dataset_id=version.dataset_id
		          AND materialization.dataset_version_id=version.dataset_version_id
		          AND materialization.layer='DWS'
		          AND materialization.status='ACTIVE'
		      )
		    )
		  )
	)`, dimensionID, metricVersionID, metricID, metricDatasetVersionID,
		requirePublishedDimensionAndMaterializations).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) SearchMemberMetrics(ctx context.Context, tenantID, actorID, query string, limit int) (items []MemberMetricSearchResult, err error) {
	items = []MemberMetricSearchResult{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, memberReadScopeCTE+` SELECT
				COALESCE(matched_alias.alias,member.canonical_label),
				CASE WHEN matched_alias.id IS NULL THEN 'MEMBER_VALUE' ELSE 'MEMBER_ALIAS' END,
				dimension.id::text,dimension.code::text,dimension.name,
				member.id::text,member.member_key,member.canonical_label,
				metric.id::text,version.id::text,metric.code::text,metric.name,
				dataset.id::text,version.dataset_version_id::text,
				dataset.code::text,dataset.name,compatibility.compatibility_type,
				compatibility.fanout_policy,metric_materialization.published_schema,
				metric_materialization.published_name
			FROM platform.dimension_members AS member
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=member.tenant_id AND dimension.id=member.dimension_id
			JOIN platform.dataset_fields AS dimension_field
			  ON dimension_field.tenant_id=dimension.tenant_id
			  AND dimension_field.dataset_version_id=dimension.dataset_version_id
			  AND dimension_field.field_id=dimension.field_id
			JOIN platform.dataset_versions AS dimension_version
			  ON dimension_version.tenant_id=dimension.tenant_id
			  AND dimension_version.id=dimension.dataset_version_id
			  AND dimension_version.dataset_id=dimension.dataset_id
			  AND dimension_version.layer='DWS'
			  AND dimension_version.status='PUBLISHED'
			JOIN platform.datasets AS dimension_dataset
			  ON dimension_dataset.tenant_id=dimension_version.tenant_id
			  AND dimension_dataset.id=dimension_version.dataset_id
			  AND dimension_dataset.layer='DWS'
			  AND dimension_dataset.status='PUBLISHED'
			  AND dimension_dataset.current_published_version_id=
			    dimension_version.id
			  AND dimension_dataset.deleted_at IS NULL
			LEFT JOIN LATERAL (
				SELECT alias.id,alias.alias
				FROM platform.dimension_member_aliases AS alias
				WHERE alias.tenant_id=member.tenant_id
				  AND alias.dimension_id=member.dimension_id
				  AND alias.dimension_member_id=member.id
				  AND alias.normalized_alias=$2
				  AND (alias.valid_from IS NULL OR alias.valid_from<=now())
				  AND (alias.valid_to IS NULL OR alias.valid_to>now())
				ORDER BY alias.id LIMIT 1
			) AS matched_alias ON true
			JOIN platform.dimension_metric_compatibility AS compatibility
			  ON compatibility.tenant_id=dimension.tenant_id
			  AND compatibility.dimension_id=dimension.id
			  AND compatibility.status='VERIFIED' AND compatibility.fanout_policy<>'UNSAFE'
			JOIN platform.metric_versions AS version
			  ON version.tenant_id=compatibility.tenant_id
			  AND version.id=compatibility.metric_version_id
			  AND version.metric_id=compatibility.metric_id
			  AND version.dataset_version_id=compatibility.metric_dataset_version_id
			  AND version.status='PUBLISHED'
			JOIN platform.dataset_versions AS metric_dataset_version
			  ON metric_dataset_version.tenant_id=version.tenant_id
			  AND metric_dataset_version.id=version.dataset_version_id
			  AND metric_dataset_version.dataset_id=version.dataset_id
			  AND metric_dataset_version.layer='DWS'
			  AND metric_dataset_version.status='PUBLISHED'
			JOIN platform.metrics AS metric
			  ON metric.tenant_id=version.tenant_id AND metric.id=version.metric_id
			  AND metric.status='PUBLISHED' AND metric.current_published_version_id=version.id
			  AND metric.deleted_at IS NULL
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=version.tenant_id AND dataset.id=version.dataset_id
			  AND dataset.layer='DWS' AND dataset.status='PUBLISHED'
			  AND dataset.current_published_version_id=
			    metric_dataset_version.id
			  AND dataset.deleted_at IS NULL
			JOIN platform.dataset_materializations AS dimension_materialization
			  ON dimension_materialization.tenant_id=dimension.tenant_id
			  AND dimension_materialization.dataset_id=dimension.dataset_id
			  AND dimension_materialization.dataset_version_id=dimension.dataset_version_id
			  AND dimension_materialization.layer='DWS'
			  AND dimension_materialization.status='ACTIVE'
			  AND dimension_materialization.schema_hash=
			    dimension_version.schema_hash
			JOIN platform.dimension_profile_jobs AS dimension_profile
			  ON dimension_profile.tenant_id=
			    dimension_materialization.tenant_id
			  AND dimension_profile.dataset_id=
			    dimension_materialization.dataset_id
			  AND dimension_profile.dataset_version_id=
			    dimension_materialization.dataset_version_id
			  AND dimension_profile.materialization_id=
			    dimension_materialization.id
			  AND dimension_profile.schema_hash=
			    dimension_materialization.schema_hash
			  AND dimension_profile.materialization_snapshot_hash=
			    dimension_materialization.snapshot_hash
			  AND dimension_profile.field_id=dimension.field_id
			  AND dimension_profile.profile_version=
			    'dws-dimension-profile-v1'
			  AND dimension_profile.policy_version=
			    'dimension-member-policy-v1'
			  AND dimension_profile.status='SUCCEEDED'
			  AND dimension_profile.recommended_member_index_policy='FULL'
			JOIN platform.dimension_member_refresh_jobs AS dimension_refresh_job
			  ON dimension_refresh_job.tenant_id=dimension.tenant_id
			  AND dimension_refresh_job.dimension_id=dimension.id
			  AND dimension_refresh_job.id=dimension.last_member_refresh_job_id
			  AND dimension_refresh_job.materialization_id=
			    dimension_materialization.id
			  AND dimension_refresh_job.refresh_generation=
			    dimension.member_refresh_generation
			  AND dimension_refresh_job.dimension_version=dimension.version
			  AND dimension_refresh_job.status='SUCCEEDED'
			JOIN platform.dataset_materializations AS metric_materialization
			  ON metric_materialization.tenant_id=version.tenant_id
			  AND metric_materialization.dataset_id=version.dataset_id
			  AND metric_materialization.dataset_version_id=version.dataset_version_id
			  AND metric_materialization.layer='DWS'
			  AND metric_materialization.status='ACTIVE'
			WHERE member.tenant_id=platform.current_tenant_id()
			  AND member.status='ACTIVE' AND dimension.status='PUBLISHED'
			  AND member.refresh_generation=dimension.member_refresh_generation
			  AND member.last_refresh_job_id=dimension_refresh_job.id
			  AND NOT dimension.sensitive
			  AND dimension.member_index_policy='FULL'
			  AND (member.normalized_value=$2 OR matched_alias.id IS NOT NULL)
			`+authorizedDimensionMemberPredicate+authorizedMetricDatasetPredicate+`
			ORDER BY
			  CASE WHEN matched_alias.id IS NULL THEN 1 ELSE 0 END,
			  metric.name,metric.id,member.id
			LIMIT $3`, actorID, query, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item MemberMetricSearchResult
			if err := rows.Scan(
				&item.MatchedValue, &item.MatchType, &item.DimensionID,
				&item.DimensionCode, &item.DimensionName, &item.DimensionMemberID,
				&item.MemberKey, &item.CanonicalLabel, &item.MetricID,
				&item.MetricVersionID, &item.MetricCode, &item.MetricName,
				&item.DatasetID, &item.DatasetVersionID, &item.DatasetCode,
				&item.DatasetName, &item.CompatibilityType, &item.FanoutPolicy,
				&item.PublishedSchema, &item.PublishedName,
			); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

type scanRow interface{ Scan(...any) error }

func scanDimension(row scanRow, item *Dimension, extra ...any) error {
	targets := []any{
		&item.ID, &item.DatasetID, &item.DatasetVersionID, &item.FieldID, &item.FieldCode,
		&item.Code, &item.Name, &item.Description, &item.DimensionType,
		&item.MemberIndexPolicy, &item.HighCardinality, &item.Sensitive,
		&item.Status, &item.DefinitionHash, &item.Version,
		&item.MemberRefreshGeneration, &item.MemberCount, &item.MemberRefreshedAt,
		&item.LastMemberRefreshJobID, &item.CreatedBy, &item.UpdatedBy,
		&item.CreatedAt, &item.UpdatedAt,
	}
	return row.Scan(append(targets, extra...)...)
}

func scanBareDimension(row scanRow, item *Dimension) error {
	return scanDimension(row, item)
}

func scanDimensionMember(row scanRow, item *DimensionMember, extra ...any) error {
	targets := []any{
		&item.ID, &item.DimensionID, &item.MemberKey, &item.CanonicalLabel,
		&item.NormalizedValue, &item.Status, &item.FirstSeenAt, &item.LastSeenAt,
		&item.ValidFrom, &item.ValidTo, &item.RefreshGeneration,
		&item.LastRefreshJobID, &item.UpdatedAt,
	}
	return row.Scan(append(targets, extra...)...)
}

func scanDimensionMemberAlias(row scanRow, item *DimensionMemberAlias, extra ...any) error {
	targets := []any{
		&item.ID, &item.DimensionID, &item.DimensionMemberID, &item.Alias,
		&item.NormalizedAlias, &item.AliasType, &item.ValidFrom, &item.ValidTo,
		&item.Version, &item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt,
	}
	return row.Scan(append(targets, extra...)...)
}

func scanCompatibility(row scanRow, item *DimensionMetricCompatibility, extra ...any) error {
	targets := []any{
		&item.ID, &item.DimensionID, &item.MetricID, &item.MetricVersionID,
		&item.MetricDatasetVersionID, &item.CompatibilityType, &item.FanoutPolicy,
		&item.JoinPath, &item.EvidenceSource, &item.Confidence, &item.Status,
		&item.Version, &item.VerifiedBy, &item.VerifiedAt, &item.CreatedBy,
		&item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt,
	}
	return row.Scan(append(targets, extra...)...)
}
