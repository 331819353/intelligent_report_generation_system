package semanticmanagement

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct {
	pool                       *pgxpool.Pool
	warehousePool              *pgxpool.Pool
	dimensionRefreshScanHook   func(context.Context) error
	dimensionRefreshCommitHook func(context.Context) error
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool, warehousePool: pool}
}

func NewPostgresStoreWithWarehouse(
	controlPool, warehousePool *pgxpool.Pool,
) *PostgresStore {
	return &PostgresStore{pool: controlPool, warehousePool: warehousePool}
}

const tagColumns = `id::text,COALESCE(parent_tag_id::text,''),code::text,name,description,
	category,governance,status,version,created_by::text,updated_by::text,created_at,updated_at`

func (s *PostgresStore) ListTags(ctx context.Context, tenantID string, filter TagFilter) (items []Tag, total int, err error) {
	items = []Tag{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+tagColumns+`,count(*) OVER()::int
			FROM platform.semantic_tags AS tag
			WHERE tag.tenant_id=platform.current_tenant_id()
			  AND ($1='' OR tag.code::text ILIKE '%'||$1||'%' OR tag.name ILIKE '%'||$1||'%'
			    OR EXISTS(SELECT 1 FROM platform.semantic_tag_aliases AS alias
			      WHERE alias.tenant_id=tag.tenant_id AND alias.tag_id=tag.id
			        AND alias.alias::text ILIKE '%'||$1||'%'))
			  AND ($2='' OR tag.category=$2)
			  AND ($3='' OR tag.status=$3)
			ORDER BY tag.category,tag.code::text,tag.id
			LIMIT $4 OFFSET $5`, filter.Query, filter.Category, filter.Status, filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item Tag
			if err := scanTag(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) CreateTag(ctx context.Context, tenantID, actorID string, input CreateTagInput) (item Tag, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := lockLexeme(ctx, tx, input.Code); err != nil {
			return err
		}
		conflict, err := aliasValueExists(ctx, tx, input.Code, "")
		if err != nil {
			return err
		}
		if conflict {
			return ErrConflict
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.semantic_tags(
				tenant_id,parent_tag_id,code,name,description,category,governance,status,created_by,updated_by
			) VALUES(platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$8)
			RETURNING `+tagColumns,
			nullableUUID(input.ParentTagID), input.Code, input.Name, input.Description,
			input.Category, input.Governance, input.Status, actorID)
		if err := scanTag(row, &item); err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_TAG_CREATE", "SEMANTIC_TAG", item.ID,
			map[string]any{"version": item.Version})
	})
	return item, err
}

func (s *PostgresStore) UpdateTag(ctx context.Context, tenantID, actorID, id string, input UpdateTagInput) (item Tag, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := lockTagTaxonomy(ctx, tx); err != nil {
			return err
		}
		var currentVersion int64
		var currentStatus string
		err := tx.QueryRow(ctx, `SELECT version,status FROM platform.semantic_tags
			WHERE id=$1::uuid FOR UPDATE`, id).Scan(&currentVersion, &currentStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentVersion != input.ExpectedVersion || currentStatus == "DEPRECATED" {
			return ErrConflict
		}
		if input.ParentTagID != "" {
			var cyclic bool
			if err := tx.QueryRow(ctx, `WITH RECURSIVE ancestors(id,parent_tag_id) AS (
					SELECT id,parent_tag_id FROM platform.semantic_tags WHERE id=$1::uuid
					UNION ALL
					SELECT parent.id,parent.parent_tag_id
					FROM platform.semantic_tags AS parent
					JOIN ancestors AS child ON parent.id=child.parent_tag_id
				) SELECT EXISTS(SELECT 1 FROM ancestors WHERE id=$2::uuid)`,
				input.ParentTagID, id).Scan(&cyclic); err != nil {
				return err
			}
			if cyclic {
				return ErrInvalidRequest
			}
		}
		if err := lockLexeme(ctx, tx, input.Code); err != nil {
			return err
		}
		conflict, err := aliasValueExists(ctx, tx, input.Code, "")
		if err != nil {
			return err
		}
		if conflict {
			return ErrConflict
		}
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_tags SET
				parent_tag_id=$1,code=$2,name=$3,description=$4,category=$5,
				governance=$6,status=$7,version=version+1,updated_by=$8
			WHERE id=$9::uuid
			RETURNING `+tagColumns,
			nullableUUID(input.ParentTagID), input.Code, input.Name, input.Description,
			input.Category, input.Governance, input.Status, actorID, id)
		if err := scanTag(row, &item); err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_TAG_UPDATE", "SEMANTIC_TAG", item.ID,
			map[string]any{"previousVersion": currentVersion, "version": item.Version})
	})
	return item, err
}

func (s *PostgresStore) DeprecateTag(ctx context.Context, tenantID, actorID, id string, expectedVersion int64) (item Tag, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_tags SET
				status='DEPRECATED',version=version+1,updated_by=$1
			WHERE id=$2::uuid AND version=$3 AND status<>'DEPRECATED'
			RETURNING `+tagColumns, actorID, id, expectedVersion)
		if err := scanTag(row, &item); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return mapWriteError(err)
			}
			return classifyMissingOrConflict(ctx, tx, "platform.semantic_tags", id)
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_TAG_DEPRECATE", "SEMANTIC_TAG", item.ID,
			map[string]any{"previousVersion": expectedVersion, "version": item.Version})
	})
	return item, err
}

const aliasColumns = `id::text,tag_id::text,alias::text,alias_type,language_code,
	created_by::text,created_at,xmin::text`

func (s *PostgresStore) ListTagAliases(ctx context.Context, tenantID string, filter AliasFilter) (items []TagAlias, total int, err error) {
	items = []TagAlias{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+aliasColumns+`,count(*) OVER()::int
			FROM platform.semantic_tag_aliases
			WHERE tenant_id=platform.current_tenant_id()
			  AND ($1='' OR tag_id::text=$1)
			  AND ($2='' OR alias::text ILIKE '%'||$2||'%')
			  AND ($3='' OR alias_type=$3)
			ORDER BY alias::text,id
			LIMIT $4 OFFSET $5`, filter.TagID, filter.Query, filter.AliasType, filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item TagAlias
			if err := scanAlias(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) CreateTagAlias(ctx context.Context, tenantID, actorID string, input CreateTagAliasInput) (item TagAlias, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := requireWritableTag(ctx, tx, input.TagID); err != nil {
			return err
		}
		if err := lockLexeme(ctx, tx, input.Alias); err != nil {
			return err
		}
		conflict, err := tagCodeExists(ctx, tx, input.Alias)
		if err != nil {
			return err
		}
		if conflict {
			return ErrConflict
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.semantic_tag_aliases(
				tenant_id,tag_id,alias,alias_type,language_code,created_by
			) VALUES(platform.current_tenant_id(),$1,$2,$3,$4,$5)
			RETURNING `+aliasColumns,
			input.TagID, input.Alias, input.AliasType, input.LanguageCode, actorID)
		if err := scanAlias(row, &item); err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_TAG_ALIAS_CREATE", "SEMANTIC_TAG_ALIAS", item.ID,
			map[string]any{"tagId": item.TagID, "aliasType": item.AliasType})
	})
	return item, err
}

func (s *PostgresStore) UpdateTagAlias(ctx context.Context, tenantID, actorID, id string, input UpdateTagAliasInput) (item TagAlias, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		var currentTagID, recordVersion string
		err := tx.QueryRow(ctx, `SELECT tag_id::text,xmin::text
			FROM platform.semantic_tag_aliases WHERE id=$1::uuid FOR UPDATE`, id).
			Scan(&currentTagID, &recordVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if recordVersion != input.ExpectedRecordVersion || currentTagID != input.TagID {
			return ErrConflict
		}
		if err := requireWritableTag(ctx, tx, input.TagID); err != nil {
			return err
		}
		if err := lockLexeme(ctx, tx, input.Alias); err != nil {
			return err
		}
		conflict, err := tagCodeExists(ctx, tx, input.Alias)
		if err != nil {
			return err
		}
		if conflict {
			return ErrConflict
		}
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_tag_aliases SET
				alias=$1,alias_type=$2,language_code=$3
			WHERE id=$4::uuid AND xmin=$5::xid
			RETURNING `+aliasColumns,
			input.Alias, input.AliasType, input.LanguageCode, id, input.ExpectedRecordVersion)
		if err := scanAlias(row, &item); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrConflict
			}
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_TAG_ALIAS_UPDATE", "SEMANTIC_TAG_ALIAS", item.ID,
			map[string]any{"tagId": item.TagID, "aliasType": item.AliasType})
	})
	return item, err
}

func (s *PostgresStore) DeleteTagAlias(ctx context.Context, tenantID, actorID, id, expectedRecordVersion string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		var tagID string
		err := tx.QueryRow(ctx, `DELETE FROM platform.semantic_tag_aliases
			WHERE id=$1::uuid AND xmin=$2::xid RETURNING tag_id::text`,
			id, expectedRecordVersion).Scan(&tagID)
		if errors.Is(err, pgx.ErrNoRows) {
			return classifyMissingOrConflict(ctx, tx, "platform.semantic_tag_aliases", id)
		}
		if err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "SEMANTIC_TAG_ALIAS_DELETE", "SEMANTIC_TAG_ALIAS", id,
			map[string]any{"tagId": tagID})
	})
}

const bindingColumns = `id::text,tag_id::text,asset_type,
	COALESCE(dataset_id::text,''),COALESCE(dataset_version_id::text,''),COALESCE(dataset_field_id,''),
	COALESCE(dimension_id::text,''),COALESCE(dimension_member_id::text,''),
	COALESCE(metric_id::text,''),COALESCE(metric_version_id::text,''),
	COALESCE(metric_dataset_version_id::text,''),origin,status,confidence,evidence_json,
	COALESCE(assigned_by::text,''),COALESCE(approved_by::text,''),approved_at,
	created_at,updated_at,xmin::text`

func (s *PostgresStore) ListAssetTagBindings(ctx context.Context, tenantID string, filter BindingFilter) (items []AssetTagBinding, total int, err error) {
	items = []AssetTagBinding{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+bindingColumns+`,count(*) OVER()::int
			FROM platform.asset_tag_bindings
			WHERE tenant_id=platform.current_tenant_id()
			  AND ($1='' OR tag_id::text=$1)
			  AND ($2='' OR asset_type=$2)
			  AND ($3='' OR status=$3)
			ORDER BY updated_at DESC,id
			LIMIT $4 OFFSET $5`, filter.TagID, filter.AssetType, filter.Status, filter.Limit, filter.Offset)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item AssetTagBinding
			if err := scanBinding(rows, &item, &total); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) CreateAssetTagBinding(ctx context.Context, tenantID, actorID string, input CreateAssetTagBindingInput) (item AssetTagBinding, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		if err := requireWritableTag(ctx, tx, input.TagID); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `INSERT INTO platform.asset_tag_bindings(
				tenant_id,tag_id,asset_type,dataset_id,dataset_version_id,dataset_field_id,
				dimension_id,dimension_member_id,metric_id,metric_version_id,metric_dataset_version_id,
				origin,status,confidence,evidence_json,assigned_by,approved_by,approved_at
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
				$11,$12,$13,$14,$15,
				CASE WHEN $12='APPROVED' THEN $15::uuid ELSE NULL END,
				CASE WHEN $12='APPROVED' THEN now() ELSE NULL END
			) RETURNING `+bindingColumns,
			input.TagID, input.AssetType, nullableUUID(input.DatasetID),
			nullableUUID(input.DatasetVersionID), nullableText(input.DatasetFieldID),
			nullableUUID(input.DimensionID), nullableUUID(input.DimensionMemberID),
			nullableUUID(input.MetricID), nullableUUID(input.MetricVersionID),
			nullableUUID(input.MetricDatasetVersionID), input.Origin, input.Status,
			input.Confidence, input.Evidence, actorID)
		if err := scanBinding(row, &item); err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "ASSET_TAG_BINDING_CREATE", "ASSET_TAG_BINDING", item.ID,
			map[string]any{"tagId": item.TagID, "assetType": item.AssetType, "status": item.Status})
	})
	return item, err
}

func (s *PostgresStore) UpdateAssetTagBinding(ctx context.Context, tenantID, actorID, id string, input UpdateAssetTagBindingInput) (item AssetTagBinding, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `UPDATE platform.asset_tag_bindings SET
				origin=$1,status=$2,confidence=$3,evidence_json=$4,assigned_by=$5,
				approved_by=CASE WHEN $2='APPROVED' THEN $5::uuid ELSE NULL END,
				approved_at=CASE WHEN $2='APPROVED' THEN now() ELSE NULL END
			WHERE id=$6::uuid AND xmin=$7::xid
			RETURNING `+bindingColumns,
			input.Origin, input.Status, input.Confidence, input.Evidence, actorID, id,
			input.ExpectedRecordVersion)
		if err := scanBinding(row, &item); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return mapWriteError(err)
			}
			return classifyMissingOrConflict(ctx, tx, "platform.asset_tag_bindings", id)
		}
		return auditMutation(ctx, tx, actorID, "ASSET_TAG_BINDING_UPDATE", "ASSET_TAG_BINDING", item.ID,
			map[string]any{"tagId": item.TagID, "assetType": item.AssetType, "status": item.Status})
	})
	return item, err
}

func (s *PostgresStore) DeleteAssetTagBinding(ctx context.Context, tenantID, actorID, id, expectedRecordVersion string) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockSemanticGovernanceWriteGate(ctx, tx); err != nil {
			return err
		}
		var tagID, assetType string
		err := tx.QueryRow(ctx, `DELETE FROM platform.asset_tag_bindings
			WHERE id=$1::uuid AND xmin=$2::xid
			RETURNING tag_id::text,asset_type`, id, expectedRecordVersion).Scan(&tagID, &assetType)
		if errors.Is(err, pgx.ErrNoRows) {
			return classifyMissingOrConflict(ctx, tx, "platform.asset_tag_bindings", id)
		}
		if err != nil {
			return mapWriteError(err)
		}
		return auditMutation(ctx, tx, actorID, "ASSET_TAG_BINDING_DELETE", "ASSET_TAG_BINDING", id,
			map[string]any{"tagId": tagID, "assetType": assetType})
	})
}

type scanner interface{ Scan(...any) error }

func scanTag(row scanner, item *Tag, extra ...any) error {
	targets := []any{
		&item.ID, &item.ParentTagID, &item.Code, &item.Name, &item.Description,
		&item.Category, &item.Governance, &item.Status, &item.Version,
		&item.CreatedBy, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt,
	}
	return row.Scan(append(targets, extra...)...)
}

func scanAlias(row scanner, item *TagAlias, extra ...any) error {
	targets := []any{
		&item.ID, &item.TagID, &item.Alias, &item.AliasType, &item.LanguageCode,
		&item.CreatedBy, &item.CreatedAt, &item.RecordVersion,
	}
	return row.Scan(append(targets, extra...)...)
}

func scanBinding(row scanner, item *AssetTagBinding, extra ...any) error {
	targets := []any{
		&item.ID, &item.TagID, &item.AssetType,
		&item.DatasetID, &item.DatasetVersionID, &item.DatasetFieldID,
		&item.DimensionID, &item.DimensionMemberID, &item.MetricID,
		&item.MetricVersionID, &item.MetricDatasetVersionID,
		&item.Origin, &item.Status, &item.Confidence, &item.Evidence,
		&item.AssignedBy, &item.ApprovedBy, &item.ApprovedAt,
		&item.CreatedAt, &item.UpdatedAt, &item.RecordVersion,
	}
	return row.Scan(append(targets, extra...)...)
}

func requireWritableTag(ctx context.Context, tx pgx.Tx, id string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM platform.semantic_tags WHERE id=$1::uuid`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "DEPRECATED" {
		return ErrConflict
	}
	return nil
}

func aliasValueExists(ctx context.Context, tx pgx.Tx, value, excludeID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.semantic_tag_aliases
		WHERE alias=$1 AND ($2='' OR id::text<>$2)
	)`, value, excludeID).Scan(&exists)
	return exists, err
}

func tagCodeExists(ctx context.Context, tx pgx.Tx, value string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.semantic_tags WHERE code=$1
	)`, value).Scan(&exists)
	return exists, err
}

func lockLexeme(ctx context.Context, tx pgx.Tx, value string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
		platform.current_tenant_id()::text||':'||lower($1),0
	))`, value)
	return err
}

func lockTagTaxonomy(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
		'semantic-taxonomy:'||platform.current_tenant_id()::text,0
	))`)
	return err
}

func classifyMissingOrConflict(ctx context.Context, tx pgx.Tx, table, id string) error {
	allowed := map[string]bool{
		"platform.semantic_tags":                  true,
		"platform.semantic_tag_aliases":           true,
		"platform.asset_tag_bindings":             true,
		"platform.semantic_dimensions":            true,
		"platform.dimension_member_aliases":       true,
		"platform.dimension_metric_compatibility": true,
	}
	if !allowed[table] {
		return ErrConflict
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1::uuid)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	return ErrNotFound
}

func auditMutation(ctx context.Context, tx pgx.Tx, actorID, action, resourceType, resourceID string, detail map[string]any) error {
	document, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES(platform.current_tenant_id(),$1,$2,$3,$4,$5)`,
		actorID, action, resourceType, resourceID, document)
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

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
