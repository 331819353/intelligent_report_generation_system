package metadataai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"intelligent-report-generation-system/internal/semanticquality"
	"intelligent-report-generation-system/internal/warehouselayer"
)

const (
	SchemaVersion = "1.1"
	// v16：表级清洗必须给出恰好一个“层级:”标签（ODS/DIM/DWD/DWS/ADS）。
	PromptVersion        = "metadata-completion-v16"
	SourceFormatCSV      = "CSV"
	SourceFormatExcel    = "EXCEL"
	SourceFormatDatabase = "DATABASE"
)

var (
	ErrProviderUnavailable = errors.New("AI metadata provider is not configured")
	ErrInvalidOutput       = errors.New("AI metadata output is invalid")
	ErrInvalidDecision     = errors.New("metadata AI decision is invalid")
)

// PartialOutputError 表示模型返回中至少有一个可信目标可保存，但没有覆盖当前
// 请求的全部目标。上层保存有效部分后会只针对剩余目标继续重试。
type PartialOutputError struct {
	MissingTargets int
}

func (e *PartialOutputError) Error() string {
	return fmt.Sprintf("%v: %d target(s) still require completion", ErrInvalidOutput, e.MissingTargets)
}

func (e *PartialOutputError) Unwrap() error { return ErrInvalidOutput }

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
	MissingFields       []string        `json:"missingFields,omitempty"`
	ManualLocked        bool            `json:"manualLocked"`
	BusinessVersion     int64           `json:"-"`
	StructureHash       string          `json:"-"`
	NeedsCompletion     bool            `json:"-"`
	CompletionTracked   bool            `json:"-"`
}

type CompletionInput struct {
	SchemaVersion string `json:"schemaVersion"`
	// SourceFormat 只描述本次资产来自 CSV、Excel 工作簿还是数据库，不包含文件名或连接信息。
	SourceFormat string `json:"sourceFormat,omitempty"`
	// ContextColumns 是分批请求使用的全表紧凑字段上下文；Columns 仍是本批唯一
	// 允许输出的目标，模型不得为 ContextColumns 中的其他字段生成结果。
	ContextColumns []CompletionColumnContext `json:"contextColumns,omitempty"`
	// StructureHash 只用于数据库并发栅栏，不发送给外部模型，也不混入提示词输入哈希。
	StructureHash string           `json:"-"`
	TargetTable   bool             `json:"targetTable"`
	Table         Target           `json:"table"`
	Columns       []Target         `json:"columns"`
	SampleRows    []map[string]any `json:"sampleRows,omitempty"`
}

type CompletionColumnContext struct {
	Name                string   `json:"name"`
	CanonicalType       string   `json:"canonicalType,omitempty"`
	PrimaryKey          bool     `json:"primaryKey,omitempty"`
	ForeignKey          bool     `json:"foreignKey,omitempty"`
	Unique              bool     `json:"unique,omitempty"`
	Nullable            bool     `json:"nullable"`
	CurrentBusinessName string   `json:"currentBusinessName,omitempty"`
	CurrentDescription  string   `json:"currentDescription,omitempty"`
	CurrentTags         []string `json:"currentTags,omitempty"`
	CurrentSemanticType string   `json:"currentSemanticType,omitempty"`
	CurrentSensitivity  string   `json:"currentSensitivity,omitempty"`
	ManualLocked        bool     `json:"manualLocked,omitempty"`
}

func completionColumnContexts(columns []Target) []CompletionColumnContext {
	contexts := make([]CompletionColumnContext, 0, len(columns))
	for _, column := range columns {
		contexts = append(contexts, CompletionColumnContext{
			Name: column.Name, CanonicalType: column.CanonicalType,
			PrimaryKey: column.PrimaryKey, ForeignKey: column.ForeignKey,
			Unique: column.Unique, Nullable: column.Nullable,
			CurrentBusinessName: column.CurrentBusinessName,
			CurrentDescription:  column.CurrentDescription,
			CurrentTags:         append([]string(nil), column.CurrentTags...),
			CurrentSemanticType: column.CurrentSemanticType,
			CurrentSensitivity:  column.CurrentSensitivity,
			ManualLocked:        column.ManualLocked,
		})
	}
	return contexts
}

type SuggestionValue struct {
	TargetID            string   `json:"targetId"`
	BusinessName        string   `json:"businessName"`
	BusinessDescription string   `json:"businessDescription"`
	Tags                []string `json:"tags"`
	SensitivityLevel    string   `json:"sensitivityLevel"`
	SemanticType        string   `json:"semanticType,omitempty"`
	Confidence          float64  `json:"confidence"`
	// Complete 与 ProvidedFields 只在本地增量合并链路中使用，不进入模型合同或审计快照。
	Complete       bool     `json:"-"`
	ProvidedFields []string `json:"-"`
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
	// Applicable / BlockedReason / ChangedFields 是读取时按目标当前状态计算的可应用性，
	// 让页面在用户点击之前就知道某条建议能不能被接受、以及为什么不能。
	Applicable    bool     `json:"applicable"`
	BlockedReason string   `json:"blockedReason,omitempty"`
	ChangedFields []string `json:"changedFields,omitempty"`
	// TargetBusinessVersion 是应用后目标的最新业务版本，供编辑器同步本地版本号，
	// 避免接受建议后紧接着的保存因版本过期而失败。
	TargetBusinessVersion int64 `json:"targetBusinessVersion,omitempty"`
}

// 建议不可应用的原因。除 TargetEdited 外都是硬阻断：目标已不存在、技术结构已变化
// 或语义类型与当前物理类型不相容时，这条建议描述的对象已经不是当前对象。
const (
	// SuggestionBlockedTargetEdited 表示建议生成后目标业务字段被改写。这是唯一
	// 可以由用户确认后强制覆盖的原因，因为内容本身仍然有效，只是会覆盖他人的修改。
	SuggestionBlockedTargetEdited     = "TARGET_EDITED"
	SuggestionBlockedAssetRemoved     = "ASSET_REMOVED"
	SuggestionBlockedStructureChanged = "STRUCTURE_CHANGED"
	SuggestionBlockedSemanticType     = "SEMANTIC_TYPE_INCOMPATIBLE"
	SuggestionBlockedAlreadyDecided   = "ALREADY_DECIDED"
)

// SuggestionConflictError 让上层把"为什么不能应用"原样透传给用户，而不是所有
// 冲突都退化成同一句"建议已失效或资产已变更"。
type SuggestionConflictError struct {
	Reason        string
	ChangedFields []string
}

func (e *SuggestionConflictError) Error() string {
	return "metadata AI suggestion conflict: " + e.Reason
}

// Unwrap 保持既有 errors.Is(err, ErrConflict) 调用点继续工作。
func (e *SuggestionConflictError) Unwrap() error { return ErrConflict }

// suggestionBlockedMessages 是面向用户的确切原因；每一条都说明用户可以怎么处理。
var suggestionBlockedMessages = map[string]string{
	SuggestionBlockedTargetEdited:     "该目标的业务定义在建议生成后已被修改，确认覆盖后才能应用此建议",
	SuggestionBlockedAssetRemoved:     "该字段或表资产已被删除或停用，建议不再适用",
	SuggestionBlockedStructureChanged: "该目标的技术结构已变化，请重新生成元数据建议",
	SuggestionBlockedSemanticType:     "建议的语义类型与当前物理类型不相容，请改为手工维护",
	SuggestionBlockedAlreadyDecided:   "该建议已被处理，请刷新后查看最新结果",
}

// SuggestionBlockedMessage 返回可直接展示的中文原因。
func SuggestionBlockedMessage(reason string) string {
	if message, ok := suggestionBlockedMessages[reason]; ok {
		return message
	}
	return "该建议当前不可应用，请刷新后重试"
}

// businessFieldsChanged 比较建议生成时的基线与目标当前值，返回被改写的字段。
// 只比较建议真正会覆盖的业务字段：业务版本号、结构哈希、锁定状态和其他字段的
// 变化都不构成冲突，因为它们不会让这条建议的内容失效。
func businessFieldsChanged(baseline, current SuggestionValue, column bool) []string {
	changed := make([]string, 0, 5)
	if strings.TrimSpace(baseline.BusinessName) != strings.TrimSpace(current.BusinessName) {
		changed = append(changed, "业务名称")
	}
	if strings.TrimSpace(baseline.BusinessDescription) != strings.TrimSpace(current.BusinessDescription) {
		changed = append(changed, "业务说明")
	}
	if !sameTags(baseline.Tags, current.Tags) {
		changed = append(changed, "标签")
	}
	if baseline.SensitivityLevel != current.SensitivityLevel {
		changed = append(changed, "敏感级")
	}
	if column && strings.TrimSpace(baseline.SemanticType) != strings.TrimSpace(current.SemanticType) {
		changed = append(changed, "语义类型")
	}
	return changed
}

// sameTags 按集合语义比较标签，标签顺序调整不算内容变化。
func sameTags(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, tag := range left {
		seen[strings.TrimSpace(tag)]++
	}
	for _, tag := range right {
		key := strings.TrimSpace(tag)
		seen[key]--
		if seen[key] < 0 {
			return false
		}
	}
	return true
}

var allowedTags = map[string]bool{
	"产业:制造业": true, "产业:服务业": true, "产业:信息产业": true,
	"主题:经营分析": true, "主题:风险监控": true, "主题:企业画像": true,
	"主题:订单": true, "主题:订单商品": true, "主题:售后服务": true, "主题:支付": true, "主题:履约": true,
	"主题:客户": true, "主题:商品": true, "主题:门店": true, "主题:库存": true, "主题:采购": true,
	"主题:供应商": true, "主题:员工": true, "主题:组织": true, "主题:渠道": true, "主题:营销": true,
	"作用:维度表": true, "作用:事实表": true, "作用:主数据": true, "作用:指标来源": true, "作用:辅助信息": true,
	"过程:销售": true, "过程:支付": true, "过程:履约": true, "过程:售后": true, "过程:客户经营": true,
	"过程:商品管理": true, "过程:门店经营": true, "过程:库存管理": true, "过程:采购": true, "过程:营销": true,
	"功能:交易明细": true, "功能:业务流水": true, "功能:事件明细": true, "功能:周期快照": true,
	"功能:汇总结果": true, "功能:实体主数据": true, "功能:关系映射": true, "功能:代码映射": true,
	"内容:订单头": true, "内容:订单行": true, "内容:支付信息": true, "内容:营销归因": true,
	"内容:收货地域": true, "内容:履约信息": true, "内容:售后处理": true, "内容:客户画像": true,
	"内容:商品属性": true, "内容:门店属性": true, "内容:金额指标": true, "内容:状态信息": true,
	"范围:运营分析": true, "范围:财务分析": true, "范围:风险分析": true, "范围:客户分析": true,
	"范围:供应链分析": true, "范围:商品分析": true, "范围:履约分析": true, "范围:营销分析": true, "范围:人力资源分析": true,
	"粒度:事件": true, "粒度:订单": true, "粒度:订单商品": true, "粒度:售后工单": true,
	"粒度:客户": true, "粒度:产品": true, "粒度:门店": true, "粒度:组织": true, "粒度:渠道": true,
	"粒度:供应商": true, "粒度:员工": true, "粒度:交易": true, "粒度:支付": true, "粒度:库存记录": true,
	"粒度:日期": true, "粒度:日": true, "粒度:月": true,
	"时间:事件时间": true, "时间:业务日期": true, "时间:快照日期": true, "时间:生效时间": true,
	"关联:主键": true, "关联:外键": true, "关联:业务键": true, "关联:桥接键": true,
	// 层级分面：表资产必须恰好一个，字段资产不得携带（见 warehouselayer 包）。
	"层级:ODS": true, "层级:DIM": true, "层级:DWD": true, "层级:DWS": true, "层级:ADS": true,
}

const maxControlledTagsPerTarget = 16

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
		if err := validateValue(*output.Table, false, false); err != nil {
			return fmt.Errorf("%w: table: %v", ErrInvalidOutput, err)
		}
		if isFileSourceFormat(input.SourceFormat) && !containsChinese(output.Table.BusinessName) {
			return invalid("table businessName must contain Chinese text for file sources")
		}
	} else if output.Table != nil {
		return invalid("table suggestion was not requested")
	}
	expected := make(map[string]Target, len(input.Columns))
	for _, column := range input.Columns {
		expected[column.ID] = column
	}
	seen := make(map[string]bool, len(output.Columns))
	for i, column := range output.Columns {
		target, exists := expected[column.TargetID]
		if !exists {
			return invalid("columns[%d] references an unknown targetId", i)
		}
		if seen[column.TargetID] {
			return invalid("columns[%d] duplicates targetId", i)
		}
		seen[column.TargetID] = true
		if err := validateValue(column, true, isFileSourceFormat(input.SourceFormat)); err != nil {
			return fmt.Errorf("%w: columns[%d]: %v", ErrInvalidOutput, i, err)
		}
		if strings.TrimSpace(target.CanonicalType) != "" &&
			!semanticquality.Compatible(target.CanonicalType, column.SemanticType) {
			return invalid(
				"columns[%d] semanticType %q is incompatible with canonicalType %q",
				i, column.SemanticType, target.CanonicalType,
			)
		}
	}
	if len(seen) != len(expected) {
		return invalid("output must contain every requested column exactly once")
	}
	return nil
}

// validateValue 校验单条表或字段建议的名称、标签、敏感级别与置信度。
func validateValue(value SuggestionValue, column, fileColumn bool) error {
	if strings.TrimSpace(value.TargetID) == "" {
		return errors.New("targetId is required")
	}
	if err := validateText("businessName", value.BusinessName, 120); err != nil {
		return err
	}
	if err := validateText("businessDescription", value.BusinessDescription, 1000); err != nil {
		return err
	}
	if fileColumn && !containsChinese(value.BusinessName) {
		return errors.New("businessName must contain Chinese text for file columns")
	}
	if fileColumn && !containsChinese(value.BusinessDescription) {
		return errors.New("businessDescription must contain Chinese text for file columns")
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
	if len(value.Tags) == 0 {
		return errors.New("tags must contain at least one controlled tag")
	}
	if len(value.Tags) > maxControlledTagsPerTarget {
		return fmt.Errorf("tags must contain at most %d controlled tags", maxControlledTagsPerTarget)
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
	// 表资产必须由清洗结果给出恰好一个数仓层级标签；字段资产不携带层级。
	if err := warehouselayer.Validate(value.Tags, column, true); err != nil {
		return err
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

// normalizeOutputForInput 保留输入参数以维持调用边界；文件字段显示名与数据库
// 字段一样只做通用清理，不再把模型输出改写为英文技术标识符。
func normalizeOutputForInput(input CompletionInput, output CompletionOutput) CompletionOutput {
	_ = input
	return normalizeOutput(output)
}

func isFileSourceFormat(value string) bool {
	return value == SourceFormatCSV || value == SourceFormatExcel
}

// containsChinese 允许描述中保留 ID、SKU 等英文缩写，但至少要有一个中文字符。
func containsChinese(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
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
