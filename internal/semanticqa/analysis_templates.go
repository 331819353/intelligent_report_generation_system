package semanticqa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/dataset"
)

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
	}
}

func autoEligibleTemplateCodes(document dataset.Document) []string {
	if document.Dataset.Layer != dataset.LayerDWD || document.FactContract == nil {
		return nil
	}
	hasTime := document.FactContract.EventTimeField != ""
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
	timeField, hasTime := fieldsByCode[sourceDocument.FactContract.EventTimeField]
	if template.RequiresTime && (!hasTime || timeField.Role != "TIME") {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	if template.RequiresDimension && len(dimensions) == 0 {
		return dataset.Prepared{}, ErrUnprovenPath
	}

	selectedDimensions := []dataset.Field{}
	switch template.Code {
	case "DISTRIBUTION", "RANKING":
		selectedDimensions = append(selectedDimensions, dimensions[0])
	case "DRILLDOWN":
		limit := min(3, len(dimensions))
		selectedDimensions = append(selectedDimensions, dimensions[:limit]...)
	}
	includeTime := template.RequiresTime
	outputFields := []dataset.Field{}
	groupBy := []string{}
	grainFields := []string{}
	conformedDimensions := []string{}
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
		output.CanonicalType = "DATE"
		outputFields = append(outputFields, output)
		groupBy = append(groupBy, output.ID)
		grainFields = append(grainFields, output.Code)
		conformedDimensions = append(conformedDimensions, output.Code)
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
		sourceField, exists := fieldsByCode[measure.Field]
		if !exists || sourceField.Role != "MEASURE" {
			continue
		}
		aggregation := "SUM"
		if measure.Additivity == "SEMI_ADDITIVE" {
			aggregation = "MAX"
		}
		if measure.Additivity == "NON_ADDITIVE" {
			continue
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
			Unit: measure.Unit, Currency: measure.Currency,
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
			FieldID: "field_stat_month", Direction: "ASC",
		})
	}
	code := generatedDWSCode(template.Code, source.Code)
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: code, Name: template.Name + " · " + source.Name,
			Description: "基于精确 DWD 版本自动生成的可评审市场分析草稿",
			Type:        "SINGLE_SOURCE", Layer: dataset.LayerDWS,
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
		document.AnalysisContract.TimeField = "stat_month"
		document.AnalysisContract.TimeGrain = "MONTH"
		document.OutputGrain.TimeField = "stat_month"
		document.OutputGrain.DefaultTimeGrain = "MONTH"
	}
	raw, err := jsonMarshal(document)
	if err != nil {
		return dataset.Prepared{}, err
	}
	return dataset.Prepare(raw)
}

func generatedDWSCode(templateCode, sourceCode string) string {
	value := "auto_" + strings.ToLower(templateCode) + "_" + sourceCode
	if len(value) <= 128 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	suffix := hex.EncodeToString(sum[:])[:12]
	return value[:115] + "_" + suffix
}

func jsonMarshal(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal DWS candidate", ErrInvalidRequest)
	}
	return raw, nil
}
