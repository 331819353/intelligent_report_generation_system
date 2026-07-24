package semanticmanagement

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// NewHandler registers the tenant taxonomy management API. Existing DATASET
// READ/MANAGE permissions are reused so v60 remains immutable and upgraded
// tenants do not acquire a silently unusable new permission type.
func NewHandler(
	authService *auth.Service,
	permissions *access.Service,
	service *Service,
	dimensionServices ...*DimensionService,
) http.Handler {
	mux := http.NewServeMux()
	protect := func(action string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATASET", action, nil, next))
	}

	mux.Handle("GET /api/v1/semantic/tags", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListTags(r.Context(), claims.TenantID, TagFilter{
			Page: page, Query: r.URL.Query().Get("q"),
			Category: r.URL.Query().Get("category"), Status: r.URL.Query().Get("status"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))
	mux.Handle("POST /api/v1/semantic/tags", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateTagInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.CreateTag(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("PUT /api/v1/semantic/tags/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input UpdateTagInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.UpdateTag(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /api/v1/semantic/tags/{id}/deprecate", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input DeprecateTagInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.DeprecateTag(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input.ExpectedVersion)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))

	mux.Handle("GET /api/v1/semantic/tag-aliases", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListTagAliases(r.Context(), claims.TenantID, AliasFilter{
			Page: page, TagID: r.URL.Query().Get("tagId"),
			Query: r.URL.Query().Get("q"), AliasType: r.URL.Query().Get("aliasType"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))
	mux.Handle("POST /api/v1/semantic/tag-aliases", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateTagAliasInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.CreateTagAlias(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("PUT /api/v1/semantic/tag-aliases/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input UpdateTagAliasInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.UpdateTagAlias(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	mux.Handle("DELETE /api/v1/semantic/tag-aliases/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input DeleteRecordInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		if err := service.DeleteTagAlias(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input.ExpectedRecordVersion); err != nil {
			writeSemanticError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("GET /api/v1/semantic/asset-tag-bindings", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListAssetTagBindings(r.Context(), claims.TenantID, BindingFilter{
			Page: page, TagID: r.URL.Query().Get("tagId"),
			AssetType: r.URL.Query().Get("assetType"), Status: r.URL.Query().Get("status"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))
	mux.Handle("POST /api/v1/semantic/asset-tag-bindings", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateAssetTagBindingInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.CreateAssetTagBinding(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("PUT /api/v1/semantic/asset-tag-bindings/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input UpdateAssetTagBindingInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.UpdateAssetTagBinding(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	mux.Handle("DELETE /api/v1/semantic/asset-tag-bindings/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input DeleteRecordInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		if err := service.DeleteAssetTagBinding(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input.ExpectedRecordVersion); err != nil {
			writeSemanticError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	if len(dimensionServices) > 0 && dimensionServices[0] != nil {
		registerDimensionRoutes(mux, authService, permissions, dimensionServices[0], protect)
	}
	return mux
}

func semanticPage(w http.ResponseWriter, r *http.Request) (Page, bool) {
	page := Page{Limit: defaultPageLimit}
	for key, target := range map[string]*int{"limit": &page.Limit, "offset": &page.Offset} {
		raw := r.URL.Query().Get(key)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeSemanticJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
			return Page{}, false
		}
		*target = value
	}
	if !normalizePage(&page) {
		writeSemanticJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
		return Page{}, false
	}
	return page, true
}

func decodeSemanticRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		writeSemanticJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体不是有效的语义管理 JSON"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeSemanticJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

func writeSemanticError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeSemanticJSON(w, http.StatusBadRequest, map[string]string{"code": "SEMANTIC_REQUEST_INVALID", "message": "语义管理请求无效"})
	case errors.Is(err, ErrNotFound):
		writeSemanticJSON(w, http.StatusNotFound, map[string]string{"code": "SEMANTIC_RESOURCE_NOT_FOUND", "message": "语义资源不存在"})
	case errors.Is(err, ErrConflict):
		writeSemanticJSON(w, http.StatusConflict, map[string]string{"code": "SEMANTIC_VERSION_CONFLICT", "message": "资源已变化、标识重复或状态不允许该操作，请重新加载"})
	case errors.Is(err, ErrIdempotencyConflict):
		writeSemanticJSON(w, http.StatusConflict, map[string]string{"code": "SEMANTIC_IDEMPOTENCY_CONFLICT", "message": "幂等键已用于不同的成员刷新请求"})
	case errors.Is(err, ErrMemberAccessDenied):
		w.Header().Set("Cache-Control", "no-store")
		writeSemanticJSON(w, http.StatusForbidden, map[string]string{"code": "SEMANTIC_MEMBER_ACCESS_DENIED", "message": "当前数据集权限或数据策略不允许读取维度成员"})
	default:
		writeSemanticJSON(w, http.StatusInternalServerError, map[string]string{"code": "SEMANTIC_PERSISTENCE_FAILED", "message": "语义管理服务暂不可用"})
	}
}

func writeSemanticList[T any](w http.ResponseWriter, items []T, total int, page Page) {
	w.Header().Set("Cache-Control", "no-store")
	writeSemanticJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": page.Limit, "offset": page.Offset,
	})
}

func writeSemanticJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
