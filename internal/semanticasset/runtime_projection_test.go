package semanticasset

import (
	"strings"
	"testing"
)

func TestSemanticSearchViewsBuildThreeGovernedViews(t *testing.T) {
	views := semanticSearchViews("example_paid_gmv", "CERTIFIED_EXAMPLE", map[string]any{
		"title":      "支付 GMV 问法",
		"aliases":    []any{"成交额", "支付金额"},
		"definition": "按支付时间统计成功支付金额",
		"question":   "华东上月支付 GMV 是多少",
		"intent":     map[string]any{"metricIds": []any{"paid_gmv"}},
		"objectIds":  []any{"paid_gmv", "sales_region"},
	})
	if len(views) != 3 ||
		!strings.Contains(views["NAME_ALIAS"], "成交额") ||
		!strings.Contains(views["DEFINITION_QUESTION"], "支付时间") ||
		!strings.Contains(views["EXAMPLE_INTENT"], "paid_gmv") {
		t.Fatalf("unexpected release search views: %+v", views)
	}
}

func TestBoundedSemanticSearchTextRemovesControlsAndCapsRunes(t *testing.T) {
	value := "safe\x00text " + strings.Repeat("数", 40000)
	result := boundedSemanticSearchText(value)
	if strings.ContainsRune(result, '\x00') || len([]rune(result)) != 32768 {
		t.Fatalf("search document was not safely bounded: runes=%d", len([]rune(result)))
	}
}
