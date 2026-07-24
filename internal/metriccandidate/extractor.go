package metriccandidate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

// ExtractorVersion identifies the deterministic rule contract used for job deduplication.
// Changing extraction semantics requires a new value so exact dataset versions can be
// reconciled again without rewriting prior audit evidence.
const ExtractorVersion = "metric-candidate-v4"

// JobVersion advances the durable publication workflow whenever extraction or semantic
// enrichment changes, while keeping prior jobs and audit evidence immutable.
const JobVersion = "metric-candidate-semantic-v5"

var ErrInvalidDatasetVersion = errors.New("metric candidate extraction requires an exact published dataset version")

const (
	BlockReasonAggregatedDataset   = "AGGREGATED_DATASET_UNSUPPORTED"
	BlockReasonPreAggregation      = "PRE_AGGREGATION_UNSUPPORTED"
	BlockReasonAggregateExpression = "AGGREGATE_EXPRESSION_UNSUPPORTED"
	BlockReasonDatasetUnavailable  = "DATASET_DEPENDENCY_UNAVAILABLE"
)

var supportedAggregations = map[string]bool{
	"SUM": true, "AVG": true, "MIN": true, "MAX": true, "COUNT": true, "COUNT_DISTINCT": true,
}

// Extract derives every structurally supportable atomic candidate from an exact immutable dataset
// version. It never reads mutable pointers, calls an LLM, persists state, or publishes metrics.
func Extract(version dataset.VersionRecord) (ExtractionResult, error) {
	result := ExtractionResult{
		Status: TaskStatusFailed, DatasetID: version.DatasetID, DatasetVersionID: version.ID,
		DSLHash: version.DSLHash, Candidates: []CandidateDraft{}, Warnings: []string{},
	}
	if version.Status != "PUBLISHED" || !canonicalUUID(version.DatasetID) || !canonicalUUID(version.ID) ||
		version.DSLHash == "" || version.PlanHash == "" {
		return result, ErrInvalidDatasetVersion
	}
	preparedDataset, err := dataset.Prepare(version.DSL)
	if err != nil {
		return result, fmt.Errorf("%w: invalid dataset DSL: %v", ErrInvalidDatasetVersion, err)
	}
	if preparedDataset.DSLHash != version.DSLHash || preparedDataset.PlanHash != version.PlanHash {
		return result, fmt.Errorf("%w: dataset hashes do not match the immutable envelope", ErrInvalidDatasetVersion)
	}

	document := preparedDataset.Document
	dimensions := extractDimensions(document.Fields)
	timeFieldID, timeGrain, timeWarnings := extractTimeSemantics(document, dimensions)
	globalBlocks := datasetBlockReasons(document)
	partial := false
	detailCompatible := len(document.GroupBy) == 0 && len(document.Having) == 0
	for _, field := range document.Fields {
		if expressionContainsAggregate(field.Expression) {
			detailCompatible = false
			break
		}
	}
	rules := deriveCandidateRules(document, detailCompatible)

	for _, rule := range rules {
		candidate, err := buildCandidate(version, document, rule, dimensions, timeFieldID, timeGrain, globalBlocks, timeWarnings)
		if err != nil {
			return result, err
		}
		if candidate.Status != CandidateStatusReady {
			partial = true
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	if len(result.Candidates) == 0 {
		result.Warnings = append(result.Warnings, "数据集发布版本没有可由输出粒度、标识符、数值字段或聚合输出确定的指标候选。")
	} else if detailCompatible && len(globalBlocks) == 0 && !hasRecordCountRule(rules) {
		result.Warnings = append(result.Warnings, "数据集没有非空输出字段，无法安全生成明细记录数候选。")
	}
	result.Status = TaskStatusSucceeded
	if partial {
		result.Status = TaskStatusPartial
	}
	return result, nil
}

type candidateRule struct {
	Field       dataset.Field
	FieldIndex  int
	Aggregation string
	Confidence  Confidence
	Status      CandidateStatus
	Name        string
	Description string
	CodeSeed    string
	Evidence    []Evidence
	Warnings    []string
	Kind        string
	MetricType  string
	// BusinessAggregation keeps the aggregation already executed by an aggregate DAG
	// output. The metric definition uses NONE to avoid nesting the aggregation, while
	// descriptions, units and semantic caliber must still describe the real business
	// calculation.
	BusinessAggregation string
}

func deriveCandidateRules(document dataset.Document, detailCompatible bool) []candidateRule {
	rules := []candidateRule{}
	seen := map[string]bool{}
	add := func(rule candidateRule) {
		if rule.MetricType == "" {
			if len(document.Joins) > 0 || len(document.PreAggregations) > 0 {
				rule.MetricType = "DERIVED"
			} else {
				rule.MetricType = "ATOMIC"
			}
		}
		key := rule.Field.ID + "\x1f" + rule.Aggregation
		if seen[key] {
			return
		}
		seen[key] = true
		rules = append(rules, rule)
	}
	if !detailCompatible {
		for index, field := range document.Fields {
			aggregateFunction, ok := topLevelAggregateFunction(field.Expression)
			if !ok || field.Role != "MEASURE" {
				continue
			}
			add(candidateRule{
				Field: field, FieldIndex: index, Aggregation: "NONE",
				Confidence: ConfidenceHigh, Status: CandidateStatusReady,
				Name:        safeText(field.Name, field.Code, 200),
				Description: businessMetricDescription(document, field, aggregateFunction),
				CodeSeed:    field.Code,
				Evidence: []Evidence{{
					Code: "DAG_AGGREGATE_OUTPUT", Path: fmt.Sprintf("dsl.fields[%d].expression.function", index),
					Value: aggregateFunction,
				}},
				Kind: "DAG_DERIVED_METRIC", MetricType: "DERIVED", BusinessAggregation: aggregateFunction,
			})
		}
		return rules
	}
	if detailCompatible {
		if field, index, evidence, ok := recordCountField(document); ok {
			add(candidateRule{
				Field: field, FieldIndex: index, Aggregation: "COUNT", Confidence: ConfidenceHigh,
				Status: CandidateStatusReady, Name: safeText(document.Dataset.Name+"记录数", "记录数", 200),
				Description: safeText(fmt.Sprintf("统计%s的明细记录数量。", businessContextName(document)), "统计明细记录数量。", 2000),
				CodeSeed:    "record", Evidence: evidence, Kind: "RECORD_COUNT",
			})
		}
	}
	grainKeys := map[string]bool{}
	for _, code := range document.OutputGrain.KeyFields {
		grainKeys[strings.TrimSpace(code)] = true
	}
	for index, field := range document.Fields {
		isIdentifier := field.Role == "IDENTIFIER" || field.SemanticType == "IDENTIFIER" || grainKeys[field.Code]
		if isIdentifier && field.CanonicalType != "DATE" && field.CanonicalType != "DATETIME" && field.CanonicalType != "TIMESTAMP" {
			confidence := ConfidenceMedium
			evidenceCode := "IDENTIFIER_COUNT_DISTINCT"
			if grainKeys[field.Code] {
				confidence = ConfidenceHigh
				evidenceCode = "OUTPUT_GRAIN_KEY_COUNT_DISTINCT"
			}
			warnings := []string{}
			if field.Nullable {
				warnings = append(warnings, "标识符字段允许为空，去重计数会忽略空值。")
			}
			add(candidateRule{
				Field: field, FieldIndex: index, Aggregation: "COUNT_DISTINCT", Confidence: confidence,
				Status: CandidateStatusReady, Name: identifierCountName(field),
				Description: safeText(fmt.Sprintf("统计%s数量，重复%s只计算一次。", metricObjectName(field), metricObjectName(field)), "统计去重业务对象数量。", 2000),
				CodeSeed:    field.Code, Evidence: []Evidence{{Code: evidenceCode, Path: fmt.Sprintf("dsl.fields[%d]", index), Value: field.Code}},
				Warnings: warnings, Kind: "IDENTIFIER_COUNT",
			})
		}

		isNumeric := field.CanonicalType == "INTEGER" || field.CanonicalType == "DECIMAL"
		isNumericFact := isNumeric && field.Role != "IDENTIFIER" && field.Role != "DIMENSION" && field.Role != "TIME" &&
			field.SemanticType != "IDENTIFIER" && !grainKeys[field.Code] &&
			(field.Role == "MEASURE" || field.Role == "ATTRIBUTE" || field.SemanticType == "AMOUNT" || field.SemanticType == "QUANTITY" || field.SemanticType == "PERCENTAGE")
		isExplicitCount := field.Role == "MEASURE" && (strings.EqualFold(field.Aggregation, "COUNT") || strings.EqualFold(field.Aggregation, "COUNT_DISTINCT"))
		if !isNumericFact && !isExplicitCount {
			continue
		}
		aggregation, confidence, status, evidence, warnings := classifyAggregation(field, index)
		add(candidateRule{
			Field: field, FieldIndex: index, Aggregation: aggregation, Confidence: confidence, Status: status,
			Name: safeText(field.Name, field.Code, 200), Description: candidateDescription(document, field),
			CodeSeed: field.Code, Evidence: evidence, Warnings: warnings, Kind: "VALUE_METRIC",
		})
	}
	return rules
}

func recordCountField(document dataset.Document) (dataset.Field, int, []Evidence, bool) {
	visibleAndNonNull := func(field dataset.Field) bool { return !field.Nullable && (field.Visible == nil || *field.Visible) }
	for _, code := range document.OutputGrain.KeyFields {
		for index, field := range document.Fields {
			if field.Code == strings.TrimSpace(code) && visibleAndNonNull(field) {
				return field, index, []Evidence{{Code: "RECORD_COUNT_FROM_GRAIN_KEY", Path: "dsl.outputGrain.keyFields", Value: field.Code}}, true
			}
		}
	}
	for index, field := range document.Fields {
		if visibleAndNonNull(field) && (field.Role == "IDENTIFIER" || field.SemanticType == "IDENTIFIER") {
			return field, index, []Evidence{{Code: "RECORD_COUNT_FROM_NONNULL_IDENTIFIER", Path: fmt.Sprintf("dsl.fields[%d].nullable", index), Value: field.Code + ":false"}}, true
		}
	}
	for index, field := range document.Fields {
		if visibleAndNonNull(field) {
			return field, index, []Evidence{{Code: "RECORD_COUNT_FROM_NONNULL_FIELD", Path: fmt.Sprintf("dsl.fields[%d].nullable", index), Value: field.Code + ":false"}}, true
		}
	}
	return dataset.Field{}, 0, nil, false
}

func hasRecordCountRule(rules []candidateRule) bool {
	for _, rule := range rules {
		if rule.Kind == "RECORD_COUNT" {
			return true
		}
	}
	return false
}

func identifierCountName(field dataset.Field) string {
	name := safeText(field.Name, field.Code, 180)
	for _, suffix := range []string{"标识符", "编号", "编码", "标识", "ID", "Id", "id"} {
		if trimmed := strings.TrimSpace(strings.TrimSuffix(name, suffix)); trimmed != "" && trimmed != name {
			return safeText(trimmed+"数", name+"去重数", 200)
		}
	}
	return safeText(name+"去重数", "实体去重数", 200)
}

func buildCandidate(
	version dataset.VersionRecord,
	document dataset.Document,
	rule candidateRule,
	dimensions []metric.Dimension,
	timeFieldID, timeGrain string,
	globalBlocks, timeWarnings []string,
) (CandidateDraft, error) {
	field, fieldIndex := rule.Field, rule.FieldIndex
	aggregation, confidence, status := rule.Aggregation, rule.Confidence, rule.Status
	ruleEvidence, warnings := append([]Evidence(nil), rule.Evidence...), append([]string(nil), rule.Warnings...)
	blockReasons := append([]string{}, globalBlocks...)
	if len(blockReasons) > 0 {
		status = CandidateStatusBlocked
	}
	warnings = append(warnings, timeWarnings...)
	grainWarnings := semanticGrainWarnings(document)
	warnings = append(warnings, grainWarnings...)
	if len(grainWarnings) > 0 && status == CandidateStatusReady {
		status = CandidateStatusNeedsReview
		if confidence == ConfidenceHigh {
			confidence = ConfidenceMedium
		}
	}
	if field.Visible != nil && !*field.Visible {
		warnings = append(warnings, "源字段在数据集输出中不可见，应用候选前需要复核展示范围。")
	}

	unit := strings.TrimSpace(field.Unit)
	businessAggregation := rule.BusinessAggregation
	if businessAggregation == "" {
		businessAggregation = aggregation
	}
	if unit == "" && (businessAggregation == "COUNT" || businessAggregation == "COUNT_DISTINCT") {
		unit = inferredCountUnit(field, rule.Kind)
	}
	if !validBoundedText(unit, 32) {
		unit = ""
		warnings = append(warnings, "源字段单位超出指标定义边界，候选未自动携带单位。")
	}
	numberFormat := strings.TrimSpace(field.Format)
	if numberFormat == "" || !validBoundedText(numberFormat, 64) {
		numberFormat = defaultNumberFormat(field, aggregation)
		if strings.TrimSpace(field.Format) != "" {
			warnings = append(warnings, "源字段格式超出指标定义边界，候选改用安全默认格式。")
		}
	}

	definition := metric.Definition{
		SchemaVersion: metric.DefinitionVersion,
		Metric: metric.Descriptor{
			Code:        metricCode(document.Dataset.Code, rule.CodeSeed, aggregation),
			Name:        rule.Name,
			Description: rule.Description,
			Type:        rule.MetricType,
		},
		DatasetID:                    version.DatasetID,
		DatasetVersionID:             version.ID,
		Expression:                   metric.Expression{Type: "FIELD_REF", FieldID: field.ID},
		Aggregation:                  aggregation,
		Unit:                         unit,
		NumberFormat:                 numberFormat,
		TimeFieldID:                  timeFieldID,
		TimeGrain:                    timeGrain,
		Additivity:                   defaultAdditivity(aggregation),
		NonAdditiveDimensionFieldIDs: []string{},
		AllowedDimensions:            cloneDimensions(dimensions),
		DecimalScale:                 defaultDecimalScale(field, aggregation),
		RoundingMode:                 "HALF_UP",
		NullHandling:                 "IGNORE",
		DivisionByZero:               "NULL",
	}
	definitionRaw, err := json.Marshal(definition)
	if err != nil {
		return CandidateDraft{}, err
	}
	preparedMetric, err := metric.Prepare(definitionRaw)
	if err != nil {
		return CandidateDraft{}, fmt.Errorf("derive metric candidate for field %s: %w", field.ID, err)
	}
	definition = preparedMetric.Definition

	evidence := []Evidence{
		{Code: "EXACT_DATASET_VERSION", Path: "datasetVersion.id", Value: version.ID},
		{Code: "DATASET_DSL_HASH", Path: "datasetVersion.dslHash", Value: version.DSLHash},
		{Code: "OUTPUT_FIELD_TYPE", Path: fmt.Sprintf("dsl.fields[%d].canonicalType", fieldIndex), Value: field.CanonicalType},
		{Code: "FIELD_ROLE", Path: fmt.Sprintf("dsl.fields[%d].role", fieldIndex), Value: field.Role},
		{Code: "FIELD_EXPRESSION", Path: fmt.Sprintf("dsl.fields[%d].expression", fieldIndex), Value: expressionEvidence(field.Expression)},
	}
	if field.SemanticType != "" {
		evidence = append(evidence, Evidence{Code: "FIELD_SEMANTIC_TYPE", Path: fmt.Sprintf("dsl.fields[%d].semanticType", fieldIndex), Value: field.SemanticType})
	}
	evidence = append(evidence, ruleEvidence...)
	if len(dimensions) > 0 {
		evidence = append(evidence, Evidence{Code: "EXPLICIT_DIMENSIONS", Path: "dsl.fields", Value: strings.Join(dimensionFieldIDs(dimensions), ",")})
	}
	if timeFieldID != "" {
		evidence = append(evidence, Evidence{Code: "EXACT_TIME_GRAIN", Path: "dsl.outputGrain", Value: timeFieldID + ":" + timeGrain})
	}

	return CandidateDraft{
		DatasetID: version.DatasetID, DatasetVersionID: version.ID,
		SourceFieldID: field.ID, SourceFieldCode: field.Code,
		Status: status, Confidence: confidence, Definition: definition,
		DefinitionHash: preparedMetric.DefinitionHash,
		Fingerprint:    candidateFingerprint(version, field.ID, preparedMetric.DefinitionHash),
		Evidence:       evidence, Warnings: nonNilStrings(warnings), BlockReasons: nonNilStrings(blockReasons),
	}, nil
}

func classifyAggregation(field dataset.Field, fieldIndex int) (string, Confidence, CandidateStatus, []Evidence, []string) {
	path := fmt.Sprintf("dsl.fields[%d]", fieldIndex)
	explicit := strings.ToUpper(strings.TrimSpace(field.Aggregation))
	warnings := []string{}
	if field.Role == "MEASURE" && supportedAggregations[explicit] {
		return explicit, ConfidenceHigh, CandidateStatusReady,
			[]Evidence{{Code: "EXPLICIT_MEASURE_AGGREGATION", Path: path + ".aggregation", Value: explicit}}, warnings
	}
	if field.Role == "MEASURE" && explicit != "" {
		warnings = append(warnings, "显式度量的 aggregation 不受指标定义支持，已按语义规则重新建议。")
	}
	switch field.SemanticType {
	case "AMOUNT", "QUANTITY":
		return "SUM", ConfidenceMedium, CandidateStatusReady,
			[]Evidence{{Code: "SEMANTIC_DEFAULT_AGGREGATION", Path: path + ".semanticType", Value: field.SemanticType + ":SUM"}}, warnings
	case "PERCENTAGE":
		return "AVG", ConfidenceMedium, CandidateStatusReady,
			[]Evidence{{Code: "SEMANTIC_DEFAULT_AGGREGATION", Path: path + ".semanticType", Value: "PERCENTAGE:AVG"}}, warnings
	}
	if field.Role != "MEASURE" && explicit != "" {
		warnings = append(warnings, "非度量字段上的 aggregation 不作为高置信事实，候选需要人工复核。")
	}
	return "SUM", ConfidenceLow, CandidateStatusNeedsReview,
		[]Evidence{{Code: "REVIEW_DEFAULT_AGGREGATION", Path: path + ".canonicalType", Value: field.CanonicalType + ":SUM"}}, warnings
}

func extractDimensions(fields []dataset.Field) []metric.Dimension {
	dimensions := []metric.Dimension{}
	for _, field := range fields {
		if field.Role != "DIMENSION" && field.Role != "TIME" {
			continue
		}
		dimensions = append(dimensions, metric.Dimension{
			FieldID: field.ID, Name: safeText(field.Name, field.Code, 200),
			HierarchyFieldIDs: []string{}, SortDirection: "ASC", NullLabel: "未分类",
		})
	}
	return dimensions
}

func extractTimeSemantics(document dataset.Document, dimensions []metric.Dimension) (string, string, []string) {
	timeCode := strings.TrimSpace(document.OutputGrain.TimeField)
	timeGrain := strings.ToUpper(strings.TrimSpace(document.OutputGrain.DefaultTimeGrain))
	if timeCode == "" && timeGrain == "" {
		return "", "NONE", []string{}
	}
	if timeCode == "" || timeGrain == "" {
		return "", "NONE", []string{"数据集时间字段与默认时间粒度未同时声明，候选未自动设置时间口径。"}
	}
	for _, field := range document.Fields {
		if field.Code != timeCode {
			continue
		}
		if field.Role != "TIME" || field.CanonicalType != "DATE" && field.CanonicalType != "DATETIME" || !dimensionExists(dimensions, field.ID) {
			return "", "NONE", []string{"数据集默认时间字段不是可执行的 DATE/DATETIME TIME 维度，候选未自动设置时间口径。"}
		}
		return field.ID, timeGrain, []string{}
	}
	return "", "NONE", []string{"数据集默认时间字段不在输出字段中，候选未自动设置时间口径。"}
}

func datasetBlockReasons(document dataset.Document) []string {
	// Grouped DAG outputs are valid derived metric sources. Their aggregate expressions are
	// reused with aggregation=NONE and must not be treated as atomic detail fields.
	reasons := []string{}
	if len(document.GroupBy) > 0 || len(document.Having) > 0 {
		return reasons
	}
	if len(document.PreAggregations) > 0 {
		return reasons
	}
	return reasons
}

func topLevelAggregateFunction(expression dataset.Expression) (string, bool) {
	if expression.Type != "AGGREGATE" {
		return "", false
	}
	function := strings.ToUpper(strings.TrimSpace(expression.Function))
	return function, supportedAggregations[function]
}

func expressionContainsAggregate(expression dataset.Expression) bool {
	if expression.Type == "AGGREGATE" {
		return true
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil && expressionContainsAggregate(*child) {
			return true
		}
	}
	for _, child := range expression.Arguments {
		if expressionContainsAggregate(child) {
			return true
		}
	}
	for _, branch := range expression.Whens {
		if expressionContainsAggregate(branch.When) || expressionContainsAggregate(branch.Then) {
			return true
		}
	}
	return false
}

func metricCode(datasetCode, fieldCode, aggregation string) string {
	seed := strings.ToLower(strings.TrimSpace(datasetCode) + "_" + strings.TrimSpace(fieldCode) + "_" + strings.ToLower(aggregation))
	var builder strings.Builder
	for _, char := range seed {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	value := strings.Trim(builder.String(), "_")
	if value == "" || value[0] < 'a' || value[0] > 'z' && value[0] < 'A' || value[0] > 'Z' {
		value = "metric_" + value
	}
	if len(value) <= 64 {
		return value
	}
	suffix := "_" + shortHash(seed, 12)
	return strings.TrimRight(value[:64-len(suffix)], "_") + suffix
}

func candidateDescription(document dataset.Document, field dataset.Field) string {
	if description := safeText(field.Description, "", 2000); description != "" {
		return description
	}
	return safeText(fmt.Sprintf("统计%s。", field.Name), "统计业务度量。", 2000)
}

func businessMetricDescription(document dataset.Document, field dataset.Field, aggregation string) string {
	contextName := businessContextName(document)
	objectName := metricObjectName(field)
	switch aggregation {
	case "COUNT", "COUNT_DISTINCT":
		if strings.HasSuffix(contextName, "金额") {
			contextName = strings.TrimSpace(strings.TrimSuffix(contextName, "金额"))
		}
		if contextName == "" {
			return safeText(fmt.Sprintf("统计%s数量。", objectName), "统计业务对象数量。", 2000)
		}
		return safeText(fmt.Sprintf("统计%s场景下的%s数量。", contextName, objectName), "统计业务对象数量。", 2000)
	case "SUM":
		return safeText(fmt.Sprintf("统计%s口径下的%s合计。", contextName, field.Name), "统计业务金额或数量合计。", 2000)
	case "AVG":
		return safeText(fmt.Sprintf("统计%s口径下的%s平均值。", contextName, field.Name), "统计业务度量平均值。", 2000)
	case "MIN":
		return safeText(fmt.Sprintf("统计%s口径下的%s最小值。", contextName, field.Name), "统计业务度量最小值。", 2000)
	case "MAX":
		return safeText(fmt.Sprintf("统计%s口径下的%s最大值。", contextName, field.Name), "统计业务度量最大值。", 2000)
	default:
		return candidateDescription(document, field)
	}
}

func businessContextName(document dataset.Document) string {
	contextName := safeText(document.Dataset.Name, "当前业务", 160)
	for _, suffix := range []string{"数据集", "明细表", "详情表", "明细", "详情"} {
		contextName = strings.TrimSpace(strings.TrimSuffix(contextName, suffix))
	}
	return contextName
}

func metricObjectName(field dataset.Field) string {
	name := safeText(field.Name, field.Code, 160)
	for _, suffix := range []string{"总数量", "数量", "总数", "去重数", "数"} {
		if trimmed := strings.TrimSpace(strings.TrimSuffix(name, suffix)); trimmed != "" && trimmed != name {
			return trimmed
		}
	}
	source := aggregateSourceField(field.Expression)
	for _, suffix := range []string{"_id", "_no", "_code", "Id", "ID", "编号", "编码"} {
		if trimmed := strings.TrimSpace(strings.TrimSuffix(source, suffix)); trimmed != "" && trimmed != source {
			return trimmed
		}
	}
	return name
}

func aggregateSourceField(expression dataset.Expression) string {
	if expression.Type == "AGGREGATE" && expression.Argument != nil {
		return strings.TrimSpace(expression.Argument.Field)
	}
	return strings.TrimSpace(expression.Field)
}

func inferredCountUnit(field dataset.Field, kind string) string {
	if kind == "RECORD_COUNT" {
		return "条"
	}
	search := strings.ToLower(strings.Join([]string{
		field.Name, field.Code, metricObjectName(field), aggregateSourceField(field.Expression),
	}, " "))
	transactionWords := []string{
		"订单", "交易", "支付", "付款", "退款", "发票", "账单", "结算", "流水", "工单",
		"order", "transaction", "payment", "refund", "invoice", "bill", "settlement", "ticket",
	}
	for _, word := range transactionWords {
		if strings.Contains(search, word) {
			return "笔"
		}
	}
	return "个"
}

func semanticGrainWarnings(document dataset.Document) []string {
	businessText := strings.Join([]string{
		document.Dataset.Name, document.Dataset.Description, document.OutputGrain.Description,
		strings.Join(designerBusinessHints(document.Designer), " "),
	}, " ")
	expected := ""
	switch {
	case strings.Contains(businessText, "每月") || strings.Contains(businessText, "月度") || strings.Contains(strings.ToLower(businessText), "monthly"):
		expected = "MONTH"
	case strings.Contains(businessText, "每日") || strings.Contains(businessText, "按日") || strings.Contains(strings.ToLower(businessText), "daily"):
		expected = "DAY"
	case strings.Contains(businessText, "每周") || strings.Contains(businessText, "周度") || strings.Contains(strings.ToLower(businessText), "weekly"):
		expected = "WEEK"
	case strings.Contains(businessText, "季度") || strings.Contains(strings.ToLower(businessText), "quarterly"):
		expected = "QUARTER"
	case strings.Contains(businessText, "每年") || strings.Contains(businessText, "年度") || strings.Contains(strings.ToLower(businessText), "yearly"):
		expected = "YEAR"
	}
	if expected == "" {
		return nil
	}
	actual := strings.ToUpper(strings.TrimSpace(document.OutputGrain.DefaultTimeGrain))
	if strings.TrimSpace(document.OutputGrain.TimeField) == "" || actual != expected {
		return []string{fmt.Sprintf("业务名称或粒度说明包含 %s 周期，但输出粒度未声明对应时间字段和时间粒度；LLM 不会把该周期写成已实现口径，接受候选前应复核 DAG。", expected)}
	}
	return nil
}

func defaultAdditivity(aggregation string) string {
	if aggregation == "SUM" || aggregation == "COUNT" {
		return "ADDITIVE"
	}
	return "NON_ADDITIVE"
}

func defaultDecimalScale(field dataset.Field, aggregation string) int {
	if aggregation == "COUNT" || aggregation == "COUNT_DISTINCT" {
		return 0
	}
	if field.CanonicalType == "INTEGER" && aggregation != "AVG" {
		return 0
	}
	return 2
}

func defaultNumberFormat(field dataset.Field, aggregation string) string {
	if defaultDecimalScale(field, aggregation) == 0 {
		return "#,##0"
	}
	return "#,##0.00"
}

func candidateFingerprint(version dataset.VersionRecord, fieldID, definitionHash string) string {
	payload := struct {
		ExtractorVersion string `json:"extractorVersion"`
		DatasetID        string `json:"datasetId"`
		DatasetVersionID string `json:"datasetVersionId"`
		DSLHash          string `json:"dslHash"`
		FieldID          string `json:"fieldId"`
		DefinitionHash   string `json:"definitionHash"`
	}{ExtractorVersion, version.DatasetID, version.ID, version.DSLHash, fieldID, definitionHash}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func expressionEvidence(expression dataset.Expression) string {
	raw, _ := json.Marshal(expression)
	return string(raw)
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validBoundedText(value string, maximum int) bool {
	if len([]rune(value)) > maximum {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func safeText(value, fallback string, maximum int) string {
	value = strings.TrimSpace(strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, value))
	if value == "" {
		value = fallback
	}
	runes := []rune(value)
	if len(runes) > maximum {
		value = strings.TrimSpace(string(runes[:maximum]))
	}
	return value
}

func cloneDimensions(values []metric.Dimension) []metric.Dimension {
	result := make([]metric.Dimension, len(values))
	for index, value := range values {
		result[index] = value
		result[index].HierarchyFieldIDs = append([]string{}, value.HierarchyFieldIDs...)
	}
	return result
}

func dimensionFieldIDs(values []metric.Dimension) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.FieldID
	}
	return result
}

func dimensionExists(values []metric.Dimension, fieldID string) bool {
	for _, value := range values {
		if value.FieldID == fieldID {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func shortHash(value string, length int) string {
	sum := sha256.Sum256([]byte(value))
	encoded := hex.EncodeToString(sum[:])
	return encoded[:length]
}
