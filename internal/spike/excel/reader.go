package excel

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/extrame/xls"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
)

type Limits struct {
	MaxFileBytes                     int64
	MaxRows, MaxColumns              int
	UnzipBytes, WorksheetMemoryBytes int64
}
type Sheet struct {
	Name string
	Rows [][]string
}
type Workbook struct{ Sheets []Sheet }

// XLSXExpandedSize reads only the ZIP central directory and returns the exact
// declared uncompressed size. The subsequent Excelize open still enforces the
// same limit while decompressing, so a forged directory cannot bypass it.
func XLSXExpandedSize(data []byte) (int64, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, err
	}
	var total uint64
	for _, file := range reader.File {
		if ^uint64(0)-total < file.UncompressedSize64 {
			return 0, errors.New("XLSX expanded size overflows")
		}
		total += file.UncompressedSize64
		if total > uint64(^uint64(0)>>1) {
			return 0, errors.New("XLSX expanded size is too large")
		}
	}
	return int64(total), nil
}

// CSVOptions 定义 CSV 的字符编码和方言。分隔符与引号均限制为单个字符。
type CSVOptions struct {
	Encoding         string
	Delimiter        rune
	Quote            rune
	LazyQuotes       bool
	TrimLeadingSpace bool
}

// DefaultCSVOptions 返回兼容 RFC 4180 常见文件的默认配置。
func DefaultCSVOptions() CSVOptions { return CSVOptions{Encoding: "UTF-8", Delimiter: ',', Quote: '"'} }

// DefaultLimits 返回防止压缩炸弹和超大工作簿占用资源的默认限制。
func DefaultLimits() Limits {
	return Limits{MaxFileBytes: 50 << 20, MaxRows: 100000, MaxColumns: 500, UnzipBytes: 512 << 20, WorksheetMemoryBytes: 16 << 20}
}

// Read 使用默认 CSV 方言读取 Excel 或 CSV 文件。
func Read(name string, r io.Reader, size int64, limits Limits) (Workbook, error) {
	return ReadWithOptions(name, r, size, limits, DefaultCSVOptions())
}

// ReadWithOptions 在统一配额保护下读取 Excel 或带方言配置的 CSV。
func ReadWithOptions(name string, r io.Reader, size int64, limits Limits, csvOptions CSVOptions) (Workbook, error) {
	if size <= 0 || size > limits.MaxFileBytes {
		return Workbook{}, errors.New("excel file size exceeds limit")
	}
	data, err := io.ReadAll(io.LimitReader(r, limits.MaxFileBytes+1))
	if err != nil {
		return Workbook{}, err
	}
	if int64(len(data)) > limits.MaxFileBytes {
		return Workbook{}, errors.New("excel file size exceeds limit")
	}
	switch strings.ToLower(name[strings.LastIndex(name, ".")+1:]) {
	case "xlsx":
		return readXLSX(data, limits)
	case "xls":
		return readXLS(data, limits)
	case "csv":
		return readCSV(data, limits, csvOptions)
	default:
		return Workbook{}, errors.New("unsupported excel extension")
	}
}

// ReadPreviewWithOptions 只为 XLSX 控制面操作构造前 maxRows 行的安全预览。
// 原始文件大小和预览所需的共享字符串等元数据仍受配额保护；超大 worksheet 的
// 剩余 XML 不会被解压。CSV/XLS 继续使用完整读取语义。
func ReadPreviewWithOptions(
	name string,
	data []byte,
	size int64,
	limits Limits,
	csvOptions CSVOptions,
	maxRows int,
) (Workbook, error) {
	if size <= 0 || size > limits.MaxFileBytes ||
		int64(len(data)) != size || int64(len(data)) > limits.MaxFileBytes {
		return Workbook{}, errors.New("excel file size exceeds limit")
	}
	if maxRows < 1 || maxRows > limits.MaxRows {
		return Workbook{}, errors.New("excel preview row limit is invalid")
	}
	if !strings.EqualFold(name[strings.LastIndex(name, ".")+1:], "xlsx") {
		return ReadWithOptions(
			name, bytes.NewReader(data), size, limits, csvOptions,
		)
	}
	preview, err := buildXLSXPreview(data, limits, maxRows)
	if err != nil {
		return Workbook{}, err
	}
	if int64(len(preview)) > limits.MaxFileBytes {
		return Workbook{}, errors.New("excel preview file size exceeds limit")
	}
	previewLimits := limits
	previewLimits.MaxRows = maxRows
	return readXLSX(preview, previewLimits)
}

// StreamSheetRows reads one worksheet row by row. Excelize spills worksheet XML
// larger than WorksheetMemoryBytes to a temporary file, so callers can process
// large sheets without retaining the full two-dimensional workbook in memory.
func StreamSheetRows(
	ctx context.Context,
	name string,
	data []byte,
	size int64,
	limits Limits,
	csvOptions CSVOptions,
	sheetName string,
	maxRows int,
	consume func([]string) error,
) error {
	if ctx == nil || consume == nil || strings.TrimSpace(sheetName) == "" ||
		maxRows < 1 || maxRows > limits.MaxRows ||
		size <= 0 || size > limits.MaxFileBytes ||
		int64(len(data)) != size || int64(len(data)) > limits.MaxFileBytes {
		return errors.New("excel stream input is invalid")
	}
	extension := strings.ToLower(name[strings.LastIndex(name, ".")+1:])
	if extension != "xlsx" {
		book, err := ReadWithOptions(
			name, bytes.NewReader(data), size, limits, csvOptions,
		)
		if err != nil {
			return err
		}
		for _, sheet := range book.Sheets {
			if sheet.Name != sheetName {
				continue
			}
			if len(sheet.Rows) > maxRows {
				return fmt.Errorf("sheet %s exceeds row limit", sheetName)
			}
			for _, row := range sheet.Rows {
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := consume(row); err != nil {
					return err
				}
			}
			return nil
		}
		return fmt.Errorf("sheet %s was not found", sheetName)
	}

	file, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		UnzipSizeLimit: limits.UnzipBytes, UnzipXMLSizeLimit: limits.WorksheetMemoryBytes,
	})
	if err != nil {
		return err
	}
	defer file.Close()
	found := false
	for _, candidate := range file.GetSheetList() {
		found = found || candidate == sheetName
	}
	if !found {
		return fmt.Errorf("sheet %s was not found", sheetName)
	}
	rows, err := file.Rows(sheetName)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > maxRows {
			return fmt.Errorf("sheet %s exceeds row limit", sheetName)
		}
		columns, err := rows.Columns()
		if err != nil {
			return err
		}
		if len(columns) > limits.MaxColumns {
			return fmt.Errorf("sheet %s exceeds column limit", sheetName)
		}
		if err := consume(columns); err != nil {
			return err
		}
	}
	return rows.Close()
}

// buildXLSXPreview 保留工作簿结构、样式和共享字符串，只截取各 worksheet 的
// 前 maxRows 个 XML row。这样 Excelize 校验的是有界预览包，而不是原文件中
// 可能达到数 GB 的完整 worksheet 展开体积。
func buildXLSXPreview(data []byte, limits Limits, maxRows int) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	var expandedBytes int64
	worksheetCount := 0
	for _, file := range reader.File {
		if !xlsxPreviewEntry(file.Name) {
			continue
		}
		var content []byte
		if strings.HasPrefix(file.Name, "xl/worksheets/") &&
			strings.HasSuffix(file.Name, ".xml") {
			content, err = trimXLSXWorksheet(
				file, maxRows, limits.WorksheetMemoryBytes,
			)
			worksheetCount++
		} else {
			content, err = readZIPEntry(
				file, limits.UnzipBytes-expandedBytes,
			)
		}
		if err != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("read XLSX preview entry %s: %w", file.Name, err)
		}
		expandedBytes += int64(len(content))
		if expandedBytes > limits.UnzipBytes {
			_ = writer.Close()
			return nil, errors.New("XLSX preview exceeds unzip limit")
		}
		method := file.Method
		if method != zip.Store && method != zip.Deflate {
			method = zip.Deflate
		}
		header := &zip.FileHeader{
			Name:     file.Name,
			Method:   method,
			Modified: file.Modified,
		}
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			_ = writer.Close()
			return nil, createErr
		}
		if _, writeErr := entry.Write(content); writeErr != nil {
			_ = writer.Close()
			return nil, writeErr
		}
	}
	if worksheetCount == 0 {
		_ = writer.Close()
		return nil, errors.New("XLSX preview contains no worksheet")
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func xlsxPreviewEntry(name string) bool {
	switch name {
	case "[Content_Types].xml",
		"_rels/.rels",
		"xl/workbook.xml",
		"xl/_rels/workbook.xml.rels",
		"xl/sharedStrings.xml",
		"xl/styles.xml":
		return true
	default:
		return strings.HasPrefix(name, "docProps/") ||
			strings.HasPrefix(name, "xl/theme/") ||
			(strings.HasPrefix(name, "xl/worksheets/") &&
				strings.HasSuffix(name, ".xml"))
	}
}

func readZIPEntry(file *zip.File, remaining int64) ([]byte, error) {
	if remaining < 0 || file.UncompressedSize64 > uint64(remaining) {
		return nil, errors.New("XLSX preview exceeds unzip limit")
	}
	body, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	content, err := io.ReadAll(io.LimitReader(body, remaining+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > remaining {
		return nil, errors.New("XLSX preview exceeds unzip limit")
	}
	return content, nil
}

func trimXLSXWorksheet(
	file *zip.File,
	maxRows int,
	maxPreviewBytes int64,
) ([]byte, error) {
	if maxPreviewBytes <= 0 {
		return nil, errors.New("XLSX worksheet preview limit is invalid")
	}
	body, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var captured bytes.Buffer
	decoder := xml.NewDecoder(io.TeeReader(
		io.LimitReader(body, maxPreviewBytes+1),
		&captured,
	))
	inSheetData := false
	rowCount := 0
	var worksheetPrefix, sheetDataPrefix string
	for {
		token, decodeErr := decoder.RawToken()
		if decodeErr == io.EOF {
			if int64(captured.Len()) > maxPreviewBytes {
				return nil, errors.New("XLSX worksheet preview exceeds memory limit")
			}
			return append([]byte(nil), captured.Bytes()...), nil
		}
		if decodeErr != nil {
			if int64(captured.Len()) > maxPreviewBytes {
				return nil, errors.New("XLSX worksheet preview exceeds memory limit")
			}
			return nil, decodeErr
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "worksheet":
				worksheetPrefix = element.Name.Space
			case "sheetData":
				inSheetData = true
				sheetDataPrefix = element.Name.Space
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "row":
				if !inSheetData {
					continue
				}
				rowCount++
				if rowCount < maxRows {
					continue
				}
				offset := decoder.InputOffset()
				if offset < 0 || offset > int64(captured.Len()) {
					return nil, errors.New("XLSX worksheet preview boundary is invalid")
				}
				preview := append(
					[]byte(nil), captured.Bytes()[:int(offset)]...,
				)
				preview = append(preview, []byte(
					"</"+qualifiedXMLName(sheetDataPrefix, "sheetData")+">"+
						"</"+qualifiedXMLName(worksheetPrefix, "worksheet")+">",
				)...)
				return preview, nil
			case "sheetData":
				inSheetData = false
			}
		}
	}
}

func qualifiedXMLName(prefix, local string) string {
	if prefix == "" {
		return local
	}
	return prefix + ":" + local
}

// readCSV 解码指定字符集，解析方言并生成单工作表模型。
func readCSV(data []byte, limits Limits, options CSVOptions) (Workbook, error) {
	if options.Delimiter == 0 || options.Quote == 0 || options.Delimiter == options.Quote || options.Delimiter == '\r' || options.Delimiter == '\n' {
		return Workbook{}, errors.New("invalid csv delimiter or quote character")
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	switch strings.ToUpper(strings.TrimSpace(options.Encoding)) {
	case "UTF-8", "UTF8":
	case "GBK":
		decoded, err := io.ReadAll(simplifiedchinese.GBK.NewDecoder().Reader(bytes.NewReader(data)))
		if err != nil {
			return Workbook{}, errors.New("csv GBK decoding failed")
		}
		data = decoded
	case "GB18030":
		decoded, err := io.ReadAll(simplifiedchinese.GB18030.NewDecoder().Reader(bytes.NewReader(data)))
		if err != nil {
			return Workbook{}, errors.New("csv GB18030 decoding failed")
		}
		data = decoded
	default:
		return Workbook{}, errors.New("unsupported csv encoding")
	}
	if !utf8.Valid(data) {
		return Workbook{}, errors.New("csv content is not valid for the configured encoding")
	}
	rows, err := parseCSVRunes([]rune(string(data)), limits, options)
	if err != nil {
		return Workbook{}, err
	}
	if len(rows) == 0 {
		return Workbook{}, errors.New("csv file is empty")
	}
	return Workbook{Sheets: []Sheet{{Name: "CSV", Rows: rows}}}, nil
}

// parseCSVRunes 自行处理方言，以支持 encoding/csv 不支持的自定义引号字符。
func parseCSVRunes(input []rune, limits Limits, options CSVOptions) ([][]string, error) {
	rows, row, field := make([][]string, 0), make([]string, 0), make([]rune, 0)
	inQuotes, quotedField, quoteClosed := false, false, false
	appendField := func() {
		value := string(field)
		if options.TrimLeadingSpace && !quotedField {
			value = strings.TrimLeft(value, " \t")
		}
		row = append(row, value)
		field = field[:0]
		quotedField = false
		quoteClosed = false
	}
	appendRow := func() error {
		appendField()
		if len(row) > limits.MaxColumns {
			return errors.New("csv exceeds column limit")
		}
		if len(rows) >= limits.MaxRows {
			return errors.New("csv exceeds row limit")
		}
		rows = append(rows, append([]string(nil), row...))
		row = row[:0]
		return nil
	}
	for index := 0; index < len(input); index++ {
		char := input[index]
		if inQuotes {
			if char == options.Quote {
				if index+1 < len(input) && input[index+1] == options.Quote {
					field = append(field, char)
					index++
				} else {
					inQuotes = false
					quoteClosed = true
				}
			} else {
				field = append(field, char)
			}
			continue
		}
		if quoteClosed && char != options.Delimiter && char != '\n' && char != '\r' {
			if !options.LazyQuotes {
				return nil, fmt.Errorf("invalid csv: unexpected character after quote at character %d", index+1)
			}
			field = append(field, options.Quote)
			quoteClosed = false
		}
		if char == options.Quote {
			// 开启忽略前导空格后，引号字段前面的空格不属于字段内容。
			if options.TrimLeadingSpace && strings.Trim(string(field), " \t") == "" {
				field = field[:0]
			}
			if len(field) == 0 {
				inQuotes, quotedField = true, true
			} else if options.LazyQuotes {
				field = append(field, char)
			} else {
				return nil, fmt.Errorf("invalid csv: unexpected quote at character %d", index+1)
			}
		} else if char == options.Delimiter {
			appendField()
		} else if char == '\n' || char == '\r' {
			if char == '\r' && index+1 < len(input) && input[index+1] == '\n' {
				index++
			}
			if err := appendRow(); err != nil {
				return nil, err
			}
		} else {
			field = append(field, char)
		}
	}
	if inQuotes && !options.LazyQuotes {
		return nil, errors.New("invalid csv: unterminated quoted field")
	}
	if len(field) > 0 || len(row) > 0 {
		if err := appendRow(); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// readXLSX 读取现代 Excel 工作簿，并逐表应用行列配额。
func readXLSX(data []byte, limits Limits) (Workbook, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: limits.UnzipBytes, UnzipXMLSizeLimit: limits.WorksheetMemoryBytes})
	if err != nil {
		return Workbook{}, err
	}
	defer f.Close()
	out := Workbook{}
	for _, name := range f.GetSheetList() {
		rows, err := f.Rows(name)
		if err != nil {
			return Workbook{}, err
		}
		sheet := Sheet{Name: name}
		for rows.Next() {
			if len(sheet.Rows) >= limits.MaxRows {
				rows.Close()
				return Workbook{}, fmt.Errorf("sheet %s exceeds row limit", name)
			}
			cols, err := rows.Columns()
			if err != nil {
				rows.Close()
				return Workbook{}, err
			}
			if len(cols) > limits.MaxColumns {
				rows.Close()
				return Workbook{}, fmt.Errorf("sheet %s exceeds column limit", name)
			}
			sheet.Rows = append(sheet.Rows, cols)
		}
		if err := rows.Close(); err != nil {
			return Workbook{}, err
		}
		out.Sheets = append(out.Sheets, sheet)
	}
	return out, nil
}

// readXLS 读取旧版二进制 Excel 工作簿并转换为统一模型。
func readXLS(data []byte, limits Limits) (Workbook, error) {
	book, err := xls.OpenReader(bytes.NewReader(data), "utf-8")
	if err != nil {
		return Workbook{}, err
	}
	out := Workbook{}
	for i := 0; i < book.NumSheets(); i++ {
		source := book.GetSheet(i)
		if source == nil {
			continue
		}
		if int(source.MaxRow)+1 > limits.MaxRows {
			return Workbook{}, fmt.Errorf("sheet %s exceeds row limit", source.Name)
		}
		sheet := Sheet{Name: source.Name}
		for rowIndex := 0; rowIndex <= int(source.MaxRow); rowIndex++ {
			row := source.Row(rowIndex)
			if row == nil {
				sheet.Rows = append(sheet.Rows, nil)
				continue
			}
			if row.LastCol() > limits.MaxColumns {
				return Workbook{}, fmt.Errorf("sheet %s exceeds column limit", source.Name)
			}
			values := make([]string, row.LastCol())
			for col := row.FirstCol(); col < row.LastCol(); col++ {
				values[col] = row.Col(col)
			}
			sheet.Rows = append(sheet.Rows, values)
		}
		out.Sheets = append(out.Sheets, sheet)
	}
	return out, nil
}
