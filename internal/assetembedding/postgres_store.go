package assetembedding

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/semanticquality"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) ListTenantIDs(ctx context.Context) ([]string, error) {
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

func (s *PostgresStore) ClaimBatch(ctx context.Context, tenantID, workerID string, lease time.Duration, limit int) (claims []Claim, err error) {
	if tenantID == "" || workerID == "" || lease < time.Second || limit < 1 || limit > 32 {
		return nil, ErrInvalidRequest
	}
	claims = []Claim{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.asset_embedding_outbox SET
			status='FAILED',error_code='LEASE_EXPIRED',lease_owner='',lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now() AND attempt>=3`); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH picked AS (
			SELECT id FROM platform.asset_embedding_outbox
			WHERE attempt<3 AND (
			  (status IN ('PENDING','FAILED') AND next_attempt_at<=now())
			  OR (status='RUNNING' AND lease_expires_at<=now())
			) ORDER BY updated_at,id FOR UPDATE SKIP LOCKED LIMIT $1
		) UPDATE platform.asset_embedding_outbox AS event SET
			status='RUNNING',attempt=attempt+1,error_code='',lease_owner=$2,
			lease_expires_at=now()+($3*interval '1 second'),completed_at=NULL,updated_at=now()
		FROM picked WHERE event.id=picked.id
		RETURNING event.id::text,event.asset_type,event.asset_id::text,event.table_id::text,event.event_version`,
			limit, workerID, int64(lease/time.Second))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var claim Claim
			claim.TenantID = tenantID
			if err := rows.Scan(&claim.ID, &claim.AssetType, &claim.AssetID, &claim.TableID, &claim.EventVersion); err != nil {
				return err
			}
			claims = append(claims, claim)
		}
		return rows.Err()
	})
	return claims, err
}

func (s *PostgresStore) Prepare(ctx context.Context, claim Claim, model string) (document Document, err error) {
	document.Claim = claim
	if claim.ID == "" || claim.TenantID == "" || claim.AssetID == "" || claim.TableID == "" ||
		(claim.AssetType != "TABLE" && claim.AssetType != "COLUMN") || strings.TrimSpace(model) == "" {
		return Document{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		var facts tableFacts
		var tableStatus, managementStatus, tableStructureHash, enrichedTableHash, structureHash, enrichedStructureHash, sourceStatus string
		err := tx.QueryRow(ctx, `SELECT source.source_type::text,table_asset.catalog_name,table_asset.schema_name,
			table_asset.table_name,table_asset.business_name,table_asset.business_description,table_asset.tags,
			table_asset.asset_status::text,table_asset.management_status,table_asset.table_structure_hash,
			table_asset.last_enriched_table_structure_hash,table_asset.structure_hash,
			table_asset.last_enriched_structure_hash,source.status::text
		FROM platform.metadata_tables AS table_asset
		JOIN platform.data_sources AS source
		  ON source.tenant_id=table_asset.tenant_id AND source.id=table_asset.data_source_id
		WHERE table_asset.id=$1 AND source.deleted_at IS NULL`, claim.TableID).Scan(
			&facts.SourceType, &facts.CatalogName, &facts.SchemaName, &facts.TableName,
			&facts.BusinessName, &facts.BusinessDescription, &facts.Tags, &tableStatus, &managementStatus,
			&tableStructureHash, &enrichedTableHash, &structureHash, &enrichedStructureHash, &sourceStatus,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			document.IneligibleCode = "ASSET_NOT_FOUND"
			return nil
		}
		if err != nil {
			return err
		}
		if tableStatus != "ACTIVE" || managementStatus != "ENABLED" || sourceStatus != "ACTIVE" ||
			tableStructureHash == "" || tableStructureHash != enrichedTableHash || structureHash == "" || structureHash != enrichedStructureHash {
			document.IneligibleCode = "ASSET_NOT_ELIGIBLE"
			return nil
		}

		rows, err := tx.Query(ctx, `SELECT id::text,column_name,business_name,business_description,tags,
			semantic_type,canonical_type,ordinal_position,structure_hash,last_enriched_structure_hash
		FROM platform.metadata_columns
		WHERE table_id=$1 AND asset_status='ACTIVE' ORDER BY ordinal_position,id`, claim.TableID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var selected *columnFacts
		for rows.Next() {
			var column columnFacts
			var columnStructureHash, columnEnrichedHash string
			if err := rows.Scan(&column.ID, &column.ColumnName, &column.BusinessName, &column.BusinessDescription,
				&column.Tags, &column.SemanticType, &column.CanonicalType, &column.OrdinalPosition,
				&columnStructureHash, &columnEnrichedHash); err != nil {
				return err
			}
			if columnStructureHash == "" || columnStructureHash != columnEnrichedHash {
				document.IneligibleCode = "METADATA_NOT_COMPLETED"
				return nil
			}
			if !semanticquality.Compatible(column.CanonicalType, column.SemanticType) {
				if claim.AssetType == "TABLE" || column.ID == claim.AssetID {
					document.IneligibleCode = "SEMANTIC_TYPE_INCOMPATIBLE"
					return nil
				}
				continue
			}
			facts.Columns = append(facts.Columns, column)
			if column.ID == claim.AssetID {
				copy := column
				selected = &copy
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(facts.Columns) == 0 || (claim.AssetType == "COLUMN" && selected == nil) {
			document.IneligibleCode = "ASSET_NOT_ELIGIBLE"
			return nil
		}
		if claim.AssetType == "TABLE" {
			document.Text = tableDocument(facts)
		} else {
			document.Text = columnDocument(facts, *selected)
		}
		if len(document.Text) == 0 || len(document.Text) > 240<<10 {
			document.IneligibleCode = "DOCUMENT_TOO_LARGE"
			return nil
		}
		document.InputHash = inputHash(document.Text)
		document.Eligible = true

		var storedStatus, storedHash, storedModel string
		lookupErr := tx.QueryRow(ctx, `SELECT status,input_hash,embedding_model
			FROM platform.asset_embeddings WHERE asset_type=$1 AND asset_id=$2`, claim.AssetType, claim.AssetID).
			Scan(&storedStatus, &storedHash, &storedModel)
		if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
		if lookupErr == nil && storedStatus == "SUCCEEDED" && storedHash == document.InputHash && storedModel == model {
			document.Current = true
			return nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.asset_embeddings(
			tenant_id,asset_type,asset_id,table_id,document_version,document,input_hash,status
		) VALUES($1,$2,$3,$4,$5,$6,$7,'PENDING')
		ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
			table_id=EXCLUDED.table_id,document_version=EXCLUDED.document_version,
			document=EXCLUDED.document,input_hash=EXCLUDED.input_hash,embedding=NULL,
			embedding_model='',model_version='',status='PENDING',error_code='',embedded_at=NULL,updated_at=now()`,
			claim.TenantID, claim.AssetType, claim.AssetID, claim.TableID, DocumentVersion, document.Text, document.InputHash)
		return err
	})
	return document, err
}

func (s *PostgresStore) Acknowledge(ctx context.Context, document Document, workerID string) error {
	return s.finishEvent(ctx, document, workerID, "SUCCEEDED", "")
}

func (s *PostgresStore) Complete(ctx context.Context, document Document, workerID, model string, vector []float32) error {
	if !document.Eligible || document.InputHash == "" || len(vector) == 0 || workerID == "" || model == "" {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, s.pool, document.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.asset_embeddings SET
			embedding=$1::halfvec,embedding_model=$2,model_version=$2,status='SUCCEEDED',
			error_code='',embedded_at=now(),updated_at=now()
			WHERE asset_type=$3 AND asset_id=$4 AND input_hash=$5 AND status='PENDING'`,
			formatVector(vector), model, document.AssetType, document.AssetID, document.InputHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("asset embedding document changed before completion")
		}
		return updateEvent(ctx, tx, document, workerID, "SUCCEEDED", "")
	})
}

func (s *PostgresStore) Skip(ctx context.Context, document Document, workerID string) error {
	if document.IneligibleCode == "" {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, s.pool, document.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.asset_embeddings
			WHERE asset_type=$1 AND asset_id=$2`, document.AssetType, document.AssetID); err != nil {
			return err
		}
		return updateEvent(ctx, tx, document, workerID, "SKIPPED", document.IneligibleCode)
	})
}

func (s *PostgresStore) Fail(ctx context.Context, document Document, workerID, code string) error {
	if code == "" {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, s.pool, document.TenantID, func(tx pgx.Tx) error {
		if document.InputHash != "" {
			if _, err := tx.Exec(ctx, `UPDATE platform.asset_embeddings SET
				status='FAILED',embedding=NULL,embedding_model='',model_version='',error_code=$1,
				embedded_at=NULL,updated_at=now()
				WHERE asset_type=$2 AND asset_id=$3 AND input_hash=$4`,
				code, document.AssetType, document.AssetID, document.InputHash); err != nil {
				return err
			}
		}
		var attempt int
		if err := tx.QueryRow(ctx, `SELECT attempt FROM platform.asset_embedding_outbox
			WHERE id=$1 AND status='RUNNING' AND lease_owner=$2 AND event_version=$3 FOR UPDATE`,
			document.ID, workerID, document.EventVersion).Scan(&attempt); err != nil {
			return err
		}
		status := "PENDING"
		if attempt >= 3 {
			status = "FAILED"
		}
		_, err := tx.Exec(ctx, `UPDATE platform.asset_embedding_outbox SET
			status=$1,error_code=$2,next_attempt_at=CASE WHEN attempt=1 THEN now()+interval '30 seconds'
			  WHEN attempt=2 THEN now()+interval '2 minutes' ELSE next_attempt_at END,
			lease_owner='',lease_expires_at=NULL,completed_at=CASE WHEN $1='FAILED' THEN now() ELSE NULL END,
			updated_at=now() WHERE id=$3 AND status='RUNNING' AND lease_owner=$4 AND event_version=$5`,
			status, code, document.ID, workerID, document.EventVersion)
		return err
	})
}

func (s *PostgresStore) finishEvent(ctx context.Context, document Document, workerID, status, code string) error {
	return database.WithTenantTx(ctx, s.pool, document.TenantID, func(tx pgx.Tx) error {
		return updateEvent(ctx, tx, document, workerID, status, code)
	})
}

func updateEvent(ctx context.Context, tx pgx.Tx, document Document, workerID, status, code string) error {
	tag, err := tx.Exec(ctx, `UPDATE platform.asset_embedding_outbox SET
		status=$1,error_code=$2,lease_owner='',lease_expires_at=NULL,completed_at=now(),updated_at=now()
		WHERE id=$3 AND status='RUNNING' AND lease_owner=$4 AND event_version=$5`,
		status, code, document.ID, workerID, document.EventVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("asset embedding event lease was lost")
	}
	return nil
}

func (s *PostgresStore) VectorRanks(ctx context.Context, tenantID, assetType string, tableIDs []string, vector []float32, limit int) (items []Rank, err error) {
	if (assetType != "TABLE" && assetType != "COLUMN") || len(vector) == 0 || limit < 1 || limit > 256 {
		return nil, ErrInvalidRequest
	}
	query := tableVectorRankSQL
	if assetType == "COLUMN" {
		query = columnVectorRankSQL
	}
	if tableIDs == nil {
		tableIDs = []string{}
	}
	items = []Rank{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, tableIDs, formatVector(vector), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Rank
			if err := rows.Scan(&item.AssetID, &item.TableID, &item.Score); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (s *PostgresStore) LexicalRanks(ctx context.Context, tenantID, assetType string, tableIDs []string, query string, tokens []string, limit int) (items []Rank, err error) {
	if (assetType != "TABLE" && assetType != "COLUMN") || strings.TrimSpace(query) == "" || limit < 1 || limit > 256 {
		return nil, ErrInvalidRequest
	}
	sql := tableLexicalRankSQL
	if assetType == "COLUMN" {
		sql = columnLexicalRankSQL
	}
	if tableIDs == nil {
		tableIDs = []string{}
	}
	items = []Rank{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, tableIDs, strings.ToLower(strings.TrimSpace(query)), tokens, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Rank
			if err := rows.Scan(&item.AssetID, &item.TableID, &item.Score); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

const eligibleTableJoin = ` FROM platform.asset_embeddings AS embedding
	JOIN platform.metadata_tables AS table_asset
	  ON table_asset.tenant_id=embedding.tenant_id AND table_asset.id=embedding.asset_id
	JOIN platform.data_sources AS source
	  ON source.tenant_id=table_asset.tenant_id AND source.id=table_asset.data_source_id
	WHERE embedding.tenant_id=platform.current_tenant_id() AND embedding.asset_type='TABLE'
	  AND table_asset.asset_status='ACTIVE' AND table_asset.management_status='ENABLED'
	  AND table_asset.structure_hash<>'' AND table_asset.last_enriched_structure_hash=table_asset.structure_hash
	  AND table_asset.table_structure_hash<>'' AND table_asset.last_enriched_table_structure_hash=table_asset.table_structure_hash
	  AND source.status='ACTIVE' AND source.deleted_at IS NULL
	  AND (cardinality($1::uuid[])=0 OR table_asset.id=ANY($1::uuid[])) `

const eligibleColumnJoin = ` FROM platform.asset_embeddings AS embedding
	JOIN platform.metadata_columns AS column_asset
	  ON column_asset.tenant_id=embedding.tenant_id AND column_asset.id=embedding.asset_id
	JOIN platform.metadata_tables AS table_asset
	  ON table_asset.tenant_id=embedding.tenant_id AND table_asset.id=embedding.table_id
	JOIN platform.data_sources AS source
	  ON source.tenant_id=table_asset.tenant_id AND source.id=table_asset.data_source_id
	WHERE embedding.tenant_id=platform.current_tenant_id() AND embedding.asset_type='COLUMN'
	  AND column_asset.asset_status='ACTIVE' AND column_asset.structure_hash<>''
	  AND column_asset.last_enriched_structure_hash=column_asset.structure_hash
	  AND table_asset.asset_status='ACTIVE' AND table_asset.management_status='ENABLED'
	  AND table_asset.structure_hash<>'' AND table_asset.last_enriched_structure_hash=table_asset.structure_hash
	  AND table_asset.table_structure_hash<>'' AND table_asset.last_enriched_table_structure_hash=table_asset.table_structure_hash
	  AND source.status='ACTIVE' AND source.deleted_at IS NULL
	  AND (cardinality($1::uuid[])=0 OR table_asset.id=ANY($1::uuid[])) `

const tableVectorRankSQL = `SELECT embedding.asset_id::text,embedding.table_id::text,
	GREATEST(0.0,1.0-(embedding.embedding <=> $2::halfvec))::float8 AS score` + eligibleTableJoin + `
	AND embedding.status='SUCCEEDED' AND embedding.embedding IS NOT NULL
	ORDER BY embedding.embedding <=> $2::halfvec,embedding.asset_id LIMIT $3`

const columnVectorRankSQL = `SELECT embedding.asset_id::text,embedding.table_id::text,
	GREATEST(0.0,1.0-(embedding.embedding <=> $2::halfvec))::float8 AS score` + eligibleColumnJoin + `
	AND embedding.status='SUCCEEDED' AND embedding.embedding IS NOT NULL
	ORDER BY embedding.embedding <=> $2::halfvec,embedding.asset_id LIMIT $3`

const tableLexicalRankSQL = `WITH ranked AS (
	SELECT embedding.asset_id::text AS asset_id,embedding.table_id::text AS table_id,
	  (CASE WHEN lower(table_asset.business_name)=lower($2) THEN 8 ELSE 0 END
	   + CASE WHEN lower(table_asset.table_name)=lower($2) THEN 7 ELSE 0 END
	   + CASE WHEN EXISTS(SELECT 1 FROM unnest(table_asset.tags) tag WHERE lower(tag)=lower($2)) THEN 6 ELSE 0 END
	   + CASE WHEN strpos(lower(embedding.document),lower($2))>0 THEN 3 ELSE 0 END
	   + COALESCE((SELECT count(*)::float8*0.25 FROM unnest($3::text[]) token
	       WHERE strpos(lower(embedding.document),token)>0),0))::float8 AS score` + eligibleTableJoin + `
) SELECT asset_id,table_id,score FROM ranked WHERE score>0 ORDER BY score DESC,asset_id LIMIT $4`

const columnLexicalRankSQL = `WITH ranked AS (
	SELECT embedding.asset_id::text AS asset_id,embedding.table_id::text AS table_id,
	  (CASE WHEN lower(column_asset.business_name)=lower($2) THEN 8 ELSE 0 END
	   + CASE WHEN lower(column_asset.column_name)=lower($2) THEN 7 ELSE 0 END
	   + CASE WHEN EXISTS(SELECT 1 FROM unnest(column_asset.tags) tag WHERE lower(tag)=lower($2)) THEN 6 ELSE 0 END
	   + CASE WHEN strpos(lower(embedding.document),lower($2))>0 THEN 3 ELSE 0 END
	   + COALESCE((SELECT count(*)::float8*0.25 FROM unnest($3::text[]) token
	       WHERE strpos(lower(embedding.document),token)>0),0))::float8 AS score` + eligibleColumnJoin + `
) SELECT asset_id,table_id,score FROM ranked WHERE score>0 ORDER BY score DESC,asset_id LIMIT $4`

func formatVector(values []float32) string {
	var builder strings.Builder
	builder.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}

func stableError(code string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", code, err)
}
