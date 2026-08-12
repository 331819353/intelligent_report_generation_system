package orchestrator

import (
	"encoding/json"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

// governedChain accumulates the six hashes that make a run answerable.
//
// Run.Hashes.completeAnswerChain gates StateAnswered, so these are not
// bookkeeping: a run reaches ANSWERED only if every link was produced. That is
// what makes the deterministic layer real. GRAPH_VALIDATING, PLAN_VALIDATING and
// EXECUTING are advanced without a model, and if the platform records nothing
// while passing through them, "the query was validated and executed" rests on
// the model's say-so. Sourcing each link from the tool that actually did the
// work means a run that skipped a step cannot be finalised.
//
// Every link therefore comes from a deterministic producer:
//
//   - understanding / binding are the governed action hashes cognition itself
//     computed over the proposal, not text the model chose;
//   - graph plan, query plan and result are read out of the Tool Host responses
//     of resolve_graph_plan, compile_semantic_query and execute_query_plan.
//
// A model that claims to have executed a query without calling the tool leaves
// Result empty, and the answer transition fails closed.
type governedChain struct {
	understanding askdata.ContentHash
	binding       askdata.ContentHash
	graphPlan     askdata.ContentHash
	semanticIR    askdata.ContentHash
	queryPlan     askdata.ContentHash
	result        askdata.ContentHash
}

// observe folds one completed stage into the chain. Links are write-once: the
// first deterministic producer wins, so a later round cannot restate a hash the
// platform already committed to. Apply enforces the same rule durably; keeping
// it here too means the worker never even proposes an overwrite.
func (chain *governedChain) observe(decision cognition.RoundResult, executions []toolhost.Execution) {
	for _, execution := range executions {
		response := execution.Response
		if response.Status != toolhost.ResponseSuccess {
			continue
		}
		switch response.Tool {
		case toolhost.ToolResolveGraphPlan:
			var result toolhost.ResolveGraphPlanResult
			if json.Unmarshal(response.Result, &result) == nil {
				setHashOnce(&chain.graphPlan, result.GraphPlanHash)
			}
		case toolhost.ToolCompileSemanticQuery:
			var result toolhost.CompileSemanticQueryResult
			if json.Unmarshal(response.Result, &result) == nil {
				setHashOnce(&chain.semanticIR, result.SemanticIRHash)
				setHashOnce(&chain.queryPlan, result.PlanHash)
			}
		case toolhost.ToolExecuteQueryPlan:
			var result toolhost.ExecuteQueryPlanResult
			if json.Unmarshal(response.Result, &result) == nil {
				setHashOnce(&chain.result, result.ResultHash)
			}
		}
	}
	// A proposal is only a link once the stage that owns it actually produced
	// one; ActionHash is cognition's own canonical hash of the action.
	switch {
	case decision.Action.Understanding != nil:
		setHashOnce(&chain.understanding, decision.ActionHash)
	case decision.Action.BindingProposal != nil:
		setHashOnce(&chain.binding, decision.ActionHash)
	}
}

// updatesFor returns the links Apply will accept on a from -> to transition.
//
// Apply refuses a hash that first appears outside the state that owns it, so
// the chain is offered stage by stage rather than all at once. A link that is
// not yet available is simply omitted: the transition still succeeds, and the
// missing link resurfaces as a failed answer transition later, which is the
// honest place for it to fail.
func (chain governedChain) updatesFor(from, to State) HashUpdates {
	var updates HashUpdates
	offer := func(target **askdata.ContentHash, value askdata.ContentHash, owner State) {
		if value == "" || from != owner && to != owner {
			return
		}
		committed := value
		*target = &committed
	}
	offer(&updates.Understanding, chain.understanding, StateUnderstanding)
	offer(&updates.BindingBundle, chain.binding, StateBinding)
	offer(&updates.GraphPlan, chain.graphPlan, StateGraphValidating)
	offer(&updates.SemanticIR, chain.semanticIR, StateIRReady)
	offer(&updates.QueryPlan, chain.queryPlan, StatePlanValidating)
	offer(&updates.Result, chain.result, StateResultVerifying)
	return updates
}

// reset drops the links a bounded correction invalidates. Apply rejects a
// correction that still carries downstream hashes, and it is the same rule:
// retreating to BINDING means the graph plan, IR, query plan and result no
// longer describe this run.
func (chain *governedChain) reset() {
	chain.binding = ""
	chain.graphPlan = ""
	chain.semanticIR = ""
	chain.queryPlan = ""
	chain.result = ""
}

func setHashOnce(target *askdata.ContentHash, value askdata.ContentHash) {
	if *target != "" || value.Validate() != nil {
		return
	}
	*target = value
}

// Schema versions for the completion artifacts the worker itself authors. They
// are stable identifiers: the read API dispatches on them, so changing one is a
// contract change, not a rename.
const (
	RunBlockSchemaVersion         = "question-run-block-v1"
	RunClarificationSchemaVersion = "question-run-clarification-v1"
)

// ErrAnswerVerificationUnavailable marks a FINALIZE the worker refuses to
// terminalise on its own.
//
// Answered runs must carry a narrative that passed fact verification against
// the Result Artifact, and that is AnswerVerificationRunner's boundary, not the
// worker's. Minting an ANSWER artifact here out of the model's FinalDecision
// would put unverified prose in the one field users read as the answer, which
// is precisely what the narrative gate exists to prevent. Failing closed is
// worse product behaviour and better safety, and it is visible in the audit
// stream rather than silent.
var ErrAnswerVerificationUnavailable = errors.New(
	"answer verification runner is not wired into the question run worker",
)

// completionForDecision builds the completion artifact a terminal transition
// requires. Nonterminal targets take none, and a terminal target without a
// matching payload is a protocol violation rather than an empty artifact.
func completionForDecision(
	target State, decision cognition.RoundResult,
) (*CompletionArtifactInput, error) {
	if completionArtifactType(target) == "" {
		return nil, nil
	}
	action := decision.Action
	switch {
	case target == StateClarificationRequired && action.Clarification != nil:
		// Store the public clarification contract flat. The cognition payload uses
		// `question` for schema ergonomics, but that key is intentionally reserved
		// by the append-only audit policy for retained raw user questions.
		payload, err := json.Marshal(map[string]any{
			"conflictCode":          action.Clarification.ConflictCode,
			"clarificationQuestion": action.Clarification.Question,
			"options":               action.Clarification.Options,
		})
		if err != nil {
			return nil, err
		}
		code := upperCompletionCode(action.Clarification.ConflictCode, "QUESTION_CLARIFICATION_REQUIRED")
		return &CompletionArtifactInput{
			Code: code, Type: ArtifactClarification,
			SchemaVersion: RunClarificationSchemaVersion,
			EvidenceIDs:   evidenceIDsOf(action.EvidenceRefs), Payload: payload,
		}, nil
	case target == StateBlocked && action.Block != nil:
		payload, err := json.Marshal(map[string]any{
			"code": action.Block.Code, "publicMessage": action.Block.PublicMessage,
		})
		if err != nil {
			return nil, err
		}
		return &CompletionArtifactInput{
			Code: upperCompletionCode(action.Block.Code, "QUESTION_BLOCKED"),
			Type: ArtifactBlock, SchemaVersion: RunBlockSchemaVersion,
			EvidenceIDs: evidenceIDsOf(action.EvidenceRefs), Payload: payload,
		}, nil
	case target == StateAnswered:
		return nil, ErrAnswerVerificationUnavailable
	}
	return nil, fmt.Errorf("terminal state %s has no matching decision payload", target)
}

func evidenceIDsOf(refs []askdata.EvidenceRef) []askdata.ID {
	result := make([]askdata.ID, 0, len(refs))
	for _, ref := range refs {
		result = append(result, ref.EvidenceID)
	}
	return result
}

// upperCompletionCode keeps codes inside completionCodePattern. A model-supplied
// code that does not fit the stable-code shape is replaced rather than passed
// through, so a malformed code cannot fail the terminal transition and strand
// the run.
func upperCompletionCode(value, fallback string) string {
	if completionCodePattern.MatchString(value) {
		return value
	}
	return fallback
}
