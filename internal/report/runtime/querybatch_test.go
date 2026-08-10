package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

type queryExecutorFunc func(context.Context, QueryRequest) (QueryResult, error)

func (function queryExecutorFunc) ExecuteReportQuery(ctx context.Context, request QueryRequest) (QueryResult, error) {
	return function(ctx, request)
}

func TestBuildExecutionPlanProducesStructuredRequestsWithoutSQL(t *testing.T) {
	definition := runtimeDefinition(t, "simple-report.json")
	plan, err := BuildExecutionPlan(definition, PlanRequest{
		PageID: definition.Pages[0].ID, PolicyScopeHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Components) != 1 || plan.Components[0].Query == nil ||
		!plan.Components[0].Query.UncertifiedDefinition || plan.Components[0].Query.Timeout != 5*time.Second {
		t.Fatalf("dataset execution plan = %#v", plan)
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	if strings.Contains(lower, `"sql"`) || strings.Contains(lower, "select ") || strings.Contains(lower, " from ") {
		t.Fatalf("report plan leaked SQL: %s", raw)
	}

	semantic := runtimeDefinition(t, "ask-data-report.json")
	semanticPlan, err := BuildExecutionPlan(semantic, PlanRequest{
		PageID: semantic.Pages[0].ID, PolicyScopeHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PinExecutionVersion(&semanticPlan, testLoadedReport()); err != nil {
		t.Fatal(err)
	}
	request := semanticPlan.Components[0].Query
	if request.SemanticContentHash == "" || request.FixedQueryPlanHash == "" || request.Timeout != 5*time.Second {
		t.Fatalf("semantic execution plan lost pinned identity: %#v", request)
	}
}

func TestExecuteBatchDeduplicatesByPolicyAndBoundsConcurrency(t *testing.T) {
	base := datasetQueryRequest("dataset_version_base", strings.Repeat("a", 64))
	var calls atomic.Int32
	executor := queryExecutorFunc(func(context.Context, QueryRequest) (QueryResult, error) {
		calls.Add(1)
		return QueryResult{Rows: [][]any{{"ok"}}}, nil
	})
	plan := ExecutionPlan{Components: []ComponentPlan{
		{ComponentID: "component_a", Query: &base},
		{ComponentID: "component_b", Query: &base},
		{ComponentID: "component_c", Query: &base},
	}}
	results := ExecuteBatch(context.Background(), plan, executor, 8)
	if calls.Load() != 1 || len(results) != 3 {
		t.Fatalf("deduplicated calls=%d results=%#v", calls.Load(), results)
	}

	differentPolicy := base
	differentPolicy.PolicyScopeHash = strings.Repeat("b", 64)
	plan.Components = append(plan.Components, ComponentPlan{ComponentID: "component_d", Query: &differentPolicy})
	calls.Store(0)
	ExecuteBatch(context.Background(), plan, executor, 8)
	if calls.Load() != 2 {
		t.Fatalf("different policy scopes merged into %d calls", calls.Load())
	}

	var active atomic.Int32
	var maximum atomic.Int32
	concurrencyExecutor := queryExecutorFunc(func(context.Context, QueryRequest) (QueryResult, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		time.Sleep(8 * time.Millisecond)
		active.Add(-1)
		return QueryResult{Rows: [][]any{{1}}}, nil
	})
	concurrentPlan := ExecutionPlan{}
	for index := 0; index < 40; index++ {
		request := datasetQueryRequest(askdata.ID(fmt.Sprintf("dataset_version_%02d", index)), strings.Repeat("c", 64))
		concurrentPlan.Components = append(concurrentPlan.Components, ComponentPlan{
			ComponentID: askdata.ID(fmt.Sprintf("component_%02d", index)), Query: &request,
		})
	}
	ExecuteBatch(context.Background(), concurrentPlan, concurrencyExecutor, 100)
	if maximum.Load() > 16 || maximum.Load() < 2 {
		t.Fatalf("maximum report query concurrency = %d", maximum.Load())
	}
}

func TestExecuteBatchTimeoutAndViewerPermissionFailClosed(t *testing.T) {
	timeoutRequest := datasetQueryRequest("dataset_timeout", strings.Repeat("d", 64))
	timeoutRequest.Timeout = 5 * time.Millisecond
	timeout := queryExecutorFunc(func(ctx context.Context, _ QueryRequest) (QueryResult, error) {
		<-ctx.Done()
		return QueryResult{}, ctx.Err()
	})
	results := ExecuteBatch(context.Background(), ExecutionPlan{Components: []ComponentPlan{{ComponentID: "component_timeout", Query: &timeoutRequest}}}, timeout, 1)
	if len(results) != 1 || results[0].State != StateTimeout || results[0].ErrorCode != "REPORT_QUERY_TIMEOUT" {
		t.Fatalf("timeout results = %#v", results)
	}

	deniedRequest := datasetQueryRequest("dataset_denied", strings.Repeat("e", 64))
	denied := queryExecutorFunc(func(context.Context, QueryRequest) (QueryResult, error) {
		return QueryResult{}, codedQueryError("NO_PERMISSION")
	})
	results = ExecuteBatch(context.Background(), ExecutionPlan{Components: []ComponentPlan{{ComponentID: "component_secret", Query: &deniedRequest}}}, denied, 1)
	raw, _ := json.Marshal(results)
	if len(results) != 1 || results[0].State != StateNoPermission || results[0].Result != nil ||
		strings.Contains(string(raw), "dataset_denied") {
		t.Fatalf("permission result leaked binding information: %s", raw)
	}
}

type codedQueryError string

func (err codedQueryError) Error() string { return string(err) }
func (err codedQueryError) Code() string  { return string(err) }

type semanticRunnerFunc func(context.Context, SemanticExecutionRequest) (QueryResult, error)

func (function semanticRunnerFunc) CompileAndExecuteSemanticIR(ctx context.Context, request SemanticExecutionRequest) (QueryResult, error) {
	return function(ctx, request)
}

type datasetRunnerFunc func(context.Context, DatasetExecutionRequest) (QueryResult, error)

func (function datasetRunnerFunc) ExecuteDatasetFields(ctx context.Context, request DatasetExecutionRequest) (QueryResult, error) {
	return function(ctx, request)
}

func TestGovernedQueryExecutorDispatchesClosedBindingUnion(t *testing.T) {
	semanticDefinition := runtimeDefinition(t, "ask-data-report.json")
	semanticPlan, err := BuildExecutionPlan(semanticDefinition, PlanRequest{
		PageID: semanticDefinition.Pages[0].ID, PolicyScopeHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PinExecutionVersion(&semanticPlan, testLoadedReport()); err != nil {
		t.Fatal(err)
	}
	semanticCalled := false
	datasetCalled := false
	executor := GovernedQueryExecutor{
		Semantic: semanticRunnerFunc(func(_ context.Context, request SemanticExecutionRequest) (QueryResult, error) {
			semanticCalled = request.IR.SemanticReleaseID == request.ReleaseID && request.FixedPlanHash != ""
			return QueryResult{Rows: [][]any{{1}}}, nil
		}),
		Dataset: datasetRunnerFunc(func(_ context.Context, request DatasetExecutionRequest) (QueryResult, error) {
			datasetCalled = request.DatasetVersionID != "" && len(request.Measures) > 0
			return QueryResult{Rows: [][]any{{1}}}, nil
		}),
	}
	if _, err := executor.ExecuteReportQuery(context.Background(), *semanticPlan.Components[0].Query); err != nil {
		t.Fatal(err)
	}
	datasetDefinition := runtimeDefinition(t, "simple-report.json")
	datasetPlan, err := BuildExecutionPlan(datasetDefinition, PlanRequest{
		PageID: datasetDefinition.Pages[0].ID, PolicyScopeHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PinExecutionVersion(&datasetPlan, testLoadedReport()); err != nil {
		t.Fatal(err)
	}
	if _, err := executor.ExecuteReportQuery(context.Background(), *datasetPlan.Components[0].Query); err != nil {
		t.Fatal(err)
	}
	if !semanticCalled || !datasetCalled {
		t.Fatalf("closed union dispatch semantic=%v dataset=%v", semanticCalled, datasetCalled)
	}

	tampered := *semanticPlan.Components[0].Query
	tampered.SemanticReleaseID = "semantic_release_tampered"
	if _, err := executor.ExecuteReportQuery(context.Background(), tampered); err == nil {
		t.Fatal("tampered semantic release identity was accepted")
	}

	overLimit := *datasetPlan.Components[0].Query
	overLimit.Limit = 1
	overLimitExecutor := GovernedQueryExecutor{Dataset: datasetRunnerFunc(func(context.Context, DatasetExecutionRequest) (QueryResult, error) {
		return QueryResult{Rows: [][]any{{1}, {2}}}, nil
	})}
	if _, err := overLimitExecutor.ExecuteReportQuery(context.Background(), overLimit); err == nil {
		t.Fatal("dataset runner result above the requested maximum was accepted")
	}
}

type reportViewerContextKey struct{}

func TestViewerPolicyScopeIsReappliedAndCannotReusePublisherResult(t *testing.T) {
	definition := runtimeDefinition(t, "simple-report.json")
	publisherPlan, err := BuildExecutionPlan(definition, PlanRequest{
		PageID: definition.Pages[0].ID, PolicyScopeHash: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PinExecutionVersion(&publisherPlan, testLoadedReport()); err != nil {
		t.Fatal(err)
	}
	viewerPlan, err := BuildExecutionPlan(definition, PlanRequest{
		PageID: definition.Pages[0].ID, PolicyScopeHash: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := PinExecutionVersion(&viewerPlan, testLoadedReport()); err != nil {
		t.Fatal(err)
	}
	executor := GovernedQueryExecutor{Dataset: datasetRunnerFunc(func(ctx context.Context, request DatasetExecutionRequest) (QueryResult, error) {
		role, _ := ctx.Value(reportViewerContextKey{}).(string)
		if role == "publisher" && request.PolicyScopeHash == strings.Repeat("a", 64) {
			return QueryResult{Rows: [][]any{{"north"}, {"restricted"}}}, nil
		}
		if role == "viewer" && request.PolicyScopeHash == strings.Repeat("b", 64) {
			return QueryResult{Rows: [][]any{{"north"}}}, nil
		}
		return QueryResult{}, codedQueryError("NO_PERMISSION")
	})}
	publisherResults := ExecuteBatch(context.WithValue(context.Background(), reportViewerContextKey{}, "publisher"), publisherPlan, executor, 8)
	viewerResults := ExecuteBatch(context.WithValue(context.Background(), reportViewerContextKey{}, "viewer"), viewerPlan, executor, 8)
	if len(publisherResults) != 1 || publisherResults[0].Result == nil || len(publisherResults[0].Result.Rows) != 2 ||
		len(viewerResults) != 1 || viewerResults[0].Result == nil || len(viewerResults[0].Result.Rows) != 1 {
		t.Fatalf("viewer policy was not reapplied: publisher=%#v viewer=%#v", publisherResults, viewerResults)
	}
	if publisherResults[0].Result.Rows[1][0] == viewerResults[0].Result.Rows[0][0] {
		t.Fatal("restricted publisher row leaked to the viewer")
	}
}

func datasetQueryRequest(datasetVersionID askdata.ID, policyHash string) QueryRequest {
	return QueryRequest{
		BindingMode: report.BindingDatasetField, DatasetVersionID: datasetVersionID, DataContextID: "context_sales",
		Dimensions: []report.FieldBinding{{Role: report.RoleDimension, Field: "region"}},
		Measures:   []report.FieldBinding{{Role: report.RoleValue, Field: "sales"}},
		Limit:      100, Timeout: time.Second, PolicyScopeHash: policyHash, UncertifiedDefinition: true,
	}
}

func testLoadedReport() LoadedReport {
	return LoadedReport{
		ReportID:  "00000000-0000-4000-8000-000000000101",
		VersionID: "00000000-0000-4000-8000-000000000102",
	}
}
