package main

import "net/http"

// apiHandlers is the HTTP boundary of the API composition root. Keeping the
// route table separate from dependency construction makes it possible to audit
// the public surface without reading service wiring, and guarantees that route
// aliases share one handler instance.
type apiHandlers struct {
	auth                     http.Handler
	question                 http.Handler
	savedQuestion            http.Handler
	feedbackTicket           http.Handler
	askDataReportAsset       http.Handler
	report                   http.Handler
	reportSchedule           http.Handler
	reportFollow             http.Handler
	decision                 http.Handler
	workItem                 http.Handler
	runtimeConfig            http.Handler
	support                  http.Handler
	dataRequest              http.Handler
	semanticAdmin            http.Handler
	permissionEvaluate       http.Handler
	accessAdmin              http.Handler
	operationalObservability http.Handler
	userLifecycle            http.Handler
	assetScope               http.Handler
	backgroundTask           http.Handler
	dataSourceApproval       http.Handler
	dataSourceAI             http.Handler
	dataSource               http.Handler
	excel                    http.Handler
	asset                    http.Handler
	metadataAI               http.Handler
	datasetAI                http.Handler
	datasetApproval          http.Handler
	materializationControl   http.Handler
	dataset                  http.Handler
}

func newAPIMux(handlers apiHandlers) *http.ServeMux {
	mux := http.NewServeMux()
	handleAll := func(handler http.Handler, patterns ...string) {
		for _, pattern := range patterns {
			mux.Handle(pattern, handler)
		}
	}

	handleAll(handlers.auth, "/api/v1/auth/")
	handleAll(handlers.question,
		"/api/v1/questions", "/api/v1/questions/",
		"/api/v1/conversations", "/api/v1/conversations/",
		"/api/v1/add-to-report-intents/",
	)
	handleAll(handlers.savedQuestion, "/api/v1/askdata/saved-questions", "/api/v1/askdata/saved-questions/")
	handleAll(handlers.feedbackTicket,
		"/api/v1/askdata/feedback-tickets", "/api/v1/askdata/feedback-tickets/",
		"/api/v1/askdata/active-learning-candidates", "/api/v1/askdata/active-learning-candidates/",
	)
	handleAll(handlers.askDataReportAsset, "/api/v1/askdata/report-assets", "/api/v1/askdata/report-assets/")
	handleAll(handlers.report,
		"/api/v1/reports", "/api/v1/reports/",
		"/api/v1/report-data-contexts", "/api/v1/report-component-manifests", "/api/v1/report-card-kinds",
		"/api/v1/report-blueprints/",
		"/api/v1/report-templates", "/api/v1/report-templates/",
		"/api/v1/report-shares/",
	)
	handleAll(handlers.reportSchedule,
		"GET /api/v1/reports/{id}/schedules", "POST /api/v1/reports/{id}/schedules",
		"/api/v1/report-schedules/", "/api/v1/report-deliveries", "/api/v1/report-deliveries/",
	)
	handleAll(handlers.reportFollow,
		"/api/v1/report-follows", "POST /api/v1/reports/{id}/follow", "DELETE /api/v1/reports/{id}/follow",
	)
	handleAll(handlers.decision, "/api/v1/decisions", "/api/v1/decisions/")
	handleAll(handlers.workItem, "/api/v1/work-items", "/api/v1/work-items/")
	handleAll(handlers.runtimeConfig, "/api/v1/runtime-config/")
	handleAll(handlers.support, "/api/v1/support-tickets", "/api/v1/support-tickets/")
	handleAll(handlers.dataRequest, "/api/v1/data-requests", "/api/v1/data-requests/")
	handleAll(handlers.semanticAdmin, "/api/v1/askdata/semantic/")
	handleAll(handlers.permissionEvaluate, "POST /api/v1/permissions/evaluate")
	handleAll(handlers.accessAdmin,
		"/api/v1/domain-catalog", "/api/v1/domain-applications", "/api/v1/domain-applications/",
		"/api/v1/managed-domains", "/api/v1/platform-management/",
		"/api/v1/domains", "/api/v1/domains/", "/api/v1/users", "/api/v1/users/",
		"/api/v1/share-targets",
	)
	handleAll(handlers.operationalObservability, "GET /api/v1/platform-management/observability")
	handleAll(handlers.userLifecycle,
		"GET /api/v1/users/{id}/deactivation-preview",
		"POST /api/v1/users/{id}/deactivation-batches",
		"/api/v1/user-lifecycle-batches/",
	)
	handleAll(handlers.assetScope, "/api/v1/asset-access/")
	handleAll(handlers.backgroundTask, "/api/v1/background-tasks", "/api/v1/background-tasks/")

	handleAll(handlers.dataSourceApproval,
		"POST /api/v1/data-sources/{id}/publish",
		"POST /api/v1/data-sources/{id}/publish-requests",
		"GET /api/v1/data-sources/{id}/publish-requests",
		"POST /api/v1/data-sources/{id}/publish-requests/{requestId}/withdraw",
		"POST /api/v1/data-sources/{id}/publish-requests/{requestId}/approve",
		"POST /api/v1/data-sources/{id}/publish-requests/{requestId}/reject",
	)
	handleAll(handlers.dataSourceAI,
		"POST /api/v1/data-sources/ai/turns", "POST /api/v1/data-sources/{id}/ai/turns",
	)
	handleAll(handlers.dataSource, "/api/v1/data-sources", "/api/v1/data-sources/")
	handleAll(handlers.excel, "/api/v1/excel-files", "/api/v1/excel-files/")
	handleAll(handlers.asset, "/api/v1/assets/", "/api/v1/metadata-diffs")
	handleAll(handlers.metadataAI, "/api/v1/metadata-ai/")

	handleAll(handlers.datasetAI,
		"POST /api/v1/datasets/ai/table-suggestions",
		"POST /api/v1/datasets/ai/intake",
		"POST /api/v1/datasets/ai/proposals", "POST /api/v1/datasets/{id}/ai/proposals",
		"POST /api/v1/datasets/ai/sessions",
		"GET /api/v1/datasets/ai/sessions/{sessionId}",
		"POST /api/v1/datasets/ai/sessions/{sessionId}/intent",
		"POST /api/v1/datasets/ai/sessions/{sessionId}/scope",
		"POST /api/v1/datasets/ai/sessions/{sessionId}/blueprint",
		"POST /api/v1/datasets/ai/sessions/{sessionId}/blueprint/revisions",
		"POST /api/v1/datasets/ai/sessions/{sessionId}/stages",
		"POST /api/v1/datasets/ai/sessions/{sessionId}/events",
		"POST /api/v1/datasets/{id}/ai/session", "GET /api/v1/datasets/{id}/ai/session",
	)
	handleAll(handlers.datasetApproval,
		"POST /api/v1/datasets/{id}/publish",
		"POST /api/v1/datasets/{id}/publish-requests",
		"GET /api/v1/datasets/{id}/publish-requests",
		"POST /api/v1/datasets/{id}/publish-requests/{requestId}/approve",
		"POST /api/v1/datasets/{id}/publish-requests/{requestId}/reject",
	)
	handleAll(handlers.materializationControl,
		"/api/v1/datasets/{id}/materializations/builds",
		"/api/v1/datasets/{id}/materializations/builds/",
	)
	handleAll(handlers.dataset, "/api/v1/datasets", "/api/v1/datasets/")
	return mux
}
