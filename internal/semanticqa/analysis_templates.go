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
			Domain:      sourceDocument.Dataset.Domain,
			Subject:     sourceDocument.Dataset.Subject,
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

// buildMultiFactDWSCandidate 把同一主题中的多张事实表分别聚合到共同月份后再
// 关联。DIM 只参与上游 LLM 对实体与字段语义的判断，不会违反 DWS <- DWD 的
// 物理分层合同。
func buildMultiFactDWSCandidate(
	sources []dwsPlanningAsset,
	scope dwsModelingScope,
	templateCode string,
) (dataset.Prepared, error) {
	if len(sources) < 2 ||
		strings.ToUpper(strings.TrimSpace(templateCode)) != "MULTI_FACT_COMPARISON" {
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
				JoinType: "LEFT", Cardinality: "ONE_TO_ONE",
				FanoutPolicy: "SAFE", ManualConfirmed: true,
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
			if !exists || field.Role != "MEASURE" ||
				contract.Additivity == "NON_ADDITIVE" {
				continue
			}
			aggregation := "SUM"
			if contract.Additivity == "SEMI_ADDITIVE" {
				aggregation = "MAX"
			}
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
				Unit: contract.Unit, Currency: contract.Currency,
			})
			added++
			if added == 3 {
				break
			}
		}
		if len(preAggregation.Metrics) == 0 {
			return dataset.Prepared{}, ErrUnprovenPath
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
			// DATASET 节点不暴露底层物理数据源；DSL 的 CROSS_SOURCE 合同只适用
			// 于直接引用多个 TABLE 数据源的场景。多事实由 AnalysisContract
			// 的 MULTI_FACT 输入模式表达。
			Type: "SINGLE_SOURCE", Layer: dataset.LayerDWS,
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
		fieldsByCode := map[string]dataset.Field{}
		for _, field := range document.Fields {
			fieldsByCode[field.Code] = field
		}
		hasMeasure := false
		for _, contract := range document.FactContract.AtomicMeasures {
			field, exists := fieldsByCode[contract.Field]
			if exists && field.Role == "MEASURE" &&
				contract.Additivity != "NON_ADDITIVE" {
				hasMeasure = true
				break
			}
		}
		if hasMeasure {
			eligible = append(eligible, source)
		}
	}
	return eligible
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
	if len(value) <= 120 {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:111] + "_" + hex.EncodeToString(sum[:])[:8]
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
