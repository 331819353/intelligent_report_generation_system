package registryimport

import (
	"encoding/json"
	"strings"
)

func validateL2(rows []parsedImportRow, batch batchIndex, snapshot ValidationSnapshot) {
	for index := range rows {
		row := &rows[index]
		if row.Layer != 1 {
			continue
		}
		validateRowReferences(row, batch, snapshot)
		if len(row.Issues) == 0 {
			row.Layer = 2
		}
	}
}

func validateRowReferences(row *parsedImportRow, batch batchIndex, snapshot ValidationSnapshot) {
	switch row.Type {
	case AssetModel:
		validateDatasetReference(row, snapshot)
		validateRef(row, batch, snapshot, "entityCode", "ENTITY")
		validateOptionalRef(row, batch, snapshot, "primaryTimeDimensionCode", "DIMENSION")
		validateOptionalRef(row, batch, snapshot, "timeContractCode", "TIME_CONTRACT")
	case AssetMeasure:
		validateRef(row, batch, snapshot, "modelCode", "MODEL")
		validateLogicalField(row, snapshot, "modelCode", "logicalFieldId")
	case AssetMetric:
		validateRef(row, batch, snapshot, "modelCode", "MODEL")
		validateRefList(row, batch, snapshot, "nonAdditiveDimensionCodes", "DIMENSION")
		for kind, codes := range expressionReferences(row.Values["formula"]) {
			for _, code := range codes {
				validateRefValue(row, batch, snapshot, "formula", kind, code)
			}
		}
	case AssetMetricDimension:
		validateRef(row, batch, snapshot, "metricCode", "METRIC")
		validateRef(row, batch, snapshot, "dimensionCode", "DIMENSION")
	case AssetDimension:
		validateRef(row, batch, snapshot, "modelCode", "MODEL")
		validateLogicalField(row, snapshot, "modelCode", "logicalFieldId")
		validateOptionalRef(row, batch, snapshot, "hierarchyCode", "HIERARCHY")
	case AssetMember:
		validateRef(row, batch, snapshot, "dimensionCode", "DIMENSION")
	case AssetHierarchy:
		validateRef(row, batch, snapshot, "dimensionCode", "DIMENSION")
		validateOptionalRef(row, batch, snapshot, "parentDimensionCode", "DIMENSION")
	case AssetRelationship:
		validateRef(row, batch, snapshot, "leftModelCode", "MODEL")
		validateRef(row, batch, snapshot, "rightModelCode", "MODEL")
		validateOptionalRef(row, batch, snapshot, "bridgeModelCode", "MODEL")
	case AssetTerm:
		kind := mapTermTargetKind(row.Values["termType"])
		if kind != "" {
			validateRef(row, batch, snapshot, "targetCode", kind)
		}
	case AssetCertifiedExample:
		validateRefList(row, batch, snapshot, "expectedMetricCodes", "METRIC")
		validateRefList(row, batch, snapshot, "expectedDimensionCodes", "DIMENSION")
		validateRoleList(row, snapshot, "applicableRoles")
	case AssetKPIBundle:
		validateRefList(row, batch, snapshot, "metricCodes", "METRIC")
		validateRefList(row, batch, snapshot, "defaultDimensionCodes", "DIMENSION")
		validateRoleMapping(row, snapshot)
	case AssetEvalCase:
		validateRefList(row, batch, snapshot, "expectedMetricCodes", "METRIC")
		validateRefList(row, batch, snapshot, "expectedDimensionCodes", "DIMENSION")
		validateRole(row, snapshot, "actorRole")
	case AssetKnowledge:
		if kind := mapKnowledgeTargetKind(row.Values["targetType"]); kind != "" && row.Values["targetCode"] != "" {
			validateRef(row, batch, snapshot, "targetCode", kind)
		}
	}
	if email := row.Values["ownerEmail"]; email != "" {
		if _, exists := snapshot.Owners[canonicalLookup(email)]; !exists {
			addIssue(row, "ownerEmail", ImportOwnerUnknown, "Owner 邮箱未解析到本租户 ACTIVE 用户", "本租户 ACTIVE 用户邮箱", email)
		}
	}
}

func validateRef(row *parsedImportRow, batch batchIndex, snapshot ValidationSnapshot, column, kind string) {
	validateRefValue(row, batch, snapshot, column, kind, row.Values[column])
}

func validateOptionalRef(row *parsedImportRow, batch batchIndex, snapshot ValidationSnapshot, column, kind string) {
	if row.Values[column] != "" {
		validateRef(row, batch, snapshot, column, kind)
	}
}

func validateRefList(row *parsedImportRow, batch batchIndex, snapshot ValidationSnapshot, column, kind string) {
	for _, value := range splitList(row.Values[column]) {
		validateRefValue(row, batch, snapshot, column, kind, value)
	}
}

func validateRefValue(row *parsedImportRow, batch batchIndex, snapshot ValidationSnapshot, column, kind, value string) {
	key := canonicalLookup(value)
	if key == "" {
		return
	}
	if rows := batch.Codes[kind][key]; len(rows) > 0 {
		return
	}
	reference, exists := snapshot.References[kind][key]
	if !exists {
		addIssue(row, column, ImportRefNotFound, "引用 code 在当前业务域及本批次中不存在", kind+" code", value)
		return
	}
	if !reference.Active {
		addIssue(row, column, ImportRefNotActive, "引用对象不是当前可用的认证版本", "当前业务域内 CERTIFIED/PUBLISHED ACTIVE 的 "+kind, value)
	}
}

func validateDatasetReference(row *parsedImportRow, snapshot ValidationSnapshot) {
	id := row.Values["datasetVersionId"]
	active, exists := snapshot.Datasets[id]
	if !exists {
		addIssue(row, "datasetVersionId", ImportRefNotFound, "数据集版本不存在或不属于当前业务域", "当前业务域的 PUBLISHED DWS/ADS datasetVersionId", id)
		return
	}
	if !active {
		addIssue(row, "datasetVersionId", ImportRefNotActive, "数据集版本没有 ACTIVE 物化", "带 ACTIVE materialization 的当前 PUBLISHED DWS/ADS 版本", id)
	}
}

func validateLogicalField(row *parsedImportRow, snapshot ValidationSnapshot, modelColumn, fieldColumn string) {
	model, exists := snapshot.References["MODEL"][canonicalLookup(row.Values[modelColumn])]
	if !exists || model.DatasetVersionID == "" {
		return
	}
	fieldID := row.Values[fieldColumn]
	if !snapshot.Fields[model.DatasetVersionID][fieldID] {
		addIssue(row, fieldColumn, ImportRefNotFound, "逻辑字段不在模型固定的数据集版本中", "modelCode 对应 datasetVersionId 内的 logical field ID", fieldID)
	}
}

func validateRole(row *parsedImportRow, snapshot ValidationSnapshot, column string) {
	value := row.Values[column]
	if value == "" {
		return
	}
	if _, exists := snapshot.Roles[canonicalLookup(value)]; !exists {
		addIssue(row, column, ImportRefNotFound, "角色 code 不存在或未启用", "本租户 ACTIVE role code", value)
	}
}

func validateRoleList(row *parsedImportRow, snapshot ValidationSnapshot, column string) {
	for _, role := range splitList(row.Values[column]) {
		if _, exists := snapshot.Roles[canonicalLookup(role)]; !exists {
			addIssue(row, column, ImportRefNotFound, "角色 code 不存在或未启用", "本租户 ACTIVE role code", role)
		}
	}
}

func validateRoleMapping(row *parsedImportRow, snapshot ValidationSnapshot) {
	value := row.Values["roleMapping"]
	if value == "" {
		return
	}
	var mapping map[string]any
	if json.Unmarshal([]byte(value), &mapping) != nil {
		return
	}
	for role := range mapping {
		if _, exists := snapshot.Roles[canonicalLookup(role)]; !exists {
			addIssue(row, "roleMapping", ImportRefNotFound, "roleMapping 含未启用角色", "键为本租户 ACTIVE role code 的 JSON object", role)
		}
	}
}

func mapTermTargetKind(termType string) string {
	switch termType {
	case "METRIC", "DIMENSION", "MEMBER":
		return termType
	case "TIME":
		return "TIME_CONTRACT"
	default:
		return ""
	}
}

// mapKnowledgeTargetKind 与词条目标同一映射；空 targetType 表示纯概念
// （CONCEPT），不需要注册对象引用。
func mapKnowledgeTargetKind(targetType string) string { return mapTermTargetKind(targetType) }

func parseMemberTargetCode(value string) (string, string, error) {
	parts := strings.SplitN(value, "::", 2)
	if len(parts) != 2 {
		return "", "", ErrInvalidImportRow
	}
	dimensionCode := strings.TrimSpace(parts[0])
	memberValue := strings.TrimSpace(parts[1])
	if !importCodePattern.MatchString(dimensionCode) || memberValue == "" || len(memberValue) > 512 ||
		strings.ContainsAny(memberValue, "\x00\r\n\t") {
		return "", "", ErrInvalidImportRow
	}
	return dimensionCode, memberValue, nil
}

func expressionReferences(raw string) map[string][]string {
	result := map[string][]string{}
	if strings.TrimSpace(raw) == "" {
		return result
	}
	var document any
	if json.Unmarshal([]byte(raw), &document) != nil {
		return result
	}
	walkExpressionReferences(document, result)
	return result
}

func walkExpressionReferences(value any, result map[string][]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "metricCode":
				if code, ok := child.(string); ok {
					result["METRIC"] = append(result["METRIC"], strings.TrimSpace(code))
				}
			case "measureCode":
				if code, ok := child.(string); ok {
					result["MEASURE"] = append(result["MEASURE"], strings.TrimSpace(code))
				}
			case "dimensionCode":
				if code, ok := child.(string); ok {
					result["DIMENSION"] = append(result["DIMENSION"], strings.TrimSpace(code))
				}
			case "metricCodes", "measureCodes", "dimensionCodes":
				kind := strings.ToUpper(strings.TrimSuffix(key, "Codes"))
				if items, ok := child.([]any); ok {
					for _, item := range items {
						if code, ok := item.(string); ok {
							result[kind] = append(result[kind], strings.TrimSpace(code))
						}
					}
				}
			}
			walkExpressionReferences(child, result)
		}
	case []any:
		for _, child := range typed {
			walkExpressionReferences(child, result)
		}
	}
}
