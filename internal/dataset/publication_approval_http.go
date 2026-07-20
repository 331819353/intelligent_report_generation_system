package dataset

import (
	"net/http"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

// NewPublicationApprovalHandler owns the public publication routes. The legacy /publish path is
// intentionally intercepted as a submission alias so no HTTP caller can bypass human approval.
func NewPublicationApprovalHandler(
	authService *auth.Service,
	permissions *access.Service,
	service *PublicationApprovalService,
) http.Handler {
	mux := http.NewServeMux()
	objectID := func(request *http.Request) string { return request.PathValue("id") }
	protect := func(action string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(authService, access.Require(permissions, "DATASET", action, objectID, next))
	}
	submit := protect("MANAGE", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input SubmitPublicationInput
		if !decodeRequest(writer, request, &input) {
			return
		}
		record, err := service.Submit(request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), input)
		if err != nil {
			writeDatasetError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(writer, http.StatusAccepted, record)
	}))
	mux.Handle("POST /api/v1/datasets/{id}/publish", submit)
	mux.Handle("POST /api/v1/datasets/{id}/publish-requests", submit)
	mux.Handle("GET /api/v1/datasets/{id}/publish-requests", protect("READ", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		limit, offset, ok := datasetPage(writer, request)
		if !ok {
			return
		}
		items, total, err := service.List(request.Context(), claims.TenantID, request.PathValue("id"), limit, offset)
		if err != nil {
			writeDatasetError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(writer, http.StatusOK, PublicationRequestPage{Items: items, Total: total, Limit: limit, Offset: offset})
	})))
	mux.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/approve", protect("PUBLISH", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input ApprovePublicationInput
		if !decodeRequest(writer, request, &input) {
			return
		}
		result, err := service.Approve(
			request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), request.PathValue("requestId"), input,
		)
		if err != nil {
			writeDatasetError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(writer, http.StatusCreated, result)
	})))
	mux.Handle("POST /api/v1/datasets/{id}/publish-requests/{requestId}/reject", protect("PUBLISH", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input RejectPublicationInput
		if !decodeRequest(writer, request, &input) {
			return
		}
		record, err := service.Reject(
			request.Context(), claims.TenantID, claims.Subject, request.PathValue("id"), request.PathValue("requestId"), input,
		)
		if err != nil {
			writeDatasetError(writer, err)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writeDatasetJSON(writer, http.StatusOK, record)
	})))
	return mux
}
