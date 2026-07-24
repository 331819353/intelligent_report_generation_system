package datasource

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// NewHandler 注册数据源增删改查、连通测试和同步接口。
func NewHandler(authService *auth.Service, permissions *access.Service, service *Service, managers ...CredentialManager) http.Handler {
	mux := http.NewServeMux()
	var credentials CredentialManager
	if len(managers) > 0 {
		credentials = managers[0]
	}
	managed := func(next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATA_SOURCE", "MANAGE", nil, next))
	}
	mux.Handle("GET /api/v1/data-sources", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		items, err := service.List(r.Context(), c.TenantID)
		if err != nil {
			writeDSError(w, 500, "DATA_SOURCE_LIST_FAILED", "failed to list data sources")
			return
		}
		for index := range items {
			items[index] = publicDataSource(r.Context(), items[index], credentials)
		}
		writeDSJSON(w, 200, map[string]any{"items": items})
	})))
	mux.Handle("POST /api/v1/data-sources", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in dataSourceInput
		if !decodeDS(w, r, &in) {
			return
		}
		if !dataSourceCodePattern.MatchString(strings.TrimSpace(in.Code)) {
			writeDSError(w, http.StatusBadRequest, "DATA_SOURCE_CODE_INVALID", "数据源编码必须以英文字母开头，且只能包含英文字母、数字和下划线，最长 128 位")
			return
		}
		source, err := sourceFromInput(r.Context(), service, credentials, c.TenantID, "", in, false)
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_CONNECTION_CONFIGURATION_INVALID", connectionConfigurationMessage(err))
			return
		}
		if source.OwnerID == "" {
			source.OwnerID = c.Subject
		}
		source.CreatedBy, source.UpdatedBy = c.Subject, c.Subject
		created, err := service.Create(r.Context(), source)
		if err != nil {
			switch {
			case errors.Is(err, ErrCodeConflict):
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_CODE_CONFLICT", "数据源编码已存在，请更换编码后重试")
			case errors.Is(err, ErrQuotaExceeded):
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_QUOTA_EXCEEDED", "数据源数量已达到租户上限，请先删除不再使用的数据源")
			case errors.Is(err, ErrInvalidConfiguration):
				writeDSError(w, http.StatusBadRequest, "DATA_SOURCE_CONFIGURATION_INVALID", "数据源配置无效，请检查名称、编码和文件后重试")
			default:
				slog.ErrorContext(r.Context(), "data source create failed", "type", source.Type, "error", err)
				writeDSError(w, http.StatusInternalServerError, "DATA_SOURCE_CREATE_FAILED", "数据源创建失败，请稍后重试")
			}
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "CREATE", created.ID, map[string]any{"type": created.Type, "status": created.Status})
		writeDSJSON(w, 201, publicDataSource(r.Context(), created, credentials))
	})))
	mux.Handle("PUT /api/v1/data-sources/{id}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in dataSourceInput
		if !decodeDS(w, r, &in) {
			return
		}
		if in.ExpectedVersion < 1 {
			writeDSError(w, http.StatusBadRequest, "DATA_SOURCE_EXPECTED_VERSION_REQUIRED", "expectedVersion must be a positive data source version")
			return
		}
		if !dataSourceCodePattern.MatchString(strings.TrimSpace(in.Code)) {
			writeDSError(w, http.StatusBadRequest, "DATA_SOURCE_CODE_INVALID", "数据源编码必须以英文字母开头，且只能包含英文字母、数字和下划线，最长 128 位")
			return
		}
		source, err := sourceFromInput(r.Context(), service, credentials, c.TenantID, r.PathValue("id"), in, true)
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_CONNECTION_CONFIGURATION_INVALID", connectionConfigurationMessage(err))
			return
		}
		source.UpdatedBy = c.Subject
		updated, err := service.Update(r.Context(), source)
		if err != nil {
			if errors.Is(err, ErrVersionConflict) {
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_VERSION_CONFLICT", "data source changed; reload the latest version before saving")
				return
			}
			if errors.Is(err, ErrReviewPending) {
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_REVIEW_PENDING", "数据源正在审核中；请先撤销申请或等待审核完成")
				return
			}
			writeDSError(w, http.StatusBadRequest, "DATA_SOURCE_UPDATE_FAILED", "invalid data source configuration or state")
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "UPDATE", updated.ID, map[string]any{"type": updated.Type, "status": updated.Status})
		writeDSJSON(w, 200, publicDataSource(r.Context(), updated, credentials))
	})))
	mux.Handle("GET /api/v1/data-sources/{id}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		source, err := service.Get(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeDSError(w, 404, "DATA_SOURCE_NOT_FOUND", "data source was not found")
			return
		}
		writeDSJSON(w, 200, publicDataSource(r.Context(), source, credentials))
	})))
	mux.Handle("GET /api/v1/data-sources/{id}/tables/discovery", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		result, err := service.DiscoverTables(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeDSError(w, 502, "DATA_SOURCE_DISCOVERY_FAILED", "failed to discover source tables")
			return
		}
		writeDSJSON(w, 200, map[string]any{"items": result.Tables, "total": len(result.Tables)})
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/file-inspection", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		inspection, err := service.InspectFileSource(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			slog.ErrorContext(r.Context(), "file data source inspection failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, http.StatusUnprocessableEntity, "DATA_SOURCE_FILE_INSPECTION_FAILED", safeFileError(err))
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "INSPECT_FILE_STRUCTURE", r.PathValue("id"), map[string]any{"sheets": len(inspection.Sheets), "sampleLimit": inspection.SampleLimit})
		writeDSJSON(w, http.StatusOK, inspection)
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/tables/import", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var input struct {
			Tables         []TableSelection   `json:"tables"`
			SampleDataMode MetadataSampleMode `json:"sampleDataMode"`
		}
		if !decodeDS(w, r, &input) {
			return
		}
		job, err := service.QueueImportTablesWithSampleMode(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"),
			input.SampleDataMode, input.Tables,
		)
		if err != nil {
			if errors.Is(err, ErrMetadataJobActive) {
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_METADATA_JOB_ACTIVE", "a metadata job is already active for this data source")
				return
			}
			if errors.Is(err, ErrSamplePolicyDenied) {
				writeDSError(w, http.StatusForbidden, "METADATA_SAMPLE_POLICY_DENIED", "requested sample mode is not allowed by the tenant policy")
				return
			}
			slog.ErrorContext(r.Context(), "data source table import enqueue failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, 400, "DATA_SOURCE_TABLE_IMPORT_FAILED", "failed to submit selected tables for background processing")
			return
		}
		w.Header().Set("Location", "/api/v1/data-sources/"+r.PathValue("id")+"/metadata-jobs/"+job.ID)
		auditDS(r, service, c.TenantID, c.Subject, "QUEUE_IMPORT_TABLE_ASSETS", r.PathValue("id"), map[string]any{
			"jobId": job.ID, "count": len(input.Tables),
			"sampleDataMode": job.SampleDataMode, "samplePolicyVersion": job.SamplePolicyVersion,
		})
		writeDSJSON(w, http.StatusAccepted, job)
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/tables/refresh", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var input struct {
			Mode           MetadataRefreshMode `json:"mode"`
			TableIDs       []string            `json:"tableIds"`
			SampleDataMode MetadataSampleMode  `json:"sampleDataMode"`
		}
		if !decodeDS(w, r, &input) {
			return
		}
		job, err := service.QueueRefreshTablesWithSampleMode(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"),
			input.Mode, input.SampleDataMode, input.TableIDs...,
		)
		if err != nil {
			if errors.Is(err, ErrMetadataJobActive) {
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_METADATA_JOB_ACTIVE", "a metadata job is already active for this data source")
				return
			}
			if errors.Is(err, ErrSamplePolicyDenied) {
				writeDSError(w, http.StatusForbidden, "METADATA_SAMPLE_POLICY_DENIED", "requested sample mode is not allowed by the tenant policy")
				return
			}
			slog.ErrorContext(r.Context(), "data source table refresh enqueue failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, 400, "DATA_SOURCE_TABLE_REFRESH_FAILED", "failed to submit managed tables for background refresh")
			return
		}
		w.Header().Set("Location", "/api/v1/data-sources/"+r.PathValue("id")+"/metadata-jobs/"+job.ID)
		auditDS(r, service, c.TenantID, c.Subject, "QUEUE_REFRESH_TABLE_ASSETS", r.PathValue("id"), map[string]any{
			"jobId": job.ID, "mode": job.Mode, "total": job.Total,
			"targeted":       len(input.TableIDs) > 0,
			"sampleDataMode": job.SampleDataMode, "samplePolicyVersion": job.SamplePolicyVersion,
		})
		writeDSJSON(w, http.StatusAccepted, job)
	})))
	mux.Handle("GET /api/v1/data-sources/{id}/metadata-jobs/latest-active", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		job, err := service.LatestActiveMetadataJob(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			writeDSError(w, 500, "DATA_SOURCE_METADATA_JOB_QUERY_FAILED", "failed to query active metadata job")
			return
		}
		writeDSJSON(w, 200, map[string]any{"job": job})
	})))
	mux.Handle("GET /api/v1/data-sources/{id}/metadata-jobs/{jobId}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		job, err := service.GetMetadataJob(r.Context(), c.TenantID, r.PathValue("id"), r.PathValue("jobId"))
		if errors.Is(err, ErrMetadataJobNotFound) {
			writeDSError(w, 404, "DATA_SOURCE_METADATA_JOB_NOT_FOUND", "metadata job was not found")
			return
		}
		if err != nil {
			writeDSError(w, 500, "DATA_SOURCE_METADATA_JOB_QUERY_FAILED", "failed to query metadata job")
			return
		}
		writeDSJSON(w, 200, job)
	})))
	action := func(run func(contextClaims, *http.Request, string) error) http.Handler {
		return managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			if err := run(contextClaims{claims.TenantID}, r, r.PathValue("id")); err != nil {
				writeDSError(w, 400, "DATA_SOURCE_ACTION_FAILED", "operation is not allowed for the current state")
				return
			}
			w.WriteHeader(204)
		}))
	}
	mux.Handle("POST /api/v1/data-sources/{id}/enable", action(func(c contextClaims, r *http.Request, id string) error {
		err := service.Enable(r.Context(), c.tenantID, id)
		if err == nil {
			claims, _ := auth.ClaimsFromContext(r.Context())
			auditDS(r, service, c.tenantID, claims.Subject, "ENABLE", id, nil)
		}
		return err
	}))
	mux.Handle("POST /api/v1/data-sources/{id}/disable", action(func(c contextClaims, r *http.Request, id string) error {
		err := service.Disable(r.Context(), c.tenantID, id)
		if err == nil {
			claims, _ := auth.ClaimsFromContext(r.Context())
			auditDS(r, service, c.tenantID, claims.Subject, "DISABLE", id, nil)
		}
		return err
	}))
	mux.Handle("DELETE /api/v1/data-sources/{id}", action(func(c contextClaims, r *http.Request, id string) error {
		err := service.Delete(r.Context(), c.tenantID, id)
		if err == nil {
			claims, _ := auth.ClaimsFromContext(r.Context())
			auditDS(r, service, c.tenantID, claims.Subject, "DELETE", id, nil)
		}
		return err
	}))
	mux.Handle("POST /api/v1/data-sources/{id}/test", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		job, err := service.QueueConnectionTest(
			r.Context(), c.TenantID, c.Subject, r.PathValue("id"),
			r.Header.Get("Idempotency-Key"),
		)
		if err != nil {
			if errors.Is(err, ErrIdempotencyKeyInvalid) {
				writeDSError(w, http.StatusBadRequest, "DATA_SOURCE_TEST_IDEMPOTENCY_KEY_INVALID", "Idempotency-Key must contain at most 256 bytes")
				return
			}
			if errors.Is(err, ErrReviewPending) {
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_REVIEW_PENDING", "数据源正在审核中；撤销申请后才能重新测试")
				return
			}
			slog.ErrorContext(r.Context(), "data source connection test enqueue failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, http.StatusInternalServerError, "DATA_SOURCE_TEST_ENQUEUE_FAILED", "failed to enqueue connection test")
			return
		}
		w.Header().Set("Location", "/api/v1/data-sources/"+r.PathValue("id")+"/connection-tests/"+job.ID)
		auditDS(r, service, c.TenantID, c.Subject, "QUEUE_CONNECTION_TEST", r.PathValue("id"), map[string]any{
			"jobId": job.ID, "configVersionId": job.ConfigVersionID,
		})
		writeDSJSON(w, http.StatusAccepted, job)
	})))
	mux.Handle("GET /api/v1/data-sources/{id}/connection-tests/{jobId}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		job, err := service.GetConnectionTest(
			r.Context(), c.TenantID, r.PathValue("id"), r.PathValue("jobId"),
		)
		if errors.Is(err, ErrConnectionTestNotFound) {
			writeDSError(w, http.StatusNotFound, "DATA_SOURCE_CONNECTION_TEST_NOT_FOUND", "connection test job was not found")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "data source connection test query failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, http.StatusInternalServerError, "DATA_SOURCE_CONNECTION_TEST_QUERY_FAILED", "failed to query connection test")
			return
		}
		writeDSJSON(w, http.StatusOK, job)
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/publish", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		published, err := service.Publish(r.Context(), c.TenantID, c.Subject, r.PathValue("id"))
		if err != nil {
			switch {
			case errors.Is(err, ErrConnectionTestPending):
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_TEST_PENDING", "connection test is still running")
			case errors.Is(err, ErrConnectionTestFailed):
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_TEST_FAILED", "current data source version failed its connection test")
			case errors.Is(err, ErrTestRequired):
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_TEST_REQUIRED", "current data source version must pass a connection test before publication")
			case errors.Is(err, ErrTestExpired):
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_TEST_EXPIRED", "connection test has expired; test the current version again")
			case errors.Is(err, ErrSourceVersionChanged):
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_VERSION_CHANGED", "data source configuration changed; test the current version again")
			default:
				slog.ErrorContext(r.Context(), "data source publication failed", "source_id", r.PathValue("id"), "error", err)
				writeDSError(w, http.StatusBadRequest, "DATA_SOURCE_PUBLISH_FAILED", "data source could not be published")
			}
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "PUBLISH", published.ID, map[string]any{
			"configVersionId": published.ConfigVersionID,
		})
		writeDSJSON(w, http.StatusOK, publicDataSource(r.Context(), published, credentials))
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/sync", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		result, err := service.Sync(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			slog.ErrorContext(r.Context(), "data source metadata sync failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, 502, "DATA_SOURCE_SYNC_FAILED", "metadata sync failed")
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "SYNC", r.PathValue("id"), map[string]any{"assets": result.Assets, "snapshotHash": result.SnapshotHash})
		writeDSJSON(w, 200, result)
	})))
	return mux
}

func connectionConfigurationMessage(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "host is forbidden"):
		return "Host 不能填写 127.0.0.1、localhost 或受限元数据地址；如果配置服务运行在 Docker 中、数据库运行在宿主机，请填写 host.docker.internal"
	case strings.Contains(message, "host is invalid"):
		return "Host 只填写主机名或 IP，不要包含 http://、jdbc:、端口、路径或空格"
	case strings.Contains(message, "host, port, database and username are required"):
		return "请完整填写 Host、Port、Database/Service 和 Username；Port 必须是 1–65535 的整数"
	case strings.Contains(message, "password is required"):
		return "新建数据源必须填写密码；修改时留空会保留原密码"
	case strings.Contains(message, "unsupported data source type"):
		return "当前仅支持 MySQL、Oracle 和 Excel/CSV 数据源"
	case strings.Contains(message, "sensitive fields are forbidden"):
		return "请在 Password 输入框填写密码，不要把 password、secretRef 或连接串放入高级配置"
	default:
		return "连接配置无法保存，请检查必填项、Host 格式、端口范围和数据库类型后重试"
	}
}

type dataSourceInput struct {
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	OwnerID         string         `json:"ownerId"`
	Visibility      Visibility     `json:"visibility"`
	Type            Type           `json:"type"`
	Host            string         `json:"host"`
	Port            int            `json:"port"`
	Database        string         `json:"database"`
	Username        string         `json:"username"`
	Password        string         `json:"password"`
	Config          map[string]any `json:"config"`
	FileAssetID     string         `json:"fileAssetId"`
	ExpectedVersion int64          `json:"expectedVersion"`
}

// sourceFromInput 将可展示配置与敏感凭据分流；密码只进入加密管理器，不写入 config。
func sourceFromInput(ctx context.Context, service *Service, credentials CredentialManager, tenantID, id string, in dataSourceInput, update bool) (Source, error) {
	source := Source{
		ID: id, TenantID: tenantID, Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description), OwnerID: strings.TrimSpace(in.OwnerID),
		Visibility: in.Visibility, Type: in.Type, FileAssetID: strings.TrimSpace(in.FileAssetID),
		Version: in.ExpectedVersion,
	}
	config := make(map[string]any, len(in.Config)+4)
	for key, value := range in.Config {
		if strings.EqualFold(key, "password") || strings.EqualFold(key, "secretRef") {
			return Source{}, errors.New("sensitive fields are forbidden in config")
		}
		config[key] = value
	}
	source.Config = config
	if in.Type == TypeExcel {
		return source, nil
	}
	if in.Type != TypeMySQL && in.Type != TypeOracle {
		return Source{}, errors.New("unsupported data source type")
	}

	host, database, username := strings.TrimSpace(in.Host), strings.TrimSpace(in.Database), strings.TrimSpace(in.Username)
	connectionProvided := host != "" || in.Port != 0 || database != "" || username != "" || in.Password != ""
	if !connectionProvided {
		// 更新时由 Service 按租户和 source ID 延用当前内部引用；创建时缺少
		// 结构化连接字段会在领域校验中失败。公网请求永不接受 secretRef。
		return source, nil
	}
	if credentials == nil || host == "" || in.Port < 1 || in.Port > 65535 || database == "" || username == "" {
		return Source{}, errors.New("host, port, database and username are required")
	}
	if err := validateConnectorHost(host); err != nil {
		return Source{}, err
	}
	password := in.Password
	if update && password == "" {
		current, err := service.Get(ctx, tenantID, id)
		if err != nil {
			return Source{}, err
		}
		resolved, err := credentials.Resolve(ctx, current.SecretRef)
		if err != nil {
			return Source{}, err
		}
		password = resolved["password"]
	}
	if password == "" {
		return Source{}, errors.New("password is required")
	}
	ref, err := credentials.Seal(map[string]string{
		"host": host, "port": strconv.Itoa(in.Port), "database": database, "username": username, "password": password,
	})
	if err != nil {
		return Source{}, err
	}
	source.SecretRef = ref
	config["host"], config["port"], config["database"], config["username"] = host, in.Port, database, username
	return source, nil
}

// publicDataSource 补齐旧 ENV 数据源的可查看连接信息，同时确保密码和引用永不进入响应。
func publicDataSource(ctx context.Context, source Source, credentials CredentialManager) Source {
	if credentials == nil || (source.Type != TypeMySQL && source.Type != TypeOracle) {
		return source
	}
	// 新记录已持久化全部可展示字段，无需为普通列表读取解密密码；仅旧 ENV 记录按需补齐。
	complete := true
	for _, key := range []string{"host", "port", "database", "username"} {
		if source.Config[key] == nil || source.Config[key] == "" {
			complete = false
			break
		}
	}
	if complete {
		return source
	}
	resolved, err := credentials.Resolve(ctx, source.SecretRef)
	if err != nil {
		return source
	}
	config := make(map[string]any, len(source.Config)+4)
	for key, value := range source.Config {
		config[key] = value
	}
	for _, key := range []string{"host", "database", "username"} {
		if _, exists := config[key]; !exists && resolved[key] != "" {
			config[key] = resolved[key]
		}
	}
	if _, exists := config["port"]; !exists && resolved["port"] != "" {
		if port, err := strconv.Atoi(resolved["port"]); err == nil {
			config["port"] = port
		}
	}
	source.Config = config
	return source
}

// auditDS 附带请求信息记录操作审计；失败不改变主请求结果。
func auditDS(r *http.Request, service *Service, tenantID, actorID, action, resourceID string, detail any) {
	if err := service.Audit(r.Context(), tenantID, actorID, action, resourceID, detail); err != nil {
		slog.ErrorContext(r.Context(), "data source audit failed", "action", action, "source_id", resourceID, "error", err)
	}
}

type contextClaims struct{ tenantID string }

// decodeDS 严格解析数据源请求体并拒绝未知字段。
func decodeDS(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		writeDSError(w, 400, "INVALID_REQUEST", "invalid request body")
		return false
	}
	return true
}

// writeDSError 输出数据源模块的标准错误结构。
func writeDSError(w http.ResponseWriter, status int, code, message string) {
	writeDSJSON(w, status, map[string]string{"code": code, "message": message})
}

// writeDSJSON 输出数据源模块的 JSON 响应。
func writeDSJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
