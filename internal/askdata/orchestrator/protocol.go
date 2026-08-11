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
// model: authorization, graph validation, compilation, plan validation and
// execution are all decided by code, and a model has no say in them.
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
		// cognition.stageAllowsAction and the run state graph disagree here:
		// PLAN_SELECTION permits CLARIFY, but the state graph gives IR_READY only
		// PLAN_VALIDATING and BLOCKED. Emitting CLARIFICATION_REQUIRED there would
		// be rejected by enforce_question_run_lifecycle at write time. The
		// protocol resolves the conflict by failing closed: a clarification the
		// lifecycle cannot represent becomes a BLOCK, so the run ends with an
		// explicit reason instead of dying on a trigger.
		if clarifiableStates[current] {
			return StateClarificationRequired, true
		}
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
// actually allows to reach CLARIFICATION_REQUIRED. IR_READY is deliberately
// absent: the state graph makes it a narrow pass-through to plan validation.
var clarifiableStates = map[State]bool{
	StateUnderstanding:   true,
	StateRetrieving:      true,
	StateBinding:         true,
	StateGraphValidating: true,
	StatePlanValidating:  true,
	StateResultVerifying: true,
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
