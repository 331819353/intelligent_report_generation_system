package askdatahttp

import "testing"

func TestNormalizeConversationLabel(t *testing.T) {
	valid, err := normalizeConversationLabel("渠道毛利率下降原因")
	if err != nil || valid != "渠道毛利率下降原因" {
		t.Fatalf("valid label = %q, %v", valid, err)
	}
	for _, invalid := range []string{"", " 前后有空格", "包含\n换行", string(make([]rune, 121))} {
		if _, err = normalizeConversationLabel(invalid); err == nil {
			t.Fatalf("label %q should be rejected", invalid)
		}
	}
}
