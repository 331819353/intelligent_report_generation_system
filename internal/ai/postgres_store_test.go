package ai

import (
	"errors"
	"strings"
	"testing"
)

func TestExceedsQuotaIncludesRunningReservationAtBoundary(t *testing.T) {
	tests := []struct {
		name                  string
		used, reserved, limit int64
		want                  bool
	}{
		{"额度内", 60, 40, 100, false},
		{"超过一单位", 61, 40, 100, true},
		{"单次预留已超限", 0, 101, 100, true},
		{"负数失败关闭", -1, 1, 100, true},
		{"避免加法溢出", 1<<63 - 2, 10, 1<<63 - 1, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := exceedsQuota(test.used, test.reserved, test.limit); got != test.want {
				t.Fatalf("exceedsQuota(%d,%d,%d)=%v，期望 %v", test.used, test.reserved, test.limit, got, test.want)
			}
		})
	}
}

func TestNormalizeStartRequestProtectsAuditBoundary(t *testing.T) {
	valid := StartRequest{
		TenantID: "550e8400-e29b-41d4-a716-446655440000", ActorID: "550e8400-e29b-41d4-a716-446655440001",
		Purpose: PurposeReportGeneration, PromptVersion: " report-v1 ", Provider: " provider ", Model: " model ",
		InputHash: strings.Repeat("a", 64), ResourceType: " REPORT ", ResourceID: " report-1 ",
		InputBytes: 1024, RedactionCount: 2, ReservedTokens: 2048, ReservedCostMicros: 300, MaxAttempts: 3,
	}
	normalized, err := normalizeStartRequest(valid)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Provider != "provider" || normalized.PromptVersion != "report-v1" || normalized.ResourceType != "REPORT" {
		t.Fatalf("字段未规范化：%#v", normalized)
	}

	invalid := valid
	invalid.Purpose = "UNSUPPORTED"
	if _, err := normalizeStartRequest(invalid); !errors.Is(err, ErrTenantAIForbidden) {
		t.Fatalf("未知用途应失败关闭，实际错误：%v", err)
	}
	invalid = valid
	invalid.InputHash = strings.Repeat("A", 64)
	if _, err := normalizeStartRequest(invalid); !errors.Is(err, ErrInvalidInvocation) {
		t.Fatalf("非规范摘要应被拒绝，实际错误：%v", err)
	}
}

func TestNormalizeTerminalRecordsRejectsUnsafeAuditValues(t *testing.T) {
	completion := CompletionRecord{Attempts: 2, PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15, CostMicros: 20, LatencyMS: 30}
	if _, err := normalizeCompletionRecord(completion); err != nil {
		t.Fatal(err)
	}
	completion.TotalTokens = 14
	if _, err := normalizeCompletionRecord(completion); !errors.Is(err, ErrInvalidInvocation) {
		t.Fatalf("不一致 Token 用量应被拒绝，实际错误：%v", err)
	}
	completion = CompletionRecord{ProviderRequestID: "request\nforged", Attempts: 1}
	if _, err := normalizeCompletionRecord(completion); !errors.Is(err, ErrInvalidInvocation) {
		t.Fatalf("带控制字符的 Provider 摘要应被拒绝，实际错误：%v", err)
	}
	if _, err := normalizeFailureRecord(FailureRecord{Attempts: 1, ErrorCode: "upstream said secret=abc", LatencyMS: 1}); !errors.Is(err, ErrInvalidInvocation) {
		t.Fatalf("错误正文不能进入稳定错误码字段，实际错误：%v", err)
	}
}
