package dataset

import "testing"

func TestModeledDatasetPhysicalCodeUsesLayerDomainTopicAndBusiness(t *testing.T) {
	code, err := modeledDatasetPhysicalCode(
		LayerDWD,
		"领域:运营",
		[]string{"粒度:订单商品", "主题:经营分析"},
		"FACT_ORDER_ITEM",
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "dwd_operations_business_analysis_order_item" {
		t.Fatalf("modeled physical code=%q", code)
	}
}

func TestModeledDatasetPhysicalCodeSupportsGovernedChineseSemantics(t *testing.T) {
	code, err := modeledDatasetPhysicalCode(
		LayerDIM,
		"领域:企业",
		[]string{"主题:企业画像"},
		"DIM_CUSTOMER",
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "dim_enterprise_enterprise_profile_customer" {
		t.Fatalf("modeled physical code=%q", code)
	}
}

func TestModeledDatasetPhysicalCodeUsesExplicitFallbacks(t *testing.T) {
	code, err := modeledDatasetPhysicalCode(
		LayerDWS,
		"",
		nil,
		"ORDER_TREND",
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "dws_general_general_order_trend" {
		t.Fatalf("fallback modeled physical code=%q", code)
	}
}

func TestModeledDatasetBusinessNameRemovesLegacyGeneratedPrefix(t *testing.T) {
	for input, expected := range map[string]string{
		"DWD_订单商品明细事实表":     "订单商品明细事实表",
		"DIM_运营_企业画像_用户维度表": "用户维度表",
		"用户维度表":             "用户维度表",
	} {
		if actual := ModeledDatasetBusinessName(input); actual != expected {
			t.Fatalf("business name for %q=%q, want %q", input, actual, expected)
		}
	}
}
