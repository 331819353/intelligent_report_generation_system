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
	DimensionsMin int      `json:"dimensionsMin"`
	DimensionsMax int      `json:"dimensionsMax"`
	MeasuresMin   int      `json:"measuresMin"`
	MeasuresMax   int      `json:"measuresMax"`
	Roles         []string `json:"roles"`
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
	cardBindingPrompt    = "You bind one report card to governed dataset fields. Choose dimensions only from fields whose role is not MEASURE and measures only from fields whose role is MEASURE, using only the listed field codes. Respect the contract: dimension and measure counts must stay within [min,max] and every role must come from contract.roles (dimension roles exclude VALUE/Y_AXIS/SIZE). Prefer TIME fields for trend charts, CATEGORY/DIMENSION fields for comparisons, and measures whose names match the card title or intent. Return a short rationale in Chinese. Never invent fields."
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
	measureRole := func(role string) bool { return role == "VALUE" || role == "Y_AXIS" || role == "SIZE" }
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
	return result, nil
}
