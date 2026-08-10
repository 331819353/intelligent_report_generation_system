package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

const MaxSemanticAuthorizationObjects = 10_000

var (
	ErrInvalidSemanticAccess = errors.New("semantic access request is invalid")
	ErrSemanticAccessDenied  = errors.New("semantic access is denied")
)

// SemanticProjection is the release-pinned runtime surface that must still be
// usable when a security stage is entered. Authorization never follows a
// mutable "current release" pointer.
type SemanticProjection string

const (
	SemanticProjectionSearch    SemanticProjection = "SEARCH_INDEX"
	SemanticProjectionRegistry  SemanticProjection = "POSTGRES_REGISTRY"
	SemanticProjectionExecution SemanticProjection = "EXECUTION_SEMANTIC_LAYER"
)

// SemanticObjectRef is deliberately label-free. A policy response cannot
// disclose the name, definition, aliases or physical location of an object
// that has not passed authorization.
type SemanticObjectRef struct {
	DomainID        askdata.ID `json:"domainId"`
	ObjectType      string     `json:"objectType"`
	ObjectID        askdata.ID `json:"objectId"`
	ObjectVersionID askdata.ID `json:"objectVersionId"`
}

func (ref SemanticObjectRef) Validate() error {
	if ref.DomainID.Validate() != nil || ref.ObjectID.Validate() != nil ||
		ref.ObjectVersionID.Validate() != nil || !validSemanticObjectType(ref.ObjectType) {
		return ErrInvalidSemanticAccess
	}
	return nil
}

type SemanticAccessRequest struct {
	Scope      askdata.PolicyScope `json:"scope"`
	DomainID   askdata.ID          `json:"domainId"`
	Projection SemanticProjection  `json:"projection"`
	Objects    []SemanticObjectRef `json:"objects"`
}

func (request SemanticAccessRequest) Validate() error {
	if request.Scope.Validate() != nil || request.DomainID.Validate() != nil ||
		!containsSemanticID(request.Scope.DomainIDs, request.DomainID) ||
		!validSemanticProjection(request.Projection) ||
		len(request.Objects) > MaxSemanticAuthorizationObjects {
		return ErrInvalidSemanticAccess
	}
	if !semanticRefsAreCanonical(request.Objects) {
		return ErrInvalidSemanticAccess
	}
	for _, ref := range request.Objects {
		if ref.Validate() != nil || ref.DomainID != request.DomainID {
			return ErrInvalidSemanticAccess
		}
	}
	return nil
}

// SemanticAccessSnapshot is an immutable, label-free view of the objects the
// current database policy allowed for one exact scope and runtime projection.
// SnapshotHash is safe to include in later audit hashes.
type SemanticAccessSnapshot struct {
	ScopeHash    askdata.ContentHash `json:"scopeHash"`
	Release      askdata.ReleaseRef  `json:"release"`
	DomainID     askdata.ID          `json:"domainId"`
	Projection   SemanticProjection  `json:"projection"`
	Objects      []SemanticObjectRef `json:"objects"`
	SnapshotHash askdata.ContentHash `json:"snapshotHash"`
}

func NewSemanticAccessSnapshot(
	request SemanticAccessRequest,
	objects []SemanticObjectRef,
) (SemanticAccessSnapshot, error) {
	if request.Validate() != nil {
		return SemanticAccessSnapshot{}, ErrInvalidSemanticAccess
	}
	canonical, err := CanonicalSemanticObjectRefs(objects)
	if err != nil {
		return SemanticAccessSnapshot{}, err
	}
	for _, ref := range canonical {
		if ref.DomainID != request.DomainID ||
			(len(request.Objects) > 0 && !containsSemanticRef(request.Objects, ref)) {
			return SemanticAccessSnapshot{}, ErrInvalidSemanticAccess
		}
	}
	snapshot := SemanticAccessSnapshot{
		ScopeHash: request.Scope.PolicyHash, Release: request.Scope.Release,
		DomainID: request.DomainID, Projection: request.Projection, Objects: canonical,
	}
	hash, err := semanticAccessSnapshotHash(snapshot)
	if err != nil {
		return SemanticAccessSnapshot{}, err
	}
	snapshot.SnapshotHash = hash
	return snapshot, nil
}

func (snapshot SemanticAccessSnapshot) ValidateAgainst(request SemanticAccessRequest) error {
	if request.Validate() != nil || snapshot.Validate() != nil ||
		snapshot.ScopeHash != request.Scope.PolicyHash ||
		snapshot.Release != request.Scope.Release || snapshot.DomainID != request.DomainID ||
		snapshot.Projection != request.Projection {
		return ErrInvalidSemanticAccess
	}
	for _, ref := range snapshot.Objects {
		if len(request.Objects) > 0 && !containsSemanticRef(request.Objects, ref) {
			return ErrInvalidSemanticAccess
		}
	}
	return nil
}

// Validate verifies the self-contained snapshot proof without requiring the
// original request value.
func (snapshot SemanticAccessSnapshot) Validate() error {
	if snapshot.ScopeHash.Validate() != nil || snapshot.Release.Validate() != nil ||
		snapshot.DomainID.Validate() != nil || !validSemanticProjection(snapshot.Projection) ||
		snapshot.SnapshotHash.Validate() != nil || !semanticRefsAreCanonical(snapshot.Objects) {
		return ErrInvalidSemanticAccess
	}
	for _, ref := range snapshot.Objects {
		if ref.Validate() != nil || ref.DomainID != snapshot.DomainID {
			return ErrInvalidSemanticAccess
		}
	}
	expected, err := semanticAccessSnapshotHash(snapshot)
	if err != nil || expected != snapshot.SnapshotHash {
		return ErrInvalidSemanticAccess
	}
	return nil
}

// CanonicalSemanticObjectRefs returns an isolated, sorted, duplicate-free
// copy suitable for hashing and exact set comparisons.
func CanonicalSemanticObjectRefs(values []SemanticObjectRef) ([]SemanticObjectRef, error) {
	if len(values) > MaxSemanticAuthorizationObjects {
		return nil, ErrInvalidSemanticAccess
	}
	result := append([]SemanticObjectRef(nil), values...)
	for _, ref := range result {
		if ref.Validate() != nil {
			return nil, ErrInvalidSemanticAccess
		}
	}
	sort.Slice(result, func(i, j int) bool { return semanticRefLess(result[i], result[j]) })
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, ErrInvalidSemanticAccess
		}
	}
	if result == nil {
		result = []SemanticObjectRef{}
	}
	return result, nil
}

// ResolveSemanticAccess revalidates authenticated identity, the complete role
// set, every domain in the pinned scope, release identity and projection
// readiness in one tenant transaction. It returns no semantic labels.
func (store *PostgresStore) ResolveSemanticAccess(
	ctx context.Context,
	request SemanticAccessRequest,
) (snapshot SemanticAccessSnapshot, err error) {
	if store == nil || store.pool == nil || request.Validate() != nil {
		return SemanticAccessSnapshot{}, ErrInvalidSemanticAccess
	}
	access, authenticated := database.AccessContextFromContext(ctx)
	if !authenticated || access.UserID != string(request.Scope.ActorID) ||
		access.DomainID != string(request.DomainID) {
		return SemanticAccessSnapshot{}, ErrSemanticAccessDenied
	}
	tenantID, actorID, domainID, releaseID, domainIDs, roleIDs, objectVersionIDs, err :=
		parseSemanticAccessUUIDs(request)
	if err != nil {
		return SemanticAccessSnapshot{}, ErrInvalidSemanticAccess
	}
	allowedObjects := []SemanticObjectRef{}
	err = database.WithTenantTx(ctx, store.pool, tenantID.String(), func(tx pgx.Tx) error {
		allowed, err := semanticScopeIsCurrent(
			ctx, tx, request, actorID, domainID, releaseID, domainIDs,
		)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrSemanticAccessDenied
		}
		currentRoles, err := loadCurrentSemanticRoleIDs(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !equalUUIDs(currentRoles, roleIDs) {
			return ErrSemanticAccessDenied
		}
		allowedObjects, err = loadAuthorizedSemanticObjects(
			ctx, tx, request, releaseID, domainID, objectVersionIDs,
		)
		return err
	})
	if err != nil {
		return SemanticAccessSnapshot{}, err
	}
	return NewSemanticAccessSnapshot(request, allowedObjects)
}

func semanticScopeIsCurrent(
	ctx context.Context,
	tx pgx.Tx,
	request SemanticAccessRequest,
	actorID, domainID, releaseID uuid.UUID,
	domainIDs []uuid.UUID,
) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT
		EXISTS(
		  SELECT 1 FROM platform.users AS actor
		  WHERE actor.id=$1 AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
		)
		AND (
		  SELECT count(DISTINCT membership.domain_id)=cardinality($2::uuid[])
		  FROM platform.domain_memberships AS membership
		  JOIN platform.business_domains AS domain
		    ON domain.tenant_id=membership.tenant_id AND domain.id=membership.domain_id
		  WHERE membership.user_id=$1 AND membership.domain_id=ANY($2::uuid[])
		    AND membership.status='ACTIVE' AND domain.status='ACTIVE' AND domain.deleted_at IS NULL
		)
		AND EXISTS(
		  SELECT 1 FROM askdata.releases AS release
		  JOIN askdata.release_projections AS projection
		    ON projection.tenant_id=release.tenant_id
		   AND projection.domain_id=release.domain_id
		   AND projection.release_id=release.id
		  WHERE release.id=$3 AND release.domain_id=$4 AND release.content_hash=$5
		    AND release.status IN ('READY','ACTIVE','SUPERSEDED')
		    AND projection.target=$6 AND projection.status='READY'
		    AND projection.expected_content_hash=release.content_hash
		    AND projection.applied_content_hash=release.content_hash
		)`, actorID, domainIDs, releaseID, domainID,
		request.Scope.Release.ContentHash, request.Projection,
	).Scan(&allowed)
	return allowed, err
}

func loadCurrentSemanticRoleIDs(ctx context.Context, tx pgx.Tx, actorID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT role.id
		FROM platform.user_roles AS assignment
		JOIN platform.roles AS role
		  ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
		WHERE assignment.user_id=$1 AND role.status='ACTIVE' AND role.deleted_at IS NULL
		ORDER BY role.id LIMIT $2`, actorID, askdata.MaxPolicyRoles+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []uuid.UUID{}
	for rows.Next() {
		var roleID uuid.UUID
		if err := rows.Scan(&roleID); err != nil {
			return nil, err
		}
		result = append(result, roleID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > askdata.MaxPolicyRoles {
		return nil, ErrSemanticAccessDenied
	}
	return result, nil
}

func loadAuthorizedSemanticObjects(
	ctx context.Context,
	tx pgx.Tx,
	request SemanticAccessRequest,
	releaseID, domainID uuid.UUID,
	objectVersionIDs []uuid.UUID,
) ([]SemanticObjectRef, error) {
	query := `SELECT object_type,object_id::text,object_version_id::text
		FROM askdata.release_objects
		WHERE release_id=$1 AND domain_id=$2`
	arguments := []any{releaseID, domainID}
	if len(request.Objects) > 0 {
		query += ` AND object_version_id=ANY($3::uuid[])`
		arguments = append(arguments, objectVersionIDs)
		query += ` ORDER BY object_type,object_id,object_version_id LIMIT $4`
	} else {
		query += ` ORDER BY object_type,object_id,object_version_id LIMIT $3`
	}
	arguments = append(arguments, MaxSemanticAuthorizationObjects+1)
	rows, err := tx.Query(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []SemanticObjectRef{}
	for rows.Next() {
		var ref SemanticObjectRef
		ref.DomainID = request.DomainID
		if err := rows.Scan(&ref.ObjectType, &ref.ObjectID, &ref.ObjectVersionID); err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > MaxSemanticAuthorizationObjects {
		return nil, ErrSemanticAccessDenied
	}
	return result, nil
}

func parseSemanticAccessUUIDs(request SemanticAccessRequest) (
	tenantID, actorID, domainID, releaseID uuid.UUID,
	domainIDs, roleIDs, objectVersionIDs []uuid.UUID,
	err error,
) {
	parse := func(value askdata.ID) (uuid.UUID, error) {
		parsed, parseErr := uuid.Parse(string(value))
		if parseErr != nil || parsed.String() != string(value) {
			return uuid.Nil, ErrInvalidSemanticAccess
		}
		return parsed, nil
	}
	if tenantID, err = parse(request.Scope.TenantID); err != nil {
		return
	}
	if actorID, err = parse(request.Scope.ActorID); err != nil {
		return
	}
	if domainID, err = parse(request.DomainID); err != nil {
		return
	}
	if releaseID, err = parse(request.Scope.Release.ReleaseID); err != nil {
		return
	}
	for _, value := range request.Scope.DomainIDs {
		var parsed uuid.UUID
		if parsed, err = parse(value); err != nil {
			return
		}
		domainIDs = append(domainIDs, parsed)
	}
	for _, value := range request.Scope.RoleIDs {
		var parsed uuid.UUID
		if parsed, err = parse(value); err != nil {
			return
		}
		roleIDs = append(roleIDs, parsed)
	}
	for _, value := range request.Objects {
		var parsed uuid.UUID
		if parsed, err = parse(value.ObjectVersionID); err != nil {
			return
		}
		objectVersionIDs = append(objectVersionIDs, parsed)
	}
	return
}

func semanticAccessSnapshotHash(snapshot SemanticAccessSnapshot) (askdata.ContentHash, error) {
	copy := snapshot
	copy.SnapshotHash = ""
	payload, err := json.Marshal(copy)
	if err != nil {
		return "", fmt.Errorf("hash semantic access snapshot: %w", err)
	}
	return askdata.HashBytes(payload), nil
}

func semanticRefsAreCanonical(values []SemanticObjectRef) bool {
	for index := range values {
		if index > 0 && !semanticRefLess(values[index-1], values[index]) {
			return false
		}
	}
	return true
}

func semanticRefLess(left, right SemanticObjectRef) bool {
	if left.DomainID != right.DomainID {
		return left.DomainID < right.DomainID
	}
	if left.ObjectType != right.ObjectType {
		return left.ObjectType < right.ObjectType
	}
	if left.ObjectID != right.ObjectID {
		return left.ObjectID < right.ObjectID
	}
	return left.ObjectVersionID < right.ObjectVersionID
}

func containsSemanticRef(values []SemanticObjectRef, want SemanticObjectRef) bool {
	index := sort.Search(len(values), func(index int) bool { return !semanticRefLess(values[index], want) })
	return index < len(values) && values[index] == want
}

func containsSemanticID(values []askdata.ID, want askdata.ID) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= want })
	return index < len(values) && values[index] == want
}

func validSemanticProjection(value SemanticProjection) bool {
	return value == SemanticProjectionSearch || value == SemanticProjectionRegistry ||
		value == SemanticProjectionExecution
}

func validSemanticObjectType(value string) bool {
	switch value {
	case "DOMAIN", "ENTITY", "SEMANTIC_MODEL", "MEASURE", "METRIC", "DIMENSION", "MEMBER",
		"METRIC_DIMENSION", "HIERARCHY", "RELATIONSHIP", "QUALITY_RULE", "BUSINESS_TERM",
		"CERTIFIED_EXAMPLE", "TIME_CONTRACT", "KPI_BUNDLE", "EVAL_CASE":
		return true
	default:
		return false
	}
}

func equalUUIDs(left, right []uuid.UUID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
