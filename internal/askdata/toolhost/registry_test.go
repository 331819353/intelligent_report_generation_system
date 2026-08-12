package toolhost

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

func TestRegistryCatalogContainsEveryGovernedTool(t *testing.T) {
	registry, err := NewRegistry(stubHandlers())
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := registry.Definitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != len(validTools) || len(definitions) != 14 {
		t.Fatalf("definition count = %d", len(definitions))
	}
	seen := map[ToolName]bool{}
	for index, definition := range definitions {
		if definition.Validate() != nil || seen[definition.Name] {
			t.Fatalf("invalid definition: %#v", definition)
		}
		if index > 0 && definitions[index-1].Name >= definition.Name {
			t.Fatal("definitions are not in stable tool-name order")
		}
		requiredCharge, known := RequiredBudgetCharge(definition.Name)
		if !known || definition.Charge != requiredCharge {
			t.Fatalf("definition %s charge = %#v, required security charge = %#v", definition.Name, definition.Charge, requiredCharge)
		}
		seen[definition.Name] = true
		for _, schema := range []json.RawMessage{definition.ArgumentSchema, definition.ResultSchema} {
			var document struct {
				AdditionalProperties bool           `json:"additionalProperties"`
				Required             []string       `json:"required"`
				Properties           map[string]any `json:"properties"`
			}
			if err := json.Unmarshal(schema, &document); err != nil || document.AdditionalProperties ||
				len(document.Required) == 0 || len(document.Properties) == 0 {
				t.Fatalf("tool %s has an open or empty schema: %s", definition.Name, schema)
			}
		}
	}
	for tool := range validTools {
		if !seen[tool] || strings.Contains(string(tool), "sql") || strings.Contains(string(tool), "ngql") {
			t.Fatalf("missing or unsafe tool: %s", tool)
		}
	}

	definitions[0].ArgumentSchema[0] = 'x'
	pristine, ok := registry.Definition(definitions[0].Name)
	if !ok || pristine.Validate() != nil {
		t.Fatal("Definitions returned mutable registry schema bytes")
	}
	second, err := NewRegistry(stubHandlers())
	if err != nil {
		t.Fatal(err)
	}
	secondDefinitions, _ := second.Definitions()
	firstDefinitions, _ := registry.Definitions()
	if !reflect.DeepEqual(firstDefinitions, secondDefinitions) {
		t.Fatal("catalog definitions are not deterministic")
	}
	authorization := baseInvocation(t, ToolSearchSemanticObjects, PermissionSemanticRead).Authorization
	authorization.Permissions = []Permission{PermissionQueryExecute, PermissionSemanticRead}
	available, err := registry.AvailableTools(authorization, BudgetAllowance{
		ToolCallsRemaining: 8, FormalQueriesRemaining: 1, ValidationQueriesRemaining: 3,
	})
	if err != nil || !reflect.DeepEqual(available, []ToolName{
		ToolExecuteQueryPlan, ToolGetCertifiedExamples, ToolGetSemanticContracts, ToolSearchSemanticObjects,
	}) {
		t.Fatalf("available tools = %#v, %v", available, err)
	}
}

func TestResolveGraphPlanSchemaRequiresDegradationEvidence(t *testing.T) {
	registry, err := NewRegistry(stubHandlers())
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := registry.Definition(ToolResolveGraphPlan)
	if !ok {
		t.Fatal("resolve_graph_plan definition is missing")
	}
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(definition.ResultSchema, &schema); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, field := range schema.Required {
		found = found || field == "graphDegraded"
	}
	if !found || schema.Properties["graphDegraded"]["type"] != "boolean" {
		t.Fatalf("resolve_graph_plan result schema = %s", definition.ResultSchema)
	}
}

func TestRegistryExecutesTypedSearchWithPermissionBudgetAndHash(t *testing.T) {
	handlers := stubHandlers()
	called := false
	handlers.SearchSemanticObjects = func(
		_ context.Context,
		authorization AuthorizationContext,
		input SearchSemanticObjectsInput,
	) (ToolOutput[SearchSemanticObjectsResult], error) {
		called = true
		if authorization.DomainID != "sales" || input.Mention != "销售额" || input.Limit != 10 ||
			!reflect.DeepEqual(input.ObjectTypes, []ObjectType{ObjectTypeMetric}) ||
			!reflect.DeepEqual(input.DomainIDs, []askdata.ID{"sales"}) {
			t.Fatalf("unexpected typed input: %#v %#v", authorization, input)
		}
		evidence := toolEvidence("search-result")
		return ToolOutput[SearchSemanticObjectsResult]{
			Result: SearchSemanticObjectsResult{
				Candidates: []CandidateSummary{{
					ObjectType: ObjectTypeMetric, ObjectVersionID: "metric-sales-v1",
					Score: 0.9, MatchType: "LEXICAL", Status: "CERTIFIED",
				}},
				Truncated: false, EvidenceIDs: []askdata.ID{evidence.EvidenceID},
			},
			EvidenceRefs: []askdata.EvidenceRef{evidence}, MadeProgress: true,
		}, nil
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	invocation := searchInvocation(t)
	execution, err := registry.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if !called || execution.Response.Status != ResponseSuccess || !execution.Response.MadeProgress ||
		execution.Charge != (BudgetCharge{ToolCalls: 1}) || execution.TimedOut ||
		execution.Response.ResultHash != askdata.HashBytes(execution.Response.Result) ||
		execution.DefinitionHash.Validate() != nil || execution.Validate() != nil {
		t.Fatalf("unexpected execution: %#v", execution)
	}
	var result SearchSemanticObjectsResult
	if err := askdata.DecodeStrictJSON(execution.Response.Result, &result); err != nil || len(result.Candidates) != 1 {
		t.Fatalf("typed result = %#v, %v", result, err)
	}
}

func TestRegistryRejectsPermissionReleaseDomainAndBudgetBeforeHandler(t *testing.T) {
	handlers := stubHandlers()
	calls := 0
	handlers.SearchSemanticObjects = func(
		context.Context, AuthorizationContext, SearchSemanticObjectsInput,
	) (ToolOutput[SearchSemanticObjectsResult], error) {
		calls++
		return ToolOutput[SearchSemanticObjectsResult]{}, nil
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		edit func(*Invocation)
		code string
	}{
		{"permission", func(value *Invocation) {
			value.Authorization.Permissions = []Permission{PermissionQualityRead}
		}, "TOOL_PERMISSION_DENIED"},
		{"release", func(value *Invocation) {
			value.Call.Arguments.Release = askdata.ReleaseRef{
				ReleaseID: "other-release", ContentHash: askdata.HashBytes([]byte("other-release")),
			}
		}, "TOOL_RELEASE_MISMATCH"},
		{"domain", func(value *Invocation) {
			value.Call.Arguments.DomainIDs = []askdata.ID{"finance"}
		}, "TOOL_DOMAIN_FORBIDDEN"},
		{"budget", func(value *Invocation) {
			value.Budget.ToolCallsRemaining = 0
		}, "TOOL_BUDGET_EXHAUSTED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := searchInvocation(t)
			test.edit(&invocation)
			execution, err := registry.Execute(context.Background(), invocation)
			if err != nil {
				t.Fatal(err)
			}
			if execution.Response.Status != ResponseRejected || execution.Response.Error == nil ||
				execution.Response.Error.Code != test.code || execution.Charge != (BudgetCharge{}) ||
				execution.Validate() != nil {
				t.Fatalf("unexpected rejection: %#v", execution)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("rejected invocations reached handler %d times", calls)
	}
}

func TestRegistryEnforcesFormalQueryUnits(t *testing.T) {
	handlers := stubHandlers()
	evidence := toolEvidence("comparison")
	handlers.CompareCandidateResults = func(
		context.Context, AuthorizationContext, CompareCandidateResultsInput,
	) (ToolOutput[CompareCandidateResultsResult], error) {
		return ToolOutput[CompareCandidateResultsResult]{
			Result: CompareCandidateResultsResult{
				LeftResultHash: askdata.HashBytes([]byte("left-result")), RightResultHash: askdata.HashBytes([]byte("right-result")),
				Equivalent: true, DifferenceCount: 0, Differences: []MetricDifferenceSummary{},
				EvidenceIDs: []askdata.ID{evidence.EvidenceID},
			},
			EvidenceRefs: []askdata.EvidenceRef{evidence}, MadeProgress: true,
			QueryScanBytes: 4096,
		}, nil
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	invocation := baseInvocation(t, ToolCompareCandidateResults, PermissionQueryExecute)
	left, right, maxRows := askdata.HashBytes([]byte("left-plan")), askdata.HashBytes([]byte("right-plan")), 100
	invocation.Call.Arguments.LeftPlanHash, invocation.Call.Arguments.RightPlanHash = &left, &right
	invocation.Call.Arguments.MaxRows = &maxRows
	invocation.Budget.FormalQueriesRemaining = 1
	rejected, err := registry.Execute(context.Background(), invocation)
	if err != nil || rejected.Response.Error == nil || rejected.Response.Error.Code != "TOOL_BUDGET_EXHAUSTED" {
		t.Fatalf("formal pair budget rejection = %#v, %v", rejected, err)
	}
	invocation.Budget.FormalQueriesRemaining = 2
	accepted, err := registry.Execute(context.Background(), invocation)
	if err != nil || accepted.Response.Status != ResponseSuccess ||
		accepted.Charge != (BudgetCharge{ToolCalls: 1, FormalQueries: 2}) || accepted.QueryScanBytes != 4096 {
		t.Fatalf("formal pair execution = %#v, %v", accepted, err)
	}
}

func TestRegistryRejectsQueryScanMeasurementFromNonQueryTool(t *testing.T) {
	handlers := stubHandlers()
	evidence := toolEvidence("search-scan-forgery")
	handlers.SearchSemanticObjects = func(
		context.Context, AuthorizationContext, SearchSemanticObjectsInput,
	) (ToolOutput[SearchSemanticObjectsResult], error) {
		return ToolOutput[SearchSemanticObjectsResult]{
			Result: SearchSemanticObjectsResult{
				Candidates: []CandidateSummary{}, EvidenceIDs: []askdata.ID{evidence.EvidenceID},
			},
			EvidenceRefs: []askdata.EvidenceRef{evidence}, MadeProgress: true, QueryScanBytes: 1,
		}, nil
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.Execute(context.Background(), searchInvocation(t))
	if err != nil || execution.Response.Status != ResponseFailed || execution.Response.Error == nil ||
		execution.Response.Error.Code != "TOOL_RESULT_REJECTED" || execution.QueryScanBytes != 0 ||
		execution.Validate() != nil {
		t.Fatalf("forged scan measurement was not rejected: %#v, %v", execution, err)
	}
}

func TestRegistryRedactsSensitiveMemberLabels(t *testing.T) {
	handlers := stubHandlers()
	evidence := toolEvidence("member-lookup")
	handlers.LookupDimensionValues = func(
		context.Context, AuthorizationContext, LookupDimensionValuesInput,
	) (ToolOutput[LookupDimensionValuesResult], error) {
		return ToolOutput[LookupDimensionValuesResult]{
			Result: LookupDimensionValuesResult{
				DimensionVersionID: "dimension-account-v1",
				Members: []DimensionValueSummary{{
					MemberVersionID: "member-sensitive-v1", DisplayLabel: "confidential-label",
					Aliases: []string{"confidential-alias"}, HierarchyPath: []askdata.ID{"member-sensitive-v1"}, Sensitive: true,
				}},
				EvidenceIDs: []askdata.ID{evidence.EvidenceID},
			},
			EvidenceRefs: []askdata.EvidenceRef{evidence}, MadeProgress: true,
		}, nil
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	invocation := baseInvocation(t, ToolLookupDimensionValues, PermissionDimensionValueRead)
	mention, dimensionID, limit := "机密科目", askdata.ID("dimension-account-v1"), 10
	invocation.Call.Arguments.Mention, invocation.Call.Arguments.DimensionVersionID, invocation.Call.Arguments.Limit = &mention, &dimensionID, &limit
	execution, err := registry.Execute(context.Background(), invocation)
	if err != nil || execution.Response.Status != ResponseSuccess {
		t.Fatalf("lookup execution = %#v, %v", execution, err)
	}
	if strings.Contains(string(execution.Response.Result), "confidential") {
		t.Fatalf("sensitive member label leaked: %s", execution.Response.Result)
	}
	var result LookupDimensionValuesResult
	if err := askdata.DecodeStrictJSON(execution.Response.Result, &result); err != nil ||
		result.Members[0].DisplayLabel != "" || len(result.Members[0].Aliases) != 0 {
		t.Fatalf("sensitive result was not redacted: %#v, %v", result, err)
	}
}

func TestRegistryRejectsInvalidOrUnsafeResultsAndDoesNotLeakHandlerErrors(t *testing.T) {
	handlers := stubHandlers()
	evidence := toolEvidence("invalid-result")
	handlers.SearchSemanticObjects = func(
		context.Context, AuthorizationContext, SearchSemanticObjectsInput,
	) (ToolOutput[SearchSemanticObjectsResult], error) {
		return ToolOutput[SearchSemanticObjectsResult]{
			Result: SearchSemanticObjectsResult{
				Candidates: []CandidateSummary{{
					ObjectType: ObjectTypeMetric, ObjectVersionID: "metric-sales-v1", Score: math.NaN(),
					MatchType: "VECTOR", Status: "CERTIFIED",
				}},
				EvidenceIDs: []askdata.ID{evidence.EvidenceID},
			}, EvidenceRefs: []askdata.EvidenceRef{evidence},
		}, nil
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := registry.Execute(context.Background(), searchInvocation(t))
	if err != nil || execution.Response.Error == nil || execution.Response.Error.Code != "TOOL_RESULT_REJECTED" {
		t.Fatalf("invalid result execution = %#v, %v", execution, err)
	}

	handlers = stubHandlers()
	handlers.SearchSemanticObjects = func(
		context.Context, AuthorizationContext, SearchSemanticObjectsInput,
	) (ToolOutput[SearchSemanticObjectsResult], error) {
		return ToolOutput[SearchSemanticObjectsResult]{}, errors.New("database password and raw SQL leaked")
	}
	registry, _ = NewRegistry(handlers)
	execution, err = registry.Execute(context.Background(), searchInvocation(t))
	if err != nil || execution.Response.Error == nil || execution.Response.Error.Code != "TOOL_EXECUTION_FAILED" ||
		strings.Contains(execution.Response.Error.Message, "password") || strings.Contains(execution.Response.Error.Message, "SQL") {
		t.Fatalf("handler error leaked: %#v, %v", execution, err)
	}

	unsafe := json.RawMessage(`{"rows":[["secret"]]}`)
	direct := Response{
		SchemaVersion: SchemaVersion, CallID: "call-unsafe", Tool: ToolSearchSemanticObjects,
		Status: ResponseSuccess, Result: unsafe, EvidenceRefs: []askdata.EvidenceRef{evidence},
		ResultHash: askdata.HashBytes(unsafe), MadeProgress: true,
	}
	if err := direct.Validate(); err == nil {
		t.Fatal("direct ToolMessage response accepted row-level data outside registry")
	}
}

func TestRegistryAppliesPerToolTimeout(t *testing.T) {
	handlers := stubHandlers()
	handlers.SearchSemanticObjects = func(
		ctx context.Context, _ AuthorizationContext, _ SearchSemanticObjectsInput,
	) (ToolOutput[SearchSemanticObjectsResult], error) {
		<-ctx.Done()
		return ToolOutput[SearchSemanticObjectsResult]{}, ctx.Err()
	}
	registry, err := NewRegistry(handlers)
	if err != nil {
		t.Fatal(err)
	}
	registered := registry.tools[ToolSearchSemanticObjects]
	registered.definition.TimeoutMS = 20
	registered.definition.DefinitionHash, err = definitionContentHash(registered.definition)
	if err != nil || registered.definition.Validate() != nil {
		t.Fatal("could not construct bounded timeout fixture")
	}
	registry.tools[ToolSearchSemanticObjects] = registered
	started := time.Now()
	execution, err := registry.Execute(context.Background(), searchInvocation(t))
	if err != nil || execution.Response.Error == nil || execution.Response.Error.Code != "TOOL_TIMEOUT" ||
		!execution.TimedOut || execution.Charge != (BudgetCharge{ToolCalls: 1}) || time.Since(started) > time.Second {
		t.Fatalf("timeout execution = %#v, %v, duration=%s", execution, err, time.Since(started))
	}
}

func TestEveryTypedResultContractSanitizesToEvidenceBoundObject(t *testing.T) {
	evidence := toolEvidence("all-result-contracts")
	evidenceIDs := []askdata.ID{evidence.EvidenceID}
	hash := askdata.HashBytes([]byte("result-contract"))
	contracts := []struct {
		name   ToolName
		result resultContract
	}{
		{ToolSearchSemanticObjects, SearchSemanticObjectsResult{
			Candidates: []CandidateSummary{}, EvidenceIDs: evidenceIDs,
		}},
		{ToolGetSemanticContracts, GetSemanticContractsResult{
			Contracts: []SemanticContractSummary{{
				ObjectType: ObjectTypeMetric, ObjectVersionID: "metric-sales-v1", Name: "销售额",
				Definition: "已认证净销售额。", OwnerID: "owner-sales", Status: "CERTIFIED", ContentHash: hash,
				Formula: &FormulaSummary{FormulaHash: hash, OperatorCodes: []string{"SUM"}, ReferencedVersionIDs: []askdata.ID{"measure-sales-v1"}},
			}}, EvidenceIDs: evidenceIDs,
		}},
		{ToolLookupDimensionValues, LookupDimensionValuesResult{
			DimensionVersionID: "dimension-region-v1", Members: []DimensionValueSummary{}, EvidenceIDs: evidenceIDs,
		}},
		{ToolGetCertifiedExamples, GetCertifiedExamplesResult{
			Examples: []CertifiedExampleSummary{{
				ExampleID:                "example-sales-v1",
				ExpectedMetricVersionIDs: []askdata.ID{"metric-sales-v1"},
				ExpectedDimensionIDs:     []askdata.ID{"dimension-region-v1"},
				ExpectedTimeExpression:   "LAST_MONTH",
				ContentHash:              hash, SimilarityPermillion: 900_000,
			}}, EvidenceIDs: evidenceIDs,
		}},
		{ToolResolveGraphPlan, ResolveGraphPlanResult{
			GraphPlanHash: hash, ModelVersionIDs: []askdata.ID{"model-sales-v1"},
			RelationshipIDs: []askdata.ID{}, Risks: []GraphRisk{}, EvidenceIDs: evidenceIDs,
		}},
		{ToolValidateSemanticBundle, ValidateSemanticBundleResult{
			Valid: true, MissingObjectVersionIDs: []askdata.ID{}, Conflicts: []BundleConflict{},
			ConfidencePermillion: 900_000, EvidenceIDs: evidenceIDs,
		}},
		{ToolGetDataQualityStatus, GetDataQualityStatusResult{
			Status: "PASS", DataAsOf: "2026-08-06T00:00:00Z", CoverageStart: "2026-01-01",
			CoverageEnd: "2027-01-01", Rules: []QualityRuleSummary{}, EvidenceIDs: evidenceIDs,
		}},
		{ToolCompileSemanticQuery, CompileSemanticQueryResult{
			PlanHash: hash, SemanticIRHash: hash, PlanCount: 1,
			ParameterShapes: []ParameterShapeSummary{}, MaxRows: 100, EvidenceIDs: evidenceIDs,
		}},
		{ToolValidateQueryPlan, ValidateQueryPlanResult{
			Allowed: true, ValidationHash: hash, MaxCost: 10, MaxPlanRows: 1,
			Risks: []PlanRiskSummary{}, EvidenceIDs: evidenceIDs,
		}},
		{ToolProbeJoinCardinality, ProbeJoinCardinalityResult{Safe: true, EvidenceIDs: evidenceIDs}},
		{ToolExecuteQueryPlan, ExecuteQueryPlanResult{
			ResultHash: hash, VerificationHash: hash, Verdict: "PASS", RowCount: 0,
			Columns: []ResultColumnSummary{{Code: "net_sales", CanonicalType: "DECIMAL"}},
			Metrics: []ResultMetricSummary{{Code: "net_sales"}}, EvidenceIDs: evidenceIDs,
		}},
		{ToolExecuteValidationQuery, ExecuteValidationQueryResult{
			ValidationType: ValidationTimeCoverage, SummaryHash: hash, EvidenceIDs: evidenceIDs,
		}},
		{ToolCompareCandidateResults, CompareCandidateResultsResult{
			LeftResultHash: hash, RightResultHash: hash, Equivalent: true,
			Differences: []MetricDifferenceSummary{}, EvidenceIDs: evidenceIDs,
		}},
		{ToolRequestClarification, RequestClarificationResult{
			ConflictCode: "METRIC_AMBIGUOUS", Question: "请选择指标口径。",
			Options:     []ClarificationOption{{OptionID: "metric-sales", Label: "销售额", EvidenceRefs: []askdata.EvidenceRef{evidence}}},
			EvidenceIDs: evidenceIDs,
		}},
	}
	for _, contract := range contracts {
		t.Run(string(contract.name), func(t *testing.T) {
			payload, references, err := sanitizeToolResult(toolExecutionOutput{
				result: contract.result, evidenceRefs: []askdata.EvidenceRef{evidence}, madeProgress: true,
			}, MaxToolResultBytes)
			if err != nil || len(payload) == 0 || len(references) != 1 || rejectUnsafeResultKeys(payload) != nil {
				t.Fatalf("sanitized result = %s, %#v, %v", payload, references, err)
			}
		})
	}
}

func stubHandlers() Handlers {
	failed := errors.New("stub handler was called")
	return Handlers{
		SearchSemanticObjects: func(context.Context, AuthorizationContext, SearchSemanticObjectsInput) (ToolOutput[SearchSemanticObjectsResult], error) {
			return ToolOutput[SearchSemanticObjectsResult]{}, failed
		},
		GetSemanticContracts: func(context.Context, AuthorizationContext, GetSemanticContractsInput) (ToolOutput[GetSemanticContractsResult], error) {
			return ToolOutput[GetSemanticContractsResult]{}, failed
		},
		LookupDimensionValues: func(context.Context, AuthorizationContext, LookupDimensionValuesInput) (ToolOutput[LookupDimensionValuesResult], error) {
			return ToolOutput[LookupDimensionValuesResult]{}, failed
		},
		GetCertifiedExamples: func(context.Context, AuthorizationContext, GetCertifiedExamplesInput) (ToolOutput[GetCertifiedExamplesResult], error) {
			return ToolOutput[GetCertifiedExamplesResult]{}, failed
		},
		ResolveGraphPlan: func(context.Context, AuthorizationContext, SemanticBundleInput) (ToolOutput[ResolveGraphPlanResult], error) {
			return ToolOutput[ResolveGraphPlanResult]{}, failed
		},
		ValidateSemanticBundle: func(context.Context, AuthorizationContext, SemanticBundleInput) (ToolOutput[ValidateSemanticBundleResult], error) {
			return ToolOutput[ValidateSemanticBundleResult]{}, failed
		},
		GetDataQualityStatus: func(context.Context, AuthorizationContext, DataQualityInput) (ToolOutput[GetDataQualityStatusResult], error) {
			return ToolOutput[GetDataQualityStatusResult]{}, failed
		},
		CompileSemanticQuery: func(context.Context, AuthorizationContext, CompileSemanticQueryInput) (ToolOutput[CompileSemanticQueryResult], error) {
			return ToolOutput[CompileSemanticQueryResult]{}, failed
		},
		ValidateQueryPlan: func(context.Context, AuthorizationContext, ValidateQueryPlanInput) (ToolOutput[ValidateQueryPlanResult], error) {
			return ToolOutput[ValidateQueryPlanResult]{}, failed
		},
		ProbeJoinCardinality: func(context.Context, AuthorizationContext, ProbeJoinCardinalityInput) (ToolOutput[ProbeJoinCardinalityResult], error) {
			return ToolOutput[ProbeJoinCardinalityResult]{}, failed
		},
		ExecuteQueryPlan: func(context.Context, AuthorizationContext, ExecuteQueryPlanInput) (ToolOutput[ExecuteQueryPlanResult], error) {
			return ToolOutput[ExecuteQueryPlanResult]{}, failed
		},
		ExecuteValidationQuery: func(context.Context, AuthorizationContext, ExecuteValidationQueryInput) (ToolOutput[ExecuteValidationQueryResult], error) {
			return ToolOutput[ExecuteValidationQueryResult]{}, failed
		},
		CompareCandidateResults: func(context.Context, AuthorizationContext, CompareCandidateResultsInput) (ToolOutput[CompareCandidateResultsResult], error) {
			return ToolOutput[CompareCandidateResultsResult]{}, failed
		},
		RequestClarification: func(context.Context, AuthorizationContext, RequestClarificationInput) (ToolOutput[RequestClarificationResult], error) {
			return ToolOutput[RequestClarificationResult]{}, failed
		},
	}
}

func searchInvocation(t *testing.T) Invocation {
	t.Helper()
	invocation := baseInvocation(t, ToolSearchSemanticObjects, PermissionSemanticRead)
	mention, limit := "销售额", 10
	invocation.Call.Arguments.Mention = &mention
	invocation.Call.Arguments.ObjectTypes = []ObjectType{ObjectTypeMetric}
	invocation.Call.Arguments.DomainIDs = []askdata.ID{"sales"}
	invocation.Call.Arguments.Limit = &limit
	return invocation
}

func baseInvocation(t *testing.T, tool ToolName, permission Permission) Invocation {
	t.Helper()
	release := askdata.ReleaseRef{ReleaseID: "release-tools-v1", ContentHash: askdata.HashBytes([]byte("release-tools-v1"))}
	scope, err := askdata.NewPolicyScope("tenant-tools", "actor-tools", []askdata.ID{"sales"}, []askdata.ID{"analyst"}, release)
	if err != nil {
		t.Fatal(err)
	}
	return Invocation{
		Authorization: AuthorizationContext{Scope: scope, DomainID: "sales", Permissions: []Permission{permission}},
		Budget:        BudgetAllowance{ToolCallsRemaining: 8, FormalQueriesRemaining: 2, ValidationQueriesRemaining: 3},
		Call: CallRequest{
			SchemaVersion: SchemaVersion, CallID: "call-" + askdata.ID(tool), Tool: tool,
			Arguments: NewArguments(release),
		},
	}
}

func toolEvidence(id askdata.ID) askdata.EvidenceRef {
	return askdata.EvidenceRef{
		EvidenceID: id, Kind: askdata.EvidenceKindRule, SourceID: "release-tools-v1",
		ContentHash: askdata.HashBytes([]byte("tool-evidence:" + string(id))),
	}
}

// 认证问法只是检索先验：它携带「期望绑定的语义对象」而不是可执行计划。
// 契约必须拒绝越界的对象集合与畸形标识，避免示例把未经校验的对象或
// 一份冻结的计划夹带进绑定阶段。
func TestCertifiedExampleContractCarriesComponentsNotAnExecutablePlan(t *testing.T) {
	evidence := toolEvidence("certified-example-contract")
	known := map[askdata.ID]askdata.EvidenceRef{evidence.EvidenceID: evidence}
	hash := askdata.HashBytes([]byte("example"))
	valid := CertifiedExampleSummary{
		ExampleID:                "example-sales-v1",
		ExpectedMetricVersionIDs: []askdata.ID{"metric-sales-v1"},
		ExpectedDimensionIDs:     []askdata.ID{"dimension-region-v1"},
		ExpectedTimeExpression:   "LAST_MONTH",
		ContentHash:              hash, SimilarityPermillion: 812_000,
	}
	accepted := GetCertifiedExamplesResult{
		Examples: []CertifiedExampleSummary{valid}, EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	if err := accepted.ValidateResult(known); err != nil {
		t.Fatalf("well-formed example rejected: %v", err)
	}

	oversized := make([]askdata.ID, MaxArgumentIDs+1)
	for index := range oversized {
		oversized[index] = "metric-sales-v1"
	}
	for name, mutate := range map[string]func(*CertifiedExampleSummary){
		"unbounded metric set":    func(value *CertifiedExampleSummary) { value.ExpectedMetricVersionIDs = oversized },
		"unbounded dimension set": func(value *CertifiedExampleSummary) { value.ExpectedDimensionIDs = oversized },
		"malformed content hash":  func(value *CertifiedExampleSummary) { value.ContentHash = "not-a-hash" },
		"similarity out of range": func(value *CertifiedExampleSummary) { value.SimilarityPermillion = 1_000_001 },
		"oversized time expression": func(value *CertifiedExampleSummary) {
			value.ExpectedTimeExpression = strings.Repeat("x", 513)
		},
	} {
		broken := valid
		mutate(&broken)
		result := GetCertifiedExamplesResult{
			Examples: []CertifiedExampleSummary{broken}, EvidenceIDs: []askdata.ID{evidence.EvidenceID},
		}
		if err := result.ValidateResult(known); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}
