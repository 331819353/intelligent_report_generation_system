package metadataai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/semanticquality"
)

type Provider interface {
	Name() string
	Model() string
	Configured() bool
	Complete(context.Context, string, string, CompletionInput) (ProviderResult, error)
}

// Invoker 是元数据领域对通用 AI 编排层的最小依赖，便于隔离测试和后续替换 Provider。
type Invoker interface {
	Configured() bool
	ProviderName() string
	Model() string
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type OrchestratedProvider struct{ invoker Invoker }

const metadataCompletionSystemPrompt = "你是企业数据资产元数据补全器。只能依据给定技术元数据和最多十行数据样本生成结果，不得虚构资产或返回未请求的目标。每个目标的 missingFields 是本轮唯一需要新增或修复的属性；不在 missingFields 中的属性已经确认，必须原样复用对应 currentBusinessName、currentDescription、currentTags、currentSemanticType、currentSensitivity，不得重写。表级描述和标签必须优先说明该表的业务功能、适用范围与一行数据的粒度。对主键、外键、唯一键、业务编号或其他可能参与关联的字段，businessDescription 必须结合 constraints、字段属性和样本说明它关联的业务实体、键角色、方向、唯一性与可空性；无法从证据确定目标表时应明确写“候选关联键”，不得编造目标。每个字段的 semanticType 必须与输入 canonicalType 兼容：AMOUNT、PERCENTAGE、QUANTITY 只能用于数值类型，DATE、TIME、DATETIME 只能用于相应日期时间类型，BOOLEAN 只能用于布尔类型；TEXT 字段不得标为上述数值、日期时间或布尔语义，应从 TEXT、CATEGORY、IDENTIFIER、REGION、COMPANY_NAME 中选择。对 Excel/CSV 工作表，table.name 是 Sheet 名称，columns.name 是解析后的真实表头，sampleRows 的键和值分别对应表头和该列真实内容；必须结合 Sheet 名称、全部表头、字段类型和样本值判断表业务名称、字段业务名称及字段业务描述，不得只翻译物理名称或忽略样本内容。当 sourceFormat=CSV 或 EXCEL 时，表的 businessName 必须是准确简洁的中文业务名称；每个字段的 businessName 必须是能够表达原字段业务含义的小写英文字段名，多个单词使用下划线分隔，例如 customer_name、order_amount；businessDescription 必须使用中文描述字段含义。原始文件表头保留在 columns.name，不要把它原样复制到 businessName。标签应覆盖适用的领域、主题、作用、功能、范围、粒度和关联角色；标签数量不设固定上限，但至少返回一个标签，只能使用 JSON Schema 中的受控词表且不得重复。columns 中只包含本次发生变化且需要完善的字段，每个字段必须恰好返回一次；targetTable=false 时不得返回 table。"

// NewOrchestratedProvider 将元数据补全合同接入通用超时、重试、配额、成本和审计链路。
func NewOrchestratedProvider(invoker Invoker) *OrchestratedProvider {
	return &OrchestratedProvider{invoker: invoker}
}

func (p *OrchestratedProvider) Name() string {
	if p == nil || p.invoker == nil {
		return ""
	}
	return p.invoker.ProviderName()
}

func (p *OrchestratedProvider) Model() string {
	if p == nil || p.invoker == nil {
		return ""
	}
	return p.invoker.Model()
}

func (p *OrchestratedProvider) Configured() bool {
	return p != nil && p.invoker != nil && p.invoker.Configured()
}

// Complete 只发送最小化技术元数据与最多十行样本，绝不发送连接凭据；样本不会持久化。
func (p *OrchestratedProvider) Complete(ctx context.Context, tenantID, actorID string, input CompletionInput) (ProviderResult, error) {
	if !p.Configured() {
		return ProviderResult{}, ErrProviderUnavailable
	}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		return ProviderResult{}, err
	}
	schemaJSON, err := json.Marshal(outputSchema(input))
	if err != nil {
		return ProviderResult{}, err
	}
	temperature := 0.0
	invocation := aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID, Purpose: aiplatform.PurposeMetadataCompletion,
		PromptVersion: PromptVersion, ResourceType: "METADATA_TABLE", ResourceID: input.Table.ID,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: metadataCompletionSystemPrompt}}},
				{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(inputJSON)}}},
			},
			ResponseSchema: aiplatform.JSONSchema{Name: "metadata_completion", Description: "企业数据资产元数据结构化补全", Schema: schemaJSON},
			Temperature:    &temperature, MaxOutputTokens: 4096,
		},
	}
	result, err := p.invoker.Invoke(ctx, invocation)
	var validationErr error
	previousUsage := Usage{}
	var invalidContent json.RawMessage
	if err != nil {
		validationErr = translateOrchestrationError(err)
		if !errors.Is(validationErr, ErrInvalidOutput) {
			return ProviderResult{}, validationErr
		}
		if content, diagnostic, ok := aiplatform.InvalidOutputDetails(err); ok {
			invalidContent = content
			if diagnostic != "" {
				validationErr = fmt.Errorf("%w: %s", ErrInvalidOutput, diagnostic)
			}
			slog.WarnContext(ctx, "metadata AI structured output requires repair",
				"table_id", input.Table.ID, "output_diagnostic", diagnostic)
		}
	} else {
		output, outputErr := decodeAndValidateCompletion(input, result.ProviderResult.Content)
		if outputErr == nil {
			return completionProviderResult(result, output, Usage{}), nil
		}
		validationErr = outputErr
		invalidContent = result.ProviderResult.Content
		previousUsage = invocationUsage(result)
		slog.WarnContext(ctx, "metadata AI domain output requires repair",
			"table_id", input.Table.ID, "output_diagnostic", outputErr)
	}
	if err := ctx.Err(); err != nil {
		return ProviderResult{}, err
	}

	// JSON Schema 无法表达 targetId “各出现且只出现一次”等跨数组约束。
	// 首次非法输出时，将可用的模型原输出和安全校验原因带回同一上下文，仅纠错重试一次。
	repairInvocation := invocation
	repairMessages := append([]aiplatform.Message(nil), invocation.Request.Messages...)
	if len(invalidContent) > 0 {
		repairMessages = append(repairMessages, aiplatform.Message{Role: aiplatform.MessageRoleAssistant, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(invalidContent)}}})
	}
	repairMessages = append(repairMessages, aiplatform.Message{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: repairInstruction(input, validationErr)}}})
	repairInvocation.Request.Messages = repairMessages
	repairResult, err := p.invoker.Invoke(ctx, repairInvocation)
	if err != nil {
		translated := translateOrchestrationError(err)
		repairContent := json.RawMessage(nil)
		if content, _, ok := aiplatform.InvalidOutputDetails(err); ok {
			repairContent = content
		}
		for _, candidate := range []struct {
			result  aiplatform.InvocationResult
			content json.RawMessage
		}{
			{result: repairResult, content: repairContent},
			{result: result, content: invalidContent},
		} {
			if partial, missing, ok := decodePartialCompletion(input, candidate.content); ok {
				partialResult := completionProviderResult(
					candidate.result, partial, previousUsage,
				)
				if missing == 0 {
					return partialResult, nil
				}
				return partialResult, &PartialOutputError{MissingTargets: missing}
			}
		}
		return ProviderResult{}, translated
	}
	repairedOutput, repairErr := decodeAndValidateCompletion(input, repairResult.ProviderResult.Content)
	if repairErr != nil {
		if partial, missing, ok := decodePartialCompletion(
			input, repairResult.ProviderResult.Content,
		); ok {
			partialResult := completionProviderResult(
				repairResult, partial, previousUsage,
			)
			if missing == 0 {
				return partialResult, nil
			}
			return partialResult, &PartialOutputError{MissingTargets: missing}
		}
		if partial, missing, ok := decodePartialCompletion(input, invalidContent); ok {
			partialResult := completionProviderResult(
				result, partial, previousUsage,
			)
			if missing == 0 {
				return partialResult, nil
			}
			return partialResult, &PartialOutputError{MissingTargets: missing}
		}
		return ProviderResult{}, fmt.Errorf("%w: 纠错重试仍未通过: %v", ErrInvalidOutput, repairErr)
	}
	return completionProviderResult(repairResult, repairedOutput, previousUsage), nil
}

// decodeAndValidateCompletion 在 Provider JSON Schema 校验后继续执行领域级一一映射校验。
func decodeAndValidateCompletion(input CompletionInput, content json.RawMessage) (CompletionOutput, error) {
	var output CompletionOutput
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return CompletionOutput{}, fmt.Errorf("%w: 解码元数据结构化输出失败", ErrInvalidOutput)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return CompletionOutput{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	output = normalizeOutputForInput(input, output)
	if err := ValidateOutput(input, output); err != nil {
		return CompletionOutput{}, err
	}
	return output, nil
}

// decodePartialCompletion 只保留能够独立通过可信边界校验的表/字段建议。重复
// targetId 会整体丢弃该目标，避免在冲突候选中擅自选择；未知目标永不落库。
func decodePartialCompletion(
	input CompletionInput,
	content json.RawMessage,
) (CompletionOutput, int, bool) {
	if len(content) == 0 {
		return CompletionOutput{}, 0, false
	}
	var candidate CompletionOutput
	if err := json.Unmarshal(content, &candidate); err != nil ||
		candidate.SchemaVersion != SchemaVersion {
		return CompletionOutput{}, 0, false
	}
	candidate = normalizeOutputForInput(input, candidate)
	output := CompletionOutput{
		SchemaVersion: SchemaVersion,
		Columns:       []SuggestionValue{},
	}
	missing := len(input.Columns)
	if input.TargetTable {
		missing++
		if candidate.Table != nil && candidate.Table.TargetID == input.Table.ID {
			value, complete, accepted := mergePartialSuggestion(
				input, input.Table, *candidate.Table, false,
			)
			if accepted {
				output.Table = &value
				if complete {
					missing--
				}
			}
		}
	}
	expected := make(map[string]Target, len(input.Columns))
	for _, target := range input.Columns {
		expected[target.ID] = target
	}
	candidates := make(map[string]SuggestionValue, len(candidate.Columns))
	duplicates := make(map[string]bool)
	for _, value := range candidate.Columns {
		if _, exists := expected[value.TargetID]; !exists {
			continue
		}
		if _, seen := candidates[value.TargetID]; seen {
			delete(candidates, value.TargetID)
			duplicates[value.TargetID] = true
			continue
		}
		if duplicates[value.TargetID] {
			continue
		}
		candidates[value.TargetID] = value
	}
	for _, target := range input.Columns {
		candidateValue, exists := candidates[target.ID]
		if !exists {
			continue
		}
		value, complete, accepted := mergePartialSuggestion(
			input, target, candidateValue, true,
		)
		if !accepted {
			continue
		}
		output.Columns = append(output.Columns, value)
		if complete {
			missing--
		}
	}
	if missing == 0 {
		return output, 0, true
	}
	applied := len(output.Columns)
	if output.Table != nil {
		applied++
	}
	return output, missing, applied > 0
}

// mergePartialSuggestion 逐属性校验模型候选并与 PostgreSQL 中的当前值合并。
// 不可信属性会被忽略，已经确认的属性不会被空值或非法枚举覆盖。
func mergePartialSuggestion(
	input CompletionInput,
	target Target,
	candidate SuggestionValue,
	column bool,
) (SuggestionValue, bool, bool) {
	if candidate.TargetID != target.ID ||
		candidate.Confidence <= 0 || candidate.Confidence > 1 {
		return SuggestionValue{}, false, false
	}
	value := suggestionFromTarget(target)
	value.TargetID = target.ID
	value.Confidence = candidate.Confidence
	provided := make([]string, 0, 5)
	if err := validateText("businessName", candidate.BusinessName, 120); err == nil {
		valid := true
		if column && isFileSourceFormat(input.SourceFormat) {
			valid = csvBusinessNameRegexp.MatchString(candidate.BusinessName)
		}
		if !column && isFileSourceFormat(input.SourceFormat) {
			valid = containsChinese(candidate.BusinessName)
		}
		if valid {
			value.BusinessName = candidate.BusinessName
			provided = append(provided, "businessName")
		}
	}
	if err := validateText(
		"businessDescription", candidate.BusinessDescription, 1000,
	); err == nil && (!column || !isFileSourceFormat(input.SourceFormat) ||
		containsChinese(candidate.BusinessDescription)) {
		value.BusinessDescription = candidate.BusinessDescription
		provided = append(provided, "businessDescription")
	}
	if validControlledTags(candidate.Tags) {
		value.Tags = append([]string(nil), candidate.Tags...)
		provided = append(provided, "tags")
	}
	if allowedSensitivity[candidate.SensitivityLevel] {
		value.SensitivityLevel = candidate.SensitivityLevel
		provided = append(provided, "sensitivityLevel")
	}
	if column && allowedSemanticTypes[candidate.SemanticType] &&
		(strings.TrimSpace(target.CanonicalType) == "" ||
			semanticquality.Compatible(target.CanonicalType, candidate.SemanticType)) {
		value.SemanticType = candidate.SemanticType
		provided = append(provided, "semanticType")
	}
	if len(provided) == 0 {
		return SuggestionValue{}, false, false
	}
	value.ProvidedFields = provided
	value.Complete = len(completionMissingFields(input, target, value, column)) == 0
	return value, value.Complete, true
}

func suggestionFromTarget(target Target) SuggestionValue {
	return SuggestionValue{
		TargetID:            target.ID,
		BusinessName:        strings.TrimSpace(target.CurrentBusinessName),
		BusinessDescription: strings.TrimSpace(target.CurrentDescription),
		Tags:                append([]string{}, target.CurrentTags...),
		SensitivityLevel:    strings.TrimSpace(target.CurrentSensitivity),
		SemanticType:        strings.TrimSpace(target.CurrentSemanticType),
	}
}

func completionMissingFields(
	input CompletionInput,
	target Target,
	value SuggestionValue,
	column bool,
) []string {
	missing := make([]string, 0, 5)
	if err := validateText("businessName", value.BusinessName, 120); err != nil ||
		(column && isFileSourceFormat(input.SourceFormat) &&
			!csvBusinessNameRegexp.MatchString(value.BusinessName)) ||
		(!column && isFileSourceFormat(input.SourceFormat) &&
			!containsChinese(value.BusinessName)) {
		missing = append(missing, "businessName")
	}
	if err := validateText(
		"businessDescription", value.BusinessDescription, 1000,
	); err != nil || (column && isFileSourceFormat(input.SourceFormat) &&
		!containsChinese(value.BusinessDescription)) {
		missing = append(missing, "businessDescription")
	}
	if !validControlledTags(value.Tags) {
		missing = append(missing, "tags")
	}
	if !allowedSensitivity[value.SensitivityLevel] {
		missing = append(missing, "sensitivityLevel")
	}
	if column && (!allowedSemanticTypes[value.SemanticType] ||
		(strings.TrimSpace(target.CanonicalType) != "" &&
			!semanticquality.Compatible(target.CanonicalType, value.SemanticType))) {
		missing = append(missing, "semanticType")
	}
	return missing
}

func validControlledTags(tags []string) bool {
	if len(tags) == 0 {
		return false
	}
	seen := make(map[string]bool, len(tags))
	for _, tag := range tags {
		if !allowedTags[tag] || seen[tag] {
			return false
		}
		seen[tag] = true
	}
	return true
}

func validateSuggestionForTarget(
	input CompletionInput,
	target Target,
	value SuggestionValue,
	column bool,
) error {
	if err := validateValue(value, column, column && isFileSourceFormat(input.SourceFormat)); err != nil {
		return err
	}
	if !column {
		if isFileSourceFormat(input.SourceFormat) &&
			!containsChinese(value.BusinessName) {
			return errors.New("file table business name must contain Chinese text")
		}
		return nil
	}
	if strings.TrimSpace(target.CanonicalType) != "" &&
		!semanticquality.Compatible(target.CanonicalType, value.SemanticType) {
		return errors.New("semantic type is incompatible with canonical type")
	}
	return nil
}

// repairInstruction 明确列出本次允许的稳定 ID，避免模型再次遗漏或复用目标。
func repairInstruction(input CompletionInput, validationErr error) string {
	columnIDs := make([]string, 0, len(input.Columns))
	missingByTarget := make(map[string][]string, len(input.Columns)+1)
	for _, column := range input.Columns {
		columnIDs = append(columnIDs, column.ID)
		missingByTarget[column.ID] = column.MissingFields
	}
	encodedColumnIDs, _ := json.Marshal(columnIDs)
	if input.TargetTable {
		missingByTarget[input.Table.ID] = input.Table.MissingFields
	}
	encodedMissing, _ := json.Marshal(missingByTarget)
	tableRule := "不得返回 table"
	if input.TargetTable {
		tableRule = fmt.Sprintf("table.targetId 必须等于 %q", input.Table.ID)
	}
	return fmt.Sprintf("上一次输出未通过本地可信边界校验：%v。请先按目标检查仍缺属性，只修复 missingFields，并原样保留其他 current 值，然后重新生成完整 JSON，不要解释。各目标仍缺属性：%s。columns.targetId 必须与以下数组一一对应，保持数量一致并且每个 ID 恰好出现一次：%s；%s。", validationErr, encodedMissing, encodedColumnIDs, tableRule)
}

// completionProviderResult 合并纠错前后的令牌用量，确保元数据任务审计覆盖真实消耗。
func completionProviderResult(result aiplatform.InvocationResult, output CompletionOutput, previous Usage) ProviderResult {
	usage := invocationUsage(result)
	usage.PromptTokens += previous.PromptTokens
	usage.CompletionTokens += previous.CompletionTokens
	usage.TotalTokens += previous.TotalTokens
	return ProviderResult{Output: output, Model: result.ProviderResult.Model, Usage: usage}
}

func invocationUsage(result aiplatform.InvocationResult) Usage {
	return Usage{
		PromptTokens:     result.ProviderResult.Usage.PromptTokens,
		CompletionTokens: result.ProviderResult.Usage.CompletionTokens,
		TotalTokens:      result.ProviderResult.Usage.TotalTokens,
	}
}

// translateOrchestrationError 保持元数据 API 已发布的超时和非法输出错误合同。
func translateOrchestrationError(err error) error {
	var providerErr *aiplatform.ProviderError
	if !errors.As(err, &providerErr) {
		return err
	}
	switch providerErr.Code {
	case aiplatform.ErrorCodeTimeout:
		return errors.Join(context.DeadlineExceeded, err)
	case aiplatform.ErrorCodeInvalidOutput:
		return errors.Join(ErrInvalidOutput, err)
	default:
		return err
	}
}

// ensureJSONEOF 确保结构化输出后不存在第二个 JSON 值。
func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("结构化输出包含尾随 JSON")
		}
		return fmt.Errorf("读取结构化输出尾部失败: %w", err)
	}
	return nil
}

// firstNonBlank 返回第一个非空白字符串。
func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
