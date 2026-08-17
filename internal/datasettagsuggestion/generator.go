package datasettagsuggestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

const systemPrompt = `你是企业数据治理中的数据集标签建议助手。你只能从输入的 controlledTaxonomy 中选择标签，不能创建、改写或批准标签。
业务领域由当前用户所属领域统一确定，不属于标签；不得生成、改写或推断 BUSINESS_DOMAIN 标签。
请依据数据集说明、字段语义、DAG/粒度、技术元数据和精确上游语义摘要选择所有有充分证据的标签，重点覆盖 TABLE_FUNCTION、BUSINESS_ENTITY、BUSINESS_PROCESS、DATA_GRAIN、USAGE_SCOPE、JOIN_ROLE。不要只返回“事实/维度”作用标签：有证据时必须同时选择精确业务对象、业务过程和精确粒度，让同一业务目标能够召回多个相关事实表或维度表。订单商品、售后工单等复合粒度应优先于多个宽泛实体标签；DWS/ADS 的聚合粒度必须以 DAG.groupBy、outputGrain 和输出键为准。
ODS 的 sourceTables 只包含技术/业务元数据，不包含样本行；DIM/DWD/DWS/ADS 的 upstreams 绑定精确的当前草稿或发布版本。同批次 DWD 在 DIM 发布前可以引用当前 DIM 草稿，但不得把草稿状态推断成业务事实。不得猜测输入未提供的业务事实，不得从字段编码臆造敏感含义。
每个 tagId 最多返回一次。confidence 表示现有证据对该标签的支持程度；rationale 只简述元数据证据，不得包含业务数据值、凭据、SQL 或原始行。
标签数量由证据决定，可以返回空数组；不要为了凑数输出弱相关标签。输出只能是 JSON Schema 指定的对象。`

const maxTagSuggestionRepairContent = 32 << 10

type Generator struct {
	invoker Invoker
	timeout time.Duration
}

func NewGenerator(invoker Invoker, timeout time.Duration) *Generator {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Generator{invoker: invoker, timeout: timeout}
}

func (generator *Generator) Configured() bool {
	return generator != nil && generator.invoker != nil && generator.invoker.Configured()
}

func (generator *Generator) Generate(
	ctx context.Context,
	claim Claim,
	input Input,
) (Completion, error) {
	if !validClaim(claim) || !generator.Configured() || len(input.Taxonomy) == 0 ||
		len(input.Taxonomy) > MaxTaxonomyTags {
		return Completion{}, ErrInvalidRequest
	}
	taxonomy, err := validateTaxonomy(input.Taxonomy)
	if err != nil {
		return Completion{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return Completion{}, err
	}
	if len(payload) == 0 || len(payload) > MaxInputBytes {
		return Completion{}, ErrInputLimit
	}
	inputHash := inputDigest(payload)
	schema, err := suggestionSchema(input.Taxonomy)
	if err != nil {
		return Completion{}, err
	}
	temperature := 0.0
	request := aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{
				Role: aiplatform.MessageRoleSystem,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText,
					Text: systemPrompt,
				}},
			},
			{
				Role: aiplatform.MessageRoleUser,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText,
					Text: string(payload),
				}},
			},
		},
		ResponseSchema: aiplatform.JSONSchema{
			Name:        "dataset_tag_suggestions",
			Description: "从当前租户 ACTIVE CONTROLLED taxonomy 选择数据集标签建议",
			Schema:      schema,
		},
		Temperature: &temperature,
		// 本地 OpenAI-compatible deepseek-v3 端点拒绝超过单次输出上限的
		// 请求。标签只会选取少量受控词条，4096 足够容纳结构化理由。
		MaxOutputTokens: 4096,
	}
	invocation := aiplatform.Invocation{
		TenantID:      claim.TenantID,
		ActorID:       claim.ActorID,
		Purpose:       aiplatform.PurposeDatasetTagSuggestion,
		PromptVersion: PromptVersion,
		ResourceType:  "DATASET_VERSION",
		ResourceID:    claim.DatasetVersionID,
		Request:       request,
	}
	result, invokeErr := generator.invoke(ctx, invocation)
	if invokeErr == nil {
		completion, outputErr := tagSuggestionCompletion(result, taxonomy, inputHash)
		if outputErr == nil {
			return completion, nil
		}
		invokeErr = fmt.Errorf("%w: %v", ErrInvalidOutput, outputErr)
	}
	repairContent, repairDiagnostic, repairable := tagSuggestionRepairDetails(
		result, invokeErr,
	)
	if !repairable {
		return Completion{}, invokeErr
	}
	repair := invocation
	repair.Request.Messages = tagSuggestionRepairMessages(
		request.Messages, repairContent, repairDiagnostic,
	)
	repaired, err := generator.invoke(ctx, repair)
	if err != nil {
		return Completion{}, err
	}
	completion, err := tagSuggestionCompletion(repaired, taxonomy, inputHash)
	if err != nil {
		return Completion{}, fmt.Errorf("%w: repaired provider output", ErrInvalidOutput)
	}
	return completion, nil
}

func (generator *Generator) invoke(
	ctx context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, generator.timeout)
	defer cancel()
	return generator.invoker.Invoke(callCtx, invocation)
}

func tagSuggestionCompletion(
	result aiplatform.InvocationResult,
	taxonomy map[string]TaxonomyTag,
	inputHash string,
) (Completion, error) {
	output, err := decodeProviderOutput(result.ProviderResult.Content)
	if err != nil {
		return Completion{}, err
	}
	suggestions, err := normalizeSuggestions(output.Items, taxonomy)
	if err != nil {
		return Completion{}, err
	}
	canonical, err := canonicalOutput(suggestions)
	if err != nil {
		return Completion{}, err
	}
	outputSum := sha256.Sum256(canonical)
	return Completion{
		AIRequestID: result.RequestID,
		InputHash:   inputHash,
		OutputHash:  hex.EncodeToString(outputSum[:]),
		Suggestions: suggestions,
	}, nil
}

func tagSuggestionRepairDetails(
	result aiplatform.InvocationResult,
	err error,
) (content []byte, diagnostic string, repairable bool) {
	if candidate, safeDiagnostic, ok := aiplatform.InvalidOutputDetails(err); ok {
		return candidate, safeDiagnostic, true
	}
	if errors.Is(err, ErrInvalidOutput) {
		return result.ProviderResult.Content,
			"output violates the controlled-tag response contract", true
	}
	return nil, "", false
}

func tagSuggestionRepairMessages(
	base []aiplatform.Message,
	candidate []byte,
	diagnostic string,
) []aiplatform.Message {
	messages := append([]aiplatform.Message(nil), base...)
	if len(candidate) > 0 && len(candidate) <= maxTagSuggestionRepairContent {
		messages = append(messages, aiplatform.Message{
			Role: aiplatform.MessageRoleAssistant,
			Parts: []aiplatform.ContentPart{{
				Type: aiplatform.ContentTypeText,
				Text: string(candidate),
			}},
		})
	}
	instruction := `上一份标签建议未通过结构校验。请根据最初输入重新输出一份完整 JSON：
1. 根对象只能包含 items；items 可以为空。
2. 每项只能包含 tagId、confidence、rationale，tagId 必须来自 controlledTaxonomy 且不得重复。
3. confidence 必须在 0 到 1 之间；rationale 保持简短。
4. 不要输出 Markdown、解释过程或 JSON 之外的内容。`
	if strings.TrimSpace(diagnostic) != "" {
		instruction += "\n结构诊断：" + strings.TrimSpace(diagnostic)
	}
	return append(messages, aiplatform.Message{
		Role: aiplatform.MessageRoleUser,
		Parts: []aiplatform.ContentPart{{
			Type: aiplatform.ContentTypeText,
			Text: instruction,
		}},
	})
}

func inputDigest(payload []byte) string {
	inputSum := sha256.Sum256(append([]byte(PromptVersion+"\n"), payload...))
	return hex.EncodeToString(inputSum[:])
}

func suggestionSchema(tags []TaxonomyTag) (json.RawMessage, error) {
	ids := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		if tag.ID == "" || seen[tag.ID] {
			return nil, ErrInvalidRequest
		}
		seen[tag.ID] = true
		ids = append(ids, tag.ID)
	}
	sort.Strings(ids)
	maxItems := min(MaxSuggestions, len(ids))
	document := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"items"},
		"properties": map[string]any{
			"items": map[string]any{
				"type":     "array",
				"minItems": 0,
				"maxItems": maxItems,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"tagId", "confidence", "rationale"},
					"properties": map[string]any{
						"tagId": map[string]any{
							"type": "string",
							"enum": ids,
						},
						"confidence": map[string]any{
							"type":    "number",
							"minimum": 0,
							"maximum": 1,
						},
						"rationale": map[string]any{
							"type":      "string",
							"maxLength": MaxRationaleRunes,
						},
					},
				},
			},
		},
	}
	return json.Marshal(document)
}

func validateTaxonomy(tags []TaxonomyTag) (map[string]TaxonomyTag, error) {
	byID := make(map[string]TaxonomyTag, len(tags))
	aliasCount := 0
	for _, tag := range tags {
		if tag.ID == "" || tag.Code == "" || tag.Name == "" ||
			!suggestedCategory(tag.Category) || len(tag.Aliases)+aliasCount > MaxTaxonomyAliases {
			return nil, ErrInputLimit
		}
		aliasCount += len(tag.Aliases)
		if _, exists := byID[tag.ID]; exists {
			return nil, ErrInvalidRequest
		}
		byID[tag.ID] = tag
	}
	return byID, nil
}

func decodeProviderOutput(raw []byte) (providerOutput, error) {
	if len(raw) == 0 || int64(len(raw)) > aiplatform.MaxProviderResponseBytes {
		return providerOutput{}, ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var output providerOutput
	if err := decoder.Decode(&output); err != nil {
		return providerOutput{}, fmt.Errorf("%w: decode provider output", ErrInvalidRequest)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return providerOutput{}, fmt.Errorf("%w: provider output has trailing content", ErrInvalidRequest)
	}
	if output.Items == nil || len(output.Items) > MaxSuggestions {
		return providerOutput{}, ErrInvalidRequest
	}
	return output, nil
}

func normalizeSuggestions(
	items []providerSuggestion,
	taxonomy map[string]TaxonomyTag,
) ([]Suggestion, error) {
	seen := map[string]bool{}
	result := make([]Suggestion, 0, len(items))
	for _, item := range items {
		tag, exists := taxonomy[item.TagID]
		rationale := strings.TrimSpace(strings.ToValidUTF8(item.Rationale, "�"))
		if !exists || seen[item.TagID] || math.IsNaN(item.Confidence) ||
			math.IsInf(item.Confidence, 0) || item.Confidence < 0 ||
			item.Confidence > 1 || utf8.RuneCountInString(rationale) > MaxRationaleRunes ||
			hasControl(rationale) {
			return nil, ErrInvalidRequest
		}
		seen[item.TagID] = true
		result = append(result, Suggestion{
			TagID: item.TagID, TagCode: tag.Code, TagName: tag.Name,
			Category: tag.Category, Confidence: item.Confidence, Rationale: rationale,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Category == result[j].Category {
			if result[i].TagCode == result[j].TagCode {
				return result[i].TagID < result[j].TagID
			}
			return result[i].TagCode < result[j].TagCode
		}
		return result[i].Category < result[j].Category
	})
	return result, nil
}

func suggestedCategory(value string) bool {
	switch value {
	case "BUSINESS_ENTITY", "BUSINESS_PROCESS", "TABLE_FUNCTION",
		"USAGE_SCOPE", "DATA_GRAIN", "JOIN_ROLE":
		return true
	default:
		return false
	}
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
