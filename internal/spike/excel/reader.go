package excel

import (
	"bytes"
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
