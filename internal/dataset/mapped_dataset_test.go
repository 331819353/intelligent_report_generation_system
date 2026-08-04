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

func TestBuildMappedDatasetDocumentKeepsDetailFactInODS(t *testing.T) {
	document, err := BuildMappedDatasetDocument(
		MappedDatasetTable{
			ID:                  "7a1ad267-b95d-4374-a629-d9980bef0c13",
			DataSourceID:        "a4008f5d-c2a7-47ee-ac68-e357d0744420",
			TableName:           "FACT_DELIVERY_EVENT",
			BusinessName:        "配送事件事实表",
			BusinessDescription: "记录每次配送状态变化的明细事件",
		},
		[]MappedDatasetColumn{
			{ColumnName: "EVENT_ID", CanonicalType: "STRING", SemanticType: "IDENTIFIER", PrimaryKey: true},
			{ColumnName: "EVENT_TIME", CanonicalType: "DATETIME", SemanticType: "DATETIME"},
			{ColumnName: "DELIVERY_FEE", CanonicalType: "DECIMAL", SemanticType: "AMOUNT"},
		},
	)
	if err != nil {
		t.Fatalf("build mapped fact dataset: %v", err)
	}
	if document.Dataset.Layer != LayerODS {
		t.Fatalf("detail fact layer = %q, want ODS", document.Dataset.Layer)
	}
	if document.Dataset.SourceMode != "" {
		t.Fatalf("detail fact source mode = %q, want empty", document.Dataset.SourceMode)
	}
	if err := Validate(document); err != nil {
		t.Fatalf("validate mapped fact dataset: %v", err)
	}
}

func TestBuildMappedDatasetDocumentClassifiesSourceAggregateAsDWS(t *testing.T) {
	document, err := BuildMappedDatasetDocument(
		MappedDatasetTable{
			ID:                  "8f3d9cad-6752-4847-a922-b66b036617ea",
			DataSourceID:        "8f4e8b80-025c-41e1-ac7b-1c6d64750467",
			TableName:           "AGG_MERCHANT_DAILY_OPS",
			BusinessName:        "商家每日运营指标汇总",
			BusinessDescription: "按日期和商家汇总订单、配送及金额指标",
		},
		[]MappedDatasetColumn{
			{ColumnName: "METRIC_DATE", CanonicalType: "DATE", SemanticType: "DATE", PrimaryKey: true},
			{ColumnName: "MERCHANT_ID", CanonicalType: "STRING", SemanticType: "IDENTIFIER", PrimaryKey: true},
			{ColumnName: "ORDER_COUNT", CanonicalType: "INTEGER", SemanticType: "QUANTITY"},
			{ColumnName: "GMV_AMOUNT", CanonicalType: "DECIMAL", SemanticType: "AMOUNT"},
		},
	)
	if err != nil {
		t.Fatalf("build mapped aggregate dataset: %v", err)
	}
	if document.Dataset.Layer != LayerDWS {
		t.Fatalf("source aggregate layer = %q, want DWS", document.Dataset.Layer)
	}
	if document.Dataset.SourceMode != SourceModePreAggregated {
		t.Fatalf("source aggregate mode = %q, want %q", document.Dataset.SourceMode, SourceModePreAggregated)
	}
	if len(document.OutputGrain.KeyFields) != 2 {
		t.Fatalf("source aggregate grain keys = %v, want date and merchant", document.OutputGrain.KeyFields)
	}
	if !document.ExecutionPolicy.Materialization.Enabled ||
		document.ExecutionPolicy.Materialization.RefreshMode != "ON_DEMAND" {
		t.Fatal("source aggregate DWS must be ready for an on-demand warehouse build")
	}
	if document.ExecutionPolicy.PreviewLimit != 10 {
		t.Fatalf("source aggregate preview limit = %d, want 10", document.ExecutionPolicy.PreviewLimit)
	}
	if err := Validate(document); err != nil {
		t.Fatalf("validate mapped aggregate dataset: %v", err)
	}
}

func TestClassifyMappedDatasetLayerRequiresAggregateEvidence(t *testing.T) {
	table := MappedDatasetTable{
		TableName:           "CUSTOMER_SNAPSHOT",
		BusinessName:        "客户快照",
		BusinessDescription: "每日客户状态快照",
	}
	columns := []MappedDatasetColumn{
		{ColumnName: "SNAPSHOT_DATE", SemanticType: "DATE"},
		{ColumnName: "CUSTOMER_ID", SemanticType: "IDENTIFIER"},
		{ColumnName: "BALANCE", SemanticType: "AMOUNT"},
	}
	if got := ClassifyMappedDatasetLayer(table, columns); got != LayerODS {
		t.Fatalf("ambiguous snapshot layer = %q, want conservative ODS", got)
	}
}

func TestMappedDatasetCanRegeneratePristineUnpublishedDraft(t *testing.T) {
	t.Parallel()
	state := mappedDatasetState{
		Deleted: true, Status: "DEPRECATED", Version: 3,
		DraftVersionID: "draft-id", DraftVersionNo: 1,
		DraftRecordVersion: 2, RevisionCount: 2, ExactCreateCount: 1,
		DSLHash: "dsl-hash", PlanHash: "plan-hash",
	}
	if !state.canRegenerateUnpublishedDraft() {
		t.Fatal("pristine system-owned unpublished draft should be recoverable after remapping")
	}

	tests := []struct {
		name   string
		mutate func(*mappedDatasetState)
	}{
		{name: "not deleted", mutate: func(value *mappedDatasetState) { value.Deleted = false }},
		{name: "published history", mutate: func(value *mappedDatasetState) { value.PublishedCount = 1 }},
		{name: "human edit", mutate: func(value *mappedDatasetState) { value.HumanDraftMutations = 1 }},
		{name: "publication request", mutate: func(value *mappedDatasetState) { value.PublicationRequests = 1 }},
		{name: "unexpected version", mutate: func(value *mappedDatasetState) { value.Version++ }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := state
			test.mutate(&candidate)
			if candidate.canRegenerateUnpublishedDraft() {
				t.Fatal("non-pristine unpublished draft must not be regenerated")
			}
		})
	}
}
