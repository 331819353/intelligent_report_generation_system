package datasourceai

import (
	"context"
	"encoding/json"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/datasource"
)

type sourceReaderStub struct{ source datasource.Source }

func (stub sourceReaderStub) Get(context.Context, string, string) (datasource.Source, error) {
	return stub.source, nil
}

type invokerStub struct {
	result aiplatform.InvocationResult
	input  aiplatform.Invocation
}

func (stub *invokerStub) Invoke(_ context.Context, input aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	stub.input = input
	return stub.result, nil
}

func modelResult(t *testing.T, value any) aiplatform.InvocationResult {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return aiplatform.InvocationResult{
		RequestID:      "request-1",
		ProviderResult: aiplatform.ProviderResult{Content: raw},
	}
}

func TestTurnRepairsHostAndDoesNotSendPassword(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{}
	invoker.result = modelResult(t, map[string]any{
		"reply": "配置已补齐，可以测试。",
		"draft": map[string]any{
			"code": "sales", "name": "销售库", "description": "",
			"type": "MYSQL", "host": "host.docker.internal", "port": 3306,
			"database": "sales", "username": "reader",
			"visibility": "PRIVATE", "sharingScope": "PRIVATE",
		},
		"suggestedAction": "TEST", "diagnosis": "", "suggestedChecks": []string{},
	})
	service := NewService(sourceReaderStub{}, invoker, 0)
	result, err := service.Turn(context.Background(), "tenant", "actor", "", TurnRequest{
		Instruction: "创建销售库", PasswordProvided: true,
		Draft: Draft{
			Code: "sales", Name: "销售库", Type: "MYSQL",
			Host: "http://localhost:3306/path", Database: "sales", Username: "reader",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReadyToTest || result.Draft.Host != "host.docker.internal" || result.Draft.Port != 3306 {
		t.Fatalf("unexpected repaired result: %+v", result)
	}
	if invoker.input.Purpose != aiplatform.PurposeDataSourceConfiguration {
		t.Fatalf("unexpected purpose %q", invoker.input.Purpose)
	}
	prompt := invoker.input.Request.Messages[1].Parts[0].Text
	if contains(prompt, "DataSource9!") || contains(prompt, `"password"`) {
		t.Fatal("password material must not enter the model prompt")
	}
}

func TestTurnReportsMissingSecurePassword(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{}
	invoker.result = modelResult(t, map[string]any{
		"reply": "请在安全输入框填写密码。",
		"draft": map[string]any{
			"code": "sales", "name": "销售库", "description": "", "type": "MYSQL",
			"host": "db.internal", "port": 3306, "database": "sales", "username": "reader",
			"visibility": "PRIVATE", "sharingScope": "PRIVATE",
		},
		"suggestedAction": "ASK", "diagnosis": "", "suggestedChecks": []string{},
	})
	result, err := NewService(sourceReaderStub{}, invoker, 0).Turn(
		context.Background(), "tenant", "actor", "",
		TurnRequest{Instruction: "继续", Draft: Draft{Type: "MYSQL"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReadyToTest || !containsString(result.MissingFields, "password") {
		t.Fatalf("expected password to remain missing: %+v", result)
	}
}

func contains(value, fragment string) bool {
	for index := 0; index+len(fragment) <= len(value); index++ {
		if value[index:index+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
