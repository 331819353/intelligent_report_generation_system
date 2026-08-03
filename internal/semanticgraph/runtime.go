package semanticgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Runtime struct{ executor QueryExecutor }

func NewRuntime(executor QueryExecutor) *Runtime { return &Runtime{executor: executor} }

func (runtime *Runtime) ExpandCandidates(
	ctx context.Context, scope Scope, startVIDs []string,
) ([]Candidate, Evidence, error) {
	if !runtime.valid(scope) || len(startVIDs) < 1 || len(startVIDs) > 20 {
		return nil, Evidence{}, ErrInvalidRequest
	}
	statement := `MATCH (start)-[rels:groupable_by|measures|sourced_from*1..2]->(candidate)
		WHERE id(start)==$start_vid
		  AND ALL(rel IN rels WHERE rel.tenant_scope==$tenant_scope
		    AND rel.semantic_version==$semantic_version AND rel.certified==true)
		RETURN DISTINCT id(candidate) AS vid,type(rels[0]) AS relation_type
		LIMIT 30`
	unique := map[string]Candidate{}
	for _, startVID := range uniqueStrings(startVIDs) {
		result, err := runtime.executor.Execute(ctx, statement, runtime.parameters(scope, map[string]any{"start_vid": startVID}))
		if err != nil {
			return nil, Evidence{}, err
		}
		for _, row := range result.Rows {
			candidate := Candidate{VID: rowString(row, "vid"), RelationType: rowString(row, "relation_type")}
			if candidate.VID != "" {
				unique[candidate.VID] = candidate
			}
		}
	}
	items := make([]Candidate, 0, len(unique))
	for _, item := range unique {
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool { return items[left].VID < items[right].VID })
	if len(items) > MaximumOnlineCandidates {
		items = items[:MaximumOnlineCandidates]
	}
	return items, runtime.evidence(scope, "candidate_extension", items), nil
}

func (runtime *Runtime) ValidateValueOwnership(
	ctx context.Context, scope Scope, request ValueOwnershipRequest,
) (ValueOwnership, Evidence, error) {
	if !runtime.valid(scope) || request.DimensionVID == "" || request.ValueVID == "" {
		return ValueOwnership{}, Evidence{}, ErrInvalidRequest
	}
	statement := `MATCH (d:dimension)-[r:has_value]->(v:dimension_value)
		WHERE id(d)==$dimension_vid AND id(v)==$value_vid
		  AND r.tenant_scope==$tenant_scope
		  AND r.semantic_version==$semantic_version AND r.certified==true
		  AND (r.effective_from==0 OR r.effective_from<=$effective_at)
		  AND (r.effective_to==0 OR r.effective_to>$effective_at)
		RETURN id(d) AS dimension_vid,id(v) AS value_vid,
		       r.certified AS certified,r.attributes_json AS attributes_json
		LIMIT 1`
	result, err := runtime.executor.Execute(ctx, statement, runtime.parameters(scope, map[string]any{
		"dimension_vid": request.DimensionVID, "value_vid": request.ValueVID,
	}))
	if err != nil {
		return ValueOwnership{}, Evidence{}, err
	}
	if len(result.Rows) != 1 || !rowBool(result.Rows[0], "certified") {
		return ValueOwnership{}, Evidence{}, ErrNoCertifiedPath
	}
	ownership := ValueOwnership{DimensionVID: request.DimensionVID, ValueVID: request.ValueVID, Certified: true}
	return ownership, runtime.evidence(scope, "value_ownership", ownership), nil
}

func (runtime *Runtime) ValidateBundle(
	ctx context.Context, scope Scope, bundle Bundle,
) (BundleValidation, Evidence, error) {
	if !runtime.valid(scope) || len(bundle.MetricVIDs) < 1 || len(bundle.MetricVIDs) > 5 || len(bundle.DimensionVIDs) > 8 || len(bundle.Values) > 10 {
		return BundleValidation{}, Evidence{}, ErrInvalidRequest
	}
	validation := BundleValidation{Valid: true, Conflicts: []string{}, JoinPathIDs: []string{}}
	statement := `MATCH (m:metric)-[r:groupable_by]->(d:dimension)
		WHERE id(m)==$metric_vid AND id(d)==$dimension_vid
		  AND r.tenant_scope==$tenant_scope
		  AND r.semantic_version==$semantic_version
		  AND r.certified==true AND r.allowed_for_query==true
		RETURN r.relation_id AS relation_id LIMIT 1`
	for _, metricVID := range uniqueStrings(bundle.MetricVIDs) {
		for _, dimensionVID := range uniqueStrings(bundle.DimensionVIDs) {
			result, err := runtime.executor.Execute(ctx, statement, runtime.parameters(scope, map[string]any{
				"metric_vid": metricVID, "dimension_vid": dimensionVID,
			}))
			if err != nil {
				return BundleValidation{}, Evidence{}, err
			}
			if len(result.Rows) != 1 {
				validation.Valid = false
				validation.Conflicts = append(validation.Conflicts, "INCOMPATIBLE:"+metricVID+":"+dimensionVID)
				continue
			}
			validation.JoinPathIDs = append(validation.JoinPathIDs, rowString(result.Rows[0], "relation_id"))
		}
	}
	for _, value := range bundle.Values {
		if _, _, err := runtime.ValidateValueOwnership(ctx, scope, ValueOwnershipRequest{
			DimensionVID: value.DimensionVID, ValueVID: value.ValueVID,
		}); err != nil {
			if !errors.Is(err, ErrNoCertifiedPath) {
				return BundleValidation{}, Evidence{}, err
			}
			validation.Valid = false
			validation.Conflicts = append(validation.Conflicts, "VALUE_NOT_OWNED:"+value.DimensionVID+":"+value.ValueVID)
		}
	}
	validation.Conflicts, validation.JoinPathIDs = uniqueStrings(validation.Conflicts), uniqueStrings(validation.JoinPathIDs)
	if !validation.Valid {
		return validation, runtime.evidence(scope, "bundle_validation", validation), ErrNoCertifiedPath
	}
	return validation, runtime.evidence(scope, "bundle_validation", validation), nil
}

func (runtime *Runtime) FindJoinPaths(
	ctx context.Context, scope Scope, request JoinPathRequest,
) ([]JoinPath, Evidence, error) {
	if !runtime.valid(scope) || request.FactDatasetVID == "" || request.DimensionDatasetVID == "" ||
		request.MaxHops < 1 || request.MaxHops > MaximumOnlineHops {
		return nil, Evidence{}, ErrInvalidRequest
	}
	if request.Limit < 1 || request.Limit > 20 {
		request.Limit = 20
	}
	statement := fmt.Sprintf(`MATCH p=(f:dataset)-[rels:joins_to*1..%d]->(d:dataset)
		WHERE id(f)==$fact_vid AND id(d)==$dimension_vid
		  AND ALL(e IN rels WHERE e.tenant_scope==$tenant_scope
		    AND e.semantic_version==$semantic_version AND e.certified==true
		    AND e.allowed_for_query==true AND e.cardinality!="unknown")
		RETURN p AS graph_path LIMIT %d`, request.MaxHops, request.Limit)
	result, err := runtime.executor.Execute(ctx, statement, runtime.parameters(scope, map[string]any{
		"fact_vid": request.FactDatasetVID, "dimension_vid": request.DimensionDatasetVID,
	}))
	if err != nil {
		return nil, Evidence{}, err
	}
	candidates := make([]JoinPath, 0, len(result.Rows))
	for _, row := range result.Rows {
		raw, ok := row["graph_path"].(rawGraphPath)
		if ok {
			candidates = append(candidates, JoinPath{VIDs: raw.VIDs, Edges: raw.Edges})
		}
	}
	paths := RankJoinPaths(candidates, MaximumJoinPaths)
	if len(paths) == 0 {
		return nil, Evidence{}, ErrNoCertifiedPath
	}
	return paths, runtime.evidence(scope, "join_path", paths), nil
}

func (runtime *Runtime) FilterAuthorized(
	ctx context.Context, scope Scope, request AuthorizationRequest,
) ([]string, Evidence, error) {
	if !runtime.valid(scope) || len(request.RoleVIDs) < 1 || len(request.RoleVIDs) > 20 ||
		len(request.CandidateVIDs) < 1 || len(request.CandidateVIDs) > 100 {
		return nil, Evidence{}, ErrInvalidRequest
	}
	statement := `MATCH (r:role)-[permission:can_access]->(candidate)
		WHERE id(r)==$role_vid AND id(candidate)==$candidate_vid
		  AND permission.tenant_scope==$tenant_scope
		  AND permission.semantic_version==$semantic_version
		  AND permission.certified==true
		RETURN id(candidate) AS candidate_vid LIMIT 1`
	allowed := map[string]bool{}
	for _, roleVID := range uniqueStrings(request.RoleVIDs) {
		for _, candidateVID := range uniqueStrings(request.CandidateVIDs) {
			result, err := runtime.executor.Execute(ctx, statement, runtime.parameters(scope, map[string]any{
				"role_vid": roleVID, "candidate_vid": candidateVID,
			}))
			if err != nil {
				return nil, Evidence{}, err
			}
			if len(result.Rows) == 1 {
				allowed[candidateVID] = true
			}
		}
	}
	items := make([]string, 0, len(allowed))
	for candidate := range allowed {
		items = append(items, candidate)
	}
	sort.Strings(items)
	return items, runtime.evidence(scope, "permission_propagation", items), nil
}

func (runtime *Runtime) ImpactAnalysis(
	ctx context.Context, scope Scope, request ImpactRequest,
) ([]ImpactedObject, Evidence, error) {
	if !runtime.valid(scope) || len(request.ChangedVIDs) < 1 || len(request.ChangedVIDs) > 20 ||
		request.MaxHops < 1 || request.MaxHops > MaximumOnlineHops {
		return nil, Evidence{}, ErrInvalidRequest
	}
	if request.Limit < 1 || request.Limit > 200 {
		request.Limit = 100
	}
	statement := fmt.Sprintf(`MATCH p=(affected)-[rels:depends_on|derived_from|sourced_from|uses*1..%d]->(changed)
		WHERE id(changed)==$changed_vid
		  AND ALL(e IN rels WHERE e.tenant_scope==$tenant_scope
		    AND e.semantic_version==$semantic_version AND e.certified==true)
		RETURN DISTINCT id(affected) AS vid,type(rels[0]) AS relation_type,
		       size(rels) AS distance LIMIT %d`, request.MaxHops, request.Limit)
	items := map[string]ImpactedObject{}
	for _, changedVID := range uniqueStrings(request.ChangedVIDs) {
		result, err := runtime.executor.Execute(ctx, statement, runtime.parameters(scope, map[string]any{"changed_vid": changedVID}))
		if err != nil {
			return nil, Evidence{}, err
		}
		for _, row := range result.Rows {
			item := ImpactedObject{VID: rowString(row, "vid"), RelationType: rowString(row, "relation_type"), Distance: rowInt(row, "distance")}
			if item.VID == "" {
				continue
			}
			if previous, exists := items[item.VID]; !exists || item.Distance < previous.Distance {
				items[item.VID] = item
			}
		}
	}
	result := make([]ImpactedObject, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Distance != result[right].Distance {
			return result[left].Distance < result[right].Distance
		}
		return result[left].VID < result[right].VID
	})
	return result, runtime.evidence(scope, "impact_analysis", result), nil
}

func (runtime *Runtime) valid(scope Scope) bool {
	return runtime != nil && runtime.executor != nil && strings.TrimSpace(scope.TenantID) != "" &&
		strings.TrimSpace(scope.SemanticVersion) != "" && len(scope.ContentHash) == 64 && !scope.EffectiveAt.IsZero()
}

func (runtime *Runtime) parameters(scope Scope, additional map[string]any) map[string]any {
	result := map[string]any{"tenant_scope": scope.TenantID, "semantic_version": scope.SemanticVersion, "effective_at": scope.EffectiveAt.UTC().Unix()}
	for key, value := range additional {
		result[key] = value
	}
	return result
}

func (runtime *Runtime) evidence(scope Scope, operation string, result any) Evidence {
	encoded, _ := json.Marshal(struct {
		TenantID, Version, Hash, Operation string
		Result                             any
	}{scope.TenantID, scope.SemanticVersion, scope.ContentHash, operation, result})
	hash := sha256.Sum256(encoded)
	return Evidence{Source: "NEBULA_GRAPH", SemanticVersion: scope.SemanticVersion,
		ContentHash: scope.ContentHash, EvidenceID: "graph:" + hex.EncodeToString(hash[:]), ObservedAt: time.Now().UTC()}
}

type ResilientGraph struct {
	live     Graph
	cache    PlanCache
	cacheTTL time.Duration
}

func NewResilientGraph(live Graph, cache PlanCache, cacheTTL time.Duration) *ResilientGraph {
	return &ResilientGraph{live: live, cache: cache, cacheTTL: cacheTTL}
}

func (graph *ResilientGraph) FindJoinPaths(ctx context.Context, scope Scope, request JoinPathRequest) ([]JoinPath, Evidence, error) {
	requestHash := graphRequestHash(request)
	if graph != nil && graph.live != nil {
		paths, evidence, err := graph.live.FindJoinPaths(ctx, scope, request)
		if err == nil {
			if graph.cache != nil && graph.cacheTTL > 0 {
				_ = graph.cache.Put(ctx, CachedGraphPlan{Scope: scope, RequestHash: requestHash,
					Paths: paths, Evidence: evidence, Certified: true, ExpiresAt: time.Now().UTC().Add(graph.cacheTTL)})
			}
			return paths, evidence, nil
		}
		if !errors.Is(err, ErrGraphUnavailable) || graph.cache == nil {
			return nil, Evidence{}, err
		}
	}
	if graph == nil || graph.cache == nil {
		return nil, Evidence{}, ErrGraphUnavailable
	}
	cached, found, err := graph.cache.Get(ctx, scope, requestHash)
	if err != nil {
		return nil, Evidence{}, err
	}
	if !found || !cached.Certified || !cached.ExpiresAt.After(time.Now().UTC()) ||
		cached.Scope.TenantID != scope.TenantID || cached.Scope.SemanticVersion != scope.SemanticVersion || cached.Scope.ContentHash != scope.ContentHash {
		return nil, Evidence{}, ErrGraphUnavailable
	}
	cached.Evidence.Cached = true
	return cached.Paths, cached.Evidence, nil
}

func (graph *ResilientGraph) ExpandCandidates(ctx context.Context, scope Scope, starts []string) ([]Candidate, Evidence, error) {
	if graph == nil || graph.live == nil {
		return nil, Evidence{}, ErrGraphUnavailable
	}
	return graph.live.ExpandCandidates(ctx, scope, starts)
}
func (graph *ResilientGraph) ValidateValueOwnership(ctx context.Context, scope Scope, request ValueOwnershipRequest) (ValueOwnership, Evidence, error) {
	if graph == nil || graph.live == nil {
		return ValueOwnership{}, Evidence{}, ErrGraphUnavailable
	}
	return graph.live.ValidateValueOwnership(ctx, scope, request)
}
func (graph *ResilientGraph) ValidateBundle(ctx context.Context, scope Scope, bundle Bundle) (BundleValidation, Evidence, error) {
	if graph == nil || graph.live == nil {
		return BundleValidation{}, Evidence{}, ErrGraphUnavailable
	}
	return graph.live.ValidateBundle(ctx, scope, bundle)
}
func (graph *ResilientGraph) FilterAuthorized(ctx context.Context, scope Scope, request AuthorizationRequest) ([]string, Evidence, error) {
	if graph == nil || graph.live == nil {
		return nil, Evidence{}, ErrGraphUnavailable
	}
	return graph.live.FilterAuthorized(ctx, scope, request)
}
func (graph *ResilientGraph) ImpactAnalysis(ctx context.Context, scope Scope, request ImpactRequest) ([]ImpactedObject, Evidence, error) {
	if graph == nil || graph.live == nil {
		return nil, Evidence{}, ErrGraphUnavailable
	}
	return graph.live.ImpactAnalysis(ctx, scope, request)
}

func graphRequestHash(value any) string {
	encoded, _ := json.Marshal(value)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func rowString(row map[string]any, key string) string { value, _ := row[key].(string); return value }
func rowBool(row map[string]any, key string) bool     { value, _ := row[key].(bool); return value }
func rowInt(row map[string]any, key string) int {
	switch value := row[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	}
	return 0
}
