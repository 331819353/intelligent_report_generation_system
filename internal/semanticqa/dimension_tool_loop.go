package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/platform/database"
)

const semanticDimensionToolLoopPromptVersion = "semantic-dimension-tool-loop-v1"

type dimensionSemanticToolEvidence struct {
	DimensionCode string  `json:"dimensionCode"`
	DimensionName string  `json:"dimensionName"`
	DimensionType string  `json:"dimensionType"`
	Description   string  `json:"description,omitempty"`
	MemberValue   string  `json:"memberValue,omitempty"`
	Score         float64 `json:"score"`
	MatchMethod   string  `json:"matchMethod"`
}

type dimensionSemanticToolResult struct {
	AvailableDimensions []dimensionSemanticToolEvidence `json:"availableDimensions"`
	RelevantCandidates  []dimensionSemanticToolEvidence `json:"relevantCandidates"`
}

type semanticDimensionSearchInput struct {
	Query string `json:"query"`
}

type semanticDimensionSelectionInput struct {
	DecisionIDs        []string `json:"decisionIds"`
	Confidence         float64  `json:"confidence"`
	NeedsClarification bool     `json:"needsClarification"`
}

type dimensionToolDecisionCandidate struct {
	DecisionID     string  `json:"decisionId"`
	DimensionCode  string  `json:"dimensionCode"`
	DimensionName  string  `json:"dimensionName"`
	CanonicalValue string  `json:"canonicalValue"`
	Score          float64 `json:"score"`
}

type dimensionToolLookupResult struct {
	Term               string                           `json:"term"`
	DimensionCode      string                           `json:"dimensionCode"`
	DimensionName      string                           `json:"dimensionName"`
	CanonicalValue     string                           `json:"canonicalValue,omitempty"`
	Selected           bool                             `json:"selected"`
	DecisionCandidates []dimensionToolDecisionCandidate `json:"decisionCandidates"`
}

type semanticDimensionToolExecutor struct {
	interpreter      *SemanticInterpreter
	tenantID         string
	actorID          string
	metricCode       string
	question         string
	hints            []QuerySemanticDimensionHint
	semanticSearches int
	decisionSearches int
	latestQuery      string
	lookups          []QueryDimensionValueLookupTrace
}

func (interpreter *SemanticInterpreter) ResolveDimensionsWithToolLoop(
	ctx context.Context,
	tenantID, actorID, metricCode, question string,
	hints []QuerySemanticDimensionHint,
) (QueryDimensionToolResolution, error) {
	if interpreter == nil || interpreter.store == nil ||
		strings.TrimSpace(metricCode) == "" || strings.TrimSpace(question) == "" {
		return QueryDimensionToolResolution{}, ErrInvalidRequest
	}
	toolAI, ok := interpreter.ai.(semanticToolAIInvoker)
	if !ok {
		return QueryDimensionToolResolution{}, ErrUnprovenPath
	}
	executor := &semanticDimensionToolExecutor{
		interpreter: interpreter, tenantID: tenantID, actorID: actorID,
		metricCode: metricCode, question: question,
		hints:   append([]QuerySemanticDimensionHint(nil), hints...),
		lookups: []QueryDimensionValueLookupTrace{},
	}
	payload, err := json.Marshal(map[string]any{
		"question": question, "metricCode": metricCode,
	})
	if err != nil {
		return QueryDimensionToolResolution{}, err
	}
	temperature := 0.0
	tools, err := defaultQuestionToolRegistry.RequiredDefinitions(
		"search_dimension_semantics", "search_dimension_decisions",
		"submit_dimension_selection",
	)
	if err != nil {
		return QueryDimensionToolResolution{}, err
	}
	result, err := toolAI.InvokeToolLoop(ctx, aiplatform.ToolInvocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:       aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion: semanticDimensionToolLoopPromptVersion,
		ResourceType:  "SEMANTIC_DIMENSION_TOOL_LOOP",
		ResourceID:    hashText(metricCode + "\x00" + question),
		Request: aiplatform.ToolLoopRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读维度语义检索代理。指标已经由上一阶段锁定；你只能在该指标已验证兼容的维度范围内工作。
先用完整问题调用 search_dimension_semantics，查看该指标当前可执行维度目录（最多 32 个）以及与问题相关的维度、维度值语义。结合结果补全问题中的维度名和值；不足时可以调整完整表达再次检索。
然后调用 search_dimension_decisions，以补全后的完整问题检索具体维度、成员和已持久化决策图。不得发明维度、成员或决策 ID。
若问题上下文能够唯一证明维度和值，只能提交工具返回的 decisionId；已经被服务端唯一选中的条件无需重复提交。若多个维度或成员同等合理，必须 needsClarification=true 且 decisionIds 为空，交给用户选择。纯指标问题没有维度条件时可以提交空 decisionIds 且 needsClarification=false。
终止工具 submit_dimension_selection 必须单独调用。不得生成 SQL、WHERE、表名或字段名。`,
					}},
				},
				{
					Role: aiplatform.MessageRoleUser,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText, Text: string(payload),
					}},
				},
			},
			Tools:      tools,
			ToolChoice: aiplatform.ToolChoiceAuto,
			Thinking:   true, Temperature: &temperature,
			MaxOutputTokens: 1400,
			MaxRounds:       aiplatform.MaxToolLoopRounds,
			MaxToolCalls:    aiplatform.MaxToolCallsPerLoop,
		},
		Executor: executor,
	})
	if err != nil {
		return QueryDimensionToolResolution{}, err
	}
	var selection semanticDimensionSelectionInput
	if json.Unmarshal(result.Content, &selection) != nil {
		return QueryDimensionToolResolution{}, ErrUnprovenPath
	}
	lookups, valid := applyToolSelectedDecisions(
		executor.lookups, selection.DecisionIDs,
	)
	if !valid {
		return QueryDimensionToolResolution{}, ErrUnprovenPath
	}
	if selection.NeedsClarification || selection.Confidence < 0.7 {
		lookups = clearAmbiguousToolSelections(lookups)
	}
	needsClarification := len(unresolvedDimensionDecisionIDs(lookups)) > 0
	steps := make([]QueryMetricToolLoopStep, 0, len(result.Trace.Steps))
	for _, step := range result.Trace.Steps {
		steps = append(steps, QueryMetricToolLoopStep{
			Round: step.Round, ToolName: step.ToolName,
			ArgumentsHash: step.ArgumentsHash, StateHash: step.StateHash,
			EvidenceIDs:      append([]string(nil), step.EvidenceIDs...),
			NewEvidenceCount: step.NewEvidenceCount,
			ErrorCode:        step.ErrorCode, Terminal: step.Terminal,
		})
	}
	return QueryDimensionToolResolution{
		Lookups: lookups, NeedsClarification: needsClarification,
		AugmentedQuestion: executor.augmentedQuestion(),
		Trace: &QueryDimensionToolLoopTrace{
			MetricCode: metricCode, AuditRequestID: result.RequestID,
			Model: result.Model, Rounds: result.Trace.Rounds,
			ToolCalls: result.Trace.ToolCalls, Steps: steps,
		},
	}, nil
}

func (executor *semanticDimensionToolExecutor) ExecuteTool(
	ctx context.Context,
	execution aiplatform.ToolExecution,
) (aiplatform.ToolExecutionResult, error) {
	if !defaultQuestionToolRegistry.Allowed(
		execution.Name, QuestionStateValidating,
	) {
		return aiplatform.ToolExecutionResult{}, ErrInvalidState
	}
	switch execution.Name {
	case "search_dimension_semantics":
		reportQueryTurnProgress(
			ctx, QueryProgressStageDimensionSemantic,
			QueryProgressStatusRunning,
			"正在检索已锁定指标的兼容维度语义",
		)
		query, err := semanticDimensionQuery(execution.Arguments)
		if err != nil {
			return aiplatform.ToolExecutionResult{}, err
		}
		result, err := executor.interpreter.recallMetricDimensionSemanticsForTool(
			ctx, executor.tenantID, executor.metricCode, query,
		)
		if err != nil {
			return aiplatform.ToolExecutionResult{}, err
		}
		executor.semanticSearches++
		reportQueryTurnProgress(
			ctx, QueryProgressStageDimensionSemantic,
			QueryProgressStatusSucceeded,
			fmt.Sprintf(
				"维度语义检索完成，发现 %d 个相关候选",
				len(result.RelevantCandidates),
			),
		)
		content, err := json.Marshal(result)
		return aiplatform.ToolExecutionResult{
			Content: content,
			EvidenceIDs: dimensionSemanticEvidenceIDs(
				executor.metricCode, query, result,
			),
		}, err
	case "search_dimension_decisions":
		if executor.semanticSearches == 0 {
			return aiplatform.ToolExecutionResult{},
				errors.New("dimension decision search requires semantic search first")
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageDimensionDecision,
			QueryProgressStatusRunning,
			"正在检索维度成员和已验证决策图",
		)
		query, err := semanticDimensionQuery(execution.Arguments)
		if err != nil {
			return aiplatform.ToolExecutionResult{}, err
		}
		var lookups []QueryDimensionValueLookupTrace
		if len(executor.hints) > 0 {
			lookups, err = executor.interpreter.EnrichDimensionLookupsWithHints(
				ctx, executor.tenantID, executor.actorID,
				executor.metricCode, query, executor.hints,
			)
		} else {
			lookups, err = executor.interpreter.EnrichDimensionLookups(
				ctx, executor.tenantID, executor.actorID,
				executor.metricCode, query,
			)
		}
		if err != nil {
			return aiplatform.ToolExecutionResult{}, err
		}
		executor.lookups = lookups
		executor.latestQuery = query
		executor.decisionSearches++
		reportQueryTurnProgress(
			ctx, QueryProgressStageDimensionDecision,
			QueryProgressStatusSucceeded,
			fmt.Sprintf("决策图检索完成，审查 %d 组维度值映射", len(lookups)),
		)
		content, err := json.Marshal(dimensionToolLookupResults(lookups))
		return aiplatform.ToolExecutionResult{
			Content: content,
			EvidenceIDs: dimensionDecisionEvidenceIDs(
				executor.metricCode, query, lookups,
			),
		}, err
	case "submit_dimension_selection":
		if executor.semanticSearches == 0 || executor.decisionSearches == 0 {
			return aiplatform.ToolExecutionResult{},
				errors.New("dimension selection requires semantic and decision searches")
		}
		var input semanticDimensionSelectionInput
		if json.Unmarshal(execution.Arguments, &input) != nil ||
			input.Confidence < 0 || input.Confidence > 1 {
			return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
		}
		if input.NeedsClarification && len(input.DecisionIDs) > 0 {
			return aiplatform.ToolExecutionResult{}, ErrUnprovenPath
		}
		if _, valid := applyToolSelectedDecisions(
			executor.lookups, input.DecisionIDs,
		); !valid {
			return aiplatform.ToolExecutionResult{}, ErrUnprovenPath
		}
		status, message := QueryProgressStatusSucceeded, "维度与维度值选择已通过决策图校验"
		if input.NeedsClarification {
			status, message = QueryProgressStatusWarn, "维度候选仍有歧义，准备请求用户确认"
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageDimensionSelection, status, message,
		)
		content, err := json.Marshal(input)
		return aiplatform.ToolExecutionResult{Content: content, Terminal: true}, err
	default:
		return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
	}
}

func semanticDimensionQuery(arguments json.RawMessage) (string, error) {
	var input semanticDimensionSearchInput
	if json.Unmarshal(arguments, &input) != nil {
		return "", ErrInvalidRequest
	}
	query := strings.TrimSpace(input.Query)
	if query == "" || len([]rune(query)) > 1024 {
		return "", ErrInvalidRequest
	}
	return query, nil
}

func (executor *semanticDimensionToolExecutor) augmentedQuestion() string {
	if executor == nil {
		return ""
	}
	question := strings.TrimSpace(executor.question)
	query := strings.TrimSpace(executor.latestQuery)
	if query == "" || strings.EqualFold(query, question) {
		return question
	}
	return question + "【维度语义补充：" + query + "】"
}

func dimensionToolLookupResults(
	lookups []QueryDimensionValueLookupTrace,
) []dimensionToolLookupResult {
	result := make([]dimensionToolLookupResult, 0, len(lookups))
	for _, lookup := range lookups {
		item := dimensionToolLookupResult{
			Term: lookup.Term, DimensionCode: lookup.DimensionCode,
			DimensionName:  lookup.DimensionName,
			CanonicalValue: lookup.CanonicalValue, Selected: lookup.Selected,
			DecisionCandidates: []dimensionToolDecisionCandidate{},
		}
		for _, candidate := range lookup.DecisionCandidates {
			if candidate.DecisionID == "" || candidate.MemberValue == "" {
				continue
			}
			item.DecisionCandidates = append(
				item.DecisionCandidates,
				dimensionToolDecisionCandidate{
					DecisionID:     candidate.DecisionID,
					DimensionCode:  lookup.DimensionCode,
					DimensionName:  lookup.DimensionName,
					CanonicalValue: candidate.CanonicalValue,
					Score:          candidate.Score,
				},
			)
		}
		result = append(result, item)
	}
	return result
}

func applyToolSelectedDecisions(
	lookups []QueryDimensionValueLookupTrace,
	decisionIDs []string,
) ([]QueryDimensionValueLookupTrace, bool) {
	result := cloneDimensionLookups(lookups)
	requested := map[string]bool{}
	for _, id := range uniqueStrings(decisionIDs, 16) {
		requested[id] = true
	}
	if len(requested) != len(decisionIDs) {
		return nil, false
	}
	matched := map[string]bool{}
	selectedByTerm := map[string]string{}
	for lookupIndex := range result {
		selectedForLookup := ""
		for candidateIndex := range result[lookupIndex].DecisionCandidates {
			candidate := &result[lookupIndex].DecisionCandidates[candidateIndex]
			if !requested[candidate.DecisionID] {
				continue
			}
			if selectedForLookup != "" {
				return nil, false
			}
			termKey := strings.ToLower(strings.TrimSpace(result[lookupIndex].Term))
			if previous := selectedByTerm[termKey]; termKey != "" && previous != "" {
				return nil, false
			}
			selectedForLookup = candidate.DecisionID
			if termKey != "" {
				selectedByTerm[termKey] = candidate.DecisionID
			}
			matched[candidate.DecisionID] = true
			result[lookupIndex].Selected = true
			result[lookupIndex].DecisionID = candidate.DecisionID
			result[lookupIndex].CanonicalValue = candidate.CanonicalValue
			result[lookupIndex].SelectedMemberKeys = []string{candidate.MemberValue}
			result[lookupIndex].WhereCondition = candidate.WhereCondition
			result[lookupIndex].CompiledCondition = candidate.CompiledCondition
			result[lookupIndex].WhereDesignOperator = candidate.PredicateOperator
			result[lookupIndex].WhereDesignStatus = "AGENT_SELECTED_DECISION_GRAPH"
			result[lookupIndex].TableSchema = candidate.TableSchema
			result[lookupIndex].TableName = candidate.TableName
			candidate.Selected = true
		}
	}
	if len(matched) != len(requested) {
		return nil, false
	}
	finalSelectionByTerm := map[string]string{}
	for _, lookup := range result {
		if !lookup.Selected {
			continue
		}
		termKey := strings.ToLower(strings.TrimSpace(lookup.Term))
		if termKey == "" {
			return nil, false
		}
		selectionKey := strings.TrimSpace(lookup.DecisionID)
		if selectionKey == "" {
			selectionKey = strings.ToLower(strings.TrimSpace(lookup.DimensionCode)) +
				"\x00" + strings.Join(lookup.SelectedMemberKeys, "\x1f")
		}
		if previous := finalSelectionByTerm[termKey]; previous != "" &&
			previous != selectionKey {
			return nil, false
		}
		finalSelectionByTerm[termKey] = selectionKey
	}
	return result, true
}

func cloneDimensionLookups(
	lookups []QueryDimensionValueLookupTrace,
) []QueryDimensionValueLookupTrace {
	result := append([]QueryDimensionValueLookupTrace(nil), lookups...)
	for index := range result {
		result[index].AliasValues = append([]string(nil), lookups[index].AliasValues...)
		result[index].SelectedMemberKeys = append(
			[]string(nil), lookups[index].SelectedMemberKeys...,
		)
		result[index].DecisionCandidates = append(
			[]QueryDecisionCandidate(nil), lookups[index].DecisionCandidates...,
		)
	}
	return result
}

func clearAmbiguousToolSelections(
	lookups []QueryDimensionValueLookupTrace,
) []QueryDimensionValueLookupTrace {
	result := cloneDimensionLookups(lookups)
	candidatesByTerm := map[string]int{}
	for _, lookup := range result {
		termKey := strings.ToLower(strings.TrimSpace(lookup.Term))
		for _, candidate := range lookup.DecisionCandidates {
			if termKey != "" && candidate.DecisionID != "" &&
				candidate.MemberValue != "" {
				candidatesByTerm[termKey]++
			}
		}
	}
	for index := range result {
		termKey := strings.ToLower(strings.TrimSpace(result[index].Term))
		if candidatesByTerm[termKey] < 2 {
			continue
		}
		result[index].Selected = false
		result[index].DecisionID = ""
		result[index].SelectedMemberKeys = nil
		result[index].WhereCondition = ""
		result[index].CompiledCondition = ""
		result[index].WhereDesignOperator = ""
		result[index].WhereDesignStatus = "NO_SAFE_DECISION_SELECTED"
		for candidateIndex := range result[index].DecisionCandidates {
			result[index].DecisionCandidates[candidateIndex].Selected = false
		}
	}
	return result
}

func unresolvedDimensionDecisionIDs(
	lookups []QueryDimensionValueLookupTrace,
) []string {
	result := []string{}
	for _, lookup := range lookups {
		if lookup.Selected || !dimensionLookupRelevantForClarification(lookup) {
			continue
		}
		for _, candidate := range lookup.DecisionCandidates {
			if candidate.DecisionID != "" && candidate.MemberValue != "" {
				result = appendUniqueString(result, candidate.DecisionID)
			}
		}
	}
	return result
}

func (interpreter *SemanticInterpreter) recallMetricDimensionSemanticsForTool(
	ctx context.Context,
	tenantID, metricCode, query string,
) (dimensionSemanticToolResult, error) {
	available, err := interpreter.store.metricCompatibleDimensionCatalog(
		ctx, tenantID, metricCode,
	)
	if err != nil {
		return dimensionSemanticToolResult{}, err
	}
	allowed := map[string]dimensionSemanticToolEvidence{}
	for _, item := range available {
		allowed[strings.ToLower(item.DimensionCode)] = item
	}
	_, corpus, err := interpreter.store.tokenSemanticCorpus(ctx, tenantID)
	if err != nil {
		return dimensionSemanticToolResult{}, err
	}
	filteredCorpus := make([]tokenSemanticCorpusItem, 0, len(corpus))
	for _, item := range corpus {
		if _, ok := allowed[strings.ToLower(item.DimensionCode)]; ok {
			filteredCorpus = append(filteredCorpus, item)
		}
	}
	candidates := rankTokenSemanticCorpus(
		QueryToken{Text: query, EntityType: "NOUN_CANDIDATE"},
		filteredCorpus, 24,
	)
	if interpreter.embedding != nil && interpreter.embedding.Configured() {
		vectors, embedErr := interpreter.embedding.Embed(ctx, []string{query})
		if embedErr == nil && len(vectors) == 1 {
			vectorCandidates, vectorErr :=
				interpreter.store.vectorTokenSemanticCandidates(
					ctx, tenantID, query, vectors[0], 128, false, true,
				)
			if vectorErr == nil {
				filtered := make([]QueryTokenSemanticCandidate, 0, 24)
				for _, candidate := range vectorCandidates {
					if _, ok := allowed[strings.ToLower(candidate.DimensionCode)]; !ok {
						continue
					}
					filtered = append(filtered, candidate)
					if len(filtered) == 24 {
						break
					}
				}
				if len(filtered) > 0 {
					candidates = filtered
				}
			}
		}
	}
	relevant := make([]dimensionSemanticToolEvidence, 0, len(candidates))
	for _, candidate := range candidates {
		relevant = append(relevant, dimensionSemanticToolEvidence{
			DimensionCode: candidate.DimensionCode,
			DimensionName: candidate.DimensionName,
			DimensionType: candidate.DimensionType,
			Description: boundedSemanticToolText(
				candidate.Description, 240,
			),
			MemberValue: candidate.Value,
			Score:       candidate.Score, MatchMethod: candidate.MatchMethod,
		})
	}
	return dimensionSemanticToolResult{
		AvailableDimensions: available, RelevantCandidates: relevant,
	}, nil
}

func (store *PostgresStore) metricCompatibleDimensionCatalog(
	ctx context.Context,
	tenantID, metricCode string,
) (items []dimensionSemanticToolEvidence, err error) {
	items = []dimensionSemanticToolEvidence{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT dimension.code::text,
				dimension.name,dimension.dimension_type,dimension.description
			FROM platform.metrics AS metric
			JOIN platform.metric_versions AS version
			  ON version.tenant_id=metric.tenant_id
			 AND version.id=metric.current_published_version_id
			 AND version.status='PUBLISHED'
			JOIN platform.dataset_versions AS dataset_version
			  ON dataset_version.tenant_id=version.tenant_id
			 AND dataset_version.id=version.dataset_version_id
			 AND dataset_version.status='PUBLISHED'
			JOIN platform.datasets AS dataset
			  ON dataset.tenant_id=dataset_version.tenant_id
			 AND dataset.id=dataset_version.dataset_id
			 AND dataset.current_published_version_id=dataset_version.id
			 AND dataset.status='PUBLISHED'
			 AND dataset.deleted_at IS NULL
			JOIN platform.dimension_metric_compatibility AS compatibility
			  ON compatibility.tenant_id=metric.tenant_id
			 AND compatibility.metric_id=metric.id
			 AND compatibility.metric_version_id=version.id
			 AND compatibility.metric_dataset_version_id=version.dataset_version_id
			 AND compatibility.status='VERIFIED'
			 AND compatibility.fanout_policy<>'UNSAFE'
			JOIN platform.semantic_dimensions AS dimension
			  ON dimension.tenant_id=compatibility.tenant_id
			 AND dimension.id=compatibility.dimension_id
			 -- QueryPlan currently compiles one exact metric dataset version;
			 -- do not expose bridge dimensions until governed JOIN compilation
			 -- becomes part of the executable query contract.
			 AND dimension.dataset_version_id=version.dataset_version_id
			 AND dimension.status='PUBLISHED'
			WHERE metric.code=$1
			  AND metric.status='PUBLISHED'
			  AND metric.deleted_at IS NULL
			  AND NOT dimension.sensitive
			ORDER BY dimension.name,dimension.code
			LIMIT 32`, metricCode)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item dimensionSemanticToolEvidence
			if scanErr := rows.Scan(
				&item.DimensionCode, &item.DimensionName,
				&item.DimensionType, &item.Description,
			); scanErr != nil {
				return scanErr
			}
			item.MatchMethod = "VERIFIED_METRIC_DIMENSION"
			item.Description = boundedSemanticToolText(item.Description, 240)
			items = append(items, item)
		}
		return rows.Err()
	})
	sort.SliceStable(items, func(left, right int) bool {
		if items[left].DimensionName != items[right].DimensionName {
			return items[left].DimensionName < items[right].DimensionName
		}
		return items[left].DimensionCode < items[right].DimensionCode
	})
	return items, err
}
