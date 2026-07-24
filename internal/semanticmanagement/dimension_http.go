package semanticmanagement

import (
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
)

type semanticProtector func(string, http.Handler) http.Handler

func registerDimensionRoutes(
	mux *http.ServeMux,
	authService *auth.Service,
	permissions *access.Service,
	service *DimensionService,
	protect semanticProtector,
) {
	mux.Handle("GET /api/v1/semantic/dimensions", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListDimensions(r.Context(), claims.TenantID, DimensionFilter{
			Page: page, Query: r.URL.Query().Get("q"),
			DatasetVersionID: r.URL.Query().Get("datasetVersionId"),
			DimensionType:    r.URL.Query().Get("dimensionType"),
			Status:           r.URL.Query().Get("status"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))
	mux.Handle("POST /api/v1/semantic/dimensions", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateDimensionInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.CreateDimension(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("GET /api/v1/semantic/dimensions/{id}", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.GetDimension(r.Context(), claims.TenantID, r.PathValue("id"))
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	mux.Handle("PUT /api/v1/semantic/dimensions/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input UpdateDimensionInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.UpdateDimension(
			r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input,
		)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	mux.Handle("POST /api/v1/semantic/dimensions/{id}/deprecate", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CompatibilityDecisionInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.DeprecateDimension(
			r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input.ExpectedVersion,
		)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	mux.Handle("GET /api/v1/semantic/dimensions/{id}/members", auth.RequireAccessToken(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListDimensionMembers(r.Context(), claims.TenantID, claims.Subject, DimensionMemberFilter{
			Page: page, DimensionID: r.PathValue("id"),
			Query: r.URL.Query().Get("q"), Status: r.URL.Query().Get("status"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))

	mux.Handle("GET /api/v1/semantic/dimension-member-aliases", auth.RequireAccessToken(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListDimensionMemberAliases(r.Context(), claims.TenantID, claims.Subject, DimensionMemberAliasFilter{
			Page: page, DimensionID: r.URL.Query().Get("dimensionId"),
			DimensionMemberID: r.URL.Query().Get("dimensionMemberId"),
			Query:             r.URL.Query().Get("q"),
			AliasType:         r.URL.Query().Get("aliasType"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))
	mux.Handle("POST /api/v1/semantic/dimension-member-aliases", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateDimensionMemberAliasInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.CreateDimensionMemberAlias(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("PUT /api/v1/semantic/dimension-member-aliases/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input UpdateDimensionMemberAliasInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.UpdateDimensionMemberAlias(
			r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input,
		)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	mux.Handle("DELETE /api/v1/semantic/dimension-member-aliases/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CompatibilityDecisionInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		if err := service.DeleteDimensionMemberAlias(
			r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input.ExpectedVersion,
		); err != nil {
			writeSemanticError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("GET /api/v1/semantic/dimension-metric-compatibilities", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListCompatibilities(r.Context(), claims.TenantID, CompatibilityFilter{
			Page: page, DimensionID: r.URL.Query().Get("dimensionId"),
			MetricVersionID: r.URL.Query().Get("metricVersionId"),
			Status:          r.URL.Query().Get("status"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))
	mux.Handle("POST /api/v1/semantic/dimension-metric-compatibilities", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input ProposeCompatibilityInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.ProposeCompatibility(r.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("PUT /api/v1/semantic/dimension-metric-compatibilities/{id}", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input UpdateCompatibilityInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, err := service.UpdateCompatibility(
			r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"), input,
		)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticJSON(w, http.StatusOK, item)
	})))
	for path, decision := range map[string]string{"verify": "VERIFIED", "reject": "REJECTED"} {
		path, decision := path, decision
		mux.Handle("POST /api/v1/semantic/dimension-metric-compatibilities/{id}/"+path,
			protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input CompatibilityDecisionInput
				if !decodeSemanticRequest(w, r, &input) {
					return
				}
				claims, _ := auth.ClaimsFromContext(r.Context())
				item, err := service.DecideCompatibility(
					r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"),
					input.ExpectedVersion, decision,
				)
				if err != nil {
					writeSemanticError(w, err)
					return
				}
				writeSemanticJSON(w, http.StatusOK, item)
			})))
	}

	mux.Handle("POST /api/v1/semantic/dimensions/{id}/member-refresh-jobs", protect("MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input CreateRefreshJobInput
		if !decodeSemanticRequest(w, r, &input) {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		item, created, err := service.CreateRefreshJob(
			r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"),
			r.Header.Get("Idempotency-Key"), input,
		)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		if !created {
			w.Header().Set("Idempotency-Replayed", "true")
			writeSemanticJSON(w, http.StatusOK, item)
			return
		}
		writeSemanticJSON(w, http.StatusCreated, item)
	})))
	mux.Handle("GET /api/v1/semantic/dimension-member-refresh-jobs", protect("READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, ok := semanticPage(w, r)
		if !ok {
			return
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, total, err := service.ListRefreshJobs(r.Context(), claims.TenantID, RefreshJobFilter{
			Page: page, DimensionID: r.URL.Query().Get("dimensionId"),
			Status: r.URL.Query().Get("status"),
		})
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		writeSemanticList(w, items, total, page)
	})))

	search := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 20
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeSemanticJSON(w, http.StatusBadRequest, map[string]string{
					"code": "INVALID_PAGE", "message": "分页参数无效",
				})
				return
			}
			limit = parsed
		}
		claims, _ := auth.ClaimsFromContext(r.Context())
		items, err := service.SearchMemberMetrics(
			r.Context(), claims.TenantID, claims.Subject, r.URL.Query().Get("q"), limit,
		)
		if err != nil {
			writeSemanticError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeSemanticJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
	})
	metricRead := access.Require(permissions, "METRIC", "READ", nil, search)
	mux.Handle(
		"GET /api/v1/semantic/member-metric-search",
		auth.RequireAccessToken(authService, metricRead),
	)
	if service != nil && service.survey != nil {
		registerDimensionSurveyRoutes(mux, service, protect)
	}
}
