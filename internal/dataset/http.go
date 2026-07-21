package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// Previewer 由安全查询运行时实现，数据集 HTTP 层不依赖具体数据库方言。
type Previewer interface {
	Preview(context.Context, string, string, string, PreviewInput) (PreviewResult, error)
	PreviewDraft(context.Context, string, string, string, DraftPreviewInput) (DraftPreviewResult, error)
	PreviewCandidate(context.Context, string, string, CandidatePreviewInput) (CandidatePreviewResult, error)
	PreviewVersion(context.Context, string, string, string, string, PreviewInput) (PreviewResult, error)
	PreviewRevision(context.Context, string, string, string, string, PreviewInput) (PreviewResult, error)
	Cancel(context.Context, string, string, string, string) error
}

// NewHandler 注册 DSL 校验、数据集创建、加载和草稿更新接口。
func NewHandler(authService *auth.Service, permissions *access.Service, service *Service, previewer ...Previewer) http.Handler {
	mux := http.NewServeMux()
	objectID := func(r *http.Request) string { return r.PathValue("id") }
	protect := func(action string, objectID func(*http.Request) string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATASET", action, objectID, next))
	}
	noStore := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			next.ServeHTTP(w, r)
		})
	}
	protectDraftPreview := func(next http.Handler) http.Handler {
		assetRead := access.Require(permissions, "DATA_ASSET", "READ", nil, next)
		datasetRead := access.Require(permissions, "DATASET", "READ", objectID, assetRead)
		datasetManage := access.Require(permissions, "DATASET", "MANAGE", objectID, datasetRead)
		return auth.RequireAccessToken(authService, datasetManage)
	}
	protectCandidatePreview := func(next http.Handler) http.Handler {
		assetRead := access.Require(permissions, "DATA_ASSET", "READ", nil, next)
		datasetManage := access.Require(permissions, "DATASET", "MANAGE", nil, assetRead)
		return auth.RequireAccessToken(authService, datasetManage)
	}
	mux.Handle("POST /api/v1/datasets/validate", protect("MANAGE", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			DSL json.RawMessage `json:"dsl"`
		}
		if !decodeRequest(w, r, &input) {
			return
		}
		prepared, err := service.Validate(input.DSL)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		writeDatasetJSON(w, http.StatusOK, map[string]any{
			"valid": true, "dsl": prepared.Document, "dslHash": prepared.DSLHash,
			"logicalPlan": prepared.LogicalPlan, "planHash": prepared.PlanHash,
		})
	})))
	mux.Handle("POST /api/v1/datasets", protect("MANAGE", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input CreateInput
		if !decodeRequest(w, r, &input) {
			return
		}
		record, err := service.Create(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		writeDatasetJSON(w, http.StatusCreated, record)
	})))
	mux.Handle("GET /api/v1/datasets", protect("READ", nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		limit, offset := 50, 0
		if raw := r.URL.Query().Get("limit"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
				return
			}
			limit = value
		}
		if raw := r.URL.Query().Get("offset"); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
				return
			}
			offset = value
		}
		items, total, err := service.List(r.Context(), claims.TenantID, limit, offset)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		writeDatasetJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
	})))
	mux.Handle("GET /api/v1/datasets/{id}", protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		record, err := service.Get(r.Context(), claims.TenantID, r.PathValue("id"))
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		// 数据集聚合版本会随发布和草稿保存推进，不允许中间缓存返回旧并发基线。
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("GET /api/v1/datasets/{id}/revisions", protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		limit, offset, ok := datasetPage(w, r)
		if !ok {
			return
		}
		items, total, err := service.ListRevisions(r.Context(), claims.TenantID, r.PathValue("id"), limit, offset)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, RevisionPage{Items: items, Total: total, Limit: limit, Offset: offset})
	})))
	mux.Handle("GET /api/v1/datasets/{id}/revisions/{revisionId}", protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		record, err := service.GetRevision(r.Context(), claims.TenantID, r.PathValue("id"), r.PathValue("revisionId"))
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/datasets/{id}/revisions/{revisionId}/rollback", protect("MANAGE", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input RollbackRevisionInput
		if !decodeRequest(w, r, &input) {
			return
		}
		record, err := service.RollbackRevision(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), r.PathValue("revisionId"), input)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("PUT /api/v1/datasets/{id}/draft", protect("MANAGE", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input UpdateInput
		if !decodeRequest(w, r, &input) {
			return
		}
		record, err := service.Update(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/datasets/{id}/disable", protect("MANAGE", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input LifecycleInput
		if !decodeRequest(w, r, &input) {
			return
		}
		record, err := service.Disable(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/datasets/{id}/restore", protect("MANAGE", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input LifecycleInput
		if !decodeRequest(w, r, &input) {
			return
		}
		record, err := service.Restore(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("DELETE /api/v1/datasets/{id}", protect("MANAGE", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input LifecycleInput
		if !decodeRequest(w, r, &input) {
			return
		}
		if err := service.Delete(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input); err != nil {
			writeDatasetError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
	// 发布入口由 PublicationApprovalHandler 独占。基础数据集处理器不注册直接发布
	// 路由，避免被单独挂载时绕过“提交申请 -> 审批 -> 原子发布”的边界。
	mux.Handle("GET /api/v1/datasets/{id}/versions", protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		limit, offset, ok := datasetPage(w, r)
		if !ok {
			return
		}
		items, total, err := service.ListVersions(r.Context(), claims.TenantID, r.PathValue("id"), limit, offset)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
	})))
	mux.Handle("GET /api/v1/datasets/{id}/versions/{versionId}", protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		record, err := service.GetVersion(r.Context(), claims.TenantID, r.PathValue("id"), r.PathValue("versionId"))
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("POST /api/v1/datasets/{id}/versions/{versionId}/rollback", protect("MANAGE", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input RollbackRevisionInput
		if !decodeRequest(w, r, &input) {
			return
		}
		record, err := service.RollbackVersion(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), r.PathValue("versionId"), input)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	mux.Handle("GET /api/v1/datasets/{id}/versions/{versionId}/usage", protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		usage, err := service.GetVersionUsage(r.Context(), claims.TenantID, r.PathValue("id"), r.PathValue("versionId"))
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(w, http.StatusOK, usage)
	})))
	mux.Handle("POST /api/v1/datasets/{id}/versions/{versionId}/status", protect("PUBLISH", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input VersionTransitionInput
		if !decodeRequest(w, r, &input) {
			return
		}
		record, err := service.TransitionVersion(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), r.PathValue("versionId"), input)
		if err != nil {
			writeDatasetError(w, err)
			return
		}
		writeDatasetJSON(w, http.StatusOK, record)
	})))
	if len(previewer) > 0 && previewer[0] != nil {
		mux.Handle("POST /api/v1/datasets/candidate/preview", noStore(protectCandidatePreview(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input CandidatePreviewInput
			if !decodeRequest(w, r, &input) {
				return
			}
			result, err := previewer[0].PreviewCandidate(r.Context(), claims.TenantID, claims.Subject, input)
			if err != nil {
				writeDatasetError(w, err)
				return
			}
			writeDatasetJSON(w, http.StatusOK, result)
		}))))
		mux.Handle("POST /api/v1/datasets/{id}/draft/preview", noStore(protectDraftPreview(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input DraftPreviewInput
			if !decodeRequest(w, r, &input) {
				return
			}
			result, err := previewer[0].PreviewDraft(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
			if err != nil {
				writeDatasetError(w, err)
				return
			}
			writeDatasetJSON(w, http.StatusOK, result)
		}))))
		mux.Handle("POST /api/v1/datasets/{id}/preview", noStore(protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input PreviewInput
			if !decodeRequest(w, r, &input) {
				return
			}
			result, err := previewer[0].Preview(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input)
			if err != nil {
				writeDatasetError(w, err)
				return
			}
			writeDatasetJSON(w, http.StatusOK, result)
		}))))
		mux.Handle("POST /api/v1/datasets/{id}/versions/{versionId}/preview", noStore(protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input PreviewInput
			if !decodeRequest(w, r, &input) {
				return
			}
			result, err := previewer[0].PreviewVersion(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), r.PathValue("versionId"), input)
			if err != nil {
				writeDatasetError(w, err)
				return
			}
			writeDatasetJSON(w, http.StatusOK, result)
		}))))
		mux.Handle("POST /api/v1/datasets/{id}/revisions/{revisionId}/preview", noStore(protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			var input PreviewInput
			if !decodeRequest(w, r, &input) {
				return
			}
			result, err := previewer[0].PreviewRevision(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), r.PathValue("revisionId"), input)
			if err != nil {
				writeDatasetError(w, err)
				return
			}
			writeDatasetJSON(w, http.StatusOK, result)
		}))))
		mux.Handle("POST /api/v1/datasets/{id}/query-runs/{queryId}/cancel", protect("READ", objectID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			if err := previewer[0].Cancel(r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), r.PathValue("queryId")); err != nil {
				writeDatasetError(w, err)
				return
			}
			writeDatasetJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
		})))
	}
	return mux
}

// decodeRequest 严格限制请求大小、未知字段和多余 JSON 文档。
func decodeRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体不是有效的数据集 JSON"})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_REQUEST", "message": "请求体只能包含一个 JSON 文档"})
		return false
	}
	return true
}

// writeDatasetError 将领域错误映射为稳定的 HTTP 状态和 DSL 错误码。
func writeDatasetError(w http.ResponseWriter, err error) {
	var validation *ValidationError
	var publication *PublicationValidationError
	switch {
	case errors.As(err, &validation):
		writeDatasetJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "DSL-001-VALIDATION-FAILED", "message": "数据集 DSL 校验失败", "details": validation.Issues})
	case errors.As(err, &publication):
		writeDatasetJSON(w, http.StatusUnprocessableEntity, map[string]any{"code": "DATASET_PUBLISH_VALIDATION_FAILED", "message": "数据集发布前校验失败", "details": publication.Issues})
	case errors.Is(err, ErrNotFound):
		writeDatasetJSON(w, http.StatusNotFound, map[string]string{"code": "DATASET_NOT_FOUND", "message": "数据集不存在"})
	case errors.Is(err, ErrVersionNotFound):
		writeDatasetJSON(w, http.StatusNotFound, map[string]string{"code": "DATASET_VERSION_NOT_FOUND", "message": "数据集版本不存在"})
	case errors.Is(err, ErrRevisionNotFound):
		writeDatasetJSON(w, http.StatusNotFound, map[string]string{"code": "DATASET_REVISION_NOT_FOUND", "message": "数据集草稿修订不存在"})
	case errors.Is(err, ErrPublicationRequestNotFound):
		writeDatasetJSON(w, http.StatusNotFound, map[string]string{"code": "DATASET_PUBLICATION_REQUEST_NOT_FOUND", "message": "数据集发布审批申请不存在"})
	case errors.Is(err, ErrPublicationRequestConflict):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_PUBLICATION_REQUEST_CONFLICT", "message": "发布审批申请已被其他请求处理，请重新加载"})
	case errors.Is(err, ErrPublicationRequestNotPending):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_PUBLICATION_REQUEST_NOT_PENDING", "message": "发布审批申请当前状态不能执行该操作"})
	case errors.Is(err, ErrVersionRollbackUnavailable):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_VERSION_ROLLBACK_UNAVAILABLE", "message": "发布版本缺少唯一且可验证的源草稿修订，无法安全回滚"})
	case errors.Is(err, ErrVersionUnavailable):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_VERSION_UNAVAILABLE", "message": "数据集版本已失效、废弃或依赖发生变化"})
	case errors.Is(err, ErrConflict):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_VERSION_CONFLICT", "message": "数据集已被其他请求修改，请重新加载"})
	case errors.Is(err, ErrIdempotencyConflict):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_IDEMPOTENCY_CONFLICT", "message": "Idempotency-Key 已绑定其他发布请求"})
	case errors.Is(err, ErrAlreadyExists):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_CODE_CONFLICT", "message": "数据集编码已存在"})
	case errors.Is(err, ErrForbidden):
		writeDatasetJSON(w, http.StatusForbidden, map[string]string{"code": "PERMISSION_DENIED", "message": "没有执行数据集操作的权限"})
	case errors.Is(err, ErrPublishUnavailable):
		writeDatasetJSON(w, http.StatusServiceUnavailable, map[string]string{"code": "DATASET_PUBLISH_UNAVAILABLE", "message": "发布试跑服务暂时不可用"})
	case errors.Is(err, ErrInvalidTransition):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_VERSION_TRANSITION_INVALID", "message": "数据集状态迁移无效"})
	case errors.Is(err, ErrInUse):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "DATASET_IN_USE", "message": "数据集仍被指标、下游数据集、报告草稿或运行中查询占用，暂时不能删除"})
	case errors.Is(err, ErrInvalidDocument):
		// 不向客户端透出 PostgreSQL、源对象或内部实现错误。
		writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "DSL-002-INVALID-DOCUMENT", "message": "数据集文档无效或引用的资源不可用"})
	case errors.Is(err, ErrPreviewInvalid):
		writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "QUERY-001-INVALID-PREVIEW", "message": "预览参数、行数限制或数据集表达式无效"})
	case errors.Is(err, ErrPreviewUnsupported):
		writeDatasetJSON(w, http.StatusUnprocessableEntity, map[string]string{"code": "QUERY-002-UNSUPPORTED-SOURCE", "message": "当前数据源或节点类型尚不支持安全预览"})
	case errors.Is(err, ErrPreviewTimeout):
		writeDatasetJSON(w, http.StatusGatewayTimeout, map[string]string{"code": "QUERY-003-TIMEOUT", "message": "查询已超时并发起取消"})
	case errors.Is(err, ErrQueryNotFound):
		writeDatasetJSON(w, http.StatusNotFound, map[string]string{"code": "QUERY_RUN_NOT_FOUND", "message": "查询不存在、已结束或无权取消"})
	case errors.Is(err, ErrQueryConflict):
		writeDatasetJSON(w, http.StatusConflict, map[string]string{"code": "QUERY_ID_CONFLICT", "message": "查询标识已被使用"})
	case errors.Is(err, ErrPreviewFailed):
		writeDatasetJSON(w, http.StatusBadGateway, map[string]string{"code": "QUERY-004-EXECUTION-FAILED", "message": "数据源查询执行失败"})
	default:
		writeDatasetJSON(w, http.StatusInternalServerError, map[string]string{"code": "DATASET_PERSISTENCE_FAILED", "message": "数据集服务暂时不可用"})
	}
}

func datasetPage(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit, offset = 50, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		raw := r.URL.Query().Get(key)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
			return 0, 0, false
		}
		*target = value
	}
	if limit < 1 || limit > 200 || offset < 0 {
		writeDatasetJSON(w, http.StatusBadRequest, map[string]string{"code": "INVALID_PAGE", "message": "分页参数无效"})
		return 0, 0, false
	}
	return limit, offset, true
}

// writeDatasetJSON 输出数据集模块统一 JSON 响应。
func writeDatasetJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
