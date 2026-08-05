package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

const (
	validationCodeRequired            = "REG_REQUIRED"
	validationCodeInvalidID           = "REG_INVALID_ID"
	validationCodeInvalidEnum         = "REG_INVALID_ENUM"
	validationCodeInvalidAST          = "REG_INVALID_AST"
	validationCodeInvalidAdditivity   = "REG_INVALID_ADDITIVITY"
	validationCodeInvalidDependency   = "REG_INVALID_DEPENDENCY"
	validationCodeUnsafeFanout        = "REG_UNSAFE_FANOUT"
	validationCodeInvalidMemberPolicy = "REG_INVALID_MEMBER_POLICY"
	validationCodeDuplicate           = "REG_DUPLICATE"
)

var semanticCodePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

type ValidationIssue struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

type ValidationErrors struct {
	Issues []ValidationIssue `json:"issues"`
}

func (validation ValidationErrors) Error() string {
	if len(validation.Issues) == 0 {
		return "semantic registry validation failed"
	}
	return fmt.Sprintf("semantic registry validation failed at %s (%s)", validation.Issues[0].Path, validation.Issues[0].Code)
}

type validator struct{ issues []ValidationIssue }

func (validation *validator) add(code, path, message string) {
	validation.issues = append(validation.issues, ValidationIssue{Code: code, Path: path, Message: message})
}

func (validation *validator) result() error {
	if len(validation.issues) == 0 {
		return nil
	}
	sort.SliceStable(validation.issues, func(left, right int) bool {
		if validation.issues[left].Path == validation.issues[right].Path {
			return validation.issues[left].Code < validation.issues[right].Code
		}
		return validation.issues[left].Path < validation.issues[right].Path
	})
	return ValidationErrors{Issues: validation.issues}
}

func (model SemanticModel) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, model.VersionIdentity, "")
	validateCodeName(&validation, model.Code, model.Name)
	validateUUID(&validation, "datasetId", model.DatasetID, true)
	validateUUID(&validation, "datasetVersionId", model.DatasetVersionID, true)
	validateUUID(&validation, "materializationId", model.MaterializationID, true)
	if model.EntityVersionID != "" {
		validateUUID(&validation, "entityVersionId", model.EntityVersionID, true)
	}
	if err := model.DatasetSchemaHash.Validate(); err != nil {
		validation.add(validationCodeInvalidID, "datasetSchemaHash", err.Error())
	}
	if model.Layer != "DWS" && model.Layer != "ADS" {
		validation.add(validationCodeInvalidEnum, "layer", "must be DWS or ADS")
	}
	validateJSONObject(&validation, "grainContract", model.GrainContract, 65536)
	if model.PrimaryTimeFieldID != "" && !semanticCodePattern.MatchString(model.PrimaryTimeFieldID) {
		validation.add(validationCodeInvalidID, "primaryTimeFieldId", "must be a stable logical field ID")
	}
	return validation.result()
}

func (entity Entity) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, entity.VersionIdentity, "")
	validateCodeName(&validation, entity.Code, entity.Name)
	validateJSONObject(&validation, "keyContract", entity.KeyContract, 65536)
	return validation.result()
}

func (measure Measure) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, measure.VersionIdentity, "")
	validateUUID(&validation, "semanticModelVersionId", measure.SemanticModelVersionID, true)
	validateCodeName(&validation, measure.Code, measure.Name)
	validateJSONObject(&validation, "formulaAst", measure.FormulaAST, 65536)
	if !validAggregation(measure.Aggregation) {
		validation.add(validationCodeInvalidEnum, "aggregation", "unsupported aggregation")
	}
	if !validAdditivity(measure.Additivity) {
		validation.add(validationCodeInvalidEnum, "additivity", "unsupported additivity")
	}
	if measure.DataType != NumericInteger && measure.DataType != NumericDecimal {
		validation.add(validationCodeInvalidEnum, "dataType", "must be INTEGER or DECIMAL")
	}
	if (measure.Aggregation == AggregationAverage || measure.Aggregation == AggregationCountDistinct) && measure.Additivity != NonAdditive {
		validation.add(validationCodeInvalidAdditivity, "additivity", "AVG and COUNT_DISTINCT must be NON_ADDITIVE")
	}
	if (measure.Aggregation == AggregationMinimum || measure.Aggregation == AggregationMaximum) && measure.Additivity == Additive {
		validation.add(validationCodeInvalidAdditivity, "additivity", "MIN and MAX cannot be ADDITIVE")
	}
	return validation.result()
}

func (metric Metric) Validate() error {
	validation := validator{}
	validateUUID(&validation, "id", metric.ID, true)
	validateUUID(&validation, "tenantId", metric.TenantID, true)
	validateUUID(&validation, "domainId", metric.DomainID, true)
	validateUUID(&validation, "ownerId", metric.OwnerID, true)
	validateCodeName(&validation, metric.Code, metric.Name)
	if metric.Status != "DRAFT" && metric.Status != "ACTIVE" && metric.Status != "DEPRECATED" {
		validation.add(validationCodeInvalidEnum, "status", "must be DRAFT, ACTIVE or DEPRECATED")
	}
	if metric.Version < 1 {
		validation.add(validationCodeRequired, "version", "must be positive")
	}
	return validation.result()
}

func (metric MetricVersion) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, metric.VersionIdentity, "")
	validateUUID(&validation, "metricId", metric.MetricID, true)
	validateUUID(&validation, "semanticModelVersionId", metric.SemanticModelVersionID, true)
	validateJSONObject(&validation, "formulaAst", metric.FormulaAST, 131072)
	validateJSONObject(&validation, "defaultFiltersAst", metric.DefaultFiltersAST, 65536)
	if !validAdditivity(metric.Additivity) {
		validation.add(validationCodeInvalidEnum, "additivity", "unsupported additivity")
	}
	if !oneOf(metric.TimeGrain, "NONE", "DAY", "WEEK", "MONTH", "QUARTER", "YEAR") {
		validation.add(validationCodeInvalidEnum, "timeGrain", "unsupported time grain")
	}
	if !oneOf(metric.NullPolicy, "PRESERVE", "ZERO", "REJECT") {
		validation.add(validationCodeInvalidEnum, "nullPolicy", "unsupported null policy")
	}
	if len(metric.MeasureVersionIDs) == 0 || len(metric.MeasureVersionIDs) > 64 {
		validation.add(validationCodeInvalidDependency, "measureVersionIds", "must contain 1 to 64 measure versions")
	}
	seen := map[string]struct{}{}
	for index, id := range metric.MeasureVersionIDs {
		path := fmt.Sprintf("measureVersionIds[%d]", index)
		validateUUID(&validation, path, id, true)
		if _, exists := seen[id]; exists {
			validation.add(validationCodeDuplicate, path, "measure version is duplicated")
		}
		seen[id] = struct{}{}
	}
	return validation.result()
}

func (dimension Dimension) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, dimension.VersionIdentity, "")
	validateUUID(&validation, "semanticModelVersionId", dimension.SemanticModelVersionID, true)
	validateCodeName(&validation, dimension.Code, dimension.Name)
	if !semanticCodePattern.MatchString(dimension.LogicalFieldID) {
		validation.add(validationCodeInvalidID, "logicalFieldId", "must be a stable logical field ID")
	}
	if dimension.Kind != DimensionCategorical && dimension.Kind != DimensionTime && dimension.Kind != DimensionEntity {
		validation.add(validationCodeInvalidEnum, "kind", "unsupported dimension kind")
	}
	if !validSensitivity(dimension.Sensitivity) {
		validation.add(validationCodeInvalidEnum, "sensitivity", "unsupported sensitivity")
	}
	if !validMemberPolicy(dimension.MemberIndexPolicy) {
		validation.add(validationCodeInvalidEnum, "memberIndexPolicy", "unsupported member indexing policy")
	}
	if dimension.HighCardinality && dimension.MemberIndexPolicy != MemberIndexOnDemand && dimension.MemberIndexPolicy != MemberIndexNone {
		validation.add(validationCodeInvalidMemberPolicy, "memberIndexPolicy", "high-cardinality dimensions must be ON_DEMAND or NONE")
	}
	if (dimension.Sensitivity == SensitivityConfidential || dimension.Sensitivity == SensitivityRestricted) &&
		dimension.MemberIndexPolicy != MemberIndexExactOnly && dimension.MemberIndexPolicy != MemberIndexNone {
		validation.add(validationCodeInvalidMemberPolicy, "memberIndexPolicy", "sensitive dimensions must be EXACT_ONLY or NONE")
	}
	return validation.result()
}

func (relationship Relationship) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, relationship.VersionIdentity, "")
	validateUUID(&validation, "leftModelVersionId", relationship.LeftModelVersionID, true)
	validateUUID(&validation, "rightModelVersionId", relationship.RightModelVersionID, true)
	if relationship.LeftModelVersionID == relationship.RightModelVersionID {
		validation.add(validationCodeInvalidDependency, "rightModelVersionId", "must differ from leftModelVersionId")
	}
	if relationship.Type != RelationshipModelJoin && relationship.Type != RelationshipEntityLink && relationship.Type != RelationshipDimensionCompatibility {
		validation.add(validationCodeInvalidEnum, "type", "unsupported relationship type")
	}
	if !oneOf(string(relationship.JoinType), "INNER", "LEFT", "RIGHT", "FULL", "NONE") {
		validation.add(validationCodeInvalidEnum, "joinType", "unsupported join type")
	}
	if relationship.Type == RelationshipModelJoin && relationship.JoinType == JoinNone {
		validation.add(validationCodeInvalidDependency, "joinType", "MODEL_JOIN requires an executable join type")
	}
	if !oneOf(string(relationship.Cardinality), "ONE_TO_ONE", "MANY_TO_ONE", "ONE_TO_MANY", "MANY_TO_MANY") {
		validation.add(validationCodeInvalidEnum, "cardinality", "unsupported cardinality")
	}
	if !oneOf(string(relationship.FanoutPolicy), "BLOCK", "CERTIFIED_PREAGG", "SAFE") {
		validation.add(validationCodeInvalidEnum, "fanoutPolicy", "unsupported fanout policy")
	}
	if relationship.Cardinality == CardinalityManyToMany && relationship.FanoutPolicy == FanoutSafe {
		validation.add(validationCodeUnsafeFanout, "fanoutPolicy", "MANY_TO_MANY cannot be declared SAFE")
	}
	validateJSONObject(&validation, "joinAst", relationship.JoinAST, 65536)
	return validation.result()
}

func (hierarchy Hierarchy) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, hierarchy.VersionIdentity, "")
	validateCodeName(&validation, hierarchy.Code, hierarchy.Name)
	if len(hierarchy.DimensionVersionIDs) < 2 || len(hierarchy.DimensionVersionIDs) > 32 {
		validation.add(validationCodeInvalidDependency, "dimensionVersionIds", "must contain 2 to 32 ordered dimensions")
	}
	seen := make(map[string]struct{}, len(hierarchy.DimensionVersionIDs))
	for index, id := range hierarchy.DimensionVersionIDs {
		path := fmt.Sprintf("dimensionVersionIds[%d]", index)
		validateUUID(&validation, path, id, true)
		if _, exists := seen[id]; exists {
			validation.add(validationCodeDuplicate, path, "dimension version is duplicated")
		}
		seen[id] = struct{}{}
	}
	return validation.result()
}

func (rule QualityRule) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, rule.VersionIdentity, "")
	validateUUID(&validation, "targetVersionId", rule.TargetVersionID, true)
	validateCodeName(&validation, rule.Code, rule.Name)
	if !oneOf(rule.TargetType, "SEMANTIC_MODEL", "METRIC", "DIMENSION") {
		validation.add(validationCodeInvalidEnum, "targetType", "unsupported quality target")
	}
	if !oneOf(rule.Severity, "INFO", "WARNING", "BLOCKING") {
		validation.add(validationCodeInvalidEnum, "severity", "unsupported quality severity")
	}
	validateJSONObject(&validation, "ruleAst", rule.RuleAST, 65536)
	return validation.result()
}

func (term BusinessTerm) Validate() error {
	validation := validator{}
	validateVersionIdentity(&validation, term.VersionIdentity, "")
	validateCodeName(&validation, term.Code, term.Name)
	if strings.TrimSpace(term.Definition) == "" || len(term.Definition) > 4000 {
		validation.add(validationCodeRequired, "definition", "must contain 1 to 4000 characters")
	}
	if len(term.Aliases) > 64 {
		validation.add(validationCodeInvalidDependency, "aliases", "cannot contain more than 64 aliases")
	}
	seen := make(map[string]struct{}, len(term.Aliases))
	for index, alias := range term.Aliases {
		normalized := strings.ToLower(strings.TrimSpace(alias))
		if normalized == "" || len(alias) > 512 {
			validation.add(validationCodeRequired, fmt.Sprintf("aliases[%d]", index), "must contain 1 to 512 characters")
			continue
		}
		if _, exists := seen[normalized]; exists {
			validation.add(validationCodeDuplicate, fmt.Sprintf("aliases[%d]", index), "alias is duplicated after normalization")
		}
		seen[normalized] = struct{}{}
	}
	return validation.result()
}

func validateVersionIdentity(validation *validator, identity VersionIdentity, prefix string) {
	validateUUID(validation, prefix+"id", identity.ID, true)
	validateUUID(validation, prefix+"tenantId", identity.TenantID, true)
	validateUUID(validation, prefix+"domainId", identity.DomainID, true)
	validateUUID(validation, prefix+"objectId", identity.ObjectID, true)
	validateUUID(validation, prefix+"ownerId", identity.OwnerID, true)
	if identity.VersionNo < 1 {
		validation.add(validationCodeRequired, prefix+"versionNo", "must be positive")
	}
	if identity.Status != VersionStatusDraft && identity.Status != VersionStatusCertified && identity.Status != VersionStatusDeprecated {
		validation.add(validationCodeInvalidEnum, prefix+"status", "unsupported version status")
	}
	if err := identity.ContentHash.Validate(); err != nil {
		validation.add(validationCodeInvalidID, prefix+"contentHash", err.Error())
	}
}

func validateUUID(validation *validator, path, value string, required bool) {
	if value == "" {
		if required {
			validation.add(validationCodeRequired, path, "is required")
		}
		return
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != strings.ToLower(value) {
		validation.add(validationCodeInvalidID, path, "must be a canonical UUID")
	}
}

func validateCodeName(validation *validator, code, name string) {
	if !semanticCodePattern.MatchString(code) {
		validation.add(validationCodeInvalidID, "code", "must be a stable semantic code")
	}
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		validation.add(validationCodeRequired, "name", "must contain 1 to 200 characters")
	}
}

func validateJSONObject(validation *validator, path string, raw json.RawMessage, maxBytes int) {
	if len(raw) == 0 || len(raw) > maxBytes || !utf8.Valid(raw) {
		validation.add(validationCodeInvalidAST, path, fmt.Sprintf("must be a UTF-8 JSON object of at most %d bytes", maxBytes))
		return
	}
	var document map[string]any
	if err := askdata.DecodeStrictJSON(raw, &document); err != nil {
		validation.add(validationCodeInvalidAST, path, err.Error())
		return
	}
	if document == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		validation.add(validationCodeInvalidAST, path, "must be a JSON object")
		return
	}
	nodes := 0
	if err := validateASTValue(document, 0, &nodes); err != nil {
		validation.add(validationCodeInvalidAST, path, err.Error())
	}
}

func validateASTValue(value any, depth int, nodes *int) error {
	*nodes++
	if depth > 32 || *nodes > 4096 {
		return fmt.Errorf("AST exceeds depth or node limits")
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			if normalized == "sql" || normalized == "rawsql" || normalized == "ngql" || normalized == "query" || normalized == "password" || normalized == "credential" || normalized == "secret" {
				return fmt.Errorf("AST key %q is forbidden", key)
			}
			if err := validateASTValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateASTValue(child, depth+1, nodes); err != nil {
				return err
			}
		}
	case string:
		if strings.ContainsAny(typed, "\x00\r\n") {
			return fmt.Errorf("AST strings cannot contain control characters")
		}
	}
	return nil
}

func validAggregation(value Aggregation) bool {
	return value == AggregationSum || value == AggregationAverage || value == AggregationMinimum ||
		value == AggregationMaximum || value == AggregationCount || value == AggregationCountDistinct
}

func validAdditivity(value Additivity) bool {
	return value == Additive || value == SemiAdditive || value == NonAdditive
}

func validSensitivity(value Sensitivity) bool {
	return value == SensitivityPublic || value == SensitivityInternal ||
		value == SensitivityConfidential || value == SensitivityRestricted
}

func validMemberPolicy(value MemberIndexPolicy) bool {
	return value == MemberIndexFull || value == MemberIndexExactOnly || value == MemberIndexOnDemand || value == MemberIndexNone
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
