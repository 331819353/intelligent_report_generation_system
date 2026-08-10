package registryimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
)

const (
	MaxExportAssetTypes      = 12
	MaxSynchronousExportRows = 5_000
	MaxSemanticExportRows    = 100_000
)

var (
	ErrExportInvalid       = errors.New("semantic export request is invalid")
	ErrExportNotFound      = errors.New("semantic export was not found")
	ErrExportNotReady      = errors.New("semantic export artifact is not ready")
	ErrExportExpired       = errors.New("semantic export artifact has expired")
	ErrExportTooLarge      = errors.New("semantic export exceeds the governed row limit")
	ErrExportContract      = errors.New("semantic export cannot be represented by the import contract")
	ErrExportAsyncRequired = errors.New("semantic export requires asynchronous generation")
)

var governedAssetOrder = []AssetType{
	AssetModel, AssetMeasure, AssetMetric, AssetMetricDimension, AssetDimension,
	AssetMember, AssetHierarchy, AssetRelationship, AssetTerm,
	AssetCertifiedExample, AssetKPIBundle, AssetEvalCase,
}

type ExportSelection struct {
	TenantID, DomainID, ActorID, ReleaseID string
	AssetTypes                             []AssetType
	// PinnedVersionIDs freezes an asynchronous request to the exact certified
	// versions selected when the job was created. Nil means resolve either the
	// current certified head or the requested Release manifest at read time.
	PinnedVersionIDs map[AssetType][]string
	System           bool
}

type ExportSheet struct {
	AssetType AssetType
	Rows      []map[string]string
}

type ExportDataset struct {
	Sheets                  []ExportSheet
	OmittedSensitiveMembers int
}

type ExportArtifact struct {
	Filename                string
	ContentType             string
	Bytes                   []byte
	ContentHash             string
	RowCount                int
	OmittedSensitiveMembers int
}

type ExportCatalog interface {
	CountExportRows(context.Context, ExportSelection) (int, error)
	LoadExportDataset(context.Context, ExportSelection) (ExportDataset, error)
}

type ExportService struct{ catalog ExportCatalog }

func NewExportService(catalog ExportCatalog) *ExportService { return &ExportService{catalog: catalog} }

func (service *ExportService) Count(
	ctx context.Context,
	selection ExportSelection,
) (int, error) {
	if service == nil || service.catalog == nil || validateExportSelection(selection) != nil {
		return 0, ErrExportInvalid
	}
	return service.catalog.CountExportRows(ctx, selection)
}

func (service *ExportService) Generate(
	ctx context.Context,
	selection ExportSelection,
) (ExportArtifact, error) {
	if service == nil || service.catalog == nil || validateExportSelection(selection) != nil {
		return ExportArtifact{}, ErrExportInvalid
	}
	dataset, err := service.catalog.LoadExportDataset(ctx, selection)
	if err != nil {
		return ExportArtifact{}, err
	}
	return renderExportArtifact(selection, dataset)
}

func validateExportSelection(selection ExportSelection) error {
	if !canonicalUUID(selection.TenantID) || !canonicalUUID(selection.DomainID) ||
		(!selection.System && !canonicalUUID(selection.ActorID)) || len(selection.AssetTypes) == 0 ||
		len(selection.AssetTypes) > MaxExportAssetTypes {
		return ErrExportInvalid
	}
	if selection.ReleaseID != "" && !canonicalUUID(selection.ReleaseID) {
		return ErrExportInvalid
	}
	seen := map[AssetType]struct{}{}
	for _, assetType := range selection.AssetTypes {
		if !assetType.Valid() {
			return ErrExportInvalid
		}
		if _, duplicate := seen[assetType]; duplicate {
			return ErrExportInvalid
		}
		seen[assetType] = struct{}{}
	}
	if selection.PinnedVersionIDs != nil {
		if len(selection.PinnedVersionIDs) != len(seen) {
			return ErrExportInvalid
		}
		for assetType, versionIDs := range selection.PinnedVersionIDs {
			if _, selected := seen[assetType]; !selected {
				return ErrExportInvalid
			}
			versions := make(map[string]struct{}, len(versionIDs))
			for _, versionID := range versionIDs {
				if !canonicalUUID(versionID) {
					return ErrExportInvalid
				}
				if _, duplicate := versions[versionID]; duplicate {
					return ErrExportInvalid
				}
				versions[versionID] = struct{}{}
			}
		}
	}
	return nil
}

func CanonicalAssetTypes(values []AssetType) []AssetType {
	selected := make(map[AssetType]struct{}, len(values))
	for _, value := range values {
		selected[value] = struct{}{}
	}
	result := make([]AssetType, 0, len(selected))
	for _, assetType := range governedAssetOrder {
		if _, exists := selected[assetType]; exists {
			result = append(result, assetType)
		}
	}
	return result
}

func renderExportArtifact(
	selection ExportSelection,
	dataset ExportDataset,
) (ExportArtifact, error) {
	orderedTypes := CanonicalAssetTypes(selection.AssetTypes)
	byType := make(map[AssetType]ExportSheet, len(dataset.Sheets))
	total := 0
	canonicalSheets := make([]map[string]any, 0, len(orderedTypes))
	for _, sheet := range dataset.Sheets {
		if !sheet.AssetType.Valid() {
			return ExportArtifact{}, ErrExportContract
		}
		if _, duplicate := byType[sheet.AssetType]; duplicate {
			return ExportArtifact{}, ErrExportContract
		}
		definition, err := TemplateDefinitionFor(sheet.AssetType)
		if err != nil {
			return ExportArtifact{}, err
		}
		for _, row := range sheet.Rows {
			if len(row) != len(definition.Columns) {
				return ExportArtifact{}, ErrExportContract
			}
			for _, column := range definition.Columns {
				if _, exists := row[column.Name]; !exists {
					return ExportArtifact{}, ErrExportContract
				}
			}
		}
		sortExportRows(definition, sheet.Rows)
		byType[sheet.AssetType] = sheet
	}
	if len(byType) != len(orderedTypes) {
		return ExportArtifact{}, ErrExportContract
	}
	for _, assetType := range orderedTypes {
		sheet, exists := byType[assetType]
		if !exists {
			return ExportArtifact{}, ErrExportContract
		}
		sheet.AssetType = assetType
		byType[assetType] = sheet
		total += len(sheet.Rows)
		canonicalSheets = append(canonicalSheets, map[string]any{
			"assetType": assetType, "rows": sheet.Rows,
		})
	}
	if total > MaxSemanticExportRows {
		return ExportArtifact{}, ErrExportTooLarge
	}
	content, err := registry.CanonicalValue(map[string]any{
		"version": "semantic-export-v1", "sheets": canonicalSheets,
		"omittedSensitiveMembers": dataset.OmittedSensitiveMembers,
	})
	if err != nil {
		return ExportArtifact{}, err
	}
	payload, err := renderExportWorkbook(orderedTypes, byType, dataset.OmittedSensitiveMembers)
	if err != nil {
		return ExportArtifact{}, err
	}
	scope := "current"
	if selection.ReleaseID != "" {
		scope = "release-" + selection.ReleaseID
	}
	return ExportArtifact{
		Filename:    fmt.Sprintf("askdata-semantic-%s.xlsx", scope),
		ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Bytes:       payload, ContentHash: string(askdata.HashBytes(content)), RowCount: total,
		OmittedSensitiveMembers: dataset.OmittedSensitiveMembers,
	}, nil
}

func sortExportRows(definition TemplateDefinition, rows []map[string]string) {
	sort.SliceStable(rows, func(left, right int) bool {
		for _, column := range definition.Columns {
			if rows[left][column.Name] != rows[right][column.Name] {
				return rows[left][column.Name] < rows[right][column.Name]
			}
		}
		return false
	})
}

func renderExportWorkbook(
	assetTypes []AssetType,
	sheets map[AssetType]ExportSheet,
	omittedSensitiveMembers int,
) ([]byte, error) {
	workbook := excelize.NewFile()
	defer workbook.Close()
	if err := workbook.SetDocProps(&excelize.DocProperties{
		Creator: "AskData", Title: "Governed semantic export", Subject: "semantic-export-v1",
		Created: "2000-01-01T00:00:00Z", Modified: "2000-01-01T00:00:00Z",
	}); err != nil {
		return nil, err
	}
	if len(assetTypes) == 0 {
		return nil, ErrExportInvalid
	}
	for index, assetType := range assetTypes {
		name := string(assetType)
		if len(assetTypes) == 1 {
			name = "Import"
		}
		if index == 0 {
			if err := workbook.SetSheetName("Sheet1", name); err != nil {
				return nil, err
			}
		} else if _, err := workbook.NewSheet(name); err != nil {
			return nil, err
		}
		definition, err := TemplateDefinitionFor(assetType)
		if err != nil {
			return nil, err
		}
		if err := writeExportSheet(workbook, name, definition, sheets[assetType].Rows); err != nil {
			return nil, err
		}
	}
	if _, err := workbook.NewSheet("ExportInfo"); err != nil {
		return nil, err
	}
	infoRows := [][]any{
		{"contractVersion", "semantic-export-v1"},
		{"assetTypes", joinAssetTypes(assetTypes)},
		{"omittedSensitiveMembers", omittedSensitiveMembers},
		{"sensitiveMemberPolicy", "CONFIDENTIAL/RESTRICTED member rows and MEMBER terms are omitted"},
	}
	for index, row := range infoRows {
		if err := workbook.SetSheetRow("ExportInfo", fmt.Sprintf("A%d", index+1), &row); err != nil {
			return nil, err
		}
	}
	buffer, err := workbook.WriteToBuffer()
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func writeExportSheet(
	workbook *excelize.File,
	name string,
	definition TemplateDefinition,
	rows []map[string]string,
) error {
	headers := make([]any, len(definition.Columns))
	explanations := make([]any, len(definition.Columns))
	for index, column := range definition.Columns {
		headers[index] = column.Name
		description := column.Description
		if index == 0 {
			description = TemplateInstructionPrefix + " | " + description
		}
		explanations[index] = description
	}
	if err := workbook.SetSheetRow(name, "A1", &headers); err != nil {
		return err
	}
	if err := workbook.SetSheetRow(name, "A2", &explanations); err != nil {
		return err
	}
	for rowIndex, row := range rows {
		values := make([]any, len(definition.Columns))
		for columnIndex, column := range definition.Columns {
			values[columnIndex] = row[column.Name]
		}
		if err := workbook.SetSheetRow(name, fmt.Sprintf("A%d", rowIndex+3), &values); err != nil {
			return err
		}
	}
	lastColumn, err := excelize.ColumnNumberToName(len(definition.Columns))
	if err != nil {
		return err
	}
	if err := workbook.SetPanes(name, &excelize.Panes{
		Freeze: true, YSplit: 2, TopLeftCell: "A3", ActivePane: "bottomLeft",
	}); err != nil {
		return err
	}
	if err := workbook.AutoFilter(name, "A1:"+lastColumn+"1", nil); err != nil {
		return err
	}
	return workbook.SetColWidth(name, "A", lastColumn, 22)
}

func joinAssetTypes(values []AssetType) string {
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = string(value)
	}
	return strings.Join(parts, ",")
}

func pipeJoin(values []string) string { return strings.Join(values, "|") }

func exportTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	utc := value.UTC()
	if utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 && utc.Nanosecond() == 0 {
		return utc.Format("2006-01-02")
	}
	return utc.Format(time.RFC3339)
}

func exportJSON(raw json.RawMessage, empty string) (string, error) {
	if len(raw) == 0 {
		return empty, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", ErrExportContract
	}
	canonical, err := registry.CanonicalValue(value)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}
