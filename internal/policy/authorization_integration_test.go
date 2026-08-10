package policy

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresSemanticAccessRevalidatesTenantDomainRoleAndProjection(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()

	var tenantID, actorID, domainID, releaseID, releaseHash string
	err = adminPool.QueryRow(ctx, `SELECT release.tenant_id::text,membership.user_id::text,
		release.domain_id::text,release.id::text,release.content_hash
		FROM askdata.releases AS release
		JOIN platform.domain_memberships AS membership
		  ON membership.tenant_id=release.tenant_id AND membership.domain_id=release.domain_id
		JOIN platform.users AS actor
		  ON actor.tenant_id=membership.tenant_id AND actor.id=membership.user_id
		WHERE release.status IN ('READY','ACTIVE','SUPERSEDED')
		  AND membership.status='ACTIVE' AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
		  AND EXISTS(
		    SELECT 1 FROM platform.user_roles AS assignment
		    JOIN platform.roles AS role
		      ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
		    WHERE assignment.tenant_id=release.tenant_id AND assignment.user_id=membership.user_id
		      AND role.status='ACTIVE' AND role.deleted_at IS NULL
		  )
		  AND (
		    SELECT count(DISTINCT projection.target)=3
		    FROM askdata.release_projections AS projection
		    WHERE projection.tenant_id=release.tenant_id
		      AND projection.domain_id=release.domain_id AND projection.release_id=release.id
		      AND projection.target IN ('SEARCH_INDEX','POSTGRES_REGISTRY','EXECUTION_SEMANTIC_LAYER')
		      AND projection.status='READY'
		      AND projection.expected_content_hash=release.content_hash
		      AND projection.applied_content_hash=release.content_hash
		  )
		ORDER BY release.updated_at DESC,release.id,membership.user_id LIMIT 1`).Scan(
		&tenantID, &actorID, &domainID, &releaseID, &releaseHash,
	)
	if err != nil {
		t.Skipf("no release with all SEC-001 projections and an active actor: %v", err)
	}

	roleRows, err := adminPool.Query(ctx, `SELECT role.id::text
		FROM platform.user_roles AS assignment
		JOIN platform.roles AS role
		  ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
		WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
		  AND role.status='ACTIVE' AND role.deleted_at IS NULL
		ORDER BY role.id LIMIT $3`, tenantID, actorID, askdata.MaxPolicyRoles+1)
	if err != nil {
		t.Fatal(err)
	}
	roleIDs := []askdata.ID{}
	for roleRows.Next() {
		var roleID string
		if err := roleRows.Scan(&roleID); err != nil {
			roleRows.Close()
			t.Fatal(err)
		}
		roleIDs = append(roleIDs, askdata.ID(roleID))
	}
	if err := roleRows.Err(); err != nil {
		roleRows.Close()
		t.Fatal(err)
	}
	roleRows.Close()
	if len(roleIDs) == 0 || len(roleIDs) > askdata.MaxPolicyRoles {
		t.Skip("fixture actor has an unsupported role count")
	}

	var objectType, objectID, objectVersionID string
	if err := adminPool.QueryRow(ctx, `SELECT object_type,object_id::text,object_version_id::text
		FROM askdata.release_objects WHERE tenant_id=$1 AND domain_id=$2 AND release_id=$3
		ORDER BY object_type,object_id,object_version_id LIMIT 1`, tenantID, domainID, releaseID,
	).Scan(&objectType, &objectID, &objectVersionID); err != nil {
		t.Skipf("release has no object fixture: %v", err)
	}
	scope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)}, roleIDs,
		askdata.ReleaseRef{ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(releaseHash)},
	)
	if err != nil {
		t.Fatal(err)
	}
	ref := SemanticObjectRef{
		DomainID: askdata.ID(domainID), ObjectType: objectType,
		ObjectID: askdata.ID(objectID), ObjectVersionID: askdata.ID(objectVersionID),
	}
	store := NewPostgresStore(appPool)
	requestContext := database.WithAccessContext(ctx, actorID, domainID)
	for _, test := range []struct {
		projection SemanticProjection
		objects    []SemanticObjectRef
	}{
		{projection: SemanticProjectionSearch},
		{projection: SemanticProjectionRegistry, objects: []SemanticObjectRef{ref}},
		{projection: SemanticProjectionExecution, objects: []SemanticObjectRef{ref}},
	} {
		objects, err := CanonicalSemanticObjectRefs(test.objects)
		if err != nil {
			t.Fatal(err)
		}
		request := SemanticAccessRequest{
			Scope: scope, DomainID: askdata.ID(domainID), Projection: test.projection, Objects: objects,
		}
		snapshot, err := store.ResolveSemanticAccess(requestContext, request)
		if err != nil || snapshot.ValidateAgainst(request) != nil {
			t.Fatalf("ResolveSemanticAccess(%s) = %#v, %v", test.projection, snapshot, err)
		}
	}

	crossRoleScope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)},
		[]askdata.ID{askdata.ID(uuid.NewString())}, scope.Release,
	)
	if err != nil {
		t.Fatal(err)
	}
	crossTenantScope, err := askdata.NewPolicyScope(
		askdata.ID(uuid.NewString()), askdata.ID(actorID), []askdata.ID{askdata.ID(domainID)}, roleIDs, scope.Release,
	)
	if err != nil {
		t.Fatal(err)
	}
	otherDomainID := askdata.ID(uuid.NewString())
	crossDomainScope, err := askdata.NewPolicyScope(
		askdata.ID(tenantID), askdata.ID(actorID), []askdata.ID{otherDomainID}, roleIDs, scope.Release,
	)
	if err != nil {
		t.Fatal(err)
	}
	negative := []struct {
		name   string
		ctx    context.Context
		scope  askdata.PolicyScope
		domain askdata.ID
	}{
		{name: "role", ctx: requestContext, scope: crossRoleScope, domain: askdata.ID(domainID)},
		{name: "tenant", ctx: requestContext, scope: crossTenantScope, domain: askdata.ID(domainID)},
		{name: "domain", ctx: database.WithAccessContext(ctx, actorID, string(otherDomainID)), scope: crossDomainScope, domain: otherDomainID},
	}
	for _, test := range negative {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.ResolveSemanticAccess(test.ctx, SemanticAccessRequest{
				Scope: test.scope, DomainID: test.domain, Projection: SemanticProjectionSearch,
			})
			if !errors.Is(err, ErrSemanticAccessDenied) {
				t.Fatalf("ResolveSemanticAccess() error = %v", err)
			}
		})
	}
}
