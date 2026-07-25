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

func (catalog *VersionAwareAssetCatalog) SearchTables(
	ctx context.Context,
	tenantID string,
	search asset.Search,
) ([]asset.Table, int, error) {
	return catalog.physical.SearchTables(ctx, tenantID, search)
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
			datasetID, layer, updatedAt string
			versionNo, recordVersion    int64
		)
		err := tx.QueryRow(ctx, `
			SELECT dataset.id::text,dataset.code,dataset.name,dataset.description,
			       version.layer,version.schema_hash,version.version_no,
			       version.record_version,version.updated_at::text,
			       (SELECT count(*) FROM platform.dataset_fields AS field
			         WHERE field.dataset_version_id=version.id)
			FROM platform.dataset_versions AS version
			JOIN platform.datasets AS dataset
			  ON dataset.id=version.dataset_id
			 AND dataset.tenant_id=version.tenant_id
			 AND dataset.deleted_at IS NULL
			 AND dataset.status<>'DISABLED'
			WHERE version.id=$1::uuid
			  AND version.status IN ('PUBLISHED','STALE','DEPRECATED')`,
			versionID,
		).Scan(
			&datasetID, &item.TableName, &item.BusinessName,
			&item.BusinessDescription, &layer, &item.StructureHash,
			&versionNo, &recordVersion, &updatedAt, &item.ColumnCount,
		)
		if err != nil {
			return err
		}
		item.ID = catalogID
		item.DataSourceID = "dataset-layer:" + layer
		item.DataSourceName = layer + " 数据集"
		item.DataSourceType = "DATASET"
		item.CatalogName = "platform"
		item.SchemaName = layer
		item.TableType = "DATASET_VERSION"
		item.SourceComment = fmt.Sprintf("dataset=%s version=%s", datasetID, versionID)
		item.Tags = []string{"layer:" + layer}
		item.SensitivityLevel = "INTERNAL"
		item.Visibility = "TENANT_PUBLIC"
		item.AssetStatus = "ACTIVE"
		item.ManagementStatus = "ENABLED"
		item.EnrichmentStatus = "SUCCEEDED"
		item.MetadataVersion = versionNo
		item.BusinessVersion = recordVersion
		item.LastSyncAt = updatedAt
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
			       field.description,field.semantic_type,version.record_version
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
				&item.BusinessVersion,
			); err != nil {
				return err
			}
			item.TableID = catalogID
			item.NativeType = item.CanonicalType
			item.Tags = []string{}
			item.SensitivityLevel = "INTERNAL"
			item.AssetStatus = "ACTIVE"
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}
