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
const ExtractorVersion = "metric-candidate-v1"

var ErrInvalidDatasetVersion = errors.New("metric candidate extraction requires an exact published dataset version")

const (
	BlockReasonAggregatedDataset   = "AGGREGATED_DATASET_UNSUPPORTED"
	BlockReasonPreAggregation      = "PRE_AGGREGATION_UNSUPPORTED"
	BlockReasonAggregateExpression = "AGGREGATE_EXPRESSION_UNSUPPORTED"
)

var supportedAggregations = map[string]bool{
	"SUM": true, "AVG": true, "MIN": true, "MAX": true, "COUNT": true, "COUNT_DISTINCT": true,
}

// Extract derives one candidate for every numeric output field in an exact immutable dataset
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

	for index, field := range document.Fields {
		if field.CanonicalType != "INTEGER" && field.CanonicalType != "DECIMAL" {
			continue
		}
		candidate, err := buildCandidate(version, document, field, index, dimensions, timeFieldID, timeGrain, globalBlocks, timeWarnings)
		if err != nil {
			return result, err
		}
		if candidate.Status != CandidateStatusReady {
			partial = true
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	if len(result.Candidates) == 0 {
		result.Warnings = append(result.Warnings, "数据集发布版本没有可提取的数值输出字段。")
	}
	result.Status = TaskStatusSucceeded
	if partial {
		result.Status = TaskStatusPartial
	}
	return result, nil
}

func buildCandidate(
	version dataset.VersionRecord,
	document dataset.Document,
	field dataset.Field,
	fieldIndex int,
	dimensions []metric.Dimension,
	timeFieldID, timeGrain string,
	globalBlocks, timeWarnings []string,
) (CandidateDraft, error) {
	aggregation, confidence, status, ruleEvidence, warnings := classifyAggregation(field, fieldIndex)
	blockReasons := append([]string{}, globalBlocks...)
	if expressionContainsAggregate(field.Expression) && !containsString(blockReasons, BlockReasonAggregateExpression) {
		blockReasons = append(blockReasons, BlockReasonAggregateExpression)
	}
	if len(blockReasons) > 0 {
		status = CandidateStatusBlocked
	}
	warnings = append(warnings, timeWarnings...)
	if field.Visible != nil && !*field.Visible {
		warnings = append(warnings, "源字段在数据集输出中不可见，应用候选前需要复核展示范围。")
	}

	unit := strings.TrimSpace(field.Unit)
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
			Code:        metricCode(document.Dataset.Code, field.Code, aggregation),
			Name:        safeText(field.Name, field.Code, 200),
			Description: candidateDescription(document, field),
			Type:        "ATOMIC",
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
		{Code: "NUMERIC_OUTPUT_FIELD", Path: fmt.Sprintf("dsl.fields[%d].canonicalType", fieldIndex), Value: field.CanonicalType},
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
	reasons := []string{}
	if len(document.GroupBy) > 0 || len(document.Having) > 0 {
		reasons = append(reasons, BlockReasonAggregatedDataset)
	}
	if len(document.PreAggregations) > 0 {
		reasons = append(reasons, BlockReasonPreAggregation)
	}
	for _, field := range document.Fields {
		if expressionContainsAggregate(field.Expression) {
			if !containsString(reasons, BlockReasonAggregateExpression) {
				reasons = append(reasons, BlockReasonAggregateExpression)
			}
			break
		}
	}
	return reasons
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
	return safeText(fmt.Sprintf("基于数据集“%s”的字段“%s”确定性提取的指标候选。", document.Dataset.Name, field.Name), "指标候选", 2000)
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
