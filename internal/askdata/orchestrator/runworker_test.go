package orchestrator

import (
	"context"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

type recordingTransitioner struct {
	transitions []TransitionRequest
	run         Run
	err         error
}

func (store *recordingTransitioner) Transition(
	_ context.Context, request TransitionRequest,
) (TransitionResult, error) {
	store.transitions = append(store.transitions, request)
	if store.err != nil {
		return TransitionResult{}, store.err
	}
	next := store.run
	next.State = request.TargetState
	next.RecordVersion = request.ExpectedVersion + 1
	return TransitionResult{Run: next}, nil
}

func (store *recordingTransitioner) Resume(
	_ context.Context, _ ResumeRequest,
) (ReplaySnapshot, error) {
	return ReplaySnapshot{Run: store.run}, nil
}

type stubAssembler struct{ err error }

func (assembler stubAssembler) Assemble(
	_ context.Context, _ RunAssembly,
) (*Loop, toolhost.AuthorizationContext, error) {
	return nil, toolhost.AuthorizationContext{}, assembler.err
}

// Worker 选项必须在构造期校验：缺少身份、租约越界或阶段上限越界都不允许启动，
// 否则一个配置错误的 Worker 会在生产里静默领取运行。
func TestRunWorkerRejectsUnsafeOptions(t *testing.T) {
	leases := NewLeaseStore(nil)
	runs := &recordingTransitioner{}
	assembler := stubAssembler{}

	if _, err := NewRunWorker(nil, runs, assembler, DefaultRunWorkerOptions()); err == nil {
		t.Fatal("a worker without a lease store must be rejected")
	}
	if _, err := NewRunWorker(leases, nil, assembler, DefaultRunWorkerOptions()); err == nil {
		t.Fatal("a worker without a transitioner must be rejected")
	}
	if _, err := NewRunWorker(leases, runs, nil, DefaultRunWorkerOptions()); err == nil {
		t.Fatal("a worker without an assembler must be rejected")
	}

	base := DefaultRunWorkerOptions()
	base.WorkerID = "worker-1"
	for name, mutate := range map[string]func(*RunWorkerOptions){
		"no identity":     func(options *RunWorkerOptions) { options.WorkerID = "" },
		"lease too short": func(options *RunWorkerOptions) { options.Lease = time.Second },
		"lease too long":  func(options *RunWorkerOptions) { options.Lease = time.Hour },
		"no stage bound":  func(options *RunWorkerOptions) { options.MaxStages = 0 },
		"stage bound too high": func(options *RunWorkerOptions) {
			options.MaxStages = 1_000
		},
	} {
		broken := base
		mutate(&broken)
		if _, err := NewRunWorker(leases, runs, assembler, broken); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	if _, err := NewRunWorker(leases, runs, assembler, base); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
}

// 终态运行绝不能被再次写入：failClosedFrom 必须直接返回，
// 否则会撞上 enforce_question_run_lifecycle 的终态不可变约束。
func TestWorkerNeverWritesToATerminalRun(t *testing.T) {
	runs := &recordingTransitioner{}
	worker := testWorker(t, runs)
	claimed := testClaim()
	scope := testScope(t)

	for _, state := range []State{
		StateAnswered, StateBlocked, StateOutOfScope,
		StateClarificationRequired, StateClarificationExpired,
	} {
		if err := worker.failClosedFrom(context.Background(), scope, claimed, 1, state, BudgetUsage{},
			"CODE", "reason"); err != nil {
			t.Fatalf("fail-closed on terminal %s returned %v", state, err)
		}
	}
	if len(runs.transitions) != 0 {
		t.Fatalf("terminal runs were written %d times", len(runs.transitions))
	}
}

// 每一种异常退出都必须落到 BLOCKED 并带上可审计的原因码，
// 运行绝不允许静默停在中间状态。
func TestAbnormalExitsAlwaysBlockWithAnAuditableReason(t *testing.T) {
	runs := &recordingTransitioner{}
	worker := testWorker(t, runs)
	claimed := testClaim()
	scope := testScope(t)

	if err := worker.failClosedFrom(context.Background(), scope, claimed, 4, StateBinding, BudgetUsage{},
		"QUESTION_PROTOCOL_VIOLATION", "unexpected action"); err != nil {
		t.Fatalf("failClosedFrom() error = %v", err)
	}
	if len(runs.transitions) != 1 {
		t.Fatalf("expected exactly one transition, got %d", len(runs.transitions))
	}
	request := runs.transitions[0]
	if request.TargetState != StateBlocked {
		t.Fatalf("abnormal exit targeted %s, want BLOCKED", request.TargetState)
	}
	if request.Event.Stage != string(StateBlocked) || request.Event.Status != EventBlocked ||
		request.Event.Code != "QUESTION_PROTOCOL_VIOLATION" {
		t.Fatalf("abnormal exit lost its reason: %#v", request.Event)
	}
	if request.ExpectedVersion != 4 {
		t.Fatalf("abnormal exit must carry the observed version, got %d", request.ExpectedVersion)
	}
	if len(request.Event.Details) == 0 {
		t.Fatal("abnormal exit must record details")
	}
}

// 超长原因必须截断，避免把任意长度的内部错误写进审计事件。
func TestReasonsAreBounded(t *testing.T) {
	long := make([]byte, 4096)
	for index := range long {
		long[index] = 'x'
	}
	if bounded := boundedReason(string(long)); len(bounded) != 512 {
		t.Fatalf("reason length = %d, want 512", len(bounded))
	}
	if boundedReason("short") != "short" {
		t.Fatal("short reasons must pass through unchanged")
	}
}

// terminalState 必须覆盖状态图里全部五个终态，漏掉任何一个都会让
// Worker 在终态上继续写入。
func TestTerminalStateCoversEveryTerminal(t *testing.T) {
	for _, state := range []State{
		StateAnswered, StateBlocked, StateOutOfScope,
		StateClarificationRequired, StateClarificationExpired,
	} {
		if !terminalState(state) {
			t.Fatalf("%s must be treated as terminal", state)
		}
	}
	for _, state := range []State{
		StateReceived, StateUnderstanding, StateBinding,
		StateExecuting, StateAnswerVerifying,
	} {
		if terminalState(state) {
			t.Fatalf("%s must not be treated as terminal", state)
		}
	}
}

// applyingTransitioner is recordingTransitioner's honest counterpart: it runs
// the real state machine instead of accepting whatever it is handed. Any
// TransitionRequest the worker emits has to survive Apply, because that is
// exactly what PostgresStore.Transition does with it.
type applyingTransitioner struct {
	run   Run
	calls int
}

func (store *applyingTransitioner) Transition(
	_ context.Context, request TransitionRequest,
) (TransitionResult, error) {
	store.calls++
	var completion *CompletionRef
	if request.Completion != nil {
		// Mirror PostgresStore.Transition: it prepares the artifact, which is
		// what derives the hash, and only then hands Apply a CompletionRef.
		prepared, err := prepareCompletionArtifact(request, *request.Completion)
		if err != nil {
			return TransitionResult{}, err
		}
		completion = &CompletionRef{
			Code:         request.Completion.Code,
			ArtifactType: prepared.Type,
			ArtifactHash: prepared.Hash,
		}
	}
	next, err := Apply(store.run, Transition{
		ExpectedVersion: request.ExpectedVersion, TargetState: request.TargetState,
		Usage: request.Usage, Hashes: request.Hashes, Completion: completion,
	})
	if err != nil {
		return TransitionResult{}, err
	}
	store.run = next
	return TransitionResult{Run: next}, nil
}

func (store *applyingTransitioner) Resume(
	_ context.Context, _ ResumeRequest,
) (ReplaySnapshot, error) {
	return ReplaySnapshot{Run: store.run}, nil
}

// Worker 的失败关闭路径必须能真正落库。
//
// failClosedFrom 把运行推向 BLOCKED，而 BLOCKED 是终态；Apply 要求任何终态
// 迁移都带 Completion 工件。recordingTransitioner 不校验，所以既有用例看不到
// 这一点——真实的 PostgresStore 会直接拒绝，运行卡在原状态。
func TestFailClosedProducesATransitionTheStateMachineAccepts(t *testing.T) {
	claimed := testClaim()
	run := workerRun(t, claimed, StateUnderstanding)
	runs := &applyingTransitioner{run: run}
	worker := testWorker(t, runs)

	err := worker.failClosedFrom(
		context.Background(), testScope(t), claimed, run.RecordVersion,
		StateUnderstanding, run.Usage, "QUESTION_LOOP_FAILED", "loop failed",
	)
	if err != nil {
		t.Fatalf("fail-closed transition rejected by the state machine: %v", err)
	}
	if runs.run.State != StateBlocked {
		t.Fatalf("run state = %s, want BLOCKED", runs.run.State)
	}
}

func workerRun(t *testing.T, claimed LeasedRun, state State) Run {
	t.Helper()
	scope := testScope(t)
	return Run{
		ID: claimed.RunID, TenantID: claimed.TenantID, DomainID: claimed.DomainID,
		ActorID: claimed.ActorID, TraceID: "77777777-7777-4777-8777-777777777777",
		IdempotencyKeyHash: askdata.HashBytes([]byte("idempotency")),
		QuestionHash:       askdata.HashBytes([]byte("question")),
		PolicyScopeHash:    scope.PolicyHash,
		Release:            scope.Release,
		State:              state, Disposition: DispositionPending,
		Limits: DefaultBudgetLimits(), RecordVersion: 1,
	}
}

func testWorker(t *testing.T, runs RunTransitioner) *RunWorker {
	t.Helper()
	options := DefaultRunWorkerOptions()
	options.WorkerID = "worker-test"
	worker, err := NewRunWorker(NewLeaseStore(nil), runs, stubAssembler{}, options)
	if err != nil {
		t.Fatalf("NewRunWorker() error = %v", err)
	}
	return worker
}

func testClaim() LeasedRun {
	return LeasedRun{
		TenantID: "33333333-3333-4333-8333-333333333333",
		RunID:    "66666666-6666-4666-8666-666666666666",
		DomainID: "11111111-1111-4111-8111-111111111111",
		ActorID:  "44444444-4444-4444-8444-444444444444",
	}
}

func testScope(t *testing.T) askdata.PolicyScope {
	t.Helper()
	scope, err := askdata.NewPolicyScope(
		"33333333-3333-4333-8333-333333333333",
		"44444444-4444-4444-8444-444444444444",
		[]askdata.ID{"11111111-1111-4111-8111-111111111111"},
		[]askdata.ID{"55555555-5555-4555-8555-555555555555"},
		askdata.ReleaseRef{
			ReleaseID:   "22222222-2222-4222-8222-222222222222",
			ContentHash: askdata.HashBytes([]byte("release")),
		},
	)
	if err != nil {
		t.Fatalf("build scope: %v", err)
	}
	return scope
}
