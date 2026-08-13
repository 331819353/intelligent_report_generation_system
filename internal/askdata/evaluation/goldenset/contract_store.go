package goldenset

import (
	"context"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
)

// syntheticContractStore is a real compiler.ContractStore, not a shortcut past
// one. Resolve() runs validateSnapshot against whatever it returns, so a
// contract that production would refuse — a non-canonical formula AST, a
// dimension whose logical field is the wrong role, an additivity declaration
// that contradicts its aggregation — fails here exactly as it would in a query.
// Handing the compiler a hand-built Resolution instead would skip all of that
// and leave the suite asserting on a plan no resolver ever produced.
type syntheticContractStore struct {
	metrics    []compiler.MetricContract
	dimensions []compiler.DimensionContract
	model      compiler.ModelContract
}

func newSyntheticContractStore(
	metrics []compiler.MetricContract,
	dimensionIDs []askdata.ID,
) (*syntheticContractStore, error) {
	model, err := goldenModel()
	if err != nil {
		return nil, err
	}
	catalog := goldenDimensions()
	dimensions := make([]compiler.DimensionContract, 0, len(dimensionIDs))
	for _, id := range dimensionIDs {
		dimension, known := catalog[id]
		if !known {
			return nil, fmt.Errorf("%w: dimension %s is not in the synthetic model", ErrAdditivityGoldenSet, id)
		}
		dimensions = append(dimensions, dimension)
	}
	return &syntheticContractStore{metrics: metrics, dimensions: dimensions, model: model}, nil
}

func (store *syntheticContractStore) LoadContractSnapshot(
	ctx context.Context,
	lookup compiler.ContractLookup,
) (compiler.ContractSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return compiler.ContractSnapshot{}, err
	}
	// The lookup is derived from the IR by the resolver. Returning objects it did
	// not ask for is the failure mode validateSnapshot exists to catch, so the
	// store selects strictly by the requested identities.
	metrics, err := selectByID(store.metrics, lookup.MetricVersionIDs, func(
		metric compiler.MetricContract,
	) askdata.ID {
		return metric.MetricVersionID
	})
	if err != nil {
		return compiler.ContractSnapshot{}, fmt.Errorf("metric: %w", err)
	}
	dimensions, err := selectByID(store.dimensions, lookup.DimensionVersionIDs, func(
		dimension compiler.DimensionContract,
	) askdata.ID {
		return dimension.DimensionVersionID
	})
	if err != nil {
		return compiler.ContractSnapshot{}, fmt.Errorf("dimension: %w", err)
	}
	return compiler.ContractSnapshot{
		Release: lookup.Scope.Release, ReleaseStatus: "ACTIVE",
		ReleaseObjectCount: len(metrics) + len(dimensions) + 1,
		Model:              store.model, Metrics: metrics, Dimensions: dimensions,
		Members: []compiler.MemberContract{}, Relationships: []compiler.RelationshipContract{},
	}, nil
}

func selectByID[T any](available []T, requested []askdata.ID, identify func(T) askdata.ID) ([]T, error) {
	byID := make(map[askdata.ID]T, len(available))
	for _, value := range available {
		byID[identify(value)] = value
	}
	selected := make([]T, 0, len(requested))
	for _, id := range requested {
		value, known := byID[id]
		if !known {
			return nil, fmt.Errorf("%w: %s is not in the release", ErrAdditivityGoldenSet, id)
		}
		selected = append(selected, value)
	}
	return selected, nil
}
