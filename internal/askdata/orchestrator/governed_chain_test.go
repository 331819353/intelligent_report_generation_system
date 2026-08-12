package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

func chainHash(character string) askdata.ContentHash {
	return askdata.ContentHash(strings.Repeat(character, 64))
}

func toolExecution(tool toolhost.ToolName, result any) toolhost.Execution {
	payload, _ := json.Marshal(result)
	return toolhost.Execution{
		Response: toolhost.Response{
			Tool: tool, Status: toolhost.ResponseSuccess, Result: payload,
		},
	}
}

// 受治理哈希链只能由真正执行过的工具产生。
//
// 这是「确定性层」落到实处的地方：GRAPH_VALIDATING / PLAN_VALIDATING /
// EXECUTING 都是无模型推进的状态，如果经过它们时平台什么都不记录，
// 「查询已校验、已执行」就只剩模型的一面之词。
func TestGovernedChainTakesItsLinksFromToolsNotFromTheModel(t *testing.T) {
	var chain governedChain
	chain.observe(cognition.RoundResult{}, []toolhost.Execution{
		toolExecution(toolhost.ToolResolveGraphPlan, toolhost.ResolveGraphPlanResult{
			GraphPlanHash: chainHash("3"),
		}),
		toolExecution(toolhost.ToolCompileSemanticQuery, toolhost.CompileSemanticQueryResult{
			SemanticIRHash: chainHash("4"), PlanHash: chainHash("5"),
		}),
		toolExecution(toolhost.ToolExecuteQueryPlan, toolhost.ExecuteQueryPlanResult{
			ResultHash: chainHash("6"),
		}),
	})
	if chain.graphPlan != chainHash("3") || chain.semanticIR != chainHash("4") ||
		chain.queryPlan != chainHash("5") || chain.result != chainHash("6") {
		t.Fatalf("chain did not absorb the tool hashes: %+v", chain)
	}
}

// 失败或被拒绝的工具调用不产生链接：否则一次被拒的执行也会让运行看起来「已执行」。
func TestGovernedChainIgnoresUnsuccessfulToolResponses(t *testing.T) {
	var chain governedChain
	execution := toolExecution(toolhost.ToolExecuteQueryPlan, toolhost.ExecuteQueryPlanResult{
		ResultHash: chainHash("6"),
	})
	for _, status := range []toolhost.ResponseStatus{
		toolhost.ResponseFailed, toolhost.ResponseRejected,
	} {
		execution.Response.Status = status
		chain.observe(cognition.RoundResult{}, []toolhost.Execution{execution})
		if chain.result != "" {
			t.Fatalf("%s response contributed a result hash", status)
		}
	}
}

// 链接一次写定：后续轮次不能改写平台已经承诺的哈希。
func TestGovernedChainLinksAreWriteOnce(t *testing.T) {
	var chain governedChain
	first := toolExecution(toolhost.ToolExecuteQueryPlan, toolhost.ExecuteQueryPlanResult{
		ResultHash: chainHash("6"),
	})
	second := toolExecution(toolhost.ToolExecuteQueryPlan, toolhost.ExecuteQueryPlanResult{
		ResultHash: chainHash("7"),
	})
	chain.observe(cognition.RoundResult{}, []toolhost.Execution{first})
	chain.observe(cognition.RoundResult{}, []toolhost.Execution{second})
	if chain.result != chainHash("6") {
		t.Fatalf("result hash was overwritten: %s", chain.result)
	}
}

// 每个哈希只能在拥有它的状态上首次出现，这是 Apply 的规则；
// updatesFor 必须按状态逐段提交，否则迁移会被状态机拒绝。
func TestGovernedChainOffersEachLinkOnlyToItsOwningState(t *testing.T) {
	chain := governedChain{
		understanding: chainHash("1"), binding: chainHash("2"), graphPlan: chainHash("3"),
		semanticIR: chainHash("4"), queryPlan: chainHash("5"), result: chainHash("6"),
	}
	if updates := chain.updatesFor(StateBinding, StateGraphValidating); updates.GraphPlan == nil ||
		updates.BindingBundle == nil || updates.Result != nil || updates.QueryPlan != nil {
		t.Fatalf("BINDING -> GRAPH_VALIDATING offered the wrong links: %+v", updates)
	}
	if updates := chain.updatesFor(StateExecuting, StateResultVerifying); updates.Result == nil ||
		updates.GraphPlan != nil || updates.SemanticIR != nil {
		t.Fatalf("EXECUTING -> RESULT_VERIFYING offered the wrong links: %+v", updates)
	}
}

// 有界纠错退回 BINDING 时，下游链接必须一起作废——Apply 会拒绝仍然携带
// 下游哈希的纠错迁移，而且那些哈希也确实不再描述这次运行。
func TestBoundedCorrectionDropsDownstreamLinks(t *testing.T) {
	chain := governedChain{
		understanding: chainHash("1"), binding: chainHash("2"), graphPlan: chainHash("3"),
		semanticIR: chainHash("4"), queryPlan: chainHash("5"), result: chainHash("6"),
	}
	chain.reset()
	if chain.understanding != chainHash("1") {
		t.Fatal("correction must keep the understanding link")
	}
	if chain.binding != "" || chain.graphPlan != "" || chain.semanticIR != "" ||
		chain.queryPlan != "" || chain.result != "" {
		t.Fatalf("correction kept downstream links: %+v", chain)
	}
}

// 没有真正执行过查询的运行，拿不出完整的受治理链，也就无法进入 ANSWERED。
// 这正是确定性门禁：模型声称查过数，但没调用工具时，运行必须失败关闭。
func TestRunThatNeverExecutedCannotCompleteTheAnswerChain(t *testing.T) {
	var chain governedChain
	chain.observe(cognition.RoundResult{}, []toolhost.Execution{
		toolExecution(toolhost.ToolResolveGraphPlan, toolhost.ResolveGraphPlanResult{
			GraphPlanHash: chainHash("3"),
		}),
		toolExecution(toolhost.ToolCompileSemanticQuery, toolhost.CompileSemanticQueryResult{
			SemanticIRHash: chainHash("4"), PlanHash: chainHash("5"),
		}),
	})
	hashes := RunHashes{
		Understanding: chainHash("1"), BindingBundle: chainHash("2"),
		GraphPlan: chain.graphPlan, SemanticIR: chain.semanticIR,
		QueryPlan: chain.queryPlan, Result: chain.result,
	}
	if hashes.completeAnswerChain() {
		t.Fatal("a run that never executed a query must not complete the answer chain")
	}
}

// FINALIZE 不能由 Worker 自行终结：ANSWERED 必须携带通过事实校验的叙述，
// 而那是 AnswerVerificationRunner 的边界。就地编造 ANSWER 工件会把未经校验的
// 文字写进用户当作答案来读的字段。
func TestFinalizeRefusesToMintAnAnswerArtifactInTheWorker(t *testing.T) {
	_, err := completionForDecision(StateAnswered, cognition.RoundResult{
		Action: cognition.Action{Action: cognition.ActionFinalize},
	})
	if err == nil {
		t.Fatal("FINALIZE must not produce a completion artifact in the worker")
	}
}

// 非终态迁移不带完成工件；Apply 会拒绝给非终态附加完成工件。
func TestNonTerminalTransitionsCarryNoCompletionArtifact(t *testing.T) {
	completion, err := completionForDecision(StateRetrieving, cognition.RoundResult{})
	if err != nil || completion != nil {
		t.Fatalf("nonterminal completion = (%v, %v)", completion, err)
	}
}

// 模型给出的 code 不符合稳定码形状时必须被替换，否则一个畸形 code 会让终态
// 迁移失败，把运行永久卡在中间状态。
func TestMalformedModelCodeDoesNotStrandTheRun(t *testing.T) {
	completion, err := completionForDecision(StateBlocked, cognition.RoundResult{
		Action: cognition.Action{
			Action: cognition.ActionBlock,
			Block:  &cognition.BlockDecision{Code: "not a stable code", PublicMessage: "no"},
		},
	})
	if err != nil {
		t.Fatalf("completionForDecision() error = %v", err)
	}
	if !completionCodePattern.MatchString(completion.Code) {
		t.Fatalf("completion code %q is not a stable code", completion.Code)
	}
}

// 澄清文案是平台生成给用户看的内容，不是原始用户问题；它必须以公开澄清
// 字段进入终态工件，不能撞上审计层为原始 question 保留的禁用字段。
func TestClarificationCompletionUsesAuditSafePublicFields(t *testing.T) {
	completion, err := completionForDecision(StateClarificationRequired, cognition.RoundResult{
		Action: cognition.Action{
			Action: cognition.ActionClarify,
			Clarification: &cognition.Clarification{
				ConflictCode: "METRIC_AMBIGUOUS",
				Question:     "请选择销售额口径。",
				Options: []toolhost.ClarificationOption{{
					OptionID: askdata.ID("10000000-0000-4000-8000-000000000001"),
					Label:    "含税销售额",
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("completionForDecision() error = %v", err)
	}
	var payload any
	if json.Unmarshal(completion.Payload, &payload) != nil || !auditJSONSafe(payload) {
		t.Fatalf("clarification payload is not audit-safe: %s", completion.Payload)
	}
}
