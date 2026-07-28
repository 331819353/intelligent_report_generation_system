package datasource

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMetadataAIRequestLogMessagesUseOnlyStableSafeSummaries(t *testing.T) {
	tests := []struct {
		status, code, wantLevel, wantText string
	}{
		{"RUNNING", "", "INFO", "正在处理"},
		{"SUCCEEDED", "", "SUCCESS", "已完成"},
		{"FAILED", "AI_INVALID_OUTPUT", "WARN", "结构校验"},
		{"FAILED", "AI_PROVIDER_TIMEOUT", "WARN", "调用超时"},
		{"FAILED", "UPSTREAM_SECRET_DETAIL", "ERROR", "调用失败"},
	}
	for _, test := range tests {
		level, message := metadataAIRequestLogMessage(3, test.status, test.code)
		if level != test.wantLevel || !strings.Contains(message, test.wantText) {
			t.Fatalf(
				"status=%q code=%q level=%q message=%q",
				test.status,
				test.code,
				level,
				message,
			)
		}
		if test.code != "" && strings.Contains(message, test.code) {
			t.Fatalf("safe log exposed internal error code: %q", message)
		}
	}
}

func TestMetadataJobLogJSONDoesNotExposeProviderPayloadFields(t *testing.T) {
	payload, err := json.Marshal(MetadataJobLog{
		Timestamp: "2026-07-27T03:00:00Z",
		Level:     "INFO",
		Stage:     "LLM",
		Message:   "模型调用 #1 正在处理",
		TableName: "员工列表导出",
		Model:     "MiniMax-M2",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"prompt",
		"sample",
		"response",
		"requestId",
		"inputHash",
		"raw",
	} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("log payload exposed forbidden field %q: %s", forbidden, payload)
		}
	}
}
