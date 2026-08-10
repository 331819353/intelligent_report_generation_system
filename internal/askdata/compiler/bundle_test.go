package compiler

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

func TestBuildQueryPlanBundleExpandsCertifiedBundle(t *testing.T) {
	request := bundleBuildFixture(t, 3)
	request.Bundle.Items[0], request.Bundle.Items[2] = request.Bundle.Items[2], request.Bundle.Items[0]
	bundle, err := BuildQueryPlanBundle(request)
	if err != nil {
		t.Fatalf("BuildQueryPlanBundle() error = %v", err)
	}
	if bundle.Validate() != nil || bundle.SchemaVersion != QueryPlanBundleVersion ||
		bundle.BundleID != "kpi_monthly_overview" || len(bundle.Plans) != 3 ||
		bundle.MaxConcurrentPlans != MaxBundleConcurrency {
		t.Fatalf("bundle = %#v", bundle)
	}
	wantRoles := []string{
		registry.KPIBundleRoleHeadline,
		registry.KPIBundleRoleTrend,
		registry.KPIBundleRoleBreakdown,
	}
	for index, plan := range bundle.Plans {
		if plan.PlanID != askdata.ID(fmt.Sprintf("p%d", index+1)) || plan.Role != wantRoles[index] ||
			plan.SemanticIR.SemanticReleaseID != bundle.Scope.Release.ReleaseID ||
			plan.SemanticIR.SemanticContentHash != bundle.Scope.Release.ContentHash ||
			plan.SemanticIR.DomainID != bundle.SharedContext.DomainID || plan.IRHash.Validate() != nil ||
			!reflect.DeepEqual(plan.SemanticIR.Filters, bundle.SharedContext.Filters) {
			t.Fatalf("plans[%d] = %#v", index, plan)
		}
	}
	if bundle.Plans[0].SemanticIR.Limit != 1 || bundle.Plans[1].SemanticIR.Limit != ir.DefaultTopN ||
		bundle.Plans[1].SemanticIR.GroupBy[0].Grain == nil ||
		*bundle.Plans[1].SemanticIR.GroupBy[0].Grain != ir.TimeGrainMonth {
		t.Fatalf("role-specific plans = %#v", bundle.Plans)
	}

	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var replay QueryPlanBundle
	if err := askdata.DecodeStrictJSON(raw, &replay); err != nil || replay.Validate() != nil {
		t.Fatalf("replayed bundle rejected: decode=%v validate=%v", err, replay.Validate())
	}
	tampered := replay
	tampered.Plans[0].ChartType = "line-trend"
	if !errors.Is(tampered.Validate(), ErrInvalidQueryPlanBundle) {
		t.Fatal("tampered bundle hash was accepted")
	}
}

func TestBuildQueryPlanBundleRejectsDraftAndLimits(t *testing.T) {
	draft := bundleBuildFixture(t, 3)
	draft.Bundle.Status = registry.VersionStatusDraft
	if _, err := BuildQueryPlanBundle(draft); !errors.Is(err, ErrBundleNotCertified) {
		t.Fatalf("draft error = %v", err)
	}

	tooManyPlans := bundleBuildFixture(t, 7)
	if _, err := BuildQueryPlanBundle(tooManyPlans); !errors.Is(err, ErrBundleLimitExceeded) {
		t.Fatalf("seven-plan error = %v", err)
	}

	tooManyMetrics := bundleBuildFixture(t, 3)
	for len(tooManyMetrics.MetricContracts) <= MaxBundleMetrics {
		tooManyMetrics.MetricContracts = append(tooManyMetrics.MetricContracts, BundleMetricContract{
			MetricVersionID:        bundleTestID(fmt.Sprintf("extra-metric-%d", len(tooManyMetrics.MetricContracts))),
			ModelVersionID:         bundleTestID(fmt.Sprintf("extra-model-%d", len(tooManyMetrics.MetricContracts))),
			TimeDimensionVersionID: bundleTestID(fmt.Sprintf("extra-time-%d", len(tooManyMetrics.MetricContracts))),
		})
	}
	if _, err := BuildQueryPlanBundle(tooManyMetrics); !errors.Is(err, ErrBundleLimitExceeded) {
		t.Fatalf("nine-metric error = %v", err)
	}
}

func TestBuildQueryPlanBundleRejectsReleaseAndScopeDrift(t *testing.T) {
	request := bundleBuildFixture(t, 3)
	request.ReleaseManifest.ContentHash = askdata.HashBytes([]byte("other release"))
	if _, err := BuildQueryPlanBundle(request); !errors.Is(err, ErrInvalidQueryPlanBundle) {
		t.Fatalf("release drift error = %v", err)
	}

	request = bundleBuildFixture(t, 3)
	request.MetricContracts[0].ModelVersionID = request.MetricContracts[1].ModelVersionID
	if _, err := BuildQueryPlanBundle(request); !errors.Is(err, ErrInvalidQueryPlanBundle) {
		t.Fatalf("model drift error = %v", err)
	}
}

func TestQueryPlanBundleSchemaFreezesGovernedLimits(t *testing.T) {
	raw, err := os.ReadFile("../../../api/schemas/query-plan-bundle-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	plans := properties["plans"].(map[string]any)
	concurrency := properties["maxConcurrentPlans"].(map[string]any)
	if plans["maxItems"] != float64(MaxBundlePlans) ||
		concurrency["maximum"] != float64(MaxBundleConcurrency) {
		t.Fatalf("schema limits = plans:%v concurrency:%v", plans["maxItems"], concurrency["maximum"])
	}
}

func bundleBuildFixture(t *testing.T, planCount int) BundleBuildRequest {
	t.Helper()
	tenantID := bundleTestID("tenant")
	domainID := bundleTestID("domain")
	actorID := bundleTestID("actor")
	ownerID := bundleTestID("owner")
	bundleVersionID := bundleTestID("bundle-version")
	bundleObjectID := bundleTestID("bundle-object")
	timeDimensionID := bundleTestID("time-dimension-version")
	regionDimensionID := bundleTestID("region-dimension-version")
	timeContractID := bundleTestID("time-contract-version")

	roles := []string{
		registry.KPIBundleRoleHeadline,
		registry.KPIBundleRoleTrend,
		registry.KPIBundleRoleBreakdown,
	}
	chartTypes := []string{"metric-card", "line-trend", "bar-horizontal"}
	items := make([]registry.KPIBundleItem, planCount)
	metricContracts := make([]BundleMetricContract, planCount)
	objects := make([]registry.ReleaseObject, 0, planCount*2+6)
	modelsAdded := map[string]bool{}
	for index := 0; index < planCount; index++ {
		metricID := bundleTestID(fmt.Sprintf("metric-version-%d", index))
		metricObjectID := bundleTestID(fmt.Sprintf("metric-object-%d", index))
		modelID := bundleTestID(fmt.Sprintf("model-version-%d", index))
		modelObjectID := bundleTestID(fmt.Sprintf("model-object-%d", index))
		role := roles[index%len(roles)]
		groupBy := []string{}
		if role == registry.KPIBundleRoleTrend {
			groupBy = []string{string(timeDimensionID)}
		} else if role == registry.KPIBundleRoleBreakdown {
			groupBy = []string{string(regionDimensionID)}
		}
		items[index] = registry.KPIBundleItem{
			MetricVersionID: string(metricID), Role: role, GroupByDimensionVersionIDs: groupBy,
			ChartType: chartTypes[index%len(chartTypes)], Order: index + 1,
		}
		metricContracts[index] = BundleMetricContract{
			MetricVersionID: metricID, ModelVersionID: modelID,
			TimeDimensionVersionID: timeDimensionID,
		}
		objects = append(objects, bundleReleaseObject(t, registry.ReleaseObjectMetric,
			string(metricObjectID), string(metricID), map[string]any{
				"type": "METRIC", "metricId": string(metricObjectID), "versionNo": 1,
				"semanticModelVersionId": string(modelID), "unit": "COUNT",
				"additivity": string(registry.FullyAdditive),
			}))
		if !modelsAdded[string(modelID)] {
			objects = append(objects, bundleReleaseObject(t, registry.ReleaseObjectSemanticModel,
				string(modelObjectID), string(modelID), map[string]any{
					"type": "SEMANTIC_MODEL", "timeContractVersionId": string(timeContractID),
				}))
			modelsAdded[string(modelID)] = true
		}
	}
	bundle := registry.KPIBundle{
		VersionIdentity: registry.VersionIdentity{
			ID: string(bundleVersionID), TenantID: string(tenantID), DomainID: string(domainID),
			ObjectID: string(bundleObjectID), VersionNo: 1, Status: registry.VersionStatusCertified,
			OwnerID: string(ownerID),
		},
		Code: "kpi_monthly_overview", Name: "月度 KPI 总览", Items: items,
		DefaultDimensionVersionIDs: []string{string(regionDimensionID)},
		DefaultTimeExpression:      "CURRENT_MONTH", RoleMapping: json.RawMessage(`{}`),
	}
	bundle.ContentHash = registry.KPIBundleContentHash(bundle)
	bundleObject, err := registry.KPIBundleReleaseObject(bundle)
	if err != nil {
		t.Fatalf("KPIBundleReleaseObject() error = %v", err)
	}
	objects = append(objects, bundleObject)
	objects = append(objects,
		bundleReleaseObject(t, registry.ReleaseObjectDimension, string(bundleTestID("time-dimension-object")),
			string(timeDimensionID), map[string]any{"type": "DIMENSION"}),
		bundleReleaseObject(t, registry.ReleaseObjectDimension, string(bundleTestID("region-dimension-object")),
			string(regionDimensionID), map[string]any{"type": "DIMENSION"}),
		bundleReleaseObject(t, registry.ReleaseObjectTimeContract, string(bundleTestID("time-contract-object")),
			string(timeContractID), map[string]any{"type": "TIME_CONTRACT"}),
	)
	manifest, err := registry.BuildReleaseManifest(objects)
	if err != nil {
		t.Fatalf("BuildReleaseManifest() error = %v", err)
	}
	releaseID := bundleTestID("release")
	scope, err := askdata.NewPolicyScope(tenantID, actorID, []askdata.ID{domainID},
		[]askdata.ID{bundleTestID("role")}, askdata.ReleaseRef{
			ReleaseID: releaseID, ContentHash: manifest.ContentHash,
		})
	if err != nil {
		t.Fatalf("NewPolicyScope() error = %v", err)
	}
	return BundleBuildRequest{
		Scope: scope, Bundle: bundle, ReleaseManifest: manifest,
		SharedContext: BundleSharedContext{
			DomainID: domainID,
			ResolvedTimeSpec: ir.ResolvedTimeSpec{
				RequestedPeriod: "CURRENT_MONTH", Grain: "MONTH",
				PolicyApplied:        string(registry.IncompletePeriodFull),
				PolicySource:         string(registry.PolicySourceTimeContract),
				ResolvedStart:        time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				ResolvedEndExclusive: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
				DataAvailableThrough: time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
				Timezone:             "UTC",
			},
			Filters: []ir.Filter{{
				DimensionVersionID: regionDimensionID, Operator: ir.FilterIsNotNull,
				MemberVersionIDs: []askdata.ID{},
			}},
		},
		MetricContracts: metricContracts,
	}
}

func bundleReleaseObject(
	t *testing.T,
	objectType registry.ReleaseObjectType,
	objectID string,
	versionID string,
	contract any,
) registry.ReleaseObject {
	t.Helper()
	hash, _, err := registry.CanonicalContentHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	object, err := registry.NewReleaseObject(
		objectType, objectID, versionID, registry.SensitivityInternal, contract, hash,
	)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func bundleTestID(label string) askdata.ID {
	return askdata.ID(uuid.NewSHA1(uuid.NameSpaceURL, []byte("query-plan-bundle-test/"+label)).String())
}
