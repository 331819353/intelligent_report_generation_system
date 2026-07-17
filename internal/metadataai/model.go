package metadataai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	SchemaVersion = "1.1"
	PromptVersion = "metadata-completion-v3"
)

var (
	ErrProviderUnavailable = errors.New("AI metadata provider is not configured")
	ErrInvalidOutput       = errors.New("AI metadata output is invalid")
	ErrInvalidDecision     = errors.New("metadata AI decision is invalid")
)

type Target struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	Name                string          `json:"name"`
	CatalogName         string          `json:"catalogName,omitempty"`
	SchemaName          string          `json:"schemaName,omitempty"`
	TableType           string          `json:"tableType,omitempty"`
	SourceComment       string          `json:"sourceComment"`
	PrimaryKeyColumns   []string        `json:"primaryKeyColumns,omitempty"`
	Constraints         json.RawMessage `json:"constraints,omitempty"`
	Indexes             json.RawMessage `json:"indexes,omitempty"`
	OrdinalPosition     int             `json:"ordinalPosition,omitempty"`
	NativeType          string          `json:"nativeType,omitempty"`
	CanonicalType       string          `json:"canonicalType,omitempty"`
	Length              *int64          `json:"length,omitempty"`
	NumericPrecision    *int            `json:"numericPrecision,omitempty"`
	NumericScale        *int            `json:"numericScale,omitempty"`
	Nullable            bool            `json:"nullable"`
	DefaultValue        *string         `json:"defaultValue,omitempty"`
	PrimaryKey          bool            `json:"primaryKey,omitempty"`
	ForeignKey          bool            `json:"foreignKey,omitempty"`
	Unique              bool            `json:"unique,omitempty"`
	CurrentBusinessName string          `json:"currentBusinessName"`
	CurrentDescription  string          `json:"currentDescription"`
	CurrentTags         []string        `json:"currentTags"`
	CurrentSemanticType string          `json:"currentSemanticType,omitempty"`
	CurrentSensitivity  string          `json:"currentSensitivity"`
	ManualLocked        bool            `json:"manualLocked"`
	BusinessVersion     int64           `json:"-"`
	StructureHash       string          `json:"-"`
}

type CompletionInput struct {
	SchemaVersion string `json:"schemaVersion"`
	// StructureHash 只用于数据库并发栅栏，不发送给外部模型，也不混入提示词输入哈希。
	StructureHash string           `json:"-"`
	TargetTable   bool             `json:"targetTable"`
	Table         Target           `json:"table"`
	Columns       []Target         `json:"columns"`
	SampleRows    []map[string]any `json:"sampleRows,omitempty"`
}

type SuggestionValue struct {
	TargetID            string   `json:"targetId"`
	BusinessName        string   `json:"businessName"`
	BusinessDescription string   `json:"businessDescription"`
	Tags                []string `json:"tags"`
	SensitivityLevel    string   `json:"sensitivityLevel"`
	SemanticType        string   `json:"semanticType,omitempty"`
	Confidence          float64  `json:"confidence"`
}

type CompletionOutput struct {
	SchemaVersion string            `json:"schemaVersion"`
	Table         *SuggestionValue  `json:"table,omitempty"`
	Columns       []SuggestionValue `json:"columns"`
}

type Usage struct {
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	TotalTokens      int `json:"totalTokens"`
}

type ProviderResult struct {
	Output       CompletionOutput
	Usage        Usage
	Model        string
	ModelVersion string
}

type Job struct {
	ID                      string `json:"id"`
	TableID                 string `json:"tableId"`
	StructureHash           string `json:"metadataStructureHash"`
	ProcessingItemID        string `json:"-"`
	ProcessingWorkerID      string `json:"-"`
	ProcessingSourceVersion int64  `json:"-"`
	Provider                string `json:"provider"`
	Model                   string `json:"model"`
	ModelVersion            string `json:"modelVersion,omitempty"`
	PromptVersion           string `json:"promptVersion"`
	InputHash               string `json:"inputHash"`
	Status                  string `json:"status"`
	ErrorCode               string `json:"errorCode,omitempty"`
	PromptTokens            int    `json:"promptTokens"`
	CompletionTokens        int    `json:"completionTokens"`
	TotalTokens             int    `json:"totalTokens"`
	LatencyMS               int64  `json:"latencyMs"`
	CreatedAt               string `json:"createdAt"`
	CompletedAt             string `json:"completedAt,omitempty"`
}

type Suggestion struct {
	ID            string          `json:"id"`
	JobID         string          `json:"jobId"`
	TargetType    string          `json:"targetType"`
	TargetID      string          `json:"targetId"`
	Value         SuggestionValue `json:"value"`
	Confidence    float64         `json:"confidence"`
	Status        string          `json:"status"`
	PendingReason string          `json:"pendingReason,omitempty"`
	CreatedAt     string          `json:"createdAt"`
	DecidedAt     string          `json:"decidedAt,omitempty"`
}

var allowedTags = map[string]bool{
	"领域:企业": true, "领域:金融": true, "领域:产业": true, "领域:政务": true, "领域:运营": true,
	"产业:制造业": true, "产业:服务业": true, "产业:信息产业": true,
	"主题:经营分析": true, "主题:风险监控": true, "主题:企业画像": true,
	"作用:维度表": true, "作用:事实表": true, "作用:主数据": true, "作用:指标来源": true, "作用:辅助信息": true,
}

var allowedSemanticTypes = map[string]bool{
	"DATE": true, "TIME": true, "DATETIME": true, "REGION": true, "COMPANY_NAME": true,
	"AMOUNT": true, "PERCENTAGE": true, "IDENTIFIER": true, "CATEGORY": true, "QUANTITY": true,
	"BOOLEAN": true, "TEXT": true,
}

var allowedSensitivity = map[string]bool{"PUBLIC": true, "INTERNAL": true, "CONFIDENTIAL": true, "RESTRICTED": true}

// ValidateOutput 确保模型只返回输入中存在的目标，且建议字段符合领域约束。
func ValidateOutput(input CompletionInput, output CompletionOutput) error {
	if output.SchemaVersion != SchemaVersion {
		return invalid("schemaVersion must be %q", SchemaVersion)
	}
	if output.Columns == nil {
		return invalid("columns is required and must be an array")
	}
	if input.TargetTable {
		if output.Table == nil || output.Table.TargetID != input.Table.ID {
			return invalid("table targetId does not match the requested table")
		}
		if err := validateValue(*output.Table, false); err != nil {
			return fmt.Errorf("%w: table: %v", ErrInvalidOutput, err)
		}
	} else if output.Table != nil {
		return invalid("table suggestion was not requested")
	}
	expected := make(map[string]bool, len(input.Columns))
	for _, column := range input.Columns {
		expected[column.ID] = true
	}
	seen := make(map[string]bool, len(output.Columns))
	for i, column := range output.Columns {
		if !expected[column.TargetID] {
			return invalid("columns[%d] references an unknown targetId", i)
		}
		if seen[column.TargetID] {
			return invalid("columns[%d] duplicates targetId", i)
		}
		seen[column.TargetID] = true
		if err := validateValue(column, true); err != nil {
			return fmt.Errorf("%w: columns[%d]: %v", ErrInvalidOutput, i, err)
		}
	}
	if len(seen) != len(expected) {
		return invalid("output must contain every requested column exactly once")
	}
	return nil
}

// validateValue 校验单条表或字段建议的名称、标签、敏感级别与置信度。
func validateValue(value SuggestionValue, column bool) error {
	if strings.TrimSpace(value.TargetID) == "" {
		return errors.New("targetId is required")
	}
	if err := validateText("businessName", value.BusinessName, 120); err != nil {
		return err
	}
	if err := validateText("businessDescription", value.BusinessDescription, 1000); err != nil {
		return err
	}
	if value.Confidence <= 0 || value.Confidence > 1 {
		return errors.New("confidence must be greater than zero and at most one")
	}
	if !allowedSensitivity[value.SensitivityLevel] {
		return errors.New("invalid sensitivityLevel")
	}
	if value.Tags == nil {
		return errors.New("tags is required and must be an array")
	}
	if len(value.Tags) > 12 {
		return errors.New("too many tags")
	}
	seen := map[string]bool{}
	for _, tag := range value.Tags {
		if !allowedTags[tag] {
			return fmt.Errorf("tag %q is not in the allowed taxonomy", tag)
		}
		if seen[tag] {
			return fmt.Errorf("tag %q is duplicated", tag)
		}
		seen[tag] = true
	}
	if column {
		if !allowedSemanticTypes[value.SemanticType] {
			return errors.New("invalid semanticType")
		}
	} else if value.SemanticType != "" {
		return errors.New("table semanticType must be empty")
	}
	return nil
}

// normalizeOutput 在领域校验前统一清理表和字段建议中的首尾空白。
func normalizeOutput(output CompletionOutput) CompletionOutput {
	if output.Table != nil {
		normalized := normalizeValue(*output.Table)
		output.Table = &normalized
	}
	for i := range output.Columns {
		output.Columns[i] = normalizeValue(output.Columns[i])
	}
	return output
}

// normalizeValue 规范化单条建议的文本字段与标签。
func normalizeValue(value SuggestionValue) SuggestionValue {
	value.TargetID = strings.TrimSpace(value.TargetID)
	value.BusinessName = strings.TrimSpace(value.BusinessName)
	value.BusinessDescription = strings.TrimSpace(value.BusinessDescription)
	value.SensitivityLevel = strings.TrimSpace(value.SensitivityLevel)
	value.SemanticType = strings.TrimSpace(value.SemanticType)
	if value.Tags != nil {
		// 上游 Schema 不支持 uniqueItems，按首次出现顺序去重后仍交由领域枚举校验兜底。
		seen := make(map[string]bool, len(value.Tags))
		tags := make([]string, 0, len(value.Tags))
		for _, raw := range value.Tags {
			tag := strings.TrimSpace(raw)
			if seen[tag] {
				continue
			}
			seen[tag] = true
			tags = append(tags, tag)
		}
		value.Tags = tags
	}
	return value
}

// validateText 按 Unicode 字符数校验必填文本和长度上限。
func validateText(field, value string, maxRunes int) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len([]rune(value)) > maxRunes {
		return fmt.Errorf("%s is too long", field)
	}
	if strings.Contains(value, "<") || strings.Contains(value, ">") {
		return fmt.Errorf("%s contains unsafe markup", field)
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return fmt.Errorf("%s contains control characters", field)
		}
	}
	return nil
}

// invalid 构造可由上层识别的模型输出校验错误。
func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidOutput, fmt.Sprintf(format, args...))
}
