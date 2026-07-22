package datasource

import (
	"strings"
	"testing"
)

func TestSourceValidateCodeFormat(t *testing.T) {
	valid := []string{"sales_mysql", "Oracle01", "file_08288c5f576b0c70d9b444a316b4ed8d"}
	for _, code := range valid {
		source := Source{TenantID: "tenant-1", Code: code, Name: "source", Type: TypeExcel, FileAssetID: "file-1"}
		if err := source.Validate(); err != nil {
			t.Fatalf("expected code %q to be valid: %v", code, err)
		}
	}

	invalid := []string{"销售数据", "1sales", "sales-source", "sales source", "_sales", "a" + strings.Repeat("b", 128)}
	for _, code := range invalid {
		source := Source{TenantID: "tenant-1", Code: code, Name: "source", Type: TypeExcel, FileAssetID: "file-1"}
		if err := source.Validate(); err == nil {
			t.Fatalf("expected code %q to be invalid", code)
		}
	}
}
