package registryimport

import (
	"fmt"
	"strings"
)

// validateL4 的域级一致性按行类型分组执行；BUNDLE 批的每个类型组独立评估。
func validateL4(rows []parsedImportRow, batch batchIndex, snapshot ValidationSnapshot) {
	groups := map[AssetType][]*parsedImportRow{}
	for index := range rows {
		groups[rows[index].Type] = append(groups[rows[index].Type], &rows[index])
	}
	for assetType, group := range groups {
		validateBatchNameConflicts(group, assetType, snapshot)
	}
	validateTermConflicts(rowsOfType(rows, AssetTerm))
	validateKnowledgeAuthorityConflicts(rowsOfType(rows, AssetKnowledge), snapshot)
	for index := range rows {
		row := &rows[index]
		if row.Layer != 3 {
			continue
		}
		if row.Type == AssetDimension {
			validateSensitivityPolicy(row, snapshot)
		}
		if row.Type == AssetMember {
			validateMemberAliases(row, snapshot)
		}
		warnCertifiedChange(row, row.Type, snapshot)
		markIdentityResolution(row, snapshot)
		if !hasBlockingIssue(row.Issues) {
			row.Layer = 4
		}
	}
}

// markIdentityResolution 落下创建/更新裁决：code 已存在时标注 WILL_UPDATE
// （信息事实），CREATE_ONLY 模式下则阻断该行。
func markIdentityResolution(row *parsedImportRow, snapshot ValidationSnapshot) {
	kind := referenceKindForAsset(row.Type)
	if kind == "" {
		return
	}
	column := primaryCodeColumn(row.Type)
	code := canonicalLookup(row.Values[column])
	if code == "" {
		return
	}
	if _, exists := snapshot.References[kind][code]; !exists {
		return
	}
	if row.CreateOnly {
		addIssue(row, column, ImportCreateOnlyConflict,
			"CREATE_ONLY 模式下 code 已存在，不允许创建新版本",
			"改用 UPSERT 模式或更换 code", row.Values[column])
		return
	}
	addIssue(row, column, ImportWillUpdate,
		"该 code 已存在；提交将为原对象创建新草稿版本",
		"更新既有对象（非阻断信息）", row.Values[column])
}

// validateKnowledgeAuthorityConflicts 强制“同一目标至多一个 AUTHORITATIVE
// DEFINES”：批内冲突逐行阻断，与域内既有权威定义冲突同样阻断。
func validateKnowledgeAuthorityConflicts(rows []*parsedImportRow, snapshot ValidationSnapshot) {
	byTarget := map[string][]*parsedImportRow{}
	for _, row := range rows {
		if row.Layer != 3 {
			continue
		}
		if row.Values["authority"] != "AUTHORITATIVE" || row.Values["relation"] != "DEFINES" {
			continue
		}
		key := row.Values["targetType"] + "\x00" + canonicalLookup(row.Values["targetCode"])
		byTarget[key] = append(byTarget[key], row)
		ownCode := canonicalLookup(row.Values["code"])
		if holder, conflict := snapshot.AuthoritativeDefinitions[key]; conflict && holder != ownCode {
			addIssue(row, "authority", ImportKnowledgeDefinitionConflict,
				"该目标已有权威定义；一个对象只能有一个 AUTHORITATIVE DEFINES",
				"改为 SUPPLEMENTARY 或更新既有权威词条", row.Values["targetCode"])
		}
	}
	for _, conflicting := range byTarget {
		if len(conflicting) < 2 {
			continue
		}
		for _, row := range conflicting {
			addIssue(row, "authority", ImportKnowledgeDefinitionConflict,
				"本批次内同一目标出现多个 AUTHORITATIVE DEFINES",
				"每个目标只保留一个权威定义", row.Values["targetCode"])
		}
	}
}

func validateBatchNameConflicts(rows []*parsedImportRow, assetType AssetType, snapshot ValidationSnapshot) {
	kind := referenceKindForAsset(assetType)
	if kind == "" {
		return
	}
	byName := map[string][]*parsedImportRow{}
	for _, row := range rows {
		if row.Layer != 3 {
			continue
		}
		name := row.Values["name"]
		if assetType == AssetMember {
			name = row.Values["displayLabel"]
		}
		if name == "" {
			continue
		}
		normalized := canonicalLookup(name)
		byName[normalized] = append(byName[normalized], row)
		if _, exists := snapshot.Names[kind][normalized]; exists {
			code := canonicalLookup(row.Values[primaryCodeColumn(assetType)])
			existing, sameCode := snapshot.References[kind][code]
			if !sameCode || canonicalLookup(existing.Name) != normalized {
				addIssue(row, "name", ImportNameConflict, "名称与业务域内现有对象冲突", "同类资产内唯一名称或原对象同 code 的新版本", name)
			}
		}
	}
	for _, duplicates := range byName {
		if len(duplicates) < 2 {
			continue
		}
		codes := map[string]struct{}{}
		for _, row := range duplicates {
			codes[canonicalLookup(row.Values[primaryCodeColumn(assetType)])] = struct{}{}
		}
		if len(codes) < 2 {
			continue
		}
		for _, row := range duplicates {
			addIssue(row, "name", ImportNameConflict, "本批次内多个 code 使用同一名称", "同类资产内名称唯一", row.Values["name"])
		}
	}
}

func validateTermConflicts(rows []*parsedImportRow) {
	priorityTargets := map[string]map[string][]*parsedImportRow{}
	for _, row := range rows {
		if row.Layer != 3 {
			continue
		}
		key := canonicalLookup(row.Values["term"]) + "\x00" + row.Values["priority"]
		target := row.Values["termType"] + ":" + canonicalLookup(row.Values["targetCode"])
		if priorityTargets[key] == nil {
			priorityTargets[key] = map[string][]*parsedImportRow{}
		}
		priorityTargets[key][target] = append(priorityTargets[key][target], row)
		term := canonicalLookup(row.Values["term"])
		for _, context := range splitList(row.Values["negativeContexts"]) {
			if canonicalLookup(context) == term || strings.Contains(term, canonicalLookup(context)) || strings.Contains(canonicalLookup(context), term) {
				addIssue(row, "negativeContexts", ImportNegativeContextContradiction, "负向上下文与术语本身自相矛盾", "不等于且不包含 term 的负向上下文", context)
			}
		}
	}
	for _, targets := range priorityTargets {
		if len(targets) < 2 {
			continue
		}
		for _, declarations := range targets {
			for _, row := range declarations {
				addIssue(row, "priority", ImportTermPriorityConflict, "同域同术语同优先级指向多个目标", "同一 term + priority 只对应一个 target", row.Values["targetCode"])
			}
		}
	}
}

func validateSensitivityPolicy(row *parsedImportRow, snapshot ValidationSnapshot) {
	sensitivity := row.Values["sensitivity"]
	policy := row.Values["memberIndexPolicy"]
	invalid := (sensitivity == "CONFIDENTIAL" || sensitivity == "RESTRICTED") && policy == "FULL"
	existing := snapshot.References["DIMENSION"][canonicalLookup(row.Values["code"])]
	model := snapshot.References["MODEL"][canonicalLookup(row.Values["modelCode"])]
	profileHighCardinality := snapshot.HighCardinalityFields[model.DatasetVersionID][row.Values["logicalFieldId"]]
	if (existing.HighCardinality || profileHighCardinality) && policy != "ON_DEMAND" && policy != "NONE" {
		invalid = true
	}
	if invalid {
		addIssue(row, "memberIndexPolicy", ImportSensitivityPolicyInvalid, "敏感性或高基数与成员索引策略组合非法", "CONFIDENTIAL/RESTRICTED 使用 EXACT_ONLY|NONE；高基数使用 ON_DEMAND|NONE", fmt.Sprintf("%s × %s", sensitivity, policy))
	}
}

func validateMemberAliases(row *parsedImportRow, snapshot ValidationSnapshot) {
	seen := map[string]struct{}{}
	for _, value := range []string{row.Values["canonicalValue"], row.Values["displayLabel"]} {
		normalized := canonicalLookup(value)
		if normalized == "" {
			continue
		}
		seen[normalized] = struct{}{}
		if _, conflict := snapshot.Aliases[normalized]; conflict {
			addIssue(row, "aliases", ImportNameConflict, "成员名称或别名与域内现有别名冲突", "当前维度内唯一且不误导的成员别名", value)
		}
	}
	for _, value := range splitList(row.Values["aliases"]) {
		normalized := canonicalLookup(value)
		if _, duplicate := seen[normalized]; duplicate {
			addIssue(row, "aliases", ImportNameConflict, "成员别名在同一行重复", "aliases 不重复 canonicalValue、displayLabel 或其他 alias", value)
		}
		seen[normalized] = struct{}{}
		if _, conflict := snapshot.Aliases[normalized]; conflict {
			addIssue(row, "aliases", ImportNameConflict, "成员别名与域内现有别名冲突", "当前维度内唯一且不误导的成员别名", value)
		}
	}
}

func warnCertifiedChange(row *parsedImportRow, assetType AssetType, snapshot ValidationSnapshot) {
	kind := referenceKindForAsset(assetType)
	if kind == "" {
		return
	}
	code := canonicalLookup(row.Values[primaryCodeColumn(assetType)])
	reference, exists := snapshot.References[kind][code]
	if !exists || !reference.Certified {
		return
	}
	addIssue(row, primaryCodeColumn(assetType), ImportImpactRequiresReview,
		"该 code 已有认证版本；导入将创建新草稿版本，提交前必须确认下游影响",
		"二次确认受影响报告、认证问法、KPI Bundle 与黄金用例后提交",
		reference.Code)
}
