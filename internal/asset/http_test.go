package asset

import "testing"

func TestMetadataTableForPreviewIncludesOnlyActiveColumns(t *testing.T) {
	table := Table{
		CatalogName:   "FREEPDB1",
		SchemaName:    "TAKEOUT_USER",
		TableName:     "AGG_MERCHANT_DAILY_OPS",
		TableType:     "TABLE",
		SourceComment: "merchant operations",
	}
	columns := []Column{
		{
			ColumnName:      "MERCHANT_ID",
			OrdinalPosition: 1,
			NativeType:      "NUMBER(18)",
			CanonicalType:   "NUMBER",
			Nullable:        false,
			AssetStatus:     "ACTIVE",
		},
		{
			ColumnName:      "RETIRED_FIELD",
			OrdinalPosition: 2,
			NativeType:      "VARCHAR2(32)",
			CanonicalType:   "STRING",
			Nullable:        true,
			AssetStatus:     "INACTIVE",
		},
	}

	got := metadataTableForPreview(table, columns)
	if got.CatalogName != table.CatalogName || got.SchemaName != table.SchemaName ||
		got.Name != table.TableName || got.Type != table.TableType {
		t.Fatalf("table identity was not preserved: %#v", got)
	}
	if len(got.Columns) != 1 {
		t.Fatalf("got %d preview columns, want 1", len(got.Columns))
	}
	if got.Columns[0].Name != "MERCHANT_ID" || got.Columns[0].NativeType != "NUMBER(18)" ||
		got.Columns[0].CanonicalType != "NUMBER" || got.Columns[0].OrdinalPosition != 1 {
		t.Fatalf("active column metadata was not preserved: %#v", got.Columns[0])
	}
}
