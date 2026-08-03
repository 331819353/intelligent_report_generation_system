package semanticqa

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
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
	mux.Handle("POST /api/v1/questions", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input QuestionRequest
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			answerQuestionHTTP(
				w, r, service, claims.TenantID, claims.Subject, input,
			)
		},
	)))
	mux.Handle("GET /api/v1/questions/{id}", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetQuestion(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			writeResponse(w, http.StatusOK, item, err)
		},
	)))
	mux.Handle("GET /api/v1/questions/{id}/events", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, err := service.ListQuestionEvents(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			writeResponse(w, http.StatusOK, map[string]any{"items": items}, err)
		},
	)))
	mux.Handle("POST /api/v1/questions/{id}/clarifications", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input QuestionRequest
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			parent, err := service.GetQuestion(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			if err != nil {
				writeResponse(w, http.StatusOK, nil, err)
				return
			}
			if parent.State != QuestionStateClarificationRequired ||
				hashText(strings.TrimSpace(input.Question)) != parent.QuestionHash {
				writeResponse(w, http.StatusOK, nil, ErrInvalidState)
				return
			}
			input.ConversationID = parent.ConversationID
			input.ParentQuestionID = parent.QuestionID
			answerQuestionHTTP(
				w, r, service, claims.TenantID, claims.Subject, input,
			)
		},
	)))
	mux.Handle("POST /api/v1/questions/{id}/feedback", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input SubmitQueryFeedbackInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			run, err := service.GetQuestion(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			if err != nil {
				writeResponse(w, http.StatusOK, nil, err)
				return
			}
			if run.State != QuestionStateAnswered || len(run.QueryPlanIDs) == 0 {
				writeResponse(w, http.StatusOK, nil, ErrInvalidState)
				return
			}
			items := make([]QueryFeedback, 0, len(run.QueryPlanIDs))
			for _, planID := range run.QueryPlanIDs {
				item, submitErr := service.SubmitQueryFeedback(
					r.Context(), claims.TenantID, claims.Subject, planID, input,
				)
				if submitErr != nil {
					writeResponse(w, http.StatusOK, nil, submitErr)
					return
				}
				items = append(items, item)
			}
			writeResponse(w, http.StatusOK, map[string]any{"items": items}, nil)
		},
	)))
	mux.Handle("POST /api/v1/questions/{id}/cancel", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CancelQuestion(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			writeResponse(w, http.StatusAccepted, item, err)
		},
	)))
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
	mux.Handle("POST /api/v1/semantic-qa/tokenize", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input QueryTokenizeInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.TokenizeQuery(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
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
			streaming := strings.Contains(
				strings.ToLower(r.Header.Get("Accept")),
				"application/x-ndjson",
			)
			if !streaming {
				item, err := service.PlanQueryTurn(
					r.Context(), claims.TenantID, claims.Subject, input,
				)
				writeResponse(w, http.StatusCreated, item, err)
				return
			}
			flusher, ok := w.(http.Flusher)
			if !ok {
				writeError(
					w, http.StatusInternalServerError, "QUERY_STREAM_UNAVAILABLE",
					"当前服务不支持问答进度流",
				)
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			encoder := json.NewEncoder(w)
			writeFrame := func(value any) {
				if err := encoder.Encode(value); err == nil {
					flusher.Flush()
				}
			}
			ctx := withQueryTurnProgressReporter(
				r.Context(), func(event QueryTurnProgressEvent) {
					writeFrame(queryTurnProgressFrame{
						Type: "progress", Progress: event,
					})
				},
			)
			item, err := service.PlanQueryTurn(
				ctx, claims.TenantID, claims.Subject, input,
			)
			if err != nil {
				status, code, message := responseError(err)
				writeFrame(queryTurnErrorFrame{
					Type: "error", Status: status,
					Error: queryTurnStreamError{Code: code, Message: message},
				})
				return
			}
			writeFrame(queryTurnResultFrame{Type: "result", Result: item})
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
	mux.Handle("POST /api/v1/semantic-qa/query-plans/{id}/feedback", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			var input SubmitQueryFeedbackInput
			if !decodeRequest(w, r, &input) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.SubmitQueryFeedback(
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
	mux.Handle("GET /api/v1/semantic-qa/golden-question-sets/{id}/evaluation-gate", protect("READ", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetEvaluationReleaseGate(
				r.Context(), claims.TenantID, r.PathValue("id"),
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

func answerQuestionHTTP(
	w http.ResponseWriter,
	r *http.Request,
	service *Service,
	tenantID, actorID string,
	input QuestionRequest,
) {
	streaming := strings.Contains(
		strings.ToLower(r.Header.Get("Accept")), "application/x-ndjson",
	)
	if !streaming {
		item, err := service.AnswerQuestion(
			r.Context(), tenantID, actorID, input,
		)
		writeResponse(w, http.StatusCreated, item, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(
			w, http.StatusInternalServerError, "QUESTION_STREAM_UNAVAILABLE",
			"当前服务不支持问答进度流",
		)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	encoder := json.NewEncoder(w)
	writeFrame := func(value any) {
		if err := encoder.Encode(value); err == nil {
			flusher.Flush()
		}
	}
	ctx := withQueryTurnProgressReporter(
		r.Context(), func(event QueryTurnProgressEvent) {
			writeFrame(queryTurnProgressFrame{Type: "progress", Progress: event})
		},
	)
	item, err := service.AnswerQuestion(ctx, tenantID, actorID, input)
	if err != nil {
		status, code, message := responseError(err)
		writeFrame(queryTurnErrorFrame{
			Type: "error", Status: status,
			Error: queryTurnStreamError{Code: code, Message: message},
		})
		return
	}
	writeFrame(questionResultFrame{Type: "result", Result: item})
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
	status, code, message := responseError(err)
	writeError(w, status, code, message)
}

func responseError(err error) (int, string, string) {
	var providerErr *aiplatform.ProviderError
	switch {
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrUnsafeChange),
		errors.Is(err, dataset.ErrInvalidDocument):
		return http.StatusBadRequest, "INVALID_REQUEST", "请求不满足语义合同"
	case errors.Is(err, ErrNotFound), errors.Is(err, dataset.ErrNotFound):
		return http.StatusNotFound, "NOT_FOUND", "对象不存在"
	case errors.Is(err, ErrConflict), errors.Is(err, dataset.ErrConflict),
		errors.Is(err, ErrInvalidState):
		return http.StatusConflict, "CONFLICT", "对象已变化或状态不允许该操作"
	case errors.Is(err, ErrDisabled):
		return http.StatusConflict, "SEMANTIC_QA_DISABLED", "租户尚未启用该语义问答能力"
	case errors.Is(err, ErrGraphNotReady):
		return http.StatusServiceUnavailable, "SEMANTIC_GRAPH_NOT_READY", "语义图尚未追平权威控制面"
	case errors.Is(err, ErrUnprovenPath):
		return http.StatusUnprocessableEntity, "UNPROVEN_PATH", "无法证明完整语义检索链路"
	case errors.As(err, &providerErr):
		switch providerErr.Code {
		case aiplatform.ErrorCodeProviderUnavailable:
			return http.StatusServiceUnavailable, string(providerErr.Code), "智能问答模型服务暂时不可用"
		case aiplatform.ErrorCodeTimeout:
			return http.StatusGatewayTimeout, string(providerErr.Code), "智能问答模型调用超时"
		case aiplatform.ErrorCodeRateLimited:
			return http.StatusTooManyRequests, string(providerErr.Code), "智能问答模型服务暂时限流"
		case aiplatform.ErrorCodeToolNoProgress,
			aiplatform.ErrorCodeToolExecutionBlocked,
			aiplatform.ErrorCodeInvalidOutput,
			aiplatform.ErrorCodeInvalidResponse:
			return http.StatusBadGateway, string(providerErr.Code), "智能问答 Evidence Loop 未通过安全校验"
		default:
			return http.StatusBadGateway, string(providerErr.Code), "智能问答模型调用失败"
		}
	default:
		return http.StatusInternalServerError, "INTERNAL_ERROR", "语义问答处理失败"
	}
}

type queryTurnProgressFrame struct {
	Type     string                 `json:"type"`
	Progress QueryTurnProgressEvent `json:"progress"`
}

type queryTurnResultFrame struct {
	Type   string        `json:"type"`
	Result QueryTurnPlan `json:"result"`
}

type questionResultFrame struct {
	Type   string           `json:"type"`
	Result QuestionResponse `json:"result"`
}

type queryTurnStreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type queryTurnErrorFrame struct {
	Type   string               `json:"type"`
	Status int                  `json:"status"`
	Error  queryTurnStreamError `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
