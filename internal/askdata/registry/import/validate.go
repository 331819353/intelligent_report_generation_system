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
}

type ValidationCatalog interface {
	LoadValidationSnapshot(context.Context, string, string) (ValidationSnapshot, error)
}

type MemberTargetCatalog interface {
	ResolveImportMemberTargets(
		context.Context, string, string, []string,
	) (map[string]ValidationReference, error)
}

type FourLayerValidator struct{ catalog ValidationCatalog }

func NewFourLayerValidator(catalog ValidationCatalog) *FourLayerValidator {
	return &FourLayerValidator{catalog: catalog}
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
		row := parseAndValidateL1(claim.AssetType, input)
		parsed = append(parsed, row)
	}
	snapshot, err := validator.catalog.LoadValidationSnapshot(
		ctx, claim.TenantID, claim.DomainID,
	)
	if err != nil {
		return nil, err
	}
	snapshot = normalizeValidationSnapshot(snapshot)
	memberTargets := []string{}
	if claim.AssetType == AssetTerm {
		seen := map[string]struct{}{}
		for _, row := range parsed {
			if row.Layer != 1 || row.Values["termType"] != "MEMBER" {
				continue
			}
			target := row.Values["targetCode"]
			key := canonicalLookup(target)
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				memberTargets = append(memberTargets, target)
			}
		}
	}
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
	batch := buildBatchIndex(claim.AssetType, parsed)
	validateL2(parsed, batch, session.snapshot)
	validateL3(parsed, batch, session.snapshot)
	validateL4(parsed, batch, session.snapshot)
	for _, row := range parsed {
		sortIssues(row.Issues)
		state := RowValid
		if hasBlockingIssue(row.Issues) {
			state = RowInvalid
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
	RowNo  int
	Raw    json.RawMessage
	Values map[string]string
	Issues []ValidationIssue
	Layer  int
}

type batchIndex struct {
	Codes map[string]map[string][]int
	Rows  map[int]*parsedImportRow
}

func buildBatchIndex(assetType AssetType, rows []parsedImportRow) batchIndex {
	result := batchIndex{Codes: map[string]map[string][]int{}, Rows: map[int]*parsedImportRow{}}
	kind := referenceKindForAsset(assetType)
	for index := range rows {
		row := &rows[index]
		result.Rows[row.RowNo] = row
		if row.Layer != 1 || kind == "" {
			continue
		}
		code := canonicalLookup(row.Values[primaryCodeColumn(assetType)])
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

func hasBlockingIssue(issues []ValidationIssue) bool {
	for _, issue := range issues {
		if issue.Code != ImportImpactRequiresReview {
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
