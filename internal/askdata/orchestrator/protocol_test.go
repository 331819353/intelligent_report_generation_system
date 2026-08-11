package orchestrator

import (
	"testing"

	"intelligent-report-generation-system/internal/askdata/cognition"
)

// 协议表必须是既有约束的交集，不能自造权限：每个由模型驱动的状态，
// 其阶段必须允许该状态要求的推进动作，且必须有可用工具。
func TestProtocolIsTheIntersectionOfExistingConstraints(t *testing.T) {
	for state, advance := range advanceByState {
		stage, ok := StageForState(state)
		if !ok {
			t.Fatalf("state %s advances on a model action but has no cognition stage", state)
		}
		// 阶段必须允许这个推进动作，否则模型永远无法离开该状态。
		if !cognition.StageAllowsActionForTest(stage, advance.action) {
			t.Fatalf("stage %s does not permit %s, so state %s can never complete",
				stage, advance.action, state)
		}
		if len(allowedToolsForCognitionStage(stage)) == 0 {
			t.Fatalf("stage %s for state %s has no permitted tools", stage, state)
		}
	}
}

// 每个由模型驱动的状态都必须有推进动作；否则该阶段只能澄清、阻断或
// 反复调用工具直到预算耗尽——这正是 UNDERSTANDING 原本的缺陷。
func TestEveryModelDrivenStateHasAWayToComplete(t *testing.T) {
	for state := range stageByState {
		if _, ok := advanceByState[state]; !ok {
			t.Fatalf("state %s has a cognition stage but no completing action", state)
		}
	}
}

// 模型驱动与确定性推进必须互斥：一个状态不能既由模型决定又由平台自动推进。
func TestModelDrivenAndDeterministicStatesAreDisjoint(t *testing.T) {
	for state := range stageByState {
		if _, ok := deterministicNext[state]; ok {
			t.Fatalf("state %s is both model-driven and deterministic", state)
		}
	}
}

// 协议产出的每一条迁移都必须是状态图允许的，否则会在数据库触发器上失败。
func TestEveryProtocolTransitionIsPermittedByTheStateGraph(t *testing.T) {
	permitted := map[State][]State{
		StateReceived:        {StateAuthorized, StateBlocked},
		StateAuthorized:      {StateContextReady, StateBlocked},
		StateContextReady:    {StateUnderstanding, StateBlocked},
		StateUnderstanding:   {StateRetrieving, StateClarificationRequired, StateOutOfScope, StateBlocked},
		StateRetrieving:      {StateBinding, StateClarificationRequired, StateBlocked},
		StateBinding:         {StateGraphValidating, StateClarificationRequired, StateOutOfScope, StateBlocked},
		StateGraphValidating: {StateIRReady, StateClarificationRequired, StateBlocked},
		StateIRReady:         {StatePlanValidating, StateClarificationRequired, StateBlocked},
		StatePlanValidating:  {StateExecuting, StateBinding, StateClarificationRequired, StateBlocked},
		StateExecuting:       {StateResultVerifying, StateBlocked},
		StateResultVerifying: {StateAnswerVerifying, StateBinding, StateClarificationRequired, StateBlocked},
		StateAnswerVerifying: {StateAnswered, StateClarificationRequired, StateBlocked},
	}
	allows := func(from, to State) bool {
		for _, candidate := range permitted[from] {
			if candidate == to {
				return true
			}
		}
		return false
	}

	for from, advance := range advanceByState {
		if !allows(from, advance.state) {
			t.Fatalf("protocol advances %s -> %s which the state graph forbids", from, advance.state)
		}
	}
	for from, to := range deterministicNext {
		if !allows(from, to) {
			t.Fatalf("protocol advances %s -> %s which the state graph forbids", from, to)
		}
	}
	// 澄清与阻断产出的迁移必须都被状态图接受。迁移 000301 之后每个模型驱动状态
	// 都能澄清；确定性状态没有可澄清的对象，仍然失败关闭为 BLOCKED。
	for state := range stageByState {
		clarify, _ := NextState(state, cognition.ActionClarify)
		if !allows(state, clarify) {
			t.Fatalf("CLARIFY at %s produced illegal transition to %s", state, clarify)
		}
		if clarifiableStates[state] && clarify != StateClarificationRequired {
			t.Fatalf("state %s should clarify but produced %s", state, clarify)
		}
		if !clarifiableStates[state] && clarify != StateBlocked {
			t.Fatalf("state %s cannot clarify and must fail closed, got %s", state, clarify)
		}
		blocked, _ := NextState(state, cognition.ActionBlock)
		if blocked != StateBlocked || !allows(state, blocked) {
			t.Fatalf("state %s cannot reach BLOCKED", state)
		}
	}
}

// clarifiableStates 必须与状态图一致：声称可澄清的状态确实能到达
// CLARIFICATION_REQUIRED，未声称的确实不能。
func TestClarifiableStatesMatchTheStateGraph(t *testing.T) {
	graphAllowsClarify := map[State]bool{
		StateUnderstanding:   true,
		StateRetrieving:      true,
		StateBinding:         true,
		StateGraphValidating: true,
		StatePlanValidating:  true,
		StateResultVerifying: true,
		StateIRReady:         true,
		StateExecuting:       false,
		StateAnswerVerifying: true,
	}
	for state, expected := range graphAllowsClarify {
		if clarifiableStates[state] != expected {
			t.Fatalf("state %s clarifiable=%v, state graph says %v",
				state, clarifiableStates[state], expected)
		}
	}
}

// 工具调用消耗预算但绝不推进状态。
func TestToolCallsNeverAdvanceTheRun(t *testing.T) {
	for state := range stageByState {
		next, ok := NextState(state, cognition.ActionCallTool)
		if !ok || next != state {
			t.Fatalf("CALL_TOOL moved %s to %s", state, next)
		}
	}
}

// 用错阶段的推进动作必须被拒绝：模型不能靠返回别的阶段的动作跳过一步。
func TestAnActionFromTheWrongStageCannotAdvanceTheRun(t *testing.T) {
	if _, ok := NextState(StateUnderstanding, cognition.ActionFinalize); ok {
		t.Fatal("FINALIZE must not complete the understanding stage")
	}
	if _, ok := NextState(StateIRReady, cognition.ActionProposeBinding); ok {
		t.Fatal("PROPOSE_BINDING must not complete plan selection")
	}
	if _, ok := NextState(StateAnswerVerifying, cognition.ActionProposePlan); ok {
		t.Fatal("PROPOSE_PLAN must not finalize a run")
	}
}

// 有界纠错的识别必须精确：只有状态图上那两条回退边算纠错。
func TestBoundedCorrectionsAreExactlyTheTwoRetreatEdges(t *testing.T) {
	if !IsBoundedCorrection(StatePlanValidating, StateBinding) ||
		!IsBoundedCorrection(StateResultVerifying, StateBinding) {
		t.Fatal("the two governed retreats must be recognised")
	}
	if IsBoundedCorrection(StateRetrieving, StateBinding) {
		t.Fatal("forward progress into BINDING is not a correction")
	}
	if IsBoundedCorrection(StatePlanValidating, StateExecuting) {
		t.Fatal("forward progress out of PLAN_VALIDATING is not a correction")
	}
	if MaxBoundedCorrections < 1 {
		t.Fatal("bounded correction cap must permit at least one retreat")
	}
}

// 迁移 000301 之后，每个由模型驱动的状态都必须能真正走到 CLARIFICATION_REQUIRED。
// 这条断言防止 clarifiableStates 再次退化成失败关闭，从而悄悄吃掉一次
// 用户本可以回答的澄清机会。
func TestEveryModelDrivenStateCanActuallyClarify(t *testing.T) {
	for state := range stageByState {
		next, ok := NextState(state, cognition.ActionClarify)
		if !ok || next != StateClarificationRequired {
			t.Fatalf("model-driven state %s must be able to clarify, got %s", state, next)
		}
	}
}
