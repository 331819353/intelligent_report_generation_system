package registryimport

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"intelligent-report-generation-system/internal/platform/database"
)

const (
	TemplateInstructionPrefix = "# TEMPLATE_INSTRUCTIONS_V1"
	maxTemplateReferences     = 10_000
	templateDataEndRow        = 10_002
)

var (
	ErrTemplateInvalidRequest = errors.New("semantic import template request is invalid")
	ErrTemplateTooLarge       = errors.New("semantic import template reference catalog is too large")
)

type TemplateFormat string

const (
	TemplateFormatXLSX TemplateFormat = "xlsx"
	TemplateFormatCSV  TemplateFormat = "csv"
)

func (format TemplateFormat) Valid() bool {
	return format == TemplateFormatXLSX || format == TemplateFormatCSV
}

type TemplateColumn struct {
	Name        string
	Description string
	EnumValues  []string
}

type TemplateDefinition struct {
	AssetType AssetType
	Columns   []TemplateColumn
}

type TemplateReference struct {
	AssetType string
	Code      string
	Name      string
	ID        string
}

type TemplateReferenceCatalog interface {
	ListTemplateReferences(context.Context, string, string) ([]TemplateReference, error)
}

type TemplateArtifact struct {
	Filename    string
	ContentType string
	Bytes       []byte
}

type TemplateService struct{ catalog TemplateReferenceCatalog }

func NewTemplateService(catalog TemplateReferenceCatalog) *TemplateService {
	return &TemplateService{catalog: catalog}
}

func (service *TemplateService) Generate(
	ctx context.Context,
	tenantID, domainID string,
	assetType AssetType,
	format TemplateFormat,
) (TemplateArtifact, error) {
	if service == nil || service.catalog == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || !assetType.Valid() || !format.Valid() {
		return TemplateArtifact{}, ErrTemplateInvalidRequest
	}
	definition, err := TemplateDefinitionFor(assetType)
	if err != nil {
		return TemplateArtifact{}, err
	}
	references, err := service.catalog.ListTemplateReferences(ctx, tenantID, domainID)
	if err != nil {
		return TemplateArtifact{}, err
	}
	if len(references) > maxTemplateReferences {
		return TemplateArtifact{}, ErrTemplateTooLarge
	}
	for _, reference := range references {
		if !validTemplateReference(reference) {
			return TemplateArtifact{}, ErrImportConflict
		}
	}
	sort.Slice(references, func(left, right int) bool {
		first, second := references[left], references[right]
		if first.AssetType != second.AssetType {
			return first.AssetType < second.AssetType
		}
		if first.Code != second.Code {
			return first.Code < second.Code
		}
		if first.Name != second.Name {
			return first.Name < second.Name
		}
		return first.ID < second.ID
	})
	filename := "askdata-" + strings.ToLower(string(assetType)) + "-template."
	switch format {
	case TemplateFormatCSV:
		payload, err := renderCSVTemplate(definition)
		return TemplateArtifact{
			Filename: filename + "csv", ContentType: "text/csv; charset=utf-8", Bytes: payload,
		}, err
	case TemplateFormatXLSX:
		payload, err := renderXLSXTemplate(definition, references)
		return TemplateArtifact{
			Filename:    filename + "xlsx",
			ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			Bytes:       payload,
		}, err
	default:
		return TemplateArtifact{}, ErrTemplateInvalidRequest
	}
}

func TemplateDefinitionFor(assetType AssetType) (TemplateDefinition, error) {
	names, exists := templateColumnNames[assetType]
	if !exists {
		return TemplateDefinition{}, ErrTemplateInvalidRequest
	}
	columns := make([]TemplateColumn, len(names))
	for index, name := range names {
		values := append([]string(nil), templateEnumValues[name]...)
		columns[index] = TemplateColumn{
			Name: name, Description: templateColumnDescription(name, len(values) > 0),
			EnumValues: values,
		}
	}
	return TemplateDefinition{AssetType: assetType, Columns: columns}, nil
}

var templateColumnNames = map[AssetType][]string{
	AssetModel: {
		"code", "name", "datasetVersionId", "entityCode", "grainDescription",
		"grainKeyFields", "primaryTimeDimensionCode", "timeContractCode", "ownerEmail",
	},
	AssetMeasure: {
		"modelCode", "code", "name", "logicalFieldId", "defaultAggregation",
		"additivity", "unit", "currency", "nullPolicy",
	},
	AssetMetric: {
		"code", "name", "description", "modelCode", "formula", "defaultFilter",
		"unit", "currency", "additivity", "semiAdditiveTimeAggregation",
		"aggregationRestriction", "nonAdditiveDimensionCodes", "timeGrain", "dedupKey",
		"displayPrecision", "zeroDenominatorPolicy", "incompletePeriodPolicyOverride",
		"positiveExamples", "negativeExamples", "ownerEmail",
	},
	AssetMetricDimension: {"metricCode", "dimensionCode", "compatible", "role"},
	AssetDimension: {
		"modelCode", "code", "name", "description", "kind", "logicalFieldId",
		"sensitivity", "memberIndexPolicy", "groupable", "filterable", "sortable",
		"hierarchyCode", "ownerEmail",
	},
	AssetMember: {
		"dimensionCode", "canonicalValue", "displayLabel", "aliases", "hierarchyPath",
		"validFrom", "validTo", "sensitivity",
	},
	AssetHierarchy: {"code", "name", "levelOrder", "dimensionCode", "parentDimensionCode"},
	AssetRelationship: {
		"leftModelCode", "rightModelCode", "joinAst", "joinType", "cardinality",
		"fanoutPolicy", "bridgeModelCode", "validFrom", "validTo",
	},
	AssetTerm: {
		"term", "termType", "targetCode", "matchMode", "priority", "negativeContexts",
		"validFrom", "validTo", "source",
	},
	AssetCertifiedExample: {
		"question", "expectedMetricCodes", "expectedDimensionCodes", "expectedMemberValues",
		"expectedTimeExpression", "applicableRoles", "notes",
	},
	AssetKPIBundle: {
		"code", "name", "metricCodes", "defaultDimensionCodes", "defaultTimeExpression",
		"defaultChartTypes", "roleMapping", "applicableQuestionTypes",
	},
	AssetEvalCase: {
		"question", "actorRole", "expectedOutcome", "expectedMetricCodes",
		"expectedDimensionCodes", "expectedMemberValues", "expectedTimeExpression",
		"expectedResultHint", "setType", "shardId",
	},
}

var templateEnumValues = map[string][]string{
	"defaultAggregation":             {"SUM", "AVG", "MIN", "MAX", "COUNT", "COUNT_DISTINCT"},
	"additivity":                     {"FULLY_ADDITIVE", "SEMI_ADDITIVE", "NON_ADDITIVE"},
	"nullPolicy":                     {"PRESERVE", "ZERO", "REJECT"},
	"semiAdditiveTimeAggregation":    {"PERIOD_END", "PERIOD_BEGIN", "PERIOD_AVERAGE"},
	"aggregationRestriction":         {"PRE_AGGREGATE", "POST_AGGREGATE"},
	"timeGrain":                      {"NONE", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR"},
	"zeroDenominatorPolicy":          {"NULL", "ZERO"},
	"incompletePeriodPolicyOverride": {"MTD", "FULL_PERIOD", "LAST_COMPLETE"},
	"compatible":                     {"TRUE", "FALSE"},
	"kind":                           {"CATEGORICAL", "TIME", "ENTITY"},
	"sensitivity":                    {"PUBLIC", "INTERNAL", "CONFIDENTIAL", "RESTRICTED"},
	"memberIndexPolicy":              {"FULL", "EXACT_ONLY", "ON_DEMAND", "NONE"},
	"groupable":                      {"TRUE", "FALSE"},
	"filterable":                     {"TRUE", "FALSE"},
	"sortable":                       {"TRUE", "FALSE"},
	"joinType":                       {"INNER", "LEFT", "RIGHT", "FULL"},
	"cardinality":                    {"ONE_TO_ONE", "ONE_TO_MANY", "MANY_TO_ONE", "MANY_TO_MANY"},
	"fanoutPolicy":                   {"SAFE", "PRE_AGGREGATE_REQUIRED", "BRIDGE_REQUIRED", "BLOCK"},
	"termType":                       {"METRIC", "DIMENSION", "MEMBER", "TIME", "OPERATOR"},
	"matchMode":                      {"EXACT", "PREFIX", "SUFFIX", "REGEX_SAFE"},
	"source":                         {"MANUAL", "IMPORT", "CERTIFIED_EXAMPLE", "FEEDBACK_CANDIDATE"},
	"expectedOutcome":                {"DIRECT", "CLARIFY", "REFUSE"},
	"setType":                        {"TRAIN", "VALIDATION", "SEALED", "PRODUCTION_REGRESSION"},
	"shardId":                        {"1", "2", "3", "4"},
}

func templateColumnDescription(name string, enum bool) string {
	switch name {
	case "validFrom", "validTo":
		return "ISO 8601 日期或时间；留空表示开放边界"
	case "formula", "defaultFilter", "joinAst":
		return "受控表达式/AST；禁止填写原始 SQL"
	case "aliases", "grainKeyFields", "nonAdditiveDimensionCodes", "positiveExamples",
		"negativeExamples", "expectedMetricCodes", "expectedDimensionCodes",
		"expectedMemberValues", "applicableRoles", "metricCodes", "defaultDimensionCodes",
		"defaultChartTypes", "applicableQuestionTypes", "negativeContexts":
		return "多个值使用 | 分隔；值内不得包含换行"
	case "displayPrecision", "priority", "levelOrder":
		return "十进制整数"
	case "datasetVersionId":
		return "已发布且 ACTIVE 的数据集版本 UUID"
	case "ownerEmail":
		return "本租户 ACTIVE 用户邮箱"
	case "targetCode":
		return "通常填写 References 中的稳定 code；termType=MEMBER 时填写 dimensionCode::canonicalValue"
	case "expectedResultHint":
		return `JSON 合同；DIRECT 必填 expectedIrHash/expectedResultHash，可含 expectedPathHash、priority、securityExpectation、complexity、ambiguity`
	}
	if strings.HasSuffix(name, "Code") || strings.HasSuffix(name, "Codes") {
		return "使用本模板 References Sheet 中的稳定 code，不填写 UUID"
	}
	if enum {
		return "从下拉枚举中选择；不得填写表外值"
	}
	return "按列名填写；禁止控制字符和公式注入"
}

func renderCSVTemplate(definition TemplateDefinition) ([]byte, error) {
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	headers := make([]string, len(definition.Columns))
	explanations := make([]string, len(definition.Columns))
	for index, column := range definition.Columns {
		headers[index] = column.Name
		explanations[index] = column.Description
	}
	explanations[0] = TemplateInstructionPrefix + " | " + explanations[0]
	if err := writer.Write(headers); err != nil {
		return nil, err
	}
	if err := writer.Write(explanations); err != nil {
		return nil, err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func renderXLSXTemplate(
	definition TemplateDefinition,
	references []TemplateReference,
) ([]byte, error) {
	workbook := excelize.NewFile()
	defer workbook.Close()
	if err := workbook.SetSheetName("Sheet1", "Import"); err != nil {
		return nil, err
	}
	if _, err := workbook.NewSheet("References"); err != nil {
		return nil, err
	}
	if _, err := workbook.NewSheet("Instructions"); err != nil {
		return nil, err
	}
	if err := workbook.SetDocProps(&excelize.DocProperties{
		Title:   "AskData " + string(definition.AssetType) + " Import Template",
		Subject: "Governed semantic asset import template",
		Creator: "Intelligent Report Generation System",
	}); err != nil {
		return nil, err
	}
	headers := make([]any, len(definition.Columns))
	explanations := make([]any, len(definition.Columns))
	for index, column := range definition.Columns {
		headers[index] = column.Name
		explanations[index] = column.Description
	}
	explanations[0] = TemplateInstructionPrefix + " | " + definition.Columns[0].Description
	if err := workbook.SetSheetRow("Import", "A1", &headers); err != nil {
		return nil, err
	}
	if err := workbook.SetSheetRow("Import", "A2", &explanations); err != nil {
		return nil, err
	}
	if err := styleImportSheet(workbook, definition); err != nil {
		return nil, err
	}
	if err := writeReferenceSheet(workbook, references); err != nil {
		return nil, err
	}
	if err := writeInstructionSheet(workbook, definition.AssetType); err != nil {
		return nil, err
	}
	workbook.SetActiveSheet(0)
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func styleImportSheet(workbook *excelize.File, definition TemplateDefinition) error {
	headerStyle, err := workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"1F4E78"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
	})
	if err != nil {
		return err
	}
	explanationStyle, err := workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Color: "44546A", Italic: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"D9EAF7"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	if err != nil {
		return err
	}
	lastColumn, err := excelize.ColumnNumberToName(len(definition.Columns))
	if err != nil {
		return err
	}
	if err := workbook.SetCellStyle("Import", "A1", lastColumn+"1", headerStyle); err != nil {
		return err
	}
	if err := workbook.SetCellStyle("Import", "A2", lastColumn+"2", explanationStyle); err != nil {
		return err
	}
	if err := workbook.SetRowHeight("Import", 1, 28); err != nil {
		return err
	}
	if err := workbook.SetRowHeight("Import", 2, 58); err != nil {
		return err
	}
	if err := workbook.SetPanes("Import", &excelize.Panes{
		Freeze: true, YSplit: 2, TopLeftCell: "A3", ActivePane: "bottomLeft",
	}); err != nil {
		return err
	}
	if err := workbook.AutoFilter("Import", "A1:"+lastColumn+"1", nil); err != nil {
		return err
	}
	for index, column := range definition.Columns {
		columnName, _ := excelize.ColumnNumberToName(index + 1)
		width := float64(len(column.Name) + 4)
		if width < 16 {
			width = 16
		}
		if width > 30 {
			width = 30
		}
		if err := workbook.SetColWidth("Import", columnName, columnName, width); err != nil {
			return err
		}
		if len(column.EnumValues) == 0 {
			continue
		}
		validation := excelize.NewDataValidation(true)
		validation.Sqref = fmt.Sprintf("%s3:%s%d", columnName, columnName, templateDataEndRow)
		if err := validation.SetDropList(column.EnumValues); err != nil {
			return err
		}
		validation.SetInput("受控枚举", strings.Join(column.EnumValues, " | "))
		validation.SetError(excelize.DataValidationErrorStyleStop, "值不在合同内", "请从下拉列表选择")
		if err := workbook.AddDataValidation("Import", validation); err != nil {
			return err
		}
	}
	return nil
}

func writeReferenceSheet(workbook *excelize.File, references []TemplateReference) error {
	headers := []any{"assetType", "code", "name", "id"}
	if err := workbook.SetSheetRow("References", "A1", &headers); err != nil {
		return err
	}
	for index, reference := range references {
		row := []any{reference.AssetType, reference.Code, reference.Name, reference.ID}
		if err := workbook.SetSheetRow("References", fmt.Sprintf("A%d", index+2), &row); err != nil {
			return err
		}
	}
	headerStyle, err := workbook.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"548235"}, Pattern: 1},
	})
	if err != nil {
		return err
	}
	if err := workbook.SetCellStyle("References", "A1", "D1", headerStyle); err != nil {
		return err
	}
	if err := workbook.SetColWidth("References", "A", "A", 22); err != nil {
		return err
	}
	if err := workbook.SetColWidth("References", "B", "C", 28); err != nil {
		return err
	}
	if err := workbook.SetColWidth("References", "D", "D", 40); err != nil {
		return err
	}
	return workbook.SetPanes("References", &excelize.Panes{
		Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft",
	})
}

func writeInstructionSheet(workbook *excelize.File, assetType AssetType) error {
	rows := [][]any{
		{"项目", "说明"},
		{"assetType", string(assetType)},
		{"填写起始行", "Import Sheet 第 3 行；第 2 行为机器可识别的说明行，请勿改为业务数据"},
		{"引用键", "模型、指标、维度、层级等引用一律填写 code；可在 References Sheet 查阅稳定 ID"},
		{"多值分隔", "使用竖线 | 分隔；不要使用逗号，以免与 CSV 列分隔混淆"},
		{"日期", "使用 ISO 8601，例如 2026-08-07 或 2026-08-07T09:00:00+08:00"},
		{"表达式", "formula/defaultFilter/joinAst 使用受控合同语法；禁止原始 SQL"},
		{"安全边界", "导入结果只创建 DRAFT，不会直接认证或进入 Release"},
		{"常见错误", "引用 UUID 而不是 code；枚举拼写不一致；Owner 邮箱不属于本租户；多值未使用 | 分隔"},
		{"修复方式", "下载校验报告，按行列修复后重新上传；INVALID 行永不提交"},
	}
	for index, row := range rows {
		if err := workbook.SetSheetRow("Instructions", fmt.Sprintf("A%d", index+1), &row); err != nil {
			return err
		}
	}
	headerStyle, err := workbook.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"7F6000"}, Pattern: 1},
	})
	if err != nil {
		return err
	}
	if err := workbook.SetCellStyle("Instructions", "A1", "B1", headerStyle); err != nil {
		return err
	}
	if err := workbook.SetColWidth("Instructions", "A", "A", 20); err != nil {
		return err
	}
	if err := workbook.SetColWidth("Instructions", "B", "B", 90); err != nil {
		return err
	}
	return nil
}

func validTemplateReference(reference TemplateReference) bool {
	if reference.AssetType != "MODEL" && reference.AssetType != "DIMENSION" &&
		reference.AssetType != "METRIC" && reference.AssetType != "HIERARCHY" {
		return false
	}
	return boundedText(reference.Code, 128) && boundedText(reference.Name, 200) &&
		canonicalUUID(reference.ID)
}

type PostgresTemplateCatalog struct{ pool *pgxpool.Pool }

func NewPostgresTemplateCatalog(pool *pgxpool.Pool) *PostgresTemplateCatalog {
	return &PostgresTemplateCatalog{pool: pool}
}

func (catalog *PostgresTemplateCatalog) ListTemplateReferences(
	ctx context.Context,
	tenantID, domainID string,
) ([]TemplateReference, error) {
	if catalog == nil || catalog.pool == nil || !canonicalUUID(tenantID) || !canonicalUUID(domainID) {
		return nil, ErrTemplateInvalidRequest
	}
	result := []TemplateReference{}
	err := database.WithTenantTx(ctx, catalog.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH latest_models AS (
			SELECT model_id,code::text,name,status,
				row_number() OVER(PARTITION BY model_id ORDER BY version_no DESC,id DESC) AS rank
			FROM askdata.semantic_models WHERE domain_id=$1
		), latest_dimensions AS (
			SELECT dimension_id,code::text,name,status,
				row_number() OVER(PARTITION BY dimension_id ORDER BY version_no DESC,id DESC) AS rank
			FROM askdata.dimensions WHERE domain_id=$1
		), latest_hierarchies AS (
			SELECT hierarchy_id,code::text,name,status,
				row_number() OVER(PARTITION BY hierarchy_id ORDER BY version_no DESC,id DESC) AS rank
			FROM askdata.hierarchies WHERE domain_id=$1
		), catalog AS (
			SELECT 'MODEL'::text AS asset_type,code,name,model_id AS stable_id
			FROM latest_models WHERE rank=1 AND status<>'DEPRECATED'
			UNION ALL
			SELECT 'DIMENSION',code,name,dimension_id
			FROM latest_dimensions WHERE rank=1 AND status<>'DEPRECATED'
			UNION ALL
			SELECT 'METRIC',code::text,name,id
			FROM askdata.metrics WHERE domain_id=$1 AND status<>'DEPRECATED'
			UNION ALL
			SELECT 'HIERARCHY',code,name,hierarchy_id
			FROM latest_hierarchies WHERE rank=1 AND status<>'DEPRECATED'
		)
		SELECT asset_type,code,name,stable_id::text FROM catalog
		ORDER BY asset_type,code,name,stable_id LIMIT $2`, domainID, maxTemplateReferences+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var reference TemplateReference
			if err := rows.Scan(
				&reference.AssetType, &reference.Code, &reference.Name, &reference.ID,
			); err != nil {
				return err
			}
			result = append(result, reference)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(result) > maxTemplateReferences {
		return nil, ErrTemplateTooLarge
	}
	return result, nil
}
