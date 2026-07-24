package semanticmanagement

import (
	"net/http"

	"intelligent-report-generation-system/internal/auth"
)

func registerDimensionSurveyRoutes(
	mux *http.ServeMux,
	service *DimensionService,
	protect semanticProtector,
) {
	mux.Handle(
		"GET /api/v1/semantic/dimension-survey-candidates",
		protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page, ok := semanticPage(w, r)
			if !ok {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, total, err := service.ListDimensionSurveyCandidates(
				r.Context(), claims.TenantID, DimensionSurveyFilter{
					Page:             page,
					DatasetID:        r.URL.Query().Get("datasetId"),
					DatasetVersionID: r.URL.Query().Get("datasetVersionId"),
					Status:           r.URL.Query().Get("status"),
					FieldRole:        r.URL.Query().Get("fieldRole"),
				},
			)
			if err != nil {
				writeSemanticError(w, err)
				return
			}
			writeSemanticList(w, items, total, page)
		})),
	)
	mux.Handle(
		"GET /api/v1/semantic/dimension-survey-candidates/{id}",
		protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetDimensionSurveyCandidate(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			if err != nil {
				writeSemanticError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeSemanticJSON(w, http.StatusOK, item)
		})),
	)
	mux.Handle(
		"PUT /api/v1/semantic/dimension-survey-candidates/{id}",
		protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input UpdateDimensionSurveyCandidateInput
			if !decodeSemanticRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.UpdateDimensionSurveyCandidate(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			if err != nil {
				writeSemanticError(w, err)
				return
			}
			writeSemanticJSON(w, http.StatusOK, item)
		})),
	)
	mux.Handle(
		"POST /api/v1/semantic/dimension-survey-candidates/{id}/accept",
		protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input DimensionSurveyDecisionInput
			if !decodeSemanticRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			result, err := service.AcceptDimensionSurveyCandidate(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input.ExpectedVersion,
			)
			if err != nil {
				writeSemanticError(w, err)
				return
			}
			writeSemanticJSON(w, http.StatusOK, result)
		})),
	)
	mux.Handle(
		"POST /api/v1/semantic/dimension-survey-candidates/{id}/reject",
		protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input DimensionSurveyDecisionInput
			if !decodeSemanticRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.RejectDimensionSurveyCandidate(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input.ExpectedVersion, input.Reason,
			)
			if err != nil {
				writeSemanticError(w, err)
				return
			}
			writeSemanticJSON(w, http.StatusOK, item)
		})),
	)
}
