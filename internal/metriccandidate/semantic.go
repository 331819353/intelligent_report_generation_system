package metriccandidate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

const MetricEnrichmentPromptVersion = "metric-candidate-enrichment-v1"

func attachDefaultSemantics(version dataset.VersionRecord, result ExtractionResult) ExtractionResult {
	fixedFilters, optionalFilters := datasetFilterScope(version)
	for index := range result.Candidates {
		draft := &result.Candidates[index]
		definition := draft.Definition
		dimensions := make([]string, 0, len(definition.AllowedDimensions))
		dimensionIDs := make([]string, 0, len(definition.AllowedDimensions))
		for _, dimension := range definition.AllowedDimensions {
			dimensions = append(dimensions, dimension.Name)
			dimensionIDs = append(dimensionIDs, dimension.FieldID)
		}
		period := definition.TimeGrain
		if period == "" {
			period = "NONE"
		}
		caliber := fmt.Sprintf("基于字段 %s，按 %s 聚合；空值处理为 %s", draft.SourceFieldCode, definition.Aggregation, definition.NullHandling)
		if definition.Unit != "" {
			caliber += "；单位为 " + definition.Unit
		}
		if fixedFilters > 0 {
			caliber += fmt.Sprintf("；继承数据集的 %d 个固定过滤条件", fixedFilters)
		}
		if optionalFilters > 0 {
			caliber += fmt.Sprintf("；另有 %d 个运行时可选过滤条件，不属于指标固定口径", optionalFilters)
		}
		lineage := LineageMetadata{
			DatasetID: version.DatasetID, DatasetVersionID: version.ID, SourceFieldID: draft.SourceFieldID,
			Aggregation: definition.Aggregation, DimensionFieldIDs: dimensionIDs,
			DependencyMetricVersionIDs: append([]string(nil), definitionDependencyIDs(definition.Expression)...),
		}
		lineageSummary := fmt.Sprintf("来自发布数据集“%s”的字段 %s，按 %s 聚合", versionName(version), draft.SourceFieldCode, definition.Aggregation)
		tags := []string{definition.Metric.Name, definition.Metric.Code, "原子指标", definition.Aggregation}
		if fixedFilters > 0 {
			tags = append(tags, "固定过滤口径")
		}
		if definition.Unit != "" {
			tags = append(tags, definition.Unit)
		}
		if period != "NONE" {
			tags = append(tags, period)
		}
		tags = append(tags, dimensions...)
		draft.Semantic = SemanticMetadata{
			Name: definition.Metric.Name, Description: definition.Metric.Description, Caliber: caliber,
			Dimensions: nonEmptyUnique(dimensions, 32, 100), Period: period, PeriodDescription: periodDescription(period),
			Lineage: lineage, LineageSummary: lineageSummary, Tags: nonEmptyUnique(tags, 16, 32),
			Source: "RULE", PromptVersion: MetricEnrichmentPromptVersion,
		}
		draft.Semantic.InputHash = semanticInputHash(*draft, version)
	}
	return result
}

func datasetFilterScope(version dataset.VersionRecord) (fixed, optional int) {
	prepared, err := dataset.Prepare(version.DSL)
	if err != nil {
		return 0, 0
	}
	for _, node := range prepared.Document.Nodes {
		fixed += len(node.SourceFilters)
	}
	for _, filter := range append(append([]dataset.Filter(nil), prepared.Document.Filters...), prepared.Document.Having...) {
		if filter.Optional {
			optional++
		} else {
			fixed++
		}
	}
	return fixed, optional
}

func versionName(version dataset.VersionRecord) string {
	prepared, err := dataset.Prepare(version.DSL)
	if err == nil && strings.TrimSpace(prepared.Document.Dataset.Name) != "" {
		return prepared.Document.Dataset.Name
	}
	return version.DatasetID
}

func periodDescription(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DAY":
		return "按日"
	case "WEEK":
		return "按周"
	case "MONTH":
		return "按月"
	case "QUARTER":
		return "按季度"
	case "YEAR":
		return "按年"
	default:
		return "无固定统计周期"
	}
}

func definitionDependencyIDs(expression metric.Expression) []string {
	seen := map[string]bool{}
	var visit func(metric.Expression)
	visit = func(value metric.Expression) {
		if value.MetricVersionID != "" {
			seen[value.MetricVersionID] = true
		}
		for _, child := range value.Arguments {
			visit(child)
		}
	}
	visit(expression)
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	return sortedStrings(values)
}

// EmbeddingDocument is deterministic and excludes source samples, credentials and physical SQL.
func EmbeddingDocument(value SemanticMetadata) string {
	parts := []string{
		"指标名称：" + value.Name,
		"指标说明：" + value.Description,
		"统计口径：" + value.Caliber,
		"分析维度：" + strings.Join(value.Dimensions, "、"),
		"统计周期：" + value.PeriodDescription + "（" + value.Period + "）",
		"数据血缘：" + value.LineageSummary,
		"检索标签：" + strings.Join(value.Tags, "、"),
	}
	return strings.Join(parts, "\n")
}

func semanticInputHash(draft CandidateDraft, version dataset.VersionRecord) string {
	payload := struct {
		DatasetID, DatasetVersionID, DSLHash, Fingerprint string
		Semantic                                          SemanticMetadata
	}{version.DatasetID, version.ID, version.DSLHash, draft.Fingerprint, draft.Semantic}
	payload.Semantic.InputHash = ""
	payload.Semantic.RequestID = ""
	payload.Semantic.ErrorCode = ""
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func nonEmptyUnique(values []string, maximum, maxRunes int) []string {
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
		if value == "" || len([]rune(value)) > maxRunes || hasControl(value) {
			continue
		}
		key := strings.ToLower(value)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
		if len(result) >= maximum {
			break
		}
	}
	return result
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
