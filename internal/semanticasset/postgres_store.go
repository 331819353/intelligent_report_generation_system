package semanticasset

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const assetColumns = `id::text,common_term::text,mapping_value,knowledge_type,
	domain_id::text,sharing_scope::text,
	status,version,embedding_status,embedding_model,embedding_error_code,
	embedded_at,created_by::text,updated_by::text,created_at,updated_at`

func (store *PostgresStore) List(
	ctx context.Context,
	tenantID string,
	filter Filter,
) (items []Asset, total int, err error) {
	items = []Asset{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+assetColumns+`,
				count(*) OVER()::int
			FROM platform.semantic_term_assets AS asset
			WHERE asset.tenant_id=platform.current_tenant_id()
			  AND (
			    $1=''
			    OR asset.common_term::text ILIKE '%'||$1||'%'
			    OR asset.mapping_value ILIKE '%'||$1||'%'
			    OR asset.knowledge_type ILIKE '%'||$1||'%'
			  )
			  AND ($2='' OR asset.knowledge_type=$2)
			  AND ($3='' OR asset.status=$3)
			  AND ($4='' OR asset.embedding_status=$4)
			ORDER BY asset.knowledge_type,lower(asset.common_term::text),asset.id
			LIMIT $5 OFFSET $6`,
			filter.Query, filter.KnowledgeType, filter.Status,
			filter.EmbeddingStatus, filter.Limit, filter.Offset,
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item Asset
			if scanErr := scanAsset(rows, &item, &total); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (store *PostgresStore) ListKnowledgeTypes(
	ctx context.Context,
	tenantID string,
) (items []string, err error) {
	items = []string{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT DISTINCT knowledge_type
			FROM platform.semantic_term_assets
			WHERE tenant_id=platform.current_tenant_id()
			ORDER BY knowledge_type`)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item string
			if scanErr := rows.Scan(&item); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (store *PostgresStore) Create(
	ctx context.Context,
	tenantID string,
	actorID string,
	input UpsertInput,
) (item Asset, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO platform.semantic_term_assets(
				tenant_id,common_term,mapping_value,knowledge_type,
				created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$4
			)
			RETURNING `+assetColumns,
			input.CommonTerm, input.MappingValue, input.KnowledgeType, actorID,
		)
		if scanErr := scanAsset(row, &item); scanErr != nil {
			return mapWriteError(scanErr)
		}
		return audit(
			ctx, tx, actorID, "SEMANTIC_ASSET_CREATE", item.ID,
			map[string]any{
				"commonTerm": item.CommonTerm, "knowledgeType": item.KnowledgeType,
				"version": item.Version,
			},
		)
	})
	return item, err
}

func (store *PostgresStore) Update(
	ctx context.Context,
	tenantID string,
	actorID string,
	id string,
	input UpdateInput,
) (item Asset, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var currentVersion int64
		var currentStatus string
		if queryErr := tx.QueryRow(ctx, `SELECT version,status
			FROM platform.semantic_term_assets
			WHERE id=$1::uuid
			FOR UPDATE`, id).Scan(&currentVersion, &currentStatus); queryErr != nil {
			if errors.Is(queryErr, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return queryErr
		}
		if currentVersion != input.ExpectedVersion || currentStatus != "ACTIVE" {
			return ErrConflict
		}
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_term_assets SET
				mapping_value=$1,
				knowledge_type=$2,
				common_term=$3,
				version=version+1,
				updated_by=$4,
				embedding=CASE
				  WHEN common_term IS DISTINCT FROM $3::citext THEN NULL
				  ELSE embedding
				END,
				embedding_model=CASE
				  WHEN common_term IS DISTINCT FROM $3::citext THEN ''
				  ELSE embedding_model
				END,
				embedding_input_hash=CASE
				  WHEN common_term IS DISTINCT FROM $3::citext THEN ''
				  ELSE embedding_input_hash
				END,
				embedding_status=CASE
				  WHEN common_term IS DISTINCT FROM $3::citext THEN 'PENDING'
				  ELSE embedding_status
				END,
				embedding_error_code=CASE
				  WHEN common_term IS DISTINCT FROM $3::citext THEN ''
				  ELSE embedding_error_code
				END,
				embedded_at=CASE
				  WHEN common_term IS DISTINCT FROM $3::citext THEN NULL
				  ELSE embedded_at
				END
			WHERE id=$5::uuid
			RETURNING `+assetColumns,
			input.MappingValue, input.KnowledgeType, input.CommonTerm,
			actorID, id,
		)
		if scanErr := scanAsset(row, &item); scanErr != nil {
			return mapWriteError(scanErr)
		}
		return audit(
			ctx, tx, actorID, "SEMANTIC_ASSET_UPDATE", item.ID,
			map[string]any{
				"previousVersion": currentVersion, "version": item.Version,
			},
		)
	})
	return item, err
}

func (store *PostgresStore) Deprecate(
	ctx context.Context,
	tenantID string,
	actorID string,
	id string,
	expectedVersion int64,
) (item Asset, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_term_assets SET
				status='DEPRECATED',
				version=version+1,
				updated_by=$1,
				embedding=NULL,
				embedding_model='',
				embedding_input_hash='',
				embedding_status='SKIPPED',
				embedding_error_code='ASSET_DEPRECATED',
				embedded_at=NULL
			WHERE id=$2::uuid AND version=$3 AND status='ACTIVE'
			RETURNING `+assetColumns, actorID, id, expectedVersion)
		if scanErr := scanAsset(row, &item); scanErr != nil {
			if !errors.Is(scanErr, pgx.ErrNoRows) {
				return mapWriteError(scanErr)
			}
			return classifyMissingOrConflict(ctx, tx, id)
		}
		return audit(
			ctx, tx, actorID, "SEMANTIC_ASSET_DEPRECATE", item.ID,
			map[string]any{
				"previousVersion": expectedVersion, "version": item.Version,
			},
		)
	})
	return item, err
}

func (store *PostgresStore) Import(
	ctx context.Context,
	tenantID string,
	actorID string,
	inputs []UpsertInput,
) (result ImportResult, err error) {
	result.Total = len(inputs)
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		for _, input := range inputs {
			var id, mappingValue, status string
			var version int64
			queryErr := tx.QueryRow(ctx, `SELECT id::text,mapping_value,status,version
				FROM platform.semantic_term_assets
				WHERE knowledge_type=$1 AND common_term=$2
				FOR UPDATE`,
				input.KnowledgeType, input.CommonTerm,
			).Scan(&id, &mappingValue, &status, &version)
			if errors.Is(queryErr, pgx.ErrNoRows) {
				if _, insertErr := tx.Exec(ctx, `INSERT INTO platform.semantic_term_assets(
						tenant_id,common_term,mapping_value,knowledge_type,
						created_by,updated_by
					) VALUES(
						platform.current_tenant_id(),$1,$2,$3,$4,$4
					)`,
					input.CommonTerm, input.MappingValue,
					input.KnowledgeType, actorID,
				); insertErr != nil {
					return mapWriteError(insertErr)
				}
				result.Inserted++
				continue
			}
			if queryErr != nil {
				return queryErr
			}
			if mappingValue == input.MappingValue && status == "ACTIVE" {
				result.Unchanged++
				continue
			}
			_, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_term_assets SET
					mapping_value=$1,
					status='ACTIVE',
					version=version+1,
					updated_by=$2,
					embedding=CASE WHEN status='DEPRECATED' THEN NULL ELSE embedding END,
					embedding_model=CASE WHEN status='DEPRECATED' THEN '' ELSE embedding_model END,
					embedding_input_hash=CASE WHEN status='DEPRECATED' THEN '' ELSE embedding_input_hash END,
					embedding_status=CASE WHEN status='DEPRECATED' THEN 'PENDING' ELSE embedding_status END,
					embedding_error_code=CASE WHEN status='DEPRECATED' THEN '' ELSE embedding_error_code END,
					embedded_at=CASE WHEN status='DEPRECATED' THEN NULL ELSE embedded_at END
				WHERE id=$3::uuid AND version=$4`,
				input.MappingValue, actorID, id, version,
			)
			if updateErr != nil {
				return mapWriteError(updateErr)
			}
			result.Updated++
		}
		return audit(
			ctx, tx, actorID, "SEMANTIC_ASSET_IMPORT", uuid.NewString(),
			map[string]any{
				"inserted": result.Inserted, "updated": result.Updated,
				"unchanged": result.Unchanged, "total": result.Total,
			},
		)
	})
	return result, err
}

func scanAsset(row pgx.Row, item *Asset, total ...*int) error {
	destinations := []any{
		&item.ID, &item.CommonTerm, &item.MappingValue,
		&item.KnowledgeType, &item.DomainID, &item.SharingScope,
		&item.Status, &item.Version,
		&item.EmbeddingStatus, &item.EmbeddingModel,
		&item.EmbeddingErrorCode, &item.EmbeddedAt,
		&item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt,
	}
	if len(total) > 0 {
		destinations = append(destinations, total[0])
	}
	return row.Scan(destinations...)
}

func classifyMissingOrConflict(
	ctx context.Context,
	tx pgx.Tx,
	id string,
) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.semantic_term_assets WHERE id=$1::uuid
	)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	return ErrNotFound
}

func audit(
	ctx context.Context,
	tx pgx.Tx,
	actorID string,
	action string,
	resourceID string,
	detail map[string]any,
) error {
	document, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES(
			platform.current_tenant_id(),$1,$2,'SEMANTIC_ASSET',$3,$4
		)`, actorID, action, resourceID, document)
	return err
}

func mapWriteError(err error) error {
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) {
		return err
	}
	switch databaseError.Code {
	case "23505":
		return ErrConflict
	case "23503", "23514", "22P02", "22001":
		return ErrInvalidRequest
	default:
		return err
	}
}
