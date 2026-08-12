package askdatahttp

import (
	"net/http"
	"strconv"

	"intelligent-report-generation-system/internal/askdata/registry"
)

func (handler *AdminHandler) createReleaseEvaluationSet(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	var input registry.EvaluationSetCreateInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	result, err := handler.evaluationSetAdmin.CreateEvaluationSetFromCertified(
		request.Context(), scope, request.PathValue("id"), input,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, result)
}

func (handler *AdminHandler) listEvaluationCasesForReview(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	for key := range query {
		if key != "limit" && key != "offset" {
			writeAdminError(writer, registry.ErrEvaluationSetInvalid)
			return
		}
	}
	limit, offset := 100, 0
	var err error
	if query.Get("limit") != "" {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil {
			writeAdminError(writer, registry.ErrEvaluationSetInvalid)
			return
		}
	}
	if query.Get("offset") != "" {
		offset, err = strconv.Atoi(query.Get("offset"))
		if err != nil {
			writeAdminError(writer, registry.ErrEvaluationSetInvalid)
			return
		}
	}
	result, err := handler.evaluationSetAdmin.ListEvaluationCasesForReview(
		request.Context(), scope, request.PathValue("id"), limit, offset,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) reviewEvaluationSet(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	var input registry.EvaluationSetReviewInput
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	result, err := handler.evaluationSetAdmin.ReviewEvaluationSet(
		request.Context(), scope, request.PathValue("id"), input,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

// getEvaluationShardHealth reports how much sealed material remains. It returns
// counts and rotation timestamps only and never a sealed question: there is no
// endpoint anywhere that renders sealed case bodies, by design.
func (handler *AdminHandler) getEvaluationShardHealth(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	result, err := handler.evaluationSetAdmin.GetEvaluationShardHealth(
		request.Context(), scope, request.PathValue("id"),
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

// exposeEvaluationShard is a human declaration that sealed material has leaked,
// not a read of it. The declared shard retires immediately and irreversibly.
func (handler *AdminHandler) exposeEvaluationShard(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	var input struct {
		ShardID int16 `json:"shardId"`
	}
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	result, err := handler.evaluationSetAdmin.ExposeEvaluationShard(
		request.Context(), scope, request.PathValue("id"), input.ShardID,
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (handler *AdminHandler) sealEvaluationSet(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok || !requireLifecycleIdempotency(writer, request) {
		return
	}
	if request.URL.RawQuery != "" {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	var input struct{}
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeAdminError(writer, registry.ErrEvaluationSetInvalid)
		return
	}
	result, err := handler.evaluationSetAdmin.SealEvaluationSet(
		request.Context(), scope, request.PathValue("id"),
	)
	if err != nil {
		writeAdminError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
