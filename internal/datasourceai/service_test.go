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
	err    error
	calls  int
}

func (stub *invokerStub) Invoke(_ context.Context, input aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	stub.calls++
	stub.input = input
	return stub.result, stub.err
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
		Instruction: "创建销售库，密码：DataSource9!", PasswordProvided: true,
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

func TestTurnParsesMarkdownConnectionDetailsLocally(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{}
	service := NewService(sourceReaderStub{}, invoker, 0)
	result, err := service.Turn(context.Background(), "tenant", "actor", "", TurnRequest{
		Instruction: `MySQL
| Host | ` + "`127.0.0.1`" + ` |
| Port | ` + "`3306`" + ` |
| Database/Service | ` + "`takeout_master`" + ` |
| Schema | ` + "`takeout_master`" + ` |
| 用户名 | ` + "`takeout_user`" + ` |
| 密码 | ` + "`TakeoutUser2026X`" + ` |`,
		PasswordProvided: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 0 {
		t.Fatalf("structured connection details should not call the model; got %d calls", invoker.calls)
	}
	if !result.ReadyToTest || result.Draft.Host != "host.docker.internal" ||
		result.Draft.Database != "takeout_master" || result.Draft.Username != "takeout_user" {
		t.Fatalf("unexpected local parse result: %+v", result)
	}
	if result.Draft.Name != "MYSQL_host.docker.internal:3306_takeout_master_takeout_user" ||
		result.Draft.Code != "ds_mysql_host_docker_internal_3306_takeout_master_takeout_user" {
		t.Fatalf("expected name and code to be derived from database: %+v", result.Draft)
	}
	if contains(result.Reply, "TakeoutUser2026X") {
		t.Fatal("password material must not enter the assistant reply")
	}
}

func TestTurnGeneratesDatabaseNameInsteadOfAsking(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{}
	invoker.result = modelResult(t, map[string]any{
		"reply": "请提供这个数据源的名称和 code。",
		"draft": map[string]any{
			"code": "", "name": "", "description": "", "type": "ORACLE",
			"host": "host.docker.internal", "port": 11521, "database": "FREEPDB1", "username": "TAKEOUT_USER",
			"visibility": "PRIVATE", "sharingScope": "PRIVATE",
		},
		"suggestedAction": "ASK", "diagnosis": "", "suggestedChecks": []string{},
	})
	result, err := NewService(sourceReaderStub{}, invoker, 0).Turn(
		context.Background(), "tenant", "actor", "", TurnRequest{
			Instruction:      "Oracle 主机 127.0.0.1 端口 11521 服务 FREEPDB1 用户 TAKEOUT_USER",
			PasswordProvided: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReadyToTest || containsString(result.MissingFields, "name") || containsString(result.MissingFields, "code") {
		t.Fatalf("generated identity must not be requested: %+v", result)
	}
	if result.Draft.Name != "ORACLE_host.docker.internal:11521_FREEPDB1_TAKEOUT_USER" ||
		result.Draft.Code != "ds_oracle_host_docker_internal_11521_freepdb1_takeout_user" {
		t.Fatalf("unexpected generated identity: %+v", result.Draft)
	}
	if contains(result.Reply, "请提供") || contains(result.Reply, "名称和 code") {
		t.Fatalf("assistant must not ask for generated identity: %q", result.Reply)
	}
}

func TestTurnParsesNaturalOracleConnectionWithoutModel(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{}
	result, err := NewService(sourceReaderStub{}, invoker, 0).Turn(
		context.Background(), "tenant", "actor", "", TurnRequest{
			Instruction:      "Oracle 数据源，主机 127.0.0.1，端口 11521，服务名 FREEPDB1，用户名 TAKEOUT_USER。",
			PasswordProvided: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 0 {
		t.Fatalf("deterministic natural connection details should not call the model; got %d calls", invoker.calls)
	}
	if !result.ReadyToTest || result.Draft.Name != "ORACLE_host.docker.internal:11521_FREEPDB1_TAKEOUT_USER" {
		t.Fatalf("unexpected natural Oracle parse result: %+v", result)
	}
	if result.Draft.OracleConnectMode != "SERVICE_NAME" {
		t.Fatalf("service name input must select SERVICE_NAME mode: %+v", result.Draft)
	}
	if containsString(result.MissingFields, "name") || containsString(result.MissingFields, "code") {
		t.Fatalf("generated fields must not be requested: %+v", result.MissingFields)
	}
}

func TestTurnParsesOracleSIDWithoutModel(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{}
	result, err := NewService(sourceReaderStub{}, invoker, 0).Turn(
		context.Background(), "tenant", "actor", "", TurnRequest{
			Instruction:      "Oracle 数据源，Host: db.internal，Port: 1521，SID: ORCL，Username: REPORT_USER。",
			PasswordProvided: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 0 {
		t.Fatalf("deterministic SID details should not call the model; got %d calls", invoker.calls)
	}
	if !result.ReadyToTest || result.Draft.Database != "ORCL" || result.Draft.OracleConnectMode != "SID" {
		t.Fatalf("unexpected Oracle SID parse result: %+v", result)
	}
}

func TestTurnKeepsRecognizedOracleServiceNameWhenModelDraftOmitsIt(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{}
	invoker.result = modelResult(t, map[string]any{
		"reply": "已识别 Oracle 服务名 FREEPDB1，可以测试连接。",
		"draft": map[string]any{
			"code": "", "name": "", "description": "", "type": "ORACLE",
			"host": "host.docker.internal", "port": 1521, "database": "", "username": "TAKEOUT_USER",
			"oracleConnectMode": "SERVICE_NAME", "visibility": "PRIVATE", "sharingScope": "PRIVATE",
		},
		"suggestedAction": "TEST", "diagnosis": "", "suggestedChecks": []string{},
	})
	result, err := NewService(sourceReaderStub{}, invoker, 0).Turn(
		context.Background(), "tenant", "actor", "", TurnRequest{
			Instruction:      "Oracle Service Name: FREEPDB1",
			PasswordProvided: true,
			Draft: Draft{
				Type: "ORACLE", Host: "host.docker.internal", Port: 1521,
				Username: "TAKEOUT_USER", OracleConnectMode: "SERVICE_NAME",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 1 {
		t.Fatalf("partial instruction should use the model once; got %d calls", invoker.calls)
	}
	if !result.ReadyToTest || result.Draft.Database != "FREEPDB1" {
		t.Fatalf("recognized service name must be retained in structured draft: %+v", result)
	}
	if result.Draft.Name != "ORACLE_host.docker.internal:1521_FREEPDB1_TAKEOUT_USER" ||
		result.Draft.Code != "ds_oracle_host_docker_internal_1521_freepdb1_takeout_user" {
		t.Fatalf("identity must be generated after retaining service name: %+v", result.Draft)
	}
}

func TestTurnFallsBackToLocalDraftWhenModelOutputIsInvalid(t *testing.T) {
	t.Parallel()
	invoker := &invokerStub{err: ErrInvalidOutput}
	result, err := NewService(sourceReaderStub{}, invoker, 0).Turn(
		context.Background(), "tenant", "actor", "", TurnRequest{
			Instruction:      "用户名：reader",
			PasswordProvided: true,
			Draft: Draft{Code: "sales", Name: "销售库", Type: "MYSQL", Host: "db.internal",
				Port: 3306, Database: "sales", Username: "reader"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 1 || !result.ReadyToTest || result.Draft.Username != "reader" {
		t.Fatalf("unexpected fallback result: calls=%d result=%+v", invoker.calls, result)
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
