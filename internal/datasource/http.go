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
		source, err := sourceFromInput(r.Context(), service, credentials, c.TenantID, "", in, false)
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_CREATE_FAILED", "invalid data source connection configuration")
			return
		}
		created, err := service.Create(r.Context(), source)
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_CREATE_FAILED", "invalid data source configuration or quota exceeded")
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
		source, err := sourceFromInput(r.Context(), service, credentials, c.TenantID, r.PathValue("id"), in, true)
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_UPDATE_FAILED", "invalid data source connection configuration")
			return
		}
		updated, err := service.Update(r.Context(), source)
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_UPDATE_FAILED", "invalid data source configuration or state")
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
	mux.Handle("POST /api/v1/data-sources/{id}/tables/import", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var input struct {
			Tables []TableSelection `json:"tables"`
		}
		if !decodeDS(w, r, &input) {
			return
		}
		job, err := service.QueueImportTables(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), input.Tables)
		if err != nil {
			if errors.Is(err, ErrMetadataJobActive) {
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_METADATA_JOB_ACTIVE", "a metadata job is already active for this data source")
				return
			}
			slog.ErrorContext(r.Context(), "data source table import enqueue failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, 400, "DATA_SOURCE_TABLE_IMPORT_FAILED", "failed to submit selected tables for background processing")
			return
		}
		w.Header().Set("Location", "/api/v1/data-sources/"+r.PathValue("id")+"/metadata-jobs/"+job.ID)
		auditDS(r, service, c.TenantID, c.Subject, "QUEUE_IMPORT_TABLE_ASSETS", r.PathValue("id"), map[string]any{"jobId": job.ID, "count": len(input.Tables)})
		writeDSJSON(w, http.StatusAccepted, job)
	})))
	mux.Handle("POST /api/v1/data-sources/{id}/tables/refresh", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var input struct {
			Mode     MetadataRefreshMode `json:"mode"`
			TableIDs []string            `json:"tableIds"`
		}
		if !decodeDS(w, r, &input) {
			return
		}
		job, err := service.QueueRefreshTables(r.Context(), c.TenantID, c.Subject, r.PathValue("id"), input.Mode, input.TableIDs...)
		if err != nil {
			if errors.Is(err, ErrMetadataJobActive) {
				writeDSError(w, http.StatusConflict, "DATA_SOURCE_METADATA_JOB_ACTIVE", "a metadata job is already active for this data source")
				return
			}
			slog.ErrorContext(r.Context(), "data source table refresh enqueue failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, 400, "DATA_SOURCE_TABLE_REFRESH_FAILED", "failed to submit managed tables for background refresh")
			return
		}
		w.Header().Set("Location", "/api/v1/data-sources/"+r.PathValue("id")+"/metadata-jobs/"+job.ID)
		auditDS(r, service, c.TenantID, c.Subject, "QUEUE_REFRESH_TABLE_ASSETS", r.PathValue("id"), map[string]any{"jobId": job.ID, "mode": job.Mode, "total": job.Total, "targeted": len(input.TableIDs) > 0})
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
		result, err := service.Test(r.Context(), c.TenantID, r.PathValue("id"))
		if err != nil {
			slog.ErrorContext(r.Context(), "data source connection test failed", "source_id", r.PathValue("id"), "error", err)
			writeDSError(w, 502, "DATA_SOURCE_TEST_FAILED", "connection test failed")
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "TEST", r.PathValue("id"), map[string]any{"serverVersion": result.ServerVersion, "latencyMs": result.LatencyMS})
		writeDSJSON(w, 200, result)
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

type dataSourceInput struct {
	Code        string         `json:"code"`
	Name        string         `json:"name"`
	Type        Type           `json:"type"`
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	Database    string         `json:"database"`
	Username    string         `json:"username"`
	Password    string         `json:"password"`
	Config      map[string]any `json:"config"`
	SecretRef   string         `json:"secretRef"`
	FileAssetID string         `json:"fileAssetId"`
}

// sourceFromInput 将可展示配置与敏感凭据分流；密码只进入加密管理器，不写入 config。
func sourceFromInput(ctx context.Context, service *Service, credentials CredentialManager, tenantID, id string, in dataSourceInput, update bool) (Source, error) {
	source := Source{ID: id, TenantID: tenantID, Code: strings.TrimSpace(in.Code), Name: strings.TrimSpace(in.Name), Type: in.Type, FileAssetID: strings.TrimSpace(in.FileAssetID)}
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
		// 保留旧 secretRef 客户端兼容性；新页面始终走结构化连接字段。
		source.SecretRef = strings.TrimSpace(in.SecretRef)
		return source, nil
	}
	if credentials == nil || host == "" || strings.Contains(host, "://") || in.Port < 1 || in.Port > 65535 || database == "" || username == "" {
		return Source{}, errors.New("host, port, database and username are required")
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
