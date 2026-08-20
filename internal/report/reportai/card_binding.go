package reportai

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"intelligent-report-generation-system/internal/report"
)

// 卡片级“识别度量与维度”。
//
// 报表开发以数据集为基础绑定卡片：数据集字段的度量/维度角色来自受治理的 DSL，
// 模型只在这份服务端裁剪过的字段目录里，为一张卡片（展示类型 + 标题/意图）挑选
// 满足组件合同的维度与度量。输出经 ValidateCardBindingSuggestion 按字段目录、
// 角色白名单与数量上下限校验；模型不能发明字段，也不能越过合同。

type CardBindingField struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Role          string `json:"role"`
	SemanticType  string `json:"semanticType,omitempty"`
	CanonicalType string `json:"canonicalType,omitempty"`
}

type CardBindingContract struct {
	DimensionsMin  int                `json:"dimensionsMin"`
	DimensionsMax  int                `json:"dimensionsMax"`
	MeasuresMin    int                `json:"measuresMin"`
	MeasuresMax    int                `json:"measuresMax"`
	Roles          []string           `json:"roles"`
	DimensionRoles []string           `json:"dimensionRoles,omitempty"`
	MeasureRoles   []string           `json:"measureRoles,omitempty"`
	Groups         []CardBindingGroup `json:"groups,omitempty"`
}

// CardBindingGroup is the model-facing form of the component editor profile.
// It keeps manual and LLM configuration on the same component-specific rules.
type CardBindingGroup struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Description  string   `json:"description"`
	Kind         string   `json:"kind"`
	Roles        []string `json:"roles"`
	Min          int      `json:"min"`
	Max          int      `json:"max"`
	NestedUnder  string   `json:"nestedUnder,omitempty"`
	MaxPerParent int      `json:"maxPerParent,omitempty"`
}

type CardBindingRequest struct {
	CardTitle       string              `json:"cardTitle"`
	ComponentType   string              `json:"componentType"`
	ComponentName   string              `json:"componentName"`
	Contract        CardBindingContract `json:"contract"`
	DataContextName string              `json:"dataContextName"`
	Fields          []CardBindingField  `json:"fields"`
	Intent          string              `json:"intent,omitempty"`
}

type CardBindingSuggestion struct {
	Dimensions []report.FieldBinding `json:"dimensions"`
	Measures   []report.FieldBinding `json:"measures"`
	Rationale  string                `json:"rationale"`
}

type CardBindingSuggester interface {
	SuggestCardBinding(context.Context, CardBindingRequest) (CardBindingSuggestion, error)
}

const (
	maxCardBindingFields = 400
	cardBindingPrompt    = "You bind one report card to governed dataset fields. Choose dimensions only from non-MEASURE fields and measures only from MEASURE fields, using listed field codes. Follow contract.groups exactly: each group has its own allowed roles and [min,max]. When a group has nestedUnder, place its bindings immediately after the related parent binding, before the next parent, and respect maxPerParent. Use contract.dimensionRoles and contract.measureRoles to classify roles. Prefer TIME for trends, categories for comparisons, and measures matching the card title or intent. Return a short rationale in Chinese. Never invent fields."
)

// ValidateCardBindingRequest bounds the payload before it reaches a model.
func ValidateCardBindingRequest(request CardBindingRequest) error {
	if strings.TrimSpace(request.ComponentType) == "" || len(request.Fields) == 0 || len(request.Fields) > maxCardBindingFields {
		return errors.New("card binding request is invalid")
	}
	if request.Contract.DimensionsMin < 0 || request.Contract.MeasuresMin < 0 ||
		request.Contract.DimensionsMax < request.Contract.DimensionsMin ||
		request.Contract.MeasuresMax < request.Contract.MeasuresMin || len(request.Contract.Roles) == 0 {
		return errors.New("card binding contract is invalid")
	}
	allowedRoles := cardBindingStringSet(request.Contract.Roles)
	groupIDs := map[string]bool{}
	groupRoles := map[string]bool{}
	for _, group := range request.Contract.Groups {
		if strings.TrimSpace(group.ID) == "" || groupIDs[group.ID] ||
			(group.Kind != "DIMENSION" && group.Kind != "MEASURE") || len(group.Roles) == 0 ||
			group.Min < 0 || group.Max < group.Min {
			return errors.New("card binding group contract is invalid")
		}
		groupIDs[group.ID] = true
		for _, role := range group.Roles {
			role = strings.ToUpper(strings.TrimSpace(role))
			if !allowedRoles[role] || groupRoles[role] {
				return errors.New("card binding group roles are invalid")
			}
			groupRoles[role] = true
		}
	}
	for _, group := range request.Contract.Groups {
		if group.NestedUnder != "" && (!groupIDs[group.NestedUnder] || group.MaxPerParent < 1) {
			return errors.New("card binding nested group contract is invalid")
		}
	}
	return nil
}

// ValidateCardBindingSuggestion keeps only bindings that name a listed field of
// the right kind with a role allowed by the contract, de-duplicates fields and
// then enforces the contract cardinality. It fails when the model could not
// produce a contract-satisfying binding, so the caller falls back to the
// deterministic role-based fill instead of saving a partial answer.
func ValidateCardBindingSuggestion(request CardBindingRequest, suggestion CardBindingSuggestion) (CardBindingSuggestion, error) {
	if err := ValidateCardBindingRequest(request); err != nil {
		return CardBindingSuggestion{}, err
	}
	fields := make(map[string]CardBindingField, len(request.Fields))
	for _, field := range request.Fields {
		fields[strings.TrimSpace(field.Code)] = field
	}
	roles := make(map[string]bool, len(request.Contract.Roles))
	for _, role := range request.Contract.Roles {
		roles[strings.ToUpper(strings.TrimSpace(role))] = true
	}
	measureRoles := cardBindingStringSet(request.Contract.MeasureRoles)
	if len(measureRoles) == 0 {
		measureRoles = map[string]bool{"VALUE": true, "Y_AXIS": true, "SIZE": true}
	}
	measureRole := func(role string) bool { return measureRoles[role] }
	seen := map[string]bool{}
	keep := func(items []report.FieldBinding, wantMeasure bool, max int) []report.FieldBinding {
		result := make([]report.FieldBinding, 0, len(items))
		for _, item := range items {
			code := strings.TrimSpace(item.Field)
			role := strings.ToUpper(strings.TrimSpace(string(item.Role)))
			field, exists := fields[code]
			if !exists || seen[code] || !roles[role] || measureRole(role) != wantMeasure ||
				(strings.EqualFold(field.Role, "MEASURE") != wantMeasure) {
				continue
			}
			seen[code] = true
			result = append(result, report.FieldBinding{Role: report.BindingRole(role), Field: code})
			if len(result) >= max {
				break
			}
		}
		return result
	}
	result := CardBindingSuggestion{
		Dimensions: keep(suggestion.Dimensions, false, request.Contract.DimensionsMax),
		Measures:   keep(suggestion.Measures, true, request.Contract.MeasuresMax),
		Rationale:  strings.TrimSpace(suggestion.Rationale),
	}
	if len([]rune(result.Rationale)) > 400 {
		result.Rationale = string([]rune(result.Rationale)[:400])
	}
	if len(result.Dimensions) < request.Contract.DimensionsMin || len(result.Measures) < request.Contract.MeasuresMin {
		return CardBindingSuggestion{}, fmt.Errorf("card binding suggestion does not satisfy the component contract (dimensions %d/%d, measures %d/%d)",
			len(result.Dimensions), request.Contract.DimensionsMin, len(result.Measures), request.Contract.MeasuresMin)
	}
	if err := validateCardBindingGroups(request.Contract.Groups, result); err != nil {
		return CardBindingSuggestion{}, err
	}
	return result, nil
}

func cardBindingStringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.ToUpper(strings.TrimSpace(value)); value != "" {
			result[value] = true
		}
	}
	return result
}

func validateCardBindingGroups(groups []CardBindingGroup, suggestion CardBindingSuggestion) error {
	if len(groups) == 0 {
		return nil
	}
	type bindingWithKind struct {
		binding report.FieldBinding
		kind    string
	}
	items := make([]bindingWithKind, 0, len(suggestion.Dimensions)+len(suggestion.Measures))
	for _, binding := range suggestion.Dimensions {
		items = append(items, bindingWithKind{binding: binding, kind: "DIMENSION"})
	}
	for _, binding := range suggestion.Measures {
		items = append(items, bindingWithKind{binding: binding, kind: "MEASURE"})
	}
	counts := map[string]int{}
	roleGroups := map[string]CardBindingGroup{}
	for _, group := range groups {
		for _, role := range group.Roles {
			roleGroups[strings.ToUpper(strings.TrimSpace(role))] = group
		}
	}
	for _, item := range items {
		group, exists := roleGroups[strings.ToUpper(string(item.binding.Role))]
		if !exists || group.Kind != item.kind {
			return errors.New("card binding suggestion does not match its component-specific groups")
		}
		counts[group.ID]++
	}
	for _, group := range groups {
		if counts[group.ID] < group.Min || counts[group.ID] > group.Max {
			return fmt.Errorf("card binding group %s count %d is outside %d..%d", group.ID, counts[group.ID], group.Min, group.Max)
		}
		if group.NestedUnder == "" {
			continue
		}
		parentRoles := map[string]bool{}
		for _, parent := range groups {
			if parent.ID == group.NestedUnder {
				parentRoles = cardBindingStringSet(parent.Roles)
				break
			}
		}
		childRoles := cardBindingStringSet(group.Roles)
		activeParent, children := false, 0
		for _, item := range items {
			if item.kind != group.Kind {
				continue
			}
			role := strings.ToUpper(string(item.binding.Role))
			if parentRoles[role] {
				activeParent, children = true, 0
			} else if childRoles[role] {
				children++
				if !activeParent || children > group.MaxPerParent {
					return fmt.Errorf("card binding nested group %s is not attached to a parent", group.ID)
				}
			}
		}
	}
	return nil
}
