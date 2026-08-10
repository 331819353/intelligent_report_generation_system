package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

var ErrReleaseNotRebuildable = errors.New("semantic release cannot be used to rebuild the graph")

// LoadReleaseSnapshot reconstructs the canonical graph projection from an
// immutable release. It is intentionally independent of the normal projection
// lease: disaster recovery must also work for ACTIVE, SUPERSEDED and RETAINED
// releases after the derived graph has been lost.
func (store *PostgresProjectionStore) LoadReleaseSnapshot(
	ctx context.Context, tenantID, releaseID string,
) (snapshot ProjectionSnapshot, err error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil ||
		uuid.Validate(releaseID) != nil {
		return ProjectionSnapshot{}, ErrInvalidProjectionWork
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var manifestHash string
		var releaseObjectCount int
		if err := tx.QueryRow(ctx, `SELECT release.domain_id::text,
			release.semantic_version,release.content_hash,release.object_count,
			(SELECT count(*) FROM askdata.release_objects AS object
			 WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id),
			askdata.release_manifest_hash(release.id)
		FROM askdata.releases AS release
		WHERE release.tenant_id=$1 AND release.id=$2
		  AND release.status IN ('READY','ACTIVE','SUPERSEDED','RETAINED')`,
			tenantID, releaseID,
		).Scan(
			&snapshot.DomainID, &snapshot.SemanticVersion, &snapshot.ContentHash,
			&snapshot.ManifestCount, &releaseObjectCount, &manifestHash,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrReleaseNotRebuildable
			}
			return err
		}
		snapshot.TenantID = askdata.ID(tenantID)
		snapshot.ReleaseID = askdata.ID(releaseID)
		if snapshot.ManifestCount < 1 || snapshot.ManifestCount != releaseObjectCount ||
			manifestHash != string(snapshot.ContentHash) {
			return fmt.Errorf("%w: release manifest count or hash is inconsistent", ErrProjectionContract)
		}
		models, modelRefs, err := loadReleasedModels(ctx, tx, tenantID, releaseID)
		if err != nil {
			return err
		}
		snapshot.Vertices = append(snapshot.Vertices, models...)
		metrics, modeledBy, err := loadReleasedMetrics(ctx, tx, tenantID, releaseID, modelRefs)
		if err != nil {
			return err
		}
		snapshot.Vertices = append(snapshot.Vertices, metrics...)
		snapshot.Edges = append(snapshot.Edges, modeledBy...)
		dimensions, dimensionRefs, hasDimensions, err := loadReleasedDimensions(
			ctx, tx, tenantID, releaseID, modelRefs,
		)
		if err != nil {
			return err
		}
		snapshot.Vertices = append(snapshot.Vertices, dimensions...)
		snapshot.Edges = append(snapshot.Edges, hasDimensions...)
		members, hasMembers, err := loadReleasedMembers(
			ctx, tx, tenantID, releaseID, dimensionRefs,
		)
		if err != nil {
			return err
		}
		snapshot.Vertices = append(snapshot.Vertices, members...)
		snapshot.Edges = append(snapshot.Edges, hasMembers...)
		joins, err := loadReleasedRelationships(ctx, tx, tenantID, releaseID, modelRefs)
		if err != nil {
			return err
		}
		snapshot.Edges = append(snapshot.Edges, joins...)
		return snapshot.Validate()
	})
	return snapshot, err
}

func loadReleasedModels(
	ctx context.Context, tx pgx.Tx, tenantID, releaseID string,
) ([]ProjectionVertex, map[string]ObjectVersionRef, error) {
	rows, err := tx.Query(ctx, `SELECT model.model_id::text,model.id::text,model.version_no
		FROM askdata.release_objects AS released
		JOIN askdata.semantic_models AS model
		  ON model.tenant_id=released.tenant_id AND model.domain_id=released.domain_id
		 AND model.model_id=released.object_id AND model.id=released.object_version_id
		 AND model.content_hash=released.content_hash AND model.status='CERTIFIED'
		WHERE released.tenant_id=$1 AND released.release_id=$2
		  AND released.object_type='SEMANTIC_MODEL'
		ORDER BY model.id`, tenantID, releaseID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	vertices := []ProjectionVertex{}
	refs := map[string]ObjectVersionRef{}
	for rows.Next() {
		ref := ObjectVersionRef{}
		if err := rows.Scan(&ref.ObjectID, &ref.VersionID, &ref.Version); err != nil {
			return nil, nil, err
		}
		vertices = append(vertices, ProjectionVertex{Type: ObjectTypeSemanticModel, Ref: ref})
		refs[string(ref.VersionID)] = ref
	}
	return vertices, refs, rows.Err()
}

func loadReleasedMetrics(
	ctx context.Context, tx pgx.Tx, tenantID, releaseID string,
	models map[string]ObjectVersionRef,
) ([]ProjectionVertex, []ProjectionEdge, error) {
	rows, err := tx.Query(ctx, `SELECT metric.metric_id::text,metric.id::text,
		metric.version_no,metric.semantic_model_version_id::text
		FROM askdata.release_objects AS released
		JOIN askdata.metric_versions AS metric
		  ON metric.tenant_id=released.tenant_id AND metric.domain_id=released.domain_id
		 AND metric.metric_id=released.object_id AND metric.id=released.object_version_id
		 AND metric.content_hash=released.content_hash AND metric.status='CERTIFIED'
		WHERE released.tenant_id=$1 AND released.release_id=$2
		  AND released.object_type='METRIC'
		ORDER BY metric.id`, tenantID, releaseID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	vertices := []ProjectionVertex{}
	edges := []ProjectionEdge{}
	for rows.Next() {
		ref := ObjectVersionRef{}
		var modelVersionID string
		if err := rows.Scan(&ref.ObjectID, &ref.VersionID, &ref.Version, &modelVersionID); err != nil {
			return nil, nil, err
		}
		model, ok := models[modelVersionID]
		if !ok {
			return nil, nil, fmt.Errorf("%w: metric parent model is absent from release", ErrProjectionContract)
		}
		vertices = append(vertices, ProjectionVertex{Type: ObjectTypeMetric, Ref: ref})
		edges = append(edges, ProjectionEdge{
			Type: ProjectionEdgeModeledBy, FromType: ObjectTypeMetric, From: ref,
			ToType: ObjectTypeSemanticModel, To: model,
		})
	}
	return vertices, edges, rows.Err()
}

func loadReleasedDimensions(
	ctx context.Context, tx pgx.Tx, tenantID, releaseID string,
	models map[string]ObjectVersionRef,
) ([]ProjectionVertex, map[string]ObjectVersionRef, []ProjectionEdge, error) {
	rows, err := tx.Query(ctx, `SELECT dimension.dimension_id::text,dimension.id::text,
		dimension.version_no,dimension.semantic_model_version_id::text
		FROM askdata.release_objects AS released
		JOIN askdata.dimensions AS dimension
		  ON dimension.tenant_id=released.tenant_id AND dimension.domain_id=released.domain_id
		 AND dimension.dimension_id=released.object_id AND dimension.id=released.object_version_id
		 AND dimension.content_hash=released.content_hash AND dimension.status='CERTIFIED'
		WHERE released.tenant_id=$1 AND released.release_id=$2
		  AND released.object_type='DIMENSION'
		ORDER BY dimension.id`, tenantID, releaseID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	vertices := []ProjectionVertex{}
	refs := map[string]ObjectVersionRef{}
	edges := []ProjectionEdge{}
	for rows.Next() {
		ref := ObjectVersionRef{}
		var modelVersionID string
		if err := rows.Scan(&ref.ObjectID, &ref.VersionID, &ref.Version, &modelVersionID); err != nil {
			return nil, nil, nil, err
		}
		model, ok := models[modelVersionID]
		if !ok {
			return nil, nil, nil, fmt.Errorf("%w: dimension parent model is absent from release", ErrProjectionContract)
		}
		vertices = append(vertices, ProjectionVertex{Type: ObjectTypeDimension, Ref: ref})
		refs[string(ref.VersionID)] = ref
		edges = append(edges, ProjectionEdge{
			Type: ProjectionEdgeHasDimension, FromType: ObjectTypeSemanticModel, From: model,
			ToType: ObjectTypeDimension, To: ref,
		})
	}
	return vertices, refs, edges, rows.Err()
}

func loadReleasedMembers(
	ctx context.Context, tx pgx.Tx, tenantID, releaseID string,
	dimensions map[string]ObjectVersionRef,
) ([]ProjectionVertex, []ProjectionEdge, error) {
	rows, err := tx.Query(ctx, `SELECT member.member_id::text,member.id::text,
		member.version_no,member.dimension_version_id::text,
		CASE WHEN member.valid_from<=CURRENT_TIMESTAMP
		 AND (member.valid_to IS NULL OR member.valid_to>CURRENT_TIMESTAMP)
		 THEN 'ACTIVE' ELSE 'EXPIRED' END
		FROM askdata.release_objects AS released
		JOIN askdata.dimension_members AS member
		  ON member.tenant_id=released.tenant_id AND member.domain_id=released.domain_id
		 AND member.member_id=released.object_id AND member.id=released.object_version_id
		 AND member.content_hash=released.content_hash AND member.status='CERTIFIED'
		WHERE released.tenant_id=$1 AND released.release_id=$2
		  AND released.object_type='MEMBER'
		ORDER BY member.id`, tenantID, releaseID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	vertices := []ProjectionVertex{}
	edges := []ProjectionEdge{}
	for rows.Next() {
		ref := ObjectVersionRef{}
		var dimensionVersionID string
		var status MemberStatus
		if err := rows.Scan(
			&ref.ObjectID, &ref.VersionID, &ref.Version, &dimensionVersionID, &status,
		); err != nil {
			return nil, nil, err
		}
		dimension, ok := dimensions[dimensionVersionID]
		if !ok {
			return nil, nil, fmt.Errorf("%w: member parent dimension is absent from release", ErrProjectionContract)
		}
		vertices = append(vertices, ProjectionVertex{Type: ObjectTypeMember, Ref: ref, MemberStatus: status})
		edges = append(edges, ProjectionEdge{
			Type: ProjectionEdgeHasMember, FromType: ObjectTypeDimension, From: dimension,
			ToType: ObjectTypeMember, To: ref,
		})
	}
	return vertices, edges, rows.Err()
}

func loadReleasedRelationships(
	ctx context.Context, tx pgx.Tx, tenantID, releaseID string,
	models map[string]ObjectVersionRef,
) ([]ProjectionEdge, error) {
	rows, err := tx.Query(ctx, `SELECT relationship.id::text,
		relationship.left_model_version_id::text,relationship.right_model_version_id::text,
		relationship.join_type,relationship.cardinality,relationship.fanout_policy
		FROM askdata.release_objects AS released
		JOIN askdata.relationships AS relationship
		  ON relationship.tenant_id=released.tenant_id AND relationship.domain_id=released.domain_id
		 AND relationship.relationship_id=released.object_id
		 AND relationship.id=released.object_version_id
		 AND relationship.content_hash=released.content_hash
		 AND relationship.status='CERTIFIED' AND relationship.relationship_type='MODEL_JOIN'
		WHERE released.tenant_id=$1 AND released.release_id=$2
		  AND released.object_type='RELATIONSHIP'
		ORDER BY relationship.id`, tenantID, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := []ProjectionEdge{}
	for rows.Next() {
		var relationshipID, leftID, rightID string
		var joinType registry.JoinType
		var cardinality registry.Cardinality
		var fanoutPolicy registry.FanoutPolicy
		if err := rows.Scan(
			&relationshipID, &leftID, &rightID, &joinType, &cardinality, &fanoutPolicy,
		); err != nil {
			return nil, err
		}
		left, leftOK := models[leftID]
		right, rightOK := models[rightID]
		if !leftOK || !rightOK {
			return nil, fmt.Errorf("%w: relationship endpoint is absent from release", ErrProjectionContract)
		}
		edges = append(edges, ProjectionEdge{
			Type: ProjectionEdgeJoinsTo, FromType: ObjectTypeSemanticModel, From: left,
			ToType: ObjectTypeSemanticModel, To: right,
			RelationshipVersionID: askdata.ID(relationshipID), JoinType: joinType,
			Cardinality: cardinality, FanoutPolicy: fanoutPolicy, Certified: true,
		})
	}
	return edges, rows.Err()
}
