// Package reportbinding 将报告筛选和图表点击声明解析为确定性的查询目标。
package reportbinding

import (
	"fmt"
	"reflect"
	"strings"

	"intelligent-report-generation-system/internal/reportjson"
)

// InteractionKind 区分筛选器改值与图表点击，避免调用方伪造不匹配的来源类型。
type InteractionKind string

const (
	FilterChange InteractionKind = "FILTER_CHANGE"
	ChartClick   InteractionKind = "CHART_CLICK"
)

// Interaction 是一次尚未执行的报告运行态交互，不会写回报告 JSON。
type Interaction struct {
	Kind              InteractionKind
	SourceComponentID string
	Value             any
	DimensionFieldID  string
}

// Target 是一个组件最终需要使用的数据集参数映射。
type Target struct {
	ComponentID          string
	DatasetVersionID     string
	FieldID              string
	DatasetParameterCode string
	SemanticFieldCode    string
	Operator             string
}

// Resolution 同时返回报告参数 code 和各目标数据集自己的参数 code。
type Resolution struct {
	ParameterID   string
	ParameterCode string
	Parameters    map[string]any
	Targets       []Target
}

// ResolveError 提供稳定路径，便于上层把绑定问题定位到具体组件配置。
type ResolveError struct {
	Path   string
	Reason string
}

func (e *ResolveError) Error() string {
	if e.Path == "" {
		return e.Reason
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Reason)
}

type componentLocation struct {
	pageID, blockID, path string
	component             reportjson.Component
}

type effectScope struct {
	kind         string
	componentIDs []string
}

// Resolve 把 JSON 中的 parameterId 引用转换成以 Parameter.Code 为键的运行参数，
// 并按文档顺序输出目标，整个过程只读且不修改 document 或 currentParameters。
func Resolve(document reportjson.Document, currentParameters map[string]any, interaction Interaction) (Resolution, error) {
	locations, locationsByID, err := collectComponents(document)
	if err != nil {
		return Resolution{}, err
	}
	source, exists := locationsByID[interaction.SourceComponentID]
	if !exists {
		return Resolution{}, issue("pages", fmt.Sprintf("交互来源组件 %s 不存在", interaction.SourceComponentID))
	}

	config, configPath, err := interactionConfig(source, interaction.Kind)
	if err != nil {
		return Resolution{}, err
	}
	parameterID, ok := stringProperty(config, "parameterId")
	if !ok || strings.TrimSpace(parameterID) == "" {
		return Resolution{}, issue(configPath+".parameterId", "必须引用报告参数")
	}
	parameter, err := findParameter(document.Parameters, parameterID)
	if err != nil {
		return Resolution{}, issue(configPath+".parameterId", err.Error())
	}
	if parameter.Scope == "PAGE" {
		if parameter.PageID == "" {
			return Resolution{}, issue(configPath+".parameterId", fmt.Sprintf("页面参数 %s 缺少 pageId", parameter.Code))
		}
		if parameter.PageID != source.pageID {
			return Resolution{}, issue(configPath+".parameterId", fmt.Sprintf("页面参数 %s 不能从其他页面触发", parameter.Code))
		}
	}

	operator, ok := stringProperty(config, "operator")
	if !ok || !allowedOperator(operator) {
		return Resolution{}, issue(configPath+".operator", "联动操作符无效")
	}
	if reason := validateOperatorValue(operator, parameter, interaction.Value); reason != "" {
		return Resolution{}, issue(configPath+".operator", reason)
	}
	scope, err := parseEffectScope(config["effectScope"], configPath+".effectScope")
	if err != nil {
		return Resolution{}, err
	}
	candidates, err := scopedComponents(locations, locationsByID, source, scope, configPath+".effectScope")
	if err != nil {
		return Resolution{}, err
	}
	if parameter.Scope == "PAGE" {
		for _, candidate := range candidates {
			if candidate.pageID != parameter.PageID {
				return Resolution{}, issue(configPath+".effectScope", fmt.Sprintf("页面参数 %s 不能影响其他页面", parameter.Code))
			}
		}
	}

	mappings, semanticFieldCode, err := indexMappings(parameter, configPath+".parameterId")
	if err != nil {
		return Resolution{}, err
	}
	if interaction.Kind == ChartClick {
		if err := validateChartDimension(source, interaction.DimensionFieldID, mappings, configPath); err != nil {
			return Resolution{}, err
		}
	}

	targets := make([]Target, 0, len(candidates))
	for _, candidate := range candidates {
		datasetVersionID, configured, err := componentDatasetVersion(candidate)
		if err != nil {
			return Resolution{}, err
		}
		if !configured {
			if scope.kind == "COMPONENTS" {
				return Resolution{}, issue(configPath+".effectScope", fmt.Sprintf("目标组件 %s 缺少数据集版本绑定", candidate.component.ID))
			}
			continue
		}
		mapping, exists := mappings[datasetVersionID]
		if !exists {
			return Resolution{}, issue(configPath+".parameterId", fmt.Sprintf("语义字段 %s 在数据集 %s 中没有映射", semanticFieldCode, datasetVersionID))
		}
		targets = append(targets, Target{
			ComponentID: candidate.component.ID, DatasetVersionID: datasetVersionID,
			FieldID: mapping.FieldID, DatasetParameterCode: mapping.DatasetParameterCode,
			SemanticFieldCode: semanticFieldCode, Operator: operator,
		})
	}
	if len(targets) == 0 {
		return Resolution{}, issue(configPath+".effectScope", "联动作用域内没有可绑定的数据组件")
	}

	parameters := cloneParameters(currentParameters)
	parameters[parameter.Code] = cloneValue(interaction.Value)
	return Resolution{
		ParameterID: parameter.ID, ParameterCode: parameter.Code,
		Parameters: parameters, Targets: targets,
	}, nil
}

func collectComponents(document reportjson.Document) ([]componentLocation, map[string]componentLocation, error) {
	locations := make([]componentLocation, 0)
	byID := make(map[string]componentLocation)
	for pageIndex, page := range document.Pages {
		for blockIndex, block := range page.Blocks {
			for componentIndex, component := range block.Components {
				path := fmt.Sprintf("pages[%d].blocks[%d].components[%d]", pageIndex, blockIndex, componentIndex)
				if _, exists := byID[component.ID]; exists {
					return nil, nil, issue(path+".id", fmt.Sprintf("组件标识 %s 重复", component.ID))
				}
				location := componentLocation{pageID: page.ID, blockID: block.ID, path: path, component: component}
				locations = append(locations, location)
				byID[component.ID] = location
			}
		}
	}
	return locations, byID, nil
}

func interactionConfig(source componentLocation, kind InteractionKind) (map[string]any, string, error) {
	switch kind {
	case FilterChange:
		if source.component.Type != "FILTER" {
			return nil, "", issue(source.path+".type", "筛选变更只能由 FILTER 组件触发")
		}
		return source.component.Binding, source.path + ".binding", nil
	case ChartClick:
		if source.component.Type != "CHART" {
			return nil, "", issue(source.path+".type", "图表点击只能由 CHART 组件触发")
		}
		if enabled, ok := source.component.Interaction["clickFilter"].(bool); !ok || !enabled {
			return nil, "", issue(source.path+".interaction.clickFilter", "图表未启用点击联动")
		}
		linkage, ok := object(source.component.Interaction["linkage"])
		if !ok {
			return nil, "", issue(source.path+".interaction.linkage", "图表缺少联动配置")
		}
		return linkage, source.path + ".interaction.linkage", nil
	default:
		return nil, "", issue(source.path, "不支持的交互类型")
	}
}

func findParameter(parameters []reportjson.Parameter, parameterID string) (reportjson.Parameter, error) {
	var result reportjson.Parameter
	found := false
	seen := make(map[string]bool, len(parameters))
	seenCodes := make(map[string]bool, len(parameters))
	for _, parameter := range parameters {
		if seen[parameter.ID] {
			return reportjson.Parameter{}, fmt.Errorf("报告参数标识 %s 重复", parameter.ID)
		}
		seen[parameter.ID] = true
		if seenCodes[parameter.Code] {
			return reportjson.Parameter{}, fmt.Errorf("报告参数编码 %s 重复", parameter.Code)
		}
		seenCodes[parameter.Code] = true
		if parameter.ID == parameterID {
			result, found = parameter, true
		}
	}
	if !found {
		return reportjson.Parameter{}, fmt.Errorf("引用的报告参数 %s 不存在", parameterID)
	}
	if strings.TrimSpace(result.Code) == "" {
		return reportjson.Parameter{}, fmt.Errorf("报告参数 %s 缺少运行编码", parameterID)
	}
	return result, nil
}

func indexMappings(parameter reportjson.Parameter, path string) (map[string]reportjson.DatasetFieldBinding, string, error) {
	if parameter.SemanticBinding == nil {
		return nil, "", issue(path, fmt.Sprintf("参数 %s 缺少语义字段映射", parameter.Code))
	}
	semanticFieldCode := strings.TrimSpace(parameter.SemanticBinding.SemanticFieldCode)
	if semanticFieldCode == "" {
		return nil, "", issue(path, fmt.Sprintf("参数 %s 缺少语义字段编码", parameter.Code))
	}
	if len(parameter.SemanticBinding.DatasetFields) == 0 {
		return nil, "", issue(path, fmt.Sprintf("参数 %s 没有数据集字段映射", parameter.Code))
	}
	mappings := make(map[string]reportjson.DatasetFieldBinding, len(parameter.SemanticBinding.DatasetFields))
	for index, mapping := range parameter.SemanticBinding.DatasetFields {
		mappingPath := fmt.Sprintf("parameters[%s].semanticBinding.datasetFields[%d]", parameter.ID, index)
		if strings.TrimSpace(mapping.DatasetVersionID) == "" || strings.TrimSpace(mapping.FieldID) == "" || strings.TrimSpace(mapping.DatasetParameterCode) == "" {
			return nil, "", issue(mappingPath, "语义字段映射不完整")
		}
		if _, exists := mappings[mapping.DatasetVersionID]; exists {
			return nil, "", issue(mappingPath+".datasetVersionId", fmt.Sprintf("语义字段 %s 在数据集 %s 中存在重复映射", semanticFieldCode, mapping.DatasetVersionID))
		}
		mappings[mapping.DatasetVersionID] = mapping
	}
	return mappings, semanticFieldCode, nil
}

func parseEffectScope(value any, path string) (effectScope, error) {
	record, ok := object(value)
	if !ok {
		return effectScope{}, issue(path, "联动影响范围必须为对象")
	}
	kind, ok := stringProperty(record, "kind")
	if !ok || (kind != "REPORT" && kind != "PAGE" && kind != "BLOCK" && kind != "COMPONENTS") {
		return effectScope{}, issue(path+".kind", "联动影响范围无效")
	}
	if kind != "COMPONENTS" {
		if _, exists := record["componentIds"]; exists {
			return effectScope{}, issue(path+".componentIds", "当前作用域不能携带指定组件")
		}
		return effectScope{kind: kind}, nil
	}
	ids, ok := stringList(record["componentIds"])
	if !ok || len(ids) == 0 {
		return effectScope{}, issue(path+".componentIds", "指定组件作用域至少需要一个目标组件")
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return effectScope{}, issue(path+".componentIds", "目标组件标识不能为空")
		}
		if seen[id] {
			return effectScope{}, issue(path+".componentIds", fmt.Sprintf("目标组件 %s 重复", id))
		}
		seen[id] = true
	}
	return effectScope{kind: kind, componentIDs: ids}, nil
}

func scopedComponents(all []componentLocation, byID map[string]componentLocation, source componentLocation, scope effectScope, path string) ([]componentLocation, error) {
	switch scope.kind {
	case "REPORT":
		return append([]componentLocation(nil), all...), nil
	case "PAGE":
		return filterLocations(all, func(item componentLocation) bool { return item.pageID == source.pageID }), nil
	case "BLOCK":
		return filterLocations(all, func(item componentLocation) bool {
			return item.pageID == source.pageID && item.blockID == source.blockID
		}), nil
	case "COMPONENTS":
		result := make([]componentLocation, 0, len(scope.componentIDs))
		for index, id := range scope.componentIDs {
			location, exists := byID[id]
			if !exists {
				return nil, issue(fmt.Sprintf("%s.componentIds[%d]", path, index), fmt.Sprintf("指定的目标组件 %s 不存在", id))
			}
			result = append(result, location)
		}
		return result, nil
	default:
		return nil, issue(path+".kind", "联动影响范围无效")
	}
}

func componentDatasetVersion(location componentLocation) (string, bool, error) {
	value, exists := location.component.Binding["datasetVersionId"]
	if !exists {
		return "", false, nil
	}
	datasetVersionID, ok := value.(string)
	if !ok {
		return "", false, issue(location.path+".binding.datasetVersionId", "数据集版本标识必须为字符串")
	}
	if strings.TrimSpace(datasetVersionID) == "" {
		return "", false, nil
	}
	return datasetVersionID, true, nil
}

func validateChartDimension(source componentLocation, clickedFieldID string, mappings map[string]reportjson.DatasetFieldBinding, path string) error {
	if strings.TrimSpace(clickedFieldID) == "" {
		return issue(path+".dimensionFieldId", "图表点击必须声明维度字段")
	}
	datasetVersionID, configured, err := componentDatasetVersion(source)
	if err != nil {
		return err
	}
	if !configured {
		return issue(source.path+".binding.datasetVersionId", "图表缺少数据集版本绑定")
	}
	mapping, exists := mappings[datasetVersionID]
	if !exists {
		return issue(path+".parameterId", fmt.Sprintf("图表数据集 %s 缺少语义字段映射", datasetVersionID))
	}
	if mapping.FieldID != clickedFieldID {
		return issue(path+".dimensionFieldId", "点击维度与报告参数的语义字段映射不一致")
	}
	dimensions, ok := objectList(source.component.Binding["dimensions"])
	if !ok {
		return issue(source.path+".binding.dimensions", "图表维度配置无效")
	}
	for _, dimension := range dimensions {
		if fieldID, _ := stringProperty(dimension, "fieldId"); fieldID == clickedFieldID {
			return nil
		}
	}
	return issue(path+".dimensionFieldId", "图表点击字段不是已声明维度")
}

func filterLocations(values []componentLocation, keep func(componentLocation) bool) []componentLocation {
	result := make([]componentLocation, 0, len(values))
	for _, value := range values {
		if keep(value) {
			result = append(result, value)
		}
	}
	return result
}

func allowedOperator(value string) bool {
	return value == "EQUALS" || value == "IN" || value == "BETWEEN" || value == "GTE" || value == "LTE"
}

// validateOperatorValue 只校验操作符与值基数，完整类型、时区和系统默认值转换由在线运行服务负责。
func validateOperatorValue(operator string, parameter reportjson.Parameter, value any) string {
	length, collection := collectionLength(value)
	dateRange := parameter.DataType == "DATE_RANGE"
	switch operator {
	case "IN":
		if !parameter.MultiValue {
			return "IN 操作符只允许多值参数"
		}
		if !collection {
			return "IN 操作符要求多值数组"
		}
	case "BETWEEN":
		if (!parameter.MultiValue && !dateRange) || !collection || length != 2 {
			return "BETWEEN 操作符要求恰好两个参数值"
		}
	case "EQUALS", "GTE", "LTE":
		if parameter.MultiValue || dateRange || collection {
			return fmt.Sprintf("%s 操作符只允许单值参数", operator)
		}
	}
	return ""
}

func collectionLength(value any) (int, bool) {
	if value == nil {
		return 0, false
	}
	kind := reflect.TypeOf(value).Kind()
	if kind != reflect.Array && kind != reflect.Slice {
		return 0, false
	}
	return reflect.ValueOf(value).Len(), true
}

func issue(path, reason string) error { return &ResolveError{Path: path, Reason: reason} }

func object(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}

func stringProperty(record map[string]any, key string) (string, bool) {
	value, ok := record[key].(string)
	return value, ok
}

func stringList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []any:
		result := make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result[index] = text
		}
		return result, true
	default:
		return nil, false
	}
}

func objectList(value any) ([]map[string]any, bool) {
	switch typed := value.(type) {
	case []map[string]any:
		return append([]map[string]any(nil), typed...), true
	case []any:
		result := make([]map[string]any, len(typed))
		for index, item := range typed {
			record, ok := object(item)
			if !ok {
				return nil, false
			}
			result[index] = record
		}
		return result, true
	default:
		return nil, false
	}
}

func cloneParameters(values map[string]any) map[string]any {
	result := make(map[string]any, len(values)+1)
	for key, value := range values {
		result[key] = cloneValue(value)
	}
	return result
}

// cloneValue 复制常见 JSON 容器，避免返回结果与调用方输入共享可变切片或对象。
func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
