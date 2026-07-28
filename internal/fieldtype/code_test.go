package fieldtype

import "testing"

func TestIsCodeLike(t *testing.T) {
	for _, value := range []string{
		"节点组织编码", "员工编号", "产品代码", "customer_id",
		"customerId", "ORG_CODE", "account identifier",
	} {
		if !IsCodeLike(value) {
			t.Fatalf("%q was not recognized as a code field", value)
		}
	}
	for _, value := range []string{"数量", "交易金额", "paid", "identity_name"} {
		if IsCodeLike(value) {
			t.Fatalf("%q was incorrectly recognized as a code field", value)
		}
	}
}
