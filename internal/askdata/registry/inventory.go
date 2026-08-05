// Package registry owns the authoritative semantic registry and read-only
// inventory adapters for already published warehouse assets.
package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/dataset"
)

const InventoryVersion = "1.0"

var ErrUnsupportedInventoryLayer = errors.New("inventory accepts only published DWS or ADS assets")

type PublishedAssetRecord struct {
	TenantID            string
	DomainID            string
	DatasetID           string
	DatasetCode         string
	DatasetName         string
	DatasetVersionID    string
	VersionNo           int
	Layer               string
	SchemaHash          string
	DSLJSON             []byte
	MaterializationID   string
	PublishedSchema     string
	PublishedName       string
	MaterializationHash string
	SnapshotHash        string
	RowCount            int64
	ActivatedAt         time.Time
}

type InventoryStore interface {
	ListPublishedAssets(ctx context.Context, tenantID string) ([]PublishedAssetRecord, error)
}

type DocumentSummary struct {
	Fields      []InventoryField
	OutputGrain InventoryGrain
	TimeFields  []string
}

type DocumentSummarizer interface {
	Summarize(raw []byte, expectedSchemaHash string) (DocumentSummary, error)
}

type DatasetDocumentSummarizer struct{}

func (DatasetDocumentSummarizer) Summarize(raw []byte, expectedSchemaHash string) (DocumentSummary, error) {
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		return DocumentSummary{}, fmt.Errorf("prepare published dataset DSL: %w", err)
	}
	if prepared.DSLHash != expectedSchemaHash {
		return DocumentSummary{}, errors.New("published dataset DSL hash does not match schema_hash")
	}
	fields := make([]InventoryField, 0, len(prepared.Document.Fields))
	timeFields := make([]string, 0, 2)
	measureContracts := make(map[string]InventoryMeasureContract)
	if prepared.Document.FactContract != nil {
		for _, contract := range prepared.Document.FactContract.AtomicMeasures {
			measureContracts[contract.Field] = InventoryMeasureContract{
				Aggregation: contract.DefaultAggregation, Additivity: contract.Additivity,
				NullPolicy: contract.NullPolicy, Unit: contract.Unit,
			}
		}
	}
	if prepared.Document.AnalysisContract != nil {
		for _, contract := range prepared.Document.AnalysisContract.Measures {
			measureContracts[contract.Field] = InventoryMeasureContract{
				Aggregation: contract.Aggregation, Additivity: contract.Additivity,
				NullPolicy: "PRESERVE", Unit: contract.Unit,
			}
		}
	}
	for index, field := range prepared.Document.Fields {
		visible := field.Visible == nil || *field.Visible
		measureContract := measureContracts[field.Code]
		fields = append(fields, InventoryField{
			FieldID: field.ID, Code: field.Code, Name: field.Name,
			CanonicalType: field.CanonicalType, SemanticType: field.SemanticType,
			Role: field.Role, Aggregation: firstNonEmpty(measureContract.Aggregation, field.Aggregation),
			Additivity: measureContract.Additivity, NullPolicy: measureContract.NullPolicy,
			Unit:     measureContract.Unit,
			Nullable: field.Nullable, Visible: visible, Ordinal: index + 1,
		})
		if field.Role == "TIME" || field.Code == prepared.Document.OutputGrain.TimeField ||
			field.CanonicalType == "DATE" || field.CanonicalType == "DATETIME" ||
			field.SemanticType == "DATE" || field.SemanticType == "DATETIME" {
			timeFields = append(timeFields, field.Code)
		}
	}
	sort.Strings(timeFields)
	return DocumentSummary{
		Fields: fields,
		OutputGrain: InventoryGrain{
			Description:      prepared.Document.OutputGrain.Description,
			KeyFields:        append([]string(nil), prepared.Document.OutputGrain.KeyFields...),
			TimeField:        prepared.Document.OutputGrain.TimeField,
			DefaultTimeGrain: prepared.Document.OutputGrain.DefaultTimeGrain,
		},
		TimeFields: timeFields,
	}, nil
}

type InventoryOptions struct {
	IncludePhysicalIdentifiers bool
}

type Inventory struct {
	InventoryVersion string           `json:"inventoryVersion"`
	GeneratedAt      time.Time        `json:"generatedAt"`
	TenantReference  string           `json:"tenantReference"`
	Redacted         bool             `json:"redacted"`
	Assets           []InventoryAsset `json:"assets"`
}

type InventoryAsset struct {
	DomainID              string             `json:"domainId"`
	DatasetID             string             `json:"datasetId"`
	DatasetCode           string             `json:"datasetCode"`
	DatasetName           string             `json:"datasetName"`
	DatasetVersionID      string             `json:"datasetVersionId"`
	VersionNo             int                `json:"versionNo"`
	Layer                 string             `json:"layer"`
	SchemaHash            string             `json:"schemaHash"`
	MaterializationID     string             `json:"materializationId"`
	MaterializationHash   string             `json:"materializationHash"`
	SnapshotHash          string             `json:"snapshotHash"`
	ActivatedAt           time.Time          `json:"activatedAt"`
	PhysicalReferenceHash string             `json:"physicalReferenceHash"`
	Physical              *PhysicalReference `json:"physical,omitempty"`
	Fields                []InventoryField   `json:"fields"`
	OutputGrain           InventoryGrain     `json:"outputGrain"`
	TimeFields            []string           `json:"timeFields"`
}

type PhysicalReference struct {
	PublishedSchema string `json:"publishedSchema"`
	PublishedName   string `json:"publishedName"`
}

type InventoryField struct {
	FieldID       string `json:"fieldId"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	CanonicalType string `json:"canonicalType"`
	SemanticType  string `json:"semanticType"`
	Role          string `json:"role"`
	Aggregation   string `json:"aggregation"`
	Additivity    string `json:"additivity,omitempty"`
	NullPolicy    string `json:"nullPolicy,omitempty"`
	Unit          string `json:"unit,omitempty"`
	Nullable      bool   `json:"nullable"`
	Visible       bool   `json:"visible"`
	Ordinal       int    `json:"ordinal"`
}

type InventoryMeasureContract struct {
	Aggregation string
	Additivity  string
	NullPolicy  string
	Unit        string
}

type InventoryGrain struct {
	Description      string   `json:"description"`
	KeyFields        []string `json:"keyFields"`
	TimeField        string   `json:"timeField"`
	DefaultTimeGrain string   `json:"defaultTimeGrain"`
}

type InventoryService struct {
	store      InventoryStore
	summarizer DocumentSummarizer
	now        func() time.Time
}

func NewInventoryService(store InventoryStore) *InventoryService {
	return &InventoryService{store: store, summarizer: DatasetDocumentSummarizer{}, now: time.Now}
}

func (service *InventoryService) List(ctx context.Context, tenantID string, options InventoryOptions) (Inventory, error) {
	if service == nil || service.store == nil || service.summarizer == nil || service.now == nil {
		return Inventory{}, errors.New("inventory service is not configured")
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return Inventory{}, errors.New("tenant ID must be a UUID")
	}
	records, err := service.store.ListPublishedAssets(ctx, tenantID)
	if err != nil {
		return Inventory{}, fmt.Errorf("list published assets: %w", err)
	}
	assets := make([]InventoryAsset, 0, len(records))
	for index, record := range records {
		if record.TenantID != tenantID {
			return Inventory{}, fmt.Errorf("record %d crosses the requested tenant boundary", index)
		}
		if record.Layer != string(dataset.LayerDWS) && record.Layer != string(dataset.LayerADS) {
			return Inventory{}, fmt.Errorf("record %d: %w", index, ErrUnsupportedInventoryLayer)
		}
		if record.SchemaHash == "" || record.SchemaHash != record.MaterializationHash {
			return Inventory{}, fmt.Errorf("record %d has inconsistent dataset and materialization hashes", index)
		}
		if err := askdata.ContentHash(record.SchemaHash).Validate(); err != nil {
			return Inventory{}, fmt.Errorf("record %d schemaHash: %w", index, err)
		}
		if err := askdata.ContentHash(record.SnapshotHash).Validate(); err != nil {
			return Inventory{}, fmt.Errorf("record %d snapshotHash: %w", index, err)
		}
		summary, err := service.summarizer.Summarize(record.DSLJSON, record.SchemaHash)
		if err != nil {
			return Inventory{}, fmt.Errorf("record %d: %w", index, err)
		}
		physicalHash := askdata.HashBytes([]byte(record.PublishedSchema + "\x00" + record.PublishedName))
		asset := InventoryAsset{
			DomainID: record.DomainID, DatasetID: record.DatasetID,
			DatasetCode: record.DatasetCode, DatasetName: record.DatasetName,
			DatasetVersionID: record.DatasetVersionID, VersionNo: record.VersionNo,
			Layer: record.Layer, SchemaHash: record.SchemaHash,
			MaterializationID:   record.MaterializationID,
			MaterializationHash: record.MaterializationHash, SnapshotHash: record.SnapshotHash,
			ActivatedAt: record.ActivatedAt.UTC(), PhysicalReferenceHash: string(physicalHash),
			Fields: summary.Fields, OutputGrain: summary.OutputGrain, TimeFields: summary.TimeFields,
		}
		if options.IncludePhysicalIdentifiers {
			asset.Physical = &PhysicalReference{PublishedSchema: record.PublishedSchema, PublishedName: record.PublishedName}
		}
		assets = append(assets, asset)
	}
	sort.Slice(assets, func(left, right int) bool {
		if assets[left].Layer == assets[right].Layer {
			if assets[left].DatasetCode == assets[right].DatasetCode {
				return assets[left].DatasetVersionID < assets[right].DatasetVersionID
			}
			return assets[left].DatasetCode < assets[right].DatasetCode
		}
		return assets[left].Layer < assets[right].Layer
	})
	tenantDigest := askdata.HashBytes([]byte("askdata-inventory-tenant\x00" + tenantID))
	return Inventory{
		InventoryVersion: InventoryVersion,
		GeneratedAt:      service.now().UTC(),
		TenantReference:  "tenant-sha256:" + string(tenantDigest)[:16],
		Redacted:         !options.IncludePhysicalIdentifiers,
		Assets:           assets,
	}, nil
}

type PostgresInventoryStore struct {
	pool *pgxpool.Pool
}

func NewPostgresInventoryStore(pool *pgxpool.Pool) *PostgresInventoryStore {
	return &PostgresInventoryStore{pool: pool}
}

// ListPublishedAssets executes inside a read-only, repeatable-read transaction
// and never opens the warehouse database. The SQL itself also filters to DWS
// and ADS, while InventoryService repeats the layer check fail-closed.
func (store *PostgresInventoryStore) ListPublishedAssets(ctx context.Context, tenantID string) ([]PublishedAssetRecord, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("inventory PostgreSQL store is not configured")
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, errors.New("tenant ID must be a UUID")
	}
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin read-only inventory transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','SYSTEM',true),
		set_config('app.user_id','',true),
		set_config('app.domain_id','',true)`, tenantID); err != nil {
		return nil, fmt.Errorf("set inventory tenant context: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT
		dataset.tenant_id::text,
		dataset.domain_id::text,
		dataset.id::text,
		dataset.code::text,
		dataset.name,
		version.id::text,
		version.version_no,
		version.layer,
		version.schema_hash,
		version.dsl_json,
		materialization.id::text,
		materialization.published_schema,
		materialization.published_name,
		materialization.schema_hash,
		materialization.snapshot_hash,
		materialization.row_count,
		materialization.activated_at
	FROM platform.datasets AS dataset
	JOIN platform.dataset_versions AS version
	  ON version.tenant_id=dataset.tenant_id
	 AND version.dataset_id=dataset.id
	 AND version.id=dataset.current_published_version_id
	JOIN platform.dataset_materializations AS materialization
	  ON materialization.tenant_id=dataset.tenant_id
	 AND materialization.dataset_id=dataset.id
	 AND materialization.dataset_version_id=version.id
	 AND materialization.status='ACTIVE'
	WHERE dataset.tenant_id=$1
	  AND dataset.deleted_at IS NULL
	  AND dataset.status='PUBLISHED'
	  AND version.status='PUBLISHED'
	  AND version.layer IN ('DWS','ADS')
	  AND materialization.layer=version.layer
	ORDER BY version.layer,dataset.code,version.id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("query published inventory: %w", err)
	}
	defer rows.Close()
	records := make([]PublishedAssetRecord, 0)
	for rows.Next() {
		var record PublishedAssetRecord
		if err := rows.Scan(
			&record.TenantID, &record.DomainID, &record.DatasetID,
			&record.DatasetCode, &record.DatasetName,
			&record.DatasetVersionID, &record.VersionNo, &record.Layer,
			&record.SchemaHash, &record.DSLJSON,
			&record.MaterializationID, &record.PublishedSchema, &record.PublishedName,
			&record.MaterializationHash, &record.SnapshotHash,
			&record.RowCount, &record.ActivatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan published inventory: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published inventory: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit read-only inventory transaction: %w", err)
	}
	return records, nil
}

// PhysicalReferenceDigest is exported for deterministic audit and tests while
// keeping the physical identifiers themselves out of default output.
func PhysicalReferenceDigest(schema, name string) string {
	hash := askdata.HashBytes([]byte(strings.TrimSpace(schema) + "\x00" + strings.TrimSpace(name)))
	return string(hash)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
