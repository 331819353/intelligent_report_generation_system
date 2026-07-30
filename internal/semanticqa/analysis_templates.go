package semanticqa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

const dwsRecordCountMetricCode = "record_count"

type AnalysisTemplate struct {
	Code                  string   `json:"code"`
	Name                  string   `json:"name"`
	Intent                string   `json:"intent"`
	RequiredFactCount     string   `json:"requiredFactCount"`
	RequiresTime          bool     `json:"requiresTime"`
	RequiresDimension     bool     `json:"requiresDimension"`
	AutoEligible          bool     `json:"autoEligible"`
	OutputGrainRule       string   `json:"outputGrainRule"`
	SafetyRules           []string `json:"safetyRules"`
	NotApplicableWhen     []string `json:"notApplicableWhen"`
	MaterializationPolicy string   `json:"materializationPolicy"`
}

func MarketAnalysisTemplates() []AnalysisTemplate {
	commonSafety := []string{
		"只引用精确 PUBLISHED DWD 版本",
		"度量必须声明可加性、单位、币种和 NULL 策略",
		"多事实必须分别预聚合到共同粒度后再 Join",
		"逻辑候选只生成草稿，不自动发布或激活物化",
	}
	return []AnalysisTemplate{
		{
			Code: "TREND", Name: "趋势", Intent: "TREND",
			RequiredFactCount: "1", RequiresTime: true, AutoEligible: true,
			OutputGrainRule:       "时间粒度",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"缺少事件时间", "没有可安全聚合的度量"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "PERIOD_COMPARISON", Name: "同比/环比", Intent: "PERIOD_COMPARISON",
			RequiredFactCount: "1", RequiresTime: true, AutoEligible: true,
			OutputGrainRule:       "时间粒度",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"时间序列不连续", "缺少可比较时间窗"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "DISTRIBUTION", Name: "分布", Intent: "DISTRIBUTION",
			RequiredFactCount: "1", RequiresDimension: true, AutoEligible: true,
			OutputGrainRule:       "选定一致性维度",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"没有低/中基数维度", "维度为敏感或禁止索引"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "RANKING", Name: "排名与 Top N", Intent: "RANKING",
			RequiredFactCount: "1", RequiresDimension: true, AutoEligible: true,
			OutputGrainRule:       "排名维度",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"没有可排序度量", "维度值访问策略不允许"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "DRILLDOWN", Name: "多维下钻", Intent: "DRILLDOWN",
			RequiredFactCount: "1", RequiresDimension: true, AutoEligible: true,
			OutputGrainRule:       "最多三个一致性维度与可选时间",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"维度路径未 VERIFIED", "组合粒度超过租户阈值"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "FUNNEL", Name: "漏斗与转化", Intent: "FUNNEL",
			RequiredFactCount: "1..N", RequiresTime: true,
			OutputGrainRule:       "主体、窗口与有序阶段",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"缺少主体键", "阶段顺序或窗口未定义"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "RETENTION", Name: "队列、留存与复购", Intent: "RETENTION",
			RequiredFactCount: "1..N", RequiresTime: true,
			OutputGrainRule:       "队列期、观察期与主体",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"缺少稳定主体键", "观察窗口未定义"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "LIFECYCLE", Name: "生命周期与状态迁移", Intent: "LIFECYCLE",
			RequiredFactCount: "1..N", RequiresTime: true,
			OutputGrainRule:       "主体、状态与事件时间",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"状态字典不完整", "事件顺序不可证明"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "ANOMALY", Name: "异常检测", Intent: "ANOMALY",
			RequiredFactCount: "1", RequiresTime: true, AutoEligible: true,
			OutputGrainRule:       "时间粒度与可选维度",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"历史窗口不足", "度量不可比较"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "CONTRIBUTION", Name: "差异与贡献度", Intent: "CONTRIBUTION",
			RequiredFactCount: "1..N", RequiresDimension: true,
			OutputGrainRule:       "比较窗口与贡献维度",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"比较基线缺失", "单位或币种不一致"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "MULTI_FACT_COMPARISON", Name: "多事实对比", Intent: "MULTI_FACT_COMPARISON",
			RequiredFactCount: "2..N", RequiresTime: true,
			OutputGrainRule:       "各事实预聚合后的共同时间与一致性维度",
			SafetyRules:           append([]string(nil), commonSafety...),
			NotApplicableWhen:     []string{"共同粒度不可证明", "事实单位、币种或时区不兼容"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
		{
			Code: "ENTITY_COUNT", Name: "实体数量", Intent: "ENTITY_COUNT",
			RequiredFactCount: "0", RequiresDimension: true,
			OutputGrainRule: "实体说明属性或全量范围",
			SafetyRules: append([]string(nil), []string{
				"只引用一张精确 PUBLISHED DIM 版本",
				"只生成一个 COUNT 或 COUNT_DISTINCT 指标",
				"不把 DIM 属性改造成交易事实或金额指标",
				"逻辑候选只生成草稿，不自动发布或激活物化",
			}...),
			NotApplicableWhen:     []string{"DIM 缺少受治理实体键"},
			MaterializationPolicy: "SUGGESTION_ONLY",
		},
	}
}

func autoEligibleTemplateCodes(document dataset.Document) []string {
	if document.Dataset.Layer != dataset.LayerDWD || document.FactContract == nil {
		return nil
	}
	if !hasSafeAtomicMeasure(document) && !supportsSafeRecordCount(document) {
		return nil
	}
	hasTime := effectiveFactTimeField(document) != ""
	hasDimension := false
	for _, field := range document.Fields {
		hasDimension = hasDimension || field.Role == "DIMENSION"
	}
	result := []string{}
	for _, template := range MarketAnalysisTemplates() {
		if !template.AutoEligible ||
			(template.RequiresTime && !hasTime) ||
			(template.RequiresDimension && !hasDimension) {
			continue
		}
		result = append(result, template.Code)
	}
	return result
}

func buildSingleFactDWSCandidate(
	source dataset.Record,
	sourceVersionID, templateCode string,
) (dataset.Prepared, error) {
	return buildSingleFactDWSCandidateWithSelection(
		source, sourceVersionID, templateCode, nil, nil, "STANDARD", nil,
	)
}

func buildSingleFactDWSCandidateWithSelection(
	source dataset.Record,
	sourceVersionID, templateCode string,
	selectedDimensionCodes, selectedMetricCodes []string,
	groupingMode string,
	selectedGroupingSets [][]string,
) (dataset.Prepared, error) {
	sourceDocument, err := dataset.DecodeAndNormalize(source.DSL)
	if err != nil || sourceDocument.Dataset.Layer != dataset.LayerDWD ||
		sourceDocument.FactContract == nil {
		return dataset.Prepared{}, ErrInvalidRequest
	}
	templateCode = strings.ToUpper(strings.TrimSpace(templateCode))
	var template *AnalysisTemplate
	for _, candidate := range MarketAnalysisTemplates() {
		if candidate.Code == templateCode && candidate.AutoEligible {
			copy := candidate
			template = &copy
			break
		}
	}
	if template == nil {
		return dataset.Prepared{}, ErrInvalidRequest
	}
	fieldsByCode := map[string]dataset.Field{}
	projection := make([]string, 0, len(sourceDocument.Fields))
	dimensions := []dataset.Field{}
	for _, field := range sourceDocument.Fields {
		fieldsByCode[field.Code] = field
		projection = append(projection, field.Code)
		if field.Role == "DIMENSION" {
			dimensions = append(dimensions, field)
		}
	}
	timeField, hasTime := fieldsByCode[effectiveFactTimeField(sourceDocument)]
	if template.RequiresTime &&
		(!hasTime || !isTemporalCanonicalType(timeField.CanonicalType)) {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	if template.RequiresDimension && len(dimensions) == 0 {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	selectedMetricSet := map[string]bool{}
	for _, code := range selectedMetricCodes {
		selectedMetricSet[strings.ToLower(strings.TrimSpace(code))] = true
	}
	requiresNativeTime := false
	for _, measure := range sourceDocument.FactContract.AtomicMeasures {
		if len(selectedMetricSet) > 0 &&
			!selectedMetricSet[strings.ToLower(measure.Field)] {
			continue
		}
		sourceField, exists := fieldsByCode[measure.Field]
		if !exists {
			continue
		}
		measure = effectiveDWSAtomicMeasure(measure, sourceField)
		if measure.Additivity == "SEMI_ADDITIVE" ||
			measure.ValueBehavior == "CUMULATIVE" ||
			measure.ValueBehavior == "POINT_IN_TIME" {
			requiresNativeTime = true
			break
		}
	}
	if requiresNativeTime &&
		(!hasTime || !isTemporalCanonicalType(timeField.CanonicalType)) {
		return dataset.Prepared{}, ErrUnprovenPath
	}

	selectedDimensions := []dataset.Field{}
	selectedDimensionSet := map[string]bool{}
	for _, code := range selectedDimensionCodes {
		selectedDimensionSet[strings.ToLower(strings.TrimSpace(code))] = true
	}
	if len(selectedDimensionSet) > 0 {
		for _, dimension := range dimensions {
			if selectedDimensionSet[strings.ToLower(dimension.Code)] {
				selectedDimensions = append(selectedDimensions, dimension)
			}
			if len(selectedDimensions) == 3 {
				break
			}
		}
	}
	if len(selectedDimensions) == 0 {
		switch template.Code {
		case "DISTRIBUTION", "RANKING":
			selectedDimensions = append(selectedDimensions, dimensions[0])
		case "DRILLDOWN":
			limit := min(3, len(dimensions))
			selectedDimensions = append(selectedDimensions, dimensions[:limit]...)
		}
	}
	includeTime := template.RequiresTime || requiresNativeTime
	outputFields := []dataset.Field{}
	groupBy := []string{}
	grainFields := []string{}
	conformedDimensions := []string{}
	timeFieldID := ""
	timeOutputCode := ""
	timeGrain := ""
	if includeTime {
		output := timeField
		output.ID = "field_stat_month"
		output.Code = "stat_month"
		output.Name = "统计月份"
		output.Role = "TIME"
		output.Expression = dataset.Expression{
			Type: "DATE_TRUNC", Unit: "MONTH",
			Argument: &dataset.Expression{
				Type: "FIELD_REF", NodeID: "fact", Field: timeField.Code,
			},
		}
		timeGrain = "MONTH"
		if requiresNativeTime {
			output.ID = "field_stat_date"
			output.Code = "stat_date"
			output.Name = "统计日期"
			output.Expression = dataset.Expression{
				Type: "DATE_TRUNC", Unit: "DAY",
				Argument: &dataset.Expression{
					Type: "FIELD_REF", NodeID: "fact", Field: timeField.Code,
				},
			}
			timeGrain = "DAY"
		}
		output.CanonicalType = "DATE"
		outputFields = append(outputFields, output)
		groupBy = append(groupBy, output.ID)
		grainFields = append(grainFields, output.Code)
		conformedDimensions = append(conformedDimensions, output.Code)
		timeFieldID = output.ID
		timeOutputCode = output.Code
	}
	for _, selected := range selectedDimensions {
		output := selected
		output.ID = "field_" + selected.Code
		output.Expression = dataset.Expression{
			Type: "FIELD_REF", NodeID: "fact", Field: selected.Code,
		}
		outputFields = append(outputFields, output)
		groupBy = append(groupBy, output.ID)
		grainFields = append(grainFields, output.Code)
		conformedDimensions = append(conformedDimensions, output.Code)
	}
	analysisMeasures := []dataset.AnalysisMeasureContract{}
	for _, measure := range sourceDocument.FactContract.AtomicMeasures {
		if len(selectedMetricSet) > 0 &&
			!selectedMetricSet[strings.ToLower(measure.Field)] {
			continue
		}
		sourceField, exists := fieldsByCode[measure.Field]
		if !exists || sourceField.Role != "MEASURE" {
			continue
		}
		measure = effectiveDWSAtomicMeasure(measure, sourceField)
		if measure.Additivity == "NON_ADDITIVE" {
			continue
		}
		aggregation := strings.ToUpper(strings.TrimSpace(
			measure.DefaultAggregation,
		))
		if aggregation != "SUM" && aggregation != "MIN" &&
			aggregation != "MAX" && aggregation != "AVG" {
			aggregation = "SUM"
		}
		output := sourceField
		output.ID = "field_" + sourceField.Code
		output.Expression = dataset.Expression{
			Type: "AGGREGATE", Function: aggregation,
			Argument: &dataset.Expression{
				Type: "FIELD_REF", NodeID: "fact", Field: sourceField.Code,
			},
		}
		outputFields = append(outputFields, output)
		analysisMeasures = append(analysisMeasures, dataset.AnalysisMeasureContract{
			Field: sourceField.Code, SourceNodeIDs: []string{"fact"},
			Aggregation: aggregation, Additivity: measure.Additivity,
			ValueBehavior:   measure.ValueBehavior,
			TimeAggregation: measure.TimeAggregation,
			Unit:            measure.Unit, Currency: measure.Currency,
		})
	}
	if supportsSafeRecordCount(sourceDocument) &&
		(selectedMetricSet[dwsRecordCountMetricCode] || len(analysisMeasures) == 0) {
		visible := true
		outputFields = append(outputFields, dataset.Field{
			ID:   "field_" + dwsRecordCountMetricCode,
			Code: dwsRecordCountMetricCode, Name: "事实记录数",
			Description: "按当前 DWS 输出粒度统计的原子事实记录数",
			Role:        "MEASURE", CanonicalType: "INTEGER",
			Nullable: false, Visible: &visible,
			Expression: dataset.Expression{
				Type: "AGGREGATE", Function: "COUNT",
			},
		})
		analysisMeasures = append(analysisMeasures, dataset.AnalysisMeasureContract{
			Field: dwsRecordCountMetricCode, SourceNodeIDs: []string{"fact"},
			Aggregation: "COUNT", Additivity: "ADDITIVE",
			ValueBehavior: "FLOW", TimeAggregation: "SUM",
		})
	}
	if len(analysisMeasures) == 0 || len(grainFields) == 0 {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	sorts := []dataset.Sort{}
	if template.Code == "RANKING" {
		sorts = append(sorts, dataset.Sort{
			FieldID: outputFields[len(outputFields)-1].ID, Direction: "DESC",
		})
	} else if includeTime {
		sorts = append(sorts, dataset.Sort{
			FieldID: timeFieldID, Direction: "ASC",
		})
	}
	resolvedGroupingMode, groupingSets := buildDWSGroupingPlan(
		groupingMode, selectedGroupingSets, selectedDimensions,
		timeFieldID, requiresNativeTime,
	)
	code := generatedDWSCode(template.Code, source.Code)
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: code, Name: template.Name + " · " + source.Name,
			Description: "基于精确 DWD 版本自动生成的可评审市场分析草稿",
			Domain:      sourceDocument.Dataset.Domain,
			Subject:     sourceDocument.Dataset.Subject,
			Type:        sourceDocument.Dataset.Type, Layer: dataset.LayerDWS,
			SemanticContractVersion: "1.0",
		},
		Nodes: []dataset.Node{{
			ID: "fact", Type: "DATASET", DatasetVersionID: sourceVersionID,
			Alias: "fact", Projection: projection, SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{},
		AnalysisContract: &dataset.AnalysisContract{
			Intent: template.Intent, InputMode: "SINGLE_FACT",
			CommonGrainFields:   grainFields,
			ConformedDimensions: conformedDimensions,
			Measures:            analysisMeasures,
		},
		Fields: outputFields, Filters: []dataset.Filter{}, GroupBy: groupBy,
		GroupByMode: resolvedGroupingMode, GroupingSets: groupingSets,
		Having: []dataset.Filter{}, Sorts: sorts, Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表 " + strings.Join(grainFields, " + "),
			KeyFields:   grainFields,
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
	if includeTime {
		document.AnalysisContract.TimeField = timeOutputCode
		document.AnalysisContract.TimeGrain = timeGrain
		document.OutputGrain.TimeField = timeOutputCode
		document.OutputGrain.DefaultTimeGrain = timeGrain
	}
	raw, err := jsonMarshal(document)
	if err != nil {
		return dataset.Prepared{}, err
	}
	return dataset.Prepare(raw)
}

func buildDWSGroupingPlan(
	requested string,
	selectedSets [][]string,
	dimensions []dataset.Field,
	timeFieldID string,
	timeMustBeRetained bool,
) (dataset.GroupByMode, [][]string) {
	_, _, _, _, _ = requested, selectedSets, dimensions, timeFieldID, timeMustBeRetained
	return dataset.GroupByModeStandard, nil
}

func buildDimensionCountDWSCandidate(
	source dataset.Record,
	sourceVersionID string,
	selectedDimensionCodes []string,
) (dataset.Prepared, error) {
	sourceDocument, err := dataset.DecodeAndNormalize(source.DSL)
	if err != nil || sourceDocument.Dataset.Layer != dataset.LayerDIM ||
		len(sourceDocument.OutputGrain.KeyFields) == 0 {
		return dataset.Prepared{}, ErrInvalidRequest
	}
	fieldsByCode := make(map[string]dataset.Field, len(sourceDocument.Fields))
	projection := make([]string, 0, len(sourceDocument.Fields))
	keySet := make(map[string]bool, len(sourceDocument.OutputGrain.KeyFields))
	for _, code := range sourceDocument.OutputGrain.KeyFields {
		keySet[strings.ToLower(strings.TrimSpace(code))] = true
	}
	for _, field := range sourceDocument.Fields {
		fieldsByCode[strings.ToLower(field.Code)] = field
		projection = append(projection, field.Code)
	}
	countField, exists := fieldsByCode[strings.ToLower(
		sourceDocument.OutputGrain.KeyFields[0],
	)]
	if !exists {
		return dataset.Prepared{}, ErrUnprovenPath
	}

	selected := make([]dataset.Field, 0, 3)
	seen := map[string]bool{}
	for _, code := range selectedDimensionCodes {
		field, found := fieldsByCode[strings.ToLower(strings.TrimSpace(code))]
		if !found || keySet[strings.ToLower(field.Code)] ||
			field.Role == "MEASURE" || field.Role == "TIME" ||
			seen[strings.ToLower(field.Code)] {
			continue
		}
		seen[strings.ToLower(field.Code)] = true
		selected = append(selected, field)
		if len(selected) == 3 {
			break
		}
	}
	if len(selected) == 0 {
		for _, field := range sourceDocument.Fields {
			if keySet[strings.ToLower(field.Code)] || field.Role == "MEASURE" ||
				field.Role == "TIME" {
				continue
			}
			selected = append(selected, field)
			if len(selected) == 3 {
				break
			}
		}
	}

	outputFields := make([]dataset.Field, 0, len(selected)+2)
	groupBy := make([]string, 0, max(1, len(selected)))
	grainFields := make([]string, 0, max(1, len(selected)))
	conformedDimensions := make([]string, 0, max(1, len(selected)))
	if len(selected) == 0 {
		visible := true
		outputFields = append(outputFields, dataset.Field{
			ID: "field_count_scope", Code: "count_scope", Name: "统计范围",
			Description: "实体数量统计的全量范围", Role: "DIMENSION",
			CanonicalType: "STRING", Nullable: false, Visible: &visible,
			Expression: dataset.Expression{
				Type: "LITERAL", Value: "ALL",
			},
		})
		groupBy = append(groupBy, "field_count_scope")
		grainFields = append(grainFields, "count_scope")
		conformedDimensions = append(conformedDimensions, "count_scope")
	} else {
		for _, field := range selected {
			output := field
			output.ID = "field_" + safeDWSIdentifier(field.Code, "dimension")
			output.Role = "DIMENSION"
			output.Expression = dataset.Expression{
				Type: "FIELD_REF", NodeID: "dimension", Field: field.Code,
			}
			outputFields = append(outputFields, output)
			groupBy = append(groupBy, output.ID)
			grainFields = append(grainFields, output.Code)
			conformedDimensions = append(conformedDimensions, output.Code)
		}
	}
	countArgument := dataset.Expression{
		Type: "FIELD_REF", NodeID: "dimension", Field: countField.Code,
	}
	visible := true
	outputFields = append(outputFields, dataset.Field{
		ID: "field_entity_count", Code: "entity_count", Name: "实体数量",
		Description: "按 DIM 实体键统计的实体数量", Role: "MEASURE",
		CanonicalType: "INTEGER", Nullable: false, Visible: &visible,
		Expression: dataset.Expression{
			Type: "AGGREGATE", Function: "COUNT_DISTINCT",
			Argument: &countArgument,
		},
	})

	code := generatedDWSCode("ENTITY_COUNT", source.Code)
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: code, Name: "实体数量 · " + source.Name,
			Description: "基于精确 DIM 版本生成的无事实实体数量主题草稿",
			Domain:      sourceDocument.Dataset.Domain,
			Subject:     sourceDocument.Dataset.Subject,
			Type:        sourceDocument.Dataset.Type, Layer: dataset.LayerDWS,
			SemanticContractVersion: "1.0",
		},
		Nodes: []dataset.Node{{
			ID: "dimension", Type: "DATASET",
			DatasetVersionID: sourceVersionID,
			Alias:            "dimension", Projection: projection,
			SourceFilters: []dataset.SourceFilter{},
		}},
		Joins:      []dataset.Join{},
		Fields:     outputFields,
		Filters:    []dataset.Filter{},
		GroupBy:    groupBy,
		Having:     []dataset.Filter{},
		Sorts:      []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一个实体数量分析分组",
			KeyFields:   grainFields,
		},
		AnalysisContract: &dataset.AnalysisContract{
			Intent: "ENTITY_COUNT", InputMode: "SINGLE_FACT",
			CommonGrainFields:   grainFields,
			ConformedDimensions: conformedDimensions,
			Measures: []dataset.AnalysisMeasureContract{{
				Field: "entity_count", SourceNodeIDs: []string{"dimension"},
				Aggregation: "COUNT_DISTINCT", Additivity: "NON_ADDITIVE",
				ValueBehavior: "NON_ADDITIVE", TimeAggregation: "NONE",
			}},
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return dataset.Prepared{}, err
	}
	return dataset.Prepare(raw)
}

// buildMultiFactDWSCandidate 把同一主题中的多张事实表分别聚合到共同月份后再
// 关联。DIM 只参与上游 LLM 对实体与字段语义的判断，不会违反 DWS <- DWD 的
// 物理分层合同。
func buildMultiFactDWSCandidate(
	sources []dwsPlanningAsset,
	scope dwsModelingScope,
	templateCode string,
	datasetType string,
) (dataset.Prepared, error) {
	if len(sources) < 2 ||
		strings.ToUpper(strings.TrimSpace(templateCode)) != "MULTI_FACT_COMPARISON" ||
		(datasetType != "SINGLE_SOURCE" && datasetType != "CROSS_SOURCE") {
		return dataset.Prepared{}, ErrInvalidRequest
	}
	nodes := make([]dataset.Node, 0, len(sources))
	preAggregations := make([]dataset.PreAggregation, 0, len(sources))
	joins := make([]dataset.Join, 0, len(sources)-1)
	outputFields := []dataset.Field{{
		ID: "field_stat_month", Code: "stat_month", Name: "统计月份",
		Role: "TIME", CanonicalType: "DATE", Nullable: false,
		Expression: dataset.Expression{
			Type: "FIELD_REF", NodeID: "fact_1", Field: "stat_month",
		},
	}}
	measures := []dataset.AnalysisMeasureContract{}
	for index, source := range sources {
		document := source.Document
		if document.Dataset.Layer != dataset.LayerDWD || document.FactContract == nil {
			return dataset.Prepared{}, ErrUnprovenPath
		}
		fieldsByCode := map[string]dataset.Field{}
		projection := make([]string, 0, len(document.Fields))
		for _, field := range document.Fields {
			fieldsByCode[field.Code] = field
			projection = append(projection, field.Code)
		}
		timeFieldCode := effectiveFactTimeField(document)
		timeField, exists := fieldsByCode[timeFieldCode]
		if !exists || !isTemporalCanonicalType(timeField.CanonicalType) {
			return dataset.Prepared{}, ErrUnprovenPath
		}
		nodeID := fmt.Sprintf("fact_%d", index+1)
		nodes = append(nodes, dataset.Node{
			ID: nodeID, Type: "DATASET", DatasetVersionID: source.VersionID,
			Alias: nodeID, Projection: projection, SourceFilters: []dataset.SourceFilter{},
		})
		joinID, joinSide := "join_1", "LEFT"
		if index > 0 {
			joinID = fmt.Sprintf("join_%d", index)
			joinSide = "RIGHT"
			joins = append(joins, dataset.Join{
				ID: joinID, LeftNodeID: "fact_1", RightNodeID: nodeID,
				JoinType: "LEFT", Cardinality: "MANY_TO_ONE",
				ManualConfirmed: true,
				Conditions: []dataset.JoinCondition{{
					LeftExpression: dataset.Expression{
						Type: "FIELD_REF", NodeID: "fact_1", Field: "stat_month",
					},
					Operator: "EQUALS",
					RightExpression: dataset.Expression{
						Type: "FIELD_REF", NodeID: nodeID, Field: "stat_month",
					},
				}},
			})
		}
		timeExpression := dataset.Expression{
			Type: "FIELD_REF", NodeID: nodeID, Field: timeField.Code,
		}
		preAggregation := dataset.PreAggregation{
			ID: fmt.Sprintf("preagg_%d", index+1), NodeID: nodeID,
			JoinID: joinID, JoinSide: joinSide,
			GroupBy: []dataset.PreAggregationGroup{{
				Field: "stat_month", Unit: "MONTH", Expression: &timeExpression,
			}},
			Metrics: []dataset.PreAggregationMetric{},
		}
		sourcePrefix := groupedMeasurePrefix(source.Record.Code, index)
		added := 0
		for _, contract := range document.FactContract.AtomicMeasures {
			field, exists := fieldsByCode[contract.Field]
			if !exists || field.Role != "MEASURE" {
				continue
			}
			contract = effectiveDWSAtomicMeasure(contract, field)
			if contract.Additivity != "ADDITIVE" {
				continue
			}
			aggregation := "SUM"
			alias := boundedDWSFieldCode(sourcePrefix + "_" + field.Code)
			expression := dataset.Expression{
				Type: "FIELD_REF", NodeID: nodeID, Field: field.Code,
			}
			preAggregation.Metrics = append(preAggregation.Metrics,
				dataset.PreAggregationMetric{
					Field: alias, Function: aggregation, Expression: &expression,
				},
			)
			outputFields = append(outputFields, dataset.Field{
				ID: "field_" + alias, Code: alias,
				Name:        source.Record.Name + " · " + field.Name,
				Description: field.Description, Role: "MEASURE",
				CanonicalType: field.CanonicalType, SemanticType: field.SemanticType,
				Nullable: true, Unit: contract.Unit,
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: nodeID, Field: alias,
				},
			})
			measures = append(measures, dataset.AnalysisMeasureContract{
				Field: alias, SourceNodeIDs: []string{nodeID},
				Aggregation: aggregation, Additivity: contract.Additivity,
				ValueBehavior:   contract.ValueBehavior,
				TimeAggregation: contract.TimeAggregation,
				Unit:            contract.Unit, Currency: contract.Currency,
			})
			added++
			if added == 3 {
				break
			}
		}
		if len(preAggregation.Metrics) == 0 {
			if !supportsSafeRecordCount(document) {
				return dataset.Prepared{}, ErrUnprovenPath
			}
			alias := boundedDWSFieldCode(
				sourcePrefix + "_" + dwsRecordCountMetricCode,
			)
			preAggregation.Metrics = append(
				preAggregation.Metrics,
				dataset.PreAggregationMetric{
					Field: alias, Function: "COUNT", CountRows: true,
				},
			)
			outputFields = append(outputFields, dataset.Field{
				ID: "field_" + alias, Code: alias,
				Name:        source.Record.Name + " · 事实记录数",
				Description: "预聚合到共同时间粒度后的原子事实记录数",
				Role:        "MEASURE", CanonicalType: "INTEGER",
				Expression: dataset.Expression{
					Type: "FIELD_REF", NodeID: nodeID, Field: alias,
				},
			})
			measures = append(measures, dataset.AnalysisMeasureContract{
				Field: alias, SourceNodeIDs: []string{nodeID},
				Aggregation: "COUNT", Additivity: "ADDITIVE",
				ValueBehavior: "FLOW", TimeAggregation: "SUM",
			})
		}
		preAggregations = append(preAggregations, preAggregation)
	}
	if len(measures) < len(sources) {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	domainCode := safeDWSIdentifier(scope.DomainCode, "general")
	subjectCode := safeDWSIdentifier(groupedSubjectCode(scope.SubjectCode), "general")
	code := boundedDWSCode("dws_" + domainCode + "_" + subjectCode + "_multi_fact_summary")
	name := strings.TrimSpace(scope.SubjectName)
	if name == "" {
		name = "综合分析"
	}
	domain := strings.TrimSpace(scope.DomainCode)
	for _, source := range sources {
		if inherited := strings.TrimSpace(source.Document.Dataset.Domain); inherited != "" {
			domain = inherited
			break
		}
	}
	if domain == "" {
		domain = "general"
	}
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: code, Name: name + "主题汇总",
			Description: fmt.Sprintf(
				"基于 %d 张当前发布 DWD 的共同月份粒度生成的多事实主题汇总草稿",
				len(sources),
			),
			Domain: domain, Subject: subjectCode,
			Type: datasetType, Layer: dataset.LayerDWS,
			SemanticContractVersion: "1.0",
		},
		Nodes: nodes, Joins: joins, PreAggregations: preAggregations,
		AnalysisContract: &dataset.AnalysisContract{
			Intent: "MULTI_FACT_COMPARISON", InputMode: "MULTI_FACT",
			CommonGrainFields:   []string{"stat_month"},
			ConformedDimensions: []string{"stat_month"},
			TimeField:           "stat_month", TimeGrain: "MONTH", Measures: measures,
		},
		Fields: outputFields, Filters: []dataset.Filter{}, GroupBy: []string{},
		Having: []dataset.Filter{}, Sorts: []dataset.Sort{{
			FieldID: "field_stat_month", Direction: "ASC",
		}}, Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表一个统计月份",
			KeyFields:   []string{"stat_month"},
			TimeField:   "stat_month", DefaultTimeGrain: "MONTH",
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
	raw, err := jsonMarshal(document)
	if err != nil {
		return dataset.Prepared{}, err
	}
	return dataset.Prepare(raw)
}

func multiFactEligibleSources(sources []dwsPlanningAsset) []dwsPlanningAsset {
	eligible := make([]dwsPlanningAsset, 0, len(sources))
	for _, source := range sources {
		document := source.Document
		if document.Dataset.Layer != dataset.LayerDWD ||
			document.FactContract == nil ||
			effectiveFactTimeField(document) == "" {
			continue
		}
		if hasSafeAtomicMeasure(document) || supportsSafeRecordCount(document) {
			eligible = append(eligible, source)
		}
	}
	return eligible
}

func hasSafeAtomicMeasure(document dataset.Document) bool {
	if document.FactContract == nil {
		return false
	}
	fieldsByCode := map[string]dataset.Field{}
	for _, field := range document.Fields {
		fieldsByCode[field.Code] = field
	}
	for _, contract := range document.FactContract.AtomicMeasures {
		field, exists := fieldsByCode[contract.Field]
		if !exists || field.Role != "MEASURE" {
			continue
		}
		contract = effectiveDWSAtomicMeasure(contract, field)
		if contract.Additivity == "ADDITIVE" {
			return true
		}
	}
	return false
}

func supportsSafeRecordCount(document dataset.Document) bool {
	if document.Dataset.Layer != dataset.LayerDWD ||
		document.FactContract == nil ||
		len(document.FactContract.GrainKeyFields) == 0 {
		return false
	}
	fieldsByCode := map[string]bool{}
	for _, field := range document.Fields {
		fieldsByCode[field.Code] = true
	}
	for _, code := range document.FactContract.GrainKeyFields {
		if !fieldsByCode[code] {
			return false
		}
	}
	return true
}

func effectiveDWSAtomicMeasure(
	contract dataset.AtomicMeasureContract,
	field dataset.Field,
) dataset.AtomicMeasureContract {
	behavior := strings.ToUpper(strings.TrimSpace(contract.ValueBehavior))
	if inferred := inferredDWSMeasureValueBehavior(field); inferred != "" {
		behavior = inferred
	}
	if behavior == "" {
		switch contract.Additivity {
		case "SEMI_ADDITIVE":
			behavior = "POINT_IN_TIME"
		case "NON_ADDITIVE":
			behavior = "NON_ADDITIVE"
		default:
			behavior = "FLOW"
		}
	}
	contract.ValueBehavior = behavior
	switch behavior {
	case "CUMULATIVE", "POINT_IN_TIME":
		contract.Additivity = "SEMI_ADDITIVE"
		contract.DefaultAggregation = "SUM"
		contract.TimeAggregation = "LAST"
	case "NON_ADDITIVE":
		contract.Additivity = "NON_ADDITIVE"
		if contract.DefaultAggregation == "" {
			contract.DefaultAggregation = "AVG"
		}
		contract.TimeAggregation = "NONE"
	default:
		contract.ValueBehavior = "FLOW"
		contract.Additivity = "ADDITIVE"
		if contract.DefaultAggregation == "" {
			contract.DefaultAggregation = "SUM"
		}
		contract.TimeAggregation = "SUM"
	}
	return contract
}

func inferredDWSMeasureValueBehavior(field dataset.Field) string {
	value := strings.ToLower(strings.Join([]string{
		field.Code, field.Name, field.Description, field.SemanticType,
	}, " "))
	for _, marker := range []string{
		"累计", "累积", "截至", "本年累计", "本月累计",
		"cumulative", "running_total", "running total", "to_date",
		"ytd", "mtd", "qtd",
	} {
		if strings.Contains(value, marker) {
			return "CUMULATIVE"
		}
	}
	for _, marker := range []string{
		"余额", "库存", "存量", "时点", "期末", "期初", "结余",
		"在手", "保有", "未结", "balance", "inventory", "stock",
		"on_hand", "on hand", "outstanding", "closing", "ending",
		"as_of", "as of",
	} {
		if strings.Contains(value, marker) {
			return "POINT_IN_TIME"
		}
	}
	return ""
}

func effectiveFactTimeField(document dataset.Document) string {
	fieldsByCode := map[string]dataset.Field{}
	for _, field := range document.Fields {
		fieldsByCode[field.Code] = field
	}
	candidates := []string{}
	if document.FactContract != nil {
		candidates = append(candidates, document.FactContract.EventTimeField)
	}
	candidates = append(candidates, document.OutputGrain.TimeField)
	candidates = append(candidates, document.OutputGrain.KeyFields...)
	for _, code := range candidates {
		field, exists := fieldsByCode[code]
		if code != "" && exists && isTemporalCanonicalType(field.CanonicalType) {
			return code
		}
	}
	return ""
}

func isTemporalCanonicalType(value string) bool {
	return value == "DATE" || value == "DATETIME"
}

func groupedMeasurePrefix(code string, index int) string {
	value := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(code)), "dwd_")
	if value == "" {
		value = fmt.Sprintf("fact_%d", index+1)
	}
	return safeDWSIdentifier(value, fmt.Sprintf("fact_%d", index+1))
}

func groupedSubjectCode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "business":
		return "business_analysis"
	case "fulfillment":
		return "fulfillment_analysis"
	case "operations":
		return "operations_analysis"
	case "customer":
		return "customer_analysis"
	case "product":
		return "product_analysis"
	default:
		return value
	}
}

func safeDWSIdentifier(value, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	underscore := false
	for _, character := range value {
		valid := character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9'
		if valid {
			builder.WriteRune(character)
			underscore = false
		} else if builder.Len() > 0 && !underscore {
			builder.WriteByte('_')
			underscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return fallback
	}
	return result
}

func boundedDWSFieldCode(value string) string {
	// DWS outputs are materialized into PostgreSQL. Keep every generated
	// output/pre-aggregation identifier within PostgreSQL's 63-byte limit so
	// preview and publication never rely on PostgreSQL's silent truncation.
	if len(value) <= 63 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:54] + "_" + hex.EncodeToString(sum[:])[:8]
}

func boundedDWSCode(value string) string {
	if len(value) <= 63 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:54] + "_" + hex.EncodeToString(sum[:])[:8]
}

func generatedDWSCode(templateCode, sourceCode string) string {
	sourceCode = strings.ToLower(strings.TrimSpace(sourceCode))
	templateCode = dwsTemplatePhysicalCode(templateCode)
	value := ""
	if strings.HasPrefix(sourceCode, "dwd_") {
		value = "dws_" + strings.TrimPrefix(sourceCode, "dwd_") + "_" + templateCode
	} else {
		value = "dws_general_general_" + sourceCode + "_" + templateCode
	}
	if len(value) <= 63 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(sum[:])[:8]
	return value[:54] + "_" + suffix
}

func dwsTemplatePhysicalCode(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "TREND":
		return "trend"
	case "PERIOD_COMPARISON":
		return "period_cmp"
	case "DISTRIBUTION":
		return "dist"
	case "RANKING":
		return "rank"
	case "DRILLDOWN":
		return "drill"
	case "ANOMALY":
		return "anomaly"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func jsonMarshal(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal DWS candidate", ErrInvalidRequest)
	}
	return raw, nil
}
