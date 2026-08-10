package compiler

import (
	"context"
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

const pinnedIRCompilerVersion = "report-semantic-upgrade-compiler-v1"

type PinnedIRCompileRequest struct {
	Scope            askdata.PolicyScope
	SemanticIR       ir.SemanticIR
	ResolvedTimeSpec *ir.ResolvedTimeSpec
}

// PinnedIRCompiler is the deterministic compiler entry used by governed
// report upgrades. It resolves the upgraded IR only from the exact Release
// and current actor scope, then returns the same non-executable QueryArtifact
// contract used by ordinary AskData runs.
type PinnedIRCompiler struct{ store ContractStore }

func NewPinnedIRCompiler(store ContractStore) (*PinnedIRCompiler, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: semantic contract store is required", ErrContractUnavailable)
	}
	return &PinnedIRCompiler{store: store}, nil
}

func (compiler *PinnedIRCompiler) CompilePinnedIR(
	ctx context.Context, request PinnedIRCompileRequest,
) (QueryArtifact, error) {
	if compiler == nil || compiler.store == nil || request.Scope.Validate() != nil || request.SemanticIR.Validate() != nil {
		return QueryArtifact{}, ErrInvalidResolveRequest
	}
	normalized, canonical, irHash, err := ir.Canonicalize(request.SemanticIR)
	if err != nil || normalized.SemanticReleaseID != request.Scope.Release.ReleaseID ||
		normalized.SemanticContentHash != request.Scope.Release.ContentHash ||
		!containsID(request.Scope.DomainIDs, normalized.DomainID) {
		return QueryArtifact{}, ErrInvalidResolveRequest
	}
	if err := validateResolveAccessContext(ctx, request.Scope, normalized.DomainID); err != nil {
		return QueryArtifact{}, err
	}
	lookup, err := pinnedContractLookup(request.Scope, normalized, irHash)
	if err != nil {
		return QueryArtifact{}, err
	}
	snapshot, err := compiler.store.LoadContractSnapshot(ctx, lookup)
	if err != nil {
		return QueryArtifact{}, err
	}
	snapshot, err = normalizeSnapshot(snapshot)
	if err != nil {
		return QueryArtifact{}, fmt.Errorf("%w: normalize pinned snapshot", ErrContractUnavailable)
	}
	if err := validateSnapshot(lookup, nil, snapshot); err != nil {
		return QueryArtifact{}, err
	}
	provenance, _ := json.Marshal(struct {
		Version         string              `json:"version"`
		PolicyScopeHash askdata.ContentHash `json:"policyScopeHash"`
		IRHash          askdata.ContentHash `json:"irHash"`
		SemanticIR      json.RawMessage     `json:"semanticIr"`
	}{pinnedIRCompilerVersion, request.Scope.PolicyHash, irHash, canonical})
	buildHash := askdata.HashBytes(append([]byte("report-semantic-upgrade-build-v1\x00"), provenance...))
	graphHash := askdata.HashBytes([]byte(
		"report-semantic-upgrade-single-model-v1\x00" + string(normalized.ModelVersionID) + "\x00" + string(irHash),
	))
	resolution, err := finalizeResolution(Resolution{
		Version: ResolutionVersion, Scope: request.Scope, DomainID: normalized.DomainID,
		IRHash: irHash, BuildArtifactHash: buildHash, GraphPlanHash: graphHash,
		TimeDimensionVersionID: cloneID(lookup.TimeDimensionVersionID),
		MemberBindings:         append([]MemberBinding(nil), lookup.MemberBindings...),
		Model:                  snapshot.Model,
		Metrics:                snapshot.Metrics,
		Dimensions:             snapshot.Dimensions,
		Members:                snapshot.Members,
		Relationships:          []RelationshipContract{},
		memberParameterValues:  cloneMemberParameterValues(snapshot.memberParameterValues),
	})
	if err != nil {
		return QueryArtifact{}, err
	}
	return compileResolvedArtifact(normalized, resolution, request.ResolvedTimeSpec)
}
