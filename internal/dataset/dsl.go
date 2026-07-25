package dataset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

// physicalIdentifierPattern 与查询编译器的物理白名单保持一致。DSL 自身的
// code/id 仍使用更严格的 ASCII identifierPattern；projection 和 FIELD_REF
// 允许安全的 Unicode 字母/数字，从而无损表达 Excel 中文表头以及数据库中的
// Unicode 标识符。空格、引号、点号和操作符仍被拒绝。
var physicalIdentifierPattern = regexp.MustCompile(`^[\p{L}][\p{L}\p{N}_$#]{0,127}$`)

// MaxNodes 限制单个数据集的节点数量，避免校验与跨源预览出现无界扇出。
const MaxNodes = 16

// DecodeAndNormalize 严格解析、迁移并规范化 DSL，未知字段会被拒绝。
func DecodeAndNormalize(raw []byte) (Document, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Document{}, errors.New("DSL document is required")
	}
	// 先只读取版本号，再用目标结构严格解码。这样既能识别需要迁移的旧文档，
	// 又不会因为旧版本允许的默认值而放宽 1.0 对未知字段的拒绝策略。
	var version struct {
		DSLVersion string                     `json:"dslVersion"`
		Dataset    map[string]json.RawMessage `json:"dataset"`
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return Document{}, fmt.Errorf("decode DSL version: %w", err)
	}
	if version.DSLVersion == "" {
		version.DSLVersion = "0.9"
	}
	if version.DSLVersion != "0.9" && version.DSLVersion != DSLVersion {
		return Document{}, fmt.Errorf("unsupported DSL version %q", version.DSLVersion)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("decode DSL: %w", err)
	}
	_, layerSpecified := version.Dataset["layer"]
	document.layerInferred = !layerSpecified
	document.layerSpecified = layerSpecified
	if err := ensureJSONEOF(decoder); err != nil {
		return Document{}, err
	}
	// 0.9 及早期 1.0 示例把粒度放在 dataset.grain，迁移后统一到 outputGrain。
	if document.Dataset.Grain != nil {
		if document.OutputGrain.Description == "" && len(document.OutputGrain.KeyFields) == 0 {
			document.OutputGrain = *document.Dataset.Grain
		}
		document.Dataset.Grain = nil
	}
	document.DSLVersion = DSLVersion
	return normalize(document), nil
}

// InferLayer 为没有 layer 的历史 DSL 提供确定性迁移。推断只依赖可版本化 DSL，
// 不读取数据库状态，因此同一份历史文档在 API、worker 和回滚路径中结果一致。
//
// 含分组或聚合语义的文档归为 DWS；单个物理表且没有 Join 的文档归为 ODS；
// 其余清洗、转换或关联文档归为 DWD。
func InferLayer(document Document) Layer {
	if documentHasGroupingOrAggregation(document) {
		return LayerDWS
	}
	if len(document.Nodes) == 1 && document.Nodes[0].Type == "TABLE" && len(document.Joins) == 0 {
		return LayerODS
	}
	return LayerDWD
}

// Prepare 生成可持久化的规范 DSL、逻辑计划及两个稳定哈希。
func Prepare(raw []byte) (Prepared, error) {
	document, err := DecodeAndNormalize(raw)
	if err != nil {
		return Prepared{}, fmt.Errorf("%w: %v", ErrInvalidDocument, err)
	}
	if err := Validate(document); err != nil {
		return Prepared{}, err
	}
	// 缺少 layer 的历史正文继续按原形状计算哈希；推断结果由 Prepared.Document
	// 传给独立层级列。新 DSL 一旦显式声明 layer，就会把它纳入不可变正文和摘要。
	storedDocument := document
	if document.layerInferred {
		storedDocument.Dataset.Layer = ""
	}
	dslJSON, err := json.Marshal(storedDocument)
	if err != nil {
		return Prepared{}, err
	}
	plan := BuildLogicalPlan(document)
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{
		Document:        document,
		DSLJSON:         dslJSON,
		DSLHash:         hashJSON(dslJSON),
		LogicalPlan:     plan,
		LogicalPlanJSON: planJSON,
		PlanHash:        hashJSON(planJSON),
	}, nil
}

// Validate 对 DSL 的枚举、唯一性、引用完整性和执行限额进行领域校验。
func Validate(document Document) error {
	issues := make([]ValidationIssue, 0)
	add := func(path, reason string) { issues = append(issues, ValidationIssue{Path: path, Reason: reason}) }
	// 直接构造 Document 的旧调用方没有 JSON 字段存在性信息，也按历史 DSL 处理；
	// DecodeAndNormalize 会把显式空 layer 标记为 specified，使其继续严格失败。
	if document.Dataset.Layer == "" && !document.layerSpecified {
		document.Dataset.Layer = InferLayer(document)
	}
	// 校验会尽量收集完整问题集合，而不是遇到第一个错误就返回；设计器因此可以
	// 一次标出所有错误字段。后续各索引也只用于检查引用，不代表对象已经合法。
	if document.DSLVersion != DSLVersion {
		add("dslVersion", "必须为 1.0")
	}
	validateIdentifier(&issues, "dataset.code", document.Dataset.Code)
	if document.Dataset.Name == "" {
		add("dataset.name", "不能为空")
	}
	if !oneOf(document.Dataset.Type, "SINGLE_SOURCE", "CROSS_SOURCE") {
		add("dataset.type", "必须为 SINGLE_SOURCE 或 CROSS_SOURCE")
	}
	if !document.Dataset.Layer.Valid() {
		add("dataset.layer", "必须为 ODS、DIM、DWD、DWS 或 ADS")
	} else {
		validateLayerContract(&issues, document)
	}
	if document.Dataset.SemanticContractVersion != "" &&
		document.Dataset.SemanticContractVersion != "1.0" {
		add("dataset.semanticContractVersion", "当前仅支持 1.0")
	}
	if document.Dataset.ConsumerContractID != "" &&
		!canonicalUUID(document.Dataset.ConsumerContractID) {
		add("dataset.consumerContractId", "必须为规范 UUID")
	}
	validateDesigner(&issues, document.Designer)
	if len(document.Nodes) == 0 {
		add("nodes", "至少需要一个节点")
	}
	if len(document.Nodes) > MaxNodes {
		add("nodes", "最多允许 16 个节点")
	}

	// 表达式校验需要先知道完整的参数和节点命名空间，允许声明顺序与引用顺序无关。
	parameterRefs := map[string]bool{}
	for _, parameter := range document.Parameters {
		parameterRefs[parameter.Code] = true
	}
	nodeIDs := map[string]bool{}
	for _, node := range document.Nodes {
		nodeIDs[node.ID] = true
	}
	seenNodeIDs := map[string]bool{}
	nodeAliases := map[string]bool{}
	for i, node := range document.Nodes {
		path := fmt.Sprintf("nodes[%d]", i)
		validateIdentifier(&issues, path+".id", node.ID)
		if seenNodeIDs[node.ID] {
			add(path+".id", "节点标识重复")
		}
		seenNodeIDs[node.ID] = true
		validateIdentifier(&issues, path+".alias", node.Alias)
		if nodeAliases[node.Alias] {
			add(path+".alias", "节点别名重复")
		}
		nodeAliases[node.Alias] = true
		switch node.Type {
		case "TABLE":
			if node.DataSourceID == "" || node.TableID == "" {
				add(path, "TABLE 节点必须提供 datasourceId 和 tableId")
			}
			if node.DatasetVersionID != "" {
				add(path+".datasetVersionId", "TABLE 节点不能引用数据集版本")
			}
		case "DATASET":
			if node.DatasetVersionID == "" {
				add(path+".datasetVersionId", "DATASET 节点必须固定已发布版本")
			}
			if node.DataSourceID != "" || node.TableID != "" || node.FileVersionID != "" {
				add(path, "DATASET 节点不能包含物理源定位字段")
			}
		default:
			add(path+".type", "当前版本仅支持 TABLE 和 DATASET")
		}
		if len(node.Projection) == 0 {
			add(path+".projection", "至少需要一个投影字段")
		}
		seenProjection := map[string]bool{}
		for j, field := range node.Projection {
			validatePhysicalIdentifier(&issues, fmt.Sprintf("%s.projection[%d]", path, j), field)
			if seenProjection[field] {
				add(fmt.Sprintf("%s.projection[%d]", path, j), "投影字段重复")
			}
			seenProjection[field] = true
		}
		for j, filter := range node.SourceFilters {
			filterPath := fmt.Sprintf("%s.sourceFilters[%d]", path, j)
			if filter.Expression != nil {
				validateExpression(&issues, filterPath+".expression", *filter.Expression, nodeIDs, parameterRefs)
				validateSourceFilterExpression(&issues, filterPath+".expression", *filter.Expression, node.ID)
				continue
			}
			validatePhysicalIdentifier(&issues, filterPath+".field", filter.Field)
			if !seenProjection[filter.Field] {
				add(filterPath+".field", "源端过滤字段必须包含在节点 projection 中")
			}
			if !oneOf(filter.Operator, "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "IN", "NOT_IN", "IS_NULL", "IS_NOT_NULL") {
				add(filterPath+".operator", "不支持的源端过滤操作符")
			}
		}
	}
	dataSources := map[string]bool{}
	for _, node := range document.Nodes {
		if node.DataSourceID != "" {
			dataSources[node.DataSourceID] = true
		}
	}
	if document.Dataset.Type == "SINGLE_SOURCE" && len(dataSources) > 1 {
		add("dataset.type", "SINGLE_SOURCE 的全部物理表必须属于同一数据源")
	}
	if document.Dataset.Type == "CROSS_SOURCE" && len(dataSources) < 2 {
		add("dataset.type", "CROSS_SOURCE 必须引用至少两个不同数据源")
	}
	if len(document.Nodes) > 1 && len(document.Joins) == 0 {
		add("joins", "多节点数据集至少需要一个 Join")
	}

	joinIDs := map[string]bool{}
	for i, join := range document.Joins {
		path := fmt.Sprintf("joins[%d]", i)
		validateIdentifier(&issues, path+".id", join.ID)
		if joinIDs[join.ID] {
			add(path+".id", "Join 标识重复")
		}
		joinIDs[join.ID] = true
		if !nodeIDs[join.LeftNodeID] {
			add(path+".leftNodeId", "引用的左节点不存在")
		}
		if !nodeIDs[join.RightNodeID] {
			add(path+".rightNodeId", "引用的右节点不存在")
		}
		if join.LeftNodeID == join.RightNodeID && join.LeftNodeID != "" {
			add(path, "Join 两侧不能是同一节点")
		}
		if !oneOf(join.JoinType, "INNER", "LEFT", "RIGHT", "FULL") {
			add(path+".joinType", "不支持的 Join 类型")
		}
		if !oneOf(join.Cardinality, "UNKNOWN", "ONE_TO_ONE", "ONE_TO_MANY", "MANY_TO_ONE", "MANY_TO_MANY") {
			add(path+".cardinality", "不支持的 Join 基数")
		}
		if join.RelationshipType != "" && !oneOf(join.RelationshipType, "DIRECT", "ROLE_PLAYING", "BRIDGE") {
			add(path+".relationshipType", "必须为 DIRECT、ROLE_PLAYING 或 BRIDGE")
		}
		if join.RelationshipRole != "" {
			validateIdentifier(&issues, path+".relationshipRole", join.RelationshipRole)
		}
		if join.FanoutPolicy != "" && !oneOf(
			join.FanoutPolicy,
			"SAFE", "PRIMARY", "ALLOCATE", "DEDUPLICATE", "NON_ADDITIVE", "UNSAFE",
		) {
			add(path+".fanoutPolicy", "必须为 SAFE、PRIMARY、ALLOCATE、DEDUPLICATE、NON_ADDITIVE 或 UNSAFE")
		}
		if join.RelationshipType == "ROLE_PLAYING" && join.RelationshipRole == "" {
			add(path+".relationshipRole", "角色扮演维度必须声明稳定的业务角色编码")
		}
		if join.RelationshipType == "BRIDGE" && join.RelationshipRole == "" {
			add(path+".relationshipRole", "Bridge 关系必须声明稳定的业务角色编码")
		}
		if join.RelationshipType == "BRIDGE" && join.FanoutPolicy == "" {
			add(path+".fanoutPolicy", "Bridge 关系必须声明指标扇出处理策略")
		}
		if join.RelationshipType != "" && join.RelationshipType != "BRIDGE" &&
			join.FanoutPolicy != "" && join.FanoutPolicy != "SAFE" {
			add(path+".fanoutPolicy", "非 Bridge 关系只能使用 SAFE 扇出策略")
		}
		if len(join.Conditions) == 0 {
			add(path+".conditions", "至少需要一个 Join 条件")
		}
		for j, condition := range join.Conditions {
			conditionPath := fmt.Sprintf("%s.conditions[%d]", path, j)
			if !oneOf(condition.Operator, "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE") {
				add(conditionPath+".operator", "不支持的 Join 操作符")
			}
			validateExpression(&issues, conditionPath+".leftExpression", condition.LeftExpression, nodeIDs, parameterRefs)
			validateExpression(&issues, conditionPath+".rightExpression", condition.RightExpression, nodeIDs, parameterRefs)
			if condition.LeftExpression.Type != "FIELD_REF" || condition.LeftExpression.NodeID != join.LeftNodeID {
				add(conditionPath+".leftExpression", "必须引用 Join 左节点字段")
			}
			if condition.RightExpression.Type != "FIELD_REF" || condition.RightExpression.NodeID != join.RightNodeID {
				add(conditionPath+".rightExpression", "必须引用 Join 右节点字段")
			}
		}
		validateJoinSemanticContract(&issues, path, join, document.Nodes)
	}
	joinsByID := make(map[string]Join, len(document.Joins))
	for _, join := range document.Joins {
		joinsByID[join.ID] = join
	}
	projections := make(map[string]map[string]bool, len(document.Nodes))
	for _, node := range document.Nodes {
		projections[node.ID] = map[string]bool{}
		for _, field := range node.Projection {
			projections[node.ID][field] = true
		}
	}
	seenPreAggregationIDs := map[string]bool{}
	preAggregatedNodes := map[string]bool{}
	preAggregationOutputs := map[string]map[string]bool{}
	for i, item := range document.PreAggregations {
		path := fmt.Sprintf("preAggregations[%d]", i)
		validateIdentifier(&issues, path+".id", item.ID)
		if seenPreAggregationIDs[item.ID] {
			add(path+".id", "分组组件标识重复")
		}
		seenPreAggregationIDs[item.ID] = true
		if !nodeIDs[item.NodeID] {
			add(path+".nodeId", "引用的数据节点不存在")
		}
		if preAggregatedNodes[item.NodeID] {
			add(path+".nodeId", "同一个数据节点当前只允许一个关联前分组组件")
		}
		preAggregatedNodes[item.NodeID] = true
		join, exists := joinsByID[item.JoinID]
		if !exists {
			add(path+".joinId", "引用的关联组件不存在")
		} else if item.JoinSide == "LEFT" && join.LeftNodeID != item.NodeID || item.JoinSide == "RIGHT" && join.RightNodeID != item.NodeID {
			add(path+".joinSide", "分组组件连接的槽位与 Join 节点不一致")
		}
		if !oneOf(item.JoinSide, "LEFT", "RIGHT") {
			add(path+".joinSide", "必须为 LEFT 或 RIGHT")
		}
		if len(item.GroupBy) == 0 {
			add(path+".groupBy", "至少需要一个分组字段")
		}
		if len(item.Metrics) == 0 {
			add(path+".metrics", "至少需要一个聚合指标")
		}
		outputs := map[string]bool{}
		preAggregationOutputs[item.NodeID] = outputs
		for j, group := range item.GroupBy {
			fieldPath := fmt.Sprintf("%s.groupBy[%d]", path, j)
			validatePhysicalIdentifier(&issues, fieldPath+".field", group.Field)
			if group.Expression == nil && !projections[item.NodeID][group.Field] {
				add(fieldPath+".field", "分组字段必须包含在节点 projection 中")
			} else if group.Expression != nil {
				validateExpression(&issues, fieldPath+".expression", *group.Expression, nodeIDs, parameterRefs)
				validateExpressionProjection(&issues, fieldPath+".expression", *group.Expression, projections)
				validatePreAggregationSourceExpression(&issues, fieldPath+".expression", *group.Expression, item.NodeID)
				if expressionHasAggregate(*group.Expression) {
					add(fieldPath+".expression", "关联前分组字段不能包含聚合表达式")
				}
			}
			if outputs[group.Field] {
				add(fieldPath+".field", "分组输出字段重复")
			}
			outputs[group.Field] = true
			if group.Unit != "" && !oneOf(group.Unit, "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
				add(fieldPath+".unit", "不支持的日期粒度")
			}
		}
		for j, metric := range item.Metrics {
			fieldPath := fmt.Sprintf("%s.metrics[%d]", path, j)
			validatePhysicalIdentifier(&issues, fieldPath+".field", metric.Field)
			if metric.Expression == nil && !projections[item.NodeID][metric.Field] {
				add(fieldPath+".field", "指标字段必须包含在节点 projection 中")
			} else if metric.Expression != nil {
				validateExpression(&issues, fieldPath+".expression", *metric.Expression, nodeIDs, parameterRefs)
				validateExpressionProjection(&issues, fieldPath+".expression", *metric.Expression, projections)
				validatePreAggregationSourceExpression(&issues, fieldPath+".expression", *metric.Expression, item.NodeID)
				if expressionHasAggregate(*metric.Expression) {
					add(fieldPath+".expression", "关联前聚合指标的输入不能再次包含聚合表达式")
				}
			}
			if outputs[metric.Field] {
				add(fieldPath+".field", "分组组件的输出字段不能重名")
			}
			outputs[metric.Field] = true
			if !oneOf(metric.Function, "SUM", "AVG", "MIN", "MAX", "COUNT", "COUNT_DISTINCT") {
				add(fieldPath+".function", "不支持的聚合函数")
			}
		}
		if exists {
			conditions := join.Conditions
			for conditionIndex, condition := range conditions {
				expression := condition.LeftExpression
				if item.JoinSide == "RIGHT" {
					expression = condition.RightExpression
				}
				if expression.NodeID == item.NodeID && !outputs[expression.Field] {
					add(fmt.Sprintf("%s.conditions[%d]", path, conditionIndex), "关联字段必须是分组组件的输出字段")
				}
			}
		}
	}
	for i, field := range document.Fields {
		visitDatasetExpression(field.Expression, func(expression Expression) {
			if expression.Type == "FIELD_REF" && preAggregatedNodes[expression.NodeID] && !preAggregationOutputs[expression.NodeID][expression.Field] {
				add(fmt.Sprintf("fields[%d].expression", i), "最终输出只能引用分组组件产出的字段")
			}
		})
	}
	for i, filter := range document.Having {
		visitDatasetExpression(filter.Expression, func(expression Expression) {
			if expression.Type == "FIELD_REF" && preAggregatedNodes[expression.NodeID] && !preAggregationOutputs[expression.NodeID][expression.Field] {
				add(fmt.Sprintf("having[%d].expression", i), "聚合后过滤只能引用分组组件产出的字段")
			}
		})
	}
	for i, filter := range document.Filters {
		visitDatasetExpression(filter.Expression, func(expression Expression) {
			if expression.Type == "FIELD_REF" && preAggregatedNodes[expression.NodeID] {
				add(fmt.Sprintf("filters[%d].expression", i), "关联前分组节点请使用 sourceFilters，不能再配置全局聚合前过滤")
			}
		})
	}
	validateJoinGraph(&issues, document.Nodes, document.Joins)
	validateProjectedFieldRefs(&issues, document)

	parameterCodes := map[string]bool{}
	for i, parameter := range document.Parameters {
		path := fmt.Sprintf("parameters[%d]", i)
		validateIdentifier(&issues, path+".code", parameter.Code)
		if parameterCodes[parameter.Code] {
			add(path+".code", "参数编码重复")
		}
		parameterCodes[parameter.Code] = true
		if parameter.Name == "" {
			add(path+".name", "不能为空")
		}
		if !oneOf(parameter.DataType, "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME") {
			add(path+".dataType", "不支持的参数类型")
		}
	}

	fieldIDs := map[string]bool{}
	fieldCodes := map[string]bool{}
	fieldAggregations := make([]expressionAggregation, len(document.Fields))
	hasOutputAggregation := false
	for i, field := range document.Fields {
		path := fmt.Sprintf("fields[%d]", i)
		validateIdentifier(&issues, path+".id", field.ID)
		validateIdentifier(&issues, path+".code", field.Code)
		if fieldIDs[field.ID] {
			add(path+".id", "字段标识重复")
		}
		if fieldCodes[field.Code] {
			add(path+".code", "字段编码重复")
		}
		fieldIDs[field.ID], fieldCodes[field.Code] = true, true
		if field.Name == "" {
			add(path+".name", "不能为空")
		}
		if !oneOf(field.Role, "DIMENSION", "MEASURE", "ATTRIBUTE", "TIME", "IDENTIFIER") {
			add(path+".role", "不支持的字段角色")
		}
		if !oneOf(field.CanonicalType, "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME") {
			add(path+".canonicalType", "不支持的规范类型")
		}
		validateExpression(&issues, path+".expression", field.Expression, nodeIDs, parameterCodes)
		aggregation := analyzeExpressionAggregation(field.Expression, 0)
		fieldAggregations[i] = aggregation
		hasOutputAggregation = hasOutputAggregation || aggregation.hasAggregate
		if aggregation.nestedAggregate {
			add(path+".expression", "输出字段不允许嵌套聚合")
		}
		if aggregation.hasAggregate && aggregation.hasFreeField {
			add(path+".expression", "聚合输出不能混用聚合外的明细字段")
		}
	}
	if len(document.Fields) == 0 {
		add("fields", "至少需要一个输出字段")
	}

	filterIDs := map[string]bool{}
	for i, filter := range document.Filters {
		if filterIDs[filter.ID] {
			add(fmt.Sprintf("filters[%d].id", i), "过滤标识重复")
		}
		filterIDs[filter.ID] = true
		validateFilter(&issues, fmt.Sprintf("filters[%d]", i), filter, "PRE_AGGREGATION", nodeIDs, parameterCodes)
	}
	for i, filter := range document.Having {
		if filterIDs[filter.ID] {
			add(fmt.Sprintf("having[%d].id", i), "过滤标识重复")
		}
		filterIDs[filter.ID] = true
		validateFilter(&issues, fmt.Sprintf("having[%d]", i), filter, "POST_AGGREGATION", nodeIDs, parameterCodes)
	}
	groupFields := map[string]bool{}
	for i, fieldID := range document.GroupBy {
		if !fieldIDs[fieldID] {
			add(fmt.Sprintf("groupBy[%d]", i), "引用的输出字段不存在")
		}
		if groupFields[fieldID] {
			add(fmt.Sprintf("groupBy[%d]", i), "分组字段重复")
		}
		groupFields[fieldID] = true
	}
	for i, field := range document.Fields {
		aggregation := fieldAggregations[i]
		if groupFields[field.ID] && aggregation.hasAggregate {
			add(fmt.Sprintf("groupBy[%d]", groupByIndex(document.GroupBy, field.ID)), "分组字段不能引用聚合输出")
		}
		if hasOutputAggregation && !aggregation.hasAggregate &&
			aggregation.hasFreeField && !groupFields[field.ID] {
			add(fmt.Sprintf("fields[%d].expression", i), "存在聚合输出时，所有非聚合明细输出字段都必须出现在 groupBy")
		}
	}
	sortFields := map[string]bool{}
	for i, item := range document.Sorts {
		path := fmt.Sprintf("sorts[%d]", i)
		if !fieldIDs[item.FieldID] {
			add(path+".fieldId", "引用的输出字段不存在")
		}
		if !oneOf(item.Direction, "ASC", "DESC") {
			add(path+".direction", "必须为 ASC 或 DESC")
		}
		if item.Nulls != "" && !oneOf(item.Nulls, "FIRST", "LAST") {
			add(path+".nulls", "必须为 FIRST 或 LAST")
		}
		if sortFields[item.FieldID] {
			add(path+".fieldId", "排序字段重复")
		}
		sortFields[item.FieldID] = true
	}

	if document.OutputGrain.Description == "" {
		add("outputGrain.description", "必须说明每一行代表的业务含义")
	}
	if len(document.OutputGrain.KeyFields) == 0 {
		add("outputGrain.keyFields", "至少需要一个粒度键字段")
	}
	grainKeys := map[string]bool{}
	for i, fieldCode := range document.OutputGrain.KeyFields {
		if !fieldCodes[fieldCode] {
			add(fmt.Sprintf("outputGrain.keyFields[%d]", i), "引用的字段编码不存在")
		}
		if grainKeys[fieldCode] {
			add(fmt.Sprintf("outputGrain.keyFields[%d]", i), "粒度键字段重复")
		}
		grainKeys[fieldCode] = true
	}
	if document.OutputGrain.TimeField != "" && !fieldCodes[document.OutputGrain.TimeField] {
		add("outputGrain.timeField", "引用的时间字段编码不存在")
	}
	if document.OutputGrain.DefaultTimeGrain != "" && !oneOf(document.OutputGrain.DefaultTimeGrain, "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
		add("outputGrain.defaultTimeGrain", "不支持的默认时间粒度")
	}
	validateSemanticContracts(&issues, document)
	validateExecutionPolicy(&issues, document.ExecutionPolicy)
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

// Valid 把层级枚举校验集中在领域类型上，避免 API、仓储和后续物化器各自维护字符串集合。
func (layer Layer) Valid() bool {
	return layer == LayerODS || layer == LayerDIM || layer == LayerDWD ||
		layer == LayerDWS || layer == LayerADS
}

// validateLayerContract 校验仅依赖当前 DSL 的层级合同。跨数据集版本的上游层级
// 由 ValidateLayerDependencies 在仓储解析精确发布版本后补充校验。
func validateLayerContract(issues *[]ValidationIssue, document Document) {
	add := func(path, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: reason})
	}
	hasAggregation := documentHasBusinessAggregation(document)
	hasGrouping := len(document.GroupBy) > 0 || len(document.Having) > 0 || len(document.PreAggregations) > 0
	switch document.Dataset.Layer {
	case LayerODS:
		if len(document.Nodes) != 1 || document.Nodes[0].Type != "TABLE" {
			add("nodes", "ODS 只能包含一个物理 TABLE 节点")
		}
		if len(document.Joins) > 0 {
			add("joins", "ODS 不允许 Join")
		}
		if hasGrouping || hasAggregation {
			add("dataset.layer", "ODS 不允许分组或聚合")
		}
	case LayerDIM:
		if document.layerSpecified {
			for index, node := range document.Nodes {
				if node.Type != "DATASET" {
					add(fmt.Sprintf("nodes[%d].type", index), "显式 DIM 只能引用已发布 ODS 数据集版本")
				}
			}
		}
		if hasGrouping || hasAggregation {
			add("dataset.layer", "DIM 保存实体说明信息，不允许业务分组或聚合")
		}
		if document.OutputGrain.Description == "" || len(document.OutputGrain.KeyFields) == 0 {
			add("outputGrain", "DIM 必须显式声明实体粒度和业务键")
		}
	case LayerDWD:
		if document.layerSpecified {
			for index, node := range document.Nodes {
				if node.Type != "DATASET" {
					add(fmt.Sprintf("nodes[%d].type", index), "显式 DWD 只能引用已发布 ODS 或 DIM 数据集版本")
				}
			}
			for index, join := range document.Joins {
				path := fmt.Sprintf("joins[%d]", index)
				switch join.Cardinality {
				case "UNKNOWN":
					add(path+".cardinality", "显式 DWD 必须证明 Join 基数，不能使用 UNKNOWN")
				case "ONE_TO_MANY", "MANY_TO_MANY":
					if join.RelationshipType != "BRIDGE" {
						add(path+".relationshipType", "DWD 的多值关联必须显式建模为 BRIDGE，多个普通 DIM 应分别使用 MANY_TO_ONE 或 ONE_TO_ONE")
					}
					if !oneOf(join.FanoutPolicy, "PRIMARY", "ALLOCATE", "DEDUPLICATE", "NON_ADDITIVE", "UNSAFE") {
						add(path+".fanoutPolicy", "DWD Bridge 必须选择 PRIMARY、ALLOCATE、DEDUPLICATE、NON_ADDITIVE 或 UNSAFE")
					}
				case "ONE_TO_ONE", "MANY_TO_ONE":
					if join.RelationshipType == "BRIDGE" {
						add(path+".relationshipType", "ONE_TO_ONE 或 MANY_TO_ONE 的普通维度关联不应标记为 BRIDGE")
					}
				}
			}
		}
		if hasGrouping || hasAggregation {
			add("dataset.layer", "DWD 必须保持明细粒度，不允许业务分组或聚合")
		}
	case LayerDWS:
		if document.layerSpecified {
			for index, node := range document.Nodes {
				if node.Type != "DATASET" {
					add(fmt.Sprintf("nodes[%d].type", index), "显式 DWS 只能引用已发布 DWD 数据集版本")
				}
			}
		}
		if document.OutputGrain.Description == "" || len(document.OutputGrain.KeyFields) == 0 {
			add("outputGrain", "DWS 必须显式声明输出业务粒度和粒度键")
		}
		if !hasAggregation {
			add("dataset.layer", "DWS 至少需要一个聚合指标")
		}
	case LayerADS:
		if document.layerSpecified {
			for index, node := range document.Nodes {
				if node.Type != "DATASET" {
					add(fmt.Sprintf("nodes[%d].type", index), "显式 ADS 只能引用已发布 DWS 数据集版本")
				}
			}
		}
		if document.OutputGrain.Description == "" || len(document.OutputGrain.KeyFields) == 0 {
			add("outputGrain", "ADS 必须显式声明面向消费场景的输出粒度和粒度键")
		}
		if document.Dataset.SemanticContractVersion == "1.0" &&
			document.Dataset.ConsumerContractID == "" {
			add("dataset.consumerContractId", "语义合同 1.0 的 ADS 必须绑定已发布消费合同")
		}
	}
}

// documentHasBusinessAggregation 只识别合法承载业务聚合的位置：关联前分组指标和
// 最终输出字段。WHERE、JOIN、GROUP BY 辅助表达式或 HAVING 中夹带 AGGREGATE
// 不能反向把错误 DSL 伪装成 DWS；这些位置由各自的上下文校验失败关闭。
func documentHasBusinessAggregation(document Document) bool {
	for _, item := range document.PreAggregations {
		if len(item.Metrics) > 0 {
			return true
		}
	}
	found := false
	visit := func(expression Expression) {
		if expression.Type == "AGGREGATE" {
			found = true
		}
	}
	for _, field := range document.Fields {
		visitDatasetExpression(field.Expression, visit)
	}
	return found
}

func validateJoinSemanticContract(
	issues *[]ValidationIssue,
	path string,
	join Join,
	nodes []Node,
) {
	add := func(suffix, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path + suffix, Reason: reason})
	}
	projections := map[string]map[string]bool{}
	for _, node := range nodes {
		fields := map[string]bool{}
		for _, field := range node.Projection {
			fields[field] = true
		}
		projections[node.ID] = fields
	}
	if join.Bridge != nil {
		bridge := join.Bridge
		if join.RelationshipType != "BRIDGE" {
			add(".bridge", "只有 BRIDGE 关系可以声明 Bridge 字段合同")
		}
		if bridge.BridgeNodeID != join.LeftNodeID && bridge.BridgeNodeID != join.RightNodeID {
			add(".bridge.bridgeNodeId", "必须引用 Join 的左节点或右节点")
		}
		validateBridgeField := func(field, suffix string) {
			if field == "" {
				return
			}
			validatePhysicalIdentifier(issues, path+".bridge."+suffix, field)
			if !projections[bridge.BridgeNodeID][field] {
				add(".bridge."+suffix, "必须包含在 Bridge 节点 projection 中")
			}
		}
		validateBridgeField(bridge.RelationshipTypeField, "relationshipTypeField")
		validateBridgeField(bridge.AllocationWeightField, "allocationWeightField")
		validateBridgeField(bridge.PrimaryFlagField, "primaryFlagField")
		validateBridgeField(bridge.ValidFromField, "validFromField")
		validateBridgeField(bridge.ValidToField, "validToField")
		if (bridge.ValidFromField == "") != (bridge.ValidToField == "") {
			add(".bridge", "Bridge 有效区间必须同时声明 validFromField 和 validToField")
		}
		if bridge.RelationshipTypeField == "" {
			add(".bridge.relationshipTypeField", "Bridge 必须声明关系类型字段")
		}
		if join.FanoutPolicy == "ALLOCATE" && bridge.AllocationWeightField == "" {
			add(".bridge.allocationWeightField", "ALLOCATE 策略必须声明分配权重字段")
		}
		if join.FanoutPolicy == "PRIMARY" && bridge.PrimaryFlagField == "" {
			add(".bridge.primaryFlagField", "PRIMARY 策略必须声明主成员标记字段")
		}
	} else if join.RelationshipType == "BRIDGE" {
		add(".bridge", "BRIDGE 关系必须声明结构化 Bridge 字段合同")
	}

	if join.Temporal == nil {
		return
	}
	temporal := join.Temporal
	if temporal.EventNodeID != join.LeftNodeID && temporal.EventNodeID != join.RightNodeID {
		add(".temporal.eventNodeId", "必须引用 Join 的左节点或右节点")
	}
	if temporal.ValidityNodeID != join.LeftNodeID && temporal.ValidityNodeID != join.RightNodeID {
		add(".temporal.validityNodeId", "必须引用 Join 的左节点或右节点")
	}
	if temporal.EventNodeID == temporal.ValidityNodeID {
		add(".temporal", "事件时间节点和有效区间节点必须位于 Join 两侧")
	}
	for _, field := range []struct {
		value  string
		suffix string
	}{
		{value: temporal.EventTimeField, suffix: "eventTimeField"},
		{value: temporal.ValidFromField, suffix: "validFromField"},
		{value: temporal.ValidToField, suffix: "validToField"},
	} {
		validatePhysicalIdentifier(issues, path+".temporal."+field.suffix, field.value)
	}
	if !projections[temporal.EventNodeID][temporal.EventTimeField] {
		add(".temporal.eventTimeField", "必须包含在事件节点 projection 中")
	}
	if !projections[temporal.ValidityNodeID][temporal.ValidFromField] {
		add(".temporal.validFromField", "必须包含在有效区间节点 projection 中")
	}
	if !projections[temporal.ValidityNodeID][temporal.ValidToField] {
		add(".temporal.validToField", "必须包含在有效区间节点 projection 中")
	}
	if !temporalJoinConditionsPresent(join) {
		add(".temporal", "SCD2 Join conditions 必须包含 eventTime >= validFrom 且 eventTime < validTo")
	}
}

func temporalJoinConditionsPresent(join Join) bool {
	temporal := join.Temporal
	if temporal == nil {
		return false
	}
	fromFound, toFound := false, false
	for _, condition := range join.Conditions {
		left, right := condition.LeftExpression, condition.RightExpression
		if left.Type != "FIELD_REF" || right.Type != "FIELD_REF" {
			continue
		}
		eventOnLeft := left.NodeID == temporal.EventNodeID &&
			left.Field == temporal.EventTimeField
		eventOnRight := right.NodeID == temporal.EventNodeID &&
			right.Field == temporal.EventTimeField
		fromOnLeft := left.NodeID == temporal.ValidityNodeID &&
			left.Field == temporal.ValidFromField
		fromOnRight := right.NodeID == temporal.ValidityNodeID &&
			right.Field == temporal.ValidFromField
		toOnLeft := left.NodeID == temporal.ValidityNodeID &&
			left.Field == temporal.ValidToField
		toOnRight := right.NodeID == temporal.ValidityNodeID &&
			right.Field == temporal.ValidToField
		fromFound = fromFound ||
			(eventOnLeft && fromOnRight && condition.Operator == "GTE") ||
			(fromOnLeft && eventOnRight && condition.Operator == "LTE")
		toOperator, inverseToOperator := "LT", "GT"
		if temporal.ValidToInclusive {
			toOperator, inverseToOperator = "LTE", "GTE"
		}
		toFound = toFound ||
			(eventOnLeft && toOnRight && condition.Operator == toOperator) ||
			(toOnLeft && eventOnRight && condition.Operator == inverseToOperator)
	}
	return fromFound && toFound
}

func validateSemanticContracts(issues *[]ValidationIssue, document Document) {
	add := func(path, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: reason})
	}
	fields := make(map[string]Field, len(document.Fields))
	nodes := make(map[string]bool, len(document.Nodes))
	for _, field := range document.Fields {
		fields[field.Code] = field
	}
	for _, node := range document.Nodes {
		nodes[node.ID] = true
	}

	if document.FactContract != nil {
		contract := document.FactContract
		if document.Dataset.Layer != LayerDWD {
			add("factContract", "事实合同只能用于 DWD")
		}
		if strings.TrimSpace(contract.BusinessAction) == "" ||
			len([]rune(contract.BusinessAction)) > 256 {
			add("factContract.businessAction", "必须声明 1 至 256 字符的业务动作")
		}
		if !sameStringSet(contract.GrainKeyFields, document.OutputGrain.KeyFields) {
			add("factContract.grainKeyFields", "必须与 outputGrain.keyFields 完全一致")
		}
		if contract.EventTimeField != "" {
			field, exists := fields[contract.EventTimeField]
			if !exists || field.Role != "TIME" {
				add("factContract.eventTimeField", "必须引用 TIME 输出字段")
			}
		}
		measureContracts := map[string]bool{}
		for index, measure := range contract.AtomicMeasures {
			path := fmt.Sprintf("factContract.atomicMeasures[%d]", index)
			field, exists := fields[measure.Field]
			if !exists || field.Role != "MEASURE" {
				add(path+".field", "必须引用 MEASURE 输出字段")
			}
			if measureContracts[measure.Field] {
				add(path+".field", "原子度量重复")
			}
			measureContracts[measure.Field] = true
			if !oneOf(measure.Additivity, "ADDITIVE", "SEMI_ADDITIVE", "NON_ADDITIVE") {
				add(path+".additivity", "必须为 ADDITIVE、SEMI_ADDITIVE 或 NON_ADDITIVE")
			}
			if !oneOf(measure.NullPolicy, "PRESERVE", "ZERO", "REJECT") {
				add(path+".nullPolicy", "必须为 PRESERVE、ZERO 或 REJECT")
			}
			if len(measure.Unit) > 64 || len(measure.Currency) > 16 {
				add(path, "单位或币种超出长度限制")
			}
		}
		for _, field := range document.Fields {
			if field.Role == "MEASURE" && !measureContracts[field.Code] {
				add("factContract.atomicMeasures", "必须覆盖每一个 MEASURE 输出字段")
			}
		}
	} else if document.Dataset.SemanticContractVersion == "1.0" &&
		document.Dataset.Layer == LayerDWD {
		add("factContract", "语义合同 1.0 的 DWD 必须声明事实粒度和原子度量")
	}

	if document.AnalysisContract != nil {
		contract := document.AnalysisContract
		if document.Dataset.Layer != LayerDWS {
			add("analysisContract", "分析合同只能用于 DWS")
		}
		if !oneOf(
			contract.Intent,
			"TREND", "PERIOD_COMPARISON", "DISTRIBUTION", "RANKING", "TOP_N",
			"DRILLDOWN", "FUNNEL", "RETENTION", "LIFECYCLE", "ANOMALY",
			"CONTRIBUTION", "MULTI_FACT_COMPARISON",
		) {
			add("analysisContract.intent", "不支持的市场通用分析意图")
		}
		expectedMode := "SINGLE_FACT"
		if len(document.Nodes) > 1 {
			expectedMode = "MULTI_FACT"
		}
		if contract.InputMode != expectedMode {
			add("analysisContract.inputMode", "必须与 DWD 输入数量一致")
		}
		if !sameStringSet(contract.CommonGrainFields, document.OutputGrain.KeyFields) {
			add("analysisContract.commonGrainFields", "必须与 outputGrain.keyFields 完全一致")
		}
		for index, code := range contract.ConformedDimensions {
			field, exists := fields[code]
			if !exists || field.Role == "MEASURE" {
				add(fmt.Sprintf("analysisContract.conformedDimensions[%d]", index), "必须引用非度量输出字段")
			}
		}
		if contract.TimeField != "" {
			field, exists := fields[contract.TimeField]
			if !exists || field.Role != "TIME" {
				add("analysisContract.timeField", "必须引用 TIME 输出字段")
			}
		}
		if contract.TimeGrain != "" &&
			!oneOf(contract.TimeGrain, "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
			add("analysisContract.timeGrain", "不支持的时间粒度")
		}
		for index, measure := range contract.Measures {
			path := fmt.Sprintf("analysisContract.measures[%d]", index)
			field, exists := fields[measure.Field]
			if !exists || field.Role != "MEASURE" {
				add(path+".field", "必须引用 MEASURE 输出字段")
			}
			if !oneOf(measure.Aggregation, "SUM", "MIN", "MAX", "COUNT", "COUNT_DISTINCT", "AVG") {
				add(path+".aggregation", "不支持的分析聚合")
			}
			if !oneOf(measure.Additivity, "ADDITIVE", "SEMI_ADDITIVE", "NON_ADDITIVE") {
				add(path+".additivity", "必须声明指标可加性")
			}
			if len(measure.SourceNodeIDs) == 0 {
				add(path+".sourceNodeIds", "至少引用一个 DWD 输入节点")
			}
			for sourceIndex, nodeID := range measure.SourceNodeIDs {
				if !nodes[nodeID] {
					add(fmt.Sprintf("%s.sourceNodeIds[%d]", path, sourceIndex), "引用的 DWD 输入节点不存在")
				}
			}
		}
		if len(document.Nodes) > 1 {
			preAggregated := map[string]bool{}
			for _, item := range document.PreAggregations {
				preAggregated[item.NodeID] = true
			}
			for index, node := range document.Nodes {
				if !preAggregated[node.ID] {
					add(fmt.Sprintf("nodes[%d]", index), "多事实 DWS 必须先把每个 DWD 聚合到共同粒度再 Join")
				}
			}
		}
	} else if document.Dataset.SemanticContractVersion == "1.0" &&
		document.Dataset.Layer == LayerDWS {
		add("analysisContract", "语义合同 1.0 的 DWS 必须声明分析意图和共同粒度")
	}
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]int, len(left))
	for _, value := range left {
		values[value]++
	}
	for _, value := range right {
		values[value]--
		if values[value] < 0 {
			return false
		}
	}
	for _, count := range values {
		if count != 0 {
			return false
		}
	}
	return true
}

type expressionAggregation struct {
	hasAggregate, hasFreeField, nestedAggregate bool
}

func expressionHasAggregate(expression Expression) bool {
	return analyzeExpressionAggregation(expression, 0).hasAggregate
}

func analyzeExpressionAggregation(
	expression Expression,
	aggregateDepth int,
) expressionAggregation {
	result := expressionAggregation{}
	if expression.Type == "AGGREGATE" {
		result.hasAggregate = true
		if aggregateDepth > 0 {
			result.nestedAggregate = true
		}
		aggregateDepth++
	}
	if expression.Type == "FIELD_REF" && aggregateDepth == 0 {
		result.hasFreeField = true
	}
	merge := func(child Expression) {
		next := analyzeExpressionAggregation(child, aggregateDepth)
		result.hasAggregate = result.hasAggregate || next.hasAggregate
		result.hasFreeField = result.hasFreeField || next.hasFreeField
		result.nestedAggregate = result.nestedAggregate || next.nestedAggregate
	}
	for _, child := range []*Expression{
		expression.Argument, expression.Left, expression.Right,
		expression.Lower, expression.Upper, expression.Else,
	} {
		if child != nil {
			merge(*child)
		}
	}
	for _, child := range expression.Arguments {
		merge(child)
	}
	for _, branch := range expression.Whens {
		merge(branch.When)
		merge(branch.Then)
	}
	return result
}

func groupByIndex(groupBy []string, fieldID string) int {
	for index, candidate := range groupBy {
		if candidate == fieldID {
			return index
		}
	}
	return 0
}

func documentHasGroupingOrAggregation(document Document) bool {
	return len(document.GroupBy) > 0 || len(document.Having) > 0 ||
		len(document.PreAggregations) > 0 || documentHasBusinessAggregation(document)
}

// validateDesigner 只校验画布元数据的稳定边界，不把展示配置提升为查询语义。
// components 之外的扩展键会原样保留，便于前端在 DSL V1 内增量演进；执行计划
// 刻意不读取 Designer，因此整理画布不会改变 planHash。
func validateDesigner(issues *[]ValidationIssue, designer map[string]any) {
	if designer == nil {
		return
	}
	add := func(path, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: reason})
	}
	if raw, exists := designer["version"]; exists {
		version, ok := raw.(string)
		if !ok || strings.TrimSpace(version) != "1.0" {
			add("designer.version", "必须为 1.0")
		}
	}
	// 当前画布使用 nodePositions + joins/groups/transforms/end 的固定图结构。设计态拓扑
	// 必须保持引用完整且无环；字段执行语义仍由下方可执行 DSL 校验兜底。
	fixedIDs := map[string]bool{}
	if raw, exists := designer["nodePositions"]; exists {
		positions, ok := designerObject(raw)
		if !ok {
			add("designer.nodePositions", "必须为坐标对象")
		} else {
			for id, position := range positions {
				path := "designer.nodePositions." + id
				if !identifierPattern.MatchString(strings.TrimSpace(id)) {
					add(path, "节点标识不合法")
				} else if fixedIDs[id] {
					add(path, "画布组件标识重复")
				} else {
					fixedIDs[id] = true
				}
				validateDesignerPosition(issues, path, position)
			}
		}
	}
	validateDesignerItems(issues, designer, "joins", "关联", fixedIDs)
	validateDesignerItems(issues, designer, "groups", "分组", fixedIDs)
	validateDesignerItems(issues, designer, "transforms", "字段处理", fixedIDs)
	if raw, exists := designer["end"]; exists {
		end, ok := designerObject(raw)
		if !ok {
			add("designer.end", "必须为结束节点对象")
		} else {
			validateDesignerItem(issues, "designer.end", "结束", end, fixedIDs)
		}
	}
	if raw, exists := designer["components"]; exists {
		components, ok := designerList(raw)
		if !ok {
			add("designer.components", "必须为组件数组")
		} else {
			seen := map[string]bool{}
			for index, component := range components {
				path := fmt.Sprintf("designer.components[%d]", index)
				if component == nil {
					add(path, "必须为组件对象")
					continue
				}
				id, idOK := component["id"].(string)
				id = strings.TrimSpace(id)
				if !idOK || !identifierPattern.MatchString(id) {
					add(path+".id", "必须是合法且非空的组件标识")
				} else if seen[id] {
					add(path+".id", "组件标识重复")
				} else {
					seen[id] = true
				}
				kind, kindOK := component["kind"].(string)
				kind = upper(kind)
				if !kindOK || !oneOf(kind, "DATA", "NODE", "JOIN", "GROUP", "TRANSFORM", "OUTPUT") {
					add(path+".kind", "必须为 DATA、NODE、JOIN、GROUP、TRANSFORM 或 OUTPUT")
				}
				if position, exists := component["position"]; exists {
					validateDesignerPosition(issues, path+".position", position)
				} else if _, hasX := component["x"]; hasX {
					validateDesignerPosition(issues, path, component)
				} else if _, hasY := component["y"]; hasY {
					validateDesignerPosition(issues, path, component)
				}
			}
		}
	}
	// 兼容以组件 ID 为键单独保存坐标的客户端表示。
	if raw, exists := designer["positions"]; exists {
		positions, ok := designerObject(raw)
		if !ok {
			add("designer.positions", "必须为坐标对象")
			return
		}
		for id, position := range positions {
			validateDesignerPosition(issues, "designer.positions."+id, position)
		}
	}
	validateDesignerDAG(issues, designer)
}

// validateDesignerDAG 校验固定画布图中已经声明的连线。缺少输入仍允许作为设计中间态，
// 但只要提供了输入，就必须指向存在的上游组件且不能形成自环或间接循环。
func validateDesignerDAG(issues *[]ValidationIssue, designer map[string]any) {
	if _, hasJoins := designer["joins"]; !hasJoins {
		if _, hasGroups := designer["groups"]; !hasGroups {
			if _, hasTransforms := designer["transforms"]; !hasTransforms {
				if _, hasEnd := designer["end"]; !hasEnd {
					return
				}
			}
		}
	}
	add := func(path, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: reason})
	}
	existing := map[string]bool{}
	if positions, ok := designerObject(designer["nodePositions"]); ok {
		for id := range positions {
			existing["NODE:"+strings.TrimSpace(id)] = true
		}
	}
	joins, _ := designerList(designer["joins"])
	groups, _ := designerList(designer["groups"])
	transforms, _ := designerList(designer["transforms"])
	for _, item := range joins {
		if id, ok := item["id"].(string); ok && strings.TrimSpace(id) != "" {
			existing["JOIN:"+strings.TrimSpace(id)] = true
		}
	}
	for _, item := range groups {
		if id, ok := item["id"].(string); ok && strings.TrimSpace(id) != "" {
			existing["GROUP:"+strings.TrimSpace(id)] = true
		}
	}
	for _, item := range transforms {
		if id, ok := item["id"].(string); ok && strings.TrimSpace(id) != "" {
			existing["TRANSFORM:"+strings.TrimSpace(id)] = true
		}
	}
	end, hasEnd := designerObject(designer["end"])
	if hasEnd {
		if id, ok := end["id"].(string); ok && strings.TrimSpace(id) != "" {
			existing["OUTPUT:"+strings.TrimSpace(id)] = true
		}
	}

	dependencies := map[string][]string{}
	for key := range existing {
		dependencies[key] = nil
	}
	validateInput := func(path, target string, raw any) {
		if raw == nil {
			return
		}
		value, ok := designerObject(raw)
		if !ok {
			add(path, "必须为画布输入引用")
			return
		}
		kind, kindOK := value["kind"].(string)
		id, idOK := value["id"].(string)
		kind, id = upper(kind), strings.TrimSpace(id)
		if !kindOK || !idOK || !oneOf(kind, "NODE", "JOIN", "GROUP", "TRANSFORM") || !identifierPattern.MatchString(id) {
			add(path, "必须引用合法的数据、关联、分组或字段处理组件")
			return
		}
		source := kind + ":" + id
		if !existing[source] {
			add(path, "引用的上游组件不存在或已被删除")
			return
		}
		if source == target {
			add(path, "画布组件不能连接到自身")
			return
		}
		dependencies[target] = append(dependencies[target], source)
	}
	for index, item := range joins {
		id, _ := item["id"].(string)
		target := "JOIN:" + strings.TrimSpace(id)
		if target == "JOIN:" || !existing[target] {
			continue
		}
		validateInput(fmt.Sprintf("designer.joins[%d].left", index), target, item["left"])
		validateInput(fmt.Sprintf("designer.joins[%d].right", index), target, item["right"])
	}
	for index, item := range groups {
		id, _ := item["id"].(string)
		target := "GROUP:" + strings.TrimSpace(id)
		if target == "GROUP:" || !existing[target] {
			continue
		}
		validateInput(fmt.Sprintf("designer.groups[%d].input", index), target, item["input"])
	}
	for index, item := range transforms {
		id, _ := item["id"].(string)
		target := "TRANSFORM:" + strings.TrimSpace(id)
		if target == "TRANSFORM:" || !existing[target] {
			continue
		}
		validateInput(fmt.Sprintf("designer.transforms[%d].input", index), target, item["input"])
	}
	if hasEnd {
		id, _ := end["id"].(string)
		target := "OUTPUT:" + strings.TrimSpace(id)
		if target != "OUTPUT:" && existing[target] {
			validateInput("designer.end.input", target, end["input"])
		}
	}

	states := map[string]uint8{}
	stack := make([]string, 0)
	cycleAdded := false
	var visit func(string)
	visit = func(key string) {
		if cycleAdded || states[key] == 2 {
			return
		}
		states[key] = 1
		stack = append(stack, key)
		for _, dependency := range dependencies[key] {
			if states[dependency] == 1 {
				start := 0
				for index, candidate := range stack {
					if candidate == dependency {
						start = index
						break
					}
				}
				cycle := append(append([]string{}, stack[start:]...), dependency)
				add("designer", "画布存在循环依赖："+strings.Join(cycle, " → "))
				cycleAdded = true
				break
			}
			if states[dependency] == 0 {
				visit(dependency)
			}
		}
		stack = stack[:len(stack)-1]
		states[key] = 2
	}
	for key := range dependencies {
		if cycleAdded {
			break
		}
		if states[key] == 0 {
			visit(key)
		}
	}
}

func validateDesignerItems(issues *[]ValidationIssue, designer map[string]any, key, label string, seen map[string]bool) {
	raw, exists := designer[key]
	if !exists {
		return
	}
	items, ok := designerList(raw)
	if !ok {
		*issues = append(*issues, ValidationIssue{Path: "designer." + key, Reason: "必须为" + label + "节点数组"})
		return
	}
	for index, item := range items {
		validateDesignerItem(issues, fmt.Sprintf("designer.%s[%d]", key, index), label, item, seen)
	}
}

func validateDesignerItem(issues *[]ValidationIssue, path, label string, item map[string]any, seen map[string]bool) {
	id, ok := item["id"].(string)
	id = strings.TrimSpace(id)
	if !ok || !identifierPattern.MatchString(id) {
		*issues = append(*issues, ValidationIssue{Path: path + ".id", Reason: label + "节点标识不合法"})
	} else if seen[id] {
		*issues = append(*issues, ValidationIssue{Path: path + ".id", Reason: "画布组件标识重复"})
	} else {
		seen[id] = true
	}
	position, exists := item["position"]
	if !exists {
		*issues = append(*issues, ValidationIssue{Path: path + ".position", Reason: "必须提供坐标"})
		return
	}
	validateDesignerPosition(issues, path+".position", position)
}

func validateDesignerPosition(issues *[]ValidationIssue, path string, raw any) {
	position, ok := designerObject(raw)
	if !ok {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "必须为坐标对象"})
		return
	}
	for _, axis := range []string{"x", "y"} {
		value, exists := position[axis]
		number, valid := designerNumber(value)
		if !exists || !valid || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			*issues = append(*issues, ValidationIssue{Path: path + "." + axis, Reason: "必须为有限的非负数"})
		}
	}
}

func designerList(raw any) ([]map[string]any, bool) {
	switch values := raw.(type) {
	case []any:
		result := make([]map[string]any, len(values))
		for index, value := range values {
			object, ok := designerObject(value)
			if !ok {
				return nil, false
			}
			result[index] = object
		}
		return result, true
	case []map[string]any:
		return values, true
	default:
		return nil, false
	}
}

func designerObject(raw any) (map[string]any, bool) {
	value, ok := raw.(map[string]any)
	return value, ok
}

func designerNumber(raw any) (float64, bool) {
	switch value := raw.(type) {
	case json.Number:
		number, err := value.Float64()
		return number, err == nil
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int8:
		return float64(value), true
	case int16:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint8:
		return float64(value), true
	case uint16:
		return float64(value), true
	case uint32:
		return float64(value), true
	case uint64:
		return float64(value), true
	default:
		return 0, false
	}
}

func validateSourceFilterExpression(issues *[]ValidationIssue, path string, expression Expression, nodeID string) {
	// 源过滤会在 Join 前直接送往单个数据源，只允许布尔谓词，且其任意深度的
	// 字段引用都必须属于当前节点；聚合只能在分组阶段执行。
	if !oneOf(expression.Type, "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "LIKE", "CONTAINS", "NOT_CONTAINS", "IN", "NOT_IN", "BETWEEN", "IS_NULL", "IS_NOT_NULL", "AND", "OR", "NOT") {
		*issues = append(*issues, ValidationIssue{Path: path + ".type", Reason: "源过滤表达式必须是布尔谓词"})
	}
	visit := func(item Expression) {
		if item.Type == "FIELD_REF" && item.NodeID != nodeID {
			*issues = append(*issues, ValidationIssue{Path: path + ".nodeId", Reason: "源端过滤只能引用当前节点"})
		}
		if item.Type == "AGGREGATE" {
			*issues = append(*issues, ValidationIssue{Path: path + ".type", Reason: "源过滤不能包含聚合表达式"})
		}
	}
	visitDatasetExpression(expression, visit)
}

// validatePreAggregationSourceExpression keeps derived pre-join grouping expressions inside
// their single physical branch. Aggregation is applied by the preAggregation wrapper itself;
// accepting a nested aggregate here would make the resulting grain ambiguous.
func validatePreAggregationSourceExpression(issues *[]ValidationIssue, path string, expression Expression, nodeID string) {
	visitDatasetExpression(expression, func(value Expression) {
		if value.Type == "FIELD_REF" && value.NodeID != nodeID {
			*issues = append(*issues, ValidationIssue{Path: path + ".nodeId", Reason: "关联前分组表达式只能引用所属数据节点"})
		}
		if value.Type == "AGGREGATE" {
			*issues = append(*issues, ValidationIssue{Path: path, Reason: "关联前分组源表达式不能包含聚合函数"})
		}
	})
}

func visitDatasetExpression(expression Expression, visit func(Expression)) {
	visit(expression)
	for _, child := range []*Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil {
			visitDatasetExpression(*child, visit)
		}
	}
	for _, child := range expression.Arguments {
		visitDatasetExpression(child, visit)
	}
	for _, branch := range expression.Whens {
		visitDatasetExpression(branch.When, visit)
		visitDatasetExpression(branch.Then, visit)
	}
}

// BuildLogicalPlan 从规范 DSL 生成与 SQL 方言无关的稳定逻辑计划。
func BuildLogicalPlan(document Document) LogicalPlan {
	// 计划只记录可审计的逻辑步骤，不携带物理表名、SQL 或参数值。切片顺序沿用
	// 规范 DSL 的声明顺序，使相同文档每次都能生成相同 planHash。
	plan := LogicalPlan{DSLVersion: DSLVersion, OutputGrain: document.OutputGrain}
	for _, node := range document.Nodes {
		plan.Steps = append(plan.Steps, PlanStep{ID: node.ID, Kind: "SCAN", Fields: append([]string(nil), node.Projection...)})
	}
	for _, item := range document.PreAggregations {
		fields := make([]string, 0, len(item.GroupBy)+len(item.Metrics))
		for _, group := range item.GroupBy {
			fields = append(fields, group.Field)
		}
		for _, metric := range item.Metrics {
			fields = append(fields, metric.Function+":"+metric.Field)
		}
		plan.Steps = append(plan.Steps, PlanStep{ID: item.ID, Kind: "PRE_AGGREGATE", Inputs: []string{item.NodeID}, Fields: fields})
	}
	for _, join := range document.Joins {
		plan.Steps = append(plan.Steps, PlanStep{ID: join.ID, Kind: "JOIN_" + join.JoinType, Inputs: []string{join.LeftNodeID, join.RightNodeID}})
	}
	if len(document.Filters) > 0 {
		plan.Steps = append(plan.Steps, PlanStep{ID: "pre_aggregation_filters", Kind: "FILTER"})
	}
	if len(document.GroupBy) > 0 {
		plan.Steps = append(plan.Steps, PlanStep{ID: "aggregation", Kind: "AGGREGATE", Fields: append([]string(nil), document.GroupBy...)})
	}
	if len(document.Having) > 0 {
		plan.Steps = append(plan.Steps, PlanStep{ID: "post_aggregation_filters", Kind: "HAVING"})
	}
	if len(document.Sorts) > 0 {
		fields := make([]string, 0, len(document.Sorts))
		for _, item := range document.Sorts {
			fields = append(fields, item.FieldID+":"+item.Direction)
		}
		plan.Steps = append(plan.Steps, PlanStep{ID: "sort", Kind: "SORT", Fields: fields})
	}
	for _, field := range document.Fields {
		plan.OutputFields = append(plan.OutputFields, field.Code)
	}
	for _, parameter := range document.Parameters {
		plan.ParameterCodes = append(plan.ParameterCodes, parameter.Code)
	}
	return plan
}

// normalize 统一空白、枚举大小写、nil 数组和执行策略默认值。
func normalize(document Document) Document {
	// normalize 只做不会改变业务含义的规范化和默认值填充。任何引用完整性或
	// 业务合法性判断都留给 Validate，避免“修复”一个本应被用户看到的错误。
	document.Dataset.Code = strings.TrimSpace(document.Dataset.Code)
	document.Dataset.Name = strings.TrimSpace(document.Dataset.Name)
	document.Dataset.Description = strings.TrimSpace(document.Dataset.Description)
	document.Dataset.Type = upper(document.Dataset.Type)
	document.Dataset.Layer = Layer(upper(string(document.Dataset.Layer)))
	document.Dataset.SemanticContractVersion = strings.TrimSpace(document.Dataset.SemanticContractVersion)
	document.Dataset.ConsumerContractID = strings.TrimSpace(document.Dataset.ConsumerContractID)
	for i := range document.Nodes {
		node := &document.Nodes[i]
		node.ID, node.Type, node.Alias = strings.TrimSpace(node.ID), upper(node.Type), strings.TrimSpace(node.Alias)
		node.DataSourceID, node.TableID = strings.TrimSpace(node.DataSourceID), strings.TrimSpace(node.TableID)
		node.DatasetVersionID, node.FileVersionID = strings.TrimSpace(node.DatasetVersionID), strings.TrimSpace(node.FileVersionID)
		for j := range node.Projection {
			node.Projection[j] = strings.TrimSpace(node.Projection[j])
		}
		for j := range node.SourceFilters {
			node.SourceFilters[j].Field = strings.TrimSpace(node.SourceFilters[j].Field)
			node.SourceFilters[j].Operator = upper(node.SourceFilters[j].Operator)
			normalizeExpression(node.SourceFilters[j].Expression)
		}
	}
	for i := range document.Joins {
		join := &document.Joins[i]
		join.ID, join.LeftNodeID, join.RightNodeID = strings.TrimSpace(join.ID), strings.TrimSpace(join.LeftNodeID), strings.TrimSpace(join.RightNodeID)
		join.JoinType, join.Cardinality = upper(join.JoinType), upper(join.Cardinality)
		join.RelationshipType, join.RelationshipRole = upper(join.RelationshipType), upper(join.RelationshipRole)
		join.FanoutPolicy = upper(join.FanoutPolicy)
		if join.Bridge != nil {
			join.Bridge.BridgeNodeID = strings.TrimSpace(join.Bridge.BridgeNodeID)
			join.Bridge.RelationshipTypeField = strings.TrimSpace(join.Bridge.RelationshipTypeField)
			join.Bridge.AllocationWeightField = strings.TrimSpace(join.Bridge.AllocationWeightField)
			join.Bridge.PrimaryFlagField = strings.TrimSpace(join.Bridge.PrimaryFlagField)
			join.Bridge.ValidFromField = strings.TrimSpace(join.Bridge.ValidFromField)
			join.Bridge.ValidToField = strings.TrimSpace(join.Bridge.ValidToField)
		}
		if join.Temporal != nil {
			join.Temporal.EventNodeID = strings.TrimSpace(join.Temporal.EventNodeID)
			join.Temporal.EventTimeField = strings.TrimSpace(join.Temporal.EventTimeField)
			join.Temporal.ValidityNodeID = strings.TrimSpace(join.Temporal.ValidityNodeID)
			join.Temporal.ValidFromField = strings.TrimSpace(join.Temporal.ValidFromField)
			join.Temporal.ValidToField = strings.TrimSpace(join.Temporal.ValidToField)
		}
		for j := range join.Conditions {
			join.Conditions[j].Operator = upper(join.Conditions[j].Operator)
			normalizeExpression(&join.Conditions[j].LeftExpression)
			normalizeExpression(&join.Conditions[j].RightExpression)
		}
	}
	for i := range document.PreAggregations {
		item := &document.PreAggregations[i]
		item.ID, item.NodeID, item.JoinID, item.JoinSide = strings.TrimSpace(item.ID), strings.TrimSpace(item.NodeID), strings.TrimSpace(item.JoinID), upper(item.JoinSide)
		for j := range item.GroupBy {
			item.GroupBy[j].Field = strings.TrimSpace(item.GroupBy[j].Field)
			item.GroupBy[j].Unit = upper(item.GroupBy[j].Unit)
			normalizeExpression(item.GroupBy[j].Expression)
		}
		for j := range item.Metrics {
			item.Metrics[j].Field = strings.TrimSpace(item.Metrics[j].Field)
			item.Metrics[j].Function = upper(item.Metrics[j].Function)
			normalizeExpression(item.Metrics[j].Expression)
		}
	}
	if document.FactContract != nil {
		document.FactContract.BusinessAction = strings.TrimSpace(document.FactContract.BusinessAction)
		document.FactContract.EventTimeField = strings.TrimSpace(document.FactContract.EventTimeField)
		for i := range document.FactContract.GrainKeyFields {
			document.FactContract.GrainKeyFields[i] = strings.TrimSpace(document.FactContract.GrainKeyFields[i])
		}
		for i := range document.FactContract.AtomicMeasures {
			measure := &document.FactContract.AtomicMeasures[i]
			measure.Field = strings.TrimSpace(measure.Field)
			measure.Additivity = upper(measure.Additivity)
			measure.Unit = strings.TrimSpace(measure.Unit)
			measure.Currency = upper(measure.Currency)
			measure.NullPolicy = upper(measure.NullPolicy)
		}
	}
	if document.AnalysisContract != nil {
		contract := document.AnalysisContract
		contract.Intent = upper(contract.Intent)
		contract.InputMode = upper(contract.InputMode)
		contract.TimeField = strings.TrimSpace(contract.TimeField)
		contract.TimeGrain = upper(contract.TimeGrain)
		for i := range contract.CommonGrainFields {
			contract.CommonGrainFields[i] = strings.TrimSpace(contract.CommonGrainFields[i])
		}
		for i := range contract.ConformedDimensions {
			contract.ConformedDimensions[i] = strings.TrimSpace(contract.ConformedDimensions[i])
		}
		for i := range contract.Measures {
			measure := &contract.Measures[i]
			measure.Field = strings.TrimSpace(measure.Field)
			measure.Aggregation = upper(measure.Aggregation)
			measure.Additivity = upper(measure.Additivity)
			measure.Unit = strings.TrimSpace(measure.Unit)
			measure.Currency = upper(measure.Currency)
			for j := range measure.SourceNodeIDs {
				measure.SourceNodeIDs[j] = strings.TrimSpace(measure.SourceNodeIDs[j])
			}
		}
	}
	for i := range document.Parameters {
		parameter := &document.Parameters[i]
		parameter.Code, parameter.Name, parameter.DataType = strings.TrimSpace(parameter.Code), strings.TrimSpace(parameter.Name), upper(parameter.DataType)
	}
	for i := range document.Fields {
		field := &document.Fields[i]
		field.ID, field.Code, field.Name = strings.TrimSpace(field.ID), strings.TrimSpace(field.Code), strings.TrimSpace(field.Name)
		field.Description, field.Role, field.CanonicalType = strings.TrimSpace(field.Description), upper(field.Role), upper(field.CanonicalType)
		field.SemanticType, field.Aggregation = upper(field.SemanticType), upper(field.Aggregation)
		field.Format, field.Unit = strings.TrimSpace(field.Format), strings.TrimSpace(field.Unit)
		if field.Visible == nil {
			visible := true
			field.Visible = &visible
		}
		normalizeExpression(&field.Expression)
	}
	for i := range document.Filters {
		normalizeFilter(&document.Filters[i], "PRE_AGGREGATION")
	}
	for i := range document.Having {
		normalizeFilter(&document.Having[i], "POST_AGGREGATION")
	}
	for i := range document.GroupBy {
		document.GroupBy[i] = strings.TrimSpace(document.GroupBy[i])
	}
	for i := range document.Sorts {
		document.Sorts[i].FieldID = strings.TrimSpace(document.Sorts[i].FieldID)
		document.Sorts[i].Direction = upper(document.Sorts[i].Direction)
		document.Sorts[i].Nulls = upper(document.Sorts[i].Nulls)
	}
	document.OutputGrain.Description = strings.TrimSpace(document.OutputGrain.Description)
	for i := range document.OutputGrain.KeyFields {
		document.OutputGrain.KeyFields[i] = strings.TrimSpace(document.OutputGrain.KeyFields[i])
	}
	document.OutputGrain.TimeField = strings.TrimSpace(document.OutputGrain.TimeField)
	document.OutputGrain.DefaultTimeGrain = upper(document.OutputGrain.DefaultTimeGrain)
	document.ExecutionPolicy.Mode = upper(document.ExecutionPolicy.Mode)
	document.ExecutionPolicy.Materialization.RefreshMode = upper(document.ExecutionPolicy.Materialization.RefreshMode)
	document.ExecutionPolicy.Materialization.Cron = strings.TrimSpace(document.ExecutionPolicy.Materialization.Cron)
	if document.ExecutionPolicy.Mode == "" {
		document.ExecutionPolicy.Mode = "REALTIME"
	}
	if document.ExecutionPolicy.TimeoutMS == 0 {
		document.ExecutionPolicy.TimeoutMS = 5000
	}
	if document.ExecutionPolicy.PreviewLimit == 0 {
		document.ExecutionPolicy.PreviewLimit = 500
	}
	if document.ExecutionPolicy.ResultLimit == 0 {
		document.ExecutionPolicy.ResultLimit = 10000
	}
	if document.Dataset.Layer == "" && document.layerInferred {
		document.Dataset.Layer = InferLayer(document)
		document.inferredLayer = document.Dataset.Layer
	}
	// 空集合统一编码为 []，防止 nil 与空数组产生不同哈希。
	normalizeSlices(&document)
	return document
}

func normalizeSlices(document *Document) {
	// JSON 中 nil 切片编码为 null，空切片编码为 []。统一为 [] 后，语义相同的
	// 前端请求不会因为序列化差异得到不同的 DSL 哈希。
	if document.Nodes == nil {
		document.Nodes = []Node{}
	}
	if document.Joins == nil {
		document.Joins = []Join{}
	}
	if document.PreAggregations == nil {
		document.PreAggregations = []PreAggregation{}
	}
	if document.Fields == nil {
		document.Fields = []Field{}
	}
	if document.Filters == nil {
		document.Filters = []Filter{}
	}
	if document.GroupBy == nil {
		document.GroupBy = []string{}
	}
	if document.Having == nil {
		document.Having = []Filter{}
	}
	if document.Sorts == nil {
		document.Sorts = []Sort{}
	}
	if document.Parameters == nil {
		document.Parameters = []Parameter{}
	}
	if document.OutputGrain.KeyFields == nil {
		document.OutputGrain.KeyFields = []string{}
	}
	for i := range document.Nodes {
		if document.Nodes[i].Projection == nil {
			document.Nodes[i].Projection = []string{}
		}
		if document.Nodes[i].SourceFilters == nil {
			document.Nodes[i].SourceFilters = []SourceFilter{}
		}
	}
	for i := range document.Joins {
		if document.Joins[i].Conditions == nil {
			document.Joins[i].Conditions = []JoinCondition{}
		}
	}
	for i := range document.PreAggregations {
		if document.PreAggregations[i].GroupBy == nil {
			document.PreAggregations[i].GroupBy = []PreAggregationGroup{}
		}
		if document.PreAggregations[i].Metrics == nil {
			document.PreAggregations[i].Metrics = []PreAggregationMetric{}
		}
	}
	if document.FactContract != nil {
		if document.FactContract.GrainKeyFields == nil {
			document.FactContract.GrainKeyFields = []string{}
		}
		if document.FactContract.AtomicMeasures == nil {
			document.FactContract.AtomicMeasures = []AtomicMeasureContract{}
		}
	}
	if document.AnalysisContract != nil {
		if document.AnalysisContract.CommonGrainFields == nil {
			document.AnalysisContract.CommonGrainFields = []string{}
		}
		if document.AnalysisContract.ConformedDimensions == nil {
			document.AnalysisContract.ConformedDimensions = []string{}
		}
		if document.AnalysisContract.Measures == nil {
			document.AnalysisContract.Measures = []AnalysisMeasureContract{}
		}
		for i := range document.AnalysisContract.Measures {
			if document.AnalysisContract.Measures[i].SourceNodeIDs == nil {
				document.AnalysisContract.Measures[i].SourceNodeIDs = []string{}
			}
		}
	}
}

func normalizeFilter(filter *Filter, defaultStage string) {
	filter.ID = strings.TrimSpace(filter.ID)
	filter.Stage = upper(filter.Stage)
	if filter.Stage == "" {
		filter.Stage = defaultStage
	}
	normalizeExpression(&filter.Expression)
}

func normalizeExpression(expression *Expression) {
	// 表达式是递归树，新增子节点字段时必须同时扩展这里以及下方的校验和遍历函数，
	// 否则同一份 DSL 可能出现顶层已规范化、嵌套节点未规范化的情况。
	if expression == nil {
		return
	}
	expression.Type, expression.NodeID = upper(expression.Type), strings.TrimSpace(expression.NodeID)
	expression.Field, expression.Code = strings.TrimSpace(expression.Field), strings.TrimSpace(expression.Code)
	expression.Function, expression.Unit, expression.TargetType = upper(expression.Function), upper(expression.Unit), upper(expression.TargetType)
	normalizeExpression(expression.Argument)
	normalizeExpression(expression.Left)
	normalizeExpression(expression.Right)
	normalizeExpression(expression.Lower)
	normalizeExpression(expression.Upper)
	normalizeExpression(expression.Else)
	for i := range expression.Arguments {
		normalizeExpression(&expression.Arguments[i])
	}
	for i := range expression.Whens {
		normalizeExpression(&expression.Whens[i].When)
		normalizeExpression(&expression.Whens[i].Then)
	}
}

func validateJoinGraph(issues *[]ValidationIssue, nodes []Node, joins []Join) {
	// 领域层这里只验证忽略方向后的整体连通性，便于一次报告孤立节点。是否能严格
	// 按 left -> right 扩展、是否存在方向环，由编译器在不反转外连接的前提下拒绝。
	if len(nodes) <= 1 {
		return
	}
	adjacent := map[string][]string{}
	for _, join := range joins {
		adjacent[join.LeftNodeID] = append(adjacent[join.LeftNodeID], join.RightNodeID)
		adjacent[join.RightNodeID] = append(adjacent[join.RightNodeID], join.LeftNodeID)
	}
	seen := map[string]bool{nodes[0].ID: true}
	queue := []string{nodes[0].ID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacent[current] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}
	if len(seen) != len(nodes) {
		*issues = append(*issues, ValidationIssue{Path: "joins", Reason: "Join 图必须连接全部节点"})
	}
}

func validateProjectedFieldRefs(issues *[]ValidationIssue, document Document) {
	// 字段引用不仅要存在于节点，还必须包含在 projection 中。projection 是运行时
	// 从物理白名单读取的最小列集合，绕过它会让表达式访问未经授权或未加载的列。
	projections := map[string]map[string]bool{}
	for _, node := range document.Nodes {
		projections[node.ID] = map[string]bool{}
		for _, field := range node.Projection {
			projections[node.ID][field] = true
		}
	}
	// 关联前分组会把字段处理表达式物化为安全标识符别名。后续 Join 和最终字段
	// 引用的是派生表输出，因此需要在校验下游表达式前把这些别名加入可用集合；
	// 源表达式本身已在主校验流程中严格按原始 projection 检查。
	for _, item := range document.PreAggregations {
		for _, group := range item.GroupBy {
			projections[item.NodeID][group.Field] = true
		}
		for _, metric := range item.Metrics {
			projections[item.NodeID][metric.Field] = true
		}
	}
	for nodeIndex, node := range document.Nodes {
		for i, filter := range node.SourceFilters {
			if filter.Expression != nil {
				path := fmt.Sprintf("nodes[%d].sourceFilters[%d].expression", nodeIndex, i)
				validateExpressionProjection(issues, path, *filter.Expression, projections)
			}
		}
	}
	for i, join := range document.Joins {
		for j, condition := range join.Conditions {
			validateExpressionProjection(issues, fmt.Sprintf("joins[%d].conditions[%d].leftExpression", i, j), condition.LeftExpression, projections)
			validateExpressionProjection(issues, fmt.Sprintf("joins[%d].conditions[%d].rightExpression", i, j), condition.RightExpression, projections)
		}
	}
	for i, field := range document.Fields {
		validateExpressionProjection(issues, fmt.Sprintf("fields[%d].expression", i), field.Expression, projections)
	}
	for i, filter := range document.Filters {
		validateExpressionProjection(issues, fmt.Sprintf("filters[%d].expression", i), filter.Expression, projections)
	}
	for i, filter := range document.Having {
		validateExpressionProjection(issues, fmt.Sprintf("having[%d].expression", i), filter.Expression, projections)
	}
}

func validateExpressionProjection(issues *[]ValidationIssue, path string, expression Expression, projections map[string]map[string]bool) {
	// 递归检查所有 FIELD_REF，防止引用藏在 CASE、算术或逻辑表达式的深层节点中。
	if expression.Type == "FIELD_REF" && projections[expression.NodeID] != nil && !projections[expression.NodeID][expression.Field] {
		*issues = append(*issues, ValidationIssue{Path: path + ".field", Reason: "引用字段必须包含在节点 projection 中"})
	}
	for _, child := range []*Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil {
			validateExpressionProjection(issues, path, *child, projections)
		}
	}
	for _, child := range expression.Arguments {
		validateExpressionProjection(issues, path, child, projections)
	}
	for _, branch := range expression.Whens {
		validateExpressionProjection(issues, path, branch.When, projections)
		validateExpressionProjection(issues, path, branch.Then, projections)
	}
}

func validateFilter(issues *[]ValidationIssue, path string, filter Filter, expectedStage string, nodes, parameters map[string]bool) {
	validateIdentifier(issues, path+".id", filter.ID)
	if filter.Stage != expectedStage {
		*issues = append(*issues, ValidationIssue{Path: path + ".stage", Reason: "过滤阶段与所在集合不一致"})
	}
	validateExpression(issues, path+".expression", filter.Expression, nodes, parameters)
	if expectedStage == "PRE_AGGREGATION" && expressionHasAggregate(filter.Expression) {
		*issues = append(*issues, ValidationIssue{
			Path: path + ".expression", Reason: "聚合前过滤不能包含聚合表达式",
		})
	}
}

func validateExpression(issues *[]ValidationIssue, path string, expression Expression, nodes, parameters map[string]bool) {
	// 此处校验的是可执行表达式白名单和结构形状；具体数据库方言支持情况由安全
	// 编译器再次失败关闭。领域层先拒绝任意函数名，避免 DSL 退化为 SQL 载体。
	add := func(suffix, reason string) {
		*issues = append(*issues, ValidationIssue{Path: path + suffix, Reason: reason})
	}
	switch expression.Type {
	case "FIELD_REF":
		if nodes != nil && !nodes[expression.NodeID] {
			add(".nodeId", "引用的节点不存在")
		}
		if expression.Field == "" {
			add(".field", "字段引用不能为空")
		}
	case "PARAM_REF":
		if parameters != nil && !parameters[expression.Code] {
			add(".code", "引用的参数不存在")
		}
	case "LITERAL":
		// Literal 允许显式 null，因此仅依赖 type 即可表达。
	case "ARRAY":
		if len(expression.Arguments) == 0 {
			add(".arguments", "数组表达式至少需要一个元素")
		}
		for i := range expression.Arguments {
			validateExpression(issues, fmt.Sprintf("%s.arguments[%d]", path, i), expression.Arguments[i], nodes, parameters)
		}
	case "DATE_TRUNC":
		if !oneOf(expression.Unit, "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
			add(".unit", "不支持的日期粒度")
		}
		validateRequiredExpression(issues, path+".argument", expression.Argument, nodes, parameters)
	case "DATE_FORMAT":
		if !oneOf(expression.Unit, "DAY", "MONTH", "QUARTER", "YEAR") {
			add(".unit", "不支持的日期输出格式")
		}
		validateRequiredExpression(issues, path+".argument", expression.Argument, nodes, parameters)
	case "AGGREGATE":
		if !oneOf(expression.Function, "SUM", "AVG", "MIN", "MAX", "COUNT", "COUNT_DISTINCT") {
			add(".function", "不支持的聚合函数")
		}
		if expression.Function != "COUNT" || expression.Argument != nil {
			validateRequiredExpression(issues, path+".argument", expression.Argument, nodes, parameters)
		}
	case "CAST":
		if !oneOf(expression.TargetType, "STRING", "INTEGER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME") {
			add(".targetType", "不支持的目标类型")
		}
		validateRequiredExpression(issues, path+".argument", expression.Argument, nodes, parameters)
	case "TRIM", "UPPER", "LOWER":
		validateRequiredExpression(issues, path+".argument", expression.Argument, nodes, parameters)
	case "ABS", "FLOOR", "CEIL":
		validateRequiredExpression(issues, path+".argument", expression.Argument, nodes, parameters)
	case "ROUND":
		if len(expression.Arguments) != 2 {
			add(".arguments", "ROUND 必须包含数值和保留小数位两个参数")
		}
		for i := range expression.Arguments {
			validateExpression(issues, fmt.Sprintf("%s.arguments[%d]", path, i), expression.Arguments[i], nodes, parameters)
		}
		if len(expression.Arguments) == 2 {
			precision, ok := expressionLiteralInteger(expression.Arguments[1])
			if !ok || precision < -10 || precision > 10 {
				add(".arguments[1]", "保留小数位必须是 -10 到 10 的整数字面量")
			}
		}
	case "SUBSTRING":
		if len(expression.Arguments) != 3 {
			add(".arguments", "SUBSTRING 必须包含文本、起始位置和长度三个参数")
		}
		for i := range expression.Arguments {
			validateExpression(issues, fmt.Sprintf("%s.arguments[%d]", path, i), expression.Arguments[i], nodes, parameters)
		}
		if len(expression.Arguments) == 3 {
			start, startOK := expressionLiteralInteger(expression.Arguments[1])
			length, lengthOK := expressionLiteralInteger(expression.Arguments[2])
			if !startOK || start < 1 {
				add(".arguments[1]", "截取起始位置必须是大于等于 1 的整数字面量")
			}
			if !lengthOK || length < 0 {
				add(".arguments[2]", "截取长度必须是大于等于 0 的整数字面量")
			}
		}
	case "REPLACE":
		if len(expression.Arguments) != 3 {
			add(".arguments", "REPLACE 必须包含文本、查找文本和替换文本三个参数")
		}
		for i := range expression.Arguments {
			validateExpression(issues, fmt.Sprintf("%s.arguments[%d]", path, i), expression.Arguments[i], nodes, parameters)
		}
		if len(expression.Arguments) == 3 {
			search, searchOK := expressionLiteralString(expression.Arguments[1])
			if !searchOK || search == "" {
				add(".arguments[1]", "查找文本必须是非空字符串字面量")
			}
			if _, replacementOK := expressionLiteralString(expression.Arguments[2]); !replacementOK {
				add(".arguments[2]", "替换文本必须是字符串字面量")
			}
		}
	case "ADD", "SUBTRACT", "MULTIPLY", "DIVIDE", "CONCAT", "COALESCE", "AND", "OR":
		if len(expression.Arguments) < 2 {
			add(".arguments", "至少需要两个参数")
		}
		for i := range expression.Arguments {
			validateExpression(issues, fmt.Sprintf("%s.arguments[%d]", path, i), expression.Arguments[i], nodes, parameters)
		}
	case "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "LIKE", "CONTAINS", "NOT_CONTAINS", "IN", "NOT_IN":
		validateRequiredExpression(issues, path+".left", expression.Left, nodes, parameters)
		validateRequiredExpression(issues, path+".right", expression.Right, nodes, parameters)
	case "BETWEEN":
		validateRequiredExpression(issues, path+".left", expression.Left, nodes, parameters)
		validateRequiredExpression(issues, path+".lower", expression.Lower, nodes, parameters)
		validateRequiredExpression(issues, path+".upper", expression.Upper, nodes, parameters)
	case "IS_NULL", "IS_NOT_NULL", "NOT":
		validateRequiredExpression(issues, path+".argument", expression.Argument, nodes, parameters)
	case "CASE":
		if len(expression.Whens) == 0 {
			add(".whens", "至少需要一个 CASE 分支")
		}
		for i, branch := range expression.Whens {
			validateExpression(issues, fmt.Sprintf("%s.whens[%d].when", path, i), branch.When, nodes, parameters)
			validateExpression(issues, fmt.Sprintf("%s.whens[%d].then", path, i), branch.Then, nodes, parameters)
		}
		if expression.Else != nil {
			validateExpression(issues, path+".else", *expression.Else, nodes, parameters)
		}
	default:
		add(".type", "不支持的表达式类型")
	}
}

func expressionLiteralInteger(expression Expression) (int64, bool) {
	if expression.Type != "LITERAL" {
		return 0, false
	}
	switch value := expression.Value.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < math.MinInt64 || value > math.MaxInt64 {
			return 0, false
		}
		return int64(value), true
	case int:
		return int64(value), true
	case int64:
		return value, true
	case json.Number:
		result, err := value.Int64()
		return result, err == nil
	default:
		return 0, false
	}
}

func expressionLiteralString(expression Expression) (string, bool) {
	if expression.Type != "LITERAL" {
		return "", false
	}
	value, ok := expression.Value.(string)
	return value, ok
}

func validateRequiredExpression(issues *[]ValidationIssue, path string, expression *Expression, nodes, parameters map[string]bool) {
	if expression == nil {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "表达式不能为空"})
		return
	}
	validateExpression(issues, path, *expression, nodes, parameters)
}

func validateExecutionPolicy(issues *[]ValidationIssue, policy ExecutionPolicy) {
	// 执行上限属于 DSL 的安全契约，运行时只允许进一步收紧，不能由预览请求放大。
	add := func(path, reason string) { *issues = append(*issues, ValidationIssue{Path: path, Reason: reason}) }
	if !oneOf(policy.Mode, "REALTIME", "CACHE_PREFERRED", "MATERIALIZED_PREFERRED") {
		add("executionPolicy.mode", "不支持的执行模式")
	}
	if policy.TimeoutMS < 100 || policy.TimeoutMS > 120000 {
		add("executionPolicy.timeoutMs", "必须在 100 到 120000 之间")
	}
	if policy.PreviewLimit < 1 || policy.PreviewLimit > 5000 {
		add("executionPolicy.previewLimit", "必须在 1 到 5000 之间")
	}
	if policy.ResultLimit < 1 || policy.ResultLimit > 100000 {
		add("executionPolicy.resultLimit", "必须在 1 到 100000 之间")
	}
	if policy.CacheTTLSeconds < 0 || policy.CacheTTLSeconds > 86400 {
		add("executionPolicy.cacheTtlSeconds", "必须在 0 到 86400 之间")
	}
	if policy.Materialization.Enabled && !oneOf(policy.Materialization.RefreshMode, "MANUAL", "SCHEDULED", "ON_DEMAND") {
		add("executionPolicy.materialization.refreshMode", "启用物化时必须声明刷新模式")
	}
	if policy.Materialization.RefreshMode == "SCHEDULED" && policy.Materialization.Cron == "" {
		add("executionPolicy.materialization.cron", "定时物化必须提供 Cron")
	}
}

func validateIdentifier(issues *[]ValidationIssue, path, value string) {
	if !identifierPattern.MatchString(value) {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "必须以字母开头且只能包含字母、数字和下划线，长度不超过 128"})
	}
}

func validatePhysicalIdentifier(issues *[]ValidationIssue, path, value string) {
	if !physicalIdentifierPattern.MatchString(value) {
		*issues = append(*issues, ValidationIssue{Path: path, Reason: "必须是查询引擎支持的物理字段名"})
	}
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func upper(value string) string { return strings.ToUpper(strings.TrimSpace(value)) }

func hashJSON(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func ensureJSONEOF(decoder *json.Decoder) error {
	// Decoder.Decode 默认会忽略首个 JSON 值之后的内容，显式检查 EOF 可拒绝
	// 拼接的第二份文档，避免网关、审计和业务层对请求边界产生不同理解。
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("DSL must contain exactly one JSON document")
	}
	return err
}

// SortedDependencies 返回稳定排序的上游引用，供仓储派生血缘索引。
func SortedDependencies(document Document) []DependencyRef {
	// 使用复合键去重是为了让同一上游在多个节点中出现时只生成一条血缘边；最终
	// 排序则保证数据库重建索引和测试快照不受 map 遍历顺序影响。
	seen := map[string]DependencyRef{}
	for _, node := range document.Nodes {
		var refs []DependencyRef
		if node.TableID != "" {
			refs = append(refs, DependencyRef{Type: "TABLE", ID: node.TableID})
		}
		if node.FileVersionID != "" {
			refs = append(refs, DependencyRef{Type: "FILE_VERSION", ID: node.FileVersionID})
		}
		if node.DatasetVersionID != "" {
			refs = append(refs, DependencyRef{Type: "DATASET_VERSION", ID: node.DatasetVersionID})
		}
		for _, ref := range refs {
			seen[ref.Type+":"+ref.ID] = ref
		}
	}
	out := make([]DependencyRef, 0, len(seen))
	for _, ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Type == out[j].Type {
			return out[i].ID < out[j].ID
		}
		return out[i].Type < out[j].Type
	})
	return out
}

// DependencyRef 表示由 DSL 派生的上游对象引用。
type DependencyRef struct {
	Type string
	ID   string
}
