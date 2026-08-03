package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

const semanticMetricToolLoopPromptVersion = "semantic-metric-tool-loop-v3"

type semanticMetricToolExecutor struct {
	interpreter       *SemanticInterpreter
	tenantID          string
	question          string
	candidates        map[string]recallCandidate
	order             []string
	semanticSearches  int
	metricSearches    int
	latestMetricQuery string
}

type semanticMetricSearchInput struct {
	Query string `json:"query"`
}

type semanticMetricEvidence struct {
	Code        string  `json:"code,omitempty"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Score       float64 `json:"score"`
	MatchMethod string  `json:"matchMethod"`
}

type semanticMetricSelectionInput struct {
	Intent             string   `json:"intent"`
	MetricCodes        []string `json:"metricCodes"`
	Confidence         float64  `json:"confidence"`
	NeedsClarification bool     `json:"needsClarification"`
}

func (interpreter *SemanticInterpreter) interpretManyWithToolLoop(
	ctx context.Context,
	toolAI semanticToolAIInvoker,
	tenantID, actorID, question string,
	preferredModel string,
	parsingRules semanticParsingRules,
) (QueryTurnSlots, error) {
	executor := &semanticMetricToolExecutor{
		interpreter: interpreter, tenantID: tenantID, question: question,
		candidates: map[string]recallCandidate{}, order: []string{},
	}
	payload, err := json.Marshal(struct {
		Question string `json:"question"`
	}{
		Question: question,
	})
	if err != nil {
		return QueryTurnSlots{}, err
	}
	temperature := 0.0
	tools, err := defaultQuestionToolRegistry.RequiredDefinitions(
		"search_metric_semantics", "search_metrics", "submit_metric_selection",
	)
	if err != nil {
		return QueryTurnSlots{}, err
	}
	result, err := toolAI.InvokeToolLoop(ctx, aiplatform.ToolInvocation{
		TenantID: tenantID, ActorID: actorID,
		Purpose:        aiplatform.PurposeSemanticQueryPlanning,
		PromptVersion:  semanticMetricToolLoopPromptVersion,
		ResourceType:   "SEMANTIC_QUERY_TOOL_LOOP",
		ResourceID:     hashText(question),
		PreferredModel: strings.TrimSpace(preferredModel),
		Request: aiplatform.ToolLoopRequest{
			Messages: []aiplatform.Message{
				{
					Role: aiplatform.MessageRoleSystem,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText,
						Text: `你是只读指标语义检索代理。你不能访问数据库或生成 SQL，只能调用给定工具，并严格按阶段工作。
第一阶段先使用完整原问题调用 search_metric_semantics，从指标语义库取得名称、口径和说明。根据语义证据补全用户问题；证据不足时可以改写完整问题再次检索语义库。
第二阶段调用 search_metrics，把补全后的完整指标问题作为 query，从已发布指标清单取得可选择的具体指标。候选不足时，可以根据已有语义证据调整完整指标短语后再次检索指标清单。
不得把查询动作、时间、分析方式、数字、单位、维度名或维度值作为独立指标查询，不得逐个搜索普通分词。只有 search_metrics 返回的已发布指标可以被选择。
识别用户本轮明确要求的全部指标，最多 8 个。若存在多个同等可能的指标、没有可靠候选或用户必须确认，则调用 submit_metric_selection，needsClarification=true 且 metricCodes 为空；不得擅自猜测。
能够唯一判断时调用 submit_metric_selection，并逐字复制工具返回的 code。终止工具必须单独调用。`,
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
		return QueryTurnSlots{}, err
	}
	var selection semanticMetricSelectionInput
	if json.Unmarshal(result.Content, &selection) != nil {
		return QueryTurnSlots{}, ErrUnprovenPath
	}
	candidates := executor.orderedCandidates()
	external := externalRecallCandidates(candidates)
	if !validInterpretedTurnSlots(
		interpretedTurnSlots{
			Intent: selection.Intent, MetricCodes: selection.MetricCodes,
			Confidence:         selection.Confidence,
			NeedsClarification: selection.NeedsClarification,
		},
		external,
	) {
		return QueryTurnSlots{}, ErrUnprovenPath
	}
	codes := uniqueStrings(selection.MetricCodes, 8)
	needsClarification := selection.NeedsClarification ||
		selection.Confidence < 0.7 || len(codes) == 0
	if needsClarification {
		codes = []string{}
	}
	domains := make(map[string]string, len(codes))
	for _, code := range codes {
		domains[code] = candidateDomain(candidates, code)
	}
	toolSteps := make([]QueryMetricToolLoopStep, 0, len(result.Trace.Steps))
	for _, step := range result.Trace.Steps {
		toolSteps = append(toolSteps, QueryMetricToolLoopStep{
			Round: step.Round, ToolName: step.ToolName,
			ArgumentsHash: step.ArgumentsHash, StateHash: step.StateHash,
			EvidenceIDs:      append([]string(nil), step.EvidenceIDs...),
			NewEvidenceCount: step.NewEvidenceCount,
			ErrorCode:        step.ErrorCode, Terminal: step.Terminal,
		})
	}
	return QueryTurnSlots{
		Intent:      strings.ToUpper(strings.TrimSpace(selection.Intent)),
		MetricCodes: codes, MetricCandidateCount: len(candidates),
		MetricMatchMethod: "AGENT_TOOL_LOOP", Domains: domains,
		AugmentedQuestion: executor.augmentedQuestion(),
		MetricCandidates: metricCandidateTraces(
			question, candidates, codes, "AGENT_TOOL_LOOP", parsingRules,
		),
		NeedsClarification: needsClarification,
		MetricToolLoop: &QueryMetricToolLoopTrace{
			AuditRequestID: result.RequestID, Model: result.Model,
			Rounds: result.Trace.Rounds, ToolCalls: result.Trace.ToolCalls,
			Steps: toolSteps,
		},
	}, nil
}

func (executor *semanticMetricToolExecutor) ExecuteTool(
	ctx context.Context,
	execution aiplatform.ToolExecution,
) (aiplatform.ToolExecutionResult, error) {
	if !defaultQuestionToolRegistry.Allowed(
		execution.Name, QuestionStateContextReady,
	) {
		return aiplatform.ToolExecutionResult{}, ErrInvalidState
	}
	switch execution.Name {
	case "search_metric_semantics":
		reportQueryTurnProgress(
			ctx, QueryProgressStageMetricSemantic, QueryProgressStatusRunning,
			"正在检索指标语义库并补全问题表达",
		)
		var input semanticMetricSearchInput
		if json.Unmarshal(execution.Arguments, &input) != nil {
			return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
		}
		query := strings.TrimSpace(input.Query)
		if query == "" || len([]rune(query)) > 512 {
			return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
		}
		candidates, err := executor.interpreter.recallMetricSemanticsForTool(
			ctx, executor.tenantID, query,
		)
		if err != nil {
			return aiplatform.ToolExecutionResult{}, err
		}
		executor.semanticSearches++
		reportQueryTurnProgress(
			ctx, QueryProgressStageMetricSemantic,
			QueryProgressStatusSucceeded,
			fmt.Sprintf("指标语义检索完成，召回 %d 条语义候选", len(candidates)),
		)
		content, err := json.Marshal(map[string]any{
			"query": query, "semanticCandidates": candidates,
		})
		return aiplatform.ToolExecutionResult{
			Content:     content,
			EvidenceIDs: metricSemanticEvidenceIDs(query, candidates),
		}, err
	case "search_metrics":
		if executor.semanticSearches == 0 {
			return aiplatform.ToolExecutionResult{},
				errors.New("metric catalog search requires semantic search first")
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageMetricCatalog, QueryProgressStatusRunning,
			"正在使用补全后的问题检索已发布指标清单",
		)
		var input semanticMetricSearchInput
		if json.Unmarshal(execution.Arguments, &input) != nil {
			return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
		}
		query := strings.TrimSpace(input.Query)
		if query == "" || len([]rune(query)) > 512 {
			return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
		}
		candidates, err := executor.interpreter.recallMetricsForTool(
			ctx, executor.tenantID, query,
		)
		if err != nil {
			return aiplatform.ToolExecutionResult{}, err
		}
		executor.addCandidates(candidates)
		executor.metricSearches++
		executor.latestMetricQuery = query
		reportQueryTurnProgress(
			ctx, QueryProgressStageMetricCatalog,
			QueryProgressStatusSucceeded,
			fmt.Sprintf("指标清单检索完成，召回 %d 个可用候选", len(candidates)),
		)
		content, err := json.Marshal(map[string]any{
			"query":      query,
			"candidates": externalRecallCandidates(candidates),
		})
		return aiplatform.ToolExecutionResult{
			Content:     content,
			EvidenceIDs: metricCatalogEvidenceIDs(query, candidates),
		}, err
	case "submit_metric_selection":
		if executor.semanticSearches == 0 || executor.metricSearches == 0 {
			return aiplatform.ToolExecutionResult{},
				errors.New("metric selection requires semantic and catalog searches")
		}
		var input semanticMetricSelectionInput
		if json.Unmarshal(execution.Arguments, &input) != nil {
			return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
		}
		if !validInterpretedTurnSlots(
			interpretedTurnSlots{
				Intent: input.Intent, MetricCodes: input.MetricCodes,
				Confidence:         input.Confidence,
				NeedsClarification: input.NeedsClarification,
			},
			externalRecallCandidates(executor.orderedCandidates()),
		) {
			return aiplatform.ToolExecutionResult{}, ErrUnprovenPath
		}
		status, message := QueryProgressStatusSucceeded, "指标选择已通过目录约束校验"
		if input.NeedsClarification || len(input.MetricCodes) == 0 {
			status, message = QueryProgressStatusWarn, "指标候选仍有歧义，准备请求用户确认"
		}
		reportQueryTurnProgress(
			ctx, QueryProgressStageMetricSelection, status, message,
		)
		content, err := json.Marshal(input)
		return aiplatform.ToolExecutionResult{
			Content: content, Terminal: true,
		}, err
	default:
		return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
	}
}

func (executor *semanticMetricToolExecutor) augmentedQuestion() string {
	if executor == nil {
		return ""
	}
	question := strings.TrimSpace(executor.question)
	query := strings.TrimSpace(executor.latestMetricQuery)
	if query == "" || strings.EqualFold(query, question) {
		return question
	}
	return question + "【指标语义补充：" + query + "】"
}

func (interpreter *SemanticInterpreter) recallMetricSemanticsForTool(
	ctx context.Context,
	tenantID, query string,
) ([]semanticMetricEvidence, error) {
	metrics, _, err := interpreter.store.tokenSemanticCorpus(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	candidates := rankTokenSemanticCorpus(
		QueryToken{Text: query, EntityType: "NOUN_CANDIDATE"}, metrics, 12,
	)
	if interpreter.embedding != nil && interpreter.embedding.Configured() {
		vectors, embedErr := interpreter.embedding.Embed(ctx, []string{query})
		if embedErr == nil && len(vectors) == 1 {
			vectorCandidates, vectorErr :=
				interpreter.store.vectorTokenSemanticCandidates(
					ctx, tenantID, query, vectors[0], 12, true, false,
				)
			if vectorErr == nil && len(vectorCandidates) > 0 {
				candidates = vectorCandidates
			}
		}
	}
	result := make([]semanticMetricEvidence, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate.Name))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, semanticMetricEvidence{
			Code: candidate.Code, Name: candidate.Name,
			Description: boundedSemanticToolText(
				candidate.Description, 320,
			),
			Score: candidate.Score, MatchMethod: candidate.MatchMethod,
		})
	}
	return result, nil
}

func (executor *semanticMetricToolExecutor) addCandidates(
	candidates []recallCandidate,
) {
	if executor == nil {
		return
	}
	if executor.candidates == nil {
		executor.candidates = map[string]recallCandidate{}
	}
	for _, candidate := range candidates {
		if candidate.SubjectType != "METRIC" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(candidate.Code))
		if key == "" {
			continue
		}
		if current, exists := executor.candidates[key]; !exists {
			executor.candidates[key] = candidate
			executor.order = append(executor.order, key)
		} else if candidate.Score > current.Score {
			executor.candidates[key] = candidate
		}
	}
}

func boundedSemanticToolText(value string, maximum int) string {
	value = strings.TrimSpace(value)
	characters := []rune(value)
	if maximum < 1 || len(characters) <= maximum {
		return value
	}
	return strings.TrimSpace(string(characters[:maximum])) + "…"
}

func (interpreter *SemanticInterpreter) recallMetricsForTool(
	ctx context.Context,
	tenantID, query string,
) ([]recallCandidate, error) {
	candidates, err := interpreter.store.recall(
		ctx, tenantID, query, nil, 24,
	)
	if err != nil {
		return nil, err
	}
	if interpreter.embedding == nil || !interpreter.embedding.Configured() {
		return candidates, nil
	}
	vectors, err := interpreter.embedding.Embed(ctx, []string{query})
	if err != nil || len(vectors) != 1 {
		return candidates, nil
	}
	vectorCandidates, err := interpreter.store.recall(
		ctx, tenantID, query, vectors[0], 24,
	)
	if err != nil {
		return candidates, nil
	}
	return vectorCandidates, nil
}

func (executor *semanticMetricToolExecutor) orderedCandidates() []recallCandidate {
	result := make([]recallCandidate, 0, len(executor.candidates))
	for _, key := range executor.order {
		result = append(result, executor.candidates[key])
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].Score > result[right].Score
	})
	return result
}
