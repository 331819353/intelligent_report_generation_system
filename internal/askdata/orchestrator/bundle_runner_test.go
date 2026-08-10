package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/config"
)

func TestBundleRunnerCompletesThreePlans(t *testing.T) {
	bundle := runnerBundleFixture(t, 3)
	processor := &recordingBundleProcessor{delays: map[askdata.ID]time.Duration{}}
	runner, err := NewBundleRunner(processor, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), BundleRunRequest{
		RunID: uuid.NewString(), Bundle: bundle,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Validate() != nil || result.Outcome != BundleOutcomeAnswered ||
		result.SucceededPlans != 3 || result.FailedPlans != 0 || len(processor.calls) != 3 {
		t.Fatalf("result = %#v, calls = %#v", result, processor.calls)
	}
	for index, plan := range result.Plans {
		if plan.PlanID != askdata.ID(fmt.Sprintf("p%d", index+1)) || plan.Status != BundlePlanSucceeded {
			t.Fatalf("plans[%d] = %#v", index, plan)
		}
	}
}

func TestBundleRunnerAggregatesFailureAndPermissionClippingAsPartial(t *testing.T) {
	bundle := runnerBundleFixture(t, 3)
	processor := &recordingBundleProcessor{failures: map[askdata.ID]error{
		"p2": &BundlePlanFailure{Code: BundleFailureExecute, Err: errors.New("warehouse failed")},
		"p3": &BundlePlanFailure{Code: BundleFailureUnauthorized, Err: errors.New("restricted")},
	}}
	runner, _ := NewBundleRunner(processor, nil)
	result, err := runner.Run(context.Background(), BundleRunRequest{
		RunID: uuid.NewString(), Bundle: bundle,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outcome != BundleOutcomePartial || result.SucceededPlans != 1 || result.FailedPlans != 2 ||
		result.Plans[0].Status != BundlePlanSucceeded ||
		result.Plans[1].FailureCode != BundleFailureExecute ||
		result.Plans[2].FailureCode != BundleFailureUnauthorized {
		t.Fatalf("partial result = %#v", result)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || containsAny(string(raw), "warehouse failed", "restricted") {
		t.Fatalf("result leaked private error: %s", raw)
	}
}

func TestBundleRunnerLimitsConcurrencyToFour(t *testing.T) {
	bundle := runnerBundleFixture(t, 6)
	processor := &recordingBundleProcessor{defaultDelay: 30 * time.Millisecond}
	runner, _ := NewBundleRunner(processor, nil)
	result, err := runner.Run(context.Background(), BundleRunRequest{
		RunID: uuid.NewString(), Bundle: bundle,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Outcome != BundleOutcomeAnswered || processor.maxActive != compiler.MaxBundleConcurrency {
		t.Fatalf("outcome = %s, max concurrency = %d", result.Outcome, processor.maxActive)
	}
}

func TestBundleRunnerHardTimeoutPreservesCompletedPlansAsPartial(t *testing.T) {
	bundle := runnerBundleFixture(t, 3)
	budgetConfig := config.AskDataBudgetOverride{
		DomainID: string(bundle.SharedContext.DomainID), BudgetClass: string(BudgetClassBundle),
		MaxLLMCalls: 2, MaxToolCalls: 10, MaxPrimaryQueries: 6,
		MaxValidationQueries: 2, MaxCandidateCompares: 2, MaxJoinHops: 4,
		HardTimeout: 100 * time.Millisecond, P95Target: 50 * time.Millisecond,
		MaxConcurrentPlans: 4,
	}
	catalog, err := NewBudgetCatalog([]config.AskDataBudgetOverride{budgetConfig})
	if err != nil {
		t.Fatal(err)
	}
	processor := &recordingBundleProcessor{
		delays: map[askdata.ID]time.Duration{
			"p1": 5 * time.Millisecond, "p2": time.Second, "p3": time.Second,
		},
	}
	runner, _ := NewBundleRunner(processor, catalog)
	started := time.Now()
	result, err := runner.Run(context.Background(), BundleRunRequest{
		RunID: uuid.NewString(), Bundle: bundle,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 400*time.Millisecond {
		t.Fatalf("hard timeout returned after %s", elapsed)
	}
	if result.Outcome != BundleOutcomePartial || result.SucceededPlans != 1 ||
		result.FailedPlans != 2 || result.TimedOutPlans != 2 ||
		result.Plans[0].Status != BundlePlanSucceeded ||
		result.Plans[1].Status != BundlePlanTimedOut || result.Plans[2].Status != BundlePlanTimedOut {
		t.Fatalf("timeout result = %#v", result)
	}
}

type recordingBundleProcessor struct {
	mu           sync.Mutex
	calls        []askdata.ID
	active       int
	maxActive    int
	delays       map[askdata.ID]time.Duration
	defaultDelay time.Duration
	failures     map[askdata.ID]error
}

func (processor *recordingBundleProcessor) CompileValidateExecute(
	ctx context.Context,
	request BundlePlanExecutionRequest,
) (BundlePlanArtifact, error) {
	processor.mu.Lock()
	processor.calls = append(processor.calls, request.Plan.PlanID)
	processor.active++
	if processor.active > processor.maxActive {
		processor.maxActive = processor.active
	}
	processor.mu.Unlock()
	defer func() {
		processor.mu.Lock()
		processor.active--
		processor.mu.Unlock()
	}()
	delay := processor.defaultDelay
	if configured, exists := processor.delays[request.Plan.PlanID]; exists {
		delay = configured
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return BundlePlanArtifact{}, ctx.Err()
		case <-timer.C:
		}
	}
	if err := processor.failures[request.Plan.PlanID]; err != nil {
		return BundlePlanArtifact{}, err
	}
	return BundlePlanArtifact{
		QueryPlanHash:  askdata.HashBytes([]byte("query/" + request.Plan.PlanID)),
		ValidationHash: askdata.HashBytes([]byte("validation/" + request.Plan.PlanID)),
		ResultHash:     askdata.HashBytes([]byte("result/" + request.Plan.PlanID)),
		RowCount:       1,
	}, nil
}

func runnerBundleFixture(t *testing.T, planCount int) compiler.QueryPlanBundle {
	t.Helper()
	tenantID := runnerTestID("tenant")
	domainID := runnerTestID("domain")
	actorID := runnerTestID("actor")
	metricVersionID := runnerTestID("metric-version")
	modelVersionID := runnerTestID("model-version")
	timeDimensionID := runnerTestID("time-dimension-version")
	timeContractVersionID := runnerTestID("time-contract-version")
	roles := []string{registry.KPIBundleRoleHeadline, registry.KPIBundleRoleTrend, registry.KPIBundleRoleBreakdown}
	charts := []string{"metric-card", "line-trend", "bar-horizontal"}
	items := make([]registry.KPIBundleItem, planCount)
	for index := range items {
		items[index] = registry.KPIBundleItem{
			MetricVersionID: string(metricVersionID), Role: roles[index%len(roles)],
			GroupByDimensionVersionIDs: []string{}, ChartType: charts[index%len(charts)], Order: index + 1,
		}
	}
	bundle := registry.KPIBundle{
		VersionIdentity: registry.VersionIdentity{
			ID: string(runnerTestID("bundle-version")), TenantID: string(tenantID), DomainID: string(domainID),
			ObjectID: string(runnerTestID("bundle-object")), VersionNo: 1,
			Status: registry.VersionStatusCertified, OwnerID: string(runnerTestID("owner")),
		},
		Code: "runner_bundle", Name: "Runner Bundle", Items: items,
		DefaultTimeExpression: "CURRENT_MONTH", RoleMapping: json.RawMessage(`{}`),
	}
	bundle.ContentHash = registry.KPIBundleContentHash(bundle)
	bundleObject, err := registry.KPIBundleReleaseObject(bundle)
	if err != nil {
		t.Fatal(err)
	}
	objects := []registry.ReleaseObject{
		bundleObject,
		runnerReleaseObject(t, registry.ReleaseObjectMetric, runnerTestID("metric-object"), metricVersionID,
			map[string]any{
				"type": "METRIC", "semanticModelVersionId": string(modelVersionID),
				"unit": "COUNT", "additivity": string(registry.FullyAdditive),
			}),
		runnerReleaseObject(t, registry.ReleaseObjectSemanticModel, runnerTestID("model-object"), modelVersionID,
			map[string]any{"type": "SEMANTIC_MODEL", "timeContractVersionId": string(timeContractVersionID)}),
		runnerReleaseObject(t, registry.ReleaseObjectDimension, runnerTestID("time-dimension-object"), timeDimensionID,
			map[string]any{"type": "DIMENSION"}),
		runnerReleaseObject(t, registry.ReleaseObjectTimeContract, runnerTestID("time-contract-object"), timeContractVersionID,
			map[string]any{"type": "TIME_CONTRACT"}),
	}
	manifest, err := registry.BuildReleaseManifest(objects)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := askdata.NewPolicyScope(tenantID, actorID, []askdata.ID{domainID},
		[]askdata.ID{runnerTestID("role")}, askdata.ReleaseRef{
			ReleaseID: runnerTestID("release"), ContentHash: manifest.ContentHash,
		})
	if err != nil {
		t.Fatal(err)
	}
	result, err := compiler.BuildQueryPlanBundle(compiler.BundleBuildRequest{
		Scope: scope, Bundle: bundle, ReleaseManifest: manifest,
		SharedContext: compiler.BundleSharedContext{
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
			Filters: []ir.Filter{},
		},
		MetricContracts: []compiler.BundleMetricContract{{
			MetricVersionID: metricVersionID, ModelVersionID: modelVersionID,
			TimeDimensionVersionID: timeDimensionID,
		}},
	})
	if err != nil {
		t.Fatalf("BuildQueryPlanBundle() error = %v", err)
	}
	return result
}

func runnerReleaseObject(
	t *testing.T,
	objectType registry.ReleaseObjectType,
	objectID askdata.ID,
	versionID askdata.ID,
	contract any,
) registry.ReleaseObject {
	t.Helper()
	hash, _, err := registry.CanonicalContentHash(contract)
	if err != nil {
		t.Fatal(err)
	}
	object, err := registry.NewReleaseObject(
		objectType, string(objectID), string(versionID), registry.SensitivityInternal, contract, hash,
	)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func runnerTestID(label string) askdata.ID {
	return askdata.ID(uuid.NewSHA1(uuid.NameSpaceURL, []byte("bundle-runner-test/"+label)).String())
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
