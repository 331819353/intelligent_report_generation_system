package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/embedding"
	"intelligent-report-generation-system/internal/platform/database"
)

type QuerySlots struct {
	Intent                string
	Domain                string
	MetricCode            string
	DimensionCode         string
	MemberValue           string
	MemberFilters         []QueryMemberFilterInput
	TimeRange             *QueryTimeRange
	MetricCandidateCount  int
	MetricMatchMethod     string
	DimensionValueLookups []QueryDimensionValueLookupTrace
}

type QueryTurnSlots struct {
	Intent               string
	MetricCodes          []string
	MetricCandidateCount int
	MetricMatchMethod    string
	Domains              map[string]string
	MetricCandidates     []QueryMetricCandidateTrace
	NeedsClarification   bool
	MetricToolLoop       *QueryMetricToolLoopTrace
}

type QueryInterpreter interface {
	Interpret(context.Context, string, string, string) (QuerySlots, error)
}

type QueryTurnInterpreter interface {
	InterpretMany(context.Context, string, string, string) (QueryTurnSlots, error)
}

type QueryMetricConfirmationResolver interface {
	ConfirmMetricCodes(
		context.Context, string, []string,
	) (QueryTurnSlots, error)
}

type QueryDimensionLookupEnricher interface {
	EnrichDimensionLookups(
		context.Context, string, string, string, string,
	) ([]QueryDimensionValueLookupTrace, error)
}

type QueryDimensionHintEnricher interface {
	EnrichDimensionLookupsWithHints(
		context.Context, string, string, string, string,
		[]QuerySemanticDimensionHint,
	) ([]QueryDimensionValueLookupTrace, error)
}

type semanticAIInvoker interface {
	Configured() bool
	Invoke(context.Context, aiplatform.Invocation) (aiplatform.InvocationResult, error)
}

type semanticToolAIInvoker interface {
	InvokeToolLoop(
		context.Context,
		aiplatform.ToolInvocation,
	) (aiplatform.ToolInvocationResult, error)
}

type semanticToolAIFallbackInvoker interface {
	semanticToolAIInvoker
	FallbackModels() []string
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

// EnrichDimensionLookups first lets the LLM normalize each non-empty dimension
// value and design one constrained predicate at a time. It then embeds the
// exact "dimension description:canonical value" key and searches only members
// compatible with the selected metric. Exact semantic mappings remain
// authoritative and the server validates and parameterizes the final
// predicate, so the LLM never becomes an arbitrary SQL execution boundary.
func (interpreter *SemanticInterpreter) EnrichDimensionLookups(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
) ([]QueryDimensionValueLookupTrace, error) {
	if interpreter == nil || interpreter.store == nil {
		return nil, ErrInvalidRequest
	}
	lookups, err := interpreter.store.PreviewMetricDimensionLookups(
		ctx, tenantID, metricCode, question,
	)
	if err != nil || len(lookups) == 0 {
		return lookups, err
	}
	nonEmpty := make([]QueryDimensionValueLookupTrace, 0, len(lookups))
	for _, lookup := range lookups {
		if strings.TrimSpace(lookup.Term) == "" {
			continue
		}
		nonEmpty = append(nonEmpty, lookup)
	}
	lookups = interpreter.resolveAmbiguousDimensionLookups(
		ctx, tenantID, actorID, metricCode, question, nonEmpty,
	)
	for index := range lookups {
		lookups[index].CanonicalValue =
			deterministicCanonicalValue(lookups[index])
		lookups[index].AliasValues = appendUniqueString(
			lookups[index].AliasValues, lookups[index].Term,
		)
	}
	lookups = deduplicateSemanticLookups(lookups)
	pairs := []string{}
	pairIndexes := []int{}
	for index := range lookups {
		lookups[index].VectorQuery = dimensionVectorQuery(lookups[index])
		if lookups[index].VectorQuery == "" {
			lookups[index].VectorSearchStatus =
				"SKIPPED_VECTOR_KEY_INCOMPLETE"
			continue
		}
		if lookups[index].Sensitive {
			lookups[index].VectorSearchStatus =
				"SKIPPED_SENSITIVE_DIMENSION"
			continue
		}
		pairs = append(pairs, lookups[index].VectorQuery)
		pairIndexes = append(pairIndexes, index)
	}
	if interpreter.embedding == nil || !interpreter.embedding.Configured() {
		for index := range lookups {
			if lookups[index].VectorSearchStatus == "" {
				lookups[index].VectorSearchStatus =
					"SKIPPED_PROVIDER_NOT_CONFIGURED"
			}
		}
	} else if len(pairs) > 0 {
		vectors, embedErr := interpreter.embedding.Embed(ctx, pairs)
		if embedErr != nil || len(vectors) != len(pairIndexes) {
			for index := range lookups {
				if lookups[index].VectorSearchStatus == "" {
					lookups[index].VectorSearchStatus = "FAILED"
				}
			}
		} else {
			for vectorIndex, index := range pairIndexes {
				lookups[index].VectorModel = interpreter.embedding.Model()
				lookups[index].VectorDimensions = len(vectors[vectorIndex])
				lookups[index].VectorEmbedding = append(
					[]float32(nil), vectors[vectorIndex]...,
				)
				candidates, recallErr :=
					interpreter.store.recallMetricDimensionDecisions(
						ctx, tenantID, metricCode, lookups[index].DimensionCode,
						lookups[index].CanonicalValue,
						vectors[vectorIndex], 128,
					)
				if recallErr != nil {
					lookups[index].VectorSearchStatus = "FAILED"
					continue
				}
				lookups[index].VectorSearchStatus = "SUCCEEDED"
				lookups[index].VectorCandidateCount = len(candidates)
				for candidateIndex, candidate := range candidates {
					lookups[index].VectorCandidateMemberKeys = append(
						lookups[index].VectorCandidateMemberKeys,
						candidate.CanonicalValue,
					)
					if candidateIndex == 0 {
						lookups[index].VectorTopScore = candidate.Score
					}
					lookups[index].DecisionCandidates = append(
						lookups[index].DecisionCandidates,
						QueryDecisionCandidate{
							DecisionID:        candidate.DecisionID,
							CanonicalValue:    candidate.CanonicalValue,
							MemberValue:       candidate.MemberValue,
							MetricCode:        lookups[index].MetricCode,
							MetricName:        lookups[index].MetricName,
							TableSchema:       candidate.TableSchema,
							TableName:         candidate.TableName,
							WhereCondition:    candidate.WhereCondition,
							CompiledCondition: candidate.CompiledCondition,
							PredicateOperator: candidate.PredicateOperator,
							Score:             candidate.Score,
						},
					)
					if lookups[index].DecisionID == "" &&
						decisionMatchesLookup(candidate, lookups[index]) {
						lookups[index].DecisionID = candidate.DecisionID
						lookups[index].WhereCondition =
							candidate.WhereCondition
						lookups[index].CompiledCondition =
							candidate.CompiledCondition
						lookups[index].WhereDesignStatus =
							"REUSED_DECISION_GRAPH"
						lookups[index].WhereDesignOperator =
							candidate.PredicateOperator
						lookups[index].WhereDesignReason =
							candidate.LLMReason
						lookups[index].WhereDesignModel =
							candidate.LLMModel
						lookups[index].MetricFieldID =
							candidate.MetricFieldID
						lookups[index].TableSchema =
							candidate.TableSchema
						lookups[index].TableName = candidate.TableName
						lookups[index].DecisionCandidates[len(lookups[index].DecisionCandidates)-1].Selected = true
					}
				}
			}
		}
	}
	lookups = interpreter.designWherePredicates(
		ctx, tenantID, actorID, metricCode, question, lookups,
	)
	return lookups, nil
}

// EnrichDimensionLookupsWithHints chains the bounded output of token semantic
// completion into the governed decision graph. Hints contain names and values
// only. Every field, member key, materialization and predicate is reloaded from
// the published catalog or an existing decision; no caller-supplied SQL can
// cross this boundary.
func (interpreter *SemanticInterpreter) EnrichDimensionLookupsWithHints(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
	hints []QuerySemanticDimensionHint,
) ([]QueryDimensionValueLookupTrace, error) {
	lookups, err := interpreter.EnrichDimensionLookups(
		ctx, tenantID, actorID, metricCode, question,
	)
	if err != nil {
		return lookups, err
	}
	for _, hint := range hints {
		if strings.EqualFold(strings.TrimSpace(hint.DimensionType), "TIME") ||
			strings.TrimSpace(hint.Value) == "" {
			continue
		}
		alreadyResolved := false
		for _, lookup := range lookups {
			if strings.EqualFold(lookup.DimensionCode, hint.DimensionCode) &&
				(strings.EqualFold(lookup.Term, hint.Value) ||
					strings.EqualFold(lookup.CanonicalValue, hint.Value)) {
				alreadyResolved = true
				break
			}
		}
		if alreadyResolved {
			continue
		}
		hintLookup, lookupErr := interpreter.decisionLookupFromHint(
			ctx, tenantID, actorID, metricCode, question, hint,
		)
		if lookupErr != nil {
			return lookups, lookupErr
		}
		if strings.TrimSpace(hintLookup.DimensionCode) != "" {
			lookups = append(lookups, hintLookup)
		}
	}
	return deduplicateSemanticLookups(lookups), nil
}

func (interpreter *SemanticInterpreter) decisionLookupFromHint(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
	hint QuerySemanticDimensionHint,
) (QueryDimensionValueLookupTrace, error) {
	lookup, err := interpreter.store.metricDimensionHintLookup(
		ctx, tenantID, metricCode, hint,
	)
	if err != nil {
		return lookup, err
	}
	if lookup.DimensionCode == "" {
		lookup = QueryDimensionValueLookupTrace{
			Term:               strings.TrimSpace(hint.Value),
			CanonicalValue:     strings.TrimSpace(hint.Value),
			MetricCode:         metricCode,
			DimensionCode:      strings.TrimSpace(hint.DimensionCode),
			DimensionName:      strings.TrimSpace(hint.DimensionName),
			DimensionFieldName: strings.TrimSpace(hint.DimensionCode),
			VectorSearchStatus: "REJECTED_INCOMPATIBLE_DIMENSION",
			WhereDesignStatus:  "NO_SAFE_DECISION_SELECTED",
			MatchMethod:        "LLM_SEMANTIC_HINT",
			Source:             "LLM_INTENT_COMPLETION",
			CandidateFilter: QueryCandidateFilterTrace{
				Status: "REJECTED_INCOMPATIBLE_DIMENSION",
				Rules: []string{
					"PUBLISHED_METRIC_DIMENSION_COMPATIBILITY_REQUIRED",
				},
			},
		}
		lookup.VectorQuery = dimensionVectorQuery(lookup)
		return lookup, nil
	}
	lookup.Term = strings.TrimSpace(hint.Value)
	lookup.CanonicalValue = lookup.Term
	lookup.AliasValues = appendUniqueString(
		lookup.AliasValues, strings.TrimSpace(hint.SourceToken),
	)
	lookup.MatchMethod = "LLM_SEMANTIC_HINT"
	lookup.Source = "LLM_INTENT_COMPLETION"
	lookup.VectorQuery = dimensionVectorQuery(lookup)
	lookup.DecisionCandidates = []QueryDecisionCandidate{}
	if lookup.VectorQuery == "" {
		lookup.VectorSearchStatus = "SKIPPED_VECTOR_KEY_INCOMPLETE"
		lookup.WhereDesignStatus = "NO_SAFE_DECISION_SELECTED"
		return lookup, nil
	}
	if lookup.Sensitive {
		lookup.VectorSearchStatus = "SKIPPED_SENSITIVE_DIMENSION"
		lookup.WhereDesignStatus = "NO_SAFE_DECISION_SELECTED"
		return lookup, nil
	}
	if interpreter.embedding == nil || !interpreter.embedding.Configured() {
		lookup.VectorSearchStatus = "SKIPPED_PROVIDER_NOT_CONFIGURED"
		lookup.WhereDesignStatus = "NO_SAFE_DECISION_SELECTED"
		return lookup, nil
	}
	vectors, err := interpreter.embedding.Embed(ctx, []string{
		lookup.VectorQuery,
	})
	if err != nil || len(vectors) != 1 {
		lookup.VectorSearchStatus = "FAILED"
		lookup.WhereDesignStatus = "NO_SAFE_DECISION_SELECTED"
		return lookup, nil
	}
	lookup.VectorModel = interpreter.embedding.Model()
	lookup.VectorDimensions = len(vectors[0])
	lookup.VectorEmbedding = append([]float32(nil), vectors[0]...)
	candidates, err := interpreter.store.recallMetricDimensionDecisions(
		ctx, tenantID, metricCode, lookup.DimensionCode,
		lookup.Term, vectors[0], 5,
	)
	if err != nil {
		lookup.VectorSearchStatus = "FAILED"
		lookup.WhereDesignStatus = "NO_SAFE_DECISION_SELECTED"
		return lookup, nil
	}
	lookup.VectorSearchStatus = "SUCCEEDED"
	lookup.VectorCandidateCount = len(candidates)
	lookup.CandidateCount = len(candidates)
	lookup.CandidateMemberKeys = []string{}
	for index, candidate := range candidates {
		lookup.VectorCandidateMemberKeys = appendUniqueString(
			lookup.VectorCandidateMemberKeys, candidate.CanonicalValue,
		)
		lookup.CandidateMemberKeys = appendUniqueString(
			lookup.CandidateMemberKeys, candidate.MemberValue,
		)
		if index == 0 {
			lookup.VectorTopScore = candidate.Score
		}
		lookup.DecisionCandidates = append(
			lookup.DecisionCandidates,
			QueryDecisionCandidate{
				DecisionID:     candidate.DecisionID,
				CanonicalValue: candidate.CanonicalValue,
				MemberValue:    candidate.MemberValue,
				MetricCode:     metricCode, MetricName: lookup.MetricName,
				TableSchema:       candidate.TableSchema,
				TableName:         candidate.TableName,
				WhereCondition:    candidate.WhereCondition,
				CompiledCondition: candidate.CompiledCondition,
				PredicateOperator: candidate.PredicateOperator,
				Score:             candidate.Score,
			},
		)
	}
	selectedIndex, reason, model, status := interpreter.selectHintDecision(
		ctx, tenantID, actorID, metricCode, question, lookup, candidates,
	)
	lookup.WhereDesignStatus = status
	lookup.WhereDesignReason = reason
	lookup.WhereDesignModel = model
	lookup.CandidateFilter = QueryCandidateFilterTrace{
		InputCount: len(candidates), RejectedCount: len(candidates),
		Status: status,
		Rules: []string{
			"PUBLISHED_DECISION_ONLY",
			"METRIC_DIMENSION_COMPATIBLE",
			"SINGLE_ACTIVE_MEMBER_REQUIRED",
			"LLM_MAY_SELECT_DECISION_ID_ONLY",
		},
	}
	if selectedIndex < 0 {
		return lookup, nil
	}
	selected := candidates[selectedIndex]
	lookup.Selected = true
	lookup.CanonicalValue = selected.CanonicalValue
	lookup.DecisionID = selected.DecisionID
	lookup.WhereDesignOperator = selected.PredicateOperator
	lookup.WhereCondition = selected.WhereCondition
	lookup.CompiledCondition = selected.CompiledCondition
	lookup.TableSchema = selected.TableSchema
	lookup.TableName = selected.TableName
	lookup.MetricFieldID = selected.MetricFieldID
	lookup.SelectedMemberKeys = []string{selected.MemberValue}
	lookup.CandidateFilter.AcceptedCount = 1
	lookup.CandidateFilter.RejectedCount = max(0, len(candidates)-1)
	if selectedIndex < len(lookup.DecisionCandidates) {
		lookup.DecisionCandidates[selectedIndex].Selected = true
	}
	return lookup, nil
}

type hintDecisionSelectionOutput struct {
	SelectedDecisionID string  `json:"selectedDecisionId"`
	Reason             string  `json:"reason"`
	Confidence         float64 `json:"confidence"`
}

type hintDecisionSelectionCandidate struct {
	DecisionID     string   `json:"decisionId"`
	CanonicalValue string   `json:"canonicalValue"`
	Aliases        []string `json:"aliases"`
	Score          float64  `json:"score"`
}

func (interpreter *SemanticInterpreter) selectHintDecision(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
	lookup QueryDimensionValueLookupTrace,
	candidates []dimensionDecisionCandidate,
) (int, string, string, string) {
	for index, candidate := range candidates {
		if candidate.SelectedMemberCount != 1 ||
			strings.TrimSpace(candidate.MemberValue) == "" {
			continue
		}
		if strings.EqualFold(candidate.CanonicalValue, lookup.Term) {
			return index, "维度值与决策图规范值精确一致",
				candidate.LLMModel, "REUSED_DECISION_GRAPH_EXACT"
		}
		for _, alias := range candidate.Aliases {
			if strings.EqualFold(alias, lookup.Term) {
				return index, "维度值与决策图别名精确一致",
					candidate.LLMModel, "REUSED_DECISION_GRAPH_ALIAS"
			}
		}
	}
	if index, ok := highConfidenceHintDecisionFallback(
		lookup, candidates,
	); ok {
		return index,
			"唯一候选通过跨语言近精确向量与分差校验",
			"", "VECTOR_FILTERED_DECISION_GRAPH"
	}
	if interpreter.ai == nil || !interpreter.ai.Configured() {
		return -1, "没有精确决策且 LLM 未配置", "",
			"NO_SAFE_DECISION_SELECTED"
	}
	selectionCandidates := []hintDecisionSelectionCandidate{}
	for _, candidate := range candidates {
		if candidate.SelectedMemberCount != 1 ||
			strings.TrimSpace(candidate.MemberValue) == "" {
			continue
		}
		selectionCandidates = append(
			selectionCandidates,
			hintDecisionSelectionCandidate{
				DecisionID:     candidate.DecisionID,
				CanonicalValue: candidate.CanonicalValue,
				Aliases:        append([]string(nil), candidate.Aliases...),
				Score:          candidate.Score,
			},
		)
	}
	if len(selectionCandidates) == 0 {
		return -1, "候选决策不能还原为单一受管维度成员", "",
			"NO_SAFE_DECISION_SELECTED"
	}
	payload, err := json.Marshal(struct {
		Question             string                           `json:"question"`
		MetricCode           string                           `json:"metricCode"`
		DimensionCode        string                           `json:"dimensionCode"`
		DimensionName        string                           `json:"dimensionName"`
		DimensionDescription string                           `json:"dimensionDescription"`
		Value                string                           `json:"value"`
		Candidates           []hintDecisionSelectionCandidate `json:"candidates"`
	}{
		Question: question, MetricCode: metricCode,
		DimensionCode:        lookup.DimensionCode,
		DimensionName:        lookup.DimensionName,
		DimensionDescription: lookup.DimensionFieldDescription,
		Value:                lookup.Term, Candidates: selectionCandidates,
	})
	if err != nil {
		return -1, "决策候选序列化失败", "",
			"NO_SAFE_DECISION_SELECTED"
	}
	temperature := 0.0
	result, err := interpreter.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: "semantic-query-decision-filter-v4",
		ResourceType:  "SEMANTIC_DECISION_FILTER",
		ResourceID: hashText(strings.Join(
			[]string{metricCode, lookup.DimensionCode, lookup.Term}, "\x00",
		)),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读语义决策过滤器。结合原问题、已选指标，以及语义资产中发布的 dimensionName、dimensionCode、dimensionDescription 和用户维度值，从同一受管维度的候选中最多选择一个语义等价决策。维度含义只能以输入的已发布语义元数据为依据，不得套用程序外的固定业务词表。判断 canonicalValue 或 aliases 是否与 value 等价；语言、书写形式或规范编码不同不等于语义不同，但向量 score 只用于排序，不能单独证明等价。相似但不等价、范围不一致、存在多个同等可能候选或证据不足时返回空字符串。若且仅有一个候选满足等价条件，选择它。selectedDecisionId 只能逐字复制 candidates 中的 decisionId，不得生成或修改 SQL、WHERE、字段、表名和值。reason 简要说明选择或拒绝原因。`,
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
				Name: "semantic_decision_filter",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["selectedDecisionId","reason","confidence"],
					"properties":{
						"selectedDecisionId":{"type":"string","maxLength":64},
						"reason":{"type":"string","maxLength":500},
						"confidence":{"type":"number","minimum":0,"maximum":1}
					}
				}`),
			},
			Temperature: &temperature, MaxOutputTokens: 500,
		},
	})
	if err != nil {
		if index, ok := highConfidenceHintDecisionFallback(
			lookup, candidates,
		); ok {
			return index,
				"LLM 过滤暂不可用；唯一向量候选通过高置信度与分差校验",
				"", "VECTOR_FILTERED_DECISION_GRAPH"
		}
		return -1, "LLM 决策过滤失败", "",
			whereDesignFailureStatus(err)
	}
	var output hintDecisionSelectionOutput
	if json.Unmarshal(result.ProviderResult.Content, &output) != nil ||
		output.Confidence < 0.7 ||
		strings.TrimSpace(output.SelectedDecisionID) == "" {
		if index, ok := highConfidenceHintDecisionFallback(
			lookup, candidates,
		); ok {
			return index,
				"LLM 未确认候选；唯一向量候选通过高置信度与分差校验",
				result.ProviderResult.Model,
				"VECTOR_FILTERED_DECISION_GRAPH"
		}
		return -1, strings.TrimSpace(output.Reason),
			result.ProviderResult.Model, "NO_SAFE_DECISION_SELECTED"
	}
	for index, candidate := range candidates {
		if candidate.DecisionID == output.SelectedDecisionID &&
			candidate.SelectedMemberCount == 1 &&
			strings.TrimSpace(candidate.MemberValue) != "" &&
			candidate.Score >= 0.80 {
			return index, output.Reason, result.ProviderResult.Model,
				"LLM_FILTERED_DECISION_GRAPH"
		}
	}
	return -1, "LLM 返回的决策不在高相关受管候选集合内", result.ProviderResult.Model,
		"NO_SAFE_DECISION_SELECTED"
}

func highConfidenceHintDecisionFallback(
	lookup QueryDimensionValueLookupTrace,
	candidates []dimensionDecisionCandidate,
) (int, bool) {
	bestIndex := -1
	bestScore, secondScore := 0.0, 0.0
	for index, candidate := range candidates {
		if candidate.SelectedMemberCount != 1 ||
			strings.TrimSpace(candidate.MemberValue) == "" ||
			strings.TrimSpace(candidate.DecisionID) == "" {
			continue
		}
		if candidate.Score > bestScore {
			secondScore = bestScore
			bestScore = candidate.Score
			bestIndex = index
		} else if candidate.Score > secondScore {
			secondScore = candidate.Score
		}
	}
	if strings.TrimSpace(lookup.Term) == "" || bestIndex < 0 ||
		!((bestScore >= 0.94 && bestScore-secondScore >= 0.06) ||
			(bestScore >= 0.985 && secondScore <= 0.98 &&
				bestScore-secondScore >= 0.01)) {
		return -1, false
	}
	return bestIndex, true
}

type rankedMetricDimensionHintLookup struct {
	value QueryDimensionValueLookupTrace
	rank  int
}

func selectMetricDimensionHintLookup(
	candidates []rankedMetricDimensionHintLookup,
) (QueryDimensionValueLookupTrace, bool) {
	if len(candidates) == 0 {
		return QueryDimensionValueLookupTrace{}, false
	}
	bestRank := candidates[0].rank
	bestCount := 0
	for _, candidate := range candidates {
		if candidate.rank == bestRank {
			bestCount++
		}
	}
	// Exact governed codes are unique within a published dataset. Name and
	// relaxed suffix/containment matches must have one unambiguous best match.
	if bestRank > 0 && bestCount != 1 {
		return QueryDimensionValueLookupTrace{}, false
	}
	return candidates[0].value, true
}

func (store *PostgresStore) metricDimensionHintLookup(
	ctx context.Context,
	tenantID, metricCode string,
	hint QuerySemanticDimensionHint,
) (lookup QueryDimensionValueLookupTrace, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `WITH candidates AS (
			SELECT
				dimension.id::text AS dimension_id,
				dimension.code::text AS dimension_code,
				dimension.name AS dimension_name,
				dimension.field_id AS dimension_field_id,
				dimension.code::text AS dimension_field_name,
				dimension.description AS dimension_description,
				dimension.sensitive,
				metric.name AS metric_name,
				COALESCE(
				  version.definition_json#>>'{expression,fieldId}',''
				) AS metric_field_id,
				version.id::text AS metric_version_id,
				version.dataset_version_id::text AS dataset_version_id,
				materialization.id::text AS materialization_id,
				materialization.published_schema,
				materialization.published_name,
				CASE
				  WHEN lower(dimension.code::text)=lower($2) THEN 0
				  WHEN lower(dimension.name)=lower($3) THEN 1
				  WHEN btrim($2)<>'' AND (
				    right(
				      lower(dimension.code::text),
				      length(lower($2))+1
				    )='_'||lower($2)
				    OR right(
				      lower($2),
				      length(lower(dimension.code::text))+1
				    )='_'||lower(dimension.code::text)
				  ) THEN 2
				  WHEN btrim($3)<>'' AND (
				    strpos(lower(dimension.name),lower($3))>0
				    OR strpos(lower($3),lower(dimension.name))>0
				  ) THEN 3
				  ELSE 4
				END AS match_rank,
				materialization.activated_at
			FROM platform.metrics AS metric
			JOIN platform.metric_versions AS version
			  ON version.tenant_id=metric.tenant_id
			 AND version.id=metric.current_published_version_id
			 AND version.metric_id=metric.id
			 AND version.status='PUBLISHED'
			JOIN platform.dimension_metric_compatibility AS compatibility
			  ON compatibility.tenant_id=metric.tenant_id
			 AND compatibility.metric_id=metric.id
			 AND compatibility.metric_version_id=version.id
			 AND compatibility.metric_dataset_version_id=
			     version.dataset_version_id
			 AND compatibility.status='VERIFIED'
			 AND compatibility.fanout_policy<>'UNSAFE'
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=compatibility.tenant_id
			 AND dimension.id=compatibility.dimension_id
			 AND dimension.dataset_version_id=version.dataset_version_id
			 AND dimension.status='PUBLISHED'
			JOIN platform.dataset_materializations AS materialization
			  ON materialization.tenant_id=version.tenant_id
			 AND materialization.dataset_version_id=version.dataset_version_id
			 AND materialization.layer='DWS'
			 AND materialization.status='ACTIVE'
			WHERE metric.tenant_id=platform.current_tenant_id()
			  AND metric.code=$1
			  AND metric.status='PUBLISHED'
			  AND metric.deleted_at IS NULL
		)
		SELECT dimension_id,dimension_code,dimension_name,
			dimension_field_id,dimension_field_name,dimension_description,
			sensitive,
			metric_name,metric_field_id,metric_version_id,
			dataset_version_id,materialization_id,published_schema,
			published_name,match_rank
		FROM candidates
		WHERE match_rank<4
		ORDER BY match_rank,activated_at DESC,dimension_id
		LIMIT 8`,
			metricCode, strings.TrimSpace(hint.DimensionCode),
			strings.TrimSpace(hint.DimensionName),
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		candidates := []rankedMetricDimensionHintLookup{}
		for rows.Next() {
			var candidate rankedMetricDimensionHintLookup
			if scanErr := rows.Scan(
				&candidate.value.DimensionID,
				&candidate.value.DimensionCode,
				&candidate.value.DimensionName,
				&candidate.value.DimensionFieldID,
				&candidate.value.DimensionFieldName,
				&candidate.value.DimensionFieldDescription,
				&candidate.value.Sensitive,
				&candidate.value.MetricName,
				&candidate.value.MetricFieldID,
				&candidate.value.MetricVersionID,
				&candidate.value.DatasetVersionID,
				&candidate.value.MaterializationID,
				&candidate.value.TableSchema,
				&candidate.value.TableName,
				&candidate.rank,
			); scanErr != nil {
				return scanErr
			}
			candidates = append(candidates, candidate)
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		// An exact governed code/name match always wins. A relaxed match
		// (for example city -> zone_city) is accepted only when exactly one
		// compatible published dimension has the best rank; ambiguity remains
		// a safe rejection instead of becoming an arbitrary WHERE field.
		selected, ok := selectMetricDimensionHintLookup(candidates)
		if !ok {
			return nil
		}
		lookup = selected
		lookup.MetricCode = metricCode
		return nil
	})
	return lookup, err
}

type dimensionChoiceCandidate struct {
	DimensionCode       string   `json:"dimensionCode"`
	DimensionName       string   `json:"dimensionName"`
	FieldName           string   `json:"fieldName"`
	FieldDescription    string   `json:"fieldDescription"`
	CandidateMemberKeys []string `json:"candidateMemberKeys"`
	DimensionValue      string   `json:"dimensionValue"`
}

type dimensionChoiceOutput struct {
	SelectedDimensionCode string `json:"selectedDimensionCode"`
}

// resolveAmbiguousDimensionLookups uses the user's surrounding wording plus
// governed field names/descriptions to choose between the same literal value
// under different dimensions. The model may only copy one supplied code. A
// term that occurs under just one dimension is selected deterministically.
func (interpreter *SemanticInterpreter) resolveAmbiguousDimensionLookups(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
	lookups []QueryDimensionValueLookupTrace,
) []QueryDimensionValueLookupTrace {
	groups := map[string][]int{}
	order := []string{}
	for index := range lookups {
		key := strings.ToLower(strings.TrimSpace(lookups[index].Term))
		if key == "" {
			continue
		}
		if _, exists := groups[key]; !exists {
			order = append(order, key)
		}
		groups[key] = append(groups[key], index)
	}
	for _, key := range order {
		indexes := groups[key]
		selectedDimension := ""
		dimensionIndexes := map[string][]int{}
		for _, index := range indexes {
			code := strings.ToLower(
				strings.TrimSpace(lookups[index].DimensionCode),
			)
			dimensionIndexes[code] = append(dimensionIndexes[code], index)
			if lookups[index].Selected {
				if selectedDimension != "" && selectedDimension != code {
					selectedDimension = ""
					break
				}
				selectedDimension = code
			}
		}
		if selectedDimension == "" && len(dimensionIndexes) == 1 {
			for code := range dimensionIndexes {
				selectedDimension = code
			}
		}
		if selectedDimension == "" && len(dimensionIndexes) > 1 {
			selectedDimension = strings.ToLower(
				interpreter.chooseDimensionWithLLM(
					ctx, tenantID, actorID, metricCode, question,
					lookups[indexes[0]].Term, indexes, lookups,
				),
			)
		}
		if selectedDimension == "" {
			continue
		}
		for _, index := range indexes {
			code := strings.ToLower(lookups[index].DimensionCode)
			lookups[index].Selected = code == selectedDimension
			if lookups[index].Selected {
				lookups[index].SelectedMemberKeys = append(
					[]string(nil), lookups[index].CandidateMemberKeys...,
				)
				sort.Strings(lookups[index].SelectedMemberKeys)
			} else {
				lookups[index].SelectedMemberKeys = nil
			}
		}
	}
	return lookups
}

func (interpreter *SemanticInterpreter) chooseDimensionWithLLM(
	ctx context.Context,
	tenantID, actorID, metricCode, question, term string,
	indexes []int,
	lookups []QueryDimensionValueLookupTrace,
) string {
	candidates := make([]dimensionChoiceCandidate, 0, len(indexes))
	seen := map[string]bool{}
	for _, index := range indexes {
		lookup := lookups[index]
		key := strings.ToLower(strings.TrimSpace(lookup.DimensionCode))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, dimensionChoiceCandidate{
			DimensionCode:    lookup.DimensionCode,
			DimensionName:    lookup.DimensionName,
			FieldName:        lookup.DimensionFieldName,
			FieldDescription: lookup.DimensionFieldDescription,
			CandidateMemberKeys: append(
				[]string(nil), lookup.CandidateMemberKeys...,
			),
			DimensionValue: term,
		})
	}
	if interpreter == nil || interpreter.ai == nil ||
		!interpreter.ai.Configured() {
		return chooseDimensionByQuestionContext(question, candidates)
	}
	payload, err := json.Marshal(struct {
		Question   string                     `json:"question"`
		MetricCode string                     `json:"metricCode"`
		Value      string                     `json:"value"`
		Candidates []dimensionChoiceCandidate `json:"candidates"`
	}{
		Question: question, MetricCode: metricCode, Value: term,
		Candidates: candidates,
	})
	if err != nil {
		return chooseDimensionByQuestionContext(question, candidates)
	}
	temperature := 0.0
	result, err := interpreter.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: "semantic-query-dimension-disambiguation-v1",
		ResourceType:  "SEMANTIC_DIMENSION_DISAMBIGUATION",
		ResourceID: hashText(strings.Join(
			[]string{metricCode, question, term}, "\x00",
		)),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读语义查询的维度消歧器。同一个维度值可能出现在多个字段中。必须结合完整问题、维度名、物理字段名和字段描述判断用户指向的维度；selectedDimensionCode 只能逐字复制 candidates 中的 dimensionCode。不得输出 SQL，不得发明维度或值。诸如“最高/最高学历”应优先匹配最高学历语义；“全日制/第一学历/毕业院校/毕业学校/从…毕业”应优先匹配全日制毕业教育语义。证据不足时 selectedDimensionCode 置空。只返回 selectedDimensionCode。`,
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
				Name: "semantic_dimension_disambiguation",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["selectedDimensionCode"],
					"properties":{
						"selectedDimensionCode":{"type":"string","maxLength":128}
					}
				}`),
			},
			Temperature: &temperature, MaxOutputTokens: 400,
		},
	})
	if err != nil {
		return chooseDimensionByQuestionContext(question, candidates)
	}
	var output dimensionChoiceOutput
	if json.Unmarshal(result.ProviderResult.Content, &output) != nil {
		return chooseDimensionByQuestionContext(question, candidates)
	}
	for _, candidate := range candidates {
		if output.SelectedDimensionCode == candidate.DimensionCode {
			return candidate.DimensionCode
		}
	}
	return chooseDimensionByQuestionContext(question, candidates)
}

func chooseDimensionByQuestionContext(
	question string,
	candidates []dimensionChoiceCandidate,
) string {
	question = strings.ToLower(strings.Join(strings.Fields(question), ""))
	bestCode, bestScore, tied := "", 0, false
	for _, candidate := range candidates {
		name := strings.ToLower(candidate.DimensionName)
		code := strings.ToLower(candidate.DimensionCode)
		field := strings.ToLower(candidate.FieldName)
		description := strings.ToLower(candidate.FieldDescription)
		score := 0
		for _, explicit := range []string{name, code, field} {
			if explicit != "" && strings.Contains(question, explicit) {
				score += 100 + len([]rune(explicit))
			}
		}
		for _, token := range []string{
			"最高学历", "最高", "全日制", "第一学历", "毕业院校",
			"毕业学校", "毕业", "办学性质", "院校", "学校",
		} {
			if !strings.Contains(question, token) {
				continue
			}
			switch token {
			case "毕业", "毕业院校", "毕业学校", "第一学历":
				if strings.Contains(name+description+code+field, "全日制") ||
					strings.Contains(name+description, "毕业") {
					score += 20
				}
			default:
				if strings.Contains(name+description+code+field, token) {
					score += 20
				}
			}
		}
		switch {
		case score > bestScore:
			bestCode, bestScore, tied = candidate.DimensionCode, score, false
		case score > 0 && score == bestScore:
			tied = true
		}
	}
	if bestScore == 0 || tied {
		return ""
	}
	return bestCode
}

type whereDesignCandidate struct {
	DimensionFieldName        string   `json:"dimensionFieldName"`
	DimensionFieldDescription string   `json:"dimensionFieldDescription"`
	QueryValue                string   `json:"queryValue"`
	MatchMethod               string   `json:"matchMethod"`
	SelectedValues            []string `json:"selectedValues"`
	MetricFieldName           string   `json:"metricFieldName"`
	TableName                 string   `json:"tableName"`
}

type whereDesignDecision struct {
	DimensionFieldName string   `json:"dimensionFieldName"`
	QueryValue         string   `json:"queryValue"`
	CanonicalValue     string   `json:"canonicalValue"`
	Operator           string   `json:"operator"`
	Values             []string `json:"values"`
	Reason             string   `json:"reason"`
	Confidence         float64  `json:"confidence"`
}

type whereDesignOutput struct {
	Decisions []whereDesignDecision `json:"decisions"`
}

func (interpreter *SemanticInterpreter) designWherePredicates(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
	lookups []QueryDimensionValueLookupTrace,
) []QueryDimensionValueLookupTrace {
	for index := range lookups {
		lookup := &lookups[index]
		lookup.Term = strings.TrimSpace(lookup.Term)
		lookup.CanonicalValue = deterministicCanonicalValue(*lookup)
		lookup.AliasValues = appendUniqueString(
			lookup.AliasValues, lookup.Term,
		)
		switch {
		case lookup.Term == "":
			lookup.WhereDesignStatus = "SKIPPED_EMPTY_VALUE"
		case lookup.DecisionID != "":
			if lookup.WhereDesignStatus == "" {
				lookup.WhereDesignStatus = "REUSED_DECISION_GRAPH"
			}
		case lookup.Sensitive:
			lookup.WhereDesignStatus = "SKIPPED_SENSITIVE_DIMENSION"
		case !lookup.Selected:
			lookup.WhereDesignStatus = "SKIPPED_NOT_SELECTED"
		case strings.TrimSpace(lookup.DimensionFieldName) == "":
			lookup.WhereDesignStatus = "SKIPPED_FIELD_NAME_MISSING"
		case strings.TrimSpace(lookup.DimensionFieldDescription) == "":
			lookup.WhereDesignStatus = "SKIPPED_FIELD_DESCRIPTION_MISSING"
		case interpreter.ai == nil || !interpreter.ai.Configured():
			lookup.WhereDesignStatus = "SKIPPED_PROVIDER_NOT_CONFIGURED"
		default:
			values := append([]string(nil), lookup.SelectedMemberKeys...)
			sort.Strings(values)
			candidate := whereDesignCandidate{
				DimensionFieldName:        lookup.DimensionFieldName,
				DimensionFieldDescription: lookup.DimensionFieldDescription,
				QueryValue:                lookup.Term,
				MatchMethod:               lookup.MatchMethod,
				SelectedValues:            values,
				MetricFieldName:           lookup.MetricFieldID,
				TableName: strings.Trim(
					lookup.TableSchema+"."+lookup.TableName, ".",
				),
			}
			decision, model, status := interpreter.designOneWherePredicate(
				ctx, tenantID, actorID, metricCode, question, candidate,
			)
			lookup.WhereDesignStatus = status
			if status != "SUCCEEDED" {
				continue
			}
			// The LLM supplies the synonym judgment, while the governed
			// member mapping fixes the persisted canonical spelling. This
			// prevents model wording drift such as “关键人才标签” from
			// creating a second vector key for “关键人才”.
			lookup.CanonicalValue = deterministicCanonicalValue(*lookup)
			lookup.WhereDesignOperator = strings.ToUpper(
				strings.TrimSpace(decision.Operator),
			)
			lookup.WhereDesignReason = strings.TrimSpace(decision.Reason)
			lookup.WhereDesignModel = model
		}
	}
	return lookups
}

func (interpreter *SemanticInterpreter) designOneWherePredicate(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
	candidate whereDesignCandidate,
) (whereDesignDecision, string, string) {
	payload, err := json.Marshal(struct {
		Question   string               `json:"question"`
		MetricCode string               `json:"metricCode"`
		Candidate  whereDesignCandidate `json:"candidate"`
	}{
		Question: question, MetricCode: metricCode, Candidate: candidate,
	})
	if err != nil {
		return whereDesignDecision{}, "", whereDesignFailureStatus(err)
	}
	temperature := 0.0
	result, err := interpreter.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: "semantic-query-where-design-v2",
		ResourceType:  "SEMANTIC_WHERE_DESIGN",
		ResourceID: hashText(strings.Join([]string{
			metricCode, question, candidate.DimensionFieldName,
			candidate.QueryValue,
		}, "\x00")),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读语义查询的逐维度决策器。本次只处理一个维度。先识别用户值与已治理标准值是否同义并给出 canonicalValue，再依据维度字段名、字段描述、指标字段、表名、匹配方式和标准值设计操作符。不得输出 SQL、字段表达式、表名或新查询值。dimensionFieldName、queryValue 必须逐字复制输入；若非 SEMANTIC_TAG 且 selectedValues 只有一个，canonicalValue 必须取该标准值，以便“智家/智家生态圈”等同义表达合并；其他情况 canonicalValue 必须取 queryValue。EQUALS/IN 的 values 必须逐字复制 selectedValues，SEMANTIC_TAG 必须选择 CONTAINS 且 values 只能包含 queryValue。单值选择 EQUALS，多值集合选择 IN。只返回一个决策。`,
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
				Name: "semantic_where_design",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["decisions"],
					"properties":{
						"decisions":{
							"type":"array","minItems":1,"maxItems":1,
							"items":{
								"type":"object","additionalProperties":false,
								"required":["dimensionFieldName","queryValue","canonicalValue","operator","values","reason","confidence"],
								"properties":{
									"dimensionFieldName":{"type":"string","maxLength":128},
									"queryValue":{"type":"string","maxLength":1024},
									"canonicalValue":{"type":"string","minLength":1,"maxLength":1024},
									"operator":{"enum":["EQUALS","IN","CONTAINS"]},
									"values":{"type":"array","maxItems":128,"items":{"type":"string","maxLength":1024}},
									"reason":{"type":"string","minLength":1,"maxLength":500},
									"confidence":{"type":"number","minimum":0,"maximum":1}
								}
							}
						}
				}
			}`),
			},
			Temperature: &temperature, MaxOutputTokens: 700,
		},
	})
	if err != nil {
		return whereDesignDecision{}, "", whereDesignFailureStatus(err)
	}
	var output whereDesignOutput
	if err := json.Unmarshal(
		result.ProviderResult.Content, &output,
	); err != nil || len(output.Decisions) != 1 {
		return whereDesignDecision{}, result.ProviderResult.Model,
			"FAILED_VALIDATION"
	}
	lookup := QueryDimensionValueLookupTrace{
		Term:               candidate.QueryValue,
		DimensionFieldName: candidate.DimensionFieldName,
		MatchMethod:        candidate.MatchMethod,
		SelectedMemberKeys: append(
			[]string(nil), candidate.SelectedValues...,
		),
	}
	if !validWhereDesignDecision(lookup, output.Decisions[0]) {
		return whereDesignDecision{}, result.ProviderResult.Model,
			"FAILED_VALIDATION"
	}
	return output.Decisions[0], result.ProviderResult.Model, "SUCCEEDED"
}

func whereDesignFailureStatus(err error) string {
	switch {
	case errors.Is(err, aiplatform.ErrQuotaExceeded):
		return "FAILED_QUOTA"
	case errors.Is(err, aiplatform.ErrTenantAIForbidden):
		return "FAILED_FORBIDDEN"
	}
	var providerErr *aiplatform.ProviderError
	if errors.As(err, &providerErr) {
		switch providerErr.Code {
		case aiplatform.ErrorCodeRateLimited:
			return "FAILED_RATE_LIMITED"
		case aiplatform.ErrorCodeTimeout:
			return "FAILED_TIMEOUT"
		}
	}
	return "FAILED"
}

func validWhereDesignDecision(
	lookup QueryDimensionValueLookupTrace,
	decision whereDesignDecision,
) bool {
	if decision.DimensionFieldName != lookup.DimensionFieldName ||
		decision.QueryValue != lookup.Term ||
		!validCanonicalSuggestion(decision.CanonicalValue) ||
		strings.TrimSpace(decision.Reason) == "" {
		return false
	}
	operator := strings.ToUpper(strings.TrimSpace(decision.Operator))
	values := append([]string(nil), decision.Values...)
	sort.Strings(values)
	selected := append([]string(nil), lookup.SelectedMemberKeys...)
	sort.Strings(selected)
	switch operator {
	case "CONTAINS":
		return lookup.MatchMethod == "SEMANTIC_TAG" &&
			len(values) == 1 && values[0] == lookup.Term
	case "EQUALS":
		return lookup.MatchMethod != "SEMANTIC_TAG" &&
			len(selected) == 1 && equalStrings(values, selected)
	case "IN":
		return lookup.MatchMethod != "SEMANTIC_TAG" &&
			len(selected) > 1 && equalStrings(values, selected)
	default:
		return false
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validCanonicalSuggestion(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func deterministicCanonicalValue(
	lookup QueryDimensionValueLookupTrace,
) string {
	term := strings.TrimSpace(lookup.Term)
	if lookup.MatchMethod == "SEMANTIC_TAG" {
		return term
	}
	values := append([]string(nil), lookup.SelectedMemberKeys...)
	sort.Strings(values)
	if len(values) == 1 && strings.TrimSpace(values[0]) != "" {
		return strings.TrimSpace(values[0])
	}
	return term
}

func appendUniqueString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return items
		}
	}
	return append(items, value)
}

func deduplicateSemanticLookups(
	items []QueryDimensionValueLookupTrace,
) []QueryDimensionValueLookupTrace {
	result := make([]QueryDimensionValueLookupTrace, 0, len(items))
	indexByKey := map[string]int{}
	for _, item := range items {
		if strings.TrimSpace(item.Term) == "" {
			continue
		}
		values := append([]string(nil), item.SelectedMemberKeys...)
		sort.Strings(values)
		key := strings.Join([]string{
			strings.ToLower(strings.TrimSpace(item.MetricCode)),
			strings.ToLower(strings.TrimSpace(item.DimensionCode)),
			strings.ToLower(strings.TrimSpace(item.CanonicalValue)),
			strings.Join(values, "\x1f"),
		}, "\x00")
		index, exists := indexByKey[key]
		if !exists {
			item.AliasValues = appendUniqueString(
				item.AliasValues, item.Term,
			)
			indexByKey[key] = len(result)
			result = append(result, item)
			continue
		}
		for _, alias := range item.AliasValues {
			result[index].AliasValues = appendUniqueString(
				result[index].AliasValues, alias,
			)
		}
		result[index].AliasValues = appendUniqueString(
			result[index].AliasValues, item.Term,
		)
	}
	return result
}

func dimensionVectorQuery(lookup QueryDimensionValueLookupTrace) string {
	description := strings.TrimSpace(lookup.DimensionFieldDescription)
	if description == "" {
		description = strings.TrimSpace(lookup.DimensionName)
	}
	value := strings.TrimSpace(lookup.CanonicalValue)
	if value == "" {
		value = strings.TrimSpace(lookup.Term)
	}
	if description == "" || value == "" {
		return ""
	}
	return description + ":" + value
}

type recallCandidate struct {
	SubjectType      string   `json:"subjectType"`
	Domain           string   `json:"domain,omitempty"`
	Code             string   `json:"code,omitempty"`
	DimensionCode    string   `json:"dimensionCode,omitempty"`
	Label            string   `json:"label"`
	Aliases          []string `json:"aliases,omitempty"`
	MemberValue      string   `json:"memberValue,omitempty"`
	DatasetVersionID string   `json:"datasetVersionId,omitempty"`
	TableSchema      string   `json:"tableSchema,omitempty"`
	TableName        string   `json:"tableName,omitempty"`
	Score            float64  `json:"score"`
}

type interpretedSlots struct {
	Intent             string  `json:"intent"`
	MetricCode         string  `json:"metricCode"`
	DimensionCode      string  `json:"dimensionCode"`
	MemberValue        string  `json:"memberValue"`
	Confidence         float64 `json:"confidence"`
	NeedsClarification bool    `json:"needsClarification"`
}

type interpretedTurnSlots struct {
	Intent             string   `json:"intent"`
	MetricCodes        []string `json:"metricCodes"`
	Confidence         float64  `json:"confidence"`
	NeedsClarification bool     `json:"needsClarification"`
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

// InterpretMany resolves every metric explicitly requested in one question.
// It deliberately stops at the published metric catalog. Dimension members are
// resolved independently by each metric-scoped QueryPlan after the dataset
// version has been fixed, so a value from another metric or domain cannot leak
// into planning.
func (interpreter *SemanticInterpreter) InterpretMany(
	ctx context.Context,
	tenantID, actorID, question string,
) (QueryTurnSlots, error) {
	if interpreter == nil || interpreter.store == nil ||
		strings.TrimSpace(question) == "" {
		return QueryTurnSlots{}, ErrInvalidRequest
	}
	broadMetricQuestion := questionRequestsBroadMetricSelection(question)
	candidates, err := interpreter.store.recall(ctx, tenantID, question, nil, 24)
	if err != nil {
		return QueryTurnSlots{}, err
	}
	buildExact := func(items []recallCandidate) QueryTurnSlots {
		codes := exactMetricCodes(question, items, 8)
		domains := make(map[string]string, len(codes))
		for _, code := range codes {
			domains[code] = candidateDomain(items, code)
		}
		return QueryTurnSlots{
			Intent: inferIntent(strings.ToLower(question)), MetricCodes: codes,
			MetricCandidateCount: len(items), MetricMatchMethod: "EXACT_CATALOG",
			Domains: domains,
			MetricCandidates: metricCandidateTraces(
				question, items, codes, "EXACT_CATALOG",
			),
		}
	}
	exact := buildExact(candidates)
	if len(exact.MetricCodes) > 0 {
		return exact, nil
	}
	if interpreter.embedding != nil && interpreter.embedding.Configured() {
		vectors, embedErr := interpreter.embedding.Embed(ctx, []string{question})
		if embedErr == nil && len(vectors) == 1 {
			vectorCandidates, recallErr := interpreter.store.recall(
				ctx, tenantID, question, vectors[0], 24,
			)
			if recallErr == nil {
				candidates = vectorCandidates
				exact = buildExact(candidates)
				if len(exact.MetricCodes) > 0 {
					return exact, nil
				}
			}
		}
	}
	if broadMetricQuestion {
		return QueryTurnSlots{
			Intent:               inferIntent(strings.ToLower(question)),
			MetricCandidateCount: len(candidates),
			MetricMatchMethod:    "HYBRID_RECALL",
			Domains:              map[string]string{},
			MetricCandidates: metricCandidateTraces(
				question, candidates, nil, "HYBRID_RECALL",
			),
			NeedsClarification: true,
		}, nil
	}
	// Exact published names, aliases and unambiguous metric stems do not need
	// a model round-trip. This keeps explicit multi-metric questions bounded
	// while the tool loop remains available for genuinely semantic wording.
	if toolAI, ok := interpreter.ai.(semanticToolAIInvoker); ok {
		if result, toolErr := interpreter.interpretManyWithToolLoop(
			ctx, toolAI, tenantID, actorID, question, "",
		); toolErr == nil {
			return result, nil
		} else if shouldRetrySemanticToolLoop(toolErr) {
			if fallbackAI, supported :=
				interpreter.ai.(semanticToolAIFallbackInvoker); supported {
				for _, model := range uniqueStrings(
					fallbackAI.FallbackModels(), 8,
				) {
					if ctx.Err() != nil {
						break
					}
					result, fallbackErr :=
						interpreter.interpretManyWithToolLoop(
							ctx, toolAI, tenantID, actorID, question, model,
						)
					if fallbackErr == nil {
						return result, nil
					}
					if !shouldRetrySemanticToolLoop(fallbackErr) {
						break
					}
				}
			}
		}
	}
	llmCandidates := externalRecallCandidates(candidates)
	fallback := QueryTurnSlots{
		Intent:               inferIntent(strings.ToLower(question)),
		MetricCandidateCount: len(candidates), Domains: map[string]string{},
		NeedsClarification: broadMetricQuestion,
		MetricCandidates: metricCandidateTraces(
			question, candidates, nil, "HYBRID_RECALL",
		),
	}
	withDecisionGraph := func(base QueryTurnSlots) QueryTurnSlots {
		if broadMetricQuestion {
			return base
		}
		graphCandidates, graphErr :=
			interpreter.store.inferPersonCountMetricsFromDecisionGraph(
				ctx, tenantID, question,
			)
		if graphErr != nil || len(graphCandidates) != 1 {
			return base
		}
		selected := graphCandidates[0]
		base.Intent = inferIntent(strings.ToLower(question))
		base.MetricCodes = []string{selected.Code}
		base.MetricMatchMethod = "DECISION_GRAPH"
		if base.Domains == nil {
			base.Domains = map[string]string{}
		}
		base.Domains[selected.Code] = selected.Domain
		base.MetricCandidates = metricTracesWithDecisionGraphSelection(
			question, candidates, selected,
		)
		return base
	}
	if interpreter.ai == nil || !interpreter.ai.Configured() ||
		len(llmCandidates) == 0 {
		return withDecisionGraph(fallback), nil
	}
	payload, err := json.Marshal(struct {
		Question   string            `json:"question"`
		Candidates []recallCandidate `json:"candidates"`
	}{Question: question, Candidates: llmCandidates})
	if err != nil {
		return QueryTurnSlots{}, err
	}
	temperature := 0.0
	result, err := interpreter.ai.Invoke(ctx, aiplatform.Invocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: "semantic-query-multi-metric-intent-v1",
		ResourceType:  "SEMANTIC_QUERY_TURN", ResourceID: hashText(question),
		Request: aiplatform.ProviderRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读语义问答指标解析器。识别用户在本轮明确要求查询的全部指标，metricCodes 只能逐字复制 candidates 中的 code，最多 8 个，按用户提及顺序排列并去重。不得把维度、维度值、数据集或派生猜测当作指标；不得输出 SQL。若本轮没有明确指标，metricCodes 返回空数组，交给服务端继承多轮上下文。识别 LOOKUP、METRIC、TREND、COMPARISON、RANKING、DRILLDOWN、DISTRIBUTION、FUNNEL、RETENTION、ANOMALY 或 UNKNOWN。`,
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
				Name: "semantic_query_turn_slots",
				Schema: json.RawMessage(`{
					"type":"object","additionalProperties":false,
					"required":["intent","metricCodes","confidence","needsClarification"],
					"properties":{
						"intent":{"enum":["LOOKUP","METRIC","TREND","COMPARISON","RANKING","DRILLDOWN","DISTRIBUTION","FUNNEL","RETENTION","ANOMALY","UNKNOWN"]},
						"metricCodes":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":128}},
						"confidence":{"type":"number","minimum":0,"maximum":1},
						"needsClarification":{"type":"boolean"}
					}
				}`),
			},
			Temperature: &temperature, MaxOutputTokens: 500,
		},
	})
	if err != nil {
		return withDecisionGraph(fallback), nil
	}
	var output interpretedTurnSlots
	if err := json.Unmarshal(result.ProviderResult.Content, &output); err != nil ||
		output.NeedsClarification ||
		!validInterpretedTurnSlots(output, llmCandidates) {
		return withDecisionGraph(fallback), nil
	}
	output.Intent = strings.ToUpper(strings.TrimSpace(output.Intent))
	if output.Intent == "LOOKUP" && questionRequestsAggregate(question) {
		output.Intent = "METRIC"
	}
	if broadMetricQuestion {
		fallback.Intent = output.Intent
		return fallback, nil
	}
	codes := uniqueStrings(output.MetricCodes, 8)
	if len(codes) == 0 {
		fallback.Intent = output.Intent
		return withDecisionGraph(fallback), nil
	}
	domains := make(map[string]string, len(codes))
	for _, code := range codes {
		domains[code] = candidateDomain(candidates, code)
	}
	return QueryTurnSlots{
		Intent: output.Intent, MetricCodes: codes,
		MetricCandidateCount: len(candidates),
		MetricMatchMethod:    "CATALOG_RERANK", Domains: domains,
		MetricCandidates: metricCandidateTraces(
			question, candidates, codes, "CATALOG_RERANK",
		),
	}, nil
}

func shouldRetrySemanticToolLoop(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) ||
		errors.Is(err, aiplatform.ErrQuotaExceeded) ||
		errors.Is(err, aiplatform.ErrTenantAIForbidden) ||
		errors.Is(err, ErrInvalidRequest) {
		return false
	}
	if errors.Is(err, ErrUnprovenPath) {
		return true
	}
	var providerErr *aiplatform.ProviderError
	return errors.As(err, &providerErr)
}

// ConfirmMetricCodes reloads user-confirmed codes from the published catalog.
// Browser-supplied labels, source tables and scores are never trusted.
func (interpreter *SemanticInterpreter) ConfirmMetricCodes(
	ctx context.Context,
	tenantID string,
	codes []string,
) (QueryTurnSlots, error) {
	codes = uniqueStrings(codes, 8)
	if interpreter == nil || interpreter.store == nil || len(codes) == 0 {
		return QueryTurnSlots{}, ErrInvalidRequest
	}
	selected := make([]recallCandidate, 0, len(codes))
	for _, code := range codes {
		items, err := interpreter.store.recall(ctx, tenantID, code, nil, 8)
		if err != nil {
			return QueryTurnSlots{}, err
		}
		found := false
		for _, item := range items {
			if item.SubjectType == "METRIC" &&
				strings.EqualFold(item.Code, code) {
				selected = append(selected, item)
				found = true
				break
			}
		}
		if !found {
			return QueryTurnSlots{}, ErrUnprovenPath
		}
	}
	domains := make(map[string]string, len(selected))
	for _, item := range selected {
		domains[item.Code] = item.Domain
	}
	return QueryTurnSlots{
		Intent: "UNKNOWN", MetricCodes: codes,
		MetricCandidateCount: len(selected),
		MetricMatchMethod:    "USER_CONFIRMED", Domains: domains,
		MetricCandidates: metricCandidateTraces(
			strings.Join(codes, " "), selected, codes, "USER_CONFIRMED",
		),
	}, nil
}

func metricCandidateTraces(
	question string,
	candidates []recallCandidate,
	selectedCodes []string,
	selectedMethod string,
) []QueryMetricCandidateTrace {
	question = strings.ToLower(question)
	selected := map[string]bool{}
	for _, code := range selectedCodes {
		selected[strings.ToLower(strings.TrimSpace(code))] = true
	}
	result := make([]QueryMetricCandidateTrace, 0, min(24, len(candidates)))
	for _, candidate := range candidates {
		if candidate.SubjectType != "METRIC" || candidate.Code == "" {
			continue
		}
		matchedTerm := ""
		for _, token := range metricExplicitTerms(candidate) {
			token = strings.TrimSpace(token)
			if token != "" && strings.Contains(question, strings.ToLower(token)) &&
				len([]rune(token)) > len([]rune(matchedTerm)) {
				matchedTerm = token
			}
		}
		isSelected := selected[strings.ToLower(candidate.Code)]
		method := "HYBRID_RECALL"
		if matchedTerm != "" {
			method = "EXACT_CATALOG"
		}
		if isSelected && selectedMethod != "" {
			method = selectedMethod
		}
		result = append(result, QueryMetricCandidateTrace{
			Code: candidate.Code, Label: candidate.Label,
			Domain:           candidate.Domain,
			DatasetVersionID: candidate.DatasetVersionID,
			TableSchema:      candidate.TableSchema, TableName: candidate.TableName,
			MatchedTerm: matchedTerm, MatchMethod: method,
			Score: candidate.Score, Selected: isSelected,
			Source: "CURRENT_TURN",
		})
		if len(result) == 24 {
			break
		}
	}
	return result
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

func exactMetricCodes(
	question string,
	candidates []recallCandidate,
	limit int,
) []string {
	question = strings.ToLower(question)
	type match struct {
		code     string
		position int
		length   int
	}
	matches := []match{}
	for _, candidate := range candidates {
		if candidate.SubjectType != "METRIC" || candidate.Code == "" {
			continue
		}
		position, length := -1, 0
		tokens := metricExplicitTerms(candidate)
		for _, token := range tokens {
			token = strings.ToLower(strings.TrimSpace(token))
			if token == "" {
				continue
			}
			if index := strings.Index(question, token); index >= 0 &&
				(position < 0 || index < position ||
					(index == position && len(token) > length)) {
				position, length = index, len(token)
			}
		}
		if position >= 0 {
			matches = append(matches, match{
				code: candidate.Code, position: position, length: length,
			})
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		if matches[left].position != matches[right].position {
			return matches[left].position < matches[right].position
		}
		if matches[left].length != matches[right].length {
			return matches[left].length > matches[right].length
		}
		return matches[left].code < matches[right].code
	})
	codes := make([]string, 0, min(limit, len(matches)))
	seen := map[string]bool{}
	for _, item := range matches {
		key := strings.ToLower(item.code)
		if seen[key] {
			continue
		}
		seen[key] = true
		codes = append(codes, item.code)
		if len(codes) == limit {
			break
		}
	}
	return codes
}

func metricExplicitTerms(candidate recallCandidate) []string {
	terms := append(
		[]string{candidate.Label, candidate.Code},
		candidate.Aliases...,
	)
	suffixes := []string{
		"订单数量合计", "商品数量合计", "金额合计", "实体数量",
		"记录数量", "总数量", "总金额", "数量合计", "记录数",
		"总量", "总数", "数量", "金额", "分钟数", "时长", "收入",
		"比例", "占比", "率", "数",
	}
	for _, source := range append(
		[]string{candidate.Label}, candidate.Aliases...,
	) {
		source = strings.TrimSpace(source)
		for _, suffix := range suffixes {
			if !strings.HasSuffix(source, suffix) {
				continue
			}
			stem := strings.TrimSpace(strings.TrimSuffix(source, suffix))
			if len([]rune(stem)) >= 2 {
				terms = appendUniqueString(terms, stem)
			}
			break
		}
	}
	return terms
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

func validInterpretedTurnSlots(
	output interpretedTurnSlots,
	candidates []recallCandidate,
) bool {
	if !oneOf(strings.ToUpper(strings.TrimSpace(output.Intent)),
		"LOOKUP", "METRIC", "TREND", "COMPARISON", "RANKING",
		"DRILLDOWN", "DISTRIBUTION", "FUNNEL", "RETENTION",
		"ANOMALY", "UNKNOWN",
	) || output.Confidence < 0 || output.Confidence > 1 ||
		len(output.MetricCodes) > 8 {
		return false
	}
	allowed := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.SubjectType == "METRIC" && candidate.Code != "" {
			allowed[candidate.Code] = true
		}
	}
	for _, code := range output.MetricCodes {
		if !allowed[strings.TrimSpace(code)] {
			return false
		}
	}
	return true
}

func uniqueStrings(values []string, limit int) []string {
	result := make([]string, 0, min(limit, len(values)))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
		if len(result) == limit {
			break
		}
	}
	return result
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
							COALESCE(semantic_alias.aliases,'{}'::text[]) AS aliases,
							''::text AS member_value,
							dataset_version.id::text AS dataset_version_id,
							COALESCE(materialization.published_schema,'') AS table_schema,
							COALESCE(materialization.published_name,'') AS table_name,
							GREATEST(
						  CASE WHEN $2<>'' AND document.embedding_status='SUCCEEDED'
						    THEN 1-(document.embedding <=> $2::halfvec)
						    ELSE 0 END,
						  similarity(lower(metric.name),lower($1)),
						  similarity(lower(metric.code::text),lower($1)),
						  CASE WHEN EXISTS(
						    SELECT 1
						    FROM unnest(COALESCE(semantic_alias.aliases,'{}'::text[])) AS alias(value)
						    WHERE position(lower(alias.value) IN lower($1))>0
						  ) THEN 1.0 ELSE 0 END,
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
					LEFT JOIN LATERAL (
					  SELECT array_agg(asset.common_term::text ORDER BY
					    char_length(asset.common_term::text) DESC,
					    lower(asset.common_term::text)
					  ) AS aliases
					  FROM platform.semantic_term_assets AS asset
					  WHERE asset.status='ACTIVE'
					    AND lower(asset.knowledge_type) IN (
					      'metric','metric_alias','指标','指标别名'
					    )
					    AND lower(asset.mapping_value) IN (
					      lower(metric.code::text),lower(metric.name)
					    )
						) AS semantic_alias ON true
						LEFT JOIN LATERAL (
						  SELECT current.published_schema,current.published_name
						  FROM platform.dataset_materializations AS current
						  WHERE current.tenant_id=dataset_version.tenant_id
						    AND current.dataset_version_id=dataset_version.id
						    AND current.layer='DWS'
						    AND current.status='ACTIVE'
						  ORDER BY current.activated_at DESC,current.id
						  LIMIT 1
						) AS materialization ON true
						WHERE metric_version.status='PUBLISHED'
					)
					SELECT 'METRIC',
						domain,code,dimension_code,label,aliases,member_value,
						dataset_version_id,table_schema,table_name,score
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
				&item.Label, &item.Aliases, &item.MemberValue,
				&item.DatasetVersionID, &item.TableSchema, &item.TableName,
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

func (store *PostgresStore) recallMetricDimensionMembers(
	ctx context.Context,
	tenantID, metricCode, dimensionCode string,
	vector []float32,
	limit int,
) (items []recallCandidate, err error) {
	items = []recallCandidate{}
	if store == nil || metricCode == "" || dimensionCode == "" ||
		len(vector) == 0 || limit < 1 || limit > 128 {
		return items, nil
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `WITH eligible AS (
				SELECT member.normalized_value AS member_value,
					GREATEST(
					  0.0,1.0-(document.embedding <=> $3::halfvec)
					)::float8 AS score
				FROM platform.metrics AS metric
				JOIN platform.metric_versions AS version
				  ON version.tenant_id=metric.tenant_id
				 AND version.metric_id=metric.id
				 AND version.id=metric.current_published_version_id
				 AND version.status='PUBLISHED'
				JOIN platform.semantic_dimensions AS dimension
				  ON dimension.tenant_id=version.tenant_id
				 AND dimension.dataset_version_id=version.dataset_version_id
				 AND lower(dimension.code::text)=lower($2)
				 AND dimension.status='PUBLISHED'
				JOIN platform.dimension_metric_compatibility AS compatibility
				  ON compatibility.tenant_id=dimension.tenant_id
				 AND compatibility.dimension_id=dimension.id
				 AND compatibility.metric_id=metric.id
				 AND compatibility.metric_version_id=version.id
				 AND compatibility.metric_dataset_version_id=
				     version.dataset_version_id
				 AND compatibility.status='VERIFIED'
				 AND compatibility.fanout_policy<>'UNSAFE'
				JOIN platform.dimension_member_semantic_documents AS document
				  ON document.tenant_id=dimension.tenant_id
				 AND document.dimension_id=dimension.id
				 AND document.dataset_version_id=version.dataset_version_id
				 AND document.embedding_status='SUCCEEDED'
				JOIN platform.dimension_members AS member
				  ON member.tenant_id=document.tenant_id
				 AND member.id=document.dimension_member_id
				 AND member.dimension_id=document.dimension_id
				 AND member.status='ACTIVE'
				 AND (member.valid_from IS NULL OR member.valid_from<=now())
				 AND (member.valid_to IS NULL OR member.valid_to>now())
				WHERE metric.code=$1
				  AND metric.status='PUBLISHED'
				  AND metric.deleted_at IS NULL
			)
			SELECT 'MEMBER'::text,''::text,''::text,$2::text,
				member_value,member_value,score
			FROM eligible
			WHERE score>=0.35
			ORDER BY score DESC,member_value
			LIMIT $4`,
			metricCode, dimensionCode, formatVector(vector), limit,
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item recallCandidate
			if err := rows.Scan(
				&item.SubjectType, &item.Domain, &item.Code,
				&item.DimensionCode, &item.Label,
				&item.MemberValue, &item.Score,
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
