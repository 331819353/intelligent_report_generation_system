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
	Intent               string
	Domain               string
	MetricCode           string
	DimensionCode        string
	MemberValue          string
	MetricCandidateCount int
	MetricMatchMethod    string
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
	Domain        string  `json:"domain,omitempty"`
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
	// Stage one is deliberately metric-only. Dimension and member candidates
	// are resolved later, after the selected metric has fixed an exact governed
	// dataset version. Mixing all asset types in one recall pool lets a common
	// member value influence the metric decision and makes the result harder to
	// explain.
	candidates, err := interpreter.store.recall(ctx, tenantID, question, nil, 12)
	if err != nil {
		return QuerySlots{}, err
	}
	fallback := deterministicSlots(question, candidates)
	fallback.MetricCandidateCount = len(candidates)
	if fallback.MetricCode != "" {
		fallback.MetricMatchMethod = "EXACT_CATALOG"
		fallback.Domain = candidateDomain(candidates, fallback.MetricCode)
		memberCandidates, memberErr := interpreter.store.recallMetricMembers(
			ctx, tenantID, fallback.MetricCode, question, nil, 8,
		)
		if memberErr == nil {
			applyMemberCandidate(&fallback, memberCandidates)
		}
		if fallback.MemberValue != "" ||
			interpreter.embedding == nil || !interpreter.embedding.Configured() {
			return fallback, nil
		}
		vectors, embedErr := interpreter.embedding.Embed(ctx, []string{question})
		if embedErr == nil && len(vectors) == 1 {
			memberCandidates, memberErr = interpreter.store.recallMetricMembers(
				ctx, tenantID, fallback.MetricCode, question, vectors[0], 8,
			)
			if memberErr == nil {
				applyMemberCandidate(&fallback, memberCandidates)
			}
		}
		return fallback, nil
	}
	var questionVector []float32
	if interpreter.embedding != nil && interpreter.embedding.Configured() {
		vectors, embedErr := interpreter.embedding.Embed(ctx, []string{question})
		if embedErr == nil && len(vectors) == 1 {
			questionVector = vectors[0]
			vectorCandidates, recallErr := interpreter.store.recall(
				ctx, tenantID, question, vectors[0], 12,
			)
			if recallErr == nil {
				candidates = vectorCandidates
				fallback = deterministicSlots(question, candidates)
				fallback.MetricCandidateCount = len(candidates)
				if fallback.MetricCode != "" {
					fallback.MetricMatchMethod = "EXACT_CATALOG"
					fallback.Domain = candidateDomain(candidates, fallback.MetricCode)
					memberCandidates, memberErr := interpreter.store.recallMetricMembers(
						ctx, tenantID, fallback.MetricCode, question,
						questionVector, 8,
					)
					if memberErr == nil {
						applyMemberCandidate(&fallback, memberCandidates)
					}
					return fallback, nil
				}
			}
		}
	}
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
		PromptVersion: "semantic-query-metric-intent-v2",
		ResourceType:  "SEMANTIC_QUERY", ResourceID: hashText(question),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读语义问答槽位解析器。只能从 candidates 复制 metric code；dimensionCode 和 memberValue 必须留空并由服务端在选中指标范围内精确解析。不得发明对象，不得输出 SQL、数据集或 DAG。识别 LOOKUP、METRIC、TREND、COMPARISON、RANKING、DRILLDOWN、DISTRIBUTION、FUNNEL、RETENTION、ANOMALY 或 UNKNOWN。候选不足或歧义时 needsClarification=true，并将无法证明的槽位置空。`,
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
	if output.Intent == "LOOKUP" && questionRequestsAggregate(question) {
		output.Intent = "METRIC"
	}
	output.MetricCode = strings.TrimSpace(output.MetricCode)
	output.DimensionCode = strings.TrimSpace(output.DimensionCode)
	output.MemberValue = strings.TrimSpace(output.MemberValue)
	if output.NeedsClarification ||
		!validInterpretedSlots(output, llmCandidates) ||
		output.MemberValue != "" {
		return fallback, nil
	}
	slots := QuerySlots{
		Intent: output.Intent, MetricCode: output.MetricCode,
		DimensionCode:        output.DimensionCode,
		MetricCandidateCount: len(candidates),
	}
	if slots.MetricCode != "" {
		slots.MetricMatchMethod = "CATALOG_RERANK"
		slots.Domain = candidateDomain(candidates, slots.MetricCode)
		memberCandidates, memberErr := interpreter.store.recallMetricMembers(
			ctx, tenantID, slots.MetricCode, question, questionVector, 8,
		)
		if memberErr == nil {
			applyMemberCandidate(&slots, memberCandidates)
		}
	}
	return slots, nil
}

func candidateDomain(candidates []recallCandidate, metricCode string) string {
	for _, candidate := range candidates {
		if candidate.SubjectType == "METRIC" &&
			strings.EqualFold(candidate.Code, metricCode) {
			return candidate.Domain
		}
	}
	return ""
}

func applyMemberCandidate(slots *QuerySlots, candidates []recallCandidate) {
	if slots == nil || len(candidates) == 0 || candidates[0].Score < 0.55 {
		return
	}
	if len(candidates) > 1 &&
		candidates[0].MemberValue != candidates[1].MemberValue &&
		candidates[0].Score-candidates[1].Score < 0.08 {
		return
	}
	slots.DimensionCode = candidates[0].DimensionCode
	slots.MemberValue = candidates[0].MemberValue
}

func externalRecallCandidates(candidates []recallCandidate) []recallCandidate {
	result := make([]recallCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		// Stage one sends only the bounded published metric shortlist. Neither
		// dimensions, datasets nor member values may influence the metric
		// decision or escape the tenant-local second-stage resolver.
		if candidate.SubjectType == "METRIC" {
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
	for _, candidate := range candidates {
		labelMatch := strings.Contains(question, strings.ToLower(candidate.Label))
		codeMatch := candidate.Code != "" &&
			strings.Contains(question, strings.ToLower(candidate.Code))
		switch candidate.SubjectType {
		case "METRIC":
			if labelMatch || codeMatch {
				metrics[candidate.Code] = true
			}
		}
	}
	if len(metrics) == 1 {
		for code := range metrics {
			result.MetricCode = code
			result.Domain = candidateDomain(candidates, code)
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

func questionRequestsAggregate(question string) bool {
	question = strings.ToLower(question)
	for _, phrase := range []string{
		"数量", "总数", "多少", "金额", "总额", "平均", "均值",
		"占比", "比例", "率", "count", "sum", "average",
	} {
		if strings.Contains(question, phrase) {
			return true
		}
	}
	return false
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
				SELECT 'METRIC_VERSION'::text AS subject_type,
					platform.dataset_version_effective_domain(dataset_version.id) AS domain,
					metric.code::text AS code,
					''::text AS dimension_code,
					metric.name AS label,
					''::text AS member_value,
					GREATEST(
					  CASE WHEN $2<>'' AND document.embedding_status='SUCCEEDED'
					    THEN 1-(document.embedding <=> $2::halfvec)
					    ELSE 0 END,
					  similarity(lower(metric.name),lower($1)),
					  similarity(lower(metric.code::text),lower($1)),
					  CASE
					    WHEN position(lower(metric.name) IN lower($1))>0 THEN 1.0
					    WHEN position(lower(metric.code::text) IN lower($1))>0 THEN 1.0
					    WHEN document.document ILIKE '%'||$1||'%' THEN 0.8
					    ELSE 0 END
					)::float8 AS score
				FROM platform.metric_versions AS metric_version
				JOIN platform.metrics AS metric
				  ON metric.id=metric_version.metric_id
				 AND metric.current_published_version_id=metric_version.id
				 AND metric.status='PUBLISHED' AND metric.deleted_at IS NULL
				JOIN platform.dataset_versions AS dataset_version
				  ON dataset_version.id=metric_version.dataset_version_id
				 AND dataset_version.status='PUBLISHED'
				JOIN platform.datasets AS dataset
				  ON dataset.id=dataset_version.dataset_id
				 AND dataset.current_published_version_id=dataset_version.id
				 AND dataset.status='PUBLISHED' AND dataset.deleted_at IS NULL
				LEFT JOIN platform.metric_semantic_documents AS document
				  ON document.subject_type='METRIC_VERSION'
				 AND document.metric_version_id=metric_version.id
				WHERE metric_version.status='PUBLISHED'
			)
			SELECT 'METRIC',
				domain,code,dimension_code,label,member_value,score
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
				&item.SubjectType, &item.Domain, &item.Code, &item.DimensionCode,
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

// recallMetricMembers performs the second hybrid lookup only after a metric has
// fixed the exact dataset version and its allowed dimension set. Vector hits
// can therefore never jump to a similarly named member from another metric or
// domain.
func (store *PostgresStore) recallMetricMembers(
	ctx context.Context,
	tenantID, metricCode, question string,
	vector []float32,
	limit int,
) (items []recallCandidate, err error) {
	items = []recallCandidate{}
	if store == nil || metricCode == "" || limit < 1 {
		return items, nil
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		vectorLiteral := ""
		if len(vector) > 0 {
			vectorLiteral = formatVector(vector)
		}
		rows, queryErr := tx.Query(ctx, `WITH governed_metric AS (
				SELECT version.id AS metric_version_id,
					version.dataset_version_id,
					platform.dataset_version_effective_domain(dataset_version.id) AS domain,
					COALESCE(version.definition_json->'allowedDimensions','[]'::jsonb) AS dimensions
				FROM platform.metrics AS metric
				JOIN platform.metric_versions AS version
				  ON version.tenant_id=metric.tenant_id
				 AND version.id=metric.current_published_version_id
				 AND version.status='PUBLISHED'
				JOIN platform.dataset_versions AS dataset_version
				  ON dataset_version.tenant_id=version.tenant_id
				 AND dataset_version.id=version.dataset_version_id
				 AND dataset_version.status='PUBLISHED'
				WHERE metric.code=$1
				  AND metric.status='PUBLISHED'
				  AND metric.deleted_at IS NULL
			), eligible AS (
				SELECT metric.domain,dimension.code::text AS dimension_code,
					member.normalized_value AS member_value,
					document.member_label,
					GREATEST(
					  CASE WHEN $3<>'' AND document.embedding_status='SUCCEEDED'
					    THEN 1-(document.embedding <=> $3::halfvec)
					    ELSE 0 END,
					  similarity(lower(document.member_label),lower($2)),
					  CASE
					    WHEN position(lower(document.member_label) IN lower($2))>0
					      THEN 1.0
					    ELSE 0 END
					)::float8 AS score
				FROM governed_metric AS metric
				JOIN LATERAL jsonb_array_elements(metric.dimensions) AS allowed
				  ON true
				JOIN platform.semantic_dimensions AS dimension
				  ON dimension.dataset_version_id=metric.dataset_version_id
				 AND dimension.field_id=allowed->>'fieldId'
				 AND dimension.status='PUBLISHED'
				 AND dimension.member_index_policy='FULL'
				 AND NOT dimension.sensitive
				 AND NOT dimension.high_cardinality
				JOIN platform.dimension_members AS member
				  ON member.tenant_id=dimension.tenant_id
				 AND member.dimension_id=dimension.id
				 AND member.status='ACTIVE'
				 AND (member.valid_from IS NULL OR member.valid_from<=now())
				 AND (member.valid_to IS NULL OR member.valid_to>now())
				JOIN platform.dimension_member_semantic_documents AS document
				  ON document.tenant_id=member.tenant_id
				 AND document.dimension_member_id=member.id
				 AND document.dimension_id=dimension.id
			)
			SELECT 'MEMBER'::text,domain,''::text,dimension_code,
				member_label,member_value,score
			FROM eligible
			WHERE score>0.35
			ORDER BY score DESC,dimension_code,member_value
			LIMIT $4`, metricCode, question, vectorLiteral, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item recallCandidate
			if err := rows.Scan(
				&item.SubjectType, &item.Domain, &item.Code,
				&item.DimensionCode, &item.Label, &item.MemberValue,
				&item.Score,
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
