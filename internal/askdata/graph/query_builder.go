package graph

import (
	"fmt"
	"strconv"
	"strings"
)

type queryKind uint8

const (
	queryMetricModels queryKind = iota + 1
	queryCompatibleDimensions
	queryMemberOwnerships
	queryJoinPaths
)

type compiledQuery struct {
	kind       queryKind
	statement  string
	parameters map[string]interface{}
	maxRows    int
}

func buildMetricModelQuery(request PlanRequest) (compiledQuery, error) {
	metricVIDs, err := buildVIDList(request, ObjectTypeMetric, request.MetricRefs)
	if err != nil {
		return compiledQuery{}, err
	}
	modelVIDs, err := buildVIDList(request, ObjectTypeSemanticModel, request.ModelRefs)
	if err != nil {
		return compiledQuery{}, err
	}
	statement := fmt.Sprintf(`MATCH (metric:metric)-[modeled:MODELED_BY]->(model:semantic_model)
WHERE id(metric) IN %s AND id(model) IN %s
  AND metric.metric.tenant_id == $tenant_id
  AND metric.metric.domain_id == $domain_id
  AND metric.metric.release_hash == $release_hash
  AND model.semantic_model.tenant_id == $tenant_id
  AND model.semantic_model.domain_id == $domain_id
  AND model.semantic_model.release_hash == $release_hash
  AND modeled.tenant_id == $tenant_id
  AND modeled.domain_id == $domain_id
  AND modeled.release_hash == $release_hash
RETURN metric.metric.tenant_id AS tenant_id,
       metric.metric.domain_id AS domain_id,
       metric.metric.release_hash AS release_hash,
       metric.metric.object_id AS metric_object_id,
       metric.metric.version_id AS metric_version_id,
       metric.metric.version_no AS metric_version,
       model.semantic_model.object_id AS model_object_id,
       model.semantic_model.version_id AS model_version_id,
       model.semantic_model.version_no AS model_version
ORDER BY metric_version_id, model_version_id
LIMIT %d`, metricVIDs, modelVIDs, MaxMetricCandidates*MaxModelCandidates)
	return compiledQuery{
		kind: queryMetricModels, statement: statement, parameters: scopeParameters(request),
		maxRows: MaxMetricCandidates * MaxModelCandidates,
	}, nil
}

func buildDimensionQuery(request PlanRequest, modelRefs []ObjectVersionRef) (compiledQuery, bool, error) {
	if len(request.DimensionRefs) == 0 || len(modelRefs) == 0 {
		return compiledQuery{}, false, nil
	}
	modelVIDs, err := buildVIDList(request, ObjectTypeSemanticModel, modelRefs)
	if err != nil {
		return compiledQuery{}, false, err
	}
	dimensionVIDs, err := buildVIDList(request, ObjectTypeDimension, request.DimensionRefs)
	if err != nil {
		return compiledQuery{}, false, err
	}
	statement := fmt.Sprintf(`MATCH (model:semantic_model)-[compatible:HAS_DIMENSION]->(dimension:dimension)
WHERE id(model) IN %s AND id(dimension) IN %s
  AND model.semantic_model.tenant_id == $tenant_id
  AND model.semantic_model.domain_id == $domain_id
  AND model.semantic_model.release_hash == $release_hash
  AND dimension.dimension.tenant_id == $tenant_id
  AND dimension.dimension.domain_id == $domain_id
  AND dimension.dimension.release_hash == $release_hash
  AND compatible.tenant_id == $tenant_id
  AND compatible.domain_id == $domain_id
  AND compatible.release_hash == $release_hash
RETURN model.semantic_model.tenant_id AS tenant_id,
       model.semantic_model.domain_id AS domain_id,
       model.semantic_model.release_hash AS release_hash,
       model.semantic_model.object_id AS model_object_id,
       model.semantic_model.version_id AS model_version_id,
       model.semantic_model.version_no AS model_version,
       dimension.dimension.object_id AS dimension_object_id,
       dimension.dimension.version_id AS dimension_version_id,
       dimension.dimension.version_no AS dimension_version
ORDER BY model_version_id, dimension_version_id
LIMIT %d`, modelVIDs, dimensionVIDs, MaxModelCandidates*MaxDimensionCandidates)
	return compiledQuery{
		kind: queryCompatibleDimensions, statement: statement, parameters: scopeParameters(request),
		maxRows: MaxModelCandidates * MaxDimensionCandidates,
	}, true, nil
}

func buildMemberQuery(request PlanRequest) (compiledQuery, bool, error) {
	if len(request.MemberRefs) == 0 {
		return compiledQuery{}, false, nil
	}
	dimensionVIDs, err := buildVIDList(request, ObjectTypeDimension, request.DimensionRefs)
	if err != nil {
		return compiledQuery{}, false, err
	}
	memberVIDs, err := buildVIDList(request, ObjectTypeMember, request.MemberRefs)
	if err != nil {
		return compiledQuery{}, false, err
	}
	statement := fmt.Sprintf(`MATCH (dimension:dimension)-[owns:HAS_MEMBER]->(member:member)
WHERE id(dimension) IN %s AND id(member) IN %s
  AND dimension.dimension.tenant_id == $tenant_id
  AND dimension.dimension.domain_id == $domain_id
  AND dimension.dimension.release_hash == $release_hash
  AND member.member.tenant_id == $tenant_id
  AND member.member.domain_id == $domain_id
  AND member.member.release_hash == $release_hash
  AND owns.tenant_id == $tenant_id
  AND owns.domain_id == $domain_id
  AND owns.release_hash == $release_hash
RETURN member.member.tenant_id AS tenant_id,
       member.member.domain_id AS domain_id,
       member.member.release_hash AS release_hash,
       member.member.object_id AS member_object_id,
       member.member.version_id AS member_version_id,
       member.member.version_no AS member_version,
       member.member.member_status AS member_status,
       dimension.dimension.object_id AS dimension_object_id,
       dimension.dimension.version_id AS dimension_version_id,
       dimension.dimension.version_no AS dimension_version
ORDER BY member_version_id
LIMIT %d`, dimensionVIDs, memberVIDs, MaxMemberCandidates)
	return compiledQuery{
		kind: queryMemberOwnerships, statement: statement, parameters: scopeParameters(request),
		maxRows: MaxMemberCandidates,
	}, true, nil
}

func buildJoinPathQuery(request PlanRequest, endpointRefs []ObjectVersionRef) (compiledQuery, bool, error) {
	if len(endpointRefs) < 2 {
		return compiledQuery{}, false, nil
	}
	endpointVIDs, err := buildVIDList(request, ObjectTypeSemanticModel, endpointRefs)
	if err != nil {
		return compiledQuery{}, false, err
	}
	allowedVIDs, err := buildVIDList(request, ObjectTypeSemanticModel, request.ModelRefs)
	if err != nil {
		return compiledQuery{}, false, err
	}
	statement := fmt.Sprintf(`MATCH join_path=(source:semantic_model)-[joins:JOINS_TO*1..%d]-(target:semantic_model)
WHERE id(source) IN %s AND id(target) IN %s AND id(source) < id(target)
  AND ALL(model IN nodes(join_path) WHERE id(model) IN %s
    AND model.semantic_model.tenant_id == $tenant_id
    AND model.semantic_model.domain_id == $domain_id
    AND model.semantic_model.release_hash == $release_hash)
  AND ALL(join_edge IN joins WHERE join_edge.tenant_id == $tenant_id
    AND join_edge.domain_id == $domain_id
    AND join_edge.release_hash == $release_hash
    AND join_edge.certified == true)
RETURN join_path AS join_path,
       length(join_path) AS path_length,
       id(source) AS source_vid,
       id(target) AS target_vid
ORDER BY path_length, source_vid, target_vid
LIMIT %d`, request.MaxJoinHops, endpointVIDs, endpointVIDs, allowedVIDs, request.MaxPaths)
	return compiledQuery{
		kind: queryJoinPaths, statement: statement, parameters: scopeParameters(request),
		maxRows: request.MaxPaths,
	}, true, nil
}

func buildVIDList(request PlanRequest, objectType ObjectType, refs []ObjectVersionRef) (string, error) {
	vids := make([]string, 0, len(refs))
	for _, ref := range refs {
		vid, err := BuildVID(request.Scope.TenantID, objectType, ref)
		if err != nil {
			return "", err
		}
		vids = append(vids, quoteNGQLString(vid))
	}
	return "[" + strings.Join(vids, ",") + "]", nil
}

func scopeParameters(request PlanRequest) map[string]interface{} {
	return map[string]interface{}{
		"tenant_id":    string(request.Scope.TenantID),
		"domain_id":    string(request.DomainID),
		"release_hash": string(request.Scope.Release.ContentHash),
	}
}

func quoteNGQLString(value string) string {
	// strconv.Quote emits a double-quoted, backslash-escaped UTF-8 literal,
	// which is compatible with nGQL string literal syntax. Values reaching
	// this function are already stable-ID-derived VIDs.
	return strconv.Quote(value)
}
