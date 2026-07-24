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

func TestSourceConfigurationHashIncludesImmutableFileVersion(t *testing.T) {
	source := Source{
		Type: TypeExcel, Config: map[string]any{"headerRow": 1},
		FileAssetID: "file-1", FileVersionID: "file-version-1",
	}
	first, err := sourceConfigurationHash(source)
	if err != nil {
		t.Fatal(err)
	}
	source.FileVersionID = "file-version-2"
	second, err := sourceConfigurationHash(source)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 64 || len(second) != 64 {
		t.Fatalf("hashes do not bind file version: first=%q second=%q", first, second)
	}
}

func TestSourceValidateRejectsUnknownVisibility(t *testing.T) {
	source := Source{
		TenantID: "tenant-1", Code: "sales", Name: "Sales", Type: TypeExcel,
		FileAssetID: "file-1", Visibility: Visibility("PUBLIC_INTERNET"),
	}
	if err := source.Validate(); err == nil {
		t.Fatal("unknown visibility was accepted")
	}
}
