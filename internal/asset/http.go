package asset

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
)

type tableSampler interface {
	SampleTable(context.Context, string, string, datasource.MetadataTable, int) (datasource.SampleResult, error)
}

// NewHandler 注册资产检索、详情、业务元数据、差异和影响分析接口。
func NewHandler(authService *auth.Service, permissions *access.Service, repo *Repository, samplers ...tableSampler) http.Handler {
	mux := http.NewServeMux()
	var sampler tableSampler
	if len(samplers) > 0 {
		sampler = samplers[0]
	}
	protect := func(action string, objectID func(*http.Request) string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATA_ASSET", action, objectID, next))
	}
	tableID := func(request *http.Request) string { return request.PathValue("id") }
	columnTableID := func(request *http.Request) string {
		claims, ok := auth.ClaimsFromContext(request.Context())
		if !ok {
			return ""
		}
		id := "00000000-0000-0000-0000-000000000000"
		_ = database.WithTenantTx(request.Context(), repo.pool, claims.TenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(request.Context(), `SELECT table_id::text FROM platform.metadata_columns WHERE id=$1`, request.PathValue("id")).Scan(&id)
		})
		return id
	}
	list := func(publicOnly bool) http.Handler {
		return protect("READ", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			q := r.URL.Query()
			limit := intParam(q.Get("limit"), 50)
			if limit < 1 || limit > 200 {
				writeError(w, 400, "INVALID_PAGE_SIZE", "limit must be between 1 and 200")
				return
			}
			search := Search{Query: strings.TrimSpace(q.Get("q")), DataSourceID: q.Get("dataSourceId"), SourceType: q.Get("sourceType"), Status: q.Get("status"), Sensitivity: q.Get("sensitivity"), Tag: q.Get("tag"), Visibility: q.Get("visibility"), ManagementStatus: q.Get("managementStatus"), EnrichedOnly: q.Get("enrichedOnly") == "true", Limit: limit, Offset: max(0, intParam(q.Get("offset"), 0))}
			if publicOnly {
				search.Visibility = "TENANT_PUBLIC"
			}
			items, total, err := repo.SearchTables(r.Context(), claims.TenantID, search)
			if err != nil {
				writeError(w, 400, "ASSET_SEARCH_FAILED", "failed to search assets")
				return
			}
			writeJSON(w, 200, map[string]any{"items": items, "total": total, "limit": limit, "offset": search.Offset})
		}))
	}
	mux.Handle("GET /api/v1/assets/tables", list(false))
	mux.Handle("GET /api/v1/assets/catalog", list(true))
	mux.Handle("GET /api/v1/assets/tables/{id}", protect("READ", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		item, err := repo.GetTable(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeError(w, 404, "ASSET_NOT_FOUND", "table asset not found")
			return
		}
		writeJSON(w, 200, item)
	})))
	mux.Handle("GET /api/v1/assets/tables/{id}/columns", protect("READ", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		if _, err := repo.GetTable(r.Context(), c.TenantID, r.PathValue("id")); err != nil {
			writeError(w, 404, "ASSET_NOT_FOUND", "table asset not found")
			return
		}
		items, err := repo.ListColumns(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeError(w, 404, "ASSET_NOT_FOUND", "table asset not found")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})))
	mux.Handle("GET /api/v1/assets/tables/{id}/preview", protect("READ", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sampler == nil {
			writeError(w, 503, "ASSET_PREVIEW_UNAVAILABLE", "table preview is not available")
			return
		}
		maxRows := intParam(r.URL.Query().Get("maxRows"), 5)
		if maxRows < 1 || maxRows > 5 {
			writeError(w, 400, "INVALID_PREVIEW_LIMIT", "maxRows must be between 1 and 5")
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := repo.GetTable(r.Context(), claims.TenantID, r.PathValue("id"))
		if err != nil {
			writeError(w, 404, "ASSET_NOT_FOUND", "table asset not found")
			return
		}
		columns, err := repo.ListColumns(r.Context(), claims.TenantID, item.ID)
		if err != nil {
			writeError(w, 404, "ASSET_NOT_FOUND", "table asset not found")
			return
		}
		result, err := sampler.SampleTable(
			r.Context(), claims.TenantID, item.DataSourceID,
			metadataTableForPreview(item, columns), maxRows,
		)
		if err != nil {
			slog.ErrorContext(r.Context(), "sample table asset", "table_id", item.ID, "data_source_id", item.DataSourceID, "error", err)
			writeError(w, 502, "ASSET_PREVIEW_FAILED", "failed to sample table asset")
			return
		}
		writeJSON(w, 200, result)
	})))
	mux.Handle("PUT /api/v1/assets/tables/{id}/business-metadata", protect("MANAGE", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var input BusinessMetadata
		if !decode(w, r, &input) {
			return
		}
		item, err := repo.UpdateTable(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), input)
		if err != nil {
			writeError(w, 409, "ASSET_UPDATE_FAILED", "invalid metadata or asset version conflict")
			return
		}
		writeJSON(w, 200, item)
	})))
	mux.Handle("PUT /api/v1/assets/columns/{id}/business-metadata", protect("MANAGE", columnTableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var input BusinessMetadata
		if !decode(w, r, &input) {
			return
		}
		item, err := repo.UpdateColumn(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), input)
		if err != nil {
			writeError(w, 409, "ASSET_UPDATE_FAILED", "invalid metadata or asset version conflict")
			return
		}
		writeJSON(w, 200, item)
	})))
	mux.Handle("POST /api/v1/assets/tables/{id}/manual-completion", protect("MANAGE", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var input ManualCompletionInput
		if !decode(w, r, &input) {
			return
		}
		item, err := repo.CompleteTableManually(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), input)
		var incomplete *ManualCompletionIncompleteError
		if errors.As(err, &incomplete) {
			writeError(w, http.StatusUnprocessableEntity, "ASSET_MANUAL_COMPLETION_INCOMPLETE", incomplete.Error())
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "complete table metadata manually", "table_id", r.PathValue("id"), "error", err)
			writeError(w, http.StatusConflict, "ASSET_MANUAL_COMPLETION_FAILED", "手工完善提交失败，资产版本或结构可能已变化")
			return
		}
		writeJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /api/v1/assets/tables/{id}/disable", protect("MANAGE", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		item, err := repo.SetTableManagementStatus(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), "DISABLED")
		if err != nil {
			writeError(w, 409, "ASSET_STATUS_UPDATE_FAILED", "failed to disable table asset")
			return
		}
		writeJSON(w, 200, item)
	})))
	mux.Handle("POST /api/v1/assets/tables/{id}/enable", protect("MANAGE", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		item, err := repo.SetTableManagementStatus(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), "ENABLED")
		if err != nil {
			writeError(w, 409, "ASSET_STATUS_UPDATE_FAILED", "failed to enable table asset")
			return
		}
		writeJSON(w, 200, item)
	})))
	mux.Handle("DELETE /api/v1/assets/tables/{id}", protect("MANAGE", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		if err := repo.DeleteTableAsset(r.Context(), c.TenantID, c.Subject, r.PathValue("id")); err != nil {
			writeError(w, 409, "ASSET_DELETE_FAILED", "failed to delete table asset")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /api/v1/assets/tables/{id}/impact", protect("READ", tableID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := repo.Impact(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeError(w, 500, "IMPACT_QUERY_FAILED", "failed to query downstream impact")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
	})))
	mux.Handle("GET /api/v1/metadata-diffs", protect("READ", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		limit := intParam(r.URL.Query().Get("limit"), 100)
		if limit < 1 || limit > 500 {
			writeError(w, 400, "INVALID_PAGE_SIZE", "limit must be between 1 and 500")
			return
		}
		items, err := repo.ListDiffs(r.Context(), c.TenantID, r.URL.Query().Get("dataSourceId"), limit)
		if err != nil {
			writeError(w, 400, "DIFF_QUERY_FAILED", "failed to query metadata diffs")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})))
	return mux
}

// metadataTableForPreview restores the active column projection that is stored
// separately from the table asset. The connector intentionally treats an empty
// projection as an empty result, so passing only the table identity would make
// every database-backed node preview return zero rows without an error.
func metadataTableForPreview(table Table, columns []Column) datasource.MetadataTable {
	metadataColumns := make([]datasource.MetadataColumn, 0, len(columns))
	for _, column := range columns {
		if column.AssetStatus != "" && column.AssetStatus != "ACTIVE" {
			continue
		}
		metadataColumns = append(metadataColumns, datasource.MetadataColumn{
			Name:            column.ColumnName,
			OrdinalPosition: column.OrdinalPosition,
			SourceComment:   column.SourceComment,
			NativeType:      column.NativeType,
			CanonicalType:   column.CanonicalType,
			Nullable:        column.Nullable,
		})
	}
	return datasource.MetadataTable{
		CatalogName:   table.CatalogName,
		SchemaName:    table.SchemaName,
		Name:          table.TableName,
		Type:          table.TableType,
		SourceComment: table.SourceComment,
		Columns:       metadataColumns,
	}
}

// decode 严格解析资产修改请求并输出统一参数错误。
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, "INVALID_REQUEST", "invalid request body")
		return false
	}
	return true
}

// intParam 读取正整数查询参数，无效时使用默认值。
func intParam(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return value
}

// writeError 输出资产模块的标准错误结构。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

// writeJSON 输出资产模块的 JSON 响应。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
