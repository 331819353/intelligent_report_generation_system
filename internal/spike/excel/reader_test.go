package excel

import (
	"bytes"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestReadCSVUTF8BOMVariableColumnsAndLimits(t *testing.T) {
	data := []byte("\xef\xbb\xbfid,name,amount\n1,华东,12.5\n2,华北\n")
	book, err := Read("sample.csv", bytes.NewReader(data), int64(len(data)), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Sheets) != 1 || book.Sheets[0].Name != "CSV" || len(book.Sheets[0].Rows) != 3 || book.Sheets[0].Rows[0][0] != "id" {
		t.Fatalf("unexpected csv: %#v", book)
	}
	limits := DefaultLimits()
	limits.MaxColumns = 2
	if _, err := Read("sample.csv", bytes.NewReader(data), int64(len(data)), limits); err == nil {
		t.Fatal("expected column limit error")
	}
	invalid := []byte{0xff, 0xfe, 'a'}
	if _, err := Read("invalid.csv", bytes.NewReader(invalid), int64(len(invalid)), DefaultLimits()); err == nil {
		t.Fatal("expected UTF-8 error")
	}
}

func TestReadCSVGBKSemicolonAndCustomQuote(t *testing.T) {
	plain := "id;名称;说明\r\n1;'华东;一区';'包含''引号'\r\n"
	data, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	options := DefaultCSVOptions()
	options.Encoding = "GBK"
	options.Delimiter = ';'
	options.Quote = '\''
	book, err := ReadWithOptions("sample.csv", bytes.NewReader(data), int64(len(data)), DefaultLimits(), options)
	if err != nil {
		t.Fatal(err)
	}
	rows := book.Sheets[0].Rows
	if rows[1][1] != "华东;一区" || rows[1][2] != "包含'引号" {
		t.Fatalf("unexpected GBK csv: %#v", rows)
	}
}

func TestReadCSVTabTrimSpaceAndLazyQuotes(t *testing.T) {
	data := []byte("id\tname\tnote\n1\t  \"华东\"\tunquoted\"value\n")
	options := DefaultCSVOptions()
	options.Delimiter = '\t'
	options.TrimLeadingSpace = true
	options.LazyQuotes = true
	book, err := ReadWithOptions("sample.csv", bytes.NewReader(data), int64(len(data)), DefaultLimits(), options)
	if err != nil {
		t.Fatal(err)
	}
	if got := book.Sheets[0].Rows[1]; got[1] != "华东" || got[2] != "unquoted\"value" {
		t.Fatalf("unexpected tab csv: %#v", got)
	}
}

func TestReadCSVRejectsCharacterAfterClosingQuote(t *testing.T) {
	data := []byte("id,name\n1,\"华东\"extra\n")
	if _, err := Read("sample.csv", bytes.NewReader(data), int64(len(data)), DefaultLimits()); err == nil {
		t.Fatal("expected malformed quoted field error")
	}
}

func TestReadXLSXSheetsFormulaDateAndLimits(t *testing.T) {
	f := excelize.NewFile()
	defer f.Close()
	_ = f.SetCellValue("Sheet1", "A1", "date")
	_ = f.SetCellValue("Sheet1", "B1", "amount")
	_ = f.SetCellValue("Sheet1", "C1", "formula")
	date := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	_ = f.SetCellValue("Sheet1", "A2", date)
	_ = f.SetCellValue("Sheet1", "B2", 12.5)
	_ = f.SetCellFormula("Sheet1", "C2", "B2*2")
	_ = f.SetCellValue("Sheet1", "C2", 25)
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	book, err := Read("sample.xlsx", bytes.NewReader(buf.Bytes()), int64(buf.Len()), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Sheets) != 1 || len(book.Sheets[0].Rows) != 2 || book.Sheets[0].Rows[1][2] != "25" {
		t.Fatalf("unexpected workbook: %#v", book)
	}
	limits := DefaultLimits()
	limits.MaxRows = 1
	if _, err := Read("sample.xlsx", bytes.NewReader(buf.Bytes()), int64(buf.Len()), limits); err == nil {
		t.Fatal("row limit was not enforced")
	}
	if _, err := Read("sample.xlsx", bytes.NewReader(buf.Bytes()), limits.MaxFileBytes+1, limits); err == nil {
		t.Fatal("file limit was not enforced")
	}
}
