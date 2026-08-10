package compiler

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
)

// PinnedArtifactRehydrator turns a persisted, non-executable QueryArtifact
// back into a live plan under the current viewer scope. The persisted plan is
// used only as an immutable structural proof: member values are resolved again
// from the exact Release and executable SQL/args are deterministically rebuilt
// in process.
type PinnedArtifactRehydrator struct{ store ContractStore }

func NewPinnedArtifactRehydrator(store ContractStore) (*PinnedArtifactRehydrator, error) {
	if store == nil {
		return nil, errors.New("semantic contract store is required")
	}
	return &PinnedArtifactRehydrator{store: store}, nil
}

func (rehydrator *PinnedArtifactRehydrator) Rehydrate(
	ctx context.Context,
	scope askdata.PolicyScope,
	semanticIR ir.SemanticIR,
	fixedPlanHash askdata.ContentHash,
	persisted QueryArtifact,
) (QueryArtifact, error) {
	if rehydrator == nil || rehydrator.store == nil || scope.Validate() != nil ||
		semanticIR.Validate() != nil || fixedPlanHash.Validate() != nil ||
		persisted.Validate() != nil || persisted.PlanHash != fixedPlanHash {
		return QueryArtifact{}, fmt.Errorf("%w: persisted artifact contract", ErrInvalidQueryPlan)
	}
	normalizedIR, _, irHash, err := ir.Canonicalize(semanticIR)
	if err != nil || normalizedIR.SemanticReleaseID != scope.Release.ReleaseID ||
		normalizedIR.SemanticContentHash != scope.Release.ContentHash ||
		normalizedIR.DomainID != persisted.DomainID || persisted.IRHash != irHash ||
		persisted.Scope.TenantID != scope.TenantID || persisted.Scope.Release != scope.Release {
		return QueryArtifact{}, fmt.Errorf("%w: pinned semantic identity", ErrInvalidQueryPlan)
	}
	if err := validateResolveAccessContext(ctx, scope, normalizedIR.DomainID); err != nil {
		return QueryArtifact{}, err
	}
	lookup, err := pinnedContractLookup(scope, normalizedIR, irHash)
	if err != nil {
		return QueryArtifact{}, err
	}
	snapshot, err := rehydrator.store.LoadContractSnapshot(ctx, lookup)
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
	resolution, err := finalizeResolution(Resolution{
		Version: ResolutionVersion, Scope: scope, DomainID: normalizedIR.DomainID,
		IRHash: irHash, BuildArtifactHash: persisted.BuildArtifactHash,
		GraphPlanHash:          persisted.GraphPlanHash,
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
	live, err := compileResolvedArtifact(normalizedIR, resolution, persisted.ResolvedTimeSpec)
	if err != nil {
		return QueryArtifact{}, err
	}
	if mismatch := pinnedArtifactStructureMismatch(persisted, live); mismatch != "" {
		return QueryArtifact{}, fmt.Errorf("%w: persisted plan shape drift (%s)", ErrInvalidQueryPlan, mismatch)
	}
	return live, nil
}

func pinnedContractLookup(
	scope askdata.PolicyScope,
	semanticIR ir.SemanticIR,
	irHash askdata.ContentHash,
) (ContractLookup, error) {
	lookup := ContractLookup{
		Scope: scope, DomainID: semanticIR.DomainID, IRHash: irHash,
		ModelVersionID: semanticIR.ModelVersionID,
	}
	for _, metric := range semanticIR.Metrics {
		lookup.MetricVersionIDs = append(lookup.MetricVersionIDs, metric.MetricVersionID)
	}
	for _, group := range semanticIR.GroupBy {
		lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, group.DimensionVersionID)
	}
	for _, filter := range semanticIR.Filters {
		lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, filter.DimensionVersionID)
		lookup.MemberVersionIDs = append(lookup.MemberVersionIDs, filter.MemberVersionIDs...)
		for _, memberID := range filter.MemberVersionIDs {
			lookup.MemberBindings = append(lookup.MemberBindings, MemberBinding{
				DimensionVersionID: filter.DimensionVersionID, MemberVersionID: memberID,
			})
		}
	}
	if semanticIR.TimeRange != nil {
		lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, semanticIR.TimeRange.DimensionVersionID)
		value := semanticIR.TimeRange.DimensionVersionID
		lookup.TimeDimensionVersionID = &value
	}
	for _, sortValue := range semanticIR.Sort {
		if sortValue.TargetType == ir.SortTargetDimension {
			lookup.DimensionVersionIDs = append(lookup.DimensionVersionIDs, sortValue.TargetVersionID)
		}
	}
	lookup.MetricVersionIDs = normalizeIDs(lookup.MetricVersionIDs)
	lookup.DimensionVersionIDs = normalizeIDs(lookup.DimensionVersionIDs)
	lookup.MemberVersionIDs = normalizeIDs(lookup.MemberVersionIDs)
	lookup.MemberBindings = normalizeMemberBindings(lookup.MemberBindings)
	if err := lookup.Validate(); err != nil {
		return ContractLookup{}, err
	}
	return lookup, nil
}

func pinnedArtifactStructureMismatch(persisted, live QueryArtifact) string {
	if persisted.Version != live.Version || persisted.DomainID != live.DomainID ||
		persisted.IRHash != live.IRHash || persisted.BuildArtifactHash != live.BuildArtifactHash ||
		persisted.GraphPlanHash != live.GraphPlanHash || persisted.Timezone != live.Timezone {
		return "identity"
	}
	if !reflect.DeepEqual(persisted.Comparison, live.Comparison) {
		return "comparison"
	}
	if !reflect.DeepEqual(persisted.ResolvedTimeSpec, live.ResolvedTimeSpec) {
		return "resolved-time"
	}
	if !reflect.DeepEqual(persisted.MetricAggregations, live.MetricAggregations) {
		return "aggregation"
	}
	if len(persisted.Plans) != len(live.Plans) {
		return "plan-count"
	}
	for index := range persisted.Plans {
		left, right := persisted.Plans[index], live.Plans[index]
		left.compiled, right.compiled = nil, nil
		left.parameterValues, right.parameterValues = nil, nil
		if !reflect.DeepEqual(left, right) {
			switch {
			case left.Role != right.Role:
				return fmt.Sprintf("plan-%d-role", index)
			case !reflect.DeepEqual(left.Source, right.Source):
				return fmt.Sprintf("plan-%d-source", index)
			case !reflect.DeepEqual(left.ParameterShapes, right.ParameterShapes):
				return fmt.Sprintf("plan-%d-parameters", index)
			case left.DSLHash != right.DSLHash:
				return fmt.Sprintf("plan-%d-dsl", index)
			case left.LogicalPlanHash != right.LogicalPlanHash:
				return fmt.Sprintf("plan-%d-logical", index)
			case left.CompiledPlanHash != right.CompiledPlanHash:
				return fmt.Sprintf("plan-%d-compiled", index)
			case left.PlanHash != right.PlanHash:
				return fmt.Sprintf("plan-%d-hash", index)
			default:
				// JSON replay may normalize nil and empty slices differently.
				// Exact DSL/logical/compiled/plan hashes above are the canonical
				// structural proof, so representation-only differences are safe.
				continue
			}
		}
	}
	return ""
}
