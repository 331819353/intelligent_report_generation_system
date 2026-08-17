package registryimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xuri/excelize/v2"
)

var (
	ErrImportReportNotReady = errors.New("semantic import validation report is not ready")
	ErrImportReportInvalid  = errors.New("semantic import validation report request is invalid")
)

var reportColumns = []string{"errorCode", "errorMessage", "expected", "actual"}

type ReportStore interface {
	Get(context.Context, string, string, string) (SemanticImport, error)
	ListRows(context.Context, string, string, string) ([]ImportRow, error)
}

type ReportArtifact struct {
	Filename    string
	ContentType string
	Bytes       []byte
}

type ReportService struct{ store ReportStore }

func NewReportService(store ReportStore) *ReportService { return &ReportService{store: store} }

func (service *ReportService) Generate(
	ctx context.Context,
	tenantID, domainID, importID string,
) (ReportArtifact, error) {
	if service == nil || service.store == nil || !canonicalUUID(tenantID) ||
		!canonicalUUID(domainID) || !canonicalUUID(importID) {
		return ReportArtifact{}, ErrImportReportInvalid
	}
	batch, err := service.store.Get(ctx, tenantID, domainID, importID)
	if err != nil {
		return ReportArtifact{}, err
	}
	if batch.State == StateUploaded || batch.State == StateValidating || batch.TotalRows < 1 {
		return ReportArtifact{}, ErrImportReportNotReady
	}
	rows, err := service.store.ListRows(ctx, tenantID, domainID, importID)
	if err != nil {
		return ReportArtifact{}, err
	}
	if len(rows) != batch.TotalRows {
		return ReportArtifact{}, ErrImportConflict
	}
	var payload []byte
	if batch.AssetType == AssetBundle {
		payload, err = renderBundleValidationReport(batch, rows)
	} else {
		var definition TemplateDefinition
		definition, err = TemplateDefinitionFor(batch.AssetType)
		if err != nil {
			return ReportArtifact{}, err
		}
		payload, err = renderValidationReport(definition, rows)
	}
	if err != nil {
		return ReportArtifact{}, err
	}
	return ReportArtifact{
		Filename:    fmt.Sprintf("askdata-%s-%s-validation-report.xlsx", strings.ToLower(string(batch.AssetType)), batch.ID),
		ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Bytes:       payload,
	}, nil
}

// renderBundleValidationReport 按行级资产类型分组渲染 BUNDLE 批的校验报告：
// 每个类型一个 Sheet，沿用该类型的模板列合同。
func renderBundleValidationReport(batch SemanticImport, rows []ImportRow) ([]byte, error) {
	grouped := map[AssetType][]ImportRow{}
	for _, row := range rows {
		rowType, err := rowAssetType(batch, row)
		if err != nil {
			return nil, err
		}
		grouped[rowType] = append(grouped[rowType], row)
	}
	workbook := excelize.NewFile()
	defer workbook.Close()
	first := true
	for _, assetType := range append(append([]AssetType{}, governedAssetOrder...), AssetKnowledge) {
		groupRows, exists := grouped[assetType]
		if !exists {
			continue
		}
		definition, err := TemplateDefinitionFor(assetType)
		if err != nil {
			return nil, err
		}
		sheet := string(assetType)
		if first {
			if err := workbook.SetSheetName("Sheet1", sheet); err != nil {
				return nil, err
			}
			first = false
		} else if _, err := workbook.NewSheet(sheet); err != nil {
			return nil, err
		}
		if err := writeValidationReportSheet(workbook, sheet, definition, groupRows); err != nil {
			return nil, err
		}
	}
	if first {
		return nil, ErrImportConflict
	}
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func renderValidationReport(definition TemplateDefinition, rows []ImportRow) ([]byte, error) {
	workbook := excelize.NewFile()
	defer workbook.Close()
	if err := workbook.SetSheetName("Sheet1", "Import"); err != nil {
		return nil, err
	}
	if err := writeValidationReportSheet(workbook, "Import", definition, rows); err != nil {
		return nil, err
	}
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeValidationReportSheet(
	workbook *excelize.File,
	sheet string,
	definition TemplateDefinition,
	rows []ImportRow,
) error {
	headers := make([]any, 0, len(definition.Columns)+len(reportColumns))
	explanations := make([]any, 0, len(definition.Columns)+len(reportColumns))
	for _, column := range definition.Columns {
		headers = append(headers, column.Name)
		description := column.Description
		if len(explanations) == 0 {
			description = TemplateInstructionPrefix + " | " + description
		}
		explanations = append(explanations, description)
	}
	for _, column := range reportColumns {
		headers = append(headers, column)
		explanations = append(explanations, "校验器生成；修复业务列后可保留或删除该列再重新上传")
	}
	if err := workbook.SetSheetRow(sheet, "A1", &headers); err != nil {
		return err
	}
	if err := workbook.SetSheetRow(sheet, "A2", &explanations); err != nil {
		return err
	}
	for index, row := range rows {
		values, err := validationReportRow(definition, row)
		if err != nil {
			return err
		}
		if err := workbook.SetSheetRow(sheet, fmt.Sprintf("A%d", index+3), &values); err != nil {
			return err
		}
	}
	lastColumn, err := excelize.ColumnNumberToName(len(headers))
	if err != nil {
		return err
	}
	headerStyle, err := workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"9C0006"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
	})
	if err != nil {
		return err
	}
	explanationStyle, err := workbook.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Color: "44546A", Italic: true},
		Fill:      excelize.Fill{Type: "pattern", Color: []string{"FCE4D6"}, Pattern: 1},
		Alignment: &excelize.Alignment{Vertical: "top", WrapText: true},
	})
	if err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheet, "A1", lastColumn+"1", headerStyle); err != nil {
		return err
	}
	if err := workbook.SetCellStyle(sheet, "A2", lastColumn+"2", explanationStyle); err != nil {
		return err
	}
	if err := workbook.SetPanes(sheet, &excelize.Panes{
		Freeze: true, YSplit: 2, TopLeftCell: "A3", ActivePane: "bottomLeft",
	}); err != nil {
		return err
	}
	if err := workbook.AutoFilter(sheet, "A1:"+lastColumn+"1", nil); err != nil {
		return err
	}
	if err := workbook.SetColWidth(sheet, "A", lastColumn, 22); err != nil {
		return err
	}
	return nil
}

func validationReportRow(definition TemplateDefinition, row ImportRow) ([]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(row.RawJSON, &raw); err != nil || raw == nil {
		return nil, ErrImportConflict
	}
	values := make([]any, 0, len(definition.Columns)+len(reportColumns))
	for _, column := range definition.Columns {
		value, exists := raw[column.Name]
		if !exists {
			values = append(values, "")
			continue
		}
		text, ok := value.(string)
		if !ok {
			return nil, ErrImportConflict
		}
		values = append(values, text)
	}
	issues := append([]ValidationIssue(nil), row.Errors...)
	sortIssues(issues)
	codes := make([]string, len(issues))
	messages := make([]string, len(issues))
	expected := make([]string, len(issues))
	actual := make([]string, len(issues))
	for index, issue := range issues {
		codes[index] = issue.Code
		messages[index] = issue.Message
		expected[index] = issue.Expected
		actual[index] = issue.Actual
	}
	values = append(values,
		strings.Join(codes, "\n"), strings.Join(messages, "\n"),
		strings.Join(expected, "\n"), strings.Join(actual, "\n"),
	)
	return values, nil
}
