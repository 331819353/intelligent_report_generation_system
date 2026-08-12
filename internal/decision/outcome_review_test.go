package decision

import "testing"

func TestOutcomeReviewReadyRequiresTruthfulQualitativeFallback(t *testing.T) {
	if outcomeReviewReady(0, false, ConfirmOutcomeInput{Conclusion: ConclusionAchieved, Notes: "行动完成"}) {
		t.Fatal("metric-free review claimed an achieved outcome")
	}
	if outcomeReviewReady(0, false, ConfirmOutcomeInput{Conclusion: ConclusionInconclusive}) {
		t.Fatal("metric-free review omitted its evidence-gap note")
	}
	if !outcomeReviewReady(0, false, ConfirmOutcomeInput{Conclusion: ConclusionInconclusive, Notes: "缺少量化指标，下一周期补充跟踪。"}) {
		t.Fatal("truthful metric-free review was blocked")
	}
	if !outcomeReviewReady(1, true, ConfirmOutcomeInput{Conclusion: ConclusionAchieved}) {
		t.Fatal("ready metric-backed review was blocked")
	}
	if outcomeReviewReady(1, false, ConfirmOutcomeInput{Conclusion: ConclusionInconclusive, Notes: "刷新失败"}) {
		t.Fatal("configured metrics bypassed a failed refresh")
	}
}
