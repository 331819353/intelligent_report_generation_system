package assetembedding

import (
	"strings"
	"testing"
)

func TestDocumentsAreDeterministicAndExcludeSamples(t *testing.T) {
	table := tableFacts{
		SourceType: "MYSQL", SchemaName: "sales", TableName: "orders", BusinessName: "销售订单",
		BusinessDescription: "订单事实", Tags: []string{"经营", " 交易 ", "经营"},
		Columns: []columnFacts{
			{ID: "2", ColumnName: "amount", BusinessName: "销售额", Tags: []string{"金额", "销售"}, SemanticType: "AMOUNT", CanonicalType: "DECIMAL", OrdinalPosition: 2},
			{ID: "1", ColumnName: "order_date", BusinessName: "下单日期", SemanticType: "DATE", CanonicalType: "DATE", OrdinalPosition: 1},
		},
	}
	first := tableDocument(table)
	table.Tags = []string{"销售", "经营"}
	second := tableDocument(table)
	if first == second {
		// Different normalized tag sets should change the document.
		t.Fatal("different tags produced the same table document")
	}
	table.Tags = []string{"经营", "销售"}
	third := tableDocument(table)
	if second != third || inputHash(second) != inputHash(third) {
		t.Fatal("tag order changed the deterministic document")
	}
	if strings.Contains(second, "password") || strings.Contains(second, "样本") {
		t.Fatal("document contains prohibited data")
	}
}

func TestColumnDocumentContainsSearchableBusinessFacts(t *testing.T) {
	table := tableFacts{SourceType: "ORACLE", TableName: "REGIONS", BusinessName: "区域维表"}
	column := columnFacts{ColumnName: "REGION_NAME", BusinessName: "区域名称", BusinessDescription: "经营区域", Tags: []string{"区域", "维度"}, SemanticType: "REGION", CanonicalType: "STRING"}
	document := columnDocument(table, column)
	for _, expected := range []string{"区域维表", "REGION_NAME", "区域名称", "区域、维度", "REGION", "STRING"} {
		if !strings.Contains(document, expected) {
			t.Fatalf("document missing %q: %s", expected, document)
		}
	}
}
