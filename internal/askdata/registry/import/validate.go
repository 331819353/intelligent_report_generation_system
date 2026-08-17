package registryimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

const (
	ImportRequiredMissing              = "IMPORT_REQUIRED_MISSING"
	ImportTypeInvalid                  = "IMPORT_TYPE_INVALID"
	ImportEnumInvalid                  = "IMPORT_ENUM_INVALID"
	ImportRefNotFound                  = "IMPORT_REF_NOT_FOUND"
	ImportRefNotActive                 = "IMPORT_REF_NOT_ACTIVE"
	ImportOwnerUnknown                 = "IMPORT_OWNER_UNKNOWN"
	ImportFormulaInvalid               = "IMPORT_FORMULA_INVALID"
	ImportFormulaCycle                 = "IMPORT_FORMULA_CYCLE"
	ImportCompatAsymmetric             = "IMPORT_COMPAT_ASYMMETRIC"
	ImportHierarchyBroken              = "IMPORT_HIERARCHY_BROKEN"
	ImportFanoutCombinationInvalid     = "IMPORT_FANOUT_COMBINATION_INVALID"
	ImportAdditivityInconsistent       = "IMPORT_ADDITIVITY_INCONSISTENT"
	ImportNameConflict                 = "IMPORT_NAME_CONFLICT"
	ImportTermPriorityConflict         = "IMPORT_TERM_PRIORITY_CONFLICT"
	ImportNegativeContextContradiction = "IMPORT_NEGATIVE_CONTEXT_CONTRADICTION"
	ImportSensitivityPolicyInvalid     = "IMPORT_SENSITIVITY_POLICY_INVALID"
	ImportImpactRequiresReview         = "IMPORT_IMPACT_REQUIRES_REVIEW"
	// 四分区 Bundle 导入引入的裁决码。前两个是非阻断的信息事实：WILL_UPDATE
	// 标记“该 code 已存在，提交会创建新版本”；CONTENT_UNCHANGED 标记“内容与
	// 当前认证版本一致”，该行被判为 SKIPPED 而不是重复建版。
	ImportWillUpdate                  = "IMPORT_WILL_UPDATE"
	ImportContentUnchanged            = "IMPORT_CONTENT_UNCHANGED"
	ImportCreateOnlyConflict          = "IMPORT_BUNDLE_CREATE_ONLY_CONFLICT"
	ImportKnowledgeDefinitionConflict = "IMPORT_KNOWLEDGE_DEFINITION_CONFLICT"
)

var ErrImportValidationCatalog = errors.New("semantic import validation catalog is unavailable")

const maxPreparedImportRows = 100_000

type ValidationReference struct {
	Kind             string
	Code             string
	Name             string
	ID               string
	Active           bool
	Certified        bool
	HighCardinality  bool
	ModelCode        string
	DatasetVersionID string
	TimeGrains       []string
}

type ValidationSnapshot struct {
	References            map[string]map[string]ValidationReference
	Owners                map[string]string
	Roles                 map[string]string
	Datasets              map[string]bool
	Fields                map[string]map[string]bool
	HighCardinalityFields map[string]map[string]bool
	Names                 map[string]map[string]struct{}
	Aliases               map[string]struct{}
	// AuthoritativeDefinitions 记录已存在 AUTHORITATIVE DEFINES 词条的目标
	// （"TARGET_TYPE\x00target_code" → 持有该定义的知识 code）。同一目标至多
	// 一个权威定义在 L4 强制；同 code 更新自身的权威定义不视为冲突。
	AuthoritativeDefinitions map[string]string
}

type ValidationCatalog interface {
	LoadValidationSnapshot(context.Context, string, string) (ValidationSnapshot, error)
}

type MemberTargetCatalog interface {
	ResolveImportMemberTargets(
		context.Context, string, string, []string,
	) (map[string]ValidationReference, error)
}

type FourLayerValidator struct {
	catalog ValidationCatalog
	// current 是可选的现状目录：提供后，与当前认证版本内容一致的行会被判为
	// SKIPPED（unchanged），避免重复导入创建等价新版本。缺席时导入照常工作，
	// 只是失去 unchanged 裁决。
	current ExportCatalog
}

func NewFourLayerValidator(catalog ValidationCatalog) *FourLayerValidator {
	return &FourLayerValidator{catalog: catalog}
}

// WithCurrentAssets 注入现状目录，启用 unchanged 判定。
func (validator *FourLayerValidator) WithCurrentAssets(current ExportCatalog) *FourLayerValidator {
	if validator != nil {
		validator.current = current
	}
	return validator
}

func (validator *FourLayerValidator) ValidateRow(
	context.Context,
	Claim,
	int,
	json.RawMessage,
) (ValidatedRow, error) {
	return ValidatedRow{}, permanentImportError(
		"IMPORT_VALIDATOR_NOT_PREPARED", ErrImportValidatorUnavailable,
	)
}

func (validator *FourLayerValidator) Prepare(
	ctx context.Context,
	claim Claim,
	rows []RawImportRow,
) (RowValidator, error) {
	if validator == nil || validator.catalog == nil || validateClaim(claim, claim.TenantID) != nil {
		return nil, permanentImportError("IMPORT_VALIDATOR_UNAVAILABLE", ErrImportValidatorUnavailable)
	}
	if len(rows) == 0 {
		return nil, permanentImportError("IMPORT_FILE_EMPTY", ErrInvalidImportRow)
	}
	if len(rows) > maxPreparedImportRows {
		return nil, permanentImportError("IMPORT_FILE_ROW_LIMIT", ErrInvalidImportRow)
	}
	parsed := make([]parsedImportRow, 0, len(rows))
	seenRows := make(map[int]struct{}, len(rows))
	for _, input := range rows {
		if input.RowNo < 1 {
			return nil, permanentImportError("IMPORT_VALIDATOR_CONTRACT", ErrInvalidImportRow)
		}
		if _, duplicate := seenRows[input.RowNo]; duplicate {
			return nil, permanentImportError("IMPORT_VALIDATOR_CONTRACT", ErrInvalidImportRow)
		}
		seenRows[input.RowNo] = struct{}{}
		row := parseBatchRow(claim.AssetType, input)
		parsed = append(parsed, row)
	}
	snapshot, err := validator.catalog.LoadValidationSnapshot(
		ctx, claim.TenantID, claim.DomainID,
	)
	if err != nil {
		return nil, err
	}
	snapshot = normalizeValidationSnapshot(snapshot)
	memberTargets := collectMemberTargets(parsed)
	if len(memberTargets) > 0 {
		resolver, ok := validator.catalog.(MemberTargetCatalog)
		if !ok {
			return nil, permanentImportError("IMPORT_MEMBER_RESOLVER_UNAVAILABLE", ErrImportValidationCatalog)
		}
		resolved, err := resolver.ResolveImportMemberTargets(
			ctx, claim.TenantID, claim.DomainID, memberTargets,
		)
		if err != nil {
			return nil, err
		}
		for code, reference := range resolved {
			reference.Kind, reference.Code = "MEMBER", code
			putReference(&snapshot, reference)
		}
	}
	session := &validationSession{
		claim: claim, snapshot: snapshot,
		results: make(map[int]ValidatedRow, len(rows)),
	}
	batch := buildBatchIndex(parsed)
	validateL2(parsed, batch, session.snapshot)
	validateL3(parsed, batch, session.snapshot)
	validateL4(parsed, batch, session.snapshot)
	if validator.current != nil {
		if err := markUnchangedRows(ctx, validator.current, claim, parsed); err != nil {
			return nil, err
		}
	}
	for _, row := range parsed {
		sortIssues(row.Issues)
		state := RowValid
		if hasBlockingIssue(row.Issues) {
			state = RowInvalid
		}
		if state == RowValid && rowIsUnchanged(row.Issues) {
			// 内容与当前认证版本一致：跳过而不是重复建版。SKIPPED 行永远
			// 不会被提交，报表把它计入 unchanged。
			state = RowSkipped
		}
		normalized, marshalErr := json.Marshal(row.Values)
		if marshalErr != nil {
			return nil, permanentImportError("IMPORT_VALIDATOR_CONTRACT", marshalErr)
		}
		session.results[row.RowNo] = ValidatedRow{
			RowNo:          row.RowNo,
			RawJSON:        append(json.RawMessage(nil), row.Raw...),
			NormalizedJSON: normalized,
			State:          state,
			Errors:         append([]ValidationIssue(nil), row.Issues...),
		}
	}
	return session, nil
}

// parseBatchRow 确定行级资产类型后运行 L1。单类型批直接使用批级类型；
// BUNDLE 批从展开行的保留键读取行级类型与导入模式。
func parseBatchRow(batchType AssetType, input RawImportRow) parsedImportRow {
	if batchType != AssetBundle {
		return parseAndValidateL1(batchType, input)
	}
	var envelope struct {
		AssetType  string `json:"assetType"`
		AssetIndex string `json:"bundleAsset"`
		Mode       string `json:"bundleMode"`
	}
	rowType := AssetType("")
	if err := json.Unmarshal(input.Raw, &envelope); err == nil {
		rowType = AssetType(envelope.AssetType)
	}
	if !ValidRowAssetType(rowType) {
		result := parsedImportRow{
			RowNo: input.RowNo, Raw: append(json.RawMessage(nil), input.Raw...),
			Values: map[string]string{},
		}
		addIssue(&result, "row", ImportTypeInvalid, "Bundle 展开行缺少合法的行级资产类型",
			"assetType 为受支持的行级类型", envelope.AssetType)
		return result
	}
	row := parseAndValidateL1(rowType, input)
	row.CreateOnly = envelope.Mode == BundleModeCreateOnly
	row.BundleAsset = envelope.AssetIndex
	return row
}

// collectMemberTargets 汇总需要目录解析的成员词典目标：TERM 行的
// termType=MEMBER 与 KNOWLEDGE 行的 targetType=MEMBER 都使用
// dimensionCode::canonicalValue 合同。
func collectMemberTargets(parsed []parsedImportRow) []string {
	seen := map[string]struct{}{}
	targets := []string{}
	for _, row := range parsed {
		if row.Layer != 1 {
			continue
		}
		target := ""
		switch {
		case row.Type == AssetTerm && row.Values["termType"] == "MEMBER":
			target = row.Values["targetCode"]
		case row.Type == AssetKnowledge && row.Values["targetType"] == "MEMBER":
			target = row.Values["targetCode"]
		}
		if target == "" {
			continue
		}
		key := canonicalLookup(target)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			targets = append(targets, target)
		}
	}
	return targets
}

type validationSession struct {
	claim    Claim
	snapshot ValidationSnapshot
	results  map[int]ValidatedRow
}

func (session *validationSession) ValidateRow(
	_ context.Context,
	claim Claim,
	rowNo int,
	raw json.RawMessage,
) (ValidatedRow, error) {
	if session == nil || claim.ImportID != session.claim.ImportID || claim.LeaseToken != session.claim.LeaseToken {
		return ValidatedRow{}, permanentImportError("IMPORT_VALIDATOR_CONTRACT", ErrInvalidImportRow)
	}
	result, exists := session.results[rowNo]
	if !exists || !jsonBytesEqual(result.RawJSON, raw) {
		return ValidatedRow{}, permanentImportError("IMPORT_VALIDATOR_CONTRACT", ErrInvalidImportRow)
	}
	return result, nil
}

type parsedImportRow struct {
	RowNo int
	// Type 是该行的资产类型：单类型批与批级类型一致，BUNDLE 批逐行确定。
	Type   AssetType
	Raw    json.RawMessage
	Values map[string]string
	Issues []ValidationIssue
	Layer  int
	// CreateOnly / BundleAsset 只在 Bundle 展开行上出现：前者携带批级
	// CREATE_ONLY 模式，后者是来源 Bundle 资产下标（报表回溯用）。
	CreateOnly  bool
	BundleAsset string
}

type batchIndex struct {
	Codes map[string]map[string][]int
	Rows  map[int]*parsedImportRow
}

// buildBatchIndex 按每行自身的类型建立 code 命名空间。BUNDLE 批因此天然
// 支持跨分区引用：METRIC 行可以引用同批 MODEL/DIMENSION 行的 code。
func buildBatchIndex(rows []parsedImportRow) batchIndex {
	result := batchIndex{Codes: map[string]map[string][]int{}, Rows: map[int]*parsedImportRow{}}
	for index := range rows {
		row := &rows[index]
		result.Rows[row.RowNo] = row
		kind := referenceKindForAsset(row.Type)
		if row.Layer != 1 || kind == "" {
			continue
		}
		code := canonicalLookup(row.Values[primaryCodeColumn(row.Type)])
		if code == "" {
			continue
		}
		if result.Codes[kind] == nil {
			result.Codes[kind] = map[string][]int{}
		}
		result.Codes[kind][code] = append(result.Codes[kind][code], row.RowNo)
	}
	return result
}

func normalizeValidationSnapshot(snapshot ValidationSnapshot) ValidationSnapshot {
	if snapshot.References == nil {
		snapshot.References = map[string]map[string]ValidationReference{}
	}
	if snapshot.Owners == nil {
		snapshot.Owners = map[string]string{}
	}
	if snapshot.Roles == nil {
		snapshot.Roles = map[string]string{}
	}
	if snapshot.Datasets == nil {
		snapshot.Datasets = map[string]bool{}
	}
	if snapshot.Fields == nil {
		snapshot.Fields = map[string]map[string]bool{}
	}
	if snapshot.HighCardinalityFields == nil {
		snapshot.HighCardinalityFields = map[string]map[string]bool{}
	}
	if snapshot.Names == nil {
		snapshot.Names = map[string]map[string]struct{}{}
	}
	if snapshot.Aliases == nil {
		snapshot.Aliases = map[string]struct{}{}
	}
	if snapshot.AuthoritativeDefinitions == nil {
		snapshot.AuthoritativeDefinitions = map[string]string{}
	}
	return snapshot
}

func referenceKindForAsset(assetType AssetType) string {
	switch assetType {
	case AssetModel:
		return "MODEL"
	case AssetMeasure:
		return "MEASURE"
	case AssetMetric:
		return "METRIC"
	case AssetDimension:
		return "DIMENSION"
	case AssetMember:
		return "MEMBER"
	case AssetHierarchy:
		return "HIERARCHY"
	case AssetTerm:
		return "TERM"
	case AssetKPIBundle:
		return "KPI_BUNDLE"
	case AssetEvalCase:
		return "EVAL_CASE"
	case AssetCertifiedExample:
		return "CERTIFIED_EXAMPLE"
	case AssetKnowledge:
		return "KNOWLEDGE"
	default:
		return ""
	}
}

func primaryCodeColumn(assetType AssetType) string {
	switch assetType {
	case AssetTerm:
		return "term"
	case AssetMember:
		return "canonicalValue"
	case AssetCertifiedExample, AssetEvalCase:
		return "question"
	default:
		return "code"
	}
}

// informationalIssueCodes 是不改变行有效性的事实标注：影响确认提醒与
// 创建/更新/未变化裁决。其余任何裁决码都阻断该行。
var informationalIssueCodes = map[string]struct{}{
	ImportImpactRequiresReview: {},
	ImportWillUpdate:           {},
	ImportContentUnchanged:     {},
}

func hasBlockingIssue(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if _, informational := informationalIssueCodes[issue.Code]; !informational {
			return true
		}
	}
	return false
}

func rowIsUnchanged(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Code == ImportContentUnchanged {
			return true
		}
	}
	return false
}

func addIssue(row *parsedImportRow, column, code, message, expected, actual string) {
	column = sanitizeIssueText(column, 128)
	message = sanitizeIssueText(message, 2048)
	expected = sanitizeIssueText(expected, 1024)
	actual = sanitizeIssueText(actual, 1024)
	if row == nil || column == "" || message == "" || expected == "" {
		return
	}
	issue := ValidationIssue{
		Column: column, Code: code, Message: message,
		Expected: expected, Actual: actual,
	}
	for _, existing := range row.Issues {
		if existing == issue {
			return
		}
	}
	row.Issues = append(row.Issues, issue)
}

func sanitizeIssueText(value string, limit int) string {
	value = strings.TrimSpace(strings.Map(func(character rune) rune {
		if character == '\x00' || character == '\n' || character == '\r' || character == '\t' {
			return ' '
		}
		return character
	}, value))
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return strings.TrimSpace(value)
}

func sortIssues(issues []ValidationIssue) {
	sort.SliceStable(issues, func(left, right int) bool {
		if issues[left].Column != issues[right].Column {
			return issues[left].Column < issues[right].Column
		}
		if issues[left].Code != issues[right].Code {
			return issues[left].Code < issues[right].Code
		}
		return issues[left].Actual < issues[right].Actual
	})
}

func canonicalLookup(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

func jsonBytesEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, _ := json.Marshal(leftValue)
	rightCanonical, _ := json.Marshal(rightValue)
	return string(leftCanonical) == string(rightCanonical)
}

type PostgresValidationCatalog struct{ pool *pgxpool.Pool }

func NewPostgresValidationCatalog(pool *pgxpool.Pool) *PostgresValidationCatalog {
	return &PostgresValidationCatalog{pool: pool}
}

func (catalog *PostgresValidationCatalog) LoadValidationSnapshot(
	ctx context.Context,
	tenantID, domainID string,
) (ValidationSnapshot, error) {
	if catalog == nil || catalog.pool == nil || !canonicalUUID(tenantID) || !canonicalUUID(domainID) {
		return ValidationSnapshot{}, ErrImportValidationCatalog
	}
	snapshot := normalizeValidationSnapshot(ValidationSnapshot{})
	err := database.WithTenantTx(ctx, catalog.pool, tenantID, func(tx pgx.Tx) error {
		if err := loadSemanticReferences(ctx, tx, domainID, &snapshot); err != nil {
			return err
		}
		if err := loadValidationOwners(ctx, tx, &snapshot); err != nil {
			return err
		}
		if err := loadValidationDatasets(ctx, tx, domainID, &snapshot); err != nil {
			return err
		}
		return loadValidationAliases(ctx, tx, domainID, &snapshot)
	})
	if err != nil {
		return ValidationSnapshot{}, fmt.Errorf("%w: %v", ErrImportValidationCatalog, err)
	}
	return snapshot, nil
}

func putReference(snapshot *ValidationSnapshot, reference ValidationReference) {
	kind := strings.ToUpper(reference.Kind)
	code := canonicalLookup(reference.Code)
	if kind == "" || code == "" {
		return
	}
	if snapshot.References[kind] == nil {
		snapshot.References[kind] = map[string]ValidationReference{}
	}
	if _, exists := snapshot.References[kind][code]; !exists {
		snapshot.References[kind][code] = reference
	}
	if snapshot.Names[kind] == nil {
		snapshot.Names[kind] = map[string]struct{}{}
	}
	if normalized := canonicalLookup(reference.Name); normalized != "" {
		snapshot.Names[kind][normalized] = struct{}{}
	}
}
