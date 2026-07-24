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

// SemanticContextLoader resolves reviewed business metadata only. It never returns
// sampled rows, credentials or physical SQL.
type SemanticContextLoader interface {
	LoadMetricSemanticContext(context.Context, string, []SemanticContextRequest) ([]SemanticTableContext, error)
}

type SemanticContextRequest struct {
	TableID     string
	ColumnNames []string
}

type SemanticTableContext struct {
	TableID     string                  `json:"-"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Columns     []SemanticColumnContext `json:"columns"`
}

type SemanticColumnContext struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	SemanticType  string `json:"semanticType"`
	CanonicalType string `json:"canonicalType"`
}

type Enricher struct {
	invoker EnrichmentInvoker
	timeout time.Duration
	context SemanticContextLoader
}

func NewEnricher(invoker EnrichmentInvoker, timeout time.Duration, loaders ...SemanticContextLoader) *Enricher {
	if timeout <= 0 {
		timeout = 25 * time.Second
	}
	enricher := &Enricher{invoker: invoker, timeout: timeout}
	if len(loaders) > 0 {
		enricher.context = loaders[0]
	}
	return enricher
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
	SourceFieldName   string   `json:"sourceFieldName"`
	SourceDescription string   `json:"sourceDescription"`
	SourceRole        string   `json:"sourceRole"`
	SourceSemantic    string   `json:"sourceSemanticType"`
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
	SourceTables       []SemanticTableContext     `json:"sourceTables"`
	FixedFilters       []string                   `json:"fixedFilters"`
	BusinessHints      []string                   `json:"businessHints"`
	Candidates         []enrichmentCandidateInput `json:"candidates"`
}

const enrichmentSystemPrompt = `你是企业级指标语义专家，负责把服务端锁定的指标事实改写为专业、准确、易懂的业务定义。输入中的聚合、维度、周期、单位、固定过滤和来源均是不可修改的事实；源表与源字段业务元数据是解释业务含义的依据，不是让你新增过滤条件的授权。
对每个 fingerprint 返回且只返回一项：
1. name 使用业务人员熟悉的中文名，不出现字段编码、SQL、DAG 等技术词。
2. description 用一句话回答“这个指标衡量什么”：优先说明统计主体、时间范围、业务动作或状态、统计对象和结果含义。例如订单计数应表达为“统计……的订单数量”，不能写成“由某数据集按 COUNT_DISTINCT 计算输出”。description 不出现数据集 ID、字段编码、聚合函数名、空值规则、血缘或实现过程。
3. 只有当固定过滤、字段业务说明、数据集说明或画板业务提示明确支持时，才能写“支付成功”“已完成”“有效”等状态；不得凭常识虚构未实现的过滤条件。名称或粒度说明中的周期与锁定 period 冲突时，以锁定 period 为准，不得掩盖冲突。
4. caliber 用业务语言准确解释计算对象、去重/汇总方式、固定过滤和空值规则。可在括号中标注聚合函数，但不得把已经在 DAG 中完成的 COUNT_DISTINCT/SUM 错写成“直接取值、不进行聚合”。
5. unit 已由服务端依据字段事实和通用计量规则锁定：订单、交易、支付、退款、发票等业务单据计数通常为“笔”，明细记录数为“条”，其他实体计数通常为“个”。你不得修改单位，但描述与 caliber 应使用相符量词。
6. periodDescription 只翻译锁定 period；NONE 表示当前执行定义没有固定统计周期。
7. lineageSummary 只描述发布数据集、业务输出和真实计算关系，不猜测数据库、SQL 或组织信息。
8. tags 提供 3 到 16 个中文短标签，覆盖业务主题、口径、周期和主要维度，不包含内部 UUID 或字段编码。
输出严格遵循 JSON Schema，不输出解释。`

// Enrich attaches deterministic semantic facts first and then lets the LLM improve wording only.
// A provider failure never prevents publication-derived candidates from being persisted.
func (e *Enricher) Enrich(ctx context.Context, tenantID, actorID string, version dataset.VersionRecord, result ExtractionResult) (ExtractionResult, error) {
	result = attachDefaultSemantics(version, result)
	if len(result.Candidates) == 0 || e == nil || e.invoker == nil || !e.invoker.Configured() || strings.TrimSpace(actorID) == "" {
		return result, nil
	}
	preparedDataset, err := dataset.Prepare(version.DSL)
	if err != nil {
		return result, err
	}
	contextRequests := semanticContextRequests(preparedDataset.Document)
	sourceTables := []SemanticTableContext{}
	if e.context != nil && len(contextRequests) > 0 {
		loaded, loadErr := e.context.LoadMetricSemanticContext(ctx, tenantID, contextRequests)
		if loadErr == nil {
			sourceTables = loaded
		} else {
			result.Warnings = append(result.Warnings, "源表业务元数据暂不可用，LLM 已使用数据集 DSL 语义继续补全。")
		}
	}
	var enrichmentErr error
	for start := 0; start < len(result.Candidates); start += maxEnrichmentCandidates {
		end := start + maxEnrichmentCandidates
		if end > len(result.Candidates) {
			end = len(result.Candidates)
		}
		if batchErr := e.enrichBatch(
			ctx, tenantID, actorID, version, preparedDataset.Document, sourceTables, &result, start, end,
		); batchErr != nil {
			enrichmentErr = errors.Join(enrichmentErr, batchErr)
		}
	}
	return result, enrichmentErr
}

func (e *Enricher) enrichBatch(
	ctx context.Context,
	tenantID, actorID string,
	version dataset.VersionRecord,
	document dataset.Document,
	sourceTables []SemanticTableContext,
	result *ExtractionResult,
	start, end int,
) error {
	count := end - start
	prompt := enrichmentInput{
		DatasetName: document.Dataset.Name, DatasetDescription: document.Dataset.Description,
		OutputGrain: document.OutputGrain, SourceTables: sourceTables,
		FixedFilters:  fixedFilterSummaries(document),
		BusinessHints: designerBusinessHints(document.Designer),
		Candidates:    make([]enrichmentCandidateInput, 0, count),
	}
	fingerprints := make([]string, 0, count)
	for index := start; index < end; index++ {
		draft := result.Candidates[index]
		field := dataset.Field{}
		for _, candidateField := range document.Fields {
			if candidateField.ID == draft.SourceFieldID {
				field = candidateField
				break
			}
		}
		fingerprints = append(fingerprints, draft.Fingerprint)
		prompt.Candidates = append(prompt.Candidates, enrichmentCandidateInput{
			Fingerprint: draft.Fingerprint, Name: draft.Semantic.Name, Description: draft.Semantic.Description,
			SourceFieldCode: draft.SourceFieldCode, SourceFieldName: field.Name, SourceDescription: field.Description,
			SourceRole: field.Role, SourceSemantic: field.SemanticType,
			Aggregation: effectiveBusinessAggregation(draft), Unit: draft.Definition.Unit,
			Dimensions: append([]string(nil), draft.Semantic.Dimensions...), Period: draft.Semantic.Period,
			DeterministicRule: draft.Semantic.Caliber,
		})
	}
	userPayload, err := json.Marshal(prompt)
	if err != nil {
		markEnrichmentFallbackRange(result, start, end, "AI_INVALID_INPUT")
		return err
	}
	inputSum := sha256.Sum256(userPayload)
	inputHash := hex.EncodeToString(inputSum[:])
	schema, err := json.Marshal(enrichmentSchema(fingerprints, count))
	if err != nil {
		markEnrichmentFallbackRange(result, start, end, "AI_INVALID_INPUT")
		return err
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
	invocation, invokeErr := e.invoker.Invoke(callCtx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID, Purpose: aiplatform.PurposeMetricAuthoring,
		PromptVersion: MetricEnrichmentPromptVersion, ResourceType: "DATASET_VERSION", ResourceID: version.ID, Request: request,
	})
	cancel()
	if invokeErr != nil {
		code := string(aiplatform.ClassifyError(invokeErr).Code)
		markEnrichmentFallbackRange(result, start, end, code)
		return invokeErr
	}
	var output enrichmentOutput
	decoder := json.NewDecoder(strings.NewReader(string(invocation.ProviderResult.Content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		markEnrichmentFallbackRange(result, start, end, "AI_INVALID_OUTPUT")
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		markEnrichmentFallbackRange(result, start, end, "AI_INVALID_OUTPUT")
		return ErrInvalidRequest
	}
	byFingerprint := make(map[string]enrichmentItem, len(output.Items))
	for _, item := range output.Items {
		if _, exists := byFingerprint[item.Fingerprint]; exists || !validEnrichmentItem(item) {
			markEnrichmentFallbackRange(result, start, end, "AI_INVALID_OUTPUT")
			return ErrInvalidRequest
		}
		byFingerprint[item.Fingerprint] = item
	}
	if len(byFingerprint) != count {
		markEnrichmentFallbackRange(result, start, end, "AI_INVALID_OUTPUT")
		return ErrInvalidRequest
	}
	for index := start; index < end; index++ {
		item, exists := byFingerprint[result.Candidates[index].Fingerprint]
		if !exists {
			markEnrichmentFallbackRange(result, start, end, "AI_INVALID_OUTPUT")
			return ErrInvalidRequest
		}
		if !professionalDescription(item.Description) {
			markEnrichmentFallbackRange(result, start, end, "AI_DESCRIPTION_UNPROFESSIONAL")
			return ErrInvalidRequest
		}
	}
	for index := start; index < end; index++ {
		draft := &result.Candidates[index]
		item := byFingerprint[draft.Fingerprint]
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
	return nil
}

func semanticContextRequests(document dataset.Document) []SemanticContextRequest {
	result := make([]SemanticContextRequest, 0, len(document.Nodes))
	for _, node := range document.Nodes {
		if strings.TrimSpace(node.TableID) == "" {
			continue
		}
		columns := nonEmptyUnique(node.Projection, 64, 128)
		result = append(result, SemanticContextRequest{TableID: node.TableID, ColumnNames: columns})
		if len(result) >= 16 {
			break
		}
	}
	return result
}

func fixedFilterSummaries(document dataset.Document) []string {
	values := []string{}
	for _, node := range document.Nodes {
		for _, filter := range node.SourceFilters {
			value := safeFilterValue(filter.Value)
			values = append(values, strings.TrimSpace(strings.Join([]string{node.Alias, filter.Field, filter.Operator, value}, " ")))
		}
	}
	for _, filter := range append(append([]dataset.Filter(nil), document.Filters...), document.Having...) {
		if filter.Optional {
			continue
		}
		raw, _ := json.Marshal(filter.Expression)
		values = append(values, safeText(string(raw), "固定业务过滤", 500))
	}
	return nonEmptyUnique(values, 32, 500)
}

func safeFilterValue(value any) string {
	switch typed := value.(type) {
	case string:
		return safeText(typed, "", 100)
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, bool, json.Number:
		return safeText(strings.TrimSpace(strings.ToValidUTF8(strings.TrimSpace(toText(typed)), "�")), "", 100)
	default:
		return ""
	}
}

func toText(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}

func designerBusinessHints(designer map[string]any) []string {
	if len(designer) == 0 {
		return nil
	}
	values := []string{}
	var visit func(any, int)
	visit = func(value any, depth int) {
		if depth > 5 || len(values) >= 32 {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "name" {
					if text, ok := child.(string); ok {
						values = append(values, safeText(text, "", 200))
					}
					continue
				}
				if key == "nodeNames" {
					if names, ok := child.(map[string]any); ok {
						for _, name := range names {
							if text, ok := name.(string); ok {
								values = append(values, safeText(text, "", 200))
							}
						}
					}
					continue
				}
				if key == "groups" || key == "joins" || key == "transforms" || key == "end" || key == "metrics" || key == "dimensions" {
					visit(child, depth+1)
				}
			}
		case []any:
			for _, child := range typed {
				visit(child, depth+1)
			}
		case string:
			values = append(values, safeText(typed, "", 200))
		}
	}
	visit(designer, 0)
	return nonEmptyUnique(values, 32, 200)
}

func professionalDescription(value string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if normalized == "" {
		return false
	}
	for _, technical := range []string{
		"DAG", "COUNT_DISTINCT", "COUNT DISTINCT", "SUM(", "AVG(", "MIN(", "MAX(",
		"按 SUM", "按 COUNT", "按 AVG", "按 MIN", "按 MAX", "计算输出",
		"数据集", "逻辑源字段", "字段编码", "空值处理", "直接取值", "不进行聚合", "SQL",
	} {
		if strings.Contains(normalized, strings.ToUpper(technical)) {
			return false
		}
	}
	return true
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

func markEnrichmentFallbackRange(result *ExtractionResult, start, end int, code string) {
	for index := start; index < end && index < len(result.Candidates); index++ {
		result.Candidates[index].Semantic.Source = "RULE_FALLBACK"
		result.Candidates[index].Semantic.ErrorCode = code
	}
}
