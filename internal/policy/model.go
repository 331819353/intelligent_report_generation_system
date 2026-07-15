package policy

import "encoding/json"

// Expression 描述可安全编译的行级策略表达式树。
type Expression struct {
	Type      string          `json:"type"`
	FieldCode string          `json:"fieldCode,omitempty"`
	Attribute string          `json:"attribute,omitempty"`
	Value     any             `json:"value,omitempty"`
	Left      *Expression     `json:"left,omitempty"`
	Right     *Expression     `json:"right,omitempty"`
	Children  []Expression    `json:"children,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

// RowPolicy 定义允许或拒绝规则及其组合优先级。
type RowPolicy struct {
	ID, Effect, CombineMode string
	Priority                int
	Version                 int64
	Expression              Expression
}

// ColumnPolicy 定义字段访问、脱敏和聚合限制。
type ColumnPolicy struct {
	FieldCode           string
	PolicyType          string
	MaskRule            MaskRule
	AllowedAggregations []string
	MinimumGroupSize    int
	DenyDetailExport    bool
	Version             int64
}

// MaskRule 描述保留前后缀的字符掩码方式。
type MaskRule struct {
	Type         string `json:"type"`
	PrefixLength int    `json:"prefixLength"`
	SuffixLength int    `json:"suffixLength"`
	MaskChar     string `json:"maskChar"`
}

// UserScope 提供策略求值所需的租户、用户和动态属性。
type UserScope struct {
	TenantID, UserID string
	Attributes       map[string]any
}

// QueryContext 描述当前查询是否为明细、导出以及所用聚合函数。
type QueryContext struct {
	Detail, Export bool
	Aggregation    string
}

// ColumnPlan 包含安全投影表达式和可选分组规模约束。
type ColumnPlan struct {
	Projection string
	Having     string
}
