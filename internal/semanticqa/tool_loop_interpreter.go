package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

const semanticMetricToolLoopPromptVersion = "semantic-metric-tool-loop-v1"

type semanticMetricToolExecutor struct {
	interpreter *SemanticInterpreter
	tenantID    string
	question    string
	candidates  map[string]recallCandidate
	order       []string
}

type semanticMetricSearchInput struct {
	Query string `json:"query"`
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
	tenantID, actorID, question, preferredModel string,
) (QueryTurnSlots, error) {
	executor := &semanticMetricToolExecutor{
		interpreter: interpreter, tenantID: tenantID, question: question,
		candidates: map[string]recallCandidate{}, order: []string{},
	}
	temperature := 0.0
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
						Text: `你是只读指标语义检索代理。你不能访问数据库或生成 SQL，只能调用给定工具。
先根据完整问题调用 search_metrics；候选不足时可以改写一次检索词继续检索。只有工具返回的已发布指标可以被选择。
识别用户本轮明确要求的全部指标，最多 8 个。若存在多个同等可能的指标、没有可靠候选或用户必须确认，则调用 submit_metric_selection，needsClarification=true 且 metricCodes 为空；不得擅自猜测。
能够唯一判断时调用 submit_metric_selection，并逐字复制工具返回的 code。终止工具必须单独调用。`,
					}},
				},
				{
					Role: aiplatform.MessageRoleUser,
					Parts: []aiplatform.ContentPart{{
						Type: aiplatform.ContentTypeText, Text: question,
					}},
				},
			},
			Tools: []aiplatform.ToolDefinition{
				{
					Name:        "search_metrics",
					Description: "按用户表达检索当前租户已发布指标、指标语义和来源物化表。",
					Parameters: json.RawMessage(`{
						"type":"object","additionalProperties":false,
						"required":["query"],
						"properties":{
							"query":{"type":"string","minLength":1,"maxLength":512}
						}
					}`),
				},
				{
					Name:        "submit_metric_selection",
					Description: "提交最终指标选择或明确请求用户确认；这是唯一终止工具。",
					Parameters: json.RawMessage(`{
						"type":"object","additionalProperties":false,
						"required":["intent","metricCodes","confidence","needsClarification"],
						"properties":{
							"intent":{"enum":[
								"LOOKUP","METRIC","TREND","COMPARISON","RANKING",
								"DRILLDOWN","DISTRIBUTION","FUNNEL","RETENTION",
								"ANOMALY","UNKNOWN"
							]},
							"metricCodes":{"type":"array","maxItems":8,
								"items":{"type":"string","maxLength":128}},
							"confidence":{"type":"number","minimum":0,"maximum":1},
							"needsClarification":{"type":"boolean"}
						}
					}`),
				},
			},
			ToolChoice: aiplatform.ToolChoiceAuto,
			Thinking:   true, Temperature: &temperature,
			MaxOutputTokens: 1200, MaxRounds: 4, MaxToolCalls: 6,
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
	explicitCodes := exactMetricCodes(question, candidates, 8)
	if len(explicitCodes) > 0 {
		// Published names, aliases and distinctive metric stems in the user's
		// original question are stronger evidence than a model clarification.
		codes = explicitCodes
		needsClarification = false
	} else if len(codes) > 1 ||
		questionRequestsBroadMetricSelection(question) {
		// A broad question such as "经营情况怎么样" must not let the model
		// invent an arbitrary KPI bundle. Without any explicit metric anchor,
		// either one arbitrary KPI or a bundle is a real user decision.
		codes = []string{}
		needsClarification = true
	}
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
			Round: step.Round, ToolName: step.ToolName, Terminal: step.Terminal,
		})
	}
	return QueryTurnSlots{
		Intent:      strings.ToUpper(strings.TrimSpace(selection.Intent)),
		MetricCodes: codes, MetricCandidateCount: len(candidates),
		MetricMatchMethod: "AGENT_TOOL_LOOP", Domains: domains,
		MetricCandidates: metricCandidateTraces(
			question, candidates, codes, "AGENT_TOOL_LOOP",
		),
		NeedsClarification: needsClarification,
		MetricToolLoop: &QueryMetricToolLoopTrace{
			AuditRequestID: result.RequestID, Model: result.Model,
			Rounds: result.Trace.Rounds, ToolCalls: result.Trace.ToolCalls,
			Steps: toolSteps,
		},
	}, nil
}

func questionRequestsBroadMetricSelection(question string) bool {
	normalized := strings.ToLower(strings.TrimSpace(question))
	for _, phrase := range []string{
		"经营情况", "业务情况", "整体情况", "总体情况",
		"经营怎么样", "业务怎么样", "表现怎么样", "数据怎么样",
		"经营如何", "业务如何", "整体如何",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func (executor *semanticMetricToolExecutor) ExecuteTool(
	ctx context.Context,
	execution aiplatform.ToolExecution,
) (aiplatform.ToolExecutionResult, error) {
	switch execution.Name {
	case "search_metrics":
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
		for _, candidate := range candidates {
			key := strings.ToLower(candidate.Code)
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
		content, err := json.Marshal(map[string]any{
			"query":      query,
			"candidates": externalRecallCandidates(candidates),
		})
		return aiplatform.ToolExecutionResult{Content: content}, err
	case "submit_metric_selection":
		if len(executor.candidates) == 0 {
			return aiplatform.ToolExecutionResult{},
				errors.New("metric selection requires a completed search")
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
		content, err := json.Marshal(input)
		return aiplatform.ToolExecutionResult{
			Content: content, Terminal: true,
		}, err
	default:
		return aiplatform.ToolExecutionResult{}, ErrInvalidRequest
	}
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
