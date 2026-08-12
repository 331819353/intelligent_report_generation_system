package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/cognition"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

func questionFactFor(t *testing.T, question string) GovernedFact {
	t.Helper()
	payload, err := json.Marshal(struct {
		Question string `json:"question"`
	}{Question: question})
	if err != nil {
		t.Fatal(err)
	}
	evidenceID := askdata.ID("30000000-0000-4000-8000-000000000003")
	fact, err := cognition.NewPromptFact(evidenceID, cognition.FactConversation, payload)
	if err != nil {
		t.Fatalf("NewPromptFact() error = %v", err)
	}
	return GovernedFact{Fact: fact, Evidence: askdata.EvidenceRef{
		EvidenceID: fact.EvidenceID, Kind: askdata.EvidenceKindConversation,
		SourceID: "40000000-0000-4000-8000-000000000004", ContentHash: fact.ContentHash,
	}}
}

// 明细导出类问题不是问数能回答的，必须在消耗模型调用前被判定为超范围，
// 并带上「去提取数申请」这类下一步动作，而不是进入 Loop 去绑一个绑不上的问题。
func TestDetailExportQuestionIsRefusedBeforeTheModelRuns(t *testing.T) {
	verdict, outOfScope := scopeVerdictFor([]GovernedFact{
		questionFactFor(t, "导出本月订单明细"),
	})
	if !outOfScope {
		t.Fatal("a detail export question must be classified out of scope")
	}
	if verdict.Reason != understanding.ScopeReasonDetailList {
		t.Fatalf("scope reason = %s", verdict.Reason)
	}
	if len(verdict.NextActions) == 0 {
		t.Fatal("an out-of-scope refusal must carry a next action")
	}
}

// 可回答的问题必须原样放行：这道门宁可放过，也不能把能回答的问题拒掉。
func TestAnswerableQuestionPassesTheScopeGate(t *testing.T) {
	if _, outOfScope := scopeVerdictFor([]GovernedFact{
		questionFactFor(t, "本月销售额是多少"),
	}); outOfScope {
		t.Fatal("an answerable question must not be refused")
	}
}

// 没有问题事实时同样放行，缺少上下文不是拒答的理由。
func TestMissingQuestionFactDoesNotRefuseTheRun(t *testing.T) {
	if _, outOfScope := scopeVerdictFor(nil); outOfScope {
		t.Fatal("a missing question fact must not refuse the run")
	}
}

// 超范围工件必须是问题读取接口认得的那一种：BLOCK + 范围裁定 schema，
// 否则 question.go 解析不出 scopeVerdict，用户只看到一次无解释的拒绝。
func TestScopeCompletionMatchesTheContractTheReadAPIParses(t *testing.T) {
	verdict, _ := scopeVerdictFor([]GovernedFact{questionFactFor(t, "导出本月订单明细")})
	completion, err := scopeCompletion(verdict)
	if err != nil {
		t.Fatalf("scopeCompletion() error = %v", err)
	}
	if completion.Type != ArtifactBlock ||
		completion.SchemaVersion != understanding.ScopeVerdictSchemaVersion {
		t.Fatalf("completion = %+v", completion)
	}
	if !completionCodePattern.MatchString(completion.Code) {
		t.Fatalf("completion code %q is not a stable code", completion.Code)
	}
	var payload any
	if json.Unmarshal(completion.Payload, &payload) != nil || !auditJSONSafe(payload) {
		t.Fatalf("scope payload is not audit-safe: %s", completion.Payload)
	}
}

// 范围裁定绝不能把原始问题写进终态工件——它是留存策略单独治理的对象。
func TestScopeArtifactNeverPersistsTheRawQuestion(t *testing.T) {
	const question = "导出本月订单明细"
	verdict, _ := scopeVerdictFor([]GovernedFact{questionFactFor(t, question)})
	completion, err := scopeCompletion(verdict)
	if err != nil {
		t.Fatalf("scopeCompletion() error = %v", err)
	}
	if strings.Contains(string(completion.Payload), question) {
		t.Fatalf("scope artifact persisted the raw question: %s", completion.Payload)
	}
}

// 超范围迁移必须被真正的状态机接受：UNDERSTANDING -> OUT_OF_SCOPE 是终态迁移，
// 缺完成工件就会被 Apply 拒绝，运行会卡在 UNDERSTANDING。
func TestOutOfScopeTransitionIsAcceptedByTheRealStateMachine(t *testing.T) {
	claimed := testClaim()
	run := workerRun(t, claimed, StateUnderstanding)
	runs := &applyingTransitioner{run: run}
	worker := testWorker(t, runs)

	verdict, outOfScope := scopeVerdictFor([]GovernedFact{questionFactFor(t, "导出本月订单明细")})
	if !outOfScope {
		t.Fatal("fixture question must be out of scope")
	}
	completion, err := scopeCompletion(verdict)
	if err != nil {
		t.Fatalf("scopeCompletion() error = %v", err)
	}
	if _, _, err = worker.advance(
		context.Background(), testScope(t), claimed, run.RecordVersion,
		StateUnderstanding, StateOutOfScope, "QUESTION_OUT_OF_SCOPE",
		&run.Usage, governedChain{}, completion,
	); err != nil {
		t.Fatalf("out-of-scope transition rejected: %v", err)
	}
	if runs.run.State != StateOutOfScope {
		t.Fatalf("run state = %s, want OUT_OF_SCOPE", runs.run.State)
	}
	if runs.run.Disposition != DispositionRefuse {
		t.Fatalf("disposition = %s, want REFUSE", runs.run.Disposition)
	}
}
