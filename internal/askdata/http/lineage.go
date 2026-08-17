package askdatahttp

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/askdata/lineage"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// getLineageNeighbourhood 返回以某个语义资产为中心的血缘邻域，供四分区页面
// 的图画布渲染。family 可选（physical/semantic），缺省两族都返回。
func (handler *AdminHandler) getLineageNeighbourhood(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	query := request.URL.Query()
	families, ok := parseLineageFamilies(query.Get("family"))
	if !ok {
		writeLineageError(writer, lineage.ErrInvalidLineageRequest)
		return
	}
	depth := 0
	if raw := query.Get("depth"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > lineage.MaxNeighbourhoodDepth {
			writeLineageError(writer, lineage.ErrInvalidLineageRequest)
			return
		}
		depth = parsed
	}
	result, err := lineage.ExpandNeighbourhood(request.Context(), handler.lineageBackend, lineage.NeighbourhoodRequest{
		TenantID: scope.TenantID, DomainID: scope.DomainID,
		Node: lineage.NodeRef{
			Type: lineage.NodeType(query.Get("nodeType")),
			ID:   query.Get("nodeId"),
		},
		Families: families, Depth: depth,
	})
	if err != nil {
		writeLineageError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

type lineageImpactRequest struct {
	NodeType string `json:"nodeType"`
	NodeID   string `json:"nodeId"`
	Family   string `json:"family,omitempty"`
	MaxDepth int    `json:"maxDepth,omitempty"`
}

// getLineageImpact 只沿下游遍历：该节点变化会波及哪些资产，按跳分层返回。
func (handler *AdminHandler) getLineageImpact(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	var input lineageImpactRequest
	if _, err := decodeAdminJSON(writer, request, &input); err != nil {
		writeLineageError(writer, lineage.ErrInvalidLineageRequest)
		return
	}
	families, ok := parseLineageFamilies(input.Family)
	if !ok {
		writeLineageError(writer, lineage.ErrInvalidLineageRequest)
		return
	}
	report, err := lineage.WalkImpact(request.Context(), handler.lineageBackend, lineage.ImpactRequest{
		TenantID: scope.TenantID, DomainID: scope.DomainID,
		Node: lineage.NodeRef{
			Type: lineage.NodeType(input.NodeType),
			ID:   input.NodeID,
		},
		Families: families, MaxDepth: input.MaxDepth,
	})
	if err != nil {
		writeLineageError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, report)
}

// rebuildLineage 幂等重建当前业务域的 COMPUTED 血缘边。
func (handler *AdminHandler) rebuildLineage(writer http.ResponseWriter, request *http.Request) {
	scope, ok := handler.resolveScope(writer, request)
	if !ok {
		return
	}
	if request.URL.RawQuery != "" {
		writeLineageError(writer, lineage.ErrInvalidLineageRequest)
		return
	}
	total, err := handler.lineageBackend.Rebuild(request.Context(), scope.TenantID, scope.DomainID)
	if err != nil {
		writeLineageError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"edges": total})
}

func parseLineageFamilies(raw string) ([]lineage.Family, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, true
	}
	family := lineage.Family(strings.ToUpper(strings.TrimSpace(raw)))
	if !family.Valid() {
		return nil, false
	}
	return []lineage.Family{family}, true
}

func writeLineageError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, lineage.ErrInvalidLineageRequest):
		writeError(writer, http.StatusBadRequest, "LINEAGE_INVALID_REQUEST", "lineage request is invalid")
	case errors.Is(err, registry.ErrRegistryPermissionDenied):
		writeError(writer, http.StatusForbidden, "LINEAGE_PERMISSION_DENIED", "the selected business domain is outside the authenticated scope")
	default:
		writeError(writer, http.StatusInternalServerError, "LINEAGE_UNAVAILABLE", "lineage graph is unavailable")
	}
}
