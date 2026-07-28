package semanticqa

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/dataset"
)

func NewHandler(
	authService *auth.Service,
	permissions *access.Service,
	service *Service,
) http.Handler {
	mux := http.NewServeMux()
	protect := func(action string, next http.Handler) http.Handler {
		return auth.RequireAccessToken(
			authService,
			access.Require(permissions, "DATASET", action, nil, next),
		)
	}
	mux.Handle("GET /api/v1/semantic-qa/settings", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetSettings(r.Context(), claims.TenantID)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("PUT /api/v1/semantic-qa/settings", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input Settings
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.UpdateSettings(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/change-sets", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input CreateChangeSetInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateChangeSet(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/change-sets/from-candidate", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input CreateChangeSetFromCandidateInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateChangeSetFromCandidate(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/change-sets/{id}", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetChangeSet(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/change-sets/{id}/validate", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input ValidateChangeSetInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.ValidateChangeSet(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/change-sets/{id}/apply", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input ApplyChangeSetInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.ApplyChangeSet(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/change-sets/{id}/reject", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input RejectChangeSetInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.RejectChangeSet(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/consumer-contracts", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input CreateConsumerContractInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateConsumerContract(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/consumer-contracts/{id}", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetConsumerContract(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/consumer-contracts/{id}/publish", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input PublishConsumerContractInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.PublishConsumerContract(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/warehouse-dag", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetWarehouseDAG(
				r.Context(), claims.TenantID,
				r.URL.Query().Get("datasetVersionId"),
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/graph/status", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetGraphStatus(r.Context(), claims.TenantID)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/query-plans", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input QueryPlanInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.PlanQuery(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/query-turns", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input QueryTurnInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.PlanQueryTurn(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/query-plans/{id}", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetQueryPlan(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/query-plans/{id}/execute", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input ExecuteQueryPlanInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.ExecuteQueryPlan(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/question-templates", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input CreateQuestionTemplateInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateQuestionTemplate(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/question-templates", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, err := service.ListQuestionTemplates(r.Context(), claims.TenantID)
			writeResponse(w, http.StatusOK, map[string]any{"items": items}, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/golden-question-sets", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input CreateGoldenQuestionSetInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateGoldenQuestionSet(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/golden-question-sets", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, err := service.ListGoldenQuestionSets(r.Context(), claims.TenantID)
			writeResponse(w, http.StatusOK, map[string]any{"items": items}, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/golden-question-sets/{id}/activate", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input ActivateGoldenQuestionSetInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.ActivateGoldenQuestionSet(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/golden-questions", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input CreateGoldenQuestionInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateGoldenQuestion(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/golden-questions", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, err := service.ListGoldenQuestions(
				r.Context(), claims.TenantID, r.URL.Query().Get("setId"),
			)
			writeResponse(w, http.StatusOK, map[string]any{"items": items}, err)
		},
	)))
	mux.Handle("POST /api/v1/semantic-qa/golden-questions/{id}/replay", protect("MANAGE", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.ReplayGoldenQuestion(
				r.Context(), claims.TenantID, claims.Subject, r.PathValue("id"),
			)
			writeResponse(w, http.StatusCreated, item, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/materialization-recommendations", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			lookbackDays, parseErr := optionalPositiveInteger(
				r.URL.Query().Get("lookbackDays"),
			)
			if parseErr != nil {
				writeResponse(w, http.StatusOK, nil, ErrInvalidRequest)
				return
			}
			minimumHits, parseErr := optionalPositiveInteger(
				r.URL.Query().Get("minimumHits"),
			)
			if parseErr != nil {
				writeResponse(w, http.StatusOK, nil, ErrInvalidRequest)
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, err := service.ListMaterializationRecommendations(
				r.Context(), claims.TenantID, lookbackDays, minimumHits,
			)
			writeResponse(w, http.StatusOK, map[string]any{
				"items":  items,
				"policy": "SUGGESTION_ONLY",
			}, err)
		},
	)))
	mux.Handle("GET /api/v1/semantic-qa/analysis-templates", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{
				"items":                 MarketAnalysisTemplates(),
				"generationPolicy":      "REVIEWABLE_DRAFT_ONLY",
				"materializationPolicy": "SUGGESTION_ONLY",
			})
		},
	)))
	return mux
}

func optionalPositiveInteger(value string) (int, error) {
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return 0, ErrInvalidRequest
	}
	return parsed, nil
}

func decodeRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	body := http.MaxBytesReader(w, r.Body, 2<<20)
	defer body.Close()
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求正文无效")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求正文只能包含一个 JSON 对象")
		return false
	}
	return true
}

func writeResponse(w http.ResponseWriter, status int, value any, err error) {
	if err == nil {
		writeJSON(w, status, value)
		return
	}
	switch {
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrUnsafeChange),
		errors.Is(err, dataset.ErrInvalidDocument):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求不满足语义合同")
	case errors.Is(err, ErrNotFound), errors.Is(err, dataset.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "对象不存在")
	case errors.Is(err, ErrConflict), errors.Is(err, dataset.ErrConflict),
		errors.Is(err, ErrInvalidState):
		writeError(w, http.StatusConflict, "CONFLICT", "对象已变化或状态不允许该操作")
	case errors.Is(err, ErrDisabled):
		writeError(w, http.StatusConflict, "SEMANTIC_QA_DISABLED", "租户尚未启用该语义问答能力")
	case errors.Is(err, ErrGraphNotReady):
		writeError(w, http.StatusServiceUnavailable, "SEMANTIC_GRAPH_NOT_READY", "语义图尚未追平权威控制面")
	case errors.Is(err, ErrUnprovenPath):
		writeError(w, http.StatusUnprocessableEntity, "UNPROVEN_PATH", "无法证明完整语义检索链路")
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "语义问答处理失败")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
