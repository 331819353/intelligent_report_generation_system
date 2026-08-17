package registryimport

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

// ImportRowFact 是校验/提交后的一行 JSON 事实，供导入向导渲染。裁决
// （create/update/unchanged/failed）由行状态与信息标注推导，不另存状态。
type ImportRowFact struct {
	RowNo            int               `json:"rowNo"`
	AssetType        AssetType         `json:"assetType"`
	BundleAsset      string            `json:"bundleAsset,omitempty"`
	Code             string            `json:"code,omitempty"`
	Name             string            `json:"name,omitempty"`
	State            RowState          `json:"state"`
	Resolution       string            `json:"resolution"`
	Issues           []ValidationIssue `json:"issues"`
	CreatedObjectID  string            `json:"createdObjectId,omitempty"`
	CreatedVersionID string            `json:"createdVersionId,omitempty"`
}

// ImportResolution 的取值：CREATE / UPDATE / UNCHANGED / SKIPPED / FAILED。
// CREATE 与 UPDATE 在提交前表示意图，提交后表示既成事实。
const (
	ResolutionCreate    = "CREATE"
	ResolutionUpdate    = "UPDATE"
	ResolutionUnchanged = "UNCHANGED"
	ResolutionSkipped   = "SKIPPED"
	ResolutionFailed    = "FAILED"
)

type ImportSectionCounts struct {
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Skipped   int `json:"skipped"`
	Failed    int `json:"failed"`
	Pending   int `json:"pending"`
}

// ImportIndexSummary 是检索就绪状态的派生视图：创建的版本进入 Release 投影
// 后才会出现在 search_documents。就绪不是导入批的持久状态，而是从索引事实
// 现算——可推导的状态不落库。
type ImportIndexSummary struct {
	CreatedVersions int `json:"createdVersions"`
	Indexed         int `json:"indexed"`
	EmbeddingReady  int `json:"embeddingReady"`
	EmbeddingFailed int `json:"embeddingFailed"`
	// AwaitingRelease 是尚未进入任何 Release 投影（因此还没有检索文档）的
	// 版本数。DRAFT → CERTIFIED → Release ACTIVE 是既有认证链路。
	AwaitingRelease int `json:"awaitingRelease"`
}

type ImportReportJSON struct {
	Import    SemanticImport                    `json:"import"`
	Counts    ImportSectionCounts               `json:"counts"`
	BySection map[AssetType]ImportSectionCounts `json:"byAssetType"`
	Index     ImportIndexSummary                `json:"index"`
	Rows      []ImportRowFact                   `json:"rows"`
}

// IndexReadinessStore 回答“这些对象版本的检索文档处于什么状态”。
type IndexReadinessStore interface {
	SearchDocumentStates(context.Context, string, []string) (map[string][]string, error)
}

type JSONReportService struct {
	store     ReportStore
	readiness IndexReadinessStore
}

func NewJSONReportService(store ReportStore, readiness IndexReadinessStore) *JSONReportService {
	return &JSONReportService{store: store, readiness: readiness}
}

func (service *JSONReportService) Generate(
	ctx context.Context,
	tenantID, domainID, importID string,
) (ImportReportJSON, error) {
	if service == nil || service.store == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || !canonicalUUID(importID) {
		return ImportReportJSON{}, ErrImportReportInvalid
	}
	batch, err := service.store.Get(ctx, tenantID, domainID, importID)
	if err != nil {
		return ImportReportJSON{}, err
	}
	report := ImportReportJSON{
		Import:    batch,
		BySection: map[AssetType]ImportSectionCounts{},
		Rows:      []ImportRowFact{},
	}
	if batch.State == StateUploaded || batch.State == StateValidating {
		return report, nil
	}
	rows, err := service.store.ListRows(ctx, tenantID, domainID, importID)
	if err != nil {
		return ImportReportJSON{}, err
	}
	createdVersionIDs := []string{}
	for _, row := range rows {
		fact := buildImportRowFact(batch, row)
		report.Rows = append(report.Rows, fact)
		counts := report.BySection[fact.AssetType]
		applyResolution(&report.Counts, &counts, fact)
		report.BySection[fact.AssetType] = counts
		if row.CreatedVersionID != "" {
			createdVersionIDs = append(createdVersionIDs, row.CreatedVersionID)
		}
	}
	report.Index = ImportIndexSummary{CreatedVersions: len(createdVersionIDs)}
	if service.readiness != nil && len(createdVersionIDs) > 0 {
		states, err := service.readiness.SearchDocumentStates(ctx, tenantID, createdVersionIDs)
		if err != nil {
			return ImportReportJSON{}, err
		}
		for _, versionID := range createdVersionIDs {
			documents := states[versionID]
			if len(documents) == 0 {
				report.Index.AwaitingRelease++
				continue
			}
			report.Index.Indexed++
			ready, failed := true, false
			for _, status := range documents {
				if status == "FAILED" {
					failed = true
				}
				if status != "SUCCEEDED" && status != "SKIPPED" {
					ready = false
				}
			}
			if failed {
				report.Index.EmbeddingFailed++
			} else if ready {
				report.Index.EmbeddingReady++
			}
		}
	} else {
		report.Index.AwaitingRelease = len(createdVersionIDs)
	}
	return report, nil
}

func buildImportRowFact(batch SemanticImport, row ImportRow) ImportRowFact {
	fact := ImportRowFact{
		RowNo: row.RowNo, AssetType: batch.AssetType, State: row.ValidationState,
		Issues:          append([]ValidationIssue{}, row.Errors...),
		CreatedObjectID: row.CreatedObjectID, CreatedVersionID: row.CreatedVersionID,
	}
	var raw map[string]any
	if json.Unmarshal(row.RawJSON, &raw) == nil && raw != nil {
		if batch.AssetType == AssetBundle {
			if value, ok := raw[bundleRowAssetTypeKey].(string); ok {
				fact.AssetType = AssetType(value)
			}
			if value, ok := raw[bundleRowAssetIndexKey].(string); ok {
				fact.BundleAsset = value
			}
		}
		if value, ok := raw[primaryCodeColumn(fact.AssetType)].(string); ok {
			fact.Code = value
		}
		if value, ok := raw["name"].(string); ok {
			fact.Name = value
		}
	}
	fact.Resolution = resolveRowResolution(row)
	return fact
}

func resolveRowResolution(row ImportRow) string {
	willUpdate := false
	unchanged := false
	for _, issue := range row.Errors {
		switch issue.Code {
		case ImportWillUpdate:
			willUpdate = true
		case ImportContentUnchanged:
			unchanged = true
		}
	}
	switch row.ValidationState {
	case RowInvalid:
		return ResolutionFailed
	case RowSkipped:
		if unchanged {
			return ResolutionUnchanged
		}
		return ResolutionSkipped
	default:
		if willUpdate {
			return ResolutionUpdate
		}
		return ResolutionCreate
	}
}

func applyResolution(total, section *ImportSectionCounts, fact ImportRowFact) {
	committed := fact.State == RowCommitted
	switch fact.Resolution {
	case ResolutionFailed:
		total.Failed++
		section.Failed++
	case ResolutionUnchanged:
		total.Unchanged++
		section.Unchanged++
	case ResolutionSkipped:
		total.Skipped++
		section.Skipped++
	case ResolutionUpdate:
		if committed {
			total.Updated++
			section.Updated++
		} else {
			total.Pending++
			section.Pending++
		}
	default:
		if committed {
			total.Created++
			section.Created++
		} else {
			total.Pending++
			section.Pending++
		}
	}
}

// PostgresIndexReadinessStore 从 askdata.search_documents 读取版本的检索文档
// 嵌入状态。
type PostgresIndexReadinessStore struct{ pool *pgxpool.Pool }

func NewPostgresIndexReadinessStore(pool *pgxpool.Pool) *PostgresIndexReadinessStore {
	return &PostgresIndexReadinessStore{pool: pool}
}

func (store *PostgresIndexReadinessStore) SearchDocumentStates(
	ctx context.Context,
	tenantID string,
	versionIDs []string,
) (map[string][]string, error) {
	if store == nil || store.pool == nil || !canonicalUUID(tenantID) ||
		len(versionIDs) == 0 || len(versionIDs) > MaxImportRows {
		return nil, ErrImportReportInvalid
	}
	for _, versionID := range versionIDs {
		if !canonicalUUID(versionID) {
			return nil, ErrImportReportInvalid
		}
	}
	result := map[string][]string{}
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT object_version_id::text,embedding_status
			FROM askdata.search_documents
			WHERE object_version_id=ANY($1::uuid[])
			ORDER BY object_version_id,view_type`, versionIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var versionID, status string
			if err := rows.Scan(&versionID, &status); err != nil {
				return err
			}
			result[versionID] = append(result[versionID], status)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
