package semanticqa

import (
	"encoding/json"
	"sort"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

// QuestionToolSpec is the Host-owned contract for one read-only question tool.
// Model-visible definitions, phase policy and warehouse access are kept in one
// registry so a prompt cannot silently widen capabilities.
type QuestionToolSpec struct {
	Name            string
	CanonicalName   string
	Description     string
	Parameters      json.RawMessage
	AllowedStates   []QuestionState
	WarehouseAccess bool
	Terminal        bool
}

type QuestionToolSummary struct {
	Name            string          `json:"name"`
	CanonicalName   string          `json:"canonicalName"`
	AllowedStates   []QuestionState `json:"allowedStates"`
	WarehouseAccess bool            `json:"warehouseAccess"`
	Terminal        bool            `json:"terminal"`
}

type QuestionToolRegistry struct {
	items map[string]QuestionToolSpec
}

func newQuestionToolRegistry() *QuestionToolRegistry {
	metricQuery := json.RawMessage(`{
		"type":"object","additionalProperties":false,
		"required":["query"],
		"properties":{"query":{"type":"string","minLength":1,"maxLength":512}}
	}`)
	dimensionQuery := json.RawMessage(`{
		"type":"object","additionalProperties":false,
		"required":["query"],
		"properties":{"query":{"type":"string","minLength":1,"maxLength":1024}}
	}`)
	planID := json.RawMessage(`{
		"type":"object","additionalProperties":false,
		"required":["queryPlanId"],
		"properties":{"queryPlanId":{"type":"string","format":"uuid"}}
	}`)
	items := []QuestionToolSpec{
		{Name: "search_metric_semantics", CanonicalName: "search_semantic_objects", Description: "按完整问题检索指标语义对象，仅用于补全问题表达", Parameters: metricQuery, AllowedStates: []QuestionState{QuestionStateContextReady}},
		{Name: "search_metrics", CanonicalName: "search_semantic_objects", Description: "检索当前租户已发布且可执行的指标合同", Parameters: metricQuery, AllowedStates: []QuestionState{QuestionStateContextReady}},
		{Name: "submit_metric_selection", CanonicalName: "validate_semantic_bundle", Description: "提交最终指标绑定或请求最小澄清", Parameters: json.RawMessage(`{
			"type":"object","additionalProperties":false,
			"required":["intent","metricCodes","confidence","needsClarification"],
			"properties":{
				"intent":{"enum":["LOOKUP","METRIC","TREND","COMPARISON","RANKING","DRILLDOWN","DISTRIBUTION","FUNNEL","RETENTION","ANOMALY","UNKNOWN"]},
				"metricCodes":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":128}},
				"confidence":{"type":"number","minimum":0,"maximum":1},
				"needsClarification":{"type":"boolean"}
			}
		}`), AllowedStates: []QuestionState{QuestionStateContextReady}, Terminal: true},
		{Name: "search_dimension_semantics", CanonicalName: "search_semantic_objects", Description: "在已锁定指标范围内检索兼容维度语义", Parameters: dimensionQuery, AllowedStates: []QuestionState{QuestionStateValidating}},
		{Name: "search_dimension_decisions", CanonicalName: "lookup_dimension_values", Description: "检索维度作用域成员和值决策图", Parameters: dimensionQuery, AllowedStates: []QuestionState{QuestionStateValidating}},
		{Name: "submit_dimension_selection", CanonicalName: "validate_semantic_bundle", Description: "提交已证明的决策图 ID 或请求最小澄清", Parameters: json.RawMessage(`{
			"type":"object","additionalProperties":false,
			"required":["decisionIds","confidence","needsClarification"],
			"properties":{
				"decisionIds":{"type":"array","maxItems":16,"items":{"type":"string","maxLength":64}},
				"confidence":{"type":"number","minimum":0,"maximum":1},
				"needsClarification":{"type":"boolean"}
			}
		}`), AllowedStates: []QuestionState{QuestionStateValidating}, Terminal: true},
		{Name: "get_semantic_contracts", CanonicalName: "get_semantic_contracts", Description: "读取固定版本语义合同", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["objectIds"],"properties":{"objectIds":{"type":"array","maxItems":16,"items":{"type":"string"}}}}`), AllowedStates: []QuestionState{QuestionStateValidating}},
		{Name: "validate_semantic_bundle", CanonicalName: "validate_semantic_bundle", Description: "验证指标、维度和值的完整关系闭包", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["bundleId"],"properties":{"bundleId":{"type":"string"}}}`), AllowedStates: []QuestionState{QuestionStateValidating}},
		{Name: "get_data_quality_status", CanonicalName: "get_data_quality_status", Description: "读取数据新鲜度和阻断规则", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["datasetIds"],"properties":{"datasetIds":{"type":"array","maxItems":16,"items":{"type":"string","format":"uuid"}}}}`), AllowedStates: []QuestionState{QuestionStateValidating}, WarehouseAccess: true},
		{Name: "compile_semantic_query", CanonicalName: "compile_semantic_query", Description: "将已验证 Semantic IR 编译为只读计划", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["semanticIrHash"],"properties":{"semanticIrHash":{"type":"string"}}}`), AllowedStates: []QuestionState{QuestionStateValidating}},
		{Name: "validate_query_plan", CanonicalName: "validate_query_plan", Description: "执行图、fanout、权限和预算门禁", Parameters: planID, AllowedStates: []QuestionState{QuestionStateValidating}},
		{Name: "explain_query_plan", CanonicalName: "explain_query_plan", Description: "只读 EXPLAIN 或 dry-run", Parameters: planID, AllowedStates: []QuestionState{QuestionStateValidating}, WarehouseAccess: true},
		{Name: "execute_query_plan", CanonicalName: "execute_query_plan", Description: "执行已验证的只读计划", Parameters: planID, AllowedStates: []QuestionState{QuestionStateExecuting}, WarehouseAccess: true},
		{Name: "execute_validation_query", CanonicalName: "execute_validation_query", Description: "执行预注册结果验证查询", Parameters: planID, AllowedStates: []QuestionState{QuestionStateExecuting, QuestionStateResultVerified}, WarehouseAccess: true},
		{Name: "compare_candidate_results", CanonicalName: "compare_candidate_results", Description: "按确定性规则比较候选结果", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["leftResultId","rightResultId"],"properties":{"leftResultId":{"type":"string"},"rightResultId":{"type":"string"}}}`), AllowedStates: []QuestionState{QuestionStateResultVerified}},
		{Name: "request_clarification", CanonicalName: "request_clarification", Description: "生成有限澄清选项", Parameters: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["slot"],"properties":{"slot":{"type":"string"}}}`), AllowedStates: []QuestionState{QuestionStateValidating}, Terminal: true},
	}
	registry := &QuestionToolRegistry{items: make(map[string]QuestionToolSpec, len(items))}
	for _, item := range items {
		registry.items[item.Name] = item
	}
	return registry
}

func (registry *QuestionToolRegistry) RequiredDefinitions(
	names ...string,
) ([]aiplatform.ToolDefinition, error) {
	result := make([]aiplatform.ToolDefinition, 0, len(names))
	for _, name := range names {
		definition, ok := registry.Definition(name)
		if !ok {
			return nil, ErrInvalidState
		}
		result = append(result, definition)
	}
	return result, nil
}

var defaultQuestionToolRegistry = newQuestionToolRegistry()

func (registry *QuestionToolRegistry) Contains(name string) bool {
	if registry == nil {
		return false
	}
	_, ok := registry.items[name]
	return ok
}

func (registry *QuestionToolRegistry) Definition(name string) (aiplatform.ToolDefinition, bool) {
	if registry == nil {
		return aiplatform.ToolDefinition{}, false
	}
	item, ok := registry.items[name]
	if !ok {
		return aiplatform.ToolDefinition{}, false
	}
	return aiplatform.ToolDefinition{
		Name: item.Name, Description: item.Description,
		Parameters: append(json.RawMessage(nil), item.Parameters...),
	}, true
}

func (registry *QuestionToolRegistry) Allowed(name string, state QuestionState) bool {
	if registry == nil {
		return false
	}
	item, ok := registry.items[name]
	if !ok {
		return false
	}
	for _, allowed := range item.AllowedStates {
		if allowed == state {
			return true
		}
	}
	return false
}

func (registry *QuestionToolRegistry) PublicSummaries() []QuestionToolSummary {
	if registry == nil {
		return []QuestionToolSummary{}
	}
	names := make([]string, 0, len(registry.items))
	for name := range registry.items {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]QuestionToolSummary, 0, len(names))
	for _, name := range names {
		item := registry.items[name]
		result = append(result, QuestionToolSummary{
			Name: item.Name, CanonicalName: item.CanonicalName,
			AllowedStates:   append([]QuestionState(nil), item.AllowedStates...),
			WarehouseAccess: item.WarehouseAccess, Terminal: item.Terminal,
		})
	}
	return result
}
