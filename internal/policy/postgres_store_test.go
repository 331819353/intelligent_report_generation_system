package policy

import (
	"strings"
	"testing"
)

func TestValidatePolicyExpressionChecksFieldReferencesAndShape(t *testing.T) {
	fields := map[string]bool{"region_code": true}
	valid := Expression{Type: "EQUALS", Left: &Expression{Type: "FIELD_REF", FieldCode: "region_code"}, Right: &Expression{Type: "LITERAL", Value: "CN-SH"}}
	if err := validatePolicyExpression(valid, fields, 0); err != nil {
		t.Fatalf("valid expression error=%v", err)
	}

	unknown := valid
	unknown.Left = &Expression{Type: "FIELD_REF", FieldCode: "removed_field"}
	if err := validatePolicyExpression(unknown, fields, 0); err == nil || !strings.Contains(err.Error(), "unknown dataset field") {
		t.Fatalf("unknown field error=%v", err)
	}

	if err := validatePolicyExpression(Expression{Type: "AND", Children: []Expression{{Type: "LITERAL", Value: true}}}, fields, 0); err == nil {
		t.Fatal("单子节点逻辑表达式不应通过发布策略校验")
	}
}

func TestValidateColumnPolicyDefinitionRejectsIncompleteRules(t *testing.T) {
	tests := []struct {
		name   string
		policy ColumnPolicy
		valid  bool
	}{
		{name: "允许", policy: ColumnPolicy{FieldCode: "revenue", PolicyType: "ALLOW"}, valid: true},
		{name: "合法掩码", policy: ColumnPolicy{FieldCode: "revenue", PolicyType: "MASK", MaskRule: MaskRule{Type: "KEEP_PREFIX_SUFFIX", PrefixLength: 1, SuffixLength: 1}}, valid: true},
		{name: "非法掩码", policy: ColumnPolicy{FieldCode: "revenue", PolicyType: "MASK", MaskRule: MaskRule{Type: "UNKNOWN"}}, valid: false},
		{name: "聚合列表为空", policy: ColumnPolicy{FieldCode: "revenue", PolicyType: "AGGREGATE_ONLY"}, valid: false},
		{name: "合法聚合", policy: ColumnPolicy{FieldCode: "revenue", PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"SUM"}}, valid: true},
		{name: "未知策略", policy: ColumnPolicy{FieldCode: "revenue", PolicyType: "UNKNOWN"}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateColumnPolicyDefinition(test.policy)
			if (err == nil) != test.valid {
				t.Fatalf("error=%v, valid=%v", err, test.valid)
			}
		})
	}
}
