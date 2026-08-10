package reportasset

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
)

type CertificationRepository interface {
	ListAssets(context.Context, CertificationIdentity) ([]AssetView, error)
	Certify(context.Context, CertificationIdentity, askdata.ID, string, askdata.ContentHash, string) error
	Revoke(context.Context, CertificationIdentity, askdata.ID) error
}
type CertificationHandler struct{ repository CertificationRepository }

func NewCertificationHandler(authService *auth.Service, repository CertificationRepository) http.Handler {
	handler := &CertificationHandler{repository: repository}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/askdata/report-assets", handler.list)
	mux.HandleFunc("POST /api/v1/askdata/report-assets/{id}/certify", handler.certify)
	mux.HandleFunc("POST /api/v1/askdata/report-assets/{id}/revoke", handler.revoke)
	return auth.RequireAccessToken(authService, mux)
}
func (handler *CertificationHandler) list(writer http.ResponseWriter, request *http.Request) {
	identity, ok := certificationIdentity(writer, request)
	if !ok {
		return
	}
	items, err := handler.repository.ListAssets(request.Context(), identity)
	if err != nil {
		http.Error(writer, "report asset unavailable", http.StatusForbidden)
		return
	}
	writeAssetJSON(writer, http.StatusOK, map[string]any{"items": items})
}
func (handler *CertificationHandler) certify(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := certificationSubject(writer, request)
	if !ok {
		return
	}
	var body struct {
		Role                 string              `json:"approverRole"`
		ComponentContentHash askdata.ContentHash `json:"componentContentHash"`
		Note                 string              `json:"note"`
	}
	if decodeAssetJSON(request, &body) != nil {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return
	}
	if err := handler.repository.Certify(request.Context(), identity, id, strings.ToUpper(body.Role), body.ComponentContentHash, body.Note); err != nil {
		http.Error(writer, "certification rejected", http.StatusForbidden)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
func (handler *CertificationHandler) revoke(writer http.ResponseWriter, request *http.Request) {
	identity, id, ok := certificationSubject(writer, request)
	if !ok {
		return
	}
	if err := handler.repository.Revoke(request.Context(), identity, id); err != nil {
		http.Error(writer, "revoke rejected", http.StatusForbidden)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}
func certificationIdentity(writer http.ResponseWriter, request *http.Request) (CertificationIdentity, bool) {
	claims, ok := auth.ClaimsFromContext(request.Context())
	access, accessOK := database.AccessContextFromContext(request.Context())
	if !ok || !accessOK || claims.Subject != access.UserID || access.DomainID == "" {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return CertificationIdentity{}, false
	}
	identity := CertificationIdentity{TenantID: askdata.ID(claims.TenantID), DomainID: askdata.ID(access.DomainID), ActorID: askdata.ID(claims.Subject)}
	if identity.Validate() != nil {
		http.Error(writer, "forbidden", http.StatusForbidden)
		return CertificationIdentity{}, false
	}
	return identity, true
}
func certificationSubject(writer http.ResponseWriter, request *http.Request) (CertificationIdentity, askdata.ID, bool) {
	identity, ok := certificationIdentity(writer, request)
	if !ok {
		return CertificationIdentity{}, "", false
	}
	id := askdata.ID(request.PathValue("id"))
	if id.Validate() != nil {
		http.Error(writer, "invalid asset", http.StatusBadRequest)
		return CertificationIdentity{}, "", false
	}
	return identity, id, true
}
func decodeAssetJSON(request *http.Request, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 16<<10))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}
func writeAssetJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
