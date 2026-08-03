package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) LoadQuestionSemanticSnapshot(
	ctx context.Context,
	tenantID, actorID string,
	effectiveAt time.Time,
) (snapshot QuestionSemanticSnapshot, err error) {
	snapshot = QuestionSemanticSnapshot{
		TenantID: tenantID, RoleCodes: []string{}, Purpose: "analytics",
		EffectiveAt: effectiveAt.UTC(), Objects: []QuestionSemanticObject{},
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		queryErr := tx.QueryRow(ctx, `SELECT release.id::text,
				release.semantic_version,release.content_hash
			FROM platform.semantic_release_state AS state
			JOIN platform.semantic_releases AS release
			  ON release.tenant_id=state.tenant_id
			 AND release.id=state.active_release_id
			JOIN platform.semantic_release_projections AS projection
			  ON projection.tenant_id=release.tenant_id
			 AND projection.release_id=release.id
			 AND projection.target='NEBULA_GRAPH'
			WHERE state.tenant_id=platform.current_tenant_id()
			  AND release.status='ACTIVE'
			  AND projection.status='READY'
			  AND projection.expected_content_hash=release.content_hash
			  AND projection.applied_content_hash=release.content_hash
			  AND projection.resource_version<>''`,
		).Scan(&snapshot.ReleaseID, &snapshot.SemanticVersion, &snapshot.ContentHash)
		if errors.Is(queryErr, pgx.ErrNoRows) {
			return ErrGraphNotReady
		}
		if queryErr != nil {
			return queryErr
		}

		roleRows, roleErr := tx.Query(ctx, `SELECT role.code
			FROM platform.user_roles AS assignment
			JOIN platform.roles AS role
			  ON role.tenant_id=assignment.tenant_id
			 AND role.id=assignment.role_id
			WHERE assignment.tenant_id=platform.current_tenant_id()
			  AND assignment.user_id=$1::uuid
			  AND role.status='ACTIVE' AND role.deleted_at IS NULL
			ORDER BY role.code`, actorID)
		if roleErr != nil {
			return roleErr
		}
		for roleRows.Next() {
			var roleCode string
			if scanErr := roleRows.Scan(&roleCode); scanErr != nil {
				roleRows.Close()
				return scanErr
			}
			snapshot.RoleCodes = append(snapshot.RoleCodes, roleCode)
		}
		if rowsErr := roleRows.Err(); rowsErr != nil {
			roleRows.Close()
			return rowsErr
		}
		roleRows.Close()

		rows, rowsErr := tx.Query(ctx, `SELECT object_type,object_id,
			object_version,domain_id,content_hash,contract_json
			FROM platform.semantic_release_objects
			WHERE release_id=$1::uuid AND certification='CERTIFIED'
			  AND valid_from<=$2::timestamptz
			  AND (valid_to IS NULL OR valid_to>$2::timestamptz)
			ORDER BY object_type,object_id,object_version`, snapshot.ReleaseID, snapshot.EffectiveAt)
		if rowsErr != nil {
			return rowsErr
		}
		defer rows.Close()
		for rows.Next() {
			var object QuestionSemanticObject
			var contractJSON []byte
			if scanErr := rows.Scan(
				&object.ObjectType, &object.ObjectID, &object.ObjectVersion,
				&object.DomainID, &object.ContentHash, &contractJSON,
			); scanErr != nil {
				return scanErr
			}
			if decodeErr := json.Unmarshal(contractJSON, &object.Contract); decodeErr != nil || object.Contract == nil {
				return ErrUnprovenPath
			}
			snapshot.Objects = append(snapshot.Objects, object)
		}
		if rows.Err() != nil {
			return rows.Err()
		}
		if len(snapshot.Objects) == 0 || len(snapshot.RoleCodes) == 0 {
			return ErrUnprovenPath
		}
		return nil
	})
	return snapshot, err
}

func (store *PostgresStore) IsQuestionSemanticSnapshotCurrent(
	ctx context.Context,
	tenantID, releaseID, semanticVersion, contentHash string,
) (current bool, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.semantic_release_state AS state
			JOIN platform.semantic_releases AS release
			  ON release.tenant_id=state.tenant_id
			 AND release.id=state.active_release_id
			JOIN platform.semantic_release_projections AS projection
			  ON projection.tenant_id=release.tenant_id
			 AND projection.release_id=release.id
			 AND projection.target='NEBULA_GRAPH'
			WHERE state.tenant_id=platform.current_tenant_id()
			  AND release.id=$1::uuid
			  AND release.status='ACTIVE'
			  AND release.semantic_version=$2
			  AND release.content_hash=$3
			  AND projection.status='READY'
			  AND projection.expected_content_hash=$3
			  AND projection.applied_content_hash=$3
			  AND projection.resource_version<>''
		)`, releaseID, semanticVersion, contentHash).Scan(&current)
	})
	return current, err
}
