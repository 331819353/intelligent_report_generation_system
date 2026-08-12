package support

import "testing"

func TestCreateInputNormalize(t *testing.T) {
	input := CreateInput{
		ClientRequestID: "00000000-0000-4000-8000-000000000001",
		Category:        " system ", Priority: " high ", Subject: " 页面加载失败 ",
		Description: "进入数据集页面后持续显示接口错误。", PageURL: "/datasets?filter=mine", ErrorCode: "request_failed",
	}
	if err := input.normalize(); err != nil {
		t.Fatalf("normalize valid input: %v", err)
	}
	if input.Category != "SYSTEM" || input.Priority != "HIGH" || input.ErrorCode != "REQUEST_FAILED" {
		t.Fatalf("normalized input = %#v", input)
	}
}

func TestCreateInputRejectsExternalURLAndWeakContent(t *testing.T) {
	input := CreateInput{
		ClientRequestID: "00000000-0000-4000-8000-000000000001",
		Category:        "OTHER", Priority: "NORMAL", Subject: "太短", Description: "内容也太短",
		PageURL: "https://attacker.example/path",
	}
	if err := input.normalize(); err == nil {
		t.Fatal("expected invalid support ticket")
	}
}

func TestTransitionRequiresResolutionNote(t *testing.T) {
	input := TransitionInput{Status: "RESOLVED", RecordVersion: 1}
	if err := input.normalize(); err == nil {
		t.Fatal("expected missing resolution note to be rejected")
	}
	input.ResolutionNote = "已修复接口并完成回归。"
	if err := input.normalize(); err != nil {
		t.Fatalf("normalize valid transition: %v", err)
	}
}
