package datarequest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

const maxRequestBodyBytes = 128 << 10

type identityResolver func(context.Context) (Identity, error)

type Handler struct {
	service   *Service
	identity  identityResolver
	protected http.Handler
}

func NewHandler(authService *auth.Service, service *Service) http.Handler {
	return NewHandlerWithIdempotency(authService, service, nil)
}

func NewHandlerWithIdempotency(
	authService *auth.Service,
	service *Service,
	repository platformidempotency.Repository,
) http.Handler {
	protected := newProtectedHandler(service, authenticatedIdentity)
	if repository != nil {
		protected = withIdempotency(protected, repository, authenticatedIdentity)
	}
	return auth.RequireAccessToken(authService, protected)
}

func withIdempotency(
	next http.Handler,
	repository platformidempotency.Repository,
	resolve identityResolver,
) http.Handler {
	return platformidempotency.Middleware(platformidempotency.MiddlewareOptions{
		Repository: repository,
		ResolveIdentity: func(ctx context.Context) (platformidempotency.Identity, error) {
			identity, err := resolve(ctx)
			if err != nil {
				return platformidempotency.Identity{}, err
			}
			return platformidempotency.Identity{
				TenantID: identity.TenantID, ActorID: identity.ActorID,
			}, nil
		},
		Requires:        platformidempotency.RequiresGovernedWrite,
		WriteError:      writeError,
		MaxRequestBytes: maxRequestBodyBytes,
	}, next)
}

func authenticatedIdentity(ctx context.Context) (Identity, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	accessContext, accessOK := database.AccessContextFromContext(ctx)
	if !ok || !accessOK {
		return Identity{}, ErrPermissionDenied
	}
	identity := Identity{
		TenantID: claims.TenantID, DomainID: accessContext.DomainID, ActorID: claims.Subject,
	}
	if !identity.Valid() || accessContext.UserID != claims.Subject {
		return Identity{}, ErrPermissionDenied
	}
	return identity, nil
}

func newProtectedHandler(service *Service, resolve identityResolver) http.Handler {
	handler := &Handler{service: service, identity: resolve}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/data-requests", handler.create)
	mux.HandleFunc("GET /api/v1/data-requests", handler.list)
	mux.HandleFunc("GET /api/v1/data-requests/{requestId}", handler.get)
	mux.HandleFunc("POST /api/v1/data-requests/{requestId}/submit", handler.submit)
	mux.HandleFunc("POST /api/v1/data-requests/{requestId}/transition", handler.transition)
	mux.HandleFunc("POST /api/v1/data-requests/{requestId}/exports", handler.enqueueExport)
	handler.protected = mux
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(writer, request)
	})
}

func (handler *Handler) create(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "创建申请不接受查询参数")
		return
	}
	var input CreateInput
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeServiceError(writer, err)
		return
	}
	result, err := handler.service.Create(request.Context(), identity, input)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (handler *Handler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	limit := 50
	for key, values := range request.URL.Query() {
		if key != "limit" || len(values) != 1 {
			writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "查询参数无效")
			return
		}
	}
	if raw := request.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "limit 必须是整数")
			return
		}
		limit = value
	}
	items, err := handler.service.List(request.Context(), identity, limit)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, struct {
		Items []Request `json:"items"`
	}{Items: items})
}

func (handler *Handler) get(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "申请详情不接受查询参数")
		return
	}
	result, err := handler.service.Get(request.Context(), identity, request.PathValue("requestId"))
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) submit(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "提交申请不接受查询参数")
		return
	}
	var input struct {
		RecordVersion int64 `json:"recordVersion"`
	}
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeServiceError(writer, err)
		return
	}
	result, err := handler.service.Submit(
		request.Context(), identity, request.PathValue("requestId"), input.RecordVersion,
	)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) transition(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "状态流转不接受查询参数")
		return
	}
	var input TransitionInput
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeServiceError(writer, err)
		return
	}
	result, err := handler.service.Transition(
		request.Context(), identity, request.PathValue("requestId"), input,
	)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *Handler) enqueueExport(writer http.ResponseWriter, request *http.Request) {
	identity, ok := handler.resolveIdentity(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "受控导出不接受查询参数")
		return
	}
	var input struct {
		RecordVersion int64 `json:"recordVersion"`
	}
	if err := decodeStrictJSON(writer, request, &input); err != nil {
		writeServiceError(writer, err)
		return
	}
	job, err := handler.service.EnqueueExport(
		request.Context(), identity, request.PathValue("requestId"), input.RecordVersion,
	)
	if err != nil {
		writeServiceError(writer, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, job)
}

func (handler *Handler) resolveIdentity(
	writer http.ResponseWriter, request *http.Request,
) (Identity, bool) {
	if handler == nil || handler.service == nil || handler.identity == nil {
		writeError(writer, http.StatusInternalServerError, "DATAREQ_SERVICE_FAILED", "申请服务暂时不可用")
		return Identity{}, false
	}
	identity, err := handler.identity(request.Context())
	if err != nil || !identity.Valid() {
		writeError(writer, http.StatusUnauthorized, "DATAREQ_AUTHENTICATION_REQUIRED", "需要有效的领域登录状态")
		return Identity{}, false
	}
	return identity, true
}

func decodeStrictJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	if mediaType := strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]); mediaType != "application/json" {
		return ErrInvalidRequest
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalidRequest
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrInvalidRequest
	}
	return nil
}

func writeServiceError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeError(writer, http.StatusBadRequest, "DATAREQ_INVALID_REQUEST", "取数申请内容无效")
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrSourceRunNotFound):
		writeError(writer, http.StatusNotFound, "DATAREQ_NOT_FOUND", "取数申请或来源问数不存在")
	case errors.Is(err, ErrApproverUnavailable):
		writeError(writer, http.StatusConflict, "DATAREQ_APPROVER_UNAVAILABLE", "当前领域尚未配置申请审批人")
	case errors.Is(err, ErrSecurityCosignRequired):
		writeError(writer, http.StatusConflict, "DATAREQ_SECURITY_COSIGN_REQUIRED", "敏感明细申请需要独立安全会签")
	case errors.Is(err, ErrInvalidTransition):
		writeError(writer, http.StatusConflict, "DATAREQ_TRANSITION_INVALID", "当前状态不接受该操作")
	case errors.Is(err, ErrPermissionDenied):
		writeError(writer, http.StatusForbidden, "DATAREQ_PERMISSION_DENIED", "无权执行该申请操作")
	case errors.Is(err, ErrVersionConflict):
		writeError(writer, http.StatusConflict, "DATAREQ_VERSION_CONFLICT", "申请状态已变化，请刷新后重试")
	case errors.Is(err, ErrControlledExportExpired):
		writeError(writer, http.StatusGone, "DATAREQ_EXPORT_EXPIRED", "受控导出已过期")
	case errors.Is(err, ErrControlledExportLimit):
		writeError(writer, http.StatusConflict, "DATAREQ_EXPORT_DOWNLOAD_LIMIT", "受控导出下载次数已用尽")
	case errors.Is(err, ErrControlledExportNotReady):
		writeError(writer, http.StatusConflict, "DATAREQ_EXPORT_NOT_READY", "受控导出尚未就绪")
	case errors.Is(err, ErrControlledExportInvalid):
		writeError(writer, http.StatusBadRequest, "DATAREQ_EXPORT_INVALID", "受控导出请求无效")
	default:
		writeError(writer, http.StatusInternalServerError, "DATAREQ_SERVICE_FAILED", "取数申请服务暂时不可用")
	}
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]string{"code": code, "message": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
