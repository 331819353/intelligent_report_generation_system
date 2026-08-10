package security

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/policy"
)

const AuthorizationReceiptVersion = "askdata-authorization-receipt-v1"

var (
	ErrInvalidAuthorization     = errors.New("AskData authorization request is invalid")
	ErrAuthorizationDenied      = errors.New("AskData authorization is denied")
	ErrAuthorizationUnavailable = errors.New("AskData authorization is unavailable")
)

type AuthorizationStage string

const (
	AuthorizationBeforeRecall    AuthorizationStage = "BEFORE_RECALL"
	AuthorizationBeforeBinding   AuthorizationStage = "BEFORE_BINDING"
	AuthorizationBeforeExecution AuthorizationStage = "BEFORE_EXECUTION"
)

// SemanticAccessResolver is intentionally narrow: implementations receive
// only a release-pinned policy scope and label-free object references.
type SemanticAccessResolver interface {
	ResolveSemanticAccess(context.Context, policy.SemanticAccessRequest) (policy.SemanticAccessSnapshot, error)
}

type Authorizer struct{ resolver SemanticAccessResolver }

func NewAuthorizer(resolver SemanticAccessResolver) (*Authorizer, error) {
	if resolver == nil {
		return nil, ErrInvalidAuthorization
	}
	return &Authorizer{resolver: resolver}, nil
}

// AuthorizationReceipt is safe for cache/audit boundaries. It contains no
// object labels, definitions, aliases, physical identifiers, SQL or values.
type AuthorizationReceipt struct {
	Version            string                     `json:"version"`
	Stage              AuthorizationStage         `json:"stage"`
	ScopeHash          askdata.ContentHash        `json:"scopeHash"`
	Release            askdata.ReleaseRef         `json:"release"`
	DomainID           askdata.ID                 `json:"domainId"`
	SubjectHash        askdata.ContentHash        `json:"subjectHash"`
	AuthorizedObjects  []policy.SemanticObjectRef `json:"authorizedObjects"`
	PolicySnapshotHash askdata.ContentHash        `json:"policySnapshotHash"`
	AuditHash          askdata.ContentHash        `json:"auditHash"`
}

// AuthorizeRecall must run before any name/alias/definition-bearing index is
// queried. Its returned object set is the only eligible retrieval catalogue.
func (authorizer *Authorizer) AuthorizeRecall(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	retrievalRequestHash askdata.ContentHash,
) (AuthorizationReceipt, error) {
	return authorizer.authorize(
		ctx, AuthorizationBeforeRecall, scope, domainID, retrievalRequestHash, nil,
	)
}

// AuthorizeBinding revalidates every candidate object immediately before a
// bundle is bound. Partial authorization is denied instead of pruning after
// labels may already have reached the binder.
func (authorizer *Authorizer) AuthorizeBinding(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	candidateSetHash askdata.ContentHash,
	objects []policy.SemanticObjectRef,
) (AuthorizationReceipt, error) {
	return authorizer.authorize(
		ctx, AuthorizationBeforeBinding, scope, domainID, candidateSetHash, objects,
	)
}

// AuthorizeExecution performs a fresh database-policy lookup immediately
// before the warehouse runner is entered. A recall/binding receipt cannot be
// promoted to this stage.
func (authorizer *Authorizer) AuthorizeExecution(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	queryPlanHash askdata.ContentHash,
	objects []policy.SemanticObjectRef,
) (AuthorizationReceipt, error) {
	return authorizer.authorize(
		ctx, AuthorizationBeforeExecution, scope, domainID, queryPlanHash, objects,
	)
}

func (authorizer *Authorizer) authorize(
	ctx context.Context,
	stage AuthorizationStage,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	subjectHash askdata.ContentHash,
	objects []policy.SemanticObjectRef,
) (AuthorizationReceipt, error) {
	if authorizer == nil || authorizer.resolver == nil || ctx == nil ||
		scope.Validate() != nil || domainID.Validate() != nil ||
		!containsAuthorizationID(scope.DomainIDs, domainID) || subjectHash.Validate() != nil {
		return AuthorizationReceipt{}, ErrInvalidAuthorization
	}
	canonical, err := policy.CanonicalSemanticObjectRefs(objects)
	if err != nil || (stage != AuthorizationBeforeRecall && len(canonical) == 0) ||
		(stage == AuthorizationBeforeRecall && len(canonical) != 0) {
		return AuthorizationReceipt{}, ErrInvalidAuthorization
	}
	request := policy.SemanticAccessRequest{
		Scope: scope, DomainID: domainID, Projection: projectionForStage(stage), Objects: canonical,
	}
	if request.Validate() != nil {
		return AuthorizationReceipt{}, ErrInvalidAuthorization
	}
	snapshot, err := authorizer.resolver.ResolveSemanticAccess(ctx, request)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return AuthorizationReceipt{}, contextError
		}
		if errors.Is(err, policy.ErrSemanticAccessDenied) {
			return AuthorizationReceipt{}, ErrAuthorizationDenied
		}
		return AuthorizationReceipt{}, ErrAuthorizationUnavailable
	}
	if snapshot.ValidateAgainst(request) != nil {
		return AuthorizationReceipt{}, ErrAuthorizationUnavailable
	}
	if stage != AuthorizationBeforeRecall && !equalAuthorizationRefs(canonical, snapshot.Objects) {
		return AuthorizationReceipt{}, ErrAuthorizationDenied
	}
	receipt := AuthorizationReceipt{
		Version: AuthorizationReceiptVersion, Stage: stage, ScopeHash: scope.PolicyHash,
		Release: scope.Release, DomainID: domainID, SubjectHash: subjectHash,
		AuthorizedObjects:  append([]policy.SemanticObjectRef(nil), snapshot.Objects...),
		PolicySnapshotHash: snapshot.SnapshotHash,
	}
	receipt.AuditHash, err = authorizationReceiptHash(receipt)
	if err != nil || receipt.Validate() != nil {
		return AuthorizationReceipt{}, ErrAuthorizationUnavailable
	}
	return receipt, nil
}

func (receipt AuthorizationReceipt) Validate() error {
	if receipt.Version != AuthorizationReceiptVersion || !validAuthorizationStage(receipt.Stage) ||
		receipt.ScopeHash.Validate() != nil || receipt.Release.Validate() != nil ||
		receipt.DomainID.Validate() != nil || receipt.SubjectHash.Validate() != nil ||
		receipt.PolicySnapshotHash.Validate() != nil || receipt.AuditHash.Validate() != nil ||
		!authorizationRefsAreCanonical(receipt.AuthorizedObjects) ||
		(receipt.Stage != AuthorizationBeforeRecall && len(receipt.AuthorizedObjects) == 0) {
		return ErrInvalidAuthorization
	}
	for _, ref := range receipt.AuthorizedObjects {
		if ref.Validate() != nil || ref.DomainID != receipt.DomainID {
			return ErrInvalidAuthorization
		}
	}
	snapshot := policy.SemanticAccessSnapshot{
		ScopeHash: receipt.ScopeHash, Release: receipt.Release, DomainID: receipt.DomainID,
		Projection:   projectionForStage(receipt.Stage),
		Objects:      append([]policy.SemanticObjectRef(nil), receipt.AuthorizedObjects...),
		SnapshotHash: receipt.PolicySnapshotHash,
	}
	if snapshot.Validate() != nil {
		return ErrInvalidAuthorization
	}
	expected, err := authorizationReceiptHash(receipt)
	if err != nil || expected != receipt.AuditHash {
		return ErrInvalidAuthorization
	}
	return nil
}

func (receipt AuthorizationReceipt) Allows(ref policy.SemanticObjectRef) bool {
	if receipt.Validate() != nil || ref.Validate() != nil || ref.DomainID != receipt.DomainID {
		return false
	}
	index := sort.Search(len(receipt.AuthorizedObjects), func(index int) bool {
		return !authorizationRefLess(receipt.AuthorizedObjects[index], ref)
	})
	return index < len(receipt.AuthorizedObjects) && receipt.AuthorizedObjects[index] == ref
}

func authorizationReceiptHash(receipt AuthorizationReceipt) (askdata.ContentHash, error) {
	copy := receipt
	copy.AuditHash = ""
	payload, err := json.Marshal(copy)
	if err != nil {
		return "", err
	}
	return askdata.HashBytes(payload), nil
}

func projectionForStage(stage AuthorizationStage) policy.SemanticProjection {
	switch stage {
	case AuthorizationBeforeRecall:
		return policy.SemanticProjectionSearch
	case AuthorizationBeforeBinding:
		return policy.SemanticProjectionRegistry
	case AuthorizationBeforeExecution:
		return policy.SemanticProjectionExecution
	default:
		return ""
	}
}

func validAuthorizationStage(stage AuthorizationStage) bool {
	return projectionForStage(stage) != ""
}

func equalAuthorizationRefs(left, right []policy.SemanticObjectRef) bool {
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

func authorizationRefsAreCanonical(values []policy.SemanticObjectRef) bool {
	for index := range values {
		if index > 0 && !authorizationRefLess(values[index-1], values[index]) {
			return false
		}
	}
	return true
}

func authorizationRefLess(left, right policy.SemanticObjectRef) bool {
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

func containsAuthorizationID(values []askdata.ID, want askdata.ID) bool {
	index := sort.Search(len(values), func(index int) bool { return values[index] >= want })
	return index < len(values) && values[index] == want
}
