package promptguard

import (
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	PromptAssessmentVersion = "askdata-untrusted-prompt-assessment-v1"
	MaxUntrustedPromptBytes = 64 << 10
)

var (
	ErrInvalidUntrustedPromptData = errors.New("untrusted prompt data is invalid")
	ErrPromptInjection            = errors.New("untrusted prompt data was rejected")
)

type PromptTrustLabel string

const PromptTrustUntrustedData PromptTrustLabel = "UNTRUSTED_DATA"

type PromptDisposition string

const (
	PromptAllow  PromptDisposition = "ALLOW"
	PromptBlock  PromptDisposition = "BLOCK"
	PromptRefuse PromptDisposition = "REFUSE"
)

type PromptAssessment struct {
	Version     string              `json:"version"`
	Source      string              `json:"source"`
	TrustLabel  PromptTrustLabel    `json:"trustLabel"`
	Executable  bool                `json:"executable"`
	Disposition PromptDisposition   `json:"disposition"`
	ReasonCode  string              `json:"reasonCode,omitempty"`
	ContentHash askdata.ContentHash `json:"contentHash"`
}

func (assessment PromptAssessment) Validate() error {
	if assessment.Version != PromptAssessmentVersion || !stablePromptCode(assessment.Source) ||
		assessment.TrustLabel != PromptTrustUntrustedData || assessment.Executable ||
		assessment.ContentHash.Validate() != nil {
		return ErrInvalidUntrustedPromptData
	}
	switch assessment.Disposition {
	case PromptAllow:
		if assessment.ReasonCode != "" {
			return ErrInvalidUntrustedPromptData
		}
	case PromptBlock, PromptRefuse:
		if !stablePromptCode(assessment.ReasonCode) {
			return ErrInvalidUntrustedPromptData
		}
	default:
		return ErrInvalidUntrustedPromptData
	}
	return nil
}

func (assessment PromptAssessment) Enforce() error {
	if assessment.Validate() != nil {
		return ErrInvalidUntrustedPromptData
	}
	if assessment.Disposition == PromptAllow {
		return nil
	}
	return &PromptViolation{Disposition: assessment.Disposition, ReasonCode: assessment.ReasonCode}
}

type PromptViolation struct {
	Disposition PromptDisposition
	ReasonCode  string
}

func (violation *PromptViolation) Error() string {
	if violation == nil || !stablePromptCode(violation.ReasonCode) {
		return ErrPromptInjection.Error()
	}
	return ErrPromptInjection.Error() + ": " + violation.ReasonCode
}

func (violation *PromptViolation) Unwrap() error { return ErrPromptInjection }

func AssessUntrustedPromptData(source string, payload json.RawMessage) (PromptAssessment, error) {
	if !stablePromptCode(source) || len(payload) == 0 || len(payload) > MaxUntrustedPromptBytes {
		return PromptAssessment{}, ErrInvalidUntrustedPromptData
	}
	var value any
	if err := askdata.DecodeStrictJSON(payload, &value); err != nil {
		return PromptAssessment{}, ErrInvalidUntrustedPromptData
	}
	if _, object := value.(map[string]any); !object {
		return PromptAssessment{}, ErrInvalidUntrustedPromptData
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return PromptAssessment{}, ErrInvalidUntrustedPromptData
	}
	assessment := PromptAssessment{
		Version: PromptAssessmentVersion, Source: source,
		TrustLabel: PromptTrustUntrustedData, Executable: false,
		Disposition: PromptAllow, ContentHash: askdata.HashBytes(canonical),
	}
	texts := []string{}
	if !collectPromptStrings(value, 0, &texts) {
		return PromptAssessment{}, ErrInvalidUntrustedPromptData
	}
	for _, signature := range promptInjectionSignatures {
		for _, text := range texts {
			normalized, compact := normalizeInjectionText(text)
			if signature.matches(normalized, compact) {
				assessment.Disposition = signature.disposition
				assessment.ReasonCode = signature.reasonCode
				if assessment.Validate() != nil {
					return PromptAssessment{}, ErrInvalidUntrustedPromptData
				}
				return assessment, nil
			}
		}
	}
	return assessment, nil
}

type promptInjectionSignature struct {
	disposition PromptDisposition
	reasonCode  string
	patterns    []string
}

var promptInjectionSignatures = []promptInjectionSignature{
	{PromptRefuse, "PROMPT_TOOL_ESCALATION", []string{
		"register a new tool", "register tool", "create a new tool", "create tool",
		"add a tool", "创建新工具", "创建工具", "注册工具", "新增工具",
	}},
	{PromptRefuse, "PROMPT_QUERY_ESCALATION", []string{
		"execute arbitrary sql", "run arbitrary sql", "execute raw sql", "run raw sql",
		"execute arbitrary ngql", "run arbitrary ngql", "execute raw ngql", "run raw ngql",
		"执行任意sql", "运行任意sql", "执行原始sql", "运行原始sql",
		"执行任意ngql", "运行任意ngql", "执行原始ngql", "运行原始ngql",
	}},
	{PromptRefuse, "PROMPT_SCOPE_ESCALATION", []string{
		"switch tenant", "change tenant", "override tenant", "switch domain", "change domain",
		"switch release", "change release", "切换租户", "更换租户", "覆盖租户",
		"切换业务域", "更换业务域", "切换领域", "切换发布版本", "更换发布版本",
	}},
	{PromptRefuse, "PROMPT_BUDGET_ESCALATION", []string{
		"increase tool budget", "expand tool budget", "increase query budget", "remove budget limit",
		"increase maxrows", "increase max rows", "toolcallsremaining", "tool calls remaining",
		"formalqueriesremaining", "formal queries remaining", "validationqueriesremaining", "validation queries remaining",
		"扩大工具预算", "增加工具预算", "扩大查询预算", "增加查询预算", "取消预算上限",
	}},
	{PromptRefuse, "PROMPT_CREDENTIAL_ESCALATION", []string{
		"reveal database password", "reveal credentials", "show access token",
		"泄露数据库密码", "显示数据库密码", "显示访问令牌", "获取数据库凭据",
	}},
	{PromptBlock, "PROMPT_CONTROL_INJECTION", []string{
		"ignore previous instruction", "ignore all instruction", "disregard previous instruction",
		"override system instruction", "you are now the system", "<system", "</system",
		"<assistant", "</assistant", "</untrustedfacts", "[system]", "[assistant]",
		"忽略之前的指令", "忽略以上指令", "忽略所有指令", "忽略系统指令",
		"无视系统指令", "覆盖系统指令", "你现在是系统", "你是系统消息",
	}},
}

func (signature promptInjectionSignature) matches(normalized, compact string) bool {
	for _, pattern := range signature.patterns {
		patternNormalized, patternCompact := normalizeInjectionText(pattern)
		if strings.Contains(normalized, patternNormalized) ||
			(nonASCIIString(patternNormalized) && patternCompact != "" && strings.Contains(compact, patternCompact)) {
			return true
		}
	}
	return false
}

func collectPromptStrings(value any, depth int, result *[]string) bool {
	if depth > 64 || len(*result) > 4096 {
		return false
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if len(*result) >= 4096 {
				return false
			}
			*result = append(*result, key)
			if !collectPromptStrings(child, depth+1, result) {
				return false
			}
		}
	case []any:
		for _, child := range typed {
			if !collectPromptStrings(child, depth+1, result) {
				return false
			}
		}
	case string:
		if len(*result) >= 4096 {
			return false
		}
		*result = append(*result, typed)
	}
	return true
}

func normalizeInjectionText(value string) (string, string) {
	value = cases.Fold().String(norm.NFKC.String(value))
	value = strings.NewReplacer("_", " ", "-", " ", "\"", " ", "'", " ").Replace(value)
	normalized := strings.Join(strings.Fields(value), " ")
	compact := strings.ReplaceAll(normalized, " ", "")
	return normalized, compact
}

func nonASCIIString(value string) bool {
	for _, character := range value {
		if character > 127 {
			return true
		}
	}
	return false
}

func stablePromptCode(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return true
}
