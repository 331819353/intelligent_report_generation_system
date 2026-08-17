package registryimport

import (
	"fmt"
	"sort"
	"strconv"

	"intelligent-report-generation-system/internal/askdata/registry"
)

// validateL3 的整批规则按行类型分组执行：单类型批行为不变，BUNDLE 批的每个
// 类型组各自独立评估（公式环只看 METRIC 组，层级连续性只看 HIERARCHY 组）。
func validateL3(rows []parsedImportRow, batch batchIndex, snapshot ValidationSnapshot) {
	validateFormulaCycles(rowsOfType(rows, AssetMetric))
	validateHierarchyContinuity(rowsOfType(rows, AssetHierarchy))
	validateCompatibilityRows(rowsOfType(rows, AssetMetricDimension), snapshot)
	for index := range rows {
		row := &rows[index]
		if row.Layer != 2 {
			continue
		}
		switch row.Type {
		case AssetMeasure, AssetMetric:
			validateAdditivityConsistency(row, row.Type)
			if row.Type == AssetMetric {
				validateTimeGrain(row, batch, snapshot)
			}
		case AssetRelationship:
			validateFanoutCombination(row)
		case AssetKnowledge:
			validateKnowledgeShape(row)
		}
		if len(row.Issues) == 0 {
			row.Layer = 3
		}
	}
}

func rowsOfType(rows []parsedImportRow, assetType AssetType) []*parsedImportRow {
	selected := []*parsedImportRow{}
	for index := range rows {
		if rows[index].Type == assetType {
			selected = append(selected, &rows[index])
		}
	}
	return selected
}

// validateKnowledgeShape 校验知识词条自身的结构自洽：DEFINES 必须指向一个
// 注册对象，权威定义必须给出正文。
func validateKnowledgeShape(row *parsedImportRow) {
	if row.Values["relation"] == "DEFINES" &&
		(row.Values["targetType"] == "" || row.Values["targetCode"] == "") {
		addIssue(row, "relation", ImportTypeInvalid, "DEFINES 关系必须指向具体对象",
			"targetType 与 targetCode 均非空", row.Values["relation"])
	}
	if row.Values["targetCode"] != "" && row.Values["targetType"] == "" {
		addIssue(row, "targetType", ImportRequiredMissing, "给出 targetCode 时必须声明 targetType",
			"METRIC | DIMENSION | MEMBER | TIME", "")
	}
}

func validateFormulaCycles(rows []*parsedImportRow) {
	codeToRow := map[string]*parsedImportRow{}
	graph := map[string][]string{}
	for _, row := range rows {
		if row.Layer != 2 {
			continue
		}
		code := canonicalLookup(row.Values["code"])
		if code == "" {
			continue
		}
		codeToRow[code] = row
		for _, dependency := range expressionReferences(row.Values["formula"])["METRIC"] {
			graph[code] = append(graph[code], canonicalLookup(dependency))
		}
	}
	state := map[string]int{}
	stack := []string{}
	inCycle := map[string]bool{}
	var visit func(string)
	visit = func(code string) {
		state[code] = 1
		stack = append(stack, code)
		for _, dependency := range graph[code] {
			if codeToRow[dependency] == nil {
				continue
			}
			switch state[dependency] {
			case 0:
				visit(dependency)
			case 1:
				for index := len(stack) - 1; index >= 0; index-- {
					inCycle[stack[index]] = true
					if stack[index] == dependency {
						break
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[code] = 2
	}
	for code := range codeToRow {
		if state[code] == 0 {
			visit(code)
		}
	}
	for code := range inCycle {
		row := codeToRow[code]
		addIssue(row, "formula", ImportFormulaCycle, "指标公式依赖形成环", "有向无环的 METRIC_REF 依赖", row.Values["formula"])
	}
}

func validateHierarchyContinuity(rows []*parsedImportRow) {
	groups := map[string][]*parsedImportRow{}
	for _, row := range rows {
		if row.Layer == 2 {
			groups[canonicalLookup(row.Values["code"])] = append(groups[canonicalLookup(row.Values["code"])], row)
		}
	}
	for _, levels := range groups {
		sort.SliceStable(levels, func(left, right int) bool {
			leftLevel, _ := strconv.Atoi(levels[left].Values["levelOrder"])
			rightLevel, _ := strconv.Atoi(levels[right].Values["levelOrder"])
			return leftLevel < rightLevel
		})
		for index, row := range levels {
			level, _ := strconv.Atoi(row.Values["levelOrder"])
			expectedLevel := index + 1
			broken := level != expectedLevel
			if index == 0 {
				broken = broken || row.Values["parentDimensionCode"] != ""
			} else {
				broken = broken || canonicalLookup(row.Values["parentDimensionCode"]) != canonicalLookup(levels[index-1].Values["dimensionCode"])
			}
			if broken {
				addIssue(row, "levelOrder", ImportHierarchyBroken, "层级顺序不连续或父级不指向上一层", fmt.Sprintf("levelOrder=%d 且 parentDimensionCode 指向上一层 dimensionCode", expectedLevel), row.Values["levelOrder"]+"/"+row.Values["parentDimensionCode"])
			}
		}
	}
}

func validateCompatibilityRows(rows []*parsedImportRow, snapshot ValidationSnapshot) {
	pairs := map[string]map[string][]*parsedImportRow{}
	for _, row := range rows {
		if row.Layer != 2 {
			continue
		}
		key := canonicalLookup(row.Values["metricCode"]) + "\x00" + canonicalLookup(row.Values["dimensionCode"])
		if pairs[key] == nil {
			pairs[key] = map[string][]*parsedImportRow{}
		}
		pairs[key][row.Values["compatible"]] = append(pairs[key][row.Values["compatible"]], row)
		metric := snapshot.References["METRIC"][canonicalLookup(row.Values["metricCode"])]
		dimension := snapshot.References["DIMENSION"][canonicalLookup(row.Values["dimensionCode"])]
		if metric.ModelCode != "" && dimension.ModelCode != "" && canonicalLookup(metric.ModelCode) != canonicalLookup(dimension.ModelCode) && row.Values["compatible"] == "TRUE" {
			addIssue(row, "compatible", ImportCompatAsymmetric, "指标与维度所属模型不一致，不能单向声明兼容", "同模型维度，或显式 FALSE", row.Values["metricCode"]+"/"+row.Values["dimensionCode"])
		}
	}
	for _, declarations := range pairs {
		if len(declarations) <= 1 {
			continue
		}
		for _, duplicated := range declarations {
			for _, row := range duplicated {
				addIssue(row, "compatible", ImportCompatAsymmetric, "同一指标—维度对存在相互矛盾的声明", "每个 metricCode/dimensionCode 对只有一个一致的 compatible 值", row.Values["compatible"])
			}
		}
	}
}

func validateFanoutCombination(row *parsedImportRow) {
	cardinality := row.Values["cardinality"]
	policy := row.Values["fanoutPolicy"]
	if registry.ValidateRelationshipCombination(
		registry.Cardinality(cardinality), registry.FanoutPolicy(policy), row.Values["bridgeModelCode"],
	) != nil {
		expected := "ONE_TO_ONE/MANY_TO_ONE: SAFE|BLOCK；ONE_TO_MANY: PRE_AGGREGATE_REQUIRED|BLOCK；MANY_TO_MANY: BRIDGE_REQUIRED|BLOCK"
		addIssue(row, "fanoutPolicy", ImportFanoutCombinationInvalid, "cardinality 与 fanoutPolicy 组合不安全", expected, cardinality+" × "+policy)
	}
}

func validateAdditivityConsistency(row *parsedImportRow, assetType AssetType) {
	additivity := row.Values["additivity"]
	semi := row.Values["semiAdditiveTimeAggregation"]
	restriction := row.Values["aggregationRestriction"]
	nonAdditiveDimensions := row.Values["nonAdditiveDimensionCodes"]
	dedupKey := row.Values["dedupKey"]
	consistent := true
	expected := ""
	switch additivity {
	case "FULLY_ADDITIVE":
		consistent = semi == "" && restriction == "" && nonAdditiveDimensions == "" && dedupKey == ""
		expected = "FULLY_ADDITIVE 不填写半可加聚合、聚合限制、不可加维度或去重键"
	case "SEMI_ADDITIVE":
		consistent = semi != "" && (assetType != AssetMetric || row.Values["timeGrain"] != "NONE")
		expected = "SEMI_ADDITIVE 必须声明 semiAdditiveTimeAggregation，指标还必须有非 NONE timeGrain"
	case "NON_ADDITIVE":
		consistent = restriction != "" || nonAdditiveDimensions != "" || dedupKey != ""
		expected = "NON_ADDITIVE 至少声明 aggregationRestriction、nonAdditiveDimensionCodes 或 dedupKey"
	}
	if assetType == AssetMeasure {
		aggregation := row.Values["defaultAggregation"]
		if (aggregation == "AVG" || aggregation == "COUNT_DISTINCT") && additivity != "NON_ADDITIVE" {
			consistent = false
			expected = "AVG/COUNT_DISTINCT 必须为 NON_ADDITIVE"
		}
		if (aggregation == "MIN" || aggregation == "MAX") && additivity == "FULLY_ADDITIVE" {
			consistent = false
			expected = "MIN/MAX 不得为 FULLY_ADDITIVE"
		}
	}
	if !consistent {
		addIssue(row, "additivity", ImportAdditivityInconsistent, "可加性字段组合不自洽", expected, additivity)
	}
}

func validateTimeGrain(row *parsedImportRow, batch batchIndex, snapshot ValidationSnapshot) {
	grain := row.Values["timeGrain"]
	if grain == "" || grain == "NONE" {
		return
	}
	modelCode := canonicalLookup(row.Values["modelCode"])
	model := snapshot.References["MODEL"][modelCode]
	grains := model.TimeGrains
	if len(grains) == 0 {
		// BUNDLE 批：模型可能与指标同批创建。此时用同批 MODEL 行声明的时间
		// 合同 code 解析认证合同的支持粒度。
		grains = batchModelTimeGrains(batch, modelCode, snapshot)
	}
	if len(grains) == 0 {
		addIssue(row, "timeGrain", ImportAdditivityInconsistent, "模型没有可证明支持该时间粒度的认证时间合同", "modelCode 绑定的 CERTIFIED time contract supportedGrains", grain)
		return
	}
	for _, allowed := range grains {
		if allowed == grain {
			return
		}
	}
	addIssue(row, "timeGrain", ImportAdditivityInconsistent, "时间合同不支持指标粒度", fmt.Sprintf("supportedGrains=%v", grains), grain)
}

func batchModelTimeGrains(batch batchIndex, modelCode string, snapshot ValidationSnapshot) []string {
	for _, rowNo := range batch.Codes["MODEL"][modelCode] {
		modelRow := batch.Rows[rowNo]
		if modelRow == nil {
			continue
		}
		contract := snapshot.References["TIME_CONTRACT"][canonicalLookup(modelRow.Values["timeContractCode"])]
		if len(contract.TimeGrains) > 0 {
			return contract.TimeGrains
		}
	}
	return nil
}
