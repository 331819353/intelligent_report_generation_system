package sharing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/store"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (repository *PostgresRepository) Create(ctx context.Context, identity store.Identity, record Record) error {
	var err error
	ctx, err = repository.requestContext(ctx, identity, record.ID, record.ReportID)
	if err != nil {
		return err
	}
	if record.TenantID != identity.TenantID || record.CreatedBy != identity.ActorID ||
		record.ReportVersionID != "" && !validPostgresUUID(record.ReportVersionID) {
		return errors.New("share record identity does not match actor")
	}
	var snapshot any
	if record.FilterSnapshot != nil {
		encoded, err := json.Marshal(record.FilterSnapshot)
		if err != nil {
			return err
		}
		snapshot = encoded
	}
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.report_shares(
			id,tenant_id,report_id,report_version_id,share_type,principal_id,share_token_hash,
			filter_snapshot_json,created_by,created_at,expires_at
		) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11)`, record.ID,
			record.TenantID, record.ReportID, record.ReportVersionID, record.Type, record.PrincipalID,
			record.TokenHash, snapshot, record.CreatedBy, record.CreatedAt, record.ExpiresAt)
		return err
	})
}

func (repository *PostgresRepository) FindByTokenHash(ctx context.Context, identity store.Identity, hash string) (Record, error) {
	var err error
	ctx, err = repository.requestContext(ctx, identity)
	if err != nil || len(hash) != 64 {
		return Record{}, errors.Join(err, errors.New("invalid share lookup"))
	}
	var result Record
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var snapshot []byte
		err := tx.QueryRow(ctx, `SELECT share.id::text,share.tenant_id::text,share.report_id::text,
			COALESCE(share.report_version_id::text,''),version.version_no,share.share_type,share.principal_id::text,share.share_token_hash,
			COALESCE(share.filter_snapshot_json,'null'::jsonb),share.created_by::text,
			share.created_at,share.expires_at,share.expired_at,share.revoked_at
			FROM platform.report_shares AS share LEFT JOIN platform.report_versions AS version
			  ON version.id=share.report_version_id AND version.report_id=share.report_id
			WHERE share.share_token_hash=$1`, hash).Scan(&result.ID,
			&result.TenantID, &result.ReportID, &result.ReportVersionID, &result.ReportVersionNo, &result.Type,
			&result.PrincipalID, &result.TokenHash, &snapshot, &result.CreatedBy,
			&result.CreatedAt, &result.ExpiresAt, &result.ExpiredAt, &result.RevokedAt)
		if err != nil {
			return err
		}
		if string(snapshot) != "null" {
			return json.Unmarshal(snapshot, &result.FilterSnapshot)
		}
		return nil
	})
	return result, err
}

func (repository *PostgresRepository) ListCreated(ctx context.Context, identity store.Identity, reportID askdata.ID, limit int) ([]Record, error) {
	ctx, err := repository.requestContext(ctx, identity, reportID)
	if err != nil || limit < 1 || limit > 200 {
		return nil, errors.Join(err, errors.New("invalid share list request"))
	}
	items := []Record{}
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT id::text,tenant_id::text,report_id::text,
			COALESCE(report_version_id::text,''),share_type,principal_id::text,
			COALESCE(filter_snapshot_json,'null'::jsonb),created_by::text,created_at,expires_at,expired_at,revoked_at
			FROM platform.report_shares WHERE tenant_id=$1 AND report_id=$2 AND created_by=$3
			ORDER BY created_at DESC,id DESC LIMIT $4`, identity.TenantID, reportID, identity.ActorID, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item Record
			var snapshot []byte
			if scanErr := rows.Scan(&item.ID, &item.TenantID, &item.ReportID, &item.ReportVersionID,
				&item.Type, &item.PrincipalID, &snapshot, &item.CreatedBy, &item.CreatedAt,
				&item.ExpiresAt, &item.ExpiredAt, &item.RevokedAt); scanErr != nil {
				return scanErr
			}
			if string(snapshot) != "null" {
				if decodeErr := json.Unmarshal(snapshot, &item.FilterSnapshot); decodeErr != nil {
					return decodeErr
				}
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (repository *PostgresRepository) Revoke(ctx context.Context, identity store.Identity, id askdata.ID, now time.Time) error {
	var err error
	ctx, err = repository.requestContext(ctx, identity, id)
	if err != nil || now.IsZero() {
		return errors.Join(err, errors.New("invalid share revocation"))
	}
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE platform.report_shares SET revoked_at=$1
			WHERE id=$2 AND created_by=$3 AND revoked_at IS NULL`, now, id, identity.ActorID)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (repository *PostgresRepository) RecordAccess(ctx context.Context, identity store.Identity, id askdata.ID, now time.Time) error {
	var err error
	ctx, err = repository.requestContext(ctx, identity, id)
	if err != nil || now.IsZero() {
		return errors.Join(err, errors.New("invalid share access"))
	}
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		command, err := tx.Exec(ctx, `UPDATE platform.report_shares
			SET access_count=access_count+1,last_accessed_at=clock_timestamp()
			WHERE id=$1 AND revoked_at IS NULL AND expired_at IS NULL
			  AND expires_at>clock_timestamp()`, id)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return store.ErrNotFound
		}
		return nil
	})
}

func (repository *PostgresRepository) requestContext(
	ctx context.Context, identity store.Identity, ids ...askdata.ID,
) (context.Context, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil {
		return nil, errors.New("share repository is unavailable or identity is invalid")
	}
	for _, id := range ids {
		if !validPostgresUUID(id) {
			return nil, fmt.Errorf("invalid share repository ID %q", id)
		}
	}
	return database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID)), nil
}

func validPostgresUUID(value askdata.ID) bool {
	_, err := uuid.Parse(string(value))
	return err == nil
}
