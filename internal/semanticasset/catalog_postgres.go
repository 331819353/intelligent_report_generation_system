package semanticasset

import (
	"context"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

// Catalog reads every online semantic object and the global readiness
// watermark in one tenant transaction. This is the authoritative control-plane
// projection for asset management; native authoring APIs remain responsible
// for object-specific edits.
func (store *PostgresStore) Catalog(
	ctx context.Context,
	tenantID string,
	filter CatalogFilter,
) (items []CatalogObject, total int, snapshot ReadinessSnapshot, err error) {
	items = []CatalogObject{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if readinessErr := readReadinessSnapshot(ctx, tx, &snapshot); readinessErr != nil {
			return readinessErr
		}
		rows, queryErr := tx.Query(ctx, `WITH latest_dimension_jobs AS (
				SELECT DISTINCT ON (job.dimension_id)
					job.dimension_id,job.dimension_version,job.status
				FROM platform.dimension_member_refresh_jobs AS job
				WHERE job.tenant_id=platform.current_tenant_id()
				ORDER BY job.dimension_id,job.created_at DESC,job.id DESC
			), objects AS (
				SELECT 'METRIC'::text AS object_type,metric.id::text AS id,
					metric.code::text AS code,metric.name,metric.description,
					metric.domain_id::text,metric.sharing_scope::text,metric.status,
					CASE WHEN metric.status='PUBLISHED'
					       AND metric.current_published_version_id IS NOT NULL
					       AND version.status='PUBLISHED'
					  THEN 'CERTIFIED' ELSE 'UNCERTIFIED' END AS certification,
					COALESCE(version.version_no::bigint,metric.version) AS version,
					COALESCE(version.definition_hash,'') AS content_hash,
					COALESCE(metric.updated_by::text,metric.created_by::text,'') AS owner_id,
					'INTERNAL'::text AS sensitivity,
					(metric.status='PUBLISHED'
					 AND metric.current_published_version_id IS NOT NULL
					 AND version.status='PUBLISHED') AS execution_eligible,
					CASE WHEN metric.status='PUBLISHED'
					       AND metric.current_published_version_id IS NOT NULL
					       AND version.status='PUBLISHED'
					  THEN 'READY' ELSE 'METRIC_CONTRACT_INCOMPLETE' END AS readiness_code,
					metric.updated_at
				FROM platform.metrics AS metric
				LEFT JOIN platform.metric_versions AS version
				  ON version.tenant_id=metric.tenant_id
				 AND version.metric_id=metric.id
				 AND version.id=metric.current_published_version_id
				WHERE metric.tenant_id=platform.current_tenant_id()
				  AND metric.deleted_at IS NULL

				UNION ALL

				SELECT 'DIMENSION',dimension.id::text,dimension.code::text,
					dimension.name,dimension.description,dimension.domain_id::text,
					dimension.sharing_scope::text,dimension.status,
					CASE WHEN dimension.status='PUBLISHED' THEN 'CERTIFIED'
					     ELSE 'UNCERTIFIED' END,
					dimension.version,dimension.definition_hash,
					COALESCE(dimension.updated_by::text,dimension.created_by::text,''),
					CASE WHEN dimension.sensitive THEN 'SENSITIVE' ELSE 'INTERNAL' END,
					(dimension.status='PUBLISHED' AND (
					  dimension.member_index_policy IN ('EXACT_ONLY','NONE') OR (
					    dimension.member_index_policy='FULL'
					    AND job.dimension_version=dimension.version
					    AND job.status='SUCCEEDED'
					  )
					)),
					CASE WHEN dimension.status<>'PUBLISHED'
					       THEN 'DIMENSION_NOT_PUBLISHED'
					     WHEN dimension.member_index_policy IN ('EXACT_ONLY','NONE')
					       THEN 'READY'
					     WHEN job.dimension_version=dimension.version
					       AND job.status='SUCCEEDED' THEN 'READY'
					     ELSE 'DIMENSION_MEMBER_INDEX_PENDING' END,
					dimension.updated_at
				FROM platform.semantic_dimensions AS dimension
				LEFT JOIN latest_dimension_jobs AS job
				  ON job.dimension_id=dimension.id
				WHERE dimension.tenant_id=platform.current_tenant_id()

				UNION ALL

				SELECT 'TERM',asset.id::text,asset.knowledge_type,
					asset.common_term::text,asset.mapping_value,asset.domain_id::text,
					asset.sharing_scope::text,asset.status,
					CASE WHEN asset.status='ACTIVE' THEN 'CERTIFIED'
					     ELSE 'UNCERTIFIED' END,
					asset.version,asset.embedding_input_hash,
					COALESCE(asset.updated_by::text,asset.created_by::text,''),
					'INTERNAL',
					(asset.status='ACTIVE'
					 AND asset.embedding_status IN ('SUCCEEDED','SKIPPED')),
					CASE WHEN asset.status<>'ACTIVE' THEN 'TERM_DEPRECATED'
					     WHEN asset.embedding_status IN ('SUCCEEDED','SKIPPED')
					       THEN 'READY'
					     ELSE 'TERM_PROJECTION_'||asset.embedding_status END,
					asset.updated_at
				FROM platform.semantic_term_assets AS asset
				WHERE asset.tenant_id=platform.current_tenant_id()

				UNION ALL

				SELECT 'PARSING_RULE',rule.id::text,rule.rule_type,
					rule.pattern,rule.action,'','PLATFORM',rule.status,
					CASE WHEN rule.status='ACTIVE' THEN 'CERTIFIED'
					     ELSE 'UNCERTIFIED' END,
					rule.version,'',COALESCE(rule.updated_by::text,rule.created_by::text,''),
					'INTERNAL',(rule.status='ACTIVE'),
					CASE WHEN rule.status='ACTIVE' THEN 'READY'
					     ELSE 'RULE_DEPRECATED' END,rule.updated_at
				FROM platform.semantic_parsing_rules AS rule
				WHERE rule.tenant_id IS NULL
				   OR rule.tenant_id=platform.current_tenant_id()
			)
			SELECT object_type,id,code,name,description,domain_id,sharing_scope,
				status,certification,version,content_hash,owner_id,sensitivity,
				execution_eligible,readiness_code,updated_at,count(*) OVER()::int
			FROM objects
			WHERE ($1='' OR object_type=$1)
			  AND ($2='' OR status=$2)
			  AND ($3='' OR ($3='READY')=execution_eligible)
			  AND ($4='' OR code ILIKE '%'||$4||'%'
			       OR name ILIKE '%'||$4||'%'
			       OR description ILIKE '%'||$4||'%')
			ORDER BY object_type,name,id
			LIMIT $5 OFFSET $6`,
			filter.ObjectType, filter.Status, filter.Ready, filter.Query,
			filter.Limit, filter.Offset,
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item CatalogObject
			if scanErr := rows.Scan(
				&item.ObjectType, &item.ID, &item.Code, &item.Name,
				&item.Description, &item.DomainID, &item.SharingScope,
				&item.Status, &item.Certification, &item.Version,
				&item.ContentHash, &item.OwnerID, &item.Sensitivity,
				&item.ExecutionEligible, &item.ReadinessCode,
				&item.UpdatedAt, &total,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, snapshot, err
}
