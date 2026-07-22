package metriccandidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

const maxEnrichmentCandidates = 64

type EnrichmentInvoker interface {
	Configured() bool
	Model() string
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type Enricher struct {
	invoker EnrichmentInvoker
	timeout time.Duration
}

func NewEnricher(invoker EnrichmentInvoker, timeout time.Duration) *Enricher {
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	return &Enricher{invoker: invoker, timeout: timeout}
}

type enrichmentItem struct {
	Fingerprint       string   `json:"fingerprint"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Caliber           string   `json:"caliber"`
	PeriodDescription string   `json:"periodDescription"`
	LineageSummary    string   `json:"lineageSummary"`
	Tags              []string `json:"tags"`
}

type enrichmentOutput struct {
	Items []enrichmentItem `json:"items"`
}

type enrichmentCandidateInput struct {
	Fingerprint       string   `json:"fingerprint"`
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	SourceFieldCode   string   `json:"sourceFieldCode"`
	Aggregation       string   `json:"aggregation"`
	Unit              string   `json:"unit"`
	Dimensions        []string `json:"dimensions"`
	Period            string   `json:"period"`
	DeterministicRule string   `json:"deterministicRule"`
}

type enrichmentInput struct {
	DatasetName        string                     `json:"datasetName"`
	DatasetDescription string                     `json:"datasetDescription"`
	OutputGrain        dataset.OutputGrain        `json:"outputGrain"`
	Candidates         []enrichmentCandidateInput `json:"candidates"`
}

const enrichmentSystemPrompt = `你负责补全指标候选的可检索业务语义。输入中的聚合、维度、周期和来源均是服务端规则锁定的事实，不得修改或虚构。
对每个 fingerprint 返回且只返回一项：
1. name 是清晰的中文指标名称；description 是简洁业务说明。
2. caliber 必须准确解释计算口径、聚合和空值规则，不得增加输入中没有的过滤条件、去重条件或业务规则。
3. periodDescription 只把 period 翻译成业务可读周期；NONE 表示无固定周期。
4. lineageSummary 只描述发布数据集、逻辑源字段和聚合关系，不得猜测数据库、物理表、SQL 或组织信息。
5. tags 提供 3 到 16 个适合中文语义检索的短标签，覆盖业务主题、口径、周期和主要维度；不要包含内部 UUID。
输出严格遵循 JSON Schema，不输出解释。`

// Enrich attaches deterministic semantic facts first and then lets the LLM improve wording only.
// A provider failure never prevents publication-derived candidates from being persisted.
func (e *Enricher) Enrich(ctx context.Context, tenantID, actorID string, version dataset.VersionRecord, result ExtractionResult) (ExtractionResult, error) {
	result = attachDefaultSemantics(version, result)
	if len(result.Candidates) == 0 || e == nil || e.invoker == nil || !e.invoker.Configured() || strings.TrimSpace(actorID) == "" {
		return result, nil
	}
	limit := len(result.Candidates)
	if limit > maxEnrichmentCandidates {
		limit = maxEnrichmentCandidates
		for index := limit; index < len(result.Candidates); index++ {
			result.Candidates[index].Semantic.Source = "RULE_FALLBACK"
			result.Candidates[index].Semantic.ErrorCode = "AI_ENRICHMENT_BUDGET_EXCEEDED"
		}
	}
	preparedDataset, err := dataset.Prepare(version.DSL)
	if err != nil {
		return result, err
	}
	prompt := enrichmentInput{
		DatasetName: preparedDataset.Document.Dataset.Name, DatasetDescription: preparedDataset.Document.Dataset.Description,
		OutputGrain: preparedDataset.Document.OutputGrain, Candidates: make([]enrichmentCandidateInput, 0, limit),
	}
	fingerprints := make([]string, 0, limit)
	for index := 0; index < limit; index++ {
		draft := result.Candidates[index]
		fingerprints = append(fingerprints, draft.Fingerprint)
		prompt.Candidates = append(prompt.Candidates, enrichmentCandidateInput{
			Fingerprint: draft.Fingerprint, Name: draft.Semantic.Name, Description: draft.Semantic.Description,
			SourceFieldCode: draft.SourceFieldCode, Aggregation: draft.Definition.Aggregation, Unit: draft.Definition.Unit,
			Dimensions: append([]string(nil), draft.Semantic.Dimensions...), Period: draft.Semantic.Period,
			DeterministicRule: draft.Semantic.Caliber,
		})
	}
	userPayload, err := json.Marshal(prompt)
	if err != nil {
		return result, err
	}
	inputSum := sha256.Sum256(userPayload)
	inputHash := hex.EncodeToString(inputSum[:])
	schema, err := json.Marshal(enrichmentSchema(fingerprints, limit))
	if err != nil {
		return result, err
	}
	temperature := 0.0
	request := aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: enrichmentSystemPrompt}}},
			{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{Type: aiplatform.ContentTypeText, Text: string(userPayload)}}},
		},
		ResponseSchema: aiplatform.JSONSchema{Name: "metric_candidate_enrichment", Description: "指标候选的业务语义与检索标签", Schema: schema},
		Temperature:    &temperature, MaxOutputTokens: 8000,
	}
	callCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	invocation, invokeErr := e.invoker.Invoke(callCtx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID, Purpose: aiplatform.PurposeMetricAuthoring,
		PromptVersion: MetricEnrichmentPromptVersion, ResourceType: "DATASET_VERSION", ResourceID: version.ID, Request: request,
	})
	if invokeErr != nil {
		code := string(aiplatform.ClassifyError(invokeErr).Code)
		for index := 0; index < limit; index++ {
			result.Candidates[index].Semantic.Source = "RULE_FALLBACK"
			result.Candidates[index].Semantic.ErrorCode = code
		}
		return result, invokeErr
	}
	var output enrichmentOutput
	decoder := json.NewDecoder(strings.NewReader(string(invocation.ProviderResult.Content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return markEnrichmentFallback(result, limit, "AI_INVALID_OUTPUT"), err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return markEnrichmentFallback(result, limit, "AI_INVALID_OUTPUT"), ErrInvalidRequest
	}
	byFingerprint := make(map[string]enrichmentItem, len(output.Items))
	for _, item := range output.Items {
		if _, exists := byFingerprint[item.Fingerprint]; exists || !validEnrichmentItem(item) {
			return markEnrichmentFallback(result, limit, "AI_INVALID_OUTPUT"), ErrInvalidRequest
		}
		byFingerprint[item.Fingerprint] = item
	}
	if len(byFingerprint) != limit {
		return markEnrichmentFallback(result, limit, "AI_INVALID_OUTPUT"), ErrInvalidRequest
	}
	for index := 0; index < limit; index++ {
		draft := &result.Candidates[index]
		item, exists := byFingerprint[draft.Fingerprint]
		if !exists {
			return markEnrichmentFallback(result, limit, "AI_INVALID_OUTPUT"), ErrInvalidRequest
		}
		draft.Semantic.Name = strings.TrimSpace(item.Name)
		draft.Semantic.Description = strings.TrimSpace(item.Description)
		draft.Semantic.Caliber = strings.TrimSpace(item.Caliber)
		draft.Semantic.PeriodDescription = strings.TrimSpace(item.PeriodDescription)
		draft.Semantic.LineageSummary = strings.TrimSpace(item.LineageSummary)
		draft.Semantic.Tags = nonEmptyUnique(append(draft.Semantic.Tags, item.Tags...), 16, 32)
		draft.Semantic.Source = "HYBRID"
		draft.Semantic.Model = e.invoker.Model()
		draft.Semantic.PromptVersion = MetricEnrichmentPromptVersion
		draft.Semantic.InputHash = inputHash
		draft.Semantic.RequestID = invocation.RequestID
		draft.Semantic.ErrorCode = ""
	}
	return result, nil
}

func enrichmentSchema(fingerprints []string, count int) map[string]any {
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"items"},
		"properties": map[string]any{"items": map[string]any{
			"type": "array", "minItems": count, "maxItems": count,
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"fingerprint", "name", "description", "caliber", "periodDescription", "lineageSummary", "tags"},
				"properties": map[string]any{
					"fingerprint": map[string]any{"type": "string", "enum": fingerprints}, "name": text(200),
					"description": text(1000), "caliber": text(1200), "periodDescription": text(200),
					"lineageSummary": text(800), "tags": map[string]any{
						"type": "array", "minItems": 3, "maxItems": 16,
						"items": map[string]any{"type": "string", "minLength": 1, "maxLength": 32},
					},
				},
			},
		}},
	}
}

func validEnrichmentItem(value enrichmentItem) bool {
	return validSemanticText(value.Name, 200) && validSemanticText(value.Description, 1000) &&
		validSemanticText(value.Caliber, 1200) && validSemanticText(value.PeriodDescription, 200) &&
		validSemanticText(value.LineageSummary, 800) && len(nonEmptyUnique(value.Tags, 16, 32)) >= 3
}

func validSemanticText(value string, maximum int) bool {
	value = strings.TrimSpace(value)
	return value != "" && len([]rune(value)) <= maximum && !hasControl(value)
}

func markEnrichmentFallback(result ExtractionResult, limit int, code string) ExtractionResult {
	for index := 0; index < limit && index < len(result.Candidates); index++ {
		result.Candidates[index].Semantic.Source = "RULE_FALLBACK"
		result.Candidates[index].Semantic.ErrorCode = code
	}
	return result
}
