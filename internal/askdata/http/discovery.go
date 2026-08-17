package askdatahttp

import (
	"errors"
	"net/http"

	"intelligent-report-generation-system/internal/askdata/retrieval"
)

type semanticDiscoveryRequest struct {
	Query    string   `json:"query"`
	Sections []string `json:"sections,omitempty"`
	Limit    int      `json:"limit,omitempty"`
	Expand   bool     `json:"expand,omitempty"`
}

// retrieveSemanticAssets 是四分区的混合发现检索：确定性目录巷道 + Release
// 向量巷道 + 语义血缘扩展，融合排序后返回。绑定检索（问数）不走这里。
func (handler *AdminHandler) retrieveSemanticAssets(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	var input semanticDiscoveryRequest
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeDiscoveryError(writer, retrieval.ErrInvalidRequest)
		return
	}
	sections := make([]retrieval.Section, 0, len(input.Sections))
	for _, section := range input.Sections {
		sections = append(sections, retrieval.Section(section))
	}
	result, err := handler.discovery.Retrieve(request.Context(), retrieval.Request{
		TenantID: scope.TenantID, DomainID: scope.DomainID, ActorID: scope.ActorID,
		Query: input.Query, Sections: sections, Limit: input.Limit, Expand: input.Expand,
	})
	if err != nil {
		writeDiscoveryError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func writeDiscoveryError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, retrieval.ErrInvalidRequest):
		writeError(writer, http.StatusBadRequest, "DISCOVERY_INVALID_REQUEST", "semantic discovery request is invalid")
	default:
		writeError(writer, http.StatusInternalServerError, "DISCOVERY_UNAVAILABLE", "semantic discovery is unavailable")
	}
}
