package datasource

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// NewHandler 注册数据源增删改查、连通测试和同步接口。
func NewHandler(authService *auth.Service, permissions *access.Service, service *Service) http.Handler {
	mux := http.NewServeMux()
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
		writeDSJSON(w, 200, map[string]any{"items": items})
	})))
	mux.Handle("POST /api/v1/data-sources", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Code        string         `json:"code"`
			Name        string         `json:"name"`
			Type        Type           `json:"type"`
			Config      map[string]any `json:"config"`
			SecretRef   string         `json:"secretRef"`
			FileAssetID string         `json:"fileAssetId"`
		}
		if !decodeDS(w, r, &in) {
			return
		}
		created, err := service.Create(r.Context(), Source{TenantID: c.TenantID, Code: in.Code, Name: in.Name, Type: in.Type, Config: in.Config, SecretRef: in.SecretRef, FileAssetID: in.FileAssetID})
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_CREATE_FAILED", "invalid data source configuration or quota exceeded")
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "CREATE", created.ID, map[string]any{"type": created.Type, "status": created.Status})
		writeDSJSON(w, 201, created)
	})))
	mux.Handle("PUT /api/v1/data-sources/{id}", managed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := auth.ClaimsFromContext(r.Context())
		var in struct {
			Code        string         `json:"code"`
			Name        string         `json:"name"`
			Type        Type           `json:"type"`
			Config      map[string]any `json:"config"`
			SecretRef   string         `json:"secretRef"`
			FileAssetID string         `json:"fileAssetId"`
		}
		if !decodeDS(w, r, &in) {
			return
		}
		updated, err := service.Update(r.Context(), Source{ID: r.PathValue("id"), TenantID: c.TenantID, Code: in.Code, Name: in.Name, Type: in.Type, Config: in.Config, SecretRef: in.SecretRef, FileAssetID: in.FileAssetID})
		if err != nil {
			writeDSError(w, 400, "DATA_SOURCE_UPDATE_FAILED", "invalid data source configuration or state")
			return
		}
		auditDS(r, service, c.TenantID, c.Subject, "UPDATE", updated.ID, map[string]any{"type": updated.Type, "status": updated.Status})
		writeDSJSON(w, 200, updated)
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
