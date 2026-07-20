package metricai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"intelligent-report-generation-system/internal/access"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/auth"
)

type Proposer interface {
	Propose(context.Context, string, string, AuthoringRequest) (ProposalResult, error)
}

// NewHandler exposes one review-only authoring endpoint. Creating a proposal requires the
// collection-level METRIC/MANAGE capability; the Retriever independently filters evidence by
// object-level DATASET/READ and METRIC/READ permissions.
func NewHandler(authService *auth.Service, permissions *access.Service, proposer Proposer) http.Handler {
	mux := http.NewServeMux()
	endpoint := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		claims, _ := auth.ClaimsFromContext(request.Context())
		var input AuthoringRequest
		if !decodeAuthoringRequest(writer, request, &input) {
			return
		}
		result, err := proposer.Propose(request.Context(), claims.TenantID, claims.Subject, input)
		if err != nil {
			// Keep provider output and user input out of logs, but retain the stable local
			// validation reason so schema/prompt drift can be diagnosed operationally.
			if errors.Is(err, ErrInvalidOutput) || errors.Is(err, ErrInvalidRetrievalContext) {
				slog.WarnContext(request.Context(), "metric AI proposal rejected", "error", err)
			}
			writeAuthoringError(writer, err)
			return
		}
		writeAuthoringJSON(writer, http.StatusOK, result)
	})
	protected := auth.RequireAccessToken(authService,
		access.Require(permissions, "METRIC", "MANAGE", nil, endpoint))
	mux.Handle("POST /api/v1/metrics/ai/proposals", protected)
	return mux
}

func decodeAuthoringRequest(writer http.ResponseWriter, request *http.Request, target *AuthoringRequest) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 32<<10)
	raw, err := io.ReadAll(request.Body)
	if err != nil || rejectDuplicateAuthoringKeys(raw) != nil {
		writeAuthoringJSON(writer, http.StatusBadRequest, map[string]string{
			"code": "METRIC_AI_REQUEST_INVALID", "message": "请用自然语言描述需要创建的指标",
		})
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAuthoringJSON(writer, http.StatusBadRequest, map[string]string{
			"code": "METRIC_AI_REQUEST_INVALID", "message": "请用自然语言描述需要创建的指标",
		})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAuthoringJSON(writer, http.StatusBadRequest, map[string]string{
			"code": "METRIC_AI_REQUEST_INVALID", "message": "请求体只能包含一个 JSON 文档",
		})
		return false
	}
	return true
}

func rejectDuplicateAuthoringKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("metric AI request must be an object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok || seen[key] {
			return errors.New("metric AI request contains a duplicate key")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func writeAuthoringError(writer http.ResponseWriter, err error) {
	var providerErr *aiplatform.ProviderError
	switch {
	case errors.Is(err, ErrInvalidRequest):
		writeAuthoringJSON(writer, http.StatusBadRequest, map[string]string{"code": "METRIC_AI_REQUEST_INVALID", "message": "请用自然语言描述需要创建的指标"})
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		writeAuthoringJSON(writer, http.StatusForbidden, map[string]string{"code": "AI_TENANT_FORBIDDEN", "message": "当前租户未启用通用 AI 能力，或当前账号不可用"})
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		writeAuthoringJSON(writer, http.StatusTooManyRequests, map[string]string{"code": "AI_QUOTA_EXCEEDED", "message": "当前租户 AI 配额已用尽，请稍后重试或联系管理员"})
	case errors.Is(err, ErrProviderUnavailable):
		writeAuthoringJSON(writer, http.StatusServiceUnavailable, map[string]string{"code": "AI_PROVIDER_UNAVAILABLE", "message": "AI 指标提案服务暂时不可用"})
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &providerErr) && providerErr.Code == aiplatform.ErrorCodeTimeout:
		writeAuthoringJSON(writer, http.StatusGatewayTimeout, map[string]string{"code": "AI_TIMEOUT", "message": "AI 指标提案生成超时，未创建或修改任何资源"})
	case errors.Is(err, ErrInvalidOutput):
		writeAuthoringJSON(writer, http.StatusBadGateway, map[string]string{"code": "METRIC_AI_INVALID_OUTPUT", "message": "AI 提案未通过指标安全校验，未创建或修改任何资源"})
	case errors.Is(err, ErrInvalidRetrievalContext):
		writeAuthoringJSON(writer, http.StatusInternalServerError, map[string]string{"code": "METRIC_AI_CONTEXT_INVALID", "message": "授权元数据上下文校验失败"})
	case errors.As(err, &providerErr):
		writeAuthoringJSON(writer, http.StatusBadGateway, map[string]string{"code": "AI_COMPLETION_FAILED", "message": "AI 指标提案生成失败，未创建或修改任何资源"})
	default:
		writeAuthoringJSON(writer, http.StatusInternalServerError, map[string]string{"code": "METRIC_AI_RETRIEVAL_FAILED", "message": "授权指标元数据检索失败"})
	}
}

func writeAuthoringJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

var _ Proposer = (*Service)(nil)
