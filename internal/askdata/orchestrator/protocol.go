package orchestrator

import (
	"intelligent-report-generation-system/internal/askdata/cognition"
)

// AI-005 阶段协议。
//
// Loop 每次只执行一个认知阶段，因此必须有一张表回答两个问题：某个运行状态下
// 该跑哪个阶段，以及模型返回的动作把运行推进到哪个状态。这张表把三处既有约束
// 连接起来，不新增任何权限，也不放宽任何门禁：
//
//   - askdata.valid_question_run_transition（迁移 000247）决定哪些迁移合法；
//   - allowedToolsForCognitionStage 决定每个阶段能调用哪些工具；
//   - cognition.stageAllowsAction 决定每个阶段能返回哪些动作。
//
// 本表只是它们的交集，任何一处收紧都会自动收紧这里。

// StageForState returns the cognition stage that drives a run state, and false
// for states the deterministic layer owns. Deterministic states never invoke a
// model.
//
// What "deterministic" means here is worth stating precisely, because the state
// names invite a stronger reading than the code delivers. GRAPH_VALIDATING,
// PLAN_VALIDATING and EXECUTING do not themselves resolve a graph, validate a
// plan or run a query: that work is done by the Tool Host, during the
// model-driven stages that precede them, by code the model can invoke but not
// substitute for. These states are where the platform *commits* to what those
// tools produced — each one owns a link of the governed hash chain
// (governed_chain.go), and Apply refuses a link that first appears anywhere
// else. A model that skipped the work reaches ANSWERED with an incomplete
// chain, and the transition fails closed.
//
// So the guarantee is not "the platform re-does the work here"; it is "the
// platform will not certify work that no tool actually performed."
func StageForState(state State) (cognition.Stage, bool) {
	stage, ok := stageByState[state]
	return stage, ok
}

var stageByState = map[State]cognition.Stage{
	StateUnderstanding:   cognition.StageUnderstanding,
	StateRetrieving:      cognition.StageCandidateJudgment,
	StateBinding:         cognition.StageDisambiguation,
	StateIRReady:         cognition.StagePlanSelection,
	StateResultVerifying: cognition.StageAnomalyAnalysis,
	StateAnswerVerifying: cognition.StageResultVerification,
}

// NextState maps a cognition decision onto the run state it drives.
//
// CALL_TOOL never advances: tool calls consume budget inside the current state.
// A proposal advances one step, but only after the deterministic layer has
// accepted it — a rejected proposal is treated as BLOCK by the caller, so the
// model cannot advance a run by asserting something the platform did not verify.
func NextState(current State, action cognition.ActionType) (State, bool) {
	switch action {
	case cognition.ActionCallTool:
		return current, true
	case cognition.ActionClarify:
		// Every model-driven state can clarify. Migration 000301 widened the
		// state graph to allow IR_READY -> CLARIFICATION_REQUIRED, resolving the
		// contradiction where PLAN_SELECTION permitted CLARIFY but the lifecycle
		// had no way to represent it. A clarification is a better outcome than a
		// block whenever the user could actually resolve the ambiguity.
		if clarifiableStates[current] {
			return StateClarificationRequired, true
		}
		// Deterministic states have no model to clarify with; failing closed here
		// keeps the protocol from ever emitting a transition the trigger rejects.
		return StateBlocked, true
	case cognition.ActionBlock:
		return StateBlocked, true
	}
	next, ok := advanceByState[current]
	if !ok || next.action != action {
		return "", false
	}
	return next.state, true
}

// clarifiableStates lists the states askdata.valid_question_run_transition
// allows to reach CLARIFICATION_REQUIRED. It must stay in step with the SQL
// state graph; TestClarifiableStatesMatchTheStateGraph pins the two together.
var clarifiableStates = map[State]bool{
	StateUnderstanding:   true,
	StateRetrieving:      true,
	StateBinding:         true,
	StateGraphValidating: true,
	StateIRReady:         true,
	StatePlanValidating:  true,
	StateResultVerifying: true,
	StateAnswerVerifying: true,
}

type stageAdvance struct {
	action cognition.ActionType
	state  State
}

var advanceByState = map[State]stageAdvance{
	StateUnderstanding:   {cognition.ActionProposeUnderstanding, StateRetrieving},
	StateRetrieving:      {cognition.ActionProposeBinding, StateBinding},
	StateBinding:         {cognition.ActionProposeBinding, StateGraphValidating},
	StateIRReady:         {cognition.ActionProposePlan, StatePlanValidating},
	StateResultVerifying: {cognition.ActionAnalyzeAnomaly, StateAnswerVerifying},
	StateAnswerVerifying: {cognition.ActionFinalize, StateAnswered},
}

// DeterministicNextState gives the successor for states the platform advances
// without a model. They are listed explicitly rather than derived so that a new
// state cannot silently become "model-driven" by omission.
func DeterministicNextState(current State) (State, bool) {
	next, ok := deterministicNext[current]
	return next, ok
}

var deterministicNext = map[State]State{
	StateReceived:        StateAuthorized,
	StateAuthorized:      StateContextReady,
	StateContextReady:    StateUnderstanding,
	StateGraphValidating: StateIRReady,
	StatePlanValidating:  StateExecuting,
	StateExecuting:       StateResultVerifying,
}

// MaxBoundedCorrections caps how many times one run may retreat to BINDING.
//
// The state graph permits PLAN_VALIDATING → BINDING and RESULT_VERIFYING →
// BINDING, and the Loop budget caps total calls but not retreats, so without a
// separate cap a run could ping-pong between binding and validation until its
// whole budget is gone and still produce nothing. Exceeding this is a BLOCK.
const MaxBoundedCorrections = 2

// IsBoundedCorrection reports whether a transition is one of the two governed
// retreats rather than forward progress.
func IsBoundedCorrection(from, to State) bool {
	return to == StateBinding &&
		(from == StatePlanValidating || from == StateResultVerifying)
}
