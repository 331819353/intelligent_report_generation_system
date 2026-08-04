package datasourceai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/datasource"
)

const (
	promptVersion   = "data-source-assistant-v1"
	maxHistoryItems = 16
	maxMessageRunes = 2000
	maxReplyRunes   = 2000
)

const systemPrompt = `你是数据源配置助手，只处理 MySQL、Oracle、Excel/CSV 数据源的新建与修改。
你的任务是从用户自然语言和已有草稿中提取配置，保留未被用户修改的已有值，并通过简短多轮问题补齐缺失信息。
严禁编造 Host、数据库名、用户名或文件；无法从对话确认的值必须保持空字符串。密码永远不在输出中，passwordProvided 仅表示用户已在安全输入框填写。
数据库数据源的名称和 code 由系统根据类型、Host、端口、数据库/服务名和用户名自动生成；永远不要向用户询问名称或 code。
code 必须以英文字母开头，只能包含英文字母、数字和下划线，最长 128 位。
Host 只输出主机名或 IP，不包含协议、JDBC 前缀、端口、路径。MySQL 默认端口 3306，Oracle 默认端口 1521。
Oracle 必须明确区分 SERVICE_NAME 和 SID；用户未说明时默认 SERVICE_NAME，绝不能把 Schema 或用户名当作服务名。
测试失败时，根据稳定错误代码给出具体、可执行的检查建议；不要声称已经修复网络、账号或目标数据库。只有输入中明确可安全规范化的格式问题才能建议重试。
reply 使用中文且简洁。suggestedAction 只能是 ASK、TEST 或 WAIT。`

var codePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

type Invoker interface {
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type SourceReader interface {
	Get(context.Context, string, string) (datasource.Source, error)
}

type Service struct {
	sources SourceReader
	ai      Invoker
	timeout time.Duration
}

func NewService(sources SourceReader, ai Invoker, timeout time.Duration) *Service {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Service{sources: sources, ai: ai, timeout: timeout}
}

type promptEnvelope struct {
	Mode             string       `json:"mode"`
	ExistingSourceID string       `json:"existingSourceId,omitempty"`
	Instruction      string       `json:"instruction"`
	History          []Message    `json:"history"`
	Draft            Draft        `json:"draft"`
	PasswordProvided bool         `json:"passwordProvided"`
	FileProvided     bool         `json:"fileProvided"`
	TestFailure      *TestFailure `json:"testFailure,omitempty"`
}

type modelOutput struct {
	Reply           string   `json:"reply"`
	Draft           Draft    `json:"draft"`
	SuggestedAction string   `json:"suggestedAction"`
	Diagnosis       string   `json:"diagnosis"`
	SuggestedChecks []string `json:"suggestedChecks"`
}

func (s *Service) Turn(
	ctx context.Context, tenantID, actorID, sourceID string, input TurnRequest,
) (TurnResult, error) {
	if s == nil || s.sources == nil || s.ai == nil ||
		strings.TrimSpace(tenantID) == "" || strings.TrimSpace(actorID) == "" {
		return TurnResult{}, ErrProviderUnavailable
	}
	input.Instruction = cleanText(input.Instruction, maxMessageRunes)
	if input.Instruction == "" {
		return TurnResult{}, ErrInvalidRequest
	}
	var instructionPasswordMentioned bool
	input.Instruction, instructionPasswordMentioned = redactInstructionSecrets(input.Instruction)
	history, err := normalizeHistory(input.History)
	if err != nil {
		return TurnResult{}, err
	}
	mode := "CREATE"
	baseline := Draft{Type: "MYSQL", Port: 3306, OracleConnectMode: "SERVICE_NAME", Visibility: "PRIVATE", SharingScope: "PRIVATE"}
	if sourceID != "" {
		mode = "UPDATE"
		source, err := s.sources.Get(ctx, tenantID, sourceID)
		if err != nil {
			return TurnResult{}, err
		}
		baseline = draftFromSource(source)
	}
	draft := mergeDraft(baseline, input.Draft)
	draft, fixes := safeRepairs(draft, input.TestFailure)
	draft, identityFixes := ensureGeneratedIdentity(draft, mode == "CREATE")
	fixes = uniqueStrings(append(fixes, identityFixes...))
	parsed := parseStructuredInstruction(input.Instruction, draft)
	parsed.PasswordMentioned = parsed.PasswordMentioned || instructionPasswordMentioned
	// A complete key/value description is deterministic input. Parsing it locally
	// avoids a slow provider round trip and makes the flow resilient to malformed
	// structured output from the configured model.
	if parsed.RecognizedFields >= 4 {
		return localTurnResult(parsed, mode, input.PasswordProvided, input.FileProvided)
	}
	payload, err := json.Marshal(promptEnvelope{
		Mode: mode, ExistingSourceID: sourceID, Instruction: input.Instruction,
		History: history, Draft: draft, PasswordProvided: input.PasswordProvided,
		FileProvided: input.FileProvided, TestFailure: normalizeFailure(input.TestFailure),
	})
	if err != nil {
		return TurnResult{}, err
	}
	schema, err := json.Marshal(outputSchema())
	if err != nil {
		return TurnResult{}, err
	}
	temperature := 0.0
	callCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	invocation, err := s.ai.Invoke(callCtx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeDataSourceConfiguration,
		PromptVersion: promptVersion, ResourceType: "DATA_SOURCE",
		ResourceID: resourceID(sourceID, actorID),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: systemPrompt}}},
				{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(payload)}}},
			},
			ResponseSchema: aiplatform.JSONSchema{Name: "data_source_assistant_turn", Description: "数据源配置助手的单轮结构化结果", Schema: schema},
			Temperature:    &temperature, MaxOutputTokens: 1600,
		},
	})
	if err != nil {
		if parsed.RecognizedFields > 0 && canUseLocalFallback(err) {
			return localTurnResult(parsed, mode, input.PasswordProvided, input.FileProvided)
		}
		var providerErr *aiplatform.ProviderError
		if errors.As(err, &providerErr) && providerErr.Code == aiplatform.ErrorCodeProviderUnavailable {
			return TurnResult{}, ErrProviderUnavailable
		}
		return TurnResult{}, err
	}
	output, err := decodeOutput(invocation.ProviderResult.Content)
	if err != nil {
		if parsed.RecognizedFields > 0 && canUseLocalFallback(err) {
			return localTurnResult(parsed, mode, input.PasswordProvided, input.FileProvided)
		}
		return TurnResult{}, err
	}
	resultDraft := mergeDraft(draft, output.Draft)
	// Explicit values parsed from the user's current message take precedence over
	// model output. This keeps the displayed structured draft consistent with the
	// assistant reply even when the model mentions a value but omits it from draft.
	resultDraft = applyRecognizedFields(resultDraft, parsed)
	resultDraft, outputFixes := safeRepairs(resultDraft, input.TestFailure)
	fixes = uniqueStrings(append(fixes, outputFixes...))
	resultDraft, identityFixes = ensureGeneratedIdentity(resultDraft, mode == "CREATE")
	fixes = uniqueStrings(append(fixes, identityFixes...))
	if err := validateDraft(resultDraft); err != nil {
		outputErr := fmt.Errorf("%w: %v", ErrInvalidOutput, err)
		if parsed.RecognizedFields > 0 && canUseLocalFallback(outputErr) {
			return localTurnResult(parsed, mode, input.PasswordProvided, input.FileProvided)
		}
		return TurnResult{}, outputErr
	}
	missing := missingFields(resultDraft, mode, input.PasswordProvided, input.FileProvided)
	action := strings.ToUpper(strings.TrimSpace(output.SuggestedAction))
	if len(missing) > 0 {
		action = "ASK"
	} else if action != "TEST" && action != "WAIT" {
		action = "TEST"
	}
	checks := normalizeList(output.SuggestedChecks, 6, 160)
	if input.TestFailure != nil && len(checks) == 0 {
		checks = failureChecks(input.TestFailure.Code)
	}
	reply := cleanText(output.Reply, maxReplyRunes)
	if len(identityFixes) > 0 {
		reply = generatedIdentityReply(missing)
	}
	return TurnResult{
		Reply: reply, Draft: resultDraft,
		MissingFields: missing, ReadyToTest: len(missing) == 0,
		SuggestedAction: action, Diagnosis: cleanText(output.Diagnosis, 500),
		SuggestedChecks: checks, AutoFixes: fixes,
		AutoRetry: input.TestFailure != nil && len(fixes) > 0 && len(missing) == 0,
		RequestID: invocation.RequestID,
	}, nil
}

func resourceID(sourceID, actorID string) string {
	if sourceID != "" {
		return sourceID
	}
	return "new:" + actorID
}

func draftFromSource(source datasource.Source) Draft {
	return Draft{
		Code: source.Code, Name: source.Name, Description: source.Description,
		Type: string(source.Type), Host: configText(source.Config, "host"),
		Port: configInt(source.Config, "port"), Database: configText(source.Config, "database"),
		OracleConnectMode: configText(source.Config, "oracleConnectMode"),
		Username:          configText(source.Config, "username"), Visibility: string(source.Visibility),
		SharingScope: source.SharingScope,
	}
}

func mergeDraft(base, update Draft) Draft {
	result := base
	if strings.TrimSpace(update.Code) != "" {
		result.Code = update.Code
	}
	if strings.TrimSpace(update.Name) != "" {
		result.Name = update.Name
	}
	if strings.TrimSpace(update.Description) != "" {
		result.Description = update.Description
	}
	if strings.TrimSpace(update.Type) != "" {
		result.Type = update.Type
	}
	if strings.TrimSpace(update.Host) != "" {
		result.Host = update.Host
	}
	if update.Port != 0 {
		result.Port = update.Port
	}
	if strings.TrimSpace(update.Database) != "" {
		result.Database = update.Database
	}
	if strings.TrimSpace(update.OracleConnectMode) != "" {
		result.OracleConnectMode = update.OracleConnectMode
	}
	if strings.TrimSpace(update.Username) != "" {
		result.Username = update.Username
	}
	if strings.TrimSpace(update.Visibility) != "" {
		result.Visibility = update.Visibility
	}
	if strings.TrimSpace(update.SharingScope) != "" {
		result.SharingScope = update.SharingScope
	}
	return normalizeDraft(result)
}

func normalizeDraft(value Draft) Draft {
	value.Code = strings.TrimSpace(value.Code)
	value.Name = cleanText(value.Name, 200)
	value.Description = cleanText(value.Description, 1000)
	value.Type = strings.ToUpper(strings.TrimSpace(value.Type))
	value.Host = strings.TrimSpace(value.Host)
	value.Database = strings.TrimSpace(value.Database)
	value.OracleConnectMode = strings.ToUpper(strings.TrimSpace(value.OracleConnectMode))
	if value.OracleConnectMode == "" {
		value.OracleConnectMode = "SERVICE_NAME"
	}
	value.Username = strings.TrimSpace(value.Username)
	value.Visibility = strings.ToUpper(strings.TrimSpace(value.Visibility))
	value.SharingScope = strings.ToUpper(strings.TrimSpace(value.SharingScope))
	if value.Visibility == "" {
		value.Visibility = "PRIVATE"
	}
	if value.SharingScope == "" {
		value.SharingScope = "PRIVATE"
	}
	return value
}

func safeRepairs(value Draft, failure *TestFailure) (Draft, []string) {
	value = normalizeDraft(value)
	fixes := make([]string, 0, 3)
	originalHost := value.Host
	for _, prefix := range []string{"jdbc:mysql://", "jdbc:oracle:thin:@", "mysql://", "oracle://", "https://", "http://"} {
		if strings.HasPrefix(strings.ToLower(value.Host), prefix) {
			value.Host = value.Host[len(prefix):]
			fixes = append(fixes, "已移除 Host 中的协议或 JDBC 前缀")
			break
		}
	}
	if parsed, err := url.Parse("scheme://" + value.Host); err == nil && parsed.Hostname() != "" {
		if parsed.Port() != "" && value.Port == 0 {
			if port, err := strconv.Atoi(parsed.Port()); err == nil {
				value.Port = port
			}
		}
		if parsed.Hostname() != value.Host {
			value.Host = parsed.Hostname()
			fixes = append(fixes, "已从 Host 中拆分端口或路径")
		}
	}
	if strings.EqualFold(value.Host, "localhost") || value.Host == "127.0.0.1" || value.Host == "::1" {
		value.Host = "host.docker.internal"
		fixes = append(fixes, "已将本机地址转换为容器可访问的 host.docker.internal")
	}
	if value.Port == 0 && value.Type == "MYSQL" {
		value.Port = 3306
		fixes = append(fixes, "已补充 MySQL 默认端口 3306")
	}
	if value.Port == 0 && value.Type == "ORACLE" {
		value.Port = 1521
		fixes = append(fixes, "已补充 Oracle 默认端口 1521")
	}
	if failure == nil && originalHost == value.Host {
		fixes = nil
	}
	return value, uniqueStrings(fixes)
}

func validateDraft(value Draft) error {
	if value.Type != "MYSQL" && value.Type != "ORACLE" && value.Type != "EXCEL" {
		return errors.New("unsupported data source type")
	}
	if value.Code != "" && !codePattern.MatchString(value.Code) {
		return errors.New("invalid data source code")
	}
	if value.Visibility != "PRIVATE" && value.Visibility != "TENANT_PUBLIC" {
		return errors.New("invalid visibility")
	}
	if value.SharingScope != "PRIVATE" && value.SharingScope != "DOMAIN" {
		return errors.New("invalid sharing scope")
	}
	if value.Port < 0 || value.Port > 65535 {
		return errors.New("invalid port")
	}
	if value.OracleConnectMode != "SERVICE_NAME" && value.OracleConnectMode != "SID" {
		return errors.New("invalid Oracle connection mode")
	}
	if value.Host != "" && !validHost(value.Host) {
		return errors.New("invalid host")
	}
	return nil
}

func validHost(value string) bool {
	if len(value) > 253 || strings.ContainsAny(value, `/\\@% `) || strings.Contains(value, "://") {
		return false
	}
	if net.ParseIP(value) != nil {
		return true
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '-' {
				return false
			}
		}
	}
	return true
}

func missingFields(value Draft, mode string, passwordProvided, fileProvided bool) []string {
	missing := make([]string, 0, 7)
	if value.Type == "" {
		missing = append(missing, "type")
	}
	if value.Type == "EXCEL" {
		if value.Name == "" {
			missing = append(missing, "name")
		}
		if value.Code == "" {
			missing = append(missing, "code")
		}
		if mode == "CREATE" && !fileProvided {
			missing = append(missing, "file")
		}
		return missing
	}
	if value.Host == "" {
		missing = append(missing, "host")
	}
	if value.Port == 0 {
		missing = append(missing, "port")
	}
	if value.Database == "" {
		missing = append(missing, "database")
	}
	if value.Username == "" {
		missing = append(missing, "username")
	}
	if mode == "CREATE" && !passwordProvided {
		missing = append(missing, "password")
	}
	return missing
}

func normalizeHistory(values []Message) ([]Message, error) {
	if len(values) > maxHistoryItems {
		values = values[len(values)-maxHistoryItems:]
	}
	result := make([]Message, 0, len(values))
	for _, value := range values {
		role := strings.ToLower(strings.TrimSpace(value.Role))
		if role != "user" && role != "assistant" {
			return nil, ErrInvalidRequest
		}
		content := cleanText(value.Content, maxMessageRunes)
		content, _ = redactInstructionSecrets(content)
		if content != "" {
			result = append(result, Message{Role: role, Content: content})
		}
	}
	return result, nil
}

func normalizeFailure(value *TestFailure) *TestFailure {
	if value == nil {
		return nil
	}
	return &TestFailure{Code: strings.ToUpper(cleanText(value.Code, 64)), Message: cleanText(value.Message, 256)}
}

func decodeOutput(raw []byte) (modelOutput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var output modelOutput
	if err := decoder.Decode(&output); err != nil {
		return modelOutput{}, ErrInvalidOutput
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return modelOutput{}, ErrInvalidOutput
	}
	if cleanText(output.Reply, maxReplyRunes) == "" {
		return modelOutput{}, ErrInvalidOutput
	}
	return output, nil
}

func outputSchema() map[string]any {
	stringField := func() map[string]any { return map[string]any{"type": "string"} }
	draft := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"code", "name", "description", "type", "host", "port", "database", "oracleConnectMode", "username", "visibility", "sharingScope"},
		"properties": map[string]any{
			"code": stringField(), "name": stringField(), "description": stringField(),
			"type": map[string]any{"type": "string", "enum": []string{"MYSQL", "ORACLE", "EXCEL"}},
			"host": stringField(), "port": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535},
			"database":          stringField(),
			"oracleConnectMode": map[string]any{"type": "string", "enum": []string{"SERVICE_NAME", "SID"}},
			"username":          stringField(),
			"visibility":        map[string]any{"type": "string", "enum": []string{"PRIVATE", "TENANT_PUBLIC"}},
			"sharingScope":      map[string]any{"type": "string", "enum": []string{"PRIVATE", "DOMAIN"}},
		},
	}
	return map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"reply", "draft", "suggestedAction", "diagnosis", "suggestedChecks"},
		"properties": map[string]any{
			"reply": stringField(), "draft": draft,
			"suggestedAction": map[string]any{"type": "string", "enum": []string{"ASK", "TEST", "WAIT"}},
			"diagnosis":       stringField(),
			"suggestedChecks": map[string]any{"type": "array", "maxItems": 6, "items": stringField()},
		},
	}
}

func failureChecks(code string) []string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "CONNECTION_AUTH_FAILED":
		return []string{"地址、端口和数据库/服务名已通过；确认用户名和密码可直接登录目标数据库", "确认账号未锁定且允许从当前网络来源登录"}
	case "DATABASE_NOT_FOUND":
		return []string{"地址和端口已通过；确认 Database 或 Oracle Service Name/SID 拼写", "确认 Oracle 连接模式是 SERVICE_NAME 还是 SID"}
	case "PORT_REFUSED", "CONNECTION_REFUSED":
		return []string{"地址解析已通过；确认数据库服务已启动并监听配置端口", "检查 Docker 端口映射、安全组和防火墙入站规则"}
	case "PORT_TIMEOUT":
		return []string{"地址解析已通过，但端口连接超时", "检查端口、防火墙、安全组和网络 ACL"}
	case "ADDRESS_RESOLUTION_FAILED", "CONNECTION_DNS_FAILED":
		return []string{"地址检查未通过；确认 Host 可在 Connector 容器内解析", "必要时使用可路由的内网域名或 IP"}
	case "ADDRESS_UNREACHABLE", "NETWORK_UNREACHABLE":
		return []string{"地址路由检查未通过；检查路由、出站白名单和网络隔离策略", "确认目标地址可从 Connector 所在网络访问"}
	case "DATABASE_HANDSHAKE_TIMEOUT":
		return []string{"地址和端口已通过，但数据库握手超时", "检查数据库监听协议、TLS 要求、Service Name/SID 和服务负载"}
	case "CONNECTION_TIMEOUT":
		return []string{"连接检测超时；重新执行分层检测以确认失败阶段", "检查网络、防火墙和数据库监听状态"}
	default:
		return []string{"依次检查地址、端口、数据库/服务名和认证", "确认数据库监听协议与当前连接类型一致"}
	}
}

func configText(config map[string]any, key string) string {
	if value, ok := config[key].(string); ok {
		return value
	}
	return ""
}
func configInt(config map[string]any, key string) int {
	switch value := config[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		result, _ := strconv.Atoi(value.String())
		return result
	}
	return 0
}
func cleanText(value string, limit int) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value))
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
func normalizeList(values []string, count, size int) []string {
	result := make([]string, 0, min(len(values), count))
	for _, value := range values {
		value = cleanText(value, size)
		if value != "" && len(result) < count {
			result = append(result, value)
		}
	}
	return uniqueStrings(result)
}
func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
