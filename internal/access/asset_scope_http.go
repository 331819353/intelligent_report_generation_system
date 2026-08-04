package access

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/auth"
)

func NewAssetScopeHandler(
	authService *auth.Service, store *AssetScopeStore,
) http.Handler {
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, next)
	}
	mux.Handle("GET /api/v1/asset-access/{resourceType}/{resourceId}", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		sharing, err := store.Get(
			r.Context(), claims.TenantID, r.PathValue("resourceType"),
			r.PathValue("resourceId"),
		)
		if err != nil {
			status, code := http.StatusNotFound, "ASSET_ACCESS_NOT_FOUND"
			if !errors.Is(err, pgx.ErrNoRows) {
				status, code = http.StatusBadRequest, "ASSET_ACCESS_INVALID"
			}
			writeError(w, status, code, "asset is not available in the selected domain")
			return
		}
		writeJSON(w, http.StatusOK, sharing)
	})))
	mux.Handle("PATCH /api/v1/asset-access/{resourceType}/{resourceId}", authenticated(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		var input struct {
			SharingScope string `json:"sharingScope"`
		}
		if !decodeAdmin(w, r, &input) {
			return
		}
		sharing, err := store.Update(
			r.Context(), claims.TenantID, claims.Subject,
			r.PathValue("resourceType"), r.PathValue("resourceId"),
			input.SharingScope,
		)
		if err != nil {
			status := http.StatusBadRequest
			code := "ASSET_SHARING_UPDATE_FAILED"
			message := strings.TrimSpace(err.Error())
			if errors.Is(err, ErrAssetSharingOwnerDomainRequired) {
				status = http.StatusForbidden
				code = "ASSET_SHARING_OWNER_DOMAIN_REQUIRED"
				message = "only the asset owner or domain administrator in the owning domain can change its sharing scope"
			}
			writeError(
				w, status, code, message,
			)
			return
		}
		writeJSON(w, http.StatusOK, sharing)
	})))
	return mux
}
