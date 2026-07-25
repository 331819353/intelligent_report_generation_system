package semanticqa

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/platform/database"
)

type QuerySlots struct {
	Intent        string
	MetricCode    string
	DimensionCode string
	MemberValue   string
}

type QueryInterpreter interface {
	Interpret(context.Context, string, string, string) (QuerySlots, error)
}

type semanticAIInvoker interface {
	Configured() bool
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type SemanticInterpreter struct {
	store     *PostgresStore
	ai        semanticAIInvoker
	embedding embedding.Provider
}

func NewSemanticInterpreter(
	store *PostgresStore,
	ai semanticAIInvoker,
	provider embedding.Provider,
) *SemanticInterpreter {
	return &SemanticInterpreter{store: store, ai: ai, embedding: provider}
}

type recallCandidate struct {
	SubjectType   string  `json:"subjectType"`
	Code          string  `json:"code,omitempty"`
	DimensionCode string  `json:"dimensionCode,omitempty"`
	Label         string  `json:"label"`
	MemberValue   string  `json:"memberValue,omitempty"`
	Score         float64 `json:"score"`
}

type interpretedSlots struct {
	Intent             string  `json:"intent"`
	MetricCode         string  `json:"metricCode"`
	DimensionCode      string  `json:"dimensionCode"`
	MemberValue        string  `json:"memberValue"`
	Confidence         float64 `json:"confidence"`
	NeedsClarification bool    `json:"needsClarification"`
}

func (interpreter *SemanticInterpreter) Interpret(
	ctx context.Context,
	tenantID, actorID, question string,
) (QuerySlots, error) {
	if interpreter == nil || interpreter.store == nil ||
		strings.TrimSpace(question) == "" {
		return QuerySlots{}, ErrInvalidRequest
	}
	var vector []float32
	if interpreter.embedding != nil && interpreter.embedding.Configured() {
		vectors, err := interpreter.embedding.Embed(ctx, []string{question})
		if err == nil && len(vectors) == 1 {
			vector = vectors[0]
		}
	}
	candidates, err := interpreter.store.recall(ctx, tenantID, question, vector, 24)
	if err != nil {
		return QuerySlots{}, err
	}
	fallback := deterministicSlots(question, candidates)
	llmCandidates := externalRecallCandidates(candidates)
	if interpreter.ai == nil || !interpreter.ai.Configured() ||
		len(llmCandidates) == 0 {
		return fallback, nil
	}
	payload, err := json.Marshal(struct {
		Question   string            `json:"question"`
		Candidates []recallCandidate `json:"candidates"`
	}{Question: question, Candidates: llmCandidates})
	if err != nil {
		return QuerySlots{}, err
	}
	temperature := 0.0
	result, err := interpreter.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: "semantic-query-slots-v1",
		ResourceType:  "SEMANTIC_QUERY", ResourceID: hashText(question),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读语义问答槽位解析器。只能从 candidates 复制 metric code 和 dimension code；memberValue 必须留空并由服务端精确解析。不得发明对象，不得输出 SQL 或 DAG。识别 LOOKUP、METRIC、TREND、COMPARISON、RANKING、DRILLDOWN、DISTRIBUTION、FUNNEL、RETENTION、ANOMALY 或 UNKNOWN。候选不足或歧义时 needsClarification=true，并将无法证明的槽位置空。`,
					}},
				},
				{
					Role: aiplatform.MessageRoleUser,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText, Text: string(payload),
					}},
				},
			},
			ResponseSchema: aiplatform.JSONSchema{
				Name: "semantic_query_slots",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["intent","metricCode","dimensionCode","memberValue","confidence","needsClarification"],
					"properties":{
						"intent":{"enum":["LOOKUP","METRIC","TREND","COMPARISON","RANKING","DRILLDOWN","DISTRIBUTION","FUNNEL","RETENTION","ANOMALY","UNKNOWN"]},
						"metricCode":{"type":"string","maxLength":128},
						"dimensionCode":{"type":"string","maxLength":128},
						"memberValue":{"type":"string","maxLength":1024},
						"confidence":{"type":"number","minimum":0,"maximum":1},
						"needsClarification":{"type":"boolean"}
					}
				}`),
			},
			Temperature: &temperature, MaxOutputTokens: 400,
		},
	})
	if err != nil {
		return fallback, nil
	}
	var output interpretedSlots
	if err := json.Unmarshal(result.ProviderResult.Content, &output); err != nil {
		return fallback, nil
	}
	output.Intent = strings.ToUpper(strings.TrimSpace(output.Intent))
	output.MetricCode = strings.TrimSpace(output.MetricCode)
	output.DimensionCode = strings.TrimSpace(output.DimensionCode)
	output.MemberValue = strings.TrimSpace(output.MemberValue)
	if !validInterpretedSlots(output, llmCandidates) ||
		output.MemberValue != "" {
		return fallback, nil
	}
	slots := QuerySlots{
		Intent: output.Intent, MetricCode: output.MetricCode,
		DimensionCode: output.DimensionCode,
	}
	if fallback.MemberValue != "" {
		slots.MemberValue = fallback.MemberValue
		slots.DimensionCode = fallback.DimensionCode
	}
	return slots, nil
}

func externalRecallCandidates(candidates []recallCandidate) []recallCandidate {
	result := make([]recallCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		// Dimension member values remain in the tenant-local exact path. They
		// are never sent to an external model as recall context; the model may
		// only help select governed metadata such as metric/dimension codes.
		if candidate.SubjectType != "MEMBER" {
			result = append(result, candidate)
		}
	}
	return result
}

func deterministicSlots(
	question string,
	candidates []recallCandidate,
) QuerySlots {
	question = strings.ToLower(question)
	result := QuerySlots{Intent: inferIntent(question)}
	metrics := map[string]bool{}
	dimensions := map[string]bool{}
	members := map[string]recallCandidate{}
	for _, candidate := range candidates {
		labelMatch := strings.Contains(question, strings.ToLower(candidate.Label))
		codeMatch := candidate.Code != "" &&
			strings.Contains(question, strings.ToLower(candidate.Code))
		switch candidate.SubjectType {
		case "METRIC":
			if labelMatch || codeMatch {
				metrics[candidate.Code] = true
			}
		case "DIMENSION":
			if labelMatch || codeMatch {
				dimensions[candidate.Code] = true
			}
		case "MEMBER":
			if strings.Contains(question, strings.ToLower(candidate.MemberValue)) ||
				labelMatch {
				members[candidate.MemberValue] = candidate
			}
		}
	}
	if len(metrics) == 1 {
		for code := range metrics {
			result.MetricCode = code
		}
	}
	if len(members) == 1 {
		for value, candidate := range members {
			result.MemberValue = value
			result.DimensionCode = candidate.DimensionCode
		}
	} else if len(dimensions) == 1 {
		for code := range dimensions {
			result.DimensionCode = code
		}
	}
	return result
}

func inferIntent(question string) string {
	switch {
	case strings.Contains(question, "趋势") || strings.Contains(question, "走势"):
		return "TREND"
	case strings.Contains(question, "同比") || strings.Contains(question, "环比") ||
		strings.Contains(question, "对比"):
		return "COMPARISON"
	case strings.Contains(question, "排行") || strings.Contains(question, "top"):
		return "RANKING"
	case strings.Contains(question, "分布"):
		return "DISTRIBUTION"
	case strings.Contains(question, "漏斗"):
		return "FUNNEL"
	case strings.Contains(question, "留存"):
		return "RETENTION"
	case strings.Contains(question, "异常"):
		return "ANOMALY"
	default:
		return "METRIC"
	}
}

func validInterpretedSlots(
	output interpretedSlots,
	candidates []recallCandidate,
) bool {
	if !oneOf(output.Intent,
		"LOOKUP", "METRIC", "TREND", "COMPARISON", "RANKING",
		"DRILLDOWN", "DISTRIBUTION", "FUNNEL", "RETENTION",
		"ANOMALY", "UNKNOWN",
	) || output.Confidence < 0 || output.Confidence > 1 {
		return false
	}
	metricCodes, dimensionCodes, memberValues := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, candidate := range candidates {
		switch candidate.SubjectType {
		case "METRIC":
			metricCodes[candidate.Code] = true
		case "DIMENSION":
			dimensionCodes[candidate.Code] = true
		case "MEMBER":
			memberValues[candidate.MemberValue] = true
			dimensionCodes[candidate.DimensionCode] = true
		}
	}
	return (output.MetricCode == "" || metricCodes[output.MetricCode]) &&
		(output.DimensionCode == "" || dimensionCodes[output.DimensionCode]) &&
		(output.MemberValue == "" || memberValues[output.MemberValue])
}

func (store *PostgresStore) recall(
	ctx context.Context,
	tenantID, question string,
	vector []float32,
	limit int,
) (items []recallCandidate, err error) {
	items = []recallCandidate{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		vectorLiteral := ""
		if len(vector) > 0 {
			vectorLiteral = formatVector(vector)
		}
		rows, err := tx.Query(ctx, `WITH candidates AS (
				SELECT document.subject_type,
					CASE document.subject_type
					  WHEN 'METRIC_VERSION' THEN metric.code::text
					  WHEN 'DIMENSION' THEN dimension.code::text
					  WHEN 'DATASET_VERSION' THEN dataset.code::text
					  ELSE ''
					END AS code,
					CASE WHEN document.subject_type='DIMENSION_MEMBER'
					  THEN member_dimension.code::text ELSE '' END AS dimension_code,
					CASE document.subject_type
					  WHEN 'METRIC_VERSION' THEN metric.name
					  WHEN 'DIMENSION' THEN dimension.name
					  WHEN 'DIMENSION_MEMBER' THEN member.canonical_label
					  WHEN 'DATASET_VERSION' THEN dataset.name
					  ELSE ''
					END AS label,
					CASE WHEN document.subject_type='DIMENSION_MEMBER'
					  THEN member.normalized_value ELSE '' END AS member_value,
					GREATEST(
					  CASE WHEN $2<>'' AND document.embedding_status='SUCCEEDED'
					    THEN 1-(document.embedding <=> $2::halfvec)
					    ELSE 0 END,
					  CASE
					    WHEN position(lower(CASE document.subject_type
					      WHEN 'METRIC_VERSION' THEN metric.name
					      WHEN 'DIMENSION' THEN dimension.name
					      WHEN 'DIMENSION_MEMBER' THEN member.canonical_label
					      WHEN 'DATASET_VERSION' THEN dataset.name
					      ELSE '' END) IN lower($1))>0 THEN 1.0
					    WHEN document.document ILIKE '%'||$1||'%' THEN 0.8
					    ELSE 0 END
					)::float8 AS score
				FROM platform.semantic_documents AS document
				LEFT JOIN platform.metric_versions AS metric_version
				  ON document.subject_type='METRIC_VERSION'
				 AND metric_version.id=document.metric_version_id
				 AND metric_version.status='PUBLISHED'
				LEFT JOIN platform.metrics AS metric
				  ON metric.id=metric_version.metric_id
				 AND metric.current_published_version_id=metric_version.id
				 AND metric.status='PUBLISHED' AND metric.deleted_at IS NULL
				LEFT JOIN platform.semantic_dimensions AS dimension
				  ON document.subject_type='DIMENSION'
				 AND dimension.id=document.dimension_id AND dimension.status='PUBLISHED'
				LEFT JOIN platform.dimension_members AS member
				  ON document.subject_type='DIMENSION_MEMBER'
				 AND member.id=document.dimension_member_id AND member.status='ACTIVE'
				LEFT JOIN platform.semantic_dimensions AS member_dimension
				  ON member_dimension.id=member.dimension_id
				 AND member_dimension.status='PUBLISHED'
				LEFT JOIN platform.dataset_versions AS dataset_version
				  ON document.subject_type='DATASET_VERSION'
				 AND dataset_version.id=document.dataset_version_id
				 AND dataset_version.status='PUBLISHED'
				LEFT JOIN platform.datasets AS dataset
				  ON dataset.id=dataset_version.dataset_id
				 AND dataset.current_published_version_id=dataset_version.id
				 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
				WHERE document.subject_type IN (
				  'METRIC_VERSION','DIMENSION','DIMENSION_MEMBER','DATASET_VERSION'
				)
				  AND (
				    ($2<>'' AND document.embedding_status='SUCCEEDED')
				    OR document.document ILIKE '%'||$1||'%'
				    OR position(lower(CASE document.subject_type
				      WHEN 'METRIC_VERSION' THEN metric.name
				      WHEN 'DIMENSION' THEN dimension.name
				      WHEN 'DIMENSION_MEMBER' THEN member.canonical_label
				      WHEN 'DATASET_VERSION' THEN dataset.name
				      ELSE '' END) IN lower($1))>0
				  )
			)
			SELECT CASE subject_type
				 WHEN 'METRIC_VERSION' THEN 'METRIC'
				 WHEN 'DIMENSION_MEMBER' THEN 'MEMBER'
				 WHEN 'DATASET_VERSION' THEN 'DATASET'
				 ELSE subject_type END,
				code,dimension_code,label,member_value,score
			FROM candidates
			WHERE label<>'' AND score>0.15
			ORDER BY score DESC,subject_type,code,label
			LIMIT $3`, question, vectorLiteral, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item recallCandidate
			if err := rows.Scan(
				&item.SubjectType, &item.Code, &item.DimensionCode,
				&item.Label, &item.MemberValue, &item.Score,
			); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func formatVector(values []float32) string {
	var builder strings.Builder
	builder.Grow(len(values)*12 + 2)
	builder.WriteByte('[')
	for index, value := range values {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}

func sortedCandidateCodes(
	candidates []recallCandidate,
	subjectType string,
) []string {
	values := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.SubjectType == subjectType && candidate.Code != "" {
			values[candidate.Code] = true
		}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
