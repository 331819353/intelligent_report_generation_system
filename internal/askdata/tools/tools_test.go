package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/askdata/validator"
)

func testRun(t *testing.T) RunContext {
	t.Helper()
	domainID := askdata.ID("11111111-1111-4111-8111-111111111111")
	scope, err := askdata.NewPolicyScope(
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		[]askdata.ID{domainID},
		[]askdata.ID{"55555555-5555-4555-8555-555555555555"},
		askdata.ReleaseRef{
			ReleaseID:   "22222222-2222-4222-8222-222222222222",
			ContentHash: askdata.HashBytes([]byte("release")),
		},
	)
	if err != nil {
		t.Fatalf("build policy scope: %v", err)
	}
	return RunContext{
		Scope: scope, DomainID: domainID,
		RunID: "66666666-6666-4666-8666-666666666666",
	}
}

func testAuthorization(run RunContext) toolhost.AuthorizationContext {
	return toolhost.AuthorizationContext{
		Scope: run.Scope, DomainID: run.DomainID,
		Permissions: []toolhost.Permission{toolhost.PermissionSemanticRead},
	}
}

// 一个 Binding 只服务它自己的 Run：授权上下文的领域、策略哈希或 Release
// 与该 Run 不一致时必须拒绝，否则一次调用可能在别的 actor 的策略范围内执行。
func TestBindingRefusesAuthorizationFromAnotherRun(t *testing.T) {
	run := testRun(t)
	binding, err := NewBinding(Services{}, run)
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	if err := binding.authorize(testAuthorization(run)); err != nil {
		t.Fatalf("matching authorization rejected: %v", err)
	}

	foreignDomain := testAuthorization(run)
	foreignDomain.DomainID = "77777777-7777-4777-8777-777777777777"
	if err := binding.authorize(foreignDomain); err == nil {
		t.Fatal("a foreign domain must be rejected")
	}

	foreignRelease := testAuthorization(run)
	foreignRelease.Scope.Release = askdata.ReleaseRef{
		ReleaseID:   "88888888-8888-4888-8888-888888888888",
		ContentHash: askdata.HashBytes([]byte("other-release")),
	}
	if err := binding.authorize(foreignRelease); err == nil {
		t.Fatal("a different pinned release must be rejected")
	}

	foreignPolicy := testAuthorization(run)
	foreignPolicy.Scope.PolicyHash = askdata.HashBytes([]byte("other-policy"))
	if err := binding.authorize(foreignPolicy); err == nil {
		t.Fatal("a different policy scope must be rejected")
	}
}

// 计划哈希是 Run 内作用域：另一个 Run 编译出的计划不能在这里被校验或执行，
// 否则调用方可以执行在别的策略范围下编译的计划。
func TestPlanHashesAreScopedToTheRunThatCompiledThem(t *testing.T) {
	run := testRun(t)
	binding, err := NewBinding(Services{}, run)
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	hash := askdata.HashBytes([]byte("plan"))
	if _, ok := binding.plans.get(hash); ok {
		t.Fatal("a fresh run must not resolve any plan hash")
	}
	binding.plans.put(hash, compiler.QueryArtifact{PlanHash: hash}, ircontract.SemanticIR{})
	if _, ok := binding.plans.get(hash); !ok {
		t.Fatal("a plan compiled in this run must resolve")
	}

	other, err := NewBinding(Services{}, testRun(t))
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	if _, ok := other.plans.get(hash); ok {
		t.Fatal("a plan compiled in another run must not resolve")
	}
}

// 未配置的能力必须显式报 ErrToolUnavailable，不能返回一个伪造的空成功结果，
// 否则「没接线」会被当成「没有数据」。
func TestUnavailableCapabilitiesFailExplicitlyInsteadOfReturningEmptySuccess(t *testing.T) {
	run := testRun(t)
	binding, err := NewBinding(Services{}, run)
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	authorization := testAuthorization(run)
	ctx := context.Background()

	if _, err := binding.searchSemanticObjects(ctx, authorization,
		toolhost.SearchSemanticObjectsInput{Mention: "销售额", Limit: 10}); err != ErrToolUnavailable {
		t.Fatalf("search without a retriever = %v", err)
	}
	if _, err := binding.getSemanticContracts(ctx, authorization,
		toolhost.GetSemanticContractsInput{}); err != ErrToolUnavailable {
		t.Fatalf("contracts without a reader = %v", err)
	}
	if _, err := binding.resolveGraphPlan(ctx, authorization,
		toolhost.SemanticBundleInput{}); err != ErrToolUnavailable {
		t.Fatalf("graph plan without a resolver = %v", err)
	}
	if _, err := binding.compileSemanticQuery(ctx, authorization,
		toolhost.CompileSemanticQueryInput{}); err != ErrToolUnavailable {
		t.Fatalf("compile without a compiler = %v", err)
	}
}

func TestMetricRetrievalIncludesExecutableSemanticContext(t *testing.T) {
	completed := completeAnalyticObjectTypes([]toolhost.ObjectType{toolhost.ObjectTypeMetric})
	want := []toolhost.ObjectType{
		toolhost.ObjectTypeMetric,
		toolhost.ObjectTypeDimension,
		toolhost.ObjectTypeModel,
		toolhost.ObjectTypeReportAsset,
	}
	if len(completed) != len(want) {
		t.Fatalf("completed object types = %v, want %v", completed, want)
	}
	for index := range want {
		if completed[index] != want[index] {
			t.Fatalf("completed object types = %v, want %v", completed, want)
		}
	}
	termOnly := completeAnalyticObjectTypes([]toolhost.ObjectType{toolhost.ObjectTypeTerm})
	if len(termOnly) != 1 || termOnly[0] != toolhost.ObjectTypeTerm {
		t.Fatalf("term-only retrieval widened to %v", termOnly)
	}
	reportLookup := completeAnalyticObjectTypes([]toolhost.ObjectType{toolhost.ObjectTypeReportAsset})
	if len(reportLookup) != len(want) {
		t.Fatalf("report lookup omitted executable context: %v", reportLookup)
	}
}

// Run 上下文必须完整且固定 Release，否则不允许构造任何工具处理器。
func TestBindingRequiresACompleteRunContext(t *testing.T) {
	valid := testRun(t)
	for name, mutate := range map[string]func(*RunContext){
		"missing run id":  func(run *RunContext) { run.RunID = "" },
		"missing domain":  func(run *RunContext) { run.DomainID = "" },
		"missing release": func(run *RunContext) { run.Scope.Release = askdata.ReleaseRef{} },
	} {
		broken := valid
		mutate(&broken)
		if _, err := NewBinding(Services{}, broken); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// 全部 14 个工具都必须有实现，否则 toolhost.NewRegistry 会拒绝构造。
// 这条断言把「适配层完整」变成编译期之外的可执行事实。
func TestBindingBuildsTheCompleteGovernedToolRegistry(t *testing.T) {
	binding, err := NewBinding(Services{}, testRun(t))
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	registry, err := binding.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	definitions, err := registry.Definitions()
	if err != nil {
		t.Fatalf("Definitions() error = %v", err)
	}
	if len(definitions) != 14 {
		t.Fatalf("registry exposes %d tools, want 14", len(definitions))
	}
}

// 未经校验的计划绝不能执行：编译过但没校验过的哈希必须被拒绝，
// 执行路径不会隐式补一次校验。
func TestExecutionRequiresAPlanValidatedInThisRun(t *testing.T) {
	binding, err := NewBinding(Services{}, testRun(t))
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	hash := askdata.HashBytes([]byte("plan"))
	binding.plans.put(hash, compiler.QueryArtifact{PlanHash: hash}, ircontract.SemanticIR{})
	if _, _, _, ok := binding.plans.getValidated(hash); ok {
		t.Fatal("a compiled but unvalidated plan must not be executable")
	}
	binding.plans.markValidated(hash, validator.ValidationArtifact{QueryArtifactPlanHash: hash})
	if _, _, _, ok := binding.plans.getValidated(hash); !ok {
		t.Fatal("a validated plan must be executable within its own run")
	}
	// 未知计划不能通过 markValidated 凭空产生。
	unknown := askdata.HashBytes([]byte("unknown"))
	binding.plans.markValidated(unknown, validator.ValidationArtifact{})
	if _, _, _, ok := binding.plans.getValidated(unknown); ok {
		t.Fatal("validating an unknown plan must not create it")
	}
}

func TestExecutionSummaryUsesTheClosedResultContract(t *testing.T) {
	columns, metrics, err := summarizeCurrentResult(validator.ResultPlanContract{
		Role: compiler.QueryRoleCurrent,
		Columns: []validator.ResultColumn{
			{Name: "region", Role: "DIMENSION"},
			{Name: "sales_amount", Role: "METRIC"},
		},
	}, [][]any{
		{"华东", "10.25"},
		{"华东", "4.75"},
		{"华南", nil},
	})
	if err != nil {
		t.Fatalf("summarizeCurrentResult() error = %v", err)
	}
	if len(columns) != 2 || columns[0].DistinctCount != 2 || columns[1].NullCount != 1 {
		t.Fatalf("column summaries = %#v", columns)
	}
	if len(metrics) != 1 || metrics[0].Code != "sales_amount" || metrics[0].NonNullCount != 2 ||
		metrics[0].NullCount != 1 || metrics[0].Minimum != "19/4" ||
		metrics[0].Maximum != "41/4" || metrics[0].Sum != "15" {
		t.Fatalf("metric summaries = %#v", metrics)
	}
	known := map[askdata.ID]askdata.EvidenceRef{
		"77777777-7777-4777-8777-777777777777": {
			EvidenceID:  "77777777-7777-4777-8777-777777777777",
			Kind:        askdata.EvidenceKindQueryResult,
			SourceID:    "66666666-6666-4666-8666-666666666666",
			ContentHash: askdata.HashBytes([]byte("result-evidence")),
		},
	}
	result := toolhost.ExecuteQueryPlanResult{
		ResultHash:       askdata.HashBytes([]byte("result")),
		VerificationHash: askdata.HashBytes([]byte("verification")),
		Verdict:          executionVerdict(3), RowCount: 3,
		Columns: columns, Metrics: metrics,
		EvidenceIDs: []askdata.ID{"77777777-7777-4777-8777-777777777777"},
	}
	if result.Verdict != "PASS" || result.ValidateResult(known) != nil {
		t.Fatalf("closed execution result rejected: %#v", result)
	}
}

func TestEmptyExecutionIsAConfirmedPassingResult(t *testing.T) {
	columns, metrics, err := summarizeCurrentResult(validator.ResultPlanContract{
		Role:    compiler.QueryRoleCurrent,
		Columns: []validator.ResultColumn{{Name: "sales_amount", Role: "METRIC"}},
	}, nil)
	if err != nil || len(columns) != 1 || len(metrics) != 1 ||
		metrics[0].NonNullCount != 0 || metrics[0].NullCount != 0 || executionVerdict(0) != "PASS" {
		t.Fatalf("empty result summary = %#v, %#v, %v", columns, metrics, err)
	}
}

type stubProvider struct {
	configured bool
	calls      int
}

func (provider *stubProvider) Configured() bool { return provider.configured }

func (provider *stubProvider) Invoke(
	context.Context, ai.Invocation,
) (ai.InvocationResult, error) {
	provider.calls++
	return ai.InvocationResult{}, errors.New("stub provider does not answer")
}

// 未配置模型提供方时必须报 ErrCognitionUnavailable，并且绝不能调用 Provider。
// 问数无法在没有模型的情况下从自然语言走到受治理查询，这里必须显式失败，
// 不能把「没有模型」表现成一次畸形的模型响应。
func TestCognitionRunnerFailsLoudlyWithoutAConfiguredProvider(t *testing.T) {
	if _, err := NewCognitionRunner(nil, cognition.ExecutorOptions{}); err != ErrCognitionUnavailable {
		t.Fatalf("nil provider = %v", err)
	}

	provider := &stubProvider{configured: false}
	runner, err := NewCognitionRunner(provider, cognition.ExecutorOptions{})
	if err != nil {
		t.Fatalf("NewCognitionRunner() error = %v", err)
	}
	if _, err := runner.Execute(context.Background(), cognition.RoundRequest{
		TenantID: "tenant-1", ActorID: "actor-1", Stage: cognition.StageUnderstanding,
		PromptVersion: "askdata-cognition-v1",
	}); err != ErrCognitionUnavailable {
		t.Fatalf("unconfigured provider = %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("unconfigured provider was invoked %d times", provider.calls)
	}
}

// 构造期就要绑定封闭动作协议：schema 无效时应在启动失败，
// 而不是等到每一次提问才失败。
func TestCognitionRunnerBindsTheClosedActionProtocolAtConstruction(t *testing.T) {
	runner, err := NewCognitionRunner(&stubProvider{configured: true}, cognition.ExecutorOptions{})
	if err != nil {
		t.Fatalf("NewCognitionRunner() error = %v", err)
	}
	if runner.executor == nil {
		t.Fatal("runner has no cognition executor")
	}
}

// Assembler 必须每个 Run 建一个新的 Binding：计划哈希是 Run 内作用域的，
// 复用 Binding 会让一个 Run 能校验或执行另一个 actor 策略范围下编译的计划。
func TestAssemblerBuildsAFreshBindingPerRun(t *testing.T) {
	runner, err := NewCognitionRunner(&stubProvider{configured: true}, cognition.ExecutorOptions{})
	if err != nil {
		t.Fatalf("NewCognitionRunner() error = %v", err)
	}
	assembler, err := NewAssembler(Services{}, runner, orchestrator.LoopOptions{})
	if err != nil {
		t.Fatalf("NewAssembler() error = %v", err)
	}
	run := testRun(t)
	first, authorization, err := assembler.Assemble(context.Background(), orchestrator.RunAssembly{
		Scope: run.Scope, DomainID: run.DomainID, RunID: run.RunID,
	})
	if err != nil || first == nil {
		t.Fatalf("Assemble() = %v, %v", first, err)
	}
	if authorization.Validate() != nil {
		t.Fatalf("assembled authorization is invalid: %#v", authorization)
	}
	if authorization.DomainID != run.DomainID || authorization.Scope.Release != run.Scope.Release {
		t.Fatal("authorization must carry the run's own domain and pinned release")
	}
	second, _, err := assembler.Assemble(context.Background(), orchestrator.RunAssembly{
		Scope: run.Scope, DomainID: run.DomainID, RunID: run.RunID,
	})
	if err != nil || second == first {
		t.Fatal("each assembly must produce its own Loop over a fresh binding")
	}
}

// 没有认知运行器就无法组装：问数不能在没有模型的情况下执行。
func TestAssemblerRequiresACognitionRunner(t *testing.T) {
	if _, err := NewAssembler(Services{}, nil, orchestrator.LoopOptions{}); err != ErrCognitionUnavailable {
		t.Fatalf("assembler without cognition = %v", err)
	}
}

type stubDictionary struct {
	result understanding.DictionaryMatchResult
	err    error
	calls  int
}

func (dictionary *stubDictionary) Match(
	context.Context, understanding.DictionaryMatchRequest,
) (understanding.DictionaryMatchResult, error) {
	dictionary.calls++
	return dictionary.result, dictionary.err
}

// 没有配置词典时检索照常进行：词典是精确度辅助，缺失只影响召回质量，
// 不影响正确性，不能让问题失败。
func TestRetrievalWorksWithoutADictionary(t *testing.T) {
	binding, err := NewBinding(Services{}, testRun(t))
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	hits, refs := binding.dictionaryHits(context.Background(), "销售额")
	if hits != nil || refs != nil {
		t.Fatal("a missing dictionary must contribute nothing, not fail")
	}
}

// 词典报错同样只降级，不冒泡成问题失败。
func TestDictionaryFailureDegradesInsteadOfFailingTheQuestion(t *testing.T) {
	dictionary := &stubDictionary{err: errors.New("dictionary unavailable")}
	binding, err := NewBinding(Services{Dictionary: dictionary}, testRun(t))
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	hits, refs := binding.dictionaryHits(context.Background(), "销售额")
	if hits != nil || refs != nil {
		t.Fatal("a failing dictionary must degrade silently")
	}
	if dictionary.calls != 1 {
		t.Fatalf("dictionary was consulted %d times", dictionary.calls)
	}
}

// 空 mention 不查词典，避免无意义的加载与缓存扰动。
func TestDictionaryIsNotConsultedForAnEmptyMention(t *testing.T) {
	dictionary := &stubDictionary{}
	binding, err := NewBinding(Services{Dictionary: dictionary}, testRun(t))
	if err != nil {
		t.Fatalf("NewBinding() error = %v", err)
	}
	binding.dictionaryHits(context.Background(), "   ")
	if dictionary.calls != 0 {
		t.Fatal("dictionary must not be consulted for a blank mention")
	}
}

// 凡是被转换送进检索层的类型，检索层都必须受理。
//
// 两个枚举各自独立声明，既不同名（TERM / BUSINESS_TERM），历史成员也不同
// （MEASURE / METRIC）。跨边界做字符串强转时，检索层会以
// ErrInvalidRetrieval 整体拒绝请求——模型传了一个参数校验放行的合法值，
// 却换来一次工具失败。
func TestEveryAcceptedObjectTypeSurvivesTheRetrievalBoundary(t *testing.T) {
	for _, accepted := range []toolhost.ObjectType{
		toolhost.ObjectTypeMetric, toolhost.ObjectTypeDimension,
		toolhost.ObjectTypeModel, toolhost.ObjectTypeTerm,
	} {
		for _, converted := range mustConvert(t, accepted) {
			if !search.ValidRetrievalObjectType(converted) {
				t.Errorf(
					"objectType %q maps to retrieval type %q, which the retriever rejects",
					accepted, converted,
				)
			}
		}
	}
}

func mustConvert(t *testing.T, value toolhost.ObjectType) []search.ObjectType {
	t.Helper()
	converted, _ := retrievalObjectTypes([]toolhost.ObjectType{value})
	return converted
}

func TestToolObjectTypesCoverModelsAndLegacyMeasures(t *testing.T) {
	converted, ok := retrievalObjectTypes([]toolhost.ObjectType{
		toolhost.ObjectTypeModel, toolhost.ObjectTypeMetric,
	})
	if !ok || len(converted) != 3 || converted[0] != search.ObjectSemanticModel ||
		converted[1] != search.ObjectMetric || converted[2] != search.ObjectMeasureLegacy {
		t.Fatalf("mixed retrieval = (%v, %v)", converted, ok)
	}
	for _, fixture := range []struct {
		input search.ObjectType
		want  toolhost.ObjectType
	}{
		{search.ObjectMetric, toolhost.ObjectTypeMetric},
		{search.ObjectMeasureLegacy, toolhost.ObjectTypeMetric},
		{search.ObjectSemanticModel, toolhost.ObjectTypeModel},
	} {
		got, mapped := toolObjectType(fixture.input)
		if !mapped || got != fixture.want {
			t.Fatalf("toolObjectType(%s) = (%s, %t)", fixture.input, got, mapped)
		}
	}
}

func TestSearchEvidenceIDsCoverEverySanitizedReference(t *testing.T) {
	first := askdata.EvidenceRef{
		EvidenceID: "evidence-z", Kind: askdata.EvidenceKindCandidateSet,
		SourceID: "release-1", ContentHash: askdata.HashBytes([]byte("z")),
	}
	second := askdata.EvidenceRef{
		EvidenceID: "evidence-a", Kind: askdata.EvidenceKindVectorMatch,
		SourceID: "metric-1", ContentHash: askdata.HashBytes([]byte("a")),
	}
	ids := sortedEvidenceIDs([]askdata.EvidenceRef{first, second, first})
	if len(ids) != 2 || ids[0] != second.EvidenceID || ids[1] != first.EvidenceID {
		t.Fatalf("sortedEvidenceIDs() = %v", ids)
	}
	known := map[askdata.ID]askdata.EvidenceRef{
		first.EvidenceID: first, second.EvidenceID: second,
	}
	result := toolhost.SearchSemanticObjectsResult{
		Candidates: []toolhost.CandidateSummary{{
			ObjectType: toolhost.ObjectTypeMetric, ObjectVersionID: "metric-1",
			Score: 0.5, MatchType: "VECTOR", Status: "CERTIFIED",
		}},
		EvidenceIDs: ids,
	}
	if err := result.ValidateResult(known); err != nil {
		t.Fatalf("ValidateResult() error = %v", err)
	}
}

func TestPhysicalMeasureBecomesAQuestionMetricContract(t *testing.T) {
	row := registry.ContractRow{
		ObjectType: "MEASURE", ObjectVersionID: "measure-sales-v1",
		ContentHash: askdata.HashBytes([]byte("measure")), OwnerID: "owner-1",
		Status:   "CERTIFIED",
		Contract: json.RawMessage(`{"code":"sales_amount","name":"销售金额","unit":"CNY"}`),
	}
	summary, ok := contractSummary(row)
	if !ok || summary.ObjectType != toolhost.ObjectTypeMetric || summary.Name != "销售金额" ||
		summary.Definition != "销售金额" || summary.Unit != "CNY" {
		t.Fatalf("contractSummary(MEASURE) = (%+v, %t)", summary, ok)
	}
	summary.ContentHash = row.ContentHash
	evidence := askdata.EvidenceRef{
		EvidenceID: "measure-contract", Kind: askdata.EvidenceKindSemanticContract,
		SourceID: summary.ObjectVersionID, ContentHash: row.ContentHash,
	}
	result := toolhost.GetSemanticContractsResult{
		Contracts:   []toolhost.SemanticContractSummary{summary},
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	if err := result.ValidateResult(map[askdata.ID]askdata.EvidenceRef{evidence.EvidenceID: evidence}); err != nil {
		t.Fatalf("ValidateResult() error = %v", err)
	}
}

// 会放大行数的连接边必须在检索阶段就报成阻断风险。
//
// 编译器现在能适配连接路径，但拒绝扇出边：一对多/多对多需要先把右侧预聚合
// 或经桥表去重，否则每个度量值都会被放大且毫无报错。把这件事提前报出来，
// 跨模型问题就会带着明确原因在检索阶段失败，而不是等到 compile 时预算已尽。
func TestFanoutBearingJoinStepIsBlocking(t *testing.T) {
	plan := graph.GraphPlan{JoinPaths: []graph.JoinPath{{
		Allowed: true,
		Steps: []graph.JoinStep{{
			Hop: 1, RelationshipVersionID: "50000000-0000-4000-8000-000000000005",
			Cardinality:  registry.CardinalityOneToMany,
			FanoutPolicy: registry.FanoutPreAggregateRequired,
		}},
	}}}
	relationships, risks := graphPlanRisks(plan, false, "")
	if len(relationships) != 1 {
		t.Fatalf("relationships = %v", relationships)
	}
	blocking := map[string]bool{}
	for _, risk := range risks {
		blocking[risk.Code] = risk.Blocking
	}
	if !blocking[JoinFanoutRiskCode] {
		t.Fatalf("a fanout-bearing join step must be blocking, got %+v", risks)
	}
}

// 安全的多对一连接现在是可编译的，不能再报成风险，否则这道闸会把最常见的
// 「事实表 + 维表」问题一起挡掉。
func TestSafeManyToOneJoinCarriesNoFanoutRisk(t *testing.T) {
	plan := graph.GraphPlan{JoinPaths: []graph.JoinPath{{
		Allowed: true,
		Steps: []graph.JoinStep{{
			Hop: 1, RelationshipVersionID: "50000000-0000-4000-8000-000000000005",
			Cardinality: registry.CardinalityManyToOne, FanoutPolicy: registry.FanoutSafe,
		}},
	}}}
	_, risks := graphPlanRisks(plan, false, "")
	for _, risk := range risks {
		if risk.Code == JoinFanoutRiskCode {
			t.Fatalf("a safe many-to-one join reported a fanout risk: %+v", risks)
		}
	}
}

// 风险列表会被哈希进证据，顺序必须稳定。
func TestGraphRisksAreEmittedInDeterministicOrder(t *testing.T) {
	plan := graph.GraphPlan{JoinPaths: []graph.JoinPath{{
		Allowed: false,
		Steps: []graph.JoinStep{{
			Hop: 1, RelationshipVersionID: "50000000-0000-4000-8000-000000000005",
		}},
	}}}
	_, first := graphPlanRisks(plan, false, "")
	for attempt := 0; attempt < 8; attempt++ {
		_, next := graphPlanRisks(plan, false, "")
		if len(next) != len(first) {
			t.Fatalf("risk count changed between runs: %d vs %d", len(next), len(first))
		}
		for index := range first {
			if next[index] != first[index] {
				t.Fatalf("risk order changed between runs: %+v vs %+v", next, first)
			}
		}
	}
}
