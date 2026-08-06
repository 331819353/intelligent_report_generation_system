package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresProjectionStore struct{ pool *pgxpool.Pool }

func NewPostgresProjectionStore(pool *pgxpool.Pool) *PostgresProjectionStore {
	return &PostgresProjectionStore{pool: pool}
}

func (store *PostgresProjectionStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidProjectionWork
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text
		FROM askdata.list_release_projection_tenants($1)`, ProjectionTargetNebula)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenants := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenants = append(tenants, tenantID)
	}
	return tenants, rows.Err()
}

func (store *PostgresProjectionStore) Claim(
	ctx context.Context, tenantID, workerID string, lease time.Duration,
) (*ProjectionClaim, error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		lease < 30*time.Second || lease > 10*time.Minute {
		return nil, ErrInvalidProjectionWork
	}
	claim := ProjectionClaim{TenantID: tenantID}
	err := store.pool.QueryRow(ctx, `SELECT
		projection_id::text,domain_id::text,release_id::text,target,
		semantic_version,content_hash,lease_token::text,attempt
		FROM askdata.claim_release_projection($1,$2,$3,$4)`,
		tenantID, ProjectionTargetNebula, workerID, int64(lease/time.Second),
	).Scan(
		&claim.ProjectionID, &claim.DomainID, &claim.ReleaseID, &claim.Target,
		&claim.SemanticVersion, &claim.ContentHash, &claim.LeaseToken, &claim.Attempt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := validateProjectionClaim(claim, tenantID); err != nil {
		return &claim, err
	}
	return &claim, nil
}

func (store *PostgresProjectionStore) LoadGraphSnapshot(
	ctx context.Context, claim ProjectionClaim, workerID string,
) (snapshot ProjectionSnapshot, err error) {
	if store == nil || store.pool == nil || validateProjectionClaim(claim, claim.TenantID) != nil ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 {
		return ProjectionSnapshot{}, ErrInvalidProjectionWork
	}
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var releaseHash, semanticVersion, manifestHash string
		var releaseObjectCount, manifestCount int
		if err := tx.QueryRow(ctx, `SELECT release.content_hash,release.semantic_version,
			release.object_count,
			(SELECT count(*) FROM askdata.release_objects AS object
			 WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id),
			askdata.release_manifest_hash(release.id)
		FROM askdata.releases AS release
		JOIN askdata.release_projections AS projection
		  ON projection.release_id=release.id
		 AND projection.domain_id=release.domain_id
		 AND projection.tenant_id=release.tenant_id
		WHERE release.id=$1 AND release.domain_id=$2
		  AND projection.id=$3 AND projection.target=$4
		  AND projection.status='RUNNING' AND projection.lease_owner=$5
		  AND projection.lease_token=$6 AND projection.lease_expires_at>now()
		  AND release.status IN ('PROJECTING','BLOCKED')`,
			claim.ReleaseID, claim.DomainID, claim.ProjectionID, ProjectionTargetNebula,
			workerID, claim.LeaseToken,
		).Scan(&releaseHash, &semanticVersion, &releaseObjectCount, &manifestCount, &manifestHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrProjectionLeaseLost
			}
			return err
		}
		if releaseHash != claim.ContentHash || semanticVersion != claim.SemanticVersion ||
			releaseObjectCount != manifestCount || manifestCount < 1 || manifestHash != releaseHash {
			return fmt.Errorf("%w: release manifest changed or does not match its content hash", ErrProjectionContract)
		}

		expectedVertices := map[ObjectType]int{}
		expectedEdges := map[ProjectionEdgeType]int{}
		rows, err := tx.Query(ctx, `SELECT released.object_type,count(*)
			FROM askdata.release_objects AS released
			LEFT JOIN askdata.relationships AS relationship
			  ON released.object_type='RELATIONSHIP'
			 AND relationship.tenant_id=released.tenant_id
			 AND relationship.domain_id=released.domain_id
			 AND relationship.relationship_id=released.object_id
			 AND relationship.id=released.object_version_id
			WHERE released.release_id=$1
			  AND (
			    released.object_type IN ('SEMANTIC_MODEL','METRIC','DIMENSION','MEMBER')
			    OR (released.object_type='RELATIONSHIP' AND relationship.relationship_type='MODEL_JOIN')
			  )
			GROUP BY released.object_type ORDER BY released.object_type`, claim.ReleaseID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var objectType string
			var count int
			if err := rows.Scan(&objectType, &count); err != nil {
				rows.Close()
				return err
			}
			switch registry.ReleaseObjectType(objectType) {
			case registry.ReleaseObjectSemanticModel:
				expectedVertices[ObjectTypeSemanticModel] = count
			case registry.ReleaseObjectMetric:
				expectedVertices[ObjectTypeMetric] = count
				expectedEdges[ProjectionEdgeModeledBy] = count
			case registry.ReleaseObjectDimension:
				expectedVertices[ObjectTypeDimension] = count
				expectedEdges[ProjectionEdgeHasDimension] = count
			case registry.ReleaseObjectMember:
				expectedVertices[ObjectTypeMember] = count
				expectedEdges[ProjectionEdgeHasMember] = count
			case registry.ReleaseObjectRelationship:
				expectedEdges[ProjectionEdgeJoinsTo] = count
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		snapshot = ProjectionSnapshot{
			TenantID: askdata.ID(claim.TenantID), DomainID: askdata.ID(claim.DomainID),
			ReleaseID: askdata.ID(claim.ReleaseID), SemanticVersion: semanticVersion,
			ContentHash: askdata.ContentHash(releaseHash), ManifestCount: manifestCount,
			Vertices: []ProjectionVertex{}, Edges: []ProjectionEdge{},
		}
		rows, err = tx.Query(ctx, `SELECT * FROM askdata.load_release_graph_projection($1,$2,$3,$4)`,
			claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		for rows.Next() {
			var elementKind, graphType, objectID, objectVersionID, memberStatus string
			var fromObjectID, fromVersionID, toObjectID, toVersionID string
			var relationshipVersionID, joinType, cardinality, fanoutPolicy string
			var versionNo, fromVersionNo, toVersionNo int
			var certified bool
			if err := rows.Scan(
				&elementKind, &graphType, &objectID, &objectVersionID, &versionNo, &memberStatus,
				&fromObjectID, &fromVersionID, &fromVersionNo, &toObjectID, &toVersionID, &toVersionNo,
				&relationshipVersionID, &joinType, &cardinality, &fanoutPolicy, &certified,
			); err != nil {
				rows.Close()
				return err
			}
			switch elementKind {
			case "VERTEX":
				objectType, typeErr := projectionObjectType(graphType)
				if typeErr != nil {
					rows.Close()
					return typeErr
				}
				snapshot.Vertices = append(snapshot.Vertices, ProjectionVertex{
					Type:         objectType,
					Ref:          ObjectVersionRef{ObjectID: askdata.ID(objectID), VersionID: askdata.ID(objectVersionID), Version: versionNo},
					MemberStatus: MemberStatus(memberStatus),
				})
			case "EDGE":
				edge, edgeErr := projectionEdgeFromRow(
					graphType, fromObjectID, fromVersionID, fromVersionNo,
					toObjectID, toVersionID, toVersionNo, relationshipVersionID,
					joinType, cardinality, fanoutPolicy, certified,
				)
				if edgeErr != nil {
					rows.Close()
					return edgeErr
				}
				snapshot.Edges = append(snapshot.Edges, edge)
			default:
				rows.Close()
				return fmt.Errorf("%w: unsupported graph element kind", ErrProjectionContract)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if err := validateProjectionCounts(snapshot, expectedVertices, expectedEdges); err != nil {
			return err
		}
		return snapshot.Validate()
	})
	return snapshot, err
}

func (store *PostgresProjectionStore) Heartbeat(
	ctx context.Context, claim ProjectionClaim, workerID string, lease time.Duration,
) error {
	if store == nil || store.pool == nil || validateProjectionClaim(claim, claim.TenantID) != nil ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		lease < 30*time.Second || lease > 10*time.Minute {
		return ErrInvalidProjectionWork
	}
	var current bool
	if err := store.pool.QueryRow(ctx, `SELECT askdata.heartbeat_release_projection($1,$2,$3,$4,$5)`,
		claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken,
		int64(lease/time.Second),
	).Scan(&current); err != nil {
		return err
	}
	if !current {
		return ErrProjectionLeaseLost
	}
	return nil
}

func (store *PostgresProjectionStore) Complete(
	ctx context.Context, claim ProjectionClaim, workerID string, proof ProjectionProof,
) error {
	if store == nil || store.pool == nil || validateProjectionClaim(claim, claim.TenantID) != nil ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		proof.SchemaVersion != GraphProjectionSchemaVersion || proof.GraphHash.Validate() != nil ||
		proof.ObjectCount < 0 || proof.ObjectCount != proof.VertexCount+proof.EdgeCount {
		return ErrInvalidProjectionWork
	}
	detail, err := json.Marshal(struct {
		SchemaVersion string              `json:"schemaVersion"`
		GraphHash     askdata.ContentHash `json:"graphHash"`
		VertexCount   int                 `json:"vertexCount"`
		EdgeCount     int                 `json:"edgeCount"`
		ProofType     string              `json:"proofType"`
	}{proof.SchemaVersion, proof.GraphHash, proof.VertexCount, proof.EdgeCount, "CANONICAL_MUTATION_ACK"})
	if err != nil {
		return err
	}
	resourceVersion := GraphProjectionSchemaVersion + ":" + string(proof.GraphHash)
	var completed bool
	if err := store.pool.QueryRow(ctx, `SELECT askdata.complete_release_projection(
		$1,$2,$3,$4,$5,$6,$7,$8::jsonb
	)`, claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken,
		claim.ContentHash, resourceVersion, proof.ObjectCount, detail,
	).Scan(&completed); err != nil {
		return err
	}
	if !completed {
		return ErrProjectionLeaseLost
	}
	return nil
}

func (store *PostgresProjectionStore) Fail(
	ctx context.Context, claim ProjectionClaim, workerID, code string, retryable bool,
) error {
	if store == nil || store.pool == nil || uuid.Validate(claim.TenantID) != nil ||
		uuid.Validate(claim.ProjectionID) != nil || uuid.Validate(claim.LeaseToken) != nil ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 {
		return ErrInvalidProjectionWork
	}
	var failed bool
	if err := store.pool.QueryRow(ctx, `SELECT askdata.fail_release_projection($1,$2,$3,$4,$5,$6)`,
		claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken, code, retryable,
	).Scan(&failed); err != nil {
		return err
	}
	if !failed {
		return ErrProjectionLeaseLost
	}
	return nil
}

func projectionObjectType(value string) (ObjectType, error) {
	switch ObjectType(value) {
	case ObjectTypeSemanticModel, ObjectTypeMetric, ObjectTypeDimension, ObjectTypeMember:
		return ObjectType(value), nil
	default:
		return "", fmt.Errorf("%w: unsupported graph object type", ErrProjectionContract)
	}
}

func projectionEdgeFromRow(
	graphType, fromObjectID, fromVersionID string, fromVersionNo int,
	toObjectID, toVersionID string, toVersionNo int,
	relationshipVersionID, joinType, cardinality, fanoutPolicy string,
	certified bool,
) (ProjectionEdge, error) {
	edge := ProjectionEdge{
		Type:                  ProjectionEdgeType(graphType),
		From:                  ObjectVersionRef{ObjectID: askdata.ID(fromObjectID), VersionID: askdata.ID(fromVersionID), Version: fromVersionNo},
		To:                    ObjectVersionRef{ObjectID: askdata.ID(toObjectID), VersionID: askdata.ID(toVersionID), Version: toVersionNo},
		RelationshipVersionID: askdata.ID(relationshipVersionID), JoinType: registry.JoinType(joinType),
		Cardinality: registry.Cardinality(cardinality), FanoutPolicy: registry.FanoutPolicy(fanoutPolicy), Certified: certified,
	}
	switch edge.Type {
	case ProjectionEdgeModeledBy:
		edge.FromType, edge.ToType = ObjectTypeMetric, ObjectTypeSemanticModel
	case ProjectionEdgeHasDimension:
		edge.FromType, edge.ToType = ObjectTypeSemanticModel, ObjectTypeDimension
	case ProjectionEdgeHasMember:
		edge.FromType, edge.ToType = ObjectTypeDimension, ObjectTypeMember
	case ProjectionEdgeJoinsTo:
		edge.FromType, edge.ToType = ObjectTypeSemanticModel, ObjectTypeSemanticModel
	default:
		return ProjectionEdge{}, fmt.Errorf("%w: unsupported graph edge type", ErrProjectionContract)
	}
	if err := edge.Validate(); err != nil {
		return ProjectionEdge{}, fmt.Errorf("%w: %v", ErrProjectionContract, err)
	}
	return edge, nil
}

func validateProjectionCounts(
	snapshot ProjectionSnapshot,
	expectedVertices map[ObjectType]int,
	expectedEdges map[ProjectionEdgeType]int,
) error {
	actualVertices := map[ObjectType]int{}
	for _, vertex := range snapshot.Vertices {
		actualVertices[vertex.Type]++
	}
	actualEdges := map[ProjectionEdgeType]int{}
	for _, edge := range snapshot.Edges {
		actualEdges[edge.Type]++
	}
	for _, objectType := range []ObjectType{ObjectTypeSemanticModel, ObjectTypeMetric, ObjectTypeDimension, ObjectTypeMember} {
		if actualVertices[objectType] != expectedVertices[objectType] {
			return fmt.Errorf("%w: %s vertex count mismatch", ErrProjectionContract, objectType)
		}
	}
	for _, edgeType := range []ProjectionEdgeType{ProjectionEdgeModeledBy, ProjectionEdgeHasDimension, ProjectionEdgeHasMember, ProjectionEdgeJoinsTo} {
		if actualEdges[edgeType] != expectedEdges[edgeType] {
			return fmt.Errorf("%w: %s edge count mismatch", ErrProjectionContract, edgeType)
		}
	}
	return nil
}

var _ ProjectionStore = (*PostgresProjectionStore)(nil)
