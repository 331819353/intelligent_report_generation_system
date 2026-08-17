package datasetai

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/asset"
	"intelligent-report-generation-system/internal/platform/database"
)

const datasetVersionCatalogPrefix = "dataset-version:"

// VersionAwareAssetCatalog keeps the generic physical-asset catalog contract while
// making immutable published dataset versions first-class inputs for DIM/DWD/DWS/ADS editing.
// The synthetic identifier is deliberately namespaced so it can never be interpreted
// as a metadata_tables UUID.
type VersionAwareAssetCatalog struct {
	pool     *pgxpool.Pool
	physical AssetCatalog
}

func NewVersionAwareAssetCatalog(
	pool *pgxpool.Pool,
	physical AssetCatalog,
) *VersionAwareAssetCatalog {
	return &VersionAwareAssetCatalog{pool: pool, physical: physical}
}

func datasetVersionID(catalogID string) (string, bool) {
	if !strings.HasPrefix(catalogID, datasetVersionCatalogPrefix) {
		return "", false
	}
	value := strings.TrimPrefix(catalogID, datasetVersionCatalogPrefix)
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != strings.ToLower(value) {
		return "", false
	}
	return parsed.String(), true
}

func isDatasetVersionCatalogID(catalogID string) bool {
	_, ok := datasetVersionID(catalogID)
	return ok
}

// maxPublishedVersionCandidates bounds how many published dataset versions the
// search unions in; ranking happens afterwards in the planner.
const maxPublishedVersionCandidates = 500

// publishedVersionSourceLayers are the layers whose published versions may feed
// a new model: an ADS version is a consumer, never a source.
var publishedVersionSourceLayers = []string{"ODS", "DIM", "DWD", "DWS"}

// SearchTables returns physical tables followed by the tenant's currently
// published dataset versions, paged as one virtual list. Making curated DIM/DWD/DWS
// versions *discoverable* — not merely resolvable by id — is what lets a new DWS
// or ADS model be built on the semantic layer instead of raw tables
// (docs/08 §4.4, docs/10 §5). Physical paging is untouched; versions fill the
// tail once the physical rows are exhausted at the requested offset.
func (catalog *VersionAwareAssetCatalog) SearchTables(
	ctx context.Context,
	tenantID string,
	search asset.Search,
) ([]asset.Table, int, error) {
	physical, physicalTotal, err := catalog.physical.SearchTables(ctx, tenantID, search)
	if err != nil {
		return nil, 0, err
	}
	if catalog.pool == nil || search.DataSourceID != "" || search.SourceType != "" {
		// A data-source-bound search asks for physical tables of that source only.
		return physical, physicalTotal, nil
	}
	versions, err := catalog.listPublishedVersions(ctx, tenantID, search.Query)
	if err != nil {
		return nil, 0, err
	}
	page, total := mergeVersionPage(physical, physicalTotal, search, versions)
	return page, total, nil
}

// mergeVersionPage pages `physical ++ versions` as one virtual list: the physical
// page is returned untouched and versions fill the remainder once the physical
// rows are exhausted at this offset.
func mergeVersionPage(physical []asset.Table, physicalTotal int, search asset.Search, versions []asset.Table) ([]asset.Table, int) {
	total := physicalTotal + len(versions)
	if search.Limit <= 0 {
		return append(append([]asset.Table(nil), physical...), versions...), total
	}
	if len(physical) >= search.Limit {
		return physical, total
	}
	versionOffset := 0
	if len(physical) == 0 {
		versionOffset = max(0, search.Offset-physicalTotal)
	}
	remaining := search.Limit - len(physical)
	if versionOffset >= len(versions) {
		return physical, total
	}
	end := min(len(versions), versionOffset+remaining)
	return append(append([]asset.Table(nil), physical...), versions[versionOffset:end]...), total
}

// listPublishedVersions loads the current published version of every eligible
// dataset as a virtual catalog table.
func (catalog *VersionAwareAssetCatalog) listPublishedVersions(ctx context.Context, tenantID, query string) ([]asset.Table, error) {
	items := []asset.Table{}
	pattern := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	err := database.WithTenantTx(ctx, catalog.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT dataset.id::text,version.id::text,COALESCE(dataset.origin_table_id::text,''),
			       dataset.code,dataset.name,dataset.description,
			       version.layer,version.schema_hash,version.version_no,
			       version.record_version,version.updated_at::text,
			       (SELECT count(*) FROM platform.dataset_fields AS field
			         WHERE field.dataset_version_id=version.id),
			       COALESCE(origin_table.tags,'{}'::text[])
			         || COALESCE(governed_tags.tags,'{}'::text[])
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS version
			  ON version.id=dataset.current_published_version_id
			 AND version.tenant_id=dataset.tenant_id
			LEFT JOIN platform.metadata_tables AS origin_table
			  ON origin_table.id=dataset.origin_table_id
			 AND origin_table.tenant_id=dataset.tenant_id
			LEFT JOIN LATERAL (
			  SELECT array_agg(DISTINCT tag.name ORDER BY tag.name) AS tags
			  FROM platform.asset_tag_bindings AS binding
			  JOIN platform.semantic_tags AS tag
			    ON tag.id=binding.tag_id
			   AND tag.tenant_id=binding.tenant_id
			   AND tag.status='ACTIVE'
			  WHERE binding.asset_type='DATASET_VERSION'
			    AND binding.dataset_id=dataset.id
			    AND binding.dataset_version_id=version.id
			    AND binding.status='APPROVED'
			) AS governed_tags ON true
			WHERE dataset.tenant_id=$1::uuid
			  AND dataset.deleted_at IS NULL
			  AND dataset.status<>'DISABLED'
			  AND version.status IN ('PUBLISHED','STALE','DEPRECATED')
			  AND version.layer=ANY($2)
			  AND ($3='%%' OR lower(dataset.code)||' '||lower(dataset.name)||' '||lower(COALESCE(dataset.description,''))||' '||lower(array_to_string(COALESCE(origin_table.tags,'{}'::text[])||COALESCE(governed_tags.tags,'{}'::text[]),' ')) LIKE $3)
			ORDER BY dataset.updated_at DESC,dataset.id
			LIMIT $4`,
			tenantID, publishedVersionSourceLayers, pattern, maxPublishedVersionCandidates,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				item                     asset.Table
				datasetID, versionID     string
				originTableID            string
				layer, updatedAt         string
				versionNo, recordVersion int64
			)
			if err := rows.Scan(
				&datasetID, &versionID, &originTableID, &item.TableName, &item.BusinessName, &item.BusinessDescription,
				&layer, &item.StructureHash, &versionNo, &recordVersion, &updatedAt, &item.ColumnCount,
				&item.Tags,
			); err != nil {
				return err
			}
			decorateDatasetVersionTable(&item, datasetVersionCatalogPrefix+versionID, datasetID, versionID, originTableID, layer, updatedAt, versionNo, recordVersion)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// decorateDatasetVersionTable fills the synthetic asset fields shared by GetTable
// and SearchTables so both paths describe a version identically.
func decorateDatasetVersionTable(item *asset.Table, catalogID, datasetID, versionID, originTableID, layer, updatedAt string, versionNo, recordVersion int64) {
	item.ID = catalogID
	item.OriginTableID = strings.TrimSpace(originTableID)
	item.DataSourceID = "dataset-layer:" + layer
	item.DataSourceName = layer + " 数据集"
	item.DataSourceType = "DATASET"
	item.CatalogName = "platform"
	item.SchemaName = layer
	item.TableType = "DATASET_VERSION"
	item.SourceComment = fmt.Sprintf("dataset=%s version=%s", datasetID, versionID)
	item.Tags = mergeCatalogTags([]string{"layer:" + layer}, item.Tags)
	item.SensitivityLevel = "INTERNAL"
	item.Visibility = "TENANT_PUBLIC"
	item.AssetStatus = "ACTIVE"
	item.ManagementStatus = "ENABLED"
	item.EnrichmentStatus = "SUCCEEDED"
	item.MetadataVersion = versionNo
	item.BusinessVersion = recordVersion
	item.LastSyncAt = updatedAt
}

func (catalog *VersionAwareAssetCatalog) GetTable(
	ctx context.Context,
	tenantID string,
	catalogID string,
) (item asset.Table, err error) {
	versionID, virtual := datasetVersionID(catalogID)
	if !virtual {
		return catalog.physical.GetTable(ctx, tenantID, catalogID)
	}
	err = database.WithTenantTx(ctx, catalog.pool, tenantID, func(tx pgx.Tx) error {
		var (
			datasetID, originTableID, layer, updatedAt string
			versionNo, recordVersion                   int64
		)
		err := tx.QueryRow(ctx, `
			SELECT dataset.id::text,COALESCE(dataset.origin_table_id::text,''),
			       dataset.code,dataset.name,dataset.description,
			       version.layer,version.schema_hash,version.version_no,
			       version.record_version,version.updated_at::text,
			       (SELECT count(*) FROM platform.dataset_fields AS field
			         WHERE field.dataset_version_id=version.id),
			       COALESCE(origin_table.tags,'{}'::text[])
			         || COALESCE(governed_tags.tags,'{}'::text[])
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id
			 AND dataset.tenant_id=version.tenant_id
			 AND dataset.deleted_at IS NULL
			 AND dataset.status<>'DISABLED'
			LEFT JOIN platform.metadata_tables AS origin_table
			  ON origin_table.id=dataset.origin_table_id
			 AND origin_table.tenant_id=dataset.tenant_id
			LEFT JOIN LATERAL (
			  SELECT array_agg(DISTINCT tag.name ORDER BY tag.name) AS tags
			  FROM platform.asset_tag_bindings AS binding
			  JOIN platform.semantic_tags AS tag
			    ON tag.id=binding.tag_id
			   AND tag.tenant_id=binding.tenant_id
			   AND tag.status='ACTIVE'
			  WHERE binding.asset_type='DATASET_VERSION'
			    AND binding.dataset_id=dataset.id
			    AND binding.dataset_version_id=version.id
			    AND binding.status='APPROVED'
			) AS governed_tags ON true
			WHERE version.id=$1::uuid
			  AND version.status IN ('PUBLISHED','STALE','DEPRECATED')`,
			versionID,
		).Scan(
			&datasetID, &originTableID, &item.TableName, &item.BusinessName,
			&item.BusinessDescription, &layer, &item.StructureHash,
			&versionNo, &recordVersion, &updatedAt, &item.ColumnCount,
			&item.Tags,
		)
		if err != nil {
			return err
		}
		decorateDatasetVersionTable(&item, catalogID, datasetID, versionID, originTableID, layer, updatedAt, versionNo, recordVersion)
		return nil
	})
	return item, err
}

func (catalog *VersionAwareAssetCatalog) ListColumns(
	ctx context.Context,
	tenantID string,
	catalogID string,
) (items []asset.Column, err error) {
	versionID, virtual := datasetVersionID(catalogID)
	if !virtual {
		return catalog.physical.ListColumns(ctx, tenantID, catalogID)
	}
	items = []asset.Column{}
	err = database.WithTenantTx(ctx, catalog.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT field.id::text,field.field_code::text,field.ordinal_position,
			       field.canonical_type,field.nullable,field.field_name,
			       field.description,field.semantic_type,version.record_version,
			       COALESCE(governed_tags.tags,'{}'::text[])
			FROM platform.dataset_fields AS field
			JOIN platform.dataset_versions AS version
			  ON version.id=field.dataset_version_id
			 AND version.tenant_id=field.tenant_id
			 AND version.status IN ('PUBLISHED','STALE','DEPRECATED')
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id
			 AND dataset.tenant_id=version.tenant_id
			 AND dataset.deleted_at IS NULL
			 AND dataset.status<>'DISABLED'
			LEFT JOIN LATERAL (
			  SELECT array_agg(DISTINCT tag.name ORDER BY tag.name) AS tags
			  FROM platform.asset_tag_bindings AS binding
			  JOIN platform.semantic_tags AS tag
			    ON tag.id=binding.tag_id
			   AND tag.tenant_id=binding.tenant_id
			   AND tag.status='ACTIVE'
			  WHERE binding.asset_type='DATASET_FIELD'
			    AND binding.dataset_id=dataset.id
			    AND binding.dataset_version_id=version.id
			    AND binding.dataset_field_id=field.field_id
			    AND binding.status='APPROVED'
			) AS governed_tags ON true
			WHERE field.dataset_version_id=$1::uuid
			ORDER BY field.ordinal_position`,
			versionID,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item asset.Column
			if err := rows.Scan(
				&item.ID, &item.ColumnName, &item.OrdinalPosition,
				&item.CanonicalType, &item.Nullable, &item.BusinessName,
				&item.BusinessDescription, &item.SemanticType,
				&item.BusinessVersion, &item.Tags,
			); err != nil {
				return err
			}
			item.TableID = catalogID
			item.NativeType = item.CanonicalType
			item.SensitivityLevel = "INTERNAL"
			item.AssetStatus = "ACTIVE"
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// mergeCatalogTags keeps stable first-seen order while removing duplicate labels
// coming from the layer marker, the ODS origin table and governed bindings.
func mergeCatalogTags(groups ...[]string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, group := range groups {
		for _, raw := range group {
			tag := strings.TrimSpace(raw)
			if tag == "" || seen[tag] {
				continue
			}
			seen[tag] = true
			result = append(result, tag)
		}
	}
	return result
}
