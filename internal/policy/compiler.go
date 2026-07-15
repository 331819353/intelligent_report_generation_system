package policy

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type CompiledFilter struct {
	SQL  string
	Args []any
}

// CompileRows 将行级策略编译为参数化 SQL 条件，并保证拒绝规则优先。
func CompileRows(policies []RowPolicy, scope UserScope) (CompiledFilter, error) {
	if scope.TenantID == "" || scope.UserID == "" {
		return CompiledFilter{}, errors.New("tenant and user scope are required")
	}
	sort.SliceStable(policies, func(i, j int) bool { return policies[i].Priority < policies[j].Priority })
	var allowAND, allowOR, denies []string
	args := []any{}
	// 允许策略按组合模式分组，拒绝策略最终统一取反并与允许条件相交。
	for _, p := range policies {
		sql, err := compileExpression(p.Expression, scope.Attributes, &args)
		if err != nil {
			return CompiledFilter{}, fmt.Errorf("policy %s: %w", p.ID, err)
		}
		if p.Effect == "DENY" || p.CombineMode == "DENY_OVERRIDE" {
			denies = append(denies, sql)
			continue
		}
		switch p.CombineMode {
		case "AND":
			allowAND = append(allowAND, sql)
		case "OR":
			allowOR = append(allowOR, sql)
		default:
			return CompiledFilter{}, fmt.Errorf("policy %s: unsupported combine mode", p.ID)
		}
	}
	parts := []string{}
	if len(allowAND) > 0 {
		parts = append(parts, "("+strings.Join(allowAND, " AND ")+")")
	}
	if len(allowOR) > 0 {
		parts = append(parts, "("+strings.Join(allowOR, " OR ")+")")
	}
	if len(denies) > 0 {
		parts = append(parts, "NOT ("+strings.Join(denies, " OR ")+")")
	}
	if len(parts) == 0 {
		return CompiledFilter{SQL: "TRUE"}, nil
	}
	return CompiledFilter{SQL: strings.Join(parts, " AND "), Args: args}, nil
}

// compileExpression 递归编译受限表达式树；值全部参数化，仅白名单字段可进入 SQL。
func compileExpression(e Expression, attrs map[string]any, args *[]any) (string, error) {
	switch e.Type {
	case "FIELD_REF":
		if !identifier.MatchString(e.FieldCode) {
			return "", errors.New("invalid field reference")
		}
		return `"` + e.FieldCode + `"`, nil
	case "USER_ATTRIBUTE_REF":
		value, ok := attrs[e.Attribute]
		if !ok {
			return "", fmt.Errorf("missing user attribute %q", e.Attribute)
		}
		*args = append(*args, value)
		return fmt.Sprintf("$%d", len(*args)), nil
	case "LITERAL":
		*args = append(*args, e.Value)
		return fmt.Sprintf("$%d", len(*args)), nil
	case "EQUALS", "NOT_EQUALS", "IN":
		if e.Left == nil || e.Right == nil {
			return "", errors.New("binary expression requires left and right")
		}
		left, err := compileExpression(*e.Left, attrs, args)
		if err != nil {
			return "", err
		}
		right, err := compileExpression(*e.Right, attrs, args)
		if err != nil {
			return "", err
		}
		op := map[string]string{"EQUALS": "=", "NOT_EQUALS": "<>", "IN": "= ANY"}[e.Type]
		if e.Type == "IN" {
			return fmt.Sprintf("(%s %s(%s))", left, op, right), nil
		}
		return fmt.Sprintf("(%s %s %s)", left, op, right), nil
	case "AND", "OR":
		if len(e.Children) < 2 {
			return "", errors.New("logical expression requires at least two children")
		}
		compiled := make([]string, 0, len(e.Children))
		for _, child := range e.Children {
			part, err := compileExpression(child, attrs, args)
			if err != nil {
				return "", err
			}
			compiled = append(compiled, part)
		}
		return "(" + strings.Join(compiled, " "+e.Type+" ") + ")", nil
	default:
		return "", fmt.Errorf("unsupported expression type %q", e.Type)
	}
}

// CompileColumn 返回应用列级策略后的投影表达式。
func CompileColumn(field string, p *ColumnPolicy, ctx QueryContext) (string, error) {
	plan, err := CompileColumnPlan(field, p, ctx)
	return plan.Projection, err
}

// CompileColumnPlan 将拒绝、脱敏、哈希和仅聚合策略编译为投影及 HAVING 约束。
func CompileColumnPlan(field string, p *ColumnPolicy, ctx QueryContext) (ColumnPlan, error) {
	if !identifier.MatchString(field) {
		return ColumnPlan{}, errors.New("invalid field reference")
	}
	quoted := `"` + field + `"`
	if p == nil || p.PolicyType == "ALLOW" {
		return ColumnPlan{Projection: quoted}, nil
	}
	switch p.PolicyType {
	case "DENY":
		return ColumnPlan{}, errors.New("column access denied")
	case "NULLIFY":
		return ColumnPlan{Projection: "NULL"}, nil
	case "HASH":
		return ColumnPlan{Projection: "encode(digest(" + quoted + "::text, 'sha256'), 'hex')"}, nil
	case "MASK":
		if p.MaskRule.Type != "KEEP_PREFIX_SUFFIX" || p.MaskRule.PrefixLength < 0 || p.MaskRule.SuffixLength < 0 {
			return ColumnPlan{}, errors.New("invalid mask rule")
		}
		mask := p.MaskRule.MaskChar
		if mask == "" {
			mask = "*"
		}
		return ColumnPlan{Projection: fmt.Sprintf("left(%s, %d) || repeat('%s', greatest(length(%s)-%d, 0)) || right(%s, %d)", quoted, p.MaskRule.PrefixLength, strings.ReplaceAll(mask, "'", "''"), quoted, p.MaskRule.PrefixLength+p.MaskRule.SuffixLength, quoted, p.MaskRule.SuffixLength)}, nil
	case "AGGREGATE_ONLY":
		if ctx.Detail || ctx.Export && p.DenyDetailExport {
			return ColumnPlan{}, errors.New("column is aggregate-only")
		}
		for _, a := range p.AllowedAggregations {
			if strings.EqualFold(a, ctx.Aggregation) {
				plan := ColumnPlan{Projection: strings.ToUpper(a) + "(" + quoted + ")"}
				if p.MinimumGroupSize > 0 {
					plan.Having = fmt.Sprintf("COUNT(*) >= %d", p.MinimumGroupSize)
				}
				return plan, nil
			}
		}
		return ColumnPlan{}, errors.New("aggregation is not allowed")
	default:
		return ColumnPlan{}, errors.New("unsupported column policy")
	}
}
