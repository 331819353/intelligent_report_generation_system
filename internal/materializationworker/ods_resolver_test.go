package materializationworker

import (
	"testing"

	"intelligent-report-generation-system/internal/datasource"
)

func TestODSMetadataCanonicalTypeUsesDatabaseMetadataForCodes(t *testing.T) {
	for _, sourceType := range []datasource.Type{
		datasource.TypeMySQL,
		datasource.TypeOracle,
	} {
		if got := odsMetadataCanonicalType(
			sourceType, "customer_id", "用户编号", "NUMBER",
		); got != "NUMBER" {
			t.Fatalf("%s code type = %q, want NUMBER", sourceType, got)
		}
	}
}

func TestODSMetadataCanonicalTypeKeepsExcelCodesAsText(t *testing.T) {
	if got := odsMetadataCanonicalType(
		datasource.TypeExcel, "SKU_ID", "商品编码", "NUMBER",
	); got != "TEXT" {
		t.Fatalf("Excel code type = %q, want TEXT", got)
	}
}
