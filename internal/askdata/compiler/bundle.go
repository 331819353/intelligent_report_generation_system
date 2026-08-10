package compiler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	QueryPlanBundleVersion = "query-plan-bundle-v1"
	MaxBundleMetrics       = 8
	MaxBundlePlans         = 6
	MaxBundleConcurrency   = 4
)

var (
	ErrInvalidQueryPlanBundle = errors.New("INVALID_QUERY_PLAN_BUNDLE")
	ErrBundleNotCertified     = errors.New("BUNDLE_NOT_CERTIFIED")
	ErrBundleLimitExceeded    = errors.New("BUNDLE_LIMIT_EXCEEDED")
)

// BundleSharedContext is immutable across every independently compiled plan.
// The resolved time artifact is retained separately from SemanticIR so the
// normal compiler coverage and replay checks can consume the exact result of
// TIME-001 rather than resolving relative time again.
type BundleSharedContext struct {
	DomainID         askdata.ID          `json:"domainId"`
	ResolvedTimeSpec ir.ResolvedTimeSpec `json:"resolvedTimeSpec"`
	Filters          []ir.Filter         `json:"filters"`
}

// BundleMetricContract is trusted resolver output for one release-pinned
// metric. BuildQueryPlanBundle verifies every referenced version against the
// supplied manifest; the normal compiler still re-resolves and authorizes the
// completed SemanticIR before producing executable query artifacts.
type BundleMetricContract struct {
	MetricVersionID        askdata.ID
	ModelVersionID         askdata.ID
	TimeDimensionVersionID askdata.ID
}

type BundlePlan struct {
	PlanID     askdata.ID          `json:"planId"`
	Role       string              `json:"role"`
	SemanticIR ir.SemanticIR       `json:"semanticIr"`
	IRHash     askdata.ContentHash `json:"irHash"`
	ChartType  string              `json:"chartType"`
}

// QueryPlanBundle is the replay-safe QUERY-009 boundary. It carries only
// semantic IDs and hashes; SQL, parameters and result rows are never present.
type QueryPlanBundle struct {
	SchemaVersion       string              `json:"schemaVersion"`
	BundleID            askdata.ID          `json:"bundleId"`
	KPIBundleVersionID  askdata.ID          `json:"kpiBundleVersionId"`
	SemanticReleaseID   askdata.ID          `json:"semanticReleaseId"`
	SemanticContentHash askdata.ContentHash `json:"semanticContentHash"`
	Scope               askdata.PolicyScope `json:"scope"`
	SharedContext       BundleSharedContext `json:"sharedContext"`
	Plans               []BundlePlan        `json:"plans"`
	MaxConcurrentPlans  int                 `json:"maxConcurrentPlans"`
	BundleHash          askdata.ContentHash `json:"bundleHash"`
}

type BundleBuildRequest struct {
	Scope              askdata.PolicyScope
	Bundle             registry.KPIBundle
	ReleaseManifest    registry.ReleaseManifest
	SharedContext      BundleSharedContext
	MetricContracts    []BundleMetricContract
	MaxConcurrentPlans int
}

// BundlePlanCompileRequest is the production compiler entry point consumed by
// the bundle pipeline. Implementations must run the ordinary resolver and
// Adapt path; the orchestrator verifies the returned artifact against every
// pinned input before validation or execution.
type BundlePlanCompileRequest struct {
	Scope         askdata.PolicyScope `json:"scope"`
	SharedContext BundleSharedContext `json:"sharedContext"`
	Plan          BundlePlan          `json:"plan"`
}

// BuildQueryPlanBundle expands a CERTIFIED KPIBundleVersion into bounded,
// independent SemanticIR plans. The exact release manifest is recomputed and
// matched to PolicyScope before any plan is emitted.
func BuildQueryPlanBundle(request BundleBuildRequest) (QueryPlanBundle, error) {
	if request.Bundle.Status != registry.VersionStatusCertified {
		return QueryPlanBundle{}, ErrBundleNotCertified
	}
	if len(request.Bundle.Items) > MaxBundlePlans || len(request.MetricContracts) > MaxBundleMetrics {
		return QueryPlanBundle{}, ErrBundleLimitExceeded
	}
	if request.MaxConcurrentPlans == 0 {
		request.MaxConcurrentPlans = MaxBundleConcurrency
	}
	if err := validateBundleBuildRequest(request); err != nil {
		return QueryPlanBundle{}, err
	}

	grain, err := bundleTimeGrain(request.SharedContext.ResolvedTimeSpec.Grain)
	if err != nil {
		return QueryPlanBundle{}, err
	}
	sharedTimeRange, comparison := bundleTimeContracts(request.SharedContext, grain)
	contracts := make(map[askdata.ID]BundleMetricContract, len(request.MetricContracts))
	for _, contract := range request.MetricContracts {
		contracts[contract.MetricVersionID] = contract
	}
	items := append([]registry.KPIBundleItem(nil), request.Bundle.Items...)
	sort.Slice(items, func(left, right int) bool { return items[left].Order < items[right].Order })

	plans := make([]BundlePlan, 0, len(items))
	for _, item := range items {
		contract := contracts[askdata.ID(item.MetricVersionID)]
		timeRange := sharedTimeRange
		timeRange.DimensionVersionID = contract.TimeDimensionVersionID
		groups := make([]ir.GroupBy, 0, len(item.GroupByDimensionVersionIDs))
		for _, dimensionID := range item.GroupByDimensionVersionIDs {
			group := ir.GroupBy{DimensionVersionID: askdata.ID(dimensionID)}
			if group.DimensionVersionID == contract.TimeDimensionVersionID {
				itemGrain := grain
				group.Grain = &itemGrain
			}
			groups = append(groups, group)
		}
		limit := ir.DefaultTopN
		if item.Role == registry.KPIBundleRoleHeadline {
			limit = 1
		}
		semanticIR, _, irHash, canonicalErr := ir.Canonicalize(ir.SemanticIR{
			IRVersion: ir.Version, SemanticReleaseID: request.Scope.Release.ReleaseID,
			SemanticContentHash: request.Scope.Release.ContentHash,
			DomainID:            request.SharedContext.DomainID, ModelVersionID: contract.ModelVersionID,
			Metrics: []ir.Metric{{
				MetricVersionID: contract.MetricVersionID,
				Alias:           stableDatasetIdentifier("metric", contract.MetricVersionID),
			}},
			GroupBy: groups, Filters: cloneBundleFilters(request.SharedContext.Filters),
			TimeRange: &timeRange, Comparison: comparison, Sort: []ir.Sort{}, Limit: limit,
			OtherPolicy: ir.OtherNone, TieBreaking: ir.TieIncludeAll,
		})
		if canonicalErr != nil {
			return QueryPlanBundle{}, fmt.Errorf("%w: plan %d semantic IR: %v", ErrInvalidQueryPlanBundle, item.Order, canonicalErr)
		}
		plans = append(plans, BundlePlan{
			PlanID: askdata.ID(fmt.Sprintf("p%d", item.Order)), Role: item.Role,
			SemanticIR: semanticIR, IRHash: irHash, ChartType: item.ChartType,
		})
	}

	normalizedShared := request.SharedContext
	normalizedShared.Filters = cloneBundleFilters(plans[0].SemanticIR.Filters)
	bundle := QueryPlanBundle{
		SchemaVersion: QueryPlanBundleVersion, BundleID: askdata.ID(request.Bundle.Code),
		KPIBundleVersionID:  askdata.ID(request.Bundle.ID),
		SemanticReleaseID:   request.Scope.Release.ReleaseID,
		SemanticContentHash: request.Scope.Release.ContentHash, Scope: request.Scope,
		SharedContext: normalizedShared, Plans: plans,
		MaxConcurrentPlans: request.MaxConcurrentPlans,
	}
	bundle.BundleHash, err = queryPlanBundleHash(bundle)
	if err != nil || bundle.Validate() != nil {
		return QueryPlanBundle{}, fmt.Errorf("%w: final bundle", ErrInvalidQueryPlanBundle)
	}
	return bundle, nil
}

func (bundle QueryPlanBundle) Validate() error {
	if bundle.SchemaVersion != QueryPlanBundleVersion || bundle.BundleID.Validate() != nil ||
		uuid.Validate(string(bundle.KPIBundleVersionID)) != nil || bundle.SemanticReleaseID.Validate() != nil ||
		bundle.SemanticContentHash.Validate() != nil || bundle.Scope.Validate() != nil ||
		bundle.BundleHash.Validate() != nil || bundle.SharedContext.DomainID.Validate() != nil ||
		bundle.SemanticReleaseID != bundle.Scope.Release.ReleaseID ||
		bundle.SemanticContentHash != bundle.Scope.Release.ContentHash ||
		bundle.SharedContext.DomainID == "" || !bundleScopeContainsDomain(bundle.Scope, bundle.SharedContext.DomainID) ||
		len(bundle.Plans) < 1 || len(bundle.Plans) > MaxBundlePlans ||
		bundle.MaxConcurrentPlans < 1 || bundle.MaxConcurrentPlans > MaxBundleConcurrency ||
		validateResolvedTimeSpec(bundle.SharedContext.ResolvedTimeSpec) != nil {
		return ErrInvalidQueryPlanBundle
	}
	grain, err := bundleTimeGrain(bundle.SharedContext.ResolvedTimeSpec.Grain)
	if err != nil {
		return ErrInvalidQueryPlanBundle
	}
	sharedTimeRange, comparison := bundleTimeContracts(bundle.SharedContext, grain)
	seenMetrics := make(map[askdata.ID]struct{}, len(bundle.Plans))
	for index, plan := range bundle.Plans {
		if plan.PlanID != askdata.ID(fmt.Sprintf("p%d", index+1)) || !validBundleRole(plan.Role) ||
			!registry.IsRegisteredComponentType(plan.ChartType) {
			return ErrInvalidQueryPlanBundle
		}
		timeRange := sharedTimeRange
		if plan.SemanticIR.TimeRange != nil {
			timeRange.DimensionVersionID = plan.SemanticIR.TimeRange.DimensionVersionID
		}
		normalized, _, hash, canonicalErr := ir.Canonicalize(plan.SemanticIR)
		if canonicalErr != nil || hash != plan.IRHash || !reflect.DeepEqual(normalized, plan.SemanticIR) ||
			plan.SemanticIR.SemanticReleaseID != bundle.SemanticReleaseID ||
			plan.SemanticIR.SemanticContentHash != bundle.SemanticContentHash ||
			plan.SemanticIR.DomainID != bundle.SharedContext.DomainID ||
			!reflect.DeepEqual(plan.SemanticIR.Filters, bundle.SharedContext.Filters) ||
			plan.SemanticIR.TimeRange == nil || !reflect.DeepEqual(*plan.SemanticIR.TimeRange, timeRange) ||
			!reflect.DeepEqual(plan.SemanticIR.Comparison, comparison) || len(plan.SemanticIR.Metrics) != 1 {
			return ErrInvalidQueryPlanBundle
		}
		metricID := plan.SemanticIR.Metrics[0].MetricVersionID
		seenMetrics[metricID] = struct{}{}
	}
	if len(seenMetrics) > MaxBundleMetrics {
		return ErrInvalidQueryPlanBundle
	}
	expected, hashErr := queryPlanBundleHash(bundle)
	if hashErr != nil || expected != bundle.BundleHash {
		return ErrInvalidQueryPlanBundle
	}
	return nil
}

func validateBundleBuildRequest(request BundleBuildRequest) error {
	if request.Scope.Validate() != nil || request.Bundle.Validate() != nil ||
		request.SharedContext.DomainID.Validate() != nil ||
		request.Bundle.TenantID != string(request.Scope.TenantID) ||
		request.Bundle.DomainID != string(request.SharedContext.DomainID) ||
		!bundleScopeContainsDomain(request.Scope, request.SharedContext.DomainID) ||
		request.MaxConcurrentPlans < 1 || request.MaxConcurrentPlans > MaxBundleConcurrency ||
		validateResolvedTimeSpec(request.SharedContext.ResolvedTimeSpec) != nil {
		return fmt.Errorf("%w: build request", ErrInvalidQueryPlanBundle)
	}
	if _, err := bundleTimeGrain(request.SharedContext.ResolvedTimeSpec.Grain); err != nil {
		return err
	}
	rebuilt, err := registry.BuildReleaseManifest(request.ReleaseManifest.Objects)
	if err != nil || request.ReleaseManifest.ContentHash != rebuilt.ContentHash ||
		rebuilt.ContentHash != request.Scope.Release.ContentHash {
		return fmt.Errorf("%w: release manifest", ErrInvalidQueryPlanBundle)
	}
	expectedBundle, err := registry.KPIBundleReleaseObject(request.Bundle)
	if err != nil || !releaseManifestHasExactObject(rebuilt.Objects, expectedBundle) {
		return fmt.Errorf("%w: certified bundle is absent from pinned release", ErrInvalidQueryPlanBundle)
	}

	metricObjects := releaseObjectsByVersion(rebuilt.Objects, registry.ReleaseObjectMetric)
	modelObjects := releaseObjectsByVersion(rebuilt.Objects, registry.ReleaseObjectSemanticModel)
	dimensionObjects := releaseObjectsByVersion(rebuilt.Objects, registry.ReleaseObjectDimension)
	contracts := make(map[askdata.ID]BundleMetricContract, len(request.MetricContracts))
	for _, contract := range request.MetricContracts {
		if contract.MetricVersionID.Validate() != nil || contract.ModelVersionID.Validate() != nil ||
			contract.TimeDimensionVersionID.Validate() != nil {
			return fmt.Errorf("%w: metric contract IDs", ErrInvalidQueryPlanBundle)
		}
		if _, duplicate := contracts[contract.MetricVersionID]; duplicate {
			return fmt.Errorf("%w: duplicate metric contract", ErrInvalidQueryPlanBundle)
		}
		metricObject, metricFound := metricObjects[string(contract.MetricVersionID)]
		_, modelFound := modelObjects[string(contract.ModelVersionID)]
		_, dimensionFound := dimensionObjects[string(contract.TimeDimensionVersionID)]
		var metricReleaseContract struct {
			SemanticModelVersionID string `json:"semanticModelVersionId"`
		}
		if !metricFound || !modelFound || !dimensionFound {
			return fmt.Errorf("%w: metric dependencies are absent from pinned release", ErrInvalidQueryPlanBundle)
		}
		if decodeErr := json.Unmarshal(metricObject.Contract, &metricReleaseContract); decodeErr != nil ||
			metricReleaseContract.SemanticModelVersionID != string(contract.ModelVersionID) {
			return fmt.Errorf("%w: metric model binding", ErrInvalidQueryPlanBundle)
		}
		contracts[contract.MetricVersionID] = contract
	}
	for _, item := range request.Bundle.Items {
		contract, exists := contracts[askdata.ID(item.MetricVersionID)]
		if !exists {
			return fmt.Errorf("%w: bundle metric contract missing", ErrInvalidQueryPlanBundle)
		}
		for _, dimensionID := range item.GroupByDimensionVersionIDs {
			if _, present := dimensionObjects[dimensionID]; !present {
				return fmt.Errorf("%w: group-by is absent from pinned release", ErrInvalidQueryPlanBundle)
			}
		}
		if _, present := dimensionObjects[string(contract.TimeDimensionVersionID)]; !present {
			return fmt.Errorf("%w: time dimension is absent from pinned release", ErrInvalidQueryPlanBundle)
		}
	}
	uniqueMetrics := make(map[askdata.ID]struct{}, len(request.Bundle.Items))
	for _, item := range request.Bundle.Items {
		uniqueMetrics[askdata.ID(item.MetricVersionID)] = struct{}{}
	}
	if len(contracts) != len(uniqueMetrics) {
		return fmt.Errorf("%w: metric contracts must exactly cover bundle metrics", ErrInvalidQueryPlanBundle)
	}
	for _, filter := range request.SharedContext.Filters {
		if _, present := dimensionObjects[string(filter.DimensionVersionID)]; !present {
			return fmt.Errorf("%w: filter dimension is absent from pinned release", ErrInvalidQueryPlanBundle)
		}
	}
	return nil
}

func bundleTimeGrain(value string) (ir.TimeGrain, error) {
	switch value {
	case string(ir.TimeGrainDay):
		return ir.TimeGrainDay, nil
	case string(ir.TimeGrainWeek):
		return ir.TimeGrainWeek, nil
	case string(ir.TimeGrainMonth):
		return ir.TimeGrainMonth, nil
	case string(ir.TimeGrainQuarter):
		return ir.TimeGrainQuarter, nil
	case string(ir.TimeGrainYear):
		return ir.TimeGrainYear, nil
	default:
		return "", fmt.Errorf("%w: unsupported bundle time grain", ErrInvalidQueryPlanBundle)
	}
}

func bundleTimeContracts(shared BundleSharedContext, grain ir.TimeGrain) (ir.TimeRange, *ir.Comparison) {
	spec := shared.ResolvedTimeSpec
	timeRange := ir.TimeRange{
		Start:        spec.ResolvedStart.Format("2006-01-02"),
		EndExclusive: spec.ResolvedEndExclusive.Format("2006-01-02"),
		Timezone:     spec.Timezone, RequestedPeriod: spec.RequestedPeriod, Grain: grain,
	}
	var comparison *ir.Comparison
	if spec.Comparison != nil {
		comparison = &ir.Comparison{Type: ir.ComparisonType(spec.Comparison.Type), Periods: spec.Comparison.Periods}
	}
	return timeRange, comparison
}

func cloneBundleFilters(values []ir.Filter) []ir.Filter {
	result := make([]ir.Filter, len(values))
	for index, value := range values {
		result[index] = value
		result[index].MemberVersionIDs = append([]askdata.ID(nil), value.MemberVersionIDs...)
	}
	if result == nil {
		return []ir.Filter{}
	}
	return result
}

func bundleScopeContainsDomain(scope askdata.PolicyScope, domainID askdata.ID) bool {
	for _, candidate := range scope.DomainIDs {
		if candidate == domainID {
			return true
		}
	}
	return false
}

func validBundleRole(role string) bool {
	return role == registry.KPIBundleRoleHeadline || role == registry.KPIBundleRoleTrend ||
		role == registry.KPIBundleRoleBreakdown
}

func releaseObjectsByVersion(objects []registry.ReleaseObject, objectType registry.ReleaseObjectType) map[string]registry.ReleaseObject {
	result := make(map[string]registry.ReleaseObject)
	for _, object := range objects {
		if object.Type == objectType {
			result[object.ObjectVersionID] = object
		}
	}
	return result
}

func releaseManifestHasExactObject(objects []registry.ReleaseObject, expected registry.ReleaseObject) bool {
	for _, object := range objects {
		if object.Type == expected.Type && object.ObjectID == expected.ObjectID &&
			object.ObjectVersionID == expected.ObjectVersionID && object.ContentHash == expected.ContentHash &&
			object.Sensitivity == expected.Sensitivity && bytes.Equal(object.Contract, expected.Contract) {
			return true
		}
	}
	return false
}

func queryPlanBundleHash(bundle QueryPlanBundle) (askdata.ContentHash, error) {
	payload := struct {
		SchemaVersion       string              `json:"schemaVersion"`
		BundleID            askdata.ID          `json:"bundleId"`
		KPIBundleVersionID  askdata.ID          `json:"kpiBundleVersionId"`
		SemanticReleaseID   askdata.ID          `json:"semanticReleaseId"`
		SemanticContentHash askdata.ContentHash `json:"semanticContentHash"`
		Scope               askdata.PolicyScope `json:"scope"`
		SharedContext       BundleSharedContext `json:"sharedContext"`
		Plans               []BundlePlan        `json:"plans"`
		MaxConcurrentPlans  int                 `json:"maxConcurrentPlans"`
	}{
		bundle.SchemaVersion, bundle.BundleID, bundle.KPIBundleVersionID,
		bundle.SemanticReleaseID, bundle.SemanticContentHash, bundle.Scope,
		bundle.SharedContext, bundle.Plans, bundle.MaxConcurrentPlans,
	}
	hash, _, err := registry.CanonicalContentHash(payload)
	return hash, err
}
