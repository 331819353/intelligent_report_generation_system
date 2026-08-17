package registryimport

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// semantic-bundle/v1 是四分区语义资产的统一 JSON 导入合同：一个文件同时携带
// MODEL / METRIC / DIMENSION / KNOWLEDGE 四个分区的资产。上传后 Bundle 被
// 确定性地展开为既有的扁平导入行（每行一个 legacy assetType），从而完整复用
// 租约、四层校验、DRAFT 提交、撤回与审计。展开是纯函数：同一文件字节永远
// 产生同一行序列与行号，重试与断点续跑因此安全。
const (
	BundleContractVersion = "semantic-bundle/v1"

	BundleModeUpsert     = "UPSERT"
	BundleModeCreateOnly = "CREATE_ONLY"

	MaxBundleAssets = 20_000
)

// Bundle 分区名。分区与 legacy assetType 的映射在展开时完成。
const (
	BundleSectionModel     = "MODEL"
	BundleSectionMetric    = "METRIC"
	BundleSectionDimension = "DIMENSION"
	BundleSectionKnowledge = "KNOWLEDGE"
)

type SemanticBundle struct {
	Contract string          `json:"contract"`
	Source   json.RawMessage `json:"source,omitempty"`
	Options  BundleOptions   `json:"options"`
	Assets   []BundleAsset   `json:"assets"`
}

type BundleOptions struct {
	// Mode 控制身份解析策略：UPSERT（默认）允许更新既有 code；CREATE_ONLY
	// 在 code 已存在时把该行判为失败而不是创建新版本。
	Mode string `json:"mode,omitempty"`
}

// BundleAsset 是统一信封 + 分区专属 spec。信封承载稳定身份（section+code）
// 与可向量化的语义字段；spec 承载确定性执行所需的结构化事实。
type BundleAsset struct {
	Section     string          `json:"section"`
	Kind        string          `json:"kind"`
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Aliases     []string        `json:"aliases,omitempty"`
	Owner       string          `json:"owner,omitempty"`
	Spec        json.RawMessage `json:"spec"`
}

// bundleRowKeys 是展开行里除模板列以外的保留键：行级 assetType 分发、来源
// Bundle 资产下标（报表回溯）与批级导入模式。
const (
	bundleRowAssetTypeKey  = "assetType"
	bundleRowAssetIndexKey = "bundleAsset"
	bundleRowModeKey       = "bundleMode"
)

// ParseBundle 严格解析 semantic-bundle/v1。任何结构性问题都是文件级永久失败：
// 行级问题（引用缺失、枚举非法）留给四层校验按行报告。
func ParseBundle(data []byte) (SemanticBundle, error) {
	var bundle SemanticBundle
	if err := askdata.DecodeStrictJSON(data, &bundle); err != nil {
		return SemanticBundle{}, permanentImportError("IMPORT_BUNDLE_JSON_INVALID", err)
	}
	if bundle.Contract != BundleContractVersion {
		return SemanticBundle{}, permanentImportError("IMPORT_BUNDLE_CONTRACT_UNSUPPORTED", ErrImportFileInvalid)
	}
	switch bundle.Options.Mode {
	case "", BundleModeUpsert:
		bundle.Options.Mode = BundleModeUpsert
	case BundleModeCreateOnly:
	default:
		return SemanticBundle{}, permanentImportError("IMPORT_BUNDLE_MODE_INVALID", ErrImportFileInvalid)
	}
	if len(bundle.Assets) == 0 {
		return SemanticBundle{}, permanentImportError("IMPORT_FILE_EMPTY", ErrImportFileInvalid)
	}
	if len(bundle.Assets) > MaxBundleAssets {
		return SemanticBundle{}, permanentImportError("IMPORT_FILE_ROW_LIMIT", ErrImportFileInvalid)
	}
	return bundle, nil
}

// bundleExpansion 是一个 Bundle 资产展开出的扁平行组。同一资产可以展开成
// 多行（例如 BASE 指标 = MEASURE + METRIC + 兼容声明 + 别名词条），报表通过
// bundleAsset 下标聚合回资产粒度。
type bundleExpansion struct {
	AssetIndex int
	AssetType  AssetType
	Values     map[string]string
	// TopoCode/TopoDeps 只在 METRIC 展开行上出现，用于组内确定性拓扑排序。
	TopoCode string
	TopoDeps []string
}

// ExpandBundle 把 Bundle 确定性展开为 RawImportRow 序列。顺序规则：
//  1. 组间按依赖分层：MODEL → DIMENSION → HIERARCHY → MEMBER → MEASURE →
//     METRIC → METRIC_DIMENSION → RELATIONSHIP → KPI_BUNDLE → KNOWLEDGE → TERM；
//  2. METRIC 组内按公式依赖做稳定拓扑排序（环留在原位，由 L3 报告）；
//  3. 其余组内保持文件出现顺序。
//
// 展开只做结构映射与确定性缺省值，不查库；语义正误由四层校验判定。
func ExpandBundle(bundle SemanticBundle) ([]RawImportRow, error) {
	expansions := []bundleExpansion{}
	for index, asset := range bundle.Assets {
		expanded, err := expandBundleAsset(index, asset)
		if err != nil {
			return nil, err
		}
		expansions = append(expansions, expanded...)
	}
	if len(expansions) > MaxImportRows {
		return nil, permanentImportError("IMPORT_FILE_ROW_LIMIT", ErrImportFileInvalid)
	}
	ordered := orderBundleExpansions(expansions)
	rows := make([]RawImportRow, 0, len(ordered))
	for position, expansion := range ordered {
		payload := map[string]any{
			bundleRowAssetTypeKey:  string(expansion.AssetType),
			bundleRowAssetIndexKey: strconv.Itoa(expansion.AssetIndex),
			bundleRowModeKey:       bundle.Options.Mode,
		}
		for key, value := range expansion.Values {
			payload[key] = value
		}
		raw, err := registry.CanonicalValue(payload)
		if err != nil {
			return nil, permanentImportError("IMPORT_ROW_JSON_INVALID", err)
		}
		rows = append(rows, RawImportRow{RowNo: position + 1, Raw: raw})
	}
	return rows, nil
}

var bundleGroupRank = map[AssetType]int{
	AssetModel: 1, AssetDimension: 2, AssetHierarchy: 3, AssetMember: 4,
	AssetMeasure: 5, AssetMetric: 6, AssetMetricDimension: 7, AssetRelationship: 8,
	AssetKPIBundle: 9, AssetKnowledge: 10, AssetTerm: 11,
}

func orderBundleExpansions(expansions []bundleExpansion) []bundleExpansion {
	ordered := append([]bundleExpansion(nil), expansions...)
	sort.SliceStable(ordered, func(left, right int) bool {
		return bundleGroupRank[ordered[left].AssetType] < bundleGroupRank[ordered[right].AssetType]
	})
	start := 0
	for start < len(ordered) {
		end := start
		for end < len(ordered) && ordered[end].AssetType == ordered[start].AssetType {
			end++
		}
		if ordered[start].AssetType == AssetMetric {
			topoSortMetricExpansions(ordered[start:end])
		}
		start = end
	}
	return ordered
}

// topoSortMetricExpansions 对 METRIC 组做稳定拓扑排序（Kahn，按原始位置
// 决定同层顺序）。检测不到完整拓扑（存在环）时保持原顺序，让 L3 的
// IMPORT_FORMULA_CYCLE 逐行报告。
func topoSortMetricExpansions(group []bundleExpansion) {
	position := map[string]int{}
	for index, expansion := range group {
		if expansion.TopoCode != "" {
			position[expansion.TopoCode] = index
		}
	}
	indegree := make([]int, len(group))
	dependents := map[int][]int{}
	for index, expansion := range group {
		for _, dependency := range expansion.TopoDeps {
			source, exists := position[dependency]
			if !exists || source == index {
				continue
			}
			indegree[index]++
			dependents[source] = append(dependents[source], index)
		}
	}
	queue := []int{}
	for index := range group {
		if indegree[index] == 0 {
			queue = append(queue, index)
		}
	}
	result := make([]bundleExpansion, 0, len(group))
	for len(queue) > 0 {
		sort.Ints(queue)
		next := queue[0]
		queue = queue[1:]
		result = append(result, group[next])
		for _, dependent := range dependents[next] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	if len(result) != len(group) {
		return
	}
	copy(group, result)
}

// bundleSpec 是 spec 的宽松视图：展开只读取合同字段并转成模板列字符串，
// 类型不匹配一律展开为空串并由 L1 的必填/枚举检查按行报告。
type bundleSpec map[string]any

func decodeBundleSpec(raw json.RawMessage) bundleSpec {
	if len(raw) == 0 {
		return bundleSpec{}
	}
	var spec map[string]any
	if err := json.Unmarshal(raw, &spec); err != nil || spec == nil {
		return bundleSpec{}
	}
	return spec
}

func (spec bundleSpec) text(key string) string {
	value, _ := spec[key].(string)
	return strings.TrimSpace(value)
}

func (spec bundleSpec) textDefault(key, fallback string) string {
	if value := spec.text(key); value != "" {
		return value
	}
	return fallback
}

func (spec bundleSpec) integer(key string, fallback int) string {
	switch value := spec[key].(type) {
	case float64:
		return strconv.Itoa(int(value))
	case json.Number:
		return value.String()
	case string:
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strconv.Itoa(fallback)
}

func (spec bundleSpec) boolean(key string, fallback bool) string {
	if value, ok := spec[key].(bool); ok {
		if value {
			return "TRUE"
		}
		return "FALSE"
	}
	if fallback {
		return "TRUE"
	}
	return "FALSE"
}

func (spec bundleSpec) list(key string) []string {
	raw, ok := spec[key].([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func (spec bundleSpec) document(key string) string {
	value, exists := spec[key]
	if !exists || value == nil {
		return ""
	}
	canonical, err := registry.CanonicalValue(value)
	if err != nil {
		return ""
	}
	return string(canonical)
}

func (spec bundleSpec) objects(key string) []bundleSpec {
	raw, ok := spec[key].([]any)
	if !ok {
		return nil
	}
	result := make([]bundleSpec, 0, len(raw))
	for _, item := range raw {
		if object, ok := item.(map[string]any); ok {
			result = append(result, bundleSpec(object))
		}
	}
	return result
}

// bundleOpenValidity 是机器生成词条/成员的确定性缺省生效日：开放有效期。
// 展开必须是纯函数，任何“今天”都会破坏重试与去重，因此不用当前时间。
const bundleOpenValidity = "1970-01-01"

func expandBundleAsset(index int, asset BundleAsset) ([]bundleExpansion, error) {
	spec := decodeBundleSpec(asset.Spec)
	switch asset.Section {
	case BundleSectionModel:
		return expandModelAsset(index, asset, spec)
	case BundleSectionDimension:
		return expandDimensionAsset(index, asset, spec)
	case BundleSectionMetric:
		return expandMetricAsset(index, asset, spec)
	case BundleSectionKnowledge:
		return expandKnowledgeAsset(index, asset, spec)
	default:
		return nil, permanentImportError(
			"IMPORT_BUNDLE_SECTION_INVALID",
			fmt.Errorf("bundle asset %d: unsupported section %q", index, asset.Section),
		)
	}
}

func expandModelAsset(index int, asset BundleAsset, spec bundleSpec) ([]bundleExpansion, error) {
	switch asset.Kind {
	case "MODEL":
		grain := decodeBundleSpec(nil)
		if object, ok := spec["grain"].(map[string]any); ok {
			grain = bundleSpec(object)
		}
		return []bundleExpansion{{
			AssetIndex: index, AssetType: AssetModel,
			Values: map[string]string{
				"code": asset.Code, "name": asset.Name,
				"datasetVersionId":         spec.text("datasetVersionId"),
				"entityCode":               spec.text("entity"),
				"grainDescription":         grain.text("description"),
				"grainKeyFields":           pipeJoin(grain.list("keyFields")),
				"primaryTimeDimensionCode": spec.text("primaryTimeDimension"),
				"timeContractCode":         spec.text("timeContract"),
				"ownerEmail":               asset.Owner,
			},
		}}, nil
	case "RELATIONSHIP":
		return []bundleExpansion{{
			AssetIndex: index, AssetType: AssetRelationship,
			Values: map[string]string{
				"leftModelCode":   spec.text("leftModel"),
				"rightModelCode":  spec.text("rightModel"),
				"joinAst":         spec.document("joinAst"),
				"joinType":        spec.text("joinType"),
				"cardinality":     spec.text("cardinality"),
				"fanoutPolicy":    spec.text("fanoutPolicy"),
				"bridgeModelCode": spec.text("bridgeModel"),
				"validFrom":       spec.textDefault("validFrom", bundleOpenValidity),
				"validTo":         spec.text("validTo"),
			},
		}}, nil
	default:
		return nil, permanentImportError(
			"IMPORT_BUNDLE_KIND_INVALID",
			fmt.Errorf("bundle asset %d: MODEL section does not support kind %q", index, asset.Kind),
		)
	}
}

func expandDimensionAsset(index int, asset BundleAsset, spec bundleSpec) ([]bundleExpansion, error) {
	switch asset.Kind {
	case "CATEGORICAL", "TIME", "ENTITY":
		expansions := []bundleExpansion{{
			AssetIndex: index, AssetType: AssetDimension,
			Values: map[string]string{
				"modelCode": spec.text("model"), "code": asset.Code, "name": asset.Name,
				"description":       asset.Description,
				"kind":              asset.Kind,
				"logicalFieldId":    spec.text("field"),
				"sensitivity":       spec.textDefault("sensitivity", "INTERNAL"),
				"memberIndexPolicy": spec.textDefault("memberIndexPolicy", "NONE"),
				"groupable":         spec.boolean("groupable", true),
				"filterable":        spec.boolean("filterable", true),
				"sortable":          spec.boolean("sortable", false),
				"hierarchyCode":     spec.text("hierarchy"),
				"ownerEmail":        asset.Owner,
			},
		}}
		for _, member := range spec.objects("members") {
			expansions = append(expansions, expandMemberValues(index, asset.Code, member))
		}
		expansions = append(expansions, expandAliasTerms(index, "DIMENSION", asset.Code, asset.Aliases)...)
		return expansions, nil
	case "HIERARCHY":
		levels := spec.objects("levels")
		if len(levels) == 0 {
			return nil, permanentImportError(
				"IMPORT_BUNDLE_KIND_INVALID",
				fmt.Errorf("bundle asset %d: HIERARCHY requires spec.levels", index),
			)
		}
		expansions := make([]bundleExpansion, 0, len(levels))
		previous := ""
		for ordinal, level := range levels {
			dimension := level.text("dimension")
			expansions = append(expansions, bundleExpansion{
				AssetIndex: index, AssetType: AssetHierarchy,
				Values: map[string]string{
					"code": asset.Code, "name": asset.Name,
					"levelOrder":          strconv.Itoa(ordinal + 1),
					"dimensionCode":       dimension,
					"parentDimensionCode": previous,
				},
			})
			previous = dimension
		}
		return expansions, nil
	case "MEMBER":
		return []bundleExpansion{
			expandMemberValues(index, spec.text("dimension"), spec),
		}, nil
	default:
		return nil, permanentImportError(
			"IMPORT_BUNDLE_KIND_INVALID",
			fmt.Errorf("bundle asset %d: DIMENSION section does not support kind %q", index, asset.Kind),
		)
	}
}

func expandMemberValues(index int, dimensionCode string, member bundleSpec) bundleExpansion {
	return bundleExpansion{
		AssetIndex: index, AssetType: AssetMember,
		Values: map[string]string{
			"dimensionCode":  dimensionCode,
			"canonicalValue": member.textDefault("key", member.text("value")),
			"displayLabel":   member.text("label"),
			"aliases":        pipeJoin(member.list("aliases")),
			"hierarchyPath":  pipeJoin(member.list("path")),
			"validFrom":      member.textDefault("validFrom", bundleOpenValidity),
			"validTo":        member.text("validTo"),
			"sensitivity":    member.textDefault("sensitivity", "INTERNAL"),
			"definition":     member.text("definition"),
		},
	}
}

func expandMetricAsset(index int, asset BundleAsset, spec bundleSpec) ([]bundleExpansion, error) {
	switch asset.Kind {
	case "BASE", "DERIVED":
		return expandMetricDefinition(index, asset, spec)
	case "MEASURE":
		// 遗留独立度量：只建 MEASURE 行，不包装指标。导出遗留库存时保持
		// 语义不变的往返表示。
		return []bundleExpansion{
			expandMeasureValues(index, asset, spec, spec.text("sourceField")),
		}, nil
	case "COMPATIBILITY":
		return []bundleExpansion{{
			AssetIndex: index, AssetType: AssetMetricDimension,
			Values: map[string]string{
				"metricCode":    spec.text("metric"),
				"dimensionCode": spec.text("dimension"),
				"compatible":    spec.boolean("compatible", true),
				"role":          spec.textDefault("role", "GROUP_BY"),
			},
		}}, nil
	case "VIEW":
		metricCodes := []string{}
		chartTypes := []string{}
		for _, item := range spec.objects("items") {
			if code := item.text("metric"); code != "" {
				metricCodes = append(metricCodes, code)
			}
			if chart := item.text("chartType"); chart != "" {
				chartTypes = append(chartTypes, chart)
			}
		}
		return []bundleExpansion{{
			AssetIndex: index, AssetType: AssetKPIBundle,
			Values: map[string]string{
				"code": asset.Code, "name": asset.Name,
				"metricCodes":             pipeJoin(metricCodes),
				"defaultDimensionCodes":   pipeJoin(spec.list("defaultDimensions")),
				"defaultTimeExpression":   spec.text("defaultTimeExpression"),
				"defaultChartTypes":       pipeJoin(chartTypes),
				"roleMapping":             spec.document("roleMapping"),
				"applicableQuestionTypes": pipeJoin(spec.list("questionPatterns")),
			},
		}}, nil
	default:
		return nil, permanentImportError(
			"IMPORT_BUNDLE_KIND_INVALID",
			fmt.Errorf("bundle asset %d: METRIC section does not support kind %q", index, asset.Kind),
		)
	}
}

// expandMetricDefinition 展开 BASE / DERIVED 指标。BASE 是“度量 + 指标”合一：
// 同一 code 先落 MEASURE（物理聚合事实）再落 METRIC（业务口径），公式由展开
// 生成 MEASURE_REF；DERIVED 的公式由作者提供，引用 metricCode/measureCode。
func expandMetricDefinition(index int, asset BundleAsset, spec bundleSpec) ([]bundleExpansion, error) {
	expansions := []bundleExpansion{}
	formula := spec.document("formula")
	if asset.Kind == "BASE" {
		expansions = append(expansions, expandMeasureValues(index, asset, spec, spec.text("sourceField")))
		generated, err := registry.CanonicalValue(map[string]any{
			"type": "MEASURE_REF", "measureCode": asset.Code,
		})
		if err != nil {
			return nil, permanentImportError("IMPORT_ROW_JSON_INVALID", err)
		}
		formula = string(generated)
	}
	format := decodeBundleSpec(nil)
	if object, ok := spec["format"].(map[string]any); ok {
		format = bundleSpec(object)
	}
	metric := bundleExpansion{
		AssetIndex: index, AssetType: AssetMetric,
		TopoCode: canonicalLookup(asset.Code),
		Values: map[string]string{
			"code": asset.Code, "name": asset.Name, "description": asset.Description,
			"businessDefinition":             spec.text("businessDefinition"),
			"modelCode":                      spec.text("model"),
			"formula":                        formula,
			"defaultFilter":                  spec.document("defaultFilters"),
			"unit":                           spec.text("unit"),
			"currency":                       spec.text("currency"),
			"additivity":                     spec.textDefault("additivity", "FULLY_ADDITIVE"),
			"semiAdditiveTimeAggregation":    spec.text("semiAdditiveTimeAggregation"),
			"aggregationRestriction":         spec.text("aggregationRestriction"),
			"nonAdditiveDimensionCodes":      pipeJoin(spec.list("nonAdditiveDimensions")),
			"timeGrain":                      spec.textDefault("timeGrain", "NONE"),
			"dedupKey":                       spec.text("dedupKey"),
			"displayPrecision":               format.integer("precision", 2),
			"zeroDenominatorPolicy":          spec.textDefault("zeroDenominatorPolicy", "NULL"),
			"incompletePeriodPolicyOverride": spec.text("incompletePeriodPolicy"),
			"positiveExamples":               pipeJoin(spec.list("positiveQuestions")),
			"negativeExamples":               pipeJoin(spec.list("negativeExamples")),
			"ownerEmail":                     asset.Owner,
		},
	}
	for _, dependency := range expressionReferences(formula)["METRIC"] {
		metric.TopoDeps = append(metric.TopoDeps, canonicalLookup(dependency))
	}
	expansions = append(expansions, metric)
	for _, compatibility := range spec.objects("applicableDimensions") {
		expansions = append(expansions, bundleExpansion{
			AssetIndex: index, AssetType: AssetMetricDimension,
			Values: map[string]string{
				"metricCode":    asset.Code,
				"dimensionCode": compatibility.text("dimension"),
				"compatible":    compatibility.boolean("compatible", true),
				"role":          compatibility.textDefault("role", "GROUP_BY"),
			},
		})
	}
	expansions = append(expansions, expandAliasTerms(index, "METRIC", asset.Code, asset.Aliases)...)
	return expansions, nil
}

func expandMeasureValues(index int, asset BundleAsset, spec bundleSpec, sourceField string) bundleExpansion {
	return bundleExpansion{
		AssetIndex: index, AssetType: AssetMeasure,
		Values: map[string]string{
			"modelCode": spec.text("model"), "code": asset.Code, "name": asset.Name,
			"logicalFieldId":     sourceField,
			"defaultAggregation": spec.text("aggregation"),
			"additivity":         spec.textDefault("additivity", "FULLY_ADDITIVE"),
			"unit":               spec.text("unit"),
			"currency":           spec.text("currency"),
			"nullPolicy":         spec.textDefault("nullPolicy", "PRESERVE"),
		},
	}
}

// expandAliasTerms 把信封别名展开为问数别名词条（EXACT 匹配、开放有效期）。
// 别名是检索事实而非执行事实，落在 business_terms 上与人工词条同一治理。
func expandAliasTerms(index int, targetType, targetCode string, aliases []string) []bundleExpansion {
	expansions := make([]bundleExpansion, 0, len(aliases))
	for _, alias := range uniqueStrings(aliases) {
		expansions = append(expansions, bundleExpansion{
			AssetIndex: index, AssetType: AssetTerm,
			Values: map[string]string{
				"term": alias, "termType": targetType, "targetCode": targetCode,
				"matchMode": "EXACT", "priority": "100", "negativeContexts": "",
				"validFrom": bundleOpenValidity, "validTo": "", "source": "IMPORT",
			},
		})
	}
	return expansions
}

func expandKnowledgeAsset(index int, asset BundleAsset, spec bundleSpec) ([]bundleExpansion, error) {
	switch asset.Kind {
	case "ALIAS":
		// 往返表示：导出的问数别名词条以 KNOWLEDGE/ALIAS 资产回到 Bundle，
		// 再导入时仍落为 legacy TERM 行。
		return []bundleExpansion{{
			AssetIndex: index, AssetType: AssetTerm,
			Values: map[string]string{
				"term":             asset.Name,
				"termType":         spec.text("targetType"),
				"targetCode":       spec.text("targetCode"),
				"matchMode":        spec.textDefault("matchMode", "EXACT"),
				"priority":         spec.integer("priority", 100),
				"negativeContexts": pipeJoin(spec.list("negativeContexts")),
				"validFrom":        spec.textDefault("validFrom", bundleOpenValidity),
				"validTo":          spec.text("validTo"),
				"source":           "IMPORT",
			},
		}}, nil
	case "TERM", "DEFINITION", "CONVENTION", "POLICY", "FAQ", "DOMAIN_NOTE":
		return []bundleExpansion{{
			AssetIndex: index, AssetType: AssetKnowledge,
			Values: map[string]string{
				"code": asset.Code, "name": asset.Name,
				"knowledgeKind":    asset.Kind,
				"authority":        spec.textDefault("authority", "SUPPLEMENTARY"),
				"body":             spec.text("body"),
				"synonyms":         pipeJoin(append(append([]string{}, asset.Aliases...), spec.list("synonyms")...)),
				"targetType":       spec.text("targetType"),
				"targetCode":       spec.text("targetCode"),
				"relation":         spec.textDefault("relation", "EXPLAINS"),
				"matchMode":        spec.textDefault("matchMode", "EXACT"),
				"priority":         spec.integer("priority", 100),
				"negativeContexts": pipeJoin(spec.list("negativeContexts")),
				"validFrom":        spec.textDefault("validFrom", bundleOpenValidity),
				"validTo":          spec.text("validTo"),
			},
		}}, nil
	default:
		return nil, permanentImportError(
			"IMPORT_BUNDLE_KIND_INVALID",
			fmt.Errorf("bundle asset %d: KNOWLEDGE section does not support kind %q", index, asset.Kind),
		)
	}
}
