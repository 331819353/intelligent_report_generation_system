package security

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/policy"
)

func TestAuthorizerRevalidatesAllThreeStagesAndKeepsReceiptsLabelFree(t *testing.T) {
	scope := authorizationTestScope(t, "tenant-a", "actor-a", "sales", "analyst")
	objects := authorizationTestObjects("sales")
	resolver := &authorizationResolver{
		tenantID: "tenant-a", domainID: "sales", roleID: "analyst",
		catalog: objects, secretName: "Quarterly Revenue Secret",
	}
	authorizer, err := NewAuthorizer(resolver)
	if err != nil {
		t.Fatal(err)
	}

	recall, err := authorizer.AuthorizeRecall(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("recall request")),
	)
	if err != nil || recall.Validate() != nil || !recall.Allows(objects[0]) || !recall.Allows(objects[1]) {
		t.Fatalf("AuthorizeRecall() = %#v, %v", recall, err)
	}
	binding, err := authorizer.AuthorizeBinding(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("candidate set")), objects,
	)
	if err != nil || binding.Validate() != nil {
		t.Fatalf("AuthorizeBinding() = %#v, %v", binding, err)
	}
	execution, err := authorizer.AuthorizeExecution(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("query plan")), objects,
	)
	if err != nil || execution.Validate() != nil {
		t.Fatalf("AuthorizeExecution() = %#v, %v", execution, err)
	}

	wantStages := []policy.SemanticProjection{
		policy.SemanticProjectionSearch,
		policy.SemanticProjectionRegistry,
		policy.SemanticProjectionExecution,
	}
	if len(resolver.calls) != len(wantStages) {
		t.Fatalf("resolver calls = %d", len(resolver.calls))
	}
	for index, want := range wantStages {
		if resolver.calls[index].Projection != want || resolver.calls[index].Scope.PolicyHash != scope.PolicyHash {
			t.Fatalf("call[%d] = %#v", index, resolver.calls[index])
		}
	}
	if recall.AuditHash == binding.AuditHash || binding.AuditHash == execution.AuditHash ||
		recall.ScopeHash != scope.PolicyHash || binding.ScopeHash != scope.PolicyHash ||
		execution.ScopeHash != scope.PolicyHash {
		t.Fatal("stage receipts did not bind the exact policy scope and stage")
	}
	raw, err := json.Marshal([]AuthorizationReceipt{recall, binding, execution})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), resolver.secretName) {
		t.Fatalf("authorization receipt leaked an object name: %s", raw)
	}
}

func TestAuthorizerDeniesCrossTenantDomainAndRoleScopes(t *testing.T) {
	objects := authorizationTestObjects("sales")
	tests := []struct {
		name     string
		scope    askdata.PolicyScope
		domainID askdata.ID
	}{
		{name: "tenant", scope: authorizationTestScope(t, "tenant-b", "actor-a", "sales", "analyst"), domainID: "sales"},
		{name: "domain", scope: authorizationTestScope(t, "tenant-a", "actor-a", "finance", "analyst"), domainID: "finance"},
		{name: "role", scope: authorizationTestScope(t, "tenant-a", "actor-a", "sales", "viewer"), domainID: "sales"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := &authorizationResolver{
				tenantID: "tenant-a", domainID: "sales", roleID: "analyst", catalog: objects,
			}
			authorizer, err := NewAuthorizer(resolver)
			if err != nil {
				t.Fatal(err)
			}
			_, err = authorizer.AuthorizeRecall(
				context.Background(), test.scope, test.domainID, askdata.HashBytes([]byte("request")),
			)
			if !errors.Is(err, ErrAuthorizationDenied) {
				t.Fatalf("AuthorizeRecall() error = %v", err)
			}
		})
	}
}

func TestAuthorizerFailsClosedOnPartialBindingAndRoleRevocationBeforeExecution(t *testing.T) {
	scope := authorizationTestScope(t, "tenant-a", "actor-a", "sales", "analyst")
	objects := authorizationTestObjects("sales")
	resolver := &authorizationResolver{
		tenantID: "tenant-a", domainID: "sales", roleID: "analyst", catalog: objects,
	}
	authorizer, err := NewAuthorizer(resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authorizer.AuthorizeRecall(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("recall")),
	); err != nil {
		t.Fatal(err)
	}

	resolver.partialProjection = policy.SemanticProjectionRegistry
	_, err = authorizer.AuthorizeBinding(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("binding")), objects,
	)
	if !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("partial binding error = %v", err)
	}
	resolver.partialProjection = ""
	if _, err := authorizer.AuthorizeBinding(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("binding retry")), objects,
	); err != nil {
		t.Fatal(err)
	}

	resolver.deniedProjection = policy.SemanticProjectionExecution
	_, err = authorizer.AuthorizeExecution(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("execution")), objects,
	)
	if !errors.Is(err, ErrAuthorizationDenied) {
		t.Fatalf("revoked execution error = %v", err)
	}
	if len(resolver.calls) != 4 {
		t.Fatalf("resolver calls = %d, want 4 fresh checks", len(resolver.calls))
	}
}

func TestAuthorizerDoesNotExposeResolverDiagnostics(t *testing.T) {
	const secretName = "Unauthorized Payroll Dimension"
	scope := authorizationTestScope(t, "tenant-a", "actor-a", "sales", "analyst")
	resolver := &authorizationResolver{
		tenantID: "tenant-a", domainID: "sales", roleID: "analyst",
		failure: errors.New("database rejected object " + secretName),
	}
	authorizer, err := NewAuthorizer(resolver)
	if err != nil {
		t.Fatal(err)
	}
	_, err = authorizer.AuthorizeRecall(
		context.Background(), scope, "sales", askdata.HashBytes([]byte("request")),
	)
	if !errors.Is(err, ErrAuthorizationUnavailable) || strings.Contains(err.Error(), secretName) {
		t.Fatalf("public error = %v", err)
	}
}

type authorizationResolver struct {
	tenantID, domainID, roleID string
	catalog                    []policy.SemanticObjectRef
	secretName                 string
	deniedProjection           policy.SemanticProjection
	partialProjection          policy.SemanticProjection
	failure                    error
	calls                      []policy.SemanticAccessRequest
}

func (resolver *authorizationResolver) ResolveSemanticAccess(
	_ context.Context,
	request policy.SemanticAccessRequest,
) (policy.SemanticAccessSnapshot, error) {
	resolver.calls = append(resolver.calls, request)
	if resolver.failure != nil {
		return policy.SemanticAccessSnapshot{}, resolver.failure
	}
	if string(request.Scope.TenantID) != resolver.tenantID || string(request.DomainID) != resolver.domainID ||
		len(request.Scope.RoleIDs) != 1 || string(request.Scope.RoleIDs[0]) != resolver.roleID ||
		request.Projection == resolver.deniedProjection {
		return policy.SemanticAccessSnapshot{}, policy.ErrSemanticAccessDenied
	}
	allowed := append([]policy.SemanticObjectRef(nil), resolver.catalog...)
	if len(request.Objects) > 0 {
		allowed = append([]policy.SemanticObjectRef(nil), request.Objects...)
	}
	if request.Projection == resolver.partialProjection && len(allowed) > 0 {
		allowed = allowed[:len(allowed)-1]
	}
	return policy.NewSemanticAccessSnapshot(request, allowed)
}

func authorizationTestScope(t *testing.T, tenantID, actorID, domainID, roleID askdata.ID) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		tenantID, actorID, []askdata.ID{domainID}, []askdata.ID{roleID},
		askdata.ReleaseRef{
			ReleaseID: "release-1", ContentHash: askdata.HashBytes([]byte("release")),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func authorizationTestObjects(domainID askdata.ID) []policy.SemanticObjectRef {
	objects, err := policy.CanonicalSemanticObjectRefs([]policy.SemanticObjectRef{
		{DomainID: domainID, ObjectType: "DIMENSION", ObjectID: "dimension-1", ObjectVersionID: "dimension-version-1"},
		{DomainID: domainID, ObjectType: "METRIC", ObjectID: "metric-1", ObjectVersionID: "metric-version-1"},
	})
	if err != nil {
		panic(err)
	}
	return objects
}
