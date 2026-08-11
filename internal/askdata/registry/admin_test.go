package registry

import "testing"

// 未知状态值必须被拒绝，不能拼进 SQL，也不能退化成“不过滤”。
func TestObjectStatusFilterAllowlist(t *testing.T) {
	for _, status := range []string{"", StatusDraft, StatusCertified, StatusActive, StatusDeprecated} {
		if !validObjectStatusFilter(status) {
			t.Fatalf("status %q must be accepted", status)
		}
	}
	for _, status := range []string{"draft", "ANY", "DROP TABLE", "'"} {
		if validObjectStatusFilter(status) {
			t.Fatalf("status %q must be rejected", status)
		}
	}
}

// 空状态必须映射为 SQL NULL，谓词才能短路成“不过滤”。
func TestStatusArgMapsEmptyToNull(t *testing.T) {
	if statusArg("") != nil {
		t.Fatal("empty status must become SQL NULL")
	}
	if statusArg(StatusCertified) != StatusCertified {
		t.Fatal("explicit status must be passed through unchanged")
	}
}
