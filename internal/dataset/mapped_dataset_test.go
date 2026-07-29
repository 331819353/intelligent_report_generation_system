package dataset

import "testing"

func TestBuildMappedDatasetDocumentPreservesDatabaseCodeColumnType(t *testing.T) {
	document, err := BuildMappedDatasetDocument(
		MappedDatasetTable{
			ID:           "8c96b923-1d20-4107-a834-41762422a17d",
			DataSourceID: "f2a99a50-5241-47fd-894a-17d94d3ec6e9",
			TableName:    "customers",
			BusinessName: "用户",
		},
		[]MappedDatasetColumn{{
			ColumnName:    "customer_id",
			BusinessName:  "用户编号",
			CanonicalType: "NUMBER",
			SemanticType:  "IDENTIFIER",
			PrimaryKey:    true,
		}},
	)
	if err != nil {
		t.Fatalf("build mapped dataset: %v", err)
	}
	if got := document.Fields[0].CanonicalType; got != "INTEGER" {
		t.Fatalf("database identifier type = %q, want INTEGER", got)
	}
	if got := document.Fields[0].Expression.Type; got != "FIELD_REF" {
		t.Fatalf("database identifier expression = %q, want FIELD_REF", got)
	}
}

func TestBuildMappedDatasetDocumentKeepsExcelInferredText(t *testing.T) {
	document, err := BuildMappedDatasetDocument(
		MappedDatasetTable{
			ID:            "4af38f8a-d577-4bb7-bdf7-998030ca6a1a",
			DataSourceID:  "1d096afd-0d0f-4227-b529-aed3cf3624de",
			FileVersionID: "d579979a-67a8-4429-afb0-aec61ccf0b8d",
			TableName:     "商品明细",
			BusinessName:  "商品明细",
		},
		[]MappedDatasetColumn{{
			ColumnName:    "SKU_ID",
			BusinessName:  "商品编码",
			CanonicalType: "NUMBER",
			SemanticType:  "IDENTIFIER",
		}},
	)
	if err != nil {
		t.Fatalf("build mapped dataset: %v", err)
	}
	if got := document.Fields[0].CanonicalType; got != "STRING" {
		t.Fatalf("Excel identifier type = %q, want STRING", got)
	}
}
