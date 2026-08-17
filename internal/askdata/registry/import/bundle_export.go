package registryimport

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// RenderBundleArtifact 把扁平导出数据集渲染为 semantic-bundle/v1 JSON——导入
// 合同的逆映射，保证“导出 → 导入 → 全部 unchanged”的往返恒等：
//   - MEASURE+METRIC 同 code 且公式恰为该度量的 MEASURE_REF 时合并为 BASE 指标；
//   - METRIC_DIMENSION 在对应指标资产存在时折叠为 applicableDimensions；
//   - HIERARCHY 行按 code 聚合为带 levels 的层级资产；
//   - 词条（ALIAS）与知识词条分别落 KNOWLEDGE/ALIAS 与对应知识 kind。
//
// CERTIFIED_EXAMPLE 与 EVAL_CASE 不属于四分区语义资产，不进入 Bundle 合同。
func RenderBundleArtifact(
	selection ExportSelection,
	dataset ExportDataset,
) (ExportArtifact, error) {
	byType := map[AssetType][]map[string]string{}
	for _, sheet := range dataset.Sheets {
		if sheet.AssetType == AssetCertifiedExample || sheet.AssetType == AssetEvalCase {
			return ExportArtifact{}, ErrExportContract
		}
		definition, err := TemplateDefinitionFor(sheet.AssetType)
		if err != nil {
			return ExportArtifact{}, err
		}
		sortExportRows(definition, sheet.Rows)
		byType[sheet.AssetType] = sheet.Rows
	}
	assets := []BundleAsset{}
	baseMetricCodes := map[string]struct{}{}
	metricAssetIndex := map[string]int{}

	measureRows := map[string]map[string]string{}
	for _, row := range byType[AssetMeasure] {
		measureRows[canonicalLookup(row["code"])] = row
	}
	for _, row := range byType[AssetModel] {
		assets = append(assets, exportModelAsset(row))
	}
	for _, row := range byType[AssetDimension] {
		assets = append(assets, exportDimensionAsset(row))
	}
	hierarchyAssets, err := exportHierarchyAssets(byType[AssetHierarchy])
	if err != nil {
		return ExportArtifact{}, err
	}
	assets = append(assets, hierarchyAssets...)
	for _, row := range byType[AssetMember] {
		assets = append(assets, exportMemberAsset(row))
	}
	for _, row := range byType[AssetMetric] {
		asset, base, err := exportMetricAsset(row, measureRows)
		if err != nil {
			return ExportArtifact{}, err
		}
		metricAssetIndex[canonicalLookup(row["code"])] = len(assets)
		assets = append(assets, asset)
		if base {
			baseMetricCodes[canonicalLookup(row["code"])] = struct{}{}
		}
	}
	for code := range measureRows {
		if _, merged := baseMetricCodes[code]; merged {
			continue
		}
		assets = append(assets, exportStandaloneMeasureAsset(measureRows[code]))
	}
	assets = foldCompatibilityRows(assets, metricAssetIndex, byType[AssetMetricDimension])
	for _, row := range byType[AssetRelationship] {
		assets = append(assets, exportRelationshipAsset(row))
	}
	for _, row := range byType[AssetKPIBundle] {
		assets = append(assets, exportMetricViewAsset(row))
	}
	for _, row := range byType[AssetKnowledge] {
		assets = append(assets, exportKnowledgeAsset(row))
	}
	for _, row := range byType[AssetTerm] {
		assets = append(assets, exportAliasKnowledgeAsset(row))
	}
	bundle := map[string]any{
		"contract": BundleContractVersion,
		"options":  map[string]any{"mode": BundleModeUpsert},
		"assets":   assets,
	}
	payload, err := registry.CanonicalValue(bundle)
	if err != nil {
		return ExportArtifact{}, err
	}
	scope := "current"
	if selection.ReleaseID != "" {
		scope = "release-" + selection.ReleaseID
	}
	return ExportArtifact{
		Filename:    fmt.Sprintf("askdata-semantic-%s.bundle.json", scope),
		ContentType: "application/json",
		Bytes:       payload, ContentHash: string(askdata.HashBytes(payload)),
		RowCount:                len(assets),
		OmittedSensitiveMembers: dataset.OmittedSensitiveMembers,
	}, nil
}

func bundleSpecValue(pairs map[string]any) json.RawMessage {
	compact := map[string]any{}
	for key, value := range pairs {
		switch typed := value.(type) {
		case string:
			if typed != "" {
				compact[key] = typed
			}
		case []string:
			if len(typed) > 0 {
				compact[key] = typed
			}
		case nil:
		default:
			compact[key] = typed
		}
	}
	payload, err := registry.CanonicalValue(compact)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return payload
}

func exportModelAsset(row map[string]string) BundleAsset {
	grain := map[string]any{}
	if row["grainDescription"] != "" {
		grain["description"] = row["grainDescription"]
	}
	if keys := splitPipe(row["grainKeyFields"]); len(keys) > 0 {
		grain["keyFields"] = keys
	}
	spec := map[string]any{
		"datasetVersionId":     row["datasetVersionId"],
		"entity":               row["entityCode"],
		"primaryTimeDimension": row["primaryTimeDimensionCode"],
		"timeContract":         row["timeContractCode"],
	}
	if len(grain) > 0 {
		spec["grain"] = grain
	}
	return BundleAsset{
		Section: BundleSectionModel, Kind: "MODEL",
		Code: row["code"], Name: row["name"], Owner: row["ownerEmail"],
		Spec: bundleSpecValue(spec),
	}
}

func exportDimensionAsset(row map[string]string) BundleAsset {
	return BundleAsset{
		Section: BundleSectionDimension, Kind: row["kind"],
		Code: row["code"], Name: row["name"], Description: row["description"],
		Owner: row["ownerEmail"],
		Spec: bundleSpecValue(map[string]any{
			"model":             row["modelCode"],
			"field":             row["logicalFieldId"],
			"sensitivity":       row["sensitivity"],
			"memberIndexPolicy": row["memberIndexPolicy"],
			"groupable":         row["groupable"] == "TRUE",
			"filterable":        row["filterable"] == "TRUE",
			"sortable":          row["sortable"] == "TRUE",
			"hierarchy":         row["hierarchyCode"],
		}),
	}
}

func exportHierarchyAssets(rows []map[string]string) ([]BundleAsset, error) {
	type level struct {
		order     int
		dimension string
	}
	grouped := map[string][]level{}
	names := map[string]string{}
	codes := []string{}
	for _, row := range rows {
		order, err := strconv.Atoi(row["levelOrder"])
		if err != nil {
			return nil, ErrExportContract
		}
		code := row["code"]
		if _, exists := grouped[code]; !exists {
			codes = append(codes, code)
		}
		grouped[code] = append(grouped[code], level{order: order, dimension: row["dimensionCode"]})
		names[code] = row["name"]
	}
	sort.Strings(codes)
	assets := make([]BundleAsset, 0, len(codes))
	for _, code := range codes {
		levels := grouped[code]
		sort.Slice(levels, func(left, right int) bool { return levels[left].order < levels[right].order })
		specLevels := make([]map[string]any, len(levels))
		for index, entry := range levels {
			specLevels[index] = map[string]any{"dimension": entry.dimension}
		}
		assets = append(assets, BundleAsset{
			Section: BundleSectionDimension, Kind: "HIERARCHY",
			Code: code, Name: names[code],
			Spec: bundleSpecValue(map[string]any{"levels": specLevels}),
		})
	}
	return assets, nil
}

func exportMemberAsset(row map[string]string) BundleAsset {
	return BundleAsset{
		Section: BundleSectionDimension, Kind: "MEMBER",
		Name: row["displayLabel"],
		Spec: bundleSpecValue(map[string]any{
			"dimension":   row["dimensionCode"],
			"key":         row["canonicalValue"],
			"label":       row["displayLabel"],
			"aliases":     splitPipe(row["aliases"]),
			"path":        splitPipe(row["hierarchyPath"]),
			"validFrom":   row["validFrom"],
			"validTo":     row["validTo"],
			"sensitivity": row["sensitivity"],
			"definition":  row["definition"],
		}),
	}
}

// exportMetricAsset 判定 BASE 合并：公式恰为同 code 度量的 MEASURE_REF 且该
// 度量在同一导出中存在时，二者是一个 BASE 指标的两半。
func exportMetricAsset(
	row map[string]string,
	measures map[string]map[string]string,
) (BundleAsset, bool, error) {
	code := canonicalLookup(row["code"])
	measure, hasMeasure := measures[code]
	base := false
	if hasMeasure {
		expected, err := registry.CanonicalValue(map[string]any{
			"type": "MEASURE_REF", "measureCode": row["code"],
		})
		if err != nil {
			return BundleAsset{}, false, err
		}
		actual, err := registry.CanonicalJSON([]byte(row["formula"]))
		// 只有公式恰为该度量的引用且两半的共享事实一致时才可无损合并；
		// 任何分歧都保持“DERIVED + 独立 MEASURE”的显式表示。
		if err == nil && string(actual) == string(expected) &&
			measure["modelCode"] == row["modelCode"] &&
			measure["additivity"] == row["additivity"] &&
			measure["unit"] == row["unit"] && measure["currency"] == row["currency"] {
			base = true
		}
	}
	spec := map[string]any{
		"model":                       row["modelCode"],
		"defaultFilters":              rawJSONSpec(row["defaultFilter"]),
		"unit":                        row["unit"],
		"currency":                    row["currency"],
		"additivity":                  row["additivity"],
		"semiAdditiveTimeAggregation": row["semiAdditiveTimeAggregation"],
		"aggregationRestriction":      row["aggregationRestriction"],
		"nonAdditiveDimensions":       splitPipe(row["nonAdditiveDimensionCodes"]),
		"timeGrain":                   row["timeGrain"],
		"dedupKey":                    row["dedupKey"],
		"zeroDenominatorPolicy":       row["zeroDenominatorPolicy"],
		"incompletePeriodPolicy":      row["incompletePeriodPolicyOverride"],
		"businessDefinition":          row["businessDefinition"],
		"positiveQuestions":           splitPipe(row["positiveExamples"]),
		"negativeExamples":            splitPipe(row["negativeExamples"]),
	}
	if precision := row["displayPrecision"]; precision != "" && precision != "2" {
		if value, err := strconv.Atoi(precision); err == nil {
			spec["format"] = map[string]any{"precision": value}
		}
	}
	kind := "DERIVED"
	if base {
		kind = "BASE"
		spec["aggregation"] = measure["defaultAggregation"]
		spec["sourceField"] = measure["logicalFieldId"]
		spec["nullPolicy"] = measure["nullPolicy"]
	} else {
		spec["formula"] = rawJSONSpec(row["formula"])
	}
	return BundleAsset{
		Section: BundleSectionMetric, Kind: kind,
		Code: row["code"], Name: row["name"], Description: row["description"],
		Owner: row["ownerEmail"], Spec: bundleSpecValue(spec),
	}, base, nil
}

func exportStandaloneMeasureAsset(row map[string]string) BundleAsset {
	return BundleAsset{
		Section: BundleSectionMetric, Kind: "MEASURE",
		Code: row["code"], Name: row["name"],
		Spec: bundleSpecValue(map[string]any{
			"model":       row["modelCode"],
			"sourceField": row["logicalFieldId"],
			"aggregation": row["defaultAggregation"],
			"additivity":  row["additivity"],
			"unit":        row["unit"],
			"currency":    row["currency"],
			"nullPolicy":  row["nullPolicy"],
		}),
	}
}

// foldCompatibilityRows 把指标—维度兼容声明折叠进对应指标资产；指标不在本次
// 导出时保持独立 COMPATIBILITY 资产（部分导出仍然无损）。
func foldCompatibilityRows(
	assets []BundleAsset,
	metricAssetIndex map[string]int,
	rows []map[string]string,
) []BundleAsset {
	type compatibility struct {
		Dimension  string `json:"dimension"`
		Compatible bool   `json:"compatible"`
		Role       string `json:"role"`
	}
	folded := map[int][]compatibility{}
	for _, row := range rows {
		entry := compatibility{
			Dimension:  row["dimensionCode"],
			Compatible: row["compatible"] == "TRUE",
			Role:       row["role"],
		}
		index, exists := metricAssetIndex[canonicalLookup(row["metricCode"])]
		if !exists {
			assets = append(assets, BundleAsset{
				Section: BundleSectionMetric, Kind: "COMPATIBILITY",
				Name: row["metricCode"] + "×" + row["dimensionCode"],
				Spec: bundleSpecValue(map[string]any{
					"metric":     row["metricCode"],
					"dimension":  entry.Dimension,
					"compatible": entry.Compatible,
					"role":       entry.Role,
				}),
			})
			continue
		}
		folded[index] = append(folded[index], entry)
	}
	for index, entries := range folded {
		var spec map[string]any
		if json.Unmarshal(assets[index].Spec, &spec) != nil || spec == nil {
			continue
		}
		sort.Slice(entries, func(left, right int) bool {
			return entries[left].Dimension < entries[right].Dimension
		})
		spec["applicableDimensions"] = entries
		assets[index].Spec = bundleSpecValue(spec)
	}
	return assets
}

func exportRelationshipAsset(row map[string]string) BundleAsset {
	return BundleAsset{
		Section: BundleSectionModel, Kind: "RELATIONSHIP",
		Name: row["leftModelCode"] + "→" + row["rightModelCode"],
		Spec: bundleSpecValue(map[string]any{
			"leftModel":    row["leftModelCode"],
			"rightModel":   row["rightModelCode"],
			"joinAst":      rawJSONSpec(row["joinAst"]),
			"joinType":     row["joinType"],
			"cardinality":  row["cardinality"],
			"fanoutPolicy": row["fanoutPolicy"],
			"bridgeModel":  row["bridgeModelCode"],
			"validFrom":    row["validFrom"],
			"validTo":      row["validTo"],
		}),
	}
}

func exportMetricViewAsset(row map[string]string) BundleAsset {
	metricCodes := splitPipe(row["metricCodes"])
	chartTypes := splitPipe(row["defaultChartTypes"])
	items := make([]map[string]any, len(metricCodes))
	for index, code := range metricCodes {
		item := map[string]any{"metric": code}
		if index < len(chartTypes) {
			item["chartType"] = chartTypes[index]
		}
		items[index] = item
	}
	return BundleAsset{
		Section: BundleSectionMetric, Kind: "VIEW",
		Code: row["code"], Name: row["name"],
		Spec: bundleSpecValue(map[string]any{
			"items":                 items,
			"defaultDimensions":     splitPipe(row["defaultDimensionCodes"]),
			"defaultTimeExpression": row["defaultTimeExpression"],
			"questionPatterns":      splitPipe(row["applicableQuestionTypes"]),
			"roleMapping":           rawJSONSpec(row["roleMapping"]),
		}),
	}
}

func exportKnowledgeAsset(row map[string]string) BundleAsset {
	return BundleAsset{
		Section: BundleSectionKnowledge, Kind: row["knowledgeKind"],
		Code: row["code"], Name: row["name"],
		Spec: bundleSpecValue(map[string]any{
			"authority":        row["authority"],
			"relation":         row["relation"],
			"targetType":       row["targetType"],
			"targetCode":       row["targetCode"],
			"body":             row["body"],
			"synonyms":         splitPipe(row["synonyms"]),
			"matchMode":        row["matchMode"],
			"priority":         mustAtoiDefault(row["priority"], 100),
			"negativeContexts": splitPipe(row["negativeContexts"]),
			"validFrom":        row["validFrom"],
			"validTo":          row["validTo"],
		}),
	}
}

func exportAliasKnowledgeAsset(row map[string]string) BundleAsset {
	return BundleAsset{
		Section: BundleSectionKnowledge, Kind: "ALIAS",
		Name: row["term"],
		Spec: bundleSpecValue(map[string]any{
			"targetType":       row["termType"],
			"targetCode":       row["targetCode"],
			"matchMode":        row["matchMode"],
			"priority":         mustAtoiDefault(row["priority"], 100),
			"negativeContexts": splitPipe(row["negativeContexts"]),
			"validFrom":        row["validFrom"],
			"validTo":          row["validTo"],
		}),
	}
}

// rawJSONSpec 把导出行里的规范化 JSON 字符串还原为结构，空串还原为 nil。
func rawJSONSpec(value string) any {
	if value == "" {
		return nil
	}
	var decoded any
	if json.Unmarshal([]byte(value), &decoded) != nil {
		return nil
	}
	return decoded
}

func mustAtoiDefault(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
