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
请依据数据集说明、字段语义、DAG/粒度、技术元数据和精确上游语义摘要选择所有有充分证据的标签，重点覆盖 TABLE_FUNCTION、USAGE_SCOPE、DATA_GRAIN、JOIN_ROLE、BUSINESS_DOMAIN、BUSINESS_ENTITY。
ODS 的 sourceTables 只包含技术/业务元数据，不包含样本行；DWD/DWS 的 upstreams 绑定精确发布版本。不得猜测输入未提供的业务事实，不得从字段编码臆造敏感含义。
每个 tagId 最多返回一次。confidence 表示现有证据对该标签的支持程度；rationale 只简述元数据证据，不得包含业务数据值、凭据、SQL 或原始行。
标签数量由证据决定，可以返回空数组；不要为了凑数输出弱相关标签。输出只能是 JSON Schema 指定的对象。`

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
		Temperature:     &temperature,
		MaxOutputTokens: 32768,
	}
	callCtx, cancel := context.WithTimeout(ctx, generator.timeout)
	defer cancel()
	result, err := generator.invoker.Invoke(callCtx, aiplatform.Invocation{
		TenantID:      claim.TenantID,
		ActorID:       claim.ActorID,
		Purpose:       aiplatform.PurposeDatasetTagSuggestion,
		PromptVersion: PromptVersion,
		ResourceType:  "DATASET_VERSION",
		ResourceID:    claim.DatasetVersionID,
		Request:       request,
	})
	if err != nil {
		return Completion{}, err
	}
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
				"type":        "array",
				"minItems":    0,
				"maxItems":    maxItems,
				"uniqueItems": true,
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
	case "BUSINESS_DOMAIN", "BUSINESS_ENTITY", "TABLE_FUNCTION",
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
