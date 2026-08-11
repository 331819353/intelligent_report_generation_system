package tools

import (
	"context"
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/toolhost"
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
	binding.plans.put(hash, compiler.QueryArtifact{PlanHash: hash})
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
	binding.plans.put(hash, compiler.QueryArtifact{PlanHash: hash})
	if _, _, ok := binding.plans.getValidated(hash); ok {
		t.Fatal("a compiled but unvalidated plan must not be executable")
	}
	binding.plans.markValidated(hash, validator.ValidationArtifact{QueryArtifactPlanHash: hash})
	if _, _, ok := binding.plans.getValidated(hash); !ok {
		t.Fatal("a validated plan must be executable within its own run")
	}
	// 未知计划不能通过 markValidated 凭空产生。
	unknown := askdata.HashBytes([]byte("unknown"))
	binding.plans.markValidated(unknown, validator.ValidationArtifact{})
	if _, _, ok := binding.plans.getValidated(unknown); ok {
		t.Fatal("validating an unknown plan must not create it")
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
