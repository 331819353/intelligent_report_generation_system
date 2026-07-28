package physicalname

import "testing"

func TestValidColumnSupportsParentheticalSpreadsheetHeaders(t *testing.T) {
	for _, value := range []string{
		"单价值(分析)",
		"单价值（分析）",
		"employeeScore(analysis)",
		"字段$1",
	} {
		if !ValidColumn(value) {
			t.Fatalf("safe column rejected: %q", value)
		}
	}
	for _, value := range []string{
		"订单 金额",
		"订单金额`",
		`订单金额"`,
		"订单金额;DROP",
		"schema.column",
		"1号字段",
	} {
		if ValidColumn(value) {
			t.Fatalf("unsafe column accepted: %q", value)
		}
	}
}
