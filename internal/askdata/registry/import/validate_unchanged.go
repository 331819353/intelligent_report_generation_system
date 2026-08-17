package registryimport

import (
	"context"

	"intelligent-report-generation-system/internal/askdata/registry"
)

// markUnchangedRows 做“内容未变化”裁决：对已带 IMPORT_WILL_UPDATE 标注的
// 有效行，把归一化后的值与当前认证版本的导出表示逐列比较，完全一致的行标注
// IMPORT_CONTENT_UNCHANGED（随后被判为 SKIPPED，不再重复建版）。
//
// 比较是保守的：任何无法证明相等的情况（导出目录不支持该类型、目标只有
// 草稿版本、敏感成员被导出策略省略、列表示不可归一化）都按“有变化”处理，
// 宁可多建一版也不吞掉一次真实更新。
func markUnchangedRows(
	ctx context.Context,
	current ExportCatalog,
	claim Claim,
	rows []parsedImportRow,
) error {
	types := map[AssetType]struct{}{}
	for index := range rows {
		row := &rows[index]
		if row.Layer == 4 && rowWillUpdate(row) && exportComparableType(row.Type) {
			types[row.Type] = struct{}{}
		}
	}
	if len(types) == 0 {
		return nil
	}
	assetTypes := make([]AssetType, 0, len(types))
	for assetType := range types {
		assetTypes = append(assetTypes, assetType)
	}
	dataset, err := current.LoadExportDataset(ctx, ExportSelection{
		TenantID: claim.TenantID, DomainID: claim.DomainID,
		AssetTypes: CanonicalAssetTypes(assetTypes), System: true,
	})
	if err != nil {
		// 现状目录不可用不是行级事实：unchanged 是优化裁决，此时按“全部有
		// 变化”继续，而不是让整批失败。
		return nil
	}
	currentByType := map[AssetType]map[string]map[string]string{}
	for _, sheet := range dataset.Sheets {
		indexed := map[string]map[string]string{}
		column := primaryCodeColumn(sheet.AssetType)
		for _, exported := range sheet.Rows {
			code := canonicalLookup(exported[column])
			if code == "" {
				continue
			}
			if _, duplicate := indexed[code]; !duplicate {
				indexed[code] = exported
			}
		}
		currentByType[sheet.AssetType] = indexed
	}
	for index := range rows {
		row := &rows[index]
		if row.Layer != 4 || !rowWillUpdate(row) || !exportComparableType(row.Type) {
			continue
		}
		exported, exists := currentByType[row.Type][canonicalLookup(row.Values[primaryCodeColumn(row.Type)])]
		if !exists {
			continue
		}
		if importedRowEqualsExported(row.Type, row.Values, exported) {
			addIssue(row, primaryCodeColumn(row.Type), ImportContentUnchanged,
				"内容与当前认证版本一致；该行将被跳过，不重复创建版本",
				"unchanged（非阻断信息）", row.Values[primaryCodeColumn(row.Type)])
		}
	}
	return nil
}

func rowWillUpdate(row *parsedImportRow) bool {
	for _, issue := range row.Issues {
		if issue.Code == ImportWillUpdate {
			return true
		}
	}
	return false
}

// exportComparableType 限定 unchanged 裁决到导出目录能提供现状表示的类型。
func exportComparableType(assetType AssetType) bool {
	for _, governed := range governedAssetOrder {
		if governed == assetType {
			return true
		}
	}
	return false
}

// importedRowEqualsExported 逐模板列比较。JSON 列比较规范化字节，列表列比较
// 集合，其余列比较去空白后的原文。任何列不可判定即视为不相等。
func importedRowEqualsExported(assetType AssetType, imported, exported map[string]string) bool {
	definition, err := TemplateDefinitionFor(assetType)
	if err != nil {
		return false
	}
	for _, column := range definition.Columns {
		left, right := imported[column.Name], exported[column.Name]
		if _, isJSON := jsonObjectImportColumns[column.Name]; isJSON {
			if !canonicalJSONEqual(left, right) {
				return false
			}
			continue
		}
		if _, isList := listImportColumns[column.Name]; isList {
			if !pipeSetEqual(left, right) {
				return false
			}
			continue
		}
		if left != right {
			return false
		}
	}
	return true
}

func canonicalJSONEqual(left, right string) bool {
	if left == "" || right == "" {
		return left == right
	}
	leftCanonical, err := registry.CanonicalJSON([]byte(left))
	if err != nil {
		return false
	}
	rightCanonical, err := registry.CanonicalJSON([]byte(right))
	if err != nil {
		return false
	}
	return string(leftCanonical) == string(rightCanonical)
}

func pipeSetEqual(left, right string) bool {
	leftSet := map[string]struct{}{}
	for _, value := range splitList(left) {
		leftSet[canonicalLookup(value)] = struct{}{}
	}
	rightSet := map[string]struct{}{}
	for _, value := range splitList(right) {
		rightSet[canonicalLookup(value)] = struct{}{}
	}
	if len(leftSet) != len(rightSet) {
		return false
	}
	for value := range leftSet {
		if _, exists := rightSet[value]; !exists {
			return false
		}
	}
	return true
}
