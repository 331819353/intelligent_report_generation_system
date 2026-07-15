package policy

import (
	"strings"
	"testing"
)

func field(code string) *Expression { return &Expression{Type: "FIELD_REF", FieldCode: code} }
func attr(code string) *Expression  { return &Expression{Type: "USER_ATTRIBUTE_REF", Attribute: code} }

func TestCompileRowsUsesBoundUserAttributes(t *testing.T) {
	policies := []RowPolicy{{ID: "region", Effect: "ALLOW", CombineMode: "AND", Expression: Expression{Type: "EQUALS", Left: field("region_code"), Right: attr("region_code")}}}
	a, err := CompileRows(policies, UserScope{TenantID: "tenant-a", UserID: "user-a", Attributes: map[string]any{"region_code": "CN-SH"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := CompileRows(policies, UserScope{TenantID: "tenant-a", UserID: "user-b", Attributes: map[string]any{"region_code": "CN-BJ"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.SQL != `(("region_code" = $1))` || a.Args[0] != "CN-SH" || b.Args[0] != "CN-BJ" {
		t.Fatalf("unexpected filters: %#v %#v", a, b)
	}
	if strings.Contains(a.SQL, "CN-SH") {
		t.Fatal("user value was interpolated into SQL")
	}
}

func TestCompileRowsDenyOverrides(t *testing.T) {
	policies := []RowPolicy{
		{ID: "allow", Effect: "ALLOW", CombineMode: "AND", Expression: Expression{Type: "EQUALS", Left: field("region_code"), Right: attr("region_code")}},
		{ID: "deny", Effect: "DENY", CombineMode: "DENY_OVERRIDE", Expression: Expression{Type: "EQUALS", Left: field("status"), Right: &Expression{Type: "LITERAL", Value: "SECRET"}}},
	}
	got, err := CompileRows(policies, UserScope{TenantID: "t", UserID: "u", Attributes: map[string]any{"region_code": "CN-SH"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.SQL, "NOT") || len(got.Args) != 2 {
		t.Fatalf("unexpected compiled filter: %#v", got)
	}
}

func TestCompileRowsRejectsUnsafeField(t *testing.T) {
	_, err := CompileRows([]RowPolicy{{ID: "bad", Effect: "ALLOW", CombineMode: "AND", Expression: Expression{Type: "EQUALS", Left: field(`x"; DROP TABLE users;--`), Right: &Expression{Type: "LITERAL", Value: 1}}}}, UserScope{TenantID: "t", UserID: "u"})
	if err == nil {
		t.Fatal("unsafe field accepted")
	}
}

func TestCompileColumnPolicies(t *testing.T) {
	tests := []struct {
		name     string
		p        *ColumnPolicy
		ctx      QueryContext
		contains string
		wantErr  bool
	}{
		{"allow", nil, QueryContext{}, `"phone"`, false},
		{"deny", &ColumnPolicy{PolicyType: "DENY"}, QueryContext{}, "", true},
		{"mask", &ColumnPolicy{PolicyType: "MASK", MaskRule: MaskRule{Type: "KEEP_PREFIX_SUFFIX", PrefixLength: 3, SuffixLength: 4, MaskChar: "*"}}, QueryContext{}, "repeat", false},
		{"hash", &ColumnPolicy{PolicyType: "HASH"}, QueryContext{}, "digest", false},
		{"nullify", &ColumnPolicy{PolicyType: "NULLIFY"}, QueryContext{}, "NULL", false},
		{"aggregate", &ColumnPolicy{PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"SUM"}}, QueryContext{Aggregation: "sum"}, "SUM", false},
		{"aggregate detail denied", &ColumnPolicy{PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"SUM"}}, QueryContext{Detail: true, Aggregation: "sum"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CompileColumn("phone", tt.p, tt.ctx)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v", err)
			}
			if !tt.wantErr && !strings.Contains(got, tt.contains) {
				t.Fatalf("got %q", got)
			}
		})
	}
}

func TestAggregateOnlyAddsMinimumGroupSize(t *testing.T) {
	plan, err := CompileColumnPlan("salary", &ColumnPolicy{PolicyType: "AGGREGATE_ONLY", AllowedAggregations: []string{"AVG"}, MinimumGroupSize: 10}, QueryContext{Aggregation: "AVG"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Projection != `AVG("salary")` || plan.Having != "COUNT(*) >= 10" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}
