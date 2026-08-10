package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type PublicationClaim struct {
	Identity   Identity
	ReportID   askdata.ID
	Version    Version
	LeaseToken askdata.ID
}

func (store *PostgresStore) PublicationTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("report store is unavailable")
	}
	rows, err := store.pool.Query(ctx, `SELECT DISTINCT tenant_id::text FROM platform.report_versions
		WHERE artifact_state<>'READY' AND artifact_attempt<20 ORDER BY tenant_id`)
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

func (store *PostgresStore) ClaimPublication(
	ctx context.Context, tenantID string, lease time.Duration,
) (*PublicationClaim, error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil ||
		lease < 30*time.Second || lease > 10*time.Minute {
		return nil, errors.New("invalid report publication claim")
	}
	var result *PublicationClaim
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH picked AS(
			SELECT id FROM platform.report_versions
			WHERE artifact_state<>'READY' AND artifact_attempt<20
			  AND artifact_next_attempt_at<=now()
			  AND (artifact_lease_expires_at IS NULL OR artifact_lease_expires_at<=now())
			ORDER BY artifact_next_attempt_at,published_at,id FOR UPDATE SKIP LOCKED LIMIT 1
		), claimed AS(
			UPDATE platform.report_versions version SET artifact_state='RETRY',
			  artifact_attempt=artifact_attempt+1,artifact_lease_token=gen_random_uuid(),
			  artifact_lease_expires_at=now()+($1*interval '1 second'),artifact_error_code=''
			FROM picked WHERE version.id=picked.id RETURNING version.*
		) SELECT claimed.id::text,claimed.report_id::text,claimed.version_no,
			claimed.source_revision_no,claimed.definition_json,claimed.definition_hash,
			claimed.schema_version,claimed.object_uri,claimed.published_by::text,claimed.published_at,
			claimed.rollback_of_version_no,COALESCE(claimed.rollback_reason,''),
			claimed.stale_insights_acknowledged,claimed.artifact_state,claimed.artifact_attempt,
			claimed.artifact_next_attempt_at,claimed.artifact_lease_token::text
			FROM claimed`, int64(lease/time.Second))
		var claim PublicationClaim
		var raw []byte
		claim.Identity.TenantID = askdata.ID(tenantID)
		if err := row.Scan(&claim.Version.ID, &claim.Version.ReportID, &claim.Version.VersionNo,
			&claim.Version.SourceRevisionNo, &raw, &claim.Version.DefinitionHash,
			&claim.Version.SchemaVersion, &claim.Version.ObjectURI, &claim.Version.PublishedBy,
			&claim.Version.PublishedAt, &claim.Version.RollbackOfVersionNo,
			&claim.Version.RollbackReason, &claim.Version.StaleInsightsAcknowledged,
			&claim.Version.ArtifactState, &claim.Version.ArtifactAttempt,
			&claim.Version.ArtifactNextAttemptAt, &claim.LeaseToken); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		claim.Version.DefinitionRaw = json.RawMessage(raw)
		if err := hydrateStoredDefinition(raw, claim.Version.DefinitionHash, &claim.Version.Definition, &claim.Version.DefinitionRaw); err != nil {
			return err
		}
		claim.ReportID = claim.Version.ReportID
		claim.Identity.ActorID = claim.Version.PublishedBy
		result = &claim
		return nil
	})
	return result, err
}

func (store *PostgresStore) FailPublicationClaim(ctx context.Context, claim PublicationClaim, cause error) error {
	if claim.Identity.TenantID.Validate() != nil || claim.Version.ID.Validate() != nil || claim.LeaseToken.Validate() != nil {
		return errors.New("invalid report publication claim")
	}
	return database.WithTenantTx(ctx, store.pool, string(claim.Identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE platform.report_versions SET
			artifact_state='RETRY',artifact_next_attempt_at=now()+
			  (LEAST(300,power(2,artifact_attempt)::integer)*interval '1 second'),
			artifact_lease_token=NULL,artifact_lease_expires_at=NULL,
			artifact_error_code='REPORT_ARTIFACT_PROMOTE_FAILED'
			WHERE id=$1 AND artifact_state='RETRY' AND artifact_lease_token=$2`,
			claim.Version.ID, claim.LeaseToken)
		if err != nil || command.RowsAffected() != 1 {
			return errors.Join(err, ErrNotFound)
		}
		return nil
	})
}

func (store *PostgresStore) CompletePublicationClaim(ctx context.Context, claim PublicationClaim) error {
	if claim.Identity.TenantID.Validate() != nil || claim.ReportID.Validate() != nil ||
		claim.Version.ID.Validate() != nil || claim.LeaseToken.Validate() != nil {
		return errors.New("invalid report publication claim")
	}
	return database.WithTenantTx(ctx, store.pool, string(claim.Identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE platform.report_versions SET artifact_state='READY',
			artifact_lease_token=NULL,artifact_lease_expires_at=NULL,artifact_error_code='',
			artifact_next_attempt_at=now()
			WHERE id=$1 AND report_id=$2 AND artifact_state='RETRY' AND artifact_lease_token=$3`,
			claim.Version.ID, claim.ReportID, claim.LeaseToken)
		if err != nil || command.RowsAffected() != 1 {
			return errors.Join(err, ErrNotFound)
		}
		_, err = tx.Exec(ctx, `UPDATE platform.reports AS report SET current_published_version_id=$1
			WHERE report.id=$2 AND report.tenant_id=$3 AND (
			  report.current_published_version_id IS NULL OR
			  (SELECT current.version_no FROM platform.report_versions current
			   WHERE current.id=report.current_published_version_id)<$4
			)`, claim.Version.ID, claim.ReportID, claim.Identity.TenantID, claim.Version.VersionNo)
		return err
	})
}
