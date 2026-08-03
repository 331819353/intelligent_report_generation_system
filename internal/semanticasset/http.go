package semanticasset

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/auth"
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

	mux.Handle("GET /api/v1/semantic-assets/catalog", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page, ok := requestPage(w, r)
			if !ok {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.Catalog(
				r.Context(), claims.TenantID,
				CatalogFilter{
					Page: page, Query: r.URL.Query().Get("q"),
					ObjectType: r.URL.Query().Get("objectType"),
					Status:     r.URL.Query().Get("status"),
					Ready:      r.URL.Query().Get("ready"),
				},
			)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("GET /api/v1/semantic-assets", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page, ok := requestPage(w, r)
			if !ok {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, total, err := service.List(
				r.Context(), claims.TenantID,
				Filter{
					Page: page, Query: r.URL.Query().Get("q"),
					KnowledgeType:   r.URL.Query().Get("knowledgeType"),
					Status:          r.URL.Query().Get("status"),
					EmbeddingStatus: r.URL.Query().Get("embeddingStatus"),
				},
			)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{
				"items": items, "total": total,
				"limit": page.Limit, "offset": page.Offset,
			})
		}),
	))

	mux.Handle("GET /api/v1/semantic-assets/readiness", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.Readiness(r.Context(), claims.TenantID)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("GET /api/v1/semantic-assets/releases", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page, ok := requestPage(w, r)
			if !ok {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, total, err := service.ListSemanticReleases(
				r.Context(), claims.TenantID, page,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{
				"items": items, "total": total,
				"limit": page.Limit, "offset": page.Offset,
			})
		}),
	))

	mux.Handle("GET /api/v1/semantic-assets/releases/active", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetActiveSemanticRelease(
				r.Context(), claims.TenantID,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("GET /api/v1/semantic-assets/releases/{id}", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.GetSemanticRelease(
				r.Context(), claims.TenantID, r.PathValue("id"),
			)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("POST /api/v1/semantic-assets/releases", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input CreateSemanticReleaseInput
			if !decodeRequest(w, r, &input, 32<<20) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateSemanticRelease(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, item)
		}),
	))

	mux.Handle("POST /api/v1/semantic-assets/releases/{id}/validate", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input ValidateSemanticReleaseInput
			if !decodeRequest(w, r, &input, 64<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.ValidateSemanticRelease(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("POST /api/v1/semantic-assets/releases/{id}/activate", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input ActivateSemanticReleaseInput
			if !decodeRequest(w, r, &input, 64<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.ActivateSemanticRelease(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("GET /api/v1/semantic-assets/types", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, err := service.ListKnowledgeTypes(
				r.Context(), claims.TenantID,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"items": items})
		}),
	))

	mux.Handle("POST /api/v1/semantic-assets", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input UpsertInput
			if !decodeRequest(w, r, &input, 256<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.Create(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, item)
		}),
	))

	mux.Handle("POST /api/v1/semantic-assets/import", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input ImportInput
			if !decodeRequest(w, r, &input, 2<<20) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			result, err := service.Import(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
		}),
	))

	mux.Handle("PUT /api/v1/semantic-assets/{id}", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input UpdateInput
			if !decodeRequest(w, r, &input, 256<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.Update(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("POST /api/v1/semantic-assets/{id}/deprecate", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input DeprecateInput
			if !decodeRequest(w, r, &input, 64<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.Deprecate(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input.ExpectedVersion,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("GET /api/v1/semantic-parsing-rules", protect(
		"READ", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			page, ok := requestPage(w, r)
			if !ok {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			items, total, err := service.ListParsingRules(
				r.Context(), claims.TenantID,
				ParsingRuleFilter{
					Page: page, Query: r.URL.Query().Get("q"),
					RuleType: r.URL.Query().Get("ruleType"),
					Status:   r.URL.Query().Get("status"),
				},
			)
			if err != nil {
				writeError(w, err)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{
				"items": items, "total": total,
				"limit": page.Limit, "offset": page.Offset,
			})
		}),
	))

	mux.Handle("POST /api/v1/semantic-parsing-rules", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input ParsingRuleInput
			if !decodeRequest(w, r, &input, 256<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.CreateParsingRule(
				r.Context(), claims.TenantID, claims.Subject, input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, item)
		}),
	))

	mux.Handle("PUT /api/v1/semantic-parsing-rules/{id}", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input ParsingRuleUpdateInput
			if !decodeRequest(w, r, &input, 256<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.UpdateParsingRule(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		}),
	))

	mux.Handle("POST /api/v1/semantic-parsing-rules/{id}/deprecate", protect(
		"MANAGE", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var input DeprecateInput
			if !decodeRequest(w, r, &input, 64<<10) {
				return
			}
			claims, _ := auth.ClaimsFromContext(r.Context())
			item, err := service.DeprecateParsingRule(
				r.Context(), claims.TenantID, claims.Subject,
				r.PathValue("id"), input.ExpectedVersion,
			)
			if err != nil {
				writeError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, item)
		}),
	))

	return mux
}

func requestPage(w http.ResponseWriter, r *http.Request) (Page, bool) {
	page := Page{Limit: DefaultPageLimit}
	for key, target := range map[string]*int{
		"limit": &page.Limit, "offset": &page.Offset,
	} {
		raw := r.URL.Query().Get(key)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"code": "INVALID_PAGE", "message": "分页参数无效",
			})
			return Page{}, false
		}
		*target = value
	}
	if !normalizePage(&page) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code": "INVALID_PAGE", "message": "分页参数无效",
		})
		return Page{}, false
	}
	return page, true
}

func decodeRequest(
	w http.ResponseWriter,
	r *http.Request,
	target any,
	maxBytes int64,
) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "INVALID_REQUEST",
			"message": "请求体不是有效的语义资产 JSON",
		})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "INVALID_REQUEST",
			"message": "请求体只能包含一个 JSON 文档",
		})
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"code":    "SEMANTIC_ASSET_REQUEST_INVALID",
			"message": "语义资产请求无效",
		})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{
			"code":    "SEMANTIC_ASSET_NOT_FOUND",
			"message": "语义资产不存在",
		})
	case errors.Is(err, ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{
			"code":    "SEMANTIC_ASSET_CONFLICT",
			"message": "常用词与类型重复，或资产版本已经变化，请重新加载",
		})
	case errors.Is(err, ErrReleaseNotReady):
		writeJSON(w, http.StatusConflict, map[string]string{
			"code":    "SEMANTIC_RELEASE_NOT_READY",
			"message": "语义发布的执行层、注册表、检索或图投影尚未全部通过同版本校验",
		})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"code":    "SEMANTIC_ASSET_PERSISTENCE_FAILED",
			"message": "语义资产服务暂不可用",
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
