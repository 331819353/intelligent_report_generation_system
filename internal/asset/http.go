package asset

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// NewHandler 注册资产检索、详情、业务元数据、差异和影响分析接口。
func NewHandler(authService *auth.Service, permissions *access.Service, repo *Repository) http.Handler {
	mux := http.NewServeMux()
	protect := func(action string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATA_ASSET", action, nil, next))
	}
	list := func(publicOnly bool) http.Handler {
		return protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			q := r.URL.Query()
			limit := intParam(q.Get("limit"), 50)
			if limit < 1 || limit > 200 {
				writeError(w, 400, "INVALID_PAGE_SIZE", "limit must be between 1 and 200")
				return
			}
			search := Search{Query: strings.TrimSpace(q.Get("q")), DataSourceID: q.Get("dataSourceId"), SourceType: q.Get("sourceType"), Status: q.Get("status"), Sensitivity: q.Get("sensitivity"), Tag: q.Get("tag"), Visibility: q.Get("visibility"), Limit: limit, Offset: max(0, intParam(q.Get("offset"), 0))}
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
	mux.Handle("GET /api/v1/assets/tables/{id}", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		item, err := repo.GetTable(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeError(w, 404, "ASSET_NOT_FOUND", "table asset not found")
			return
		}
		writeJSON(w, 200, item)
	})))
	mux.Handle("GET /api/v1/assets/tables/{id}/columns", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("PUT /api/v1/assets/tables/{id}/business-metadata", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("PUT /api/v1/assets/columns/{id}/business-metadata", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mux.Handle("GET /api/v1/assets/tables/{id}/impact", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := repo.Impact(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeError(w, 500, "IMPACT_QUERY_FAILED", "failed to query downstream impact")
			return
		}
		writeJSON(w, 200, map[string]any{"items": items, "total": len(items)})
	})))
	mux.Handle("GET /api/v1/metadata-diffs", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
