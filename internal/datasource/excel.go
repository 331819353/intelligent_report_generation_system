package datasource

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	spikeexcel "intelligent-report-generation-system/internal/spike/excel"
)

type FileAsset struct {
	ID              string         `json:"id"`
	TenantID        string         `json:"tenantId"`
	Filename        string         `json:"filename"`
	MimeType        string         `json:"mimeType"`
	CurrentVersion  int            `json:"currentVersion"`
	Version         int            `json:"version"`
	VersionID       string         `json:"versionId"`
	SizeBytes       int64          `json:"sizeBytes"`
	SHA256          string         `json:"sha256"`
	WorkbookSummary map[string]any `json:"workbookSummary"`
}
type FileVersion struct {
	FileAsset
	StorageBucket string
	StorageKey    string
	ParseConfig   map[string]any
}

// FileTableData 是从固定文件版本解析出的只读二维表及规范类型。
type FileTableData struct {
	Name    string
	Columns []string
	Types   map[string]string
	Rows    [][]string
}

type ObjectStorage interface {
	Put(context.Context, string, string, io.Reader, int64, string) error
	Get(context.Context, string, string) (io.ReadCloser, error)
	Delete(context.Context, string, string) error
}

type ExcelManager struct {
	repo    *PostgresRepository
	storage ObjectStorage
	bucket  string
}

// NewExcelManager 组合文件版本仓储、对象存储和上传桶配置。
func NewExcelManager(repo *PostgresRepository, storage ObjectStorage, bucket string) *ExcelManager {
	return &ExcelManager{repo: repo, storage: storage, bucket: bucket}
}

// Upload 校验配额与文件格式，上传新版本，并在失败时回滚已写入对象。
func (m *ExcelManager) Upload(ctx context.Context, tenantID, assetID, filename, mimeType string, input io.Reader, size int64, config map[string]any) (FileAsset, error) {
	// 在写对象存储前完整读取并解析文件：只有能按租户限制安全解析、且能生成稳定
	// 元数据的内容才会成为版本。请求声明的 size 也必须与实际字节数一致。
	quota, err := m.repo.Quota(ctx, tenantID)
	if err != nil {
		return FileAsset{}, err
	}
	if size <= 0 || size > quota.MaxExcelFileBytes {
		return FileAsset{}, errors.New("excel file size exceeds tenant quota")
	}
	data, err := io.ReadAll(io.LimitReader(input, quota.MaxExcelFileBytes+1))
	if err != nil {
		return FileAsset{}, err
	}
	if int64(len(data)) != size || int64(len(data)) > quota.MaxExcelFileBytes {
		return FileAsset{}, errors.New("excel file size is invalid")
	}
	limits := spikeexcel.DefaultLimits()
	limits.MaxFileBytes = quota.MaxExcelFileBytes
	csvOptions, err := parseCSVOptions(config)
	if err != nil {
		return FileAsset{}, err
	}
	book, err := spikeexcel.ReadWithOptions(filename, bytes.NewReader(data), size, limits, csvOptions)
	if err != nil {
		return FileAsset{}, err
	}
	metadata, err := inferWorkbook(book, config)
	if err != nil {
		return FileAsset{}, err
	}
	isNew := assetID == ""
	if isNew {
		assetID = uuid.NewString()
	}
	version, err := m.repo.NextFileVersion(ctx, tenantID, assetID)
	if err != nil {
		return FileAsset{}, err
	}
	if !isNew && version == 1 {
		return FileAsset{}, errors.New("file asset not found")
	}
	versionID := uuid.NewString()
	key := fmt.Sprintf("%s/%s/v%d/%s/%s", tenantID, assetID, version, versionID, filepath.Base(filename))
	// 先写对象再登记版本；数据库失败时删除孤立对象以保持一致性。控制库提交后，
	// 对象键包含 versionID，后续版本不会覆盖这份不可变内容。
	if err := m.storage.Put(ctx, m.bucket, key, bytes.NewReader(data), size, mimeType); err != nil {
		return FileAsset{}, err
	}
	sum := sha256.Sum256(data)
	asset := FileAsset{ID: assetID, TenantID: tenantID, Filename: filename, MimeType: mimeType, CurrentVersion: version, Version: version, VersionID: versionID, SizeBytes: size, SHA256: hex.EncodeToString(sum[:]), WorkbookSummary: workbookSummary(metadata)}
	if err := m.repo.SaveFileVersion(ctx, asset, m.bucket, key, config); err != nil {
		_ = m.storage.Delete(ctx, m.bucket, key)
		return FileAsset{}, err
	}
	return asset, nil
}

// Current 返回文件资产当前生效版本的对象位置和解析配置。
func (m *ExcelManager) Current(ctx context.Context, tenantID, assetID string) (FileVersion, error) {
	return m.repo.CurrentFileVersion(ctx, tenantID, assetID)
}

// ReadVersionTables 精确读取不可变文件版本，并验证对象大小、摘要和原始解析配置。
func (m *ExcelManager) ReadVersionTables(ctx context.Context, tenantID, versionID string, maxFileBytes int64) (FileVersion, []FileTableData, error) {
	// 运行时必须按 versionID 读取不可变对象，不能回退到资产“当前版本”；否则
	// 已保存数据集会在文件替换后静默改变含义。
	version, err := m.repo.FileVersionByID(ctx, tenantID, versionID)
	if err != nil {
		return FileVersion{}, nil, err
	}
	if maxFileBytes <= 0 || version.SizeBytes <= 0 || version.SizeBytes > maxFileBytes {
		return FileVersion{}, nil, errors.New("file version exceeds execution quota")
	}
	body, err := m.storage.Get(ctx, version.StorageBucket, version.StorageKey)
	if err != nil {
		return FileVersion{}, nil, err
	}
	defer body.Close()
	// 以元数据声明大小而不是租户总配额作为读取边界；对象若被异常放大，只多读
	// 一个字节即可失败，避免小文件元数据对应的大对象占满进程内存。
	data, err := io.ReadAll(io.LimitReader(body, version.SizeBytes+1))
	if err != nil {
		return FileVersion{}, nil, err
	}
	if int64(len(data)) != version.SizeBytes || int64(len(data)) > maxFileBytes {
		return FileVersion{}, nil, errors.New("file version object size does not match metadata")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != version.SHA256 {
		return FileVersion{}, nil, errors.New("file version checksum verification failed")
	}
	limits := spikeexcel.DefaultLimits()
	limits.MaxFileBytes = maxFileBytes
	csvOptions, err := parseCSVOptions(version.ParseConfig)
	if err != nil {
		return FileVersion{}, nil, err
	}
	book, err := spikeexcel.ReadWithOptions(version.Filename, bytes.NewReader(data), version.SizeBytes, limits, csvOptions)
	if err != nil {
		return FileVersion{}, nil, err
	}
	tables, err := prepareFileTables(book, version.ParseConfig)
	if err != nil {
		return FileVersion{}, nil, err
	}
	return version, tables, nil
}

// Versions 按新到旧列出文件资产的历史版本。
func (m *ExcelManager) Versions(ctx context.Context, tenantID, assetID string) ([]FileAsset, error) {
	return m.repo.ListFileVersions(ctx, tenantID, assetID)
}

// MaxFileBytes 返回租户允许上传的单文件上限。
func (m *ExcelManager) MaxFileBytes(ctx context.Context, tenantID string) (int64, error) {
	quota, err := m.repo.Quota(ctx, tenantID)
	return quota.MaxExcelFileBytes, err
}

// Audit 记录文件上传与版本操作。
func (m *ExcelManager) Audit(ctx context.Context, tenantID, actorID, action, resourceID string, detail any) error {
	return m.repo.Audit(ctx, tenantID, actorID, action, resourceID, detail)
}

type ExcelConnector struct{ manager *ExcelManager }

// NewExcelConnector 创建将文件资产适配为统一数据源接口的连接器。
func NewExcelConnector(manager *ExcelManager) *ExcelConnector {
	return &ExcelConnector{manager: manager}
}

// Type 返回 Excel/CSV 文件连接器类型。
func (c *ExcelConnector) Type() Type { return TypeExcel }

// Test 读取当前文件版本，验证格式、配额和解析参数。
func (c *ExcelConnector) Test(ctx context.Context, source Source) (TestResult, error) {
	started := time.Now()
	version, err := c.manager.Current(ctx, source.TenantID, source.FileAssetID)
	if err != nil {
		return TestResult{}, err
	}
	body, err := c.manager.storage.Get(ctx, version.StorageBucket, version.StorageKey)
	if err != nil {
		return TestResult{}, err
	}
	defer body.Close()
	limits := spikeexcel.DefaultLimits()
	limits.MaxFileBytes = source.RuntimeQuota.MaxExcelFileBytes
	csvOptions, err := parseCSVOptions(version.ParseConfig)
	if err != nil {
		return TestResult{}, err
	}
	if _, err := spikeexcel.ReadWithOptions(version.Filename, body, version.SizeBytes, limits, csvOptions); err != nil {
		return TestResult{}, err
	}
	return TestResult{ServerVersion: "Excel " + strings.TrimPrefix(strings.ToLower(filepath.Ext(version.Filename)), "."), LatencyMS: time.Since(started).Milliseconds()}, nil
}

// Sync 读取工作簿并推断表、字段和数据类型等技术元数据。
func (c *ExcelConnector) Sync(ctx context.Context, source Source) (SyncResult, error) {
	version, err := c.manager.Current(ctx, source.TenantID, source.FileAssetID)
	if err != nil {
		return SyncResult{}, err
	}
	body, err := c.manager.storage.Get(ctx, version.StorageBucket, version.StorageKey)
	if err != nil {
		return SyncResult{}, err
	}
	defer body.Close()
	limits := spikeexcel.DefaultLimits()
	limits.MaxFileBytes = source.RuntimeQuota.MaxExcelFileBytes
	csvOptions, err := parseCSVOptions(version.ParseConfig)
	if err != nil {
		return SyncResult{}, err
	}
	book, err := spikeexcel.ReadWithOptions(version.Filename, body, version.SizeBytes, limits, csvOptions)
	if err != nil {
		return SyncResult{}, err
	}
	tables, err := inferWorkbook(book, version.ParseConfig)
	if err != nil {
		return SyncResult{}, err
	}
	hash, _, err := metadataHash(tables)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Assets: len(tables), Watermark: time.Now().UTC().Format(time.RFC3339Nano), SnapshotHash: hash, Tables: tables}, nil
}

// Close 无需释放持久连接，保留接口一致性。
func (c *ExcelConnector) Close(context.Context, Source) error { return nil }

// parseCSVOptions 将持久化的 JSON 配置转换成读取器参数。Excel 文件会忽略这些参数。
func parseCSVOptions(config map[string]any) (spikeexcel.CSVOptions, error) {
	// CSV 方言来自持久化配置；字符选项被限制为单个非换行 rune，避免同一版本
	// 在同步和查询阶段被解析成不同的列边界。
	options := spikeexcel.DefaultCSVOptions()
	raw, exists := config["csvOptions"]
	if !exists || raw == nil {
		return options, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return options, errors.New("csvOptions must be an object")
	}
	if rawEncoding, exists := values["encoding"]; exists {
		value, ok := rawEncoding.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return options, errors.New("csv encoding must be a non-empty string")
		}
		options.Encoding = strings.TrimSpace(value)
	}
	if rawDelimiter, exists := values["delimiter"]; exists {
		value, ok := rawDelimiter.(string)
		if !ok || value == "" {
			return options, errors.New("csv delimiter must be a non-empty string")
		}
		delimiter, err := csvRune(value, map[string]rune{"COMMA": ',', "SEMICOLON": ';', "TAB": '\t'})
		if err != nil {
			return options, fmt.Errorf("invalid csv delimiter: %w", err)
		}
		options.Delimiter = delimiter
	}
	if rawQuote, exists := values["quote"]; exists {
		value, ok := rawQuote.(string)
		if !ok || value == "" {
			return options, errors.New("csv quote must be a non-empty string")
		}
		quote, err := csvRune(value, nil)
		if err != nil {
			return options, fmt.Errorf("invalid csv quote: %w", err)
		}
		options.Quote = quote
	}
	options.LazyQuotes = boolConfig(values, "lazyQuotes", false)
	options.TrimLeadingSpace = boolConfig(values, "trimLeadingSpace", false)
	return options, nil
}

// csvRune 把单字符或预定义别名转换为 CSV 方言字符。
func csvRune(value string, aliases map[string]rune) (rune, error) {
	if alias, ok := aliases[strings.ToUpper(strings.TrimSpace(value))]; ok {
		return alias, nil
	}
	characters := []rune(value)
	if len(characters) != 1 || characters[0] == '\r' || characters[0] == '\n' {
		return 0, errors.New("must be exactly one non-newline character")
	}
	return characters[0], nil
}

// inferWorkbook 把工作簿内容推断为统一的技术元数据，供后续资产同步复用。
func inferWorkbook(book spikeexcel.Workbook, config map[string]any) ([]MetadataTable, error) {
	// 元数据推断与运行时 prepareFileTables 共用表头、空行和类型规则。同步阶段
	// 发布的列白名单因此能在固定版本查询时被完全复现。
	headerRow := intConfig(config, "headerRow", 1)
	if headerRow < 1 {
		return nil, errors.New("headerRow must be greater than zero")
	}
	selected := stringSet(config["selectedSheets"])
	overrides, _ := config["columnOverrides"].(map[string]any)
	var tables []MetadataTable
	for _, sheet := range book.Sheets {
		if len(selected) > 0 && !selected[sheet.Name] {
			continue
		}
		if len(sheet.Rows) < headerRow {
			return nil, fmt.Errorf("sheet %s does not contain header row", sheet.Name)
		}
		headers := deduplicateHeaders(sheet.Rows[headerRow-1])
		dataRows := sheet.Rows[headerRow:]
		if boolConfig(config, "skipEmptyRows", true) {
			filtered := dataRows[:0]
			for _, row := range dataRows {
				if !emptyRow(row) {
					filtered = append(filtered, row)
				}
			}
			dataRows = filtered
		}
		columns := make([]MetadataColumn, 0, len(headers))
		for index, name := range headers {
			values := make([]string, 0)
			for _, row := range dataRows {
				if index < len(row) && strings.TrimSpace(row[index]) != "" {
					value := strings.TrimSpace(row[index])
					if isExcelError(value) {
						return nil, fmt.Errorf("sheet %s column %s contains formula error %s", sheet.Name, name, value)
					}
					values = append(values, value)
				}
			}
			canonical := inferType(values)
			if override, ok := overrides[sheet.Name+"."+name].(string); ok && validCanonical(override) {
				canonical = override
			}
			columns = append(columns, MetadataColumn{Name: name, OrdinalPosition: index + 1, NativeType: canonical, CanonicalType: canonical, Nullable: len(values) < len(dataRows)})
		}
		tables = append(tables, MetadataTable{CatalogName: "", SchemaName: "WORKBOOK", Name: sheet.Name, Type: "SHEET", Columns: columns, PrimaryKeyColumns: []string{}, Constraints: []MetadataConstraint{}, Indexes: []MetadataIndex{}})
	}
	if len(tables) == 0 {
		return nil, errors.New("no worksheet selected")
	}
	return tables, nil
}

// prepareFileTables 使用与元数据同步相同的表头、空行和类型规则生成执行输入。
func prepareFileTables(book spikeexcel.Workbook, config map[string]any) ([]FileTableData, error) {
	// 保留原始单元格文本，只附带同步阶段确定的规范类型；真正的类型转换在查询
	// 求值时发生，错误便可以准确定位到工作表、行和列。
	metadata, err := inferWorkbook(book, config)
	if err != nil {
		return nil, err
	}
	metadataByName := make(map[string]MetadataTable, len(metadata))
	for _, table := range metadata {
		metadataByName[table.Name] = table
	}
	headerRow := intConfig(config, "headerRow", 1)
	selected := stringSet(config["selectedSheets"])
	result := make([]FileTableData, 0, len(metadata))
	for _, sheet := range book.Sheets {
		if len(selected) > 0 && !selected[sheet.Name] {
			continue
		}
		definition, ok := metadataByName[sheet.Name]
		if !ok || len(sheet.Rows) < headerRow {
			continue
		}
		columns := deduplicateHeaders(sheet.Rows[headerRow-1])
		rows := append([][]string(nil), sheet.Rows[headerRow:]...)
		if boolConfig(config, "skipEmptyRows", true) {
			filtered := make([][]string, 0, len(rows))
			for _, row := range rows {
				if !emptyRow(row) {
					filtered = append(filtered, row)
				}
			}
			rows = filtered
		}
		types := make(map[string]string, len(definition.Columns))
		for _, column := range definition.Columns {
			types[column.Name] = column.CanonicalType
		}
		result = append(result, FileTableData{Name: sheet.Name, Columns: columns, Types: types, Rows: rows})
	}
	return result, nil
}

// deduplicateHeaders 补全空表头并为重复名称追加稳定序号。
func deduplicateHeaders(row []string) []string {
	// 空表头生成 column_N，重复表头追加出现序号。同一文件重复同步时会得到相同
	// 技术列名，避免已保存 DSL 因表头规范化漂移而失效。
	seen := map[string]int{}
	out := make([]string, len(row))
	for index, value := range row {
		value = strings.TrimSpace(value)
		if value == "" {
			value = fmt.Sprintf("column_%d", index+1)
		}
		seen[value]++
		if seen[value] > 1 {
			value = fmt.Sprintf("%s_%d", value, seen[value])
		}
		out[index] = value
	}
	return out
}

// inferType 按布尔、整数、小数、日期时间的优先级推断规范类型。
func inferType(values []string) string {
	// 采用“全部非空样本都能解析”的最窄共同类型；只要出现混合值就回退 TEXT，
	// 避免少数异常单元格直到预览阶段才暴露为批量类型错误。
	if len(values) == 0 {
		return "TEXT"
	}
	allBool, allInt, allDecimal, allDate, allDateTime, allTime := true, true, true, true, true, true
	for _, value := range values {
		lower := strings.ToLower(value)
		if lower != "true" && lower != "false" && lower != "yes" && lower != "no" {
			allBool = false
		}
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			allInt = false
		}
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			allDecimal = false
		}
		allDate = allDate && parsesAny(value, []string{"2006-01-02", "2006/01/02", "01/02/2006"})
		allDateTime = allDateTime && parsesAny(value, []string{time.RFC3339, "2006-01-02 15:04:05", "01/02/06 15:04", "2006/01/02 15:04:05"})
		allTime = allTime && parsesAny(value, []string{"15:04", "15:04:05"})
	}
	if allBool {
		return "BOOLEAN"
	}
	if allInt {
		return "NUMBER"
	}
	if allDecimal {
		return "DECIMAL"
	}
	if allDate {
		return "DATE"
	}
	if allDateTime {
		return "DATETIME"
	}
	if allTime {
		return "TIME"
	}
	return "TEXT"
}

// validCanonical 判断字符串是否属于支持的规范数据类型。
func validCanonical(value string) bool {
	switch value {
	case "TEXT", "NUMBER", "DECIMAL", "BOOLEAN", "DATE", "DATETIME", "TIME", "BINARY":
		return true
	}
	return false
}

// intConfig 兼容 JSON 数值类型读取整数配置。
func intConfig(config map[string]any, key string, fallback int) int {
	if value, ok := config[key].(float64); ok {
		return int(value)
	}
	if value, ok := config[key].(int); ok {
		return value
	}
	return fallback
}

// boolConfig 读取布尔配置或返回默认值。
func boolConfig(config map[string]any, key string, fallback bool) bool {
	if value, ok := config[key].(bool); ok {
		return value
	}
	return fallback
}

// emptyRow 判断一行是否全部为空白单元格。
func emptyRow(row []string) bool {
	for _, value := range row {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

// parsesAny 判断文本是否匹配任一日期时间格式。
func parsesAny(value string, layouts []string) bool {
	for _, layout := range layouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

// isExcelError 识别常见 Excel 错误值，避免将其纳入类型推断。
func isExcelError(value string) bool {
	switch strings.ToUpper(value) {
	case "#NULL!", "#DIV/0!", "#VALUE!", "#REF!", "#NAME?", "#NUM!", "#N/A":
		return true
	}
	return false
}

// stringSet 将字符串数组配置转换为便于查找的集合。
func stringSet(value any) map[string]bool {
	out := map[string]bool{}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if text, ok := item.(string); ok {
				out[text] = true
			}
		}
	}
	return out
}

// workbookSummary 汇总工作表和字段数量，供测试连接响应展示。
func workbookSummary(tables []MetadataTable) map[string]any {
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = table.Name
	}
	return map[string]any{"sheetCount": len(tables), "sheets": names}
}
