package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"intelligent-report-generation-system/internal/auth"
)

type PlatformAdministratorChecker interface {
	IsPlatformAdministrator(ctx context.Context, tenantID, userID string) (bool, error)
}

type SnapshotReader interface {
	Snapshot(ctx context.Context, tenantID, windowLabel string, since time.Time) (OperationalSnapshot, error)
}

// NewOperationalHandler exposes aggregate operational health only to platform
// administrators. The route intentionally does not require a selected domain.
func NewOperationalHandler(authService *auth.Service, checker PlatformAdministratorChecker, store SnapshotReader) http.Handler {
	return auth.RequireTenantAccessToken(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/platform-management/observability" {
			writeOperationalError(w, http.StatusNotFound, "NOT_FOUND", "接口不存在")
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		allowed, err := checker.IsPlatformAdministrator(r.Context(), claims.TenantID, claims.Subject)
		if err != nil {
			writeOperationalError(w, http.StatusInternalServerError, "PLATFORM_ADMIN_CHECK_FAILED", "平台管理员身份校验失败")
			return
		}
		if !allowed {
			writeOperationalError(w, http.StatusForbidden, "PLATFORM_ADMIN_REQUIRED", "仅平台管理员可查看运行观测")
			return
		}
		label, duration, ok := operationalWindow(r.URL.Query().Get("window"))
		if !ok {
			writeOperationalError(w, http.StatusBadRequest, "OBSERVABILITY_WINDOW_INVALID", "时间窗口仅支持 1h、6h、24h 或 7d")
			return
		}
		snapshot, err := store.Snapshot(r.Context(), claims.TenantID, label, time.Now().UTC().Add(-duration))
		if err != nil {
			writeOperationalError(w, http.StatusInternalServerError, "OBSERVABILITY_LOAD_FAILED", "运行观测数据加载失败")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(snapshot)
	}))
}

func operationalWindow(value string) (string, time.Duration, bool) {
	switch value {
	case "", "24h":
		return "24h", 24 * time.Hour, true
	case "1h":
		return "1h", time.Hour, true
	case "6h":
		return "6h", 6 * time.Hour, true
	case "7d":
		return "7d", 7 * 24 * time.Hour, true
	default:
		return "", 0, false
	}
}

func writeOperationalError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": message})
}
