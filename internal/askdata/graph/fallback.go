package graph

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	maxFallbackRelationships = 32
	maxFallbackExpansions    = 4096
)

var (
	ErrPostgresFallbackUnavailable = errors.New("postgres graph fallback is unavailable")
	ErrFallbackAccessDenied        = errors.New("postgres graph fallback access context is invalid")
	ErrFallbackLimitExceeded       = errors.New("postgres graph fallback bound was exceeded")
)

type PostgresCertifiedPlanCache struct{ pool *pgxpool.Pool }

func NewPostgresCertifiedPlanCache(pool *pgxpool.Pool) *PostgresCertifiedPlanCache {
	return &PostgresCertifiedPlanCache{pool: pool}
}

func (cache *PostgresCertifiedPlanCache) Load(
	ctx context.Context,
	request PlanRequest,
) (plan GraphPlan, hit bool, err error) {
	if cache == nil || cache.pool == nil {
		return GraphPlan{}, false, ErrCertifiedCacheInvalid
	}
	normalized, requestHash, err := normalizePlanRequest(request)
	if err != nil {
		return GraphPlan{}, false, err
	}
	if err := validateFallbackAccessContext(ctx, normalized); err != nil {
		return GraphPlan{}, false, err
	}
	var raw []byte
	var storedPlanHash string
	err = database.WithTenantTx(ctx, cache.pool, string(normalized.Scope.TenantID), func(tx pgx.Tx) error {
		var loadErr error
		raw, storedPlanHash, hit, loadErr = loadCertifiedPlanCacheTx(
			ctx, tx, normalized, requestHash,
		)
		return loadErr
	})
	if err != nil || !hit {
		return GraphPlan{}, false, err
	}
	plan, err = decodeCertifiedPlan(raw, storedPlanHash, normalized, requestHash)
	if err != nil {
		return GraphPlan{}, false, err
	}
	return plan, true, nil
}

func loadCertifiedPlanCacheTx(
	ctx context.Context,
	tx pgx.Tx,
	normalized PlanRequest,
	requestHash askdata.ContentHash,
) ([]byte, string, bool, error) {
	var raw []byte
	var storedPlanHash string
	err := tx.QueryRow(ctx, `SELECT cache.plan_json,cache.plan_hash
			FROM askdata.graph_plan_cache AS cache
			JOIN askdata.releases AS release
			  ON release.id=cache.release_id
			 AND release.domain_id=cache.domain_id
			 AND release.tenant_id=cache.tenant_id
			JOIN askdata.release_projections AS projection
			  ON projection.release_id=release.id
			 AND projection.domain_id=release.domain_id
			 AND projection.tenant_id=release.tenant_id
			 AND projection.target='NEBULA_GRAPH'
			WHERE cache.tenant_id=$1 AND cache.domain_id=$2 AND cache.release_id=$3
			  AND cache.question_shape_hash=$4 AND cache.policy_scope_hash=$5
			  AND cache.graph_content_hash=$6 AND cache.expires_at>CURRENT_TIMESTAMP
			  AND release.status IN ('READY','ACTIVE','SUPERSEDED')
			  AND release.content_hash=cache.graph_content_hash
			  AND projection.status='READY'
			  AND projection.applied_content_hash=cache.graph_content_hash
			ORDER BY cache.created_at DESC,cache.id DESC LIMIT 1`,
		normalized.Scope.TenantID, normalized.DomainID, normalized.Scope.Release.ReleaseID,
		requestHash, normalized.Scope.PolicyHash, normalized.Scope.Release.ContentHash,
	).Scan(&raw, &storedPlanHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("load certified graph plan cache: %w", err)
	}
	return raw, storedPlanHash, true, nil
}

func decodeCertifiedPlan(
	raw []byte,
	storedPlanHash string,
	normalized PlanRequest,
	requestHash askdata.ContentHash,
) (GraphPlan, error) {
	var plan GraphPlan
	if err := askdata.DecodeStrictJSON(raw, &plan); err != nil {
		return GraphPlan{}, fmt.Errorf("%w: JSON contract", ErrCertifiedCacheInvalid)
	}
	if storedPlanHash != string(plan.PlanHash) ||
		validatePlanForRequest(plan, normalized, requestHash) != nil {
		return GraphPlan{}, fmt.Errorf("%w: plan proof", ErrCertifiedCacheInvalid)
	}
	return plan, nil
}

type PostgresFallback struct{ pool *pgxpool.Pool }

func NewPostgresFallback(pool *pgxpool.Pool) *PostgresFallback {
	return &PostgresFallback{pool: pool}
}

func (fallback *PostgresFallback) Resolve(ctx context.Context, request PlanRequest) (GraphPlan, error) {
	if fallback == nil || fallback.pool == nil {
		return GraphPlan{}, ErrPostgresFallbackUnavailable
	}
	normalized, _, err := normalizePlanRequest(request)
	if err != nil {
		return GraphPlan{}, err
	}
	if err := validateFallbackAccessContext(ctx, normalized); err != nil {
		return GraphPlan{}, err
	}

	var plan GraphPlan
	err = database.WithTenantTx(ctx, fallback.pool, string(normalized.Scope.TenantID), func(tx pgx.Tx) error {
		var resolveErr error
		plan, resolveErr = resolvePostgresFallbackTx(ctx, tx, normalized)
		return resolveErr
	})
	if err != nil {
		return GraphPlan{}, err
	}
	return plan, nil
}

func resolvePostgresFallbackTx(ctx context.Context, tx pgx.Tx, normalized PlanRequest) (GraphPlan, error) {
	certified, err := fallbackReleaseCertified(ctx, tx, normalized)
	if err != nil {
		return GraphPlan{}, err
	}
	if !certified {
		return GraphPlan{}, fmt.Errorf("%w: release projection proof is not current", ErrPostgresFallbackUnavailable)
	}

	metricRows, err := loadFallbackMetricModels(ctx, tx, normalized)
	if err != nil {
		return GraphPlan{}, err
	}
	metricModels, models, err := parseMetricModels(normalized, metricRows)
	if err != nil {
		return GraphPlan{}, err
	}

	dimensionRows, err := loadFallbackDimensions(ctx, tx, normalized, models)
	if err != nil {
		return GraphPlan{}, err
	}
	dimensions, err := parseCompatibleDimensions(normalized, models, dimensionRows)
	if err != nil {
		return GraphPlan{}, err
	}

	memberRows, err := loadFallbackMembers(ctx, tx, normalized)
	if err != nil {
		return GraphPlan{}, err
	}
	members, err := parseMemberOwnerships(normalized, memberRows)
	if err != nil {
		return GraphPlan{}, err
	}

	paths := []JoinPath{}
	if len(models) >= 2 {
		relationships, err := loadFallbackRelationships(ctx, tx, normalized)
		if err != nil {
			return GraphPlan{}, err
		}
		paths, err = enumerateFallbackPaths(normalized, models, relationships)
		if err != nil {
			return GraphPlan{}, err
		}
	}
	planModels := append([]ObjectVersionRef(nil), models...)
	requestedModels := refIndex(normalized.ModelRefs)
	for _, path := range paths {
		for _, step := range path.Steps {
			planModels = append(
				planModels,
				requestedModels[step.FromModelVersionID],
				requestedModels[step.ToModelVersionID],
			)
		}
	}
	return NewGraphPlan(
		normalized, planModels, metricModels, dimensions, members, paths,
	)
}

func validateFallbackAccessContext(ctx context.Context, request PlanRequest) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID != string(request.Scope.ActorID) || access.DomainID != string(request.DomainID) {
		return ErrFallbackAccessDenied
	}
	return nil
}

func fallbackReleaseCertified(ctx context.Context, tx pgx.Tx, request PlanRequest) (bool, error) {
	var certified bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM askdata.releases AS release
		WHERE release.tenant_id=$1 AND release.domain_id=$2 AND release.id=$3
		  AND release.content_hash=$4
		  AND release.object_count=(
		    SELECT count(*) FROM askdata.release_objects AS object
		    WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id
		  )
		  AND askdata.release_manifest_hash(release.id)=release.content_hash
		  AND release.status IN ('READY','ACTIVE','SUPERSEDED')
		  AND EXISTS(
		    SELECT 1 FROM askdata.release_projections AS projection
		    WHERE projection.tenant_id=release.tenant_id
		      AND projection.domain_id=release.domain_id
		      AND projection.release_id=release.id
		      AND projection.target='POSTGRES_REGISTRY'
		      AND projection.status='READY'
		      AND projection.applied_content_hash=release.content_hash
		  )
		  AND EXISTS(
		    SELECT 1 FROM askdata.release_projections AS projection
		    WHERE projection.tenant_id=release.tenant_id
		      AND projection.domain_id=release.domain_id
		      AND projection.release_id=release.id
		      AND projection.target='NEBULA_GRAPH'
		      AND projection.status='READY'
		      AND projection.applied_content_hash=release.content_hash
		  )
	)`, request.Scope.TenantID, request.DomainID, request.Scope.Release.ReleaseID,
		request.Scope.Release.ContentHash).Scan(&certified)
	return certified, err
}

func loadFallbackMetricModels(ctx context.Context, tx pgx.Tx, request PlanRequest) ([]queryRow, error) {
	rows, err := tx.Query(ctx, `SELECT
		metric.metric_id::text,metric.id::text,metric.version_no,
		model.model_id::text,model.id::text,model.version_no
	FROM askdata.release_objects AS metric_release
	JOIN askdata.metric_versions AS metric
	  ON metric_release.object_type='METRIC'
	 AND metric.id=metric_release.object_version_id
	 AND metric.metric_id=metric_release.object_id
	 AND metric.tenant_id=metric_release.tenant_id
	 AND metric.domain_id=metric_release.domain_id
	 AND metric.content_hash=metric_release.content_hash
	 AND metric.status='CERTIFIED'
	JOIN askdata.release_objects AS model_release
	  ON model_release.tenant_id=metric_release.tenant_id
	 AND model_release.domain_id=metric_release.domain_id
	 AND model_release.release_id=metric_release.release_id
	 AND model_release.object_type='SEMANTIC_MODEL'
	 AND model_release.object_version_id=metric.semantic_model_version_id
	JOIN askdata.semantic_models AS model
	  ON model.id=model_release.object_version_id
	 AND model.model_id=model_release.object_id
	 AND model.tenant_id=model_release.tenant_id
	 AND model.domain_id=model_release.domain_id
	 AND model.content_hash=model_release.content_hash
	 AND model.status='CERTIFIED'
	WHERE metric_release.tenant_id=$1 AND metric_release.domain_id=$2
	  AND metric_release.release_id=$3
	  AND metric.id::text=ANY($4::text[]) AND model.id::text=ANY($5::text[])
	ORDER BY metric.id,model.id
	LIMIT $6`, request.Scope.TenantID, request.DomainID, request.Scope.Release.ReleaseID,
		versionIDs(request.MetricRefs), versionIDs(request.ModelRefs),
		MaxMetricCandidates*MaxModelCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []queryRow{}
	for rows.Next() {
		var metricObjectID, metricVersionID, modelObjectID, modelVersionID string
		var metricVersion, modelVersion int64
		if err := rows.Scan(
			&metricObjectID, &metricVersionID, &metricVersion,
			&modelObjectID, &modelVersionID, &modelVersion,
		); err != nil {
			return nil, err
		}
		result = append(result, scopedFallbackRow(request, queryRow{
			"metric_object_id": metricObjectID, "metric_version_id": metricVersionID,
			"metric_version": metricVersion, "model_object_id": modelObjectID,
			"model_version_id": modelVersionID, "model_version": modelVersion,
		}))
	}
	return result, rows.Err()
}

func loadFallbackDimensions(
	ctx context.Context,
	tx pgx.Tx,
	request PlanRequest,
	models []ObjectVersionRef,
) ([]queryRow, error) {
	if len(models) == 0 || len(request.DimensionRefs) == 0 {
		return []queryRow{}, nil
	}
	rows, err := tx.Query(ctx, `SELECT
		model.model_id::text,model.id::text,model.version_no,
		dimension.dimension_id::text,dimension.id::text,dimension.version_no
	FROM askdata.release_objects AS dimension_release
	JOIN askdata.dimensions AS dimension
	  ON dimension_release.object_type='DIMENSION'
	 AND dimension.id=dimension_release.object_version_id
	 AND dimension.dimension_id=dimension_release.object_id
	 AND dimension.tenant_id=dimension_release.tenant_id
	 AND dimension.domain_id=dimension_release.domain_id
	 AND dimension.content_hash=dimension_release.content_hash
	 AND dimension.status='CERTIFIED'
	JOIN askdata.release_objects AS model_release
	  ON model_release.tenant_id=dimension_release.tenant_id
	 AND model_release.domain_id=dimension_release.domain_id
	 AND model_release.release_id=dimension_release.release_id
	 AND model_release.object_type='SEMANTIC_MODEL'
	 AND model_release.object_version_id=dimension.semantic_model_version_id
	JOIN askdata.semantic_models AS model
	  ON model.id=model_release.object_version_id
	 AND model.model_id=model_release.object_id
	 AND model.tenant_id=model_release.tenant_id
	 AND model.domain_id=model_release.domain_id
	 AND model.content_hash=model_release.content_hash
	 AND model.status='CERTIFIED'
	WHERE dimension_release.tenant_id=$1 AND dimension_release.domain_id=$2
	  AND dimension_release.release_id=$3
	  AND model.id::text=ANY($4::text[]) AND dimension.id::text=ANY($5::text[])
	ORDER BY model.id,dimension.id
	LIMIT $6`, request.Scope.TenantID, request.DomainID, request.Scope.Release.ReleaseID,
		versionIDs(models), versionIDs(request.DimensionRefs),
		MaxModelCandidates*MaxDimensionCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []queryRow{}
	for rows.Next() {
		var modelObjectID, modelVersionID, dimensionObjectID, dimensionVersionID string
		var modelVersion, dimensionVersion int64
		if err := rows.Scan(
			&modelObjectID, &modelVersionID, &modelVersion,
			&dimensionObjectID, &dimensionVersionID, &dimensionVersion,
		); err != nil {
			return nil, err
		}
		result = append(result, scopedFallbackRow(request, queryRow{
			"model_object_id": modelObjectID, "model_version_id": modelVersionID,
			"model_version": modelVersion, "dimension_object_id": dimensionObjectID,
			"dimension_version_id": dimensionVersionID, "dimension_version": dimensionVersion,
		}))
	}
	return result, rows.Err()
}

func loadFallbackMembers(ctx context.Context, tx pgx.Tx, request PlanRequest) ([]queryRow, error) {
	if len(request.MemberRefs) == 0 {
		return []queryRow{}, nil
	}
	rows, err := tx.Query(ctx, `SELECT
		member.member_id::text,member.id::text,member.version_no,
		CASE WHEN member.valid_from<=CURRENT_TIMESTAMP
		  AND (member.valid_to IS NULL OR member.valid_to>CURRENT_TIMESTAMP)
		  THEN 'ACTIVE' ELSE 'EXPIRED' END,
		dimension.dimension_id::text,dimension.id::text,dimension.version_no
	FROM askdata.release_objects AS member_release
	JOIN askdata.dimension_members AS member
	  ON member_release.object_type='MEMBER'
	 AND member.id=member_release.object_version_id
	 AND member.member_id=member_release.object_id
	 AND member.tenant_id=member_release.tenant_id
	 AND member.domain_id=member_release.domain_id
	 AND member.content_hash=member_release.content_hash
	 AND member.status='CERTIFIED'
	JOIN askdata.release_objects AS dimension_release
	  ON dimension_release.tenant_id=member_release.tenant_id
	 AND dimension_release.domain_id=member_release.domain_id
	 AND dimension_release.release_id=member_release.release_id
	 AND dimension_release.object_type='DIMENSION'
	 AND dimension_release.object_version_id=member.dimension_version_id
	JOIN askdata.dimensions AS dimension
	  ON dimension.id=dimension_release.object_version_id
	 AND dimension.dimension_id=dimension_release.object_id
	 AND dimension.tenant_id=dimension_release.tenant_id
	 AND dimension.domain_id=dimension_release.domain_id
	 AND dimension.content_hash=dimension_release.content_hash
	 AND dimension.status='CERTIFIED'
	WHERE member_release.tenant_id=$1 AND member_release.domain_id=$2
	  AND member_release.release_id=$3
	  AND member.id::text=ANY($4::text[]) AND dimension.id::text=ANY($5::text[])
	ORDER BY member.id
	LIMIT $6`, request.Scope.TenantID, request.DomainID, request.Scope.Release.ReleaseID,
		versionIDs(request.MemberRefs), versionIDs(request.DimensionRefs), MaxMemberCandidates)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []queryRow{}
	for rows.Next() {
		var memberObjectID, memberVersionID, memberStatus string
		var dimensionObjectID, dimensionVersionID string
		var memberVersion, dimensionVersion int64
		if err := rows.Scan(
			&memberObjectID, &memberVersionID, &memberVersion, &memberStatus,
			&dimensionObjectID, &dimensionVersionID, &dimensionVersion,
		); err != nil {
			return nil, err
		}
		result = append(result, scopedFallbackRow(request, queryRow{
			"member_object_id": memberObjectID, "member_version_id": memberVersionID,
			"member_version": memberVersion, "member_status": memberStatus,
			"dimension_object_id": dimensionObjectID, "dimension_version_id": dimensionVersionID,
			"dimension_version": dimensionVersion,
		}))
	}
	return result, rows.Err()
}

type fallbackRelationship struct {
	VersionID           askdata.ID
	LeftModelVersionID  askdata.ID
	RightModelVersionID askdata.ID
	JoinType            registry.JoinType
	Cardinality         registry.Cardinality
	FanoutPolicy        registry.FanoutPolicy
}

func loadFallbackRelationships(
	ctx context.Context,
	tx pgx.Tx,
	request PlanRequest,
) ([]fallbackRelationship, error) {
	rows, err := tx.Query(ctx, `SELECT
		relationship.id::text,relationship.left_model_version_id::text,
		relationship.right_model_version_id::text,relationship.join_type,
		relationship.cardinality,relationship.fanout_policy
	FROM askdata.release_objects AS relationship_release
	JOIN askdata.relationships AS relationship
	  ON relationship_release.object_type='RELATIONSHIP'
	 AND relationship.id=relationship_release.object_version_id
	 AND relationship.relationship_id=relationship_release.object_id
	 AND relationship.tenant_id=relationship_release.tenant_id
	 AND relationship.domain_id=relationship_release.domain_id
	 AND relationship.content_hash=relationship_release.content_hash
	 AND relationship.status='CERTIFIED'
	 AND relationship.relationship_type='MODEL_JOIN'
	JOIN askdata.release_objects AS left_release
	  ON left_release.tenant_id=relationship_release.tenant_id
	 AND left_release.domain_id=relationship_release.domain_id
	 AND left_release.release_id=relationship_release.release_id
	 AND left_release.object_type='SEMANTIC_MODEL'
	 AND left_release.object_version_id=relationship.left_model_version_id
	JOIN askdata.release_objects AS right_release
	  ON right_release.tenant_id=relationship_release.tenant_id
	 AND right_release.domain_id=relationship_release.domain_id
	 AND right_release.release_id=relationship_release.release_id
	 AND right_release.object_type='SEMANTIC_MODEL'
	 AND right_release.object_version_id=relationship.right_model_version_id
	JOIN askdata.semantic_models AS left_model
	  ON left_model.id=left_release.object_version_id
	 AND left_model.model_id=left_release.object_id
	 AND left_model.tenant_id=left_release.tenant_id
	 AND left_model.domain_id=left_release.domain_id
	 AND left_model.content_hash=left_release.content_hash
	 AND left_model.status='CERTIFIED'
	JOIN askdata.semantic_models AS right_model
	  ON right_model.id=right_release.object_version_id
	 AND right_model.model_id=right_release.object_id
	 AND right_model.tenant_id=right_release.tenant_id
	 AND right_model.domain_id=right_release.domain_id
	 AND right_model.content_hash=right_release.content_hash
	 AND right_model.status='CERTIFIED'
	WHERE relationship_release.tenant_id=$1 AND relationship_release.domain_id=$2
	  AND relationship_release.release_id=$3
	  AND relationship.left_model_version_id::text=ANY($4::text[])
	  AND relationship.right_model_version_id::text=ANY($4::text[])
	ORDER BY relationship.id
	LIMIT $5`, request.Scope.TenantID, request.DomainID, request.Scope.Release.ReleaseID,
		versionIDs(request.ModelRefs), maxFallbackRelationships+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []fallbackRelationship{}
	for rows.Next() {
		var relationship fallbackRelationship
		var joinType, cardinality, fanoutPolicy string
		if err := rows.Scan(
			&relationship.VersionID, &relationship.LeftModelVersionID,
			&relationship.RightModelVersionID, &joinType, &cardinality, &fanoutPolicy,
		); err != nil {
			return nil, err
		}
		relationship.JoinType = registry.JoinType(joinType)
		relationship.Cardinality = registry.Cardinality(cardinality)
		relationship.FanoutPolicy = registry.FanoutPolicy(fanoutPolicy)
		if err := relationship.validate(); err != nil {
			return nil, fmt.Errorf("%w: relationship contract", ErrPostgresFallbackUnavailable)
		}
		result = append(result, relationship)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > maxFallbackRelationships {
		return nil, ErrFallbackLimitExceeded
	}
	return result, nil
}

func (relationship fallbackRelationship) validate() error {
	return validateJoinStep(JoinStep{
		Hop: 1, RelationshipVersionID: relationship.VersionID,
		FromModelVersionID: relationship.LeftModelVersionID,
		ToModelVersionID:   relationship.RightModelVersionID,
		Direction:          TraversalForward, JoinType: relationship.JoinType,
		Cardinality: relationship.Cardinality, FanoutPolicy: relationship.FanoutPolicy,
	}, 0)
}

type fallbackArc struct {
	relationship fallbackRelationship
	from, to     askdata.ID
	direction    TraversalDirection
}

type fallbackEndpoint struct {
	ref ObjectVersionRef
	vid string
}

func enumerateFallbackPaths(
	request PlanRequest,
	models []ObjectVersionRef,
	relationships []fallbackRelationship,
) ([]JoinPath, error) {
	if len(models) < 2 {
		return []JoinPath{}, nil
	}
	allowed := refIndex(request.ModelRefs)
	adjacency := make(map[askdata.ID][]fallbackArc, len(allowed))
	for _, relationship := range relationships {
		if _, ok := allowed[relationship.LeftModelVersionID]; !ok {
			return nil, ErrPostgresFallbackUnavailable
		}
		if _, ok := allowed[relationship.RightModelVersionID]; !ok {
			return nil, ErrPostgresFallbackUnavailable
		}
		adjacency[relationship.LeftModelVersionID] = append(
			adjacency[relationship.LeftModelVersionID], fallbackArc{
				relationship: relationship, from: relationship.LeftModelVersionID,
				to: relationship.RightModelVersionID, direction: TraversalForward,
			},
		)
		adjacency[relationship.RightModelVersionID] = append(
			adjacency[relationship.RightModelVersionID], fallbackArc{
				relationship: relationship, from: relationship.RightModelVersionID,
				to: relationship.LeftModelVersionID, direction: TraversalReverse,
			},
		)
	}
	for modelID := range adjacency {
		sort.Slice(adjacency[modelID], func(i, j int) bool {
			left, right := adjacency[modelID][i], adjacency[modelID][j]
			if left.to != right.to {
				return left.to < right.to
			}
			return left.relationship.VersionID < right.relationship.VersionID
		})
	}

	endpoints := make([]fallbackEndpoint, 0, len(models))
	for _, ref := range normalizedRefs(models) {
		vid, err := BuildVID(request.Scope.TenantID, ObjectTypeSemanticModel, ref)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, fallbackEndpoint{ref: ref, vid: vid})
	}
	sort.Slice(endpoints, func(i, j int) bool { return endpoints[i].vid < endpoints[j].vid })

	paths := make([]JoinPath, 0, request.MaxPaths)
	expansions := 0
	for hops := 1; hops <= request.MaxJoinHops && len(paths) < request.MaxPaths; hops++ {
		for source := 0; source < len(endpoints)-1 && len(paths) < request.MaxPaths; source++ {
			for target := source + 1; target < len(endpoints) && len(paths) < request.MaxPaths; target++ {
				visitedModels := map[askdata.ID]bool{endpoints[source].ref.VersionID: true}
				visitedRelationships := map[askdata.ID]bool{}
				steps := []JoinStep{}
				if err := walkFallbackPaths(
					adjacency, endpoints[source].ref.VersionID, endpoints[target].ref.VersionID,
					hops, visitedModels, visitedRelationships, steps, &paths,
					request.MaxPaths, &expansions,
				); err != nil {
					return nil, err
				}
			}
		}
	}
	return paths, nil
}

func walkFallbackPaths(
	adjacency map[askdata.ID][]fallbackArc,
	current, target askdata.ID,
	remaining int,
	visitedModels map[askdata.ID]bool,
	visitedRelationships map[askdata.ID]bool,
	steps []JoinStep,
	paths *[]JoinPath,
	pathLimit int,
	expansions *int,
) error {
	if len(*paths) >= pathLimit {
		return nil
	}
	if remaining == 0 {
		if current != target {
			return nil
		}
		path, err := NewJoinPath(steps)
		if err != nil {
			return err
		}
		*paths = append(*paths, path)
		return nil
	}
	if current == target {
		return nil
	}
	for _, arc := range adjacency[current] {
		*expansions++
		if *expansions > maxFallbackExpansions {
			return ErrFallbackLimitExceeded
		}
		if visitedModels[arc.to] || visitedRelationships[arc.relationship.VersionID] {
			continue
		}
		visitedModels[arc.to] = true
		visitedRelationships[arc.relationship.VersionID] = true
		step := JoinStep{
			Hop: len(steps) + 1, RelationshipVersionID: arc.relationship.VersionID,
			FromModelVersionID: current, ToModelVersionID: arc.to,
			Direction: arc.direction, JoinType: arc.relationship.JoinType,
			Cardinality: arc.relationship.Cardinality, FanoutPolicy: arc.relationship.FanoutPolicy,
		}
		if err := walkFallbackPaths(
			adjacency, arc.to, target, remaining-1, visitedModels, visitedRelationships,
			append(steps, step), paths, pathLimit, expansions,
		); err != nil {
			return err
		}
		delete(visitedModels, arc.to)
		delete(visitedRelationships, arc.relationship.VersionID)
		if len(*paths) >= pathLimit {
			return nil
		}
	}
	return nil
}

func scopedFallbackRow(request PlanRequest, row queryRow) queryRow {
	row["tenant_id"] = string(request.Scope.TenantID)
	row["domain_id"] = string(request.DomainID)
	row["release_hash"] = string(request.Scope.Release.ContentHash)
	return row
}

func versionIDs(refs []ObjectVersionRef) []string {
	values := make([]string, len(refs))
	for index, ref := range refs {
		values[index] = string(ref.VersionID)
	}
	return values
}

var (
	_ CertifiedPlanCache = (*PostgresCertifiedPlanCache)(nil)
	_ FallbackPlanner    = (*PostgresFallback)(nil)
)
