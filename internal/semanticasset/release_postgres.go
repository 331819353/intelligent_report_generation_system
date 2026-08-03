package semanticasset

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const semanticReleaseColumns = `release.id::text,release.semantic_version,
	release.content_hash,release.status,COALESCE(release.base_release_id::text,''),
	release.notes,release.object_count,release.validation_summary,release.version,
	release.created_by::text,release.updated_by::text,
	COALESCE(release.activated_by::text,''),
	COALESCE(release.evaluation_set_id::text,''),
	release.evaluation_set_content_hash,release.created_at,release.updated_at,
	release.validated_at,release.activated_at`

func (store *PostgresStore) CreateSemanticRelease(
	ctx context.Context,
	tenantID, actorID string,
	draft semanticReleaseDraft,
) (release SemanticRelease, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO platform.semantic_releases AS release(
				tenant_id,semantic_version,content_hash,base_release_id,notes,
				object_count,created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$6
			) RETURNING `+semanticReleaseColumns,
			draft.SemanticVersion, draft.ContentHash, draft.BaseReleaseID,
			draft.Notes, len(draft.Objects), actorID,
		)
		if scanErr := scanSemanticRelease(row, &release); scanErr != nil {
			return mapWriteError(scanErr)
		}
		for _, object := range draft.Objects {
			if _, insertErr := tx.Exec(ctx, `INSERT INTO platform.semantic_release_objects(
					tenant_id,release_id,object_type,object_id,object_version,
					content_hash,domain_id,owner_id,certification,sensitivity,
					valid_from,valid_to,contract_json
				) VALUES(
					platform.current_tenant_id(),$1::uuid,$2,$3,$4,$5,$6,
					$7::uuid,$8,$9,$10,$11,$12
				)`,
				release.ID, object.ObjectType, object.ObjectID,
				object.ObjectVersion, object.ContentHash, object.DomainID,
				object.OwnerID, object.Certification, object.Sensitivity,
				object.ValidFrom, object.ValidTo, []byte(object.Contract),
			); insertErr != nil {
				return mapWriteError(insertErr)
			}
		}
		for _, target := range []string{
			SemanticProjectionExecution, SemanticProjectionRegistry,
			SemanticProjectionSearch, SemanticProjectionNebula,
		} {
			if _, insertErr := tx.Exec(ctx, `INSERT INTO platform.semantic_release_projections(
					tenant_id,release_id,target,expected_content_hash
				) VALUES(platform.current_tenant_id(),$1::uuid,$2,$3)`,
				release.ID, target, release.ContentHash,
			); insertErr != nil {
				return mapWriteError(insertErr)
			}
		}
		if eventErr := insertSemanticReleaseEvent(
			ctx, tx, release.ID, "CREATED", actorID,
			map[string]any{
				"semanticVersion": release.SemanticVersion,
				"contentHash":     release.ContentHash, "objectCount": release.ObjectCount,
			},
		); eventErr != nil {
			return eventErr
		}
		projections, loadErr := loadSemanticReleaseProjections(ctx, tx, release.ID)
		release.Projections = projections
		return loadErr
	})
	return release, err
}

func (store *PostgresStore) ListSemanticReleases(
	ctx context.Context,
	tenantID string,
	page Page,
) (releases []SemanticRelease, total int, err error) {
	releases = []SemanticRelease{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+semanticReleaseColumns+`,
				count(*) OVER()::int
			FROM platform.semantic_releases AS release
			WHERE release.tenant_id=platform.current_tenant_id()
			ORDER BY release.created_at DESC,release.id
			LIMIT $1 OFFSET $2`, page.Limit, page.Offset)
		if queryErr != nil {
			return queryErr
		}
		for rows.Next() {
			var release SemanticRelease
			if scanErr := scanSemanticRelease(rows, &release, &total); scanErr != nil {
				rows.Close()
				return scanErr
			}
			releases = append(releases, release)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			rows.Close()
			return rowsErr
		}
		rows.Close()
		for index := range releases {
			projections, loadErr := loadSemanticReleaseProjections(
				ctx, tx, releases[index].ID,
			)
			if loadErr != nil {
				return loadErr
			}
			releases[index].Projections = projections
		}
		return nil
	})
	return releases, total, err
}

func (store *PostgresStore) GetSemanticRelease(
	ctx context.Context,
	tenantID, releaseID string,
) (release SemanticRelease, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if scanErr := scanSemanticRelease(tx.QueryRow(ctx, `SELECT `+
			semanticReleaseColumns+`
			FROM platform.semantic_releases AS release
			WHERE release.id=$1::uuid`, releaseID), &release); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return scanErr
		}
		objects, loadErr := loadSemanticReleaseObjects(ctx, tx, releaseID)
		if loadErr != nil {
			return loadErr
		}
		projections, loadErr := loadSemanticReleaseProjections(ctx, tx, releaseID)
		if loadErr != nil {
			return loadErr
		}
		release.Objects, release.Projections = objects, projections
		return nil
	})
	return release, err
}

func (store *PostgresStore) GetActiveSemanticRelease(
	ctx context.Context,
	tenantID string,
) (state SemanticReleaseState, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
				COALESCE(state.active_release_id::text,''),
				COALESCE(release.semantic_version,''),COALESCE(release.content_hash,''),
				state.version,state.updated_at
			FROM platform.semantic_release_state AS state
			LEFT JOIN platform.semantic_releases AS release
			  ON release.tenant_id=state.tenant_id
			 AND release.id=state.active_release_id
			WHERE state.tenant_id=platform.current_tenant_id()`).Scan(
			&state.ActiveReleaseID, &state.SemanticVersion, &state.ContentHash,
			&state.Version, &state.UpdatedAt,
		)
	})
	return state, err
}

func (store *PostgresStore) SaveSemanticReleaseValidation(
	ctx context.Context,
	tenantID, actorID, releaseID string,
	expectedVersion int64,
	validation SemanticReleaseValidation,
) (release SemanticRelease, err error) {
	summary, marshalErr := json.Marshal(validation)
	if marshalErr != nil {
		return release, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var currentVersion int64
		var currentStatus string
		if queryErr := tx.QueryRow(ctx, `SELECT version,status
			FROM platform.semantic_releases
			WHERE id=$1::uuid FOR UPDATE`, releaseID).Scan(
			&currentVersion, &currentStatus,
		); queryErr != nil {
			if errors.Is(queryErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return queryErr
		}
		if currentVersion != expectedVersion ||
			(currentStatus != "DRAFT" && currentStatus != "BLOCKED") {
			return ErrConflict
		}
		nextStatus, eventType := "PROJECTING", "VALIDATED"
		if validation.Status != "PASS" {
			nextStatus, eventType = "BLOCKED", "VALIDATION_BLOCKED"
		}
		if nextStatus == "PROJECTING" {
			if _, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_release_projections
				SET status='PENDING',applied_content_hash='',resource_version='',
					object_count=0,error_code='',detail='{}'::jsonb,
					version=version+1,started_at=NULL,completed_at=NULL
				WHERE release_id=$1::uuid`, releaseID); updateErr != nil {
				return updateErr
			}
		}
		if scanErr := scanSemanticRelease(tx.QueryRow(ctx, `UPDATE platform.semantic_releases AS release
			SET status=$1,validation_summary=$2,validated_at=now(),
				version=version+1,updated_by=$3::uuid
			WHERE id=$4::uuid
			RETURNING `+semanticReleaseColumns,
			nextStatus, summary, actorID, releaseID,
		), &release); scanErr != nil {
			return scanErr
		}
		if eventErr := insertSemanticReleaseEvent(
			ctx, tx, releaseID, eventType, actorID,
			map[string]any{"validation": validation},
		); eventErr != nil {
			return eventErr
		}
		projections, loadErr := loadSemanticReleaseProjections(ctx, tx, releaseID)
		release.Projections = projections
		return loadErr
	})
	return release, err
}

func (store *PostgresStore) ActivateSemanticRelease(
	ctx context.Context,
	tenantID, actorID, releaseID, evaluationSetID string,
	expectedVersion, expectedStateVersion int64,
) (state SemanticReleaseState, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var activeReleaseID string
		if queryErr := tx.QueryRow(ctx, `SELECT
				COALESCE(active_release_id::text,''),version
			FROM platform.semantic_release_state
			WHERE tenant_id=platform.current_tenant_id()
			FOR UPDATE`).Scan(&activeReleaseID, &state.Version); queryErr != nil {
			return queryErr
		}
		if state.Version != expectedStateVersion {
			return ErrConflict
		}
		var releaseVersion int64
		var releaseStatus, contentHash, semanticVersion string
		if queryErr := tx.QueryRow(ctx, `SELECT
				version,status,content_hash,semantic_version
			FROM platform.semantic_releases
			WHERE id=$1::uuid FOR UPDATE`, releaseID).Scan(
			&releaseVersion, &releaseStatus, &contentHash, &semanticVersion,
		); queryErr != nil {
			if errors.Is(queryErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return queryErr
		}
		if releaseVersion != expectedVersion {
			return ErrConflict
		}
		if releaseStatus != "READY" {
			return ErrReleaseNotReady
		}
		var invalidProjectionCount int
		if queryErr := tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.semantic_release_projections
			WHERE release_id=$1::uuid AND (
			  status<>'READY' OR expected_content_hash<>$2
			  OR applied_content_hash<>$2
			)`, releaseID, contentHash).Scan(&invalidProjectionCount); queryErr != nil {
			return queryErr
		}
		if invalidProjectionCount != 0 {
			return ErrReleaseNotReady
		}
		evaluationSetContentHash := ""
		if activeReleaseID != "" && activeReleaseID != releaseID && evaluationSetID == "" {
			return ErrReleaseNotReady
		}
		if evaluationSetID != "" {
			var gatePassed bool
			if queryErr := tx.QueryRow(ctx, `SELECT
				platform.semantic_evaluation_set_passes($1::uuid,$2,$3)
				AND platform.semantic_evaluation_security_set_passes($1::uuid,$2,$3),
				COALESCE((
				  SELECT sealed_content_hash
				  FROM platform.semantic_golden_question_sets
				  WHERE id=$1::uuid
				),'')`, evaluationSetID, semanticVersion, contentHash).Scan(
				&gatePassed, &evaluationSetContentHash,
			); queryErr != nil {
				return queryErr
			}
			if !gatePassed || evaluationSetContentHash == "" {
				return ErrReleaseNotReady
			}
		}
		if activeReleaseID != "" && activeReleaseID != releaseID {
			if _, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_releases
				SET status='SUPERSEDED',version=version+1,updated_by=$1::uuid
				WHERE id=$2::uuid AND status='ACTIVE'`, actorID, activeReleaseID); updateErr != nil {
				return updateErr
			}
			if eventErr := insertSemanticReleaseEvent(
				ctx, tx, activeReleaseID, "SUPERSEDED", actorID,
				map[string]any{"replacementReleaseId": releaseID},
			); eventErr != nil {
				return eventErr
			}
		}
		if _, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_releases
			SET status='ACTIVE',version=version+1,updated_by=$1::uuid,
				activated_by=$1::uuid,activated_at=now(),
				evaluation_set_id=NULLIF($3,'')::uuid,
				evaluation_set_content_hash=$4
			WHERE id=$2::uuid`, actorID, releaseID, evaluationSetID,
			evaluationSetContentHash); updateErr != nil {
			return updateErr
		}
		if eventErr := insertSemanticReleaseEvent(
			ctx, tx, releaseID, "ACTIVATED", actorID,
			map[string]any{
				"semanticVersion": semanticVersion, "contentHash": contentHash,
				"previousReleaseId":        activeReleaseID,
				"evaluationSetId":          evaluationSetID,
				"evaluationSetContentHash": evaluationSetContentHash,
			},
		); eventErr != nil {
			return eventErr
		}
		return tx.QueryRow(ctx, `UPDATE platform.semantic_release_state
			SET active_release_id=$1::uuid,version=version+1,updated_by=$2::uuid
			WHERE tenant_id=platform.current_tenant_id()
			RETURNING active_release_id::text,$3,$4,version,updated_at`,
			releaseID, actorID, semanticVersion, contentHash,
		).Scan(
			&state.ActiveReleaseID, &state.SemanticVersion, &state.ContentHash,
			&state.Version, &state.UpdatedAt,
		)
	})
	return state, err
}

func loadSemanticReleaseObjects(
	ctx context.Context,
	tx pgx.Tx,
	releaseID string,
) ([]SemanticReleaseObject, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,object_type,object_id,
		object_version,content_hash,domain_id,owner_id::text,certification,
		sensitivity,valid_from,valid_to,contract_json,created_at
		FROM platform.semantic_release_objects
		WHERE release_id=$1::uuid
		ORDER BY object_type,object_id,object_version`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SemanticReleaseObject{}
	for rows.Next() {
		var item SemanticReleaseObject
		var contract []byte
		if scanErr := rows.Scan(
			&item.ID, &item.ObjectType, &item.ObjectID, &item.ObjectVersion,
			&item.ContentHash, &item.DomainID, &item.OwnerID,
			&item.Certification, &item.Sensitivity, &item.ValidFrom,
			&item.ValidTo, &contract, &item.CreatedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		item.Contract = append(json.RawMessage(nil), contract...)
		result = append(result, item)
	}
	return result, rows.Err()
}

func loadSemanticReleaseProjections(
	ctx context.Context,
	tx pgx.Tx,
	releaseID string,
) ([]SemanticReleaseProjection, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,target,status,
		expected_content_hash,applied_content_hash,resource_version,object_count,
		error_code,detail,version,started_at,completed_at,updated_at
		FROM platform.semantic_release_projections
		WHERE release_id=$1::uuid ORDER BY target`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SemanticReleaseProjection{}
	for rows.Next() {
		var item SemanticReleaseProjection
		var detail []byte
		if scanErr := rows.Scan(
			&item.ID, &item.Target, &item.Status, &item.ExpectedContentHash,
			&item.AppliedContentHash, &item.ResourceVersion, &item.ObjectCount,
			&item.ErrorCode, &detail, &item.Version, &item.StartedAt,
			&item.CompletedAt, &item.UpdatedAt,
		); scanErr != nil {
			return nil, scanErr
		}
		item.Detail = append(json.RawMessage(nil), detail...)
		result = append(result, item)
	}
	return result, rows.Err()
}

func scanSemanticRelease(row pgx.Row, release *SemanticRelease, extra ...any) error {
	var summary []byte
	targets := []any{
		&release.ID, &release.SemanticVersion, &release.ContentHash,
		&release.Status, &release.BaseReleaseID, &release.Notes,
		&release.ObjectCount, &summary, &release.Version,
		&release.CreatedBy, &release.UpdatedBy, &release.ActivatedBy,
		&release.EvaluationSetID, &release.EvaluationSetContentHash,
		&release.CreatedAt, &release.UpdatedAt, &release.ValidatedAt,
		&release.ActivatedAt,
	}
	targets = append(targets, extra...)
	if err := row.Scan(targets...); err != nil {
		return err
	}
	release.ValidationSummary = append(json.RawMessage(nil), summary...)
	if release.Projections == nil {
		release.Projections = []SemanticReleaseProjection{}
	}
	return nil
}

func insertSemanticReleaseEvent(
	ctx context.Context,
	tx pgx.Tx,
	releaseID, eventType, actorID string,
	detail map[string]any,
) error {
	document, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.semantic_release_events(
			tenant_id,release_id,event_type,actor_id,detail
		) VALUES(
			platform.current_tenant_id(),$1::uuid,$2,NULLIF($3,'')::uuid,$4
		)`, releaseID, eventType, actorID, document)
	return err
}

var _ semanticReleaseStore = (*PostgresStore)(nil)
