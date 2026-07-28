package datasetsemanticnaming

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/dataset"
)

const systemPrompt = `你是企业数仓 DWD/DWS/ADS 语义命名助手。输入只包含元数据，不包含业务样本行。
你必须根据数据集业务说明、精确聚合表达式、字段角色、输出粒度、上游已发布数据集及受控标签词表，完成一次保存前语义命名：
1. dataset.code 是逻辑数据集编码（小写 snake_case），dataset.name 是中文业务表名；禁止生成物理 schema、物理表名、视图名或 SQL。
2. fields 是以输入字段 id 为键的对象，每个输出字段必须返回且只返回一次。字段 code 使用稳定、可理解的小写 snake_case，name 使用准确中文业务名。维度名体现业务属性；聚合字段必须同时体现聚合函数、参数语义和实体语义，不能使用“字段N”“某某指标”“metric”等空泛占位命名。COUNT(出生日期) 表示出生日期非空记录数，不能臆断成总员工数；只有无参数的 COUNT(*) 才表示当前输入粒度的总行数，并应结合上游输出粒度命名为员工总人数、订单总数等准确实体数量。
3. 不得改变字段 id、计算表达式、类型、角色、聚合函数、DAG、粒度、领域或主题。
4. tags 只能从 controlledTaxonomy 里按 tagId 选择，不得创建、改写或批准标签；只返回有充分元数据证据的标签，最多 16 个。
5. confidence 表示证据支持度；rationale 只能概括元数据依据，不得包含业务数据值、凭据、SQL 或原始行。
输出只能是 JSON Schema 指定的对象。`

var logicalCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type Generator struct {
	catalog Catalog
	invoker Invoker
	timeout time.Duration
}

func NewGenerator(catalog Catalog, invoker Invoker, timeout time.Duration) *Generator {
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &Generator{catalog: catalog, invoker: invoker, timeout: timeout}
}

func (generator *Generator) Configured() bool {
	return generator != nil && generator.catalog != nil &&
		generator.invoker != nil && generator.invoker.Configured()
}

func (generator *Generator) Enrich(
	ctx context.Context,
	tenantID, actorID, resourceID string,
	document dataset.Document,
	lockDatasetCode bool,
) (dataset.SemanticNamingResult, error) {
	if !generator.Configured() {
		return dataset.SemanticNamingResult{}, dataset.ErrSemanticNamingUnavailable
	}
	if document.Dataset.Layer != dataset.LayerDWD &&
		document.Dataset.Layer != dataset.LayerDWS &&
		document.Dataset.Layer != dataset.LayerADS {
		return dataset.SemanticNamingResult{}, fmt.Errorf(
			"%w: only DWD/DWS/ADS drafts are supported", dataset.ErrSemanticNamingInvalid,
		)
	}
	governed, err := generator.catalog.Load(ctx, tenantID, document)
	if err != nil {
		return dataset.SemanticNamingResult{}, fmt.Errorf(
			"%w: governed context unavailable: %v", dataset.ErrSemanticNamingUnavailable, err,
		)
	}
	input, err := buildInput(document, governed)
	if err != nil {
		return dataset.SemanticNamingResult{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return dataset.SemanticNamingResult{}, err
	}
	if len(payload) == 0 || len(payload) > MaxInputBytes {
		return dataset.SemanticNamingResult{}, fmt.Errorf(
			"%w: naming input exceeds safety limit", dataset.ErrSemanticNamingInvalid,
		)
	}
	schema, err := outputSchema(document, governed.Taxonomy, lockDatasetCode)
	if err != nil {
		return dataset.SemanticNamingResult{}, err
	}
	temperature := 0.0
	callCtx, cancel := context.WithTimeout(ctx, generator.timeout)
	defer cancel()
	invocation, err := generator.invoker.Invoke(callCtx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeDatasetSemanticNaming,
		PromptVersion: PromptVersion,
		ResourceType:  "DATASET_DRAFT_SAVE", ResourceID: resourceID,
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{Role: aiplatform.MessageRoleSystem, Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: systemPrompt,
				}}},
				{Role: aiplatform.MessageRoleUser, Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: string(payload),
				}}},
			},
			ResponseSchema: aiplatform.JSONSchema{
				Name:        "dataset_semantic_naming",
				Description: "DWD/DWS/ADS 保存前的逻辑表、输出字段和受控标签语义命名",
				Schema:      schema,
			},
			Temperature:     &temperature,
			MaxOutputTokens: 8192,
		},
	})
	if err != nil {
		return dataset.SemanticNamingResult{}, fmt.Errorf(
			"%w: %v", dataset.ErrSemanticNamingUnavailable, err,
		)
	}
	output, err := decodeOutput(invocation.ProviderResult.Content)
	if err != nil {
		return dataset.SemanticNamingResult{}, err
	}
	renamed, tags, err := applyOutput(document, output, governed.Taxonomy, lockDatasetCode)
	if err != nil {
		return dataset.SemanticNamingResult{}, err
	}
	return dataset.SemanticNamingResult{
		Document: renamed, AIRequestID: invocation.RequestID,
		PromptVersion: PromptVersion, Tags: tags,
	}, nil
}

func buildInput(document dataset.Document, governed Context) (Input, error) {
	if len(document.Fields) == 0 || len(document.Fields) > MaxFields ||
		len(governed.Upstreams) > MaxUpstreams ||
		len(governed.Taxonomy) == 0 || len(governed.Taxonomy) > MaxTaxonomyTags {
		return Input{}, fmt.Errorf("%w: semantic naming input is incomplete", dataset.ErrSemanticNamingInvalid)
	}
	input := Input{
		Dataset: DatasetContext{
			Code: document.Dataset.Code, Name: document.Dataset.Name,
			Description: document.Dataset.Description, Domain: document.Dataset.Domain,
			Subject: document.Dataset.Subject, Type: document.Dataset.Type,
			Layer:       string(document.Dataset.Layer),
			OutputGrain: document.OutputGrain.Description,
		},
		GroupBy: append([]string(nil), document.GroupBy...),
		Context: governed,
	}
	for _, node := range document.Nodes {
		input.Nodes = append(input.Nodes, NodeContext{
			ID: node.ID, Type: node.Type, Alias: node.Alias,
			DatasetVersionID: node.DatasetVersionID,
			Projection:       append([]string(nil), node.Projection...),
		})
	}
	for _, field := range document.Fields {
		expression, err := json.Marshal(field.Expression)
		if err != nil {
			return Input{}, err
		}
		input.Fields = append(input.Fields, FieldContext{
			ID: field.ID, Code: field.Code, Name: field.Name,
			Description: field.Description, Role: field.Role,
			CanonicalType: field.CanonicalType, SemanticType: field.SemanticType,
			Aggregation: field.Aggregation, Expression: expression,
		})
	}
	upstreamFieldCount := 0
	for _, upstream := range governed.Upstreams {
		upstreamFieldCount += len(upstream.Fields)
	}
	if upstreamFieldCount > MaxUpstreamFields {
		return Input{}, fmt.Errorf("%w: upstream metadata exceeds safety limit", dataset.ErrSemanticNamingInvalid)
	}
	return input, nil
}

func outputSchema(
	document dataset.Document,
	taxonomy []TaxonomyTag,
	lockDatasetCode bool,
) (json.RawMessage, error) {
	fieldIDs := make([]string, 0, len(document.Fields))
	for _, field := range document.Fields {
		fieldIDs = append(fieldIDs, field.ID)
	}
	tagIDs := make([]string, 0, len(taxonomy))
	for _, tag := range taxonomy {
		tagIDs = append(tagIDs, tag.ID)
	}
	sort.Strings(fieldIDs)
	sort.Strings(tagIDs)
	codeSchema := map[string]any{
		"type": "string", "minLength": 1, "maxLength": 63,
		"pattern": "^[a-z][a-z0-9_]{0,62}$",
	}
	if lockDatasetCode {
		codeSchema = map[string]any{"type": "string", "enum": []string{document.Dataset.Code}}
	}
	fieldProperties := make(map[string]any, len(fieldIDs))
	for _, fieldID := range fieldIDs {
		fieldProperties[fieldID] = map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"code", "name", "description"},
			"properties": map[string]any{
				"code": map[string]any{
					"type": "string", "minLength": 1, "maxLength": 63,
					"pattern": "^[a-z][a-z0-9_]{0,62}$",
				},
				"name":        map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
				"description": map[string]any{"type": "string", "maxLength": 2000},
			},
		}
	}
	properties := map[string]any{
		"dataset": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"code", "name", "description"},
			"properties": map[string]any{
				"code":        codeSchema,
				"name":        map[string]any{"type": "string", "minLength": 1, "maxLength": 200},
				"description": map[string]any{"type": "string", "maxLength": 2000},
			},
		},
		"fields": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": fieldIDs, "properties": fieldProperties,
		},
		"tags": map[string]any{
			"type": "array", "minItems": 0, "maxItems": min(MaxSuggestions, len(tagIDs)),
			"items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"tagId", "confidence", "rationale"},
				"properties": map[string]any{
					"tagId": map[string]any{"type": "string", "enum": tagIDs},
					"confidence": map[string]any{
						"type": "number", "minimum": 0, "maximum": 1,
					},
					"rationale": map[string]any{
						"type": "string", "minLength": 1, "maxLength": 1024,
					},
				},
			},
		},
	}
	raw, err := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false,
		"required":   []string{"dataset", "fields", "tags"},
		"properties": properties,
	})
	return raw, err
}

func decodeOutput(raw json.RawMessage) (providerOutput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var output providerOutput
	if err := decoder.Decode(&output); err != nil {
		return providerOutput{}, fmt.Errorf("%w: malformed provider output", dataset.ErrSemanticNamingInvalid)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return providerOutput{}, fmt.Errorf("%w: trailing provider output", dataset.ErrSemanticNamingInvalid)
	}
	return output, nil
}

func applyOutput(
	document dataset.Document,
	output providerOutput,
	taxonomy []TaxonomyTag,
	lockDatasetCode bool,
) (dataset.Document, []dataset.SemanticTagSuggestion, error) {
	output.Dataset.Code = strings.TrimSpace(output.Dataset.Code)
	output.Dataset.Name = strings.TrimSpace(output.Dataset.Name)
	output.Dataset.Description = strings.TrimSpace(output.Dataset.Description)
	if !logicalCodePattern.MatchString(output.Dataset.Code) ||
		!boundedRequired(output.Dataset.Name, 200) ||
		!boundedOptional(output.Dataset.Description, 2000) ||
		(lockDatasetCode && output.Dataset.Code != document.Dataset.Code) {
		return dataset.Document{}, nil, fmt.Errorf("%w: invalid dataset naming", dataset.ErrSemanticNamingInvalid)
	}
	if len(output.Fields) != len(document.Fields) {
		return dataset.Document{}, nil, fmt.Errorf("%w: incomplete field naming", dataset.ErrSemanticNamingInvalid)
	}
	for _, field := range document.Fields {
		naming, exists := output.Fields[field.ID]
		naming.Code = strings.TrimSpace(naming.Code)
		naming.Name = strings.TrimSpace(naming.Name)
		naming.Description = strings.TrimSpace(naming.Description)
		if !exists || !logicalCodePattern.MatchString(naming.Code) ||
			!boundedRequired(naming.Name, 200) || !boundedOptional(naming.Description, 2000) {
			return dataset.Document{}, nil, fmt.Errorf("%w: invalid field naming", dataset.ErrSemanticNamingInvalid)
		}
		output.Fields[field.ID] = naming
	}
	usedCodes := make(map[string]bool, len(output.Fields))
	codeRemap := make(map[string]string, len(document.Fields))
	nameRemap := make(map[string]string, len(document.Fields))
	for index := range document.Fields {
		naming := output.Fields[document.Fields[index].ID]
		naming.Code = uniqueLogicalCode(naming.Code, usedCodes)
		oldCode := document.Fields[index].Code
		codeRemap[oldCode] = naming.Code
		nameRemap[oldCode] = naming.Name
		document.Fields[index].Code = naming.Code
		document.Fields[index].Name = naming.Name
		document.Fields[index].Description = naming.Description
	}
	document.Dataset.Code = output.Dataset.Code
	document.Dataset.Name = output.Dataset.Name
	document.Dataset.Description = output.Dataset.Description
	remapSemanticReferences(&document, codeRemap)
	remapTransformOutputs(&document, codeRemap, nameRemap)
	document.Designer = remapDesigner(document.Designer, codeRemap, nameRemap)

	taxonomyByID := make(map[string]TaxonomyTag, len(taxonomy))
	for _, tag := range taxonomy {
		taxonomyByID[tag.ID] = tag
	}
	seenTags := map[string]bool{}
	tags := make([]dataset.SemanticTagSuggestion, 0, len(output.Tags))
	if len(output.Tags) > MaxSuggestions {
		return dataset.Document{}, nil, fmt.Errorf("%w: too many tag suggestions", dataset.ErrSemanticNamingInvalid)
	}
	for _, choice := range output.Tags {
		choice.TagID = strings.TrimSpace(choice.TagID)
		choice.Rationale = strings.TrimSpace(choice.Rationale)
		tag, exists := taxonomyByID[choice.TagID]
		if !exists || choice.Confidence < 0 ||
			choice.Confidence > 1 || !boundedRequired(choice.Rationale, 1024) {
			return dataset.Document{}, nil, fmt.Errorf("%w: invalid controlled tag choice", dataset.ErrSemanticNamingInvalid)
		}
		if seenTags[choice.TagID] {
			continue
		}
		seenTags[choice.TagID] = true
		tags = append(tags, dataset.SemanticTagSuggestion{
			TagID: tag.ID, TagCode: tag.Code, TagName: tag.Name,
			Category: tag.Category, Confidence: choice.Confidence,
			Rationale: choice.Rationale,
		})
	}
	return document, tags, nil
}

func uniqueLogicalCode(preferred string, used map[string]bool) string {
	if !used[preferred] {
		used[preferred] = true
		return preferred
	}
	for suffix := 2; suffix < 10_000; suffix++ {
		tail := fmt.Sprintf("_%d", suffix)
		base := preferred
		if len(base)+len(tail) > 63 {
			base = base[:63-len(tail)]
		}
		candidate := base + tail
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
	return preferred
}

func remapSemanticReferences(document *dataset.Document, remap map[string]string) {
	remapOne := func(value string) string {
		if replacement, exists := remap[value]; exists {
			return replacement
		}
		return value
	}
	remapMany := func(values []string) {
		for index := range values {
			values[index] = remapOne(values[index])
		}
	}
	remapMany(document.OutputGrain.KeyFields)
	document.OutputGrain.TimeField = remapOne(document.OutputGrain.TimeField)
	if document.Dataset.Grain != nil {
		remapMany(document.Dataset.Grain.KeyFields)
		document.Dataset.Grain.TimeField = remapOne(document.Dataset.Grain.TimeField)
	}
	if document.FactContract != nil {
		remapMany(document.FactContract.GrainKeyFields)
		document.FactContract.EventTimeField = remapOne(document.FactContract.EventTimeField)
		for index := range document.FactContract.AtomicMeasures {
			document.FactContract.AtomicMeasures[index].Field =
				remapOne(document.FactContract.AtomicMeasures[index].Field)
		}
	}
	if document.AnalysisContract != nil {
		remapMany(document.AnalysisContract.CommonGrainFields)
		remapMany(document.AnalysisContract.ConformedDimensions)
		document.AnalysisContract.TimeField = remapOne(document.AnalysisContract.TimeField)
		for index := range document.AnalysisContract.Measures {
			document.AnalysisContract.Measures[index].Field =
				remapOne(document.AnalysisContract.Measures[index].Field)
		}
	}
}

func remapTransformOutputs(
	document *dataset.Document,
	codeRemap, nameRemap map[string]string,
) {
	for transformIndex := range document.Transforms {
		for ruleIndex := range document.Transforms[transformIndex].Rules {
			output := &document.Transforms[transformIndex].Rules[ruleIndex].Output
			oldCode := output.Code
			if replacement, exists := codeRemap[oldCode]; exists {
				output.Code = replacement
				if name, named := nameRemap[oldCode]; named {
					output.Name = name
				}
			}
		}
	}
}

func remapDesigner(
	designer map[string]any,
	codeRemap, nameRemap map[string]string,
) map[string]any {
	if designer == nil {
		return nil
	}
	var walk func(any) any
	walk = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			if code, ok := typed["code"].(string); ok {
				if replacement, exists := codeRemap[code]; exists {
					typed["code"] = replacement
					if _, hasName := typed["name"]; hasName {
						typed["name"] = nameRemap[code]
					}
				}
			}
			for key, child := range typed {
				typed[key] = walk(child)
			}
			return typed
		case []any:
			for index := range typed {
				typed[index] = walk(typed[index])
			}
			return typed
		default:
			return value
		}
	}
	return walk(designer).(map[string]any)
}

func boundedRequired(value string, maximum int) bool {
	return value != "" && boundedOptional(value, maximum)
}

func boundedOptional(value string, maximum int) bool {
	return utf8.RuneCountInString(value) <= maximum
}
