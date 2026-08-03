package semanticasset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	SemanticProjectionExecution = "EXECUTION_SEMANTIC_LAYER"
	SemanticProjectionRegistry  = "POSTGRES_REGISTRY"
	SemanticProjectionSearch    = "SEARCH_INDEX"
	SemanticProjectionNebula    = "NEBULA_GRAPH"
)

var semanticVersionPattern = regexp.MustCompile(
	`^[A-Za-z0-9][A-Za-z0-9._:-]{2,127}$`,
)

var semanticReleaseObjectTypes = map[string]bool{
	"DOMAIN": true, "BUSINESS_TERM": true, "ENTITY": true,
	"SEMANTIC_MODEL": true, "MEASURE": true, "METRIC": true,
	"DIMENSION": true, "DIMENSION_VALUE": true, "TIME": true,
	"COHORT": true, "RELATION": true, "DATASET": true,
	"TABLE_COLUMN": true, "POLICY": true, "QUALITY_RULE": true,
	"CERTIFIED_EXAMPLE": true, "PARSING_RULE": true,
}

var semanticReleaseRequiredTypes = []string{
	"METRIC", "DIMENSION", "TIME", "RELATION", "DATASET", "POLICY",
	"QUALITY_RULE",
}

var semanticContractRequiredFields = map[string][]string{
	"DOMAIN":            {"title"},
	"BUSINESS_TERM":     {"title", "mappingType", "targetIds"},
	"ENTITY":            {"title", "grain", "primaryKey"},
	"SEMANTIC_MODEL":    {"title", "primaryEntityId", "sourceDatasetId", "grain"},
	"MEASURE":           {"title", "expression", "aggregation"},
	"METRIC":            {"title", "formula", "grain", "defaultTimeDimensionId", "sourceDatasetIds", "permissionPolicyIds", "qualityRuleIds"},
	"DIMENSION":         {"title", "valueKey", "usages"},
	"DIMENSION_VALUE":   {"title", "dimensionId", "canonicalCode"},
	"TIME":              {"title", "timezone", "calendar", "completePeriodPolicy"},
	"COHORT":            {"title", "entryEvent", "observationWindow", "exclusions"},
	"RELATION":          {"title", "relationType", "fromId", "toId", "cardinality", "certified", "fanoutPolicy"},
	"DATASET":           {"title", "grain", "source", "freshness"},
	"TABLE_COLUMN":      {"title", "datasetId", "dataType"},
	"POLICY":            {"title", "roles", "purpose", "effect", "accessibleObjectIds"},
	"QUALITY_RULE":      {"title", "targetId", "severity", "validator"},
	"CERTIFIED_EXAMPLE": {"title", "question", "intent"},
	"PARSING_RULE":      {"title", "ruleType", "pattern", "action"},
}

type SemanticReleaseObjectInput struct {
	ObjectType    string          `json:"objectType"`
	ObjectID      string          `json:"objectId"`
	ObjectVersion string          `json:"objectVersion"`
	DomainID      string          `json:"domainId,omitempty"`
	OwnerID       string          `json:"ownerId"`
	Certification string          `json:"certification"`
	Sensitivity   string          `json:"sensitivity"`
	ValidFrom     time.Time       `json:"validFrom"`
	ValidTo       *time.Time      `json:"validTo,omitempty"`
	Contract      json.RawMessage `json:"contract"`
}

type CreateSemanticReleaseInput struct {
	SemanticVersion string                       `json:"semanticVersion"`
	BaseReleaseID   string                       `json:"baseReleaseId,omitempty"`
	Notes           string                       `json:"notes,omitempty"`
	Objects         []SemanticReleaseObjectInput `json:"objects"`
}

type ValidateSemanticReleaseInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type ActivateSemanticReleaseInput struct {
	ExpectedVersion      int64  `json:"expectedVersion"`
	ExpectedStateVersion int64  `json:"expectedStateVersion"`
	EvaluationSetID      string `json:"evaluationSetId,omitempty"`
}

type SemanticReleaseObject struct {
	SemanticReleaseObjectInput
	ID          string    `json:"id"`
	ContentHash string    `json:"contentHash"`
	CreatedAt   time.Time `json:"createdAt"`
}

type SemanticReleaseProjection struct {
	ID                  string          `json:"id"`
	Target              string          `json:"target"`
	Status              string          `json:"status"`
	ExpectedContentHash string          `json:"expectedContentHash"`
	AppliedContentHash  string          `json:"appliedContentHash,omitempty"`
	ResourceVersion     string          `json:"resourceVersion,omitempty"`
	ObjectCount         int             `json:"objectCount"`
	ErrorCode           string          `json:"errorCode,omitempty"`
	Detail              json.RawMessage `json:"detail"`
	Version             int64           `json:"version"`
	StartedAt           *time.Time      `json:"startedAt,omitempty"`
	CompletedAt         *time.Time      `json:"completedAt,omitempty"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

type SemanticRelease struct {
	ID                       string                      `json:"id"`
	SemanticVersion          string                      `json:"semanticVersion"`
	ContentHash              string                      `json:"contentHash"`
	Status                   string                      `json:"status"`
	BaseReleaseID            string                      `json:"baseReleaseId,omitempty"`
	Notes                    string                      `json:"notes,omitempty"`
	ObjectCount              int                         `json:"objectCount"`
	ValidationSummary        json.RawMessage             `json:"validationSummary"`
	Version                  int64                       `json:"version"`
	CreatedBy                string                      `json:"createdBy"`
	UpdatedBy                string                      `json:"updatedBy"`
	ActivatedBy              string                      `json:"activatedBy,omitempty"`
	EvaluationSetID          string                      `json:"evaluationSetId,omitempty"`
	EvaluationSetContentHash string                      `json:"evaluationSetContentHash,omitempty"`
	CreatedAt                time.Time                   `json:"createdAt"`
	UpdatedAt                time.Time                   `json:"updatedAt"`
	ValidatedAt              *time.Time                  `json:"validatedAt,omitempty"`
	ActivatedAt              *time.Time                  `json:"activatedAt,omitempty"`
	Projections              []SemanticReleaseProjection `json:"projections"`
	Objects                  []SemanticReleaseObject     `json:"objects,omitempty"`
}

type SemanticReleaseState struct {
	ActiveReleaseID string    `json:"activeReleaseId,omitempty"`
	SemanticVersion string    `json:"semanticVersion,omitempty"`
	ContentHash     string    `json:"contentHash,omitempty"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type SemanticReleaseValidation struct {
	Status string                           `json:"status"`
	Issues []SemanticReleaseValidationIssue `json:"issues"`
	Counts map[string]int                   `json:"counts"`
}

type SemanticReleaseValidationIssue struct {
	Code       string `json:"code"`
	ObjectType string `json:"objectType,omitempty"`
	ObjectID   string `json:"objectId,omitempty"`
	Field      string `json:"field,omitempty"`
	Message    string `json:"message"`
}

type semanticReleaseStore interface {
	CreateSemanticRelease(context.Context, string, string, semanticReleaseDraft) (SemanticRelease, error)
	ListSemanticReleases(context.Context, string, Page) ([]SemanticRelease, int, error)
	GetSemanticRelease(context.Context, string, string) (SemanticRelease, error)
	GetActiveSemanticRelease(context.Context, string) (SemanticReleaseState, error)
	SaveSemanticReleaseValidation(context.Context, string, string, string, int64, SemanticReleaseValidation) (SemanticRelease, error)
	ActivateSemanticRelease(context.Context, string, string, string, string, int64, int64) (SemanticReleaseState, error)
}

type semanticReleaseDraft struct {
	SemanticVersion string
	BaseReleaseID   string
	Notes           string
	ContentHash     string
	Objects         []SemanticReleaseObject
}

func (service *Service) CreateSemanticRelease(
	ctx context.Context,
	tenantID, actorID string,
	input CreateSemanticReleaseInput,
) (SemanticRelease, error) {
	store, ok := service.releaseStore()
	if !ok || !validActor(tenantID, actorID) {
		return SemanticRelease{}, ErrInvalidRequest
	}
	draft, err := normalizeSemanticRelease(input)
	if err != nil {
		return SemanticRelease{}, err
	}
	return store.CreateSemanticRelease(ctx, tenantID, actorID, draft)
}

func (service *Service) ListSemanticReleases(
	ctx context.Context,
	tenantID string,
	page Page,
) ([]SemanticRelease, int, error) {
	store, ok := service.releaseStore()
	if !ok || !validUUID(tenantID) || !normalizePage(&page) {
		return nil, 0, ErrInvalidRequest
	}
	return store.ListSemanticReleases(ctx, tenantID, page)
}

func (service *Service) GetSemanticRelease(
	ctx context.Context,
	tenantID, releaseID string,
) (SemanticRelease, error) {
	store, ok := service.releaseStore()
	if !ok || !validUUID(tenantID) || !validUUID(releaseID) {
		return SemanticRelease{}, ErrInvalidRequest
	}
	return store.GetSemanticRelease(ctx, tenantID, releaseID)
}

func (service *Service) GetActiveSemanticRelease(
	ctx context.Context,
	tenantID string,
) (SemanticReleaseState, error) {
	store, ok := service.releaseStore()
	if !ok || !validUUID(tenantID) {
		return SemanticReleaseState{}, ErrInvalidRequest
	}
	return store.GetActiveSemanticRelease(ctx, tenantID)
}

func (service *Service) ValidateSemanticRelease(
	ctx context.Context,
	tenantID, actorID, releaseID string,
	input ValidateSemanticReleaseInput,
) (SemanticRelease, error) {
	store, ok := service.releaseStore()
	if !ok || !validActor(tenantID, actorID) || !validUUID(releaseID) ||
		input.ExpectedVersion < 1 {
		return SemanticRelease{}, ErrInvalidRequest
	}
	release, err := store.GetSemanticRelease(ctx, tenantID, releaseID)
	if err != nil {
		return SemanticRelease{}, err
	}
	if release.Version != input.ExpectedVersion ||
		(release.Status != "DRAFT" && release.Status != "BLOCKED") {
		return SemanticRelease{}, ErrConflict
	}
	validation := validateSemanticReleaseObjects(release.Objects)
	return store.SaveSemanticReleaseValidation(
		ctx, tenantID, actorID, releaseID, input.ExpectedVersion, validation,
	)
}

func (service *Service) ActivateSemanticRelease(
	ctx context.Context,
	tenantID, actorID, releaseID string,
	input ActivateSemanticReleaseInput,
) (SemanticReleaseState, error) {
	store, ok := service.releaseStore()
	if !ok || !validActor(tenantID, actorID) || !validUUID(releaseID) ||
		(input.EvaluationSetID != "" && !validUUID(input.EvaluationSetID)) ||
		input.ExpectedVersion < 1 || input.ExpectedStateVersion < 1 {
		return SemanticReleaseState{}, ErrInvalidRequest
	}
	return store.ActivateSemanticRelease(
		ctx, tenantID, actorID, releaseID, input.EvaluationSetID,
		input.ExpectedVersion, input.ExpectedStateVersion,
	)
}

func (service *Service) releaseStore() (semanticReleaseStore, bool) {
	if service == nil || service.store == nil {
		return nil, false
	}
	store, ok := service.store.(semanticReleaseStore)
	return store, ok
}

func normalizeSemanticRelease(
	input CreateSemanticReleaseInput,
) (semanticReleaseDraft, error) {
	input.SemanticVersion = strings.TrimSpace(input.SemanticVersion)
	input.BaseReleaseID = strings.TrimSpace(input.BaseReleaseID)
	input.Notes = strings.TrimSpace(input.Notes)
	if !semanticVersionPattern.MatchString(input.SemanticVersion) ||
		(input.BaseReleaseID != "" && uuid.Validate(input.BaseReleaseID) != nil) ||
		len([]rune(input.Notes)) > 4096 || containsControlRune(input.Notes) ||
		len(input.Objects) < 1 || len(input.Objects) > 10000 {
		return semanticReleaseDraft{}, ErrInvalidRequest
	}
	objects := make([]SemanticReleaseObject, 0, len(input.Objects))
	seen := make(map[string]bool, len(input.Objects))
	for _, item := range input.Objects {
		normalized, err := normalizeSemanticReleaseObject(item)
		if err != nil {
			return semanticReleaseDraft{}, err
		}
		identity := normalized.ObjectType + "\x00" + normalized.ObjectID +
			"\x00" + normalized.ObjectVersion
		if seen[identity] {
			return semanticReleaseDraft{}, ErrConflict
		}
		seen[identity] = true
		objects = append(objects, normalized)
	}
	sort.Slice(objects, func(left, right int) bool {
		if objects[left].ObjectType != objects[right].ObjectType {
			return objects[left].ObjectType < objects[right].ObjectType
		}
		if objects[left].ObjectID != objects[right].ObjectID {
			return objects[left].ObjectID < objects[right].ObjectID
		}
		return objects[left].ObjectVersion < objects[right].ObjectVersion
	})
	validation := validateSemanticReleaseObjects(objects)
	if validation.Status != "PASS" {
		return semanticReleaseDraft{}, fmt.Errorf(
			"%w: %s", ErrInvalidRequest, validation.Issues[0].Code,
		)
	}
	contentHash, err := hashSemanticRelease(objects)
	if err != nil {
		return semanticReleaseDraft{}, ErrInvalidRequest
	}
	return semanticReleaseDraft{
		SemanticVersion: input.SemanticVersion, BaseReleaseID: input.BaseReleaseID,
		Notes: input.Notes, ContentHash: contentHash, Objects: objects,
	}, nil
}

func normalizeSemanticReleaseObject(
	input SemanticReleaseObjectInput,
) (SemanticReleaseObject, error) {
	input.ObjectType = strings.ToUpper(strings.TrimSpace(input.ObjectType))
	input.ObjectID = strings.TrimSpace(input.ObjectID)
	input.ObjectVersion = strings.TrimSpace(input.ObjectVersion)
	input.DomainID = strings.TrimSpace(input.DomainID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.Certification = strings.ToUpper(strings.TrimSpace(input.Certification))
	input.Sensitivity = strings.ToUpper(strings.TrimSpace(input.Sensitivity))
	if !semanticReleaseObjectTypes[input.ObjectType] ||
		!validReleaseIdentity(input.ObjectID, 256) ||
		!validReleaseIdentity(input.ObjectVersion, 128) ||
		len([]rune(input.DomainID)) > 256 || containsControlRune(input.DomainID) ||
		uuid.Validate(input.OwnerID) != nil || input.Certification != "CERTIFIED" ||
		!oneOfRelease(input.Sensitivity, "PUBLIC", "INTERNAL", "CONFIDENTIAL", "RESTRICTED") ||
		input.ValidFrom.IsZero() ||
		(input.ValidTo != nil && !input.ValidTo.After(input.ValidFrom)) ||
		len(input.Contract) < 2 || len(input.Contract) > 65536 {
		return SemanticReleaseObject{}, ErrInvalidRequest
	}
	var contract map[string]any
	if err := json.Unmarshal(input.Contract, &contract); err != nil || contract == nil {
		return SemanticReleaseObject{}, ErrInvalidRequest
	}
	canonical, err := json.Marshal(contract)
	if err != nil {
		return SemanticReleaseObject{}, ErrInvalidRequest
	}
	input.Contract = canonical
	hash := sha256.Sum256(canonical)
	return SemanticReleaseObject{
		SemanticReleaseObjectInput: input,
		ContentHash:                hex.EncodeToString(hash[:]),
	}, nil
}

func validateSemanticReleaseObjects(
	objects []SemanticReleaseObject,
) SemanticReleaseValidation {
	result := SemanticReleaseValidation{
		Status: "PASS", Issues: []SemanticReleaseValidationIssue{},
		Counts: map[string]int{},
	}
	objectTypesByID := make(map[string][]string, len(objects))
	dimensionValueKeys := map[string]string{}
	for _, object := range objects {
		objectTypesByID[object.ObjectID] = append(
			objectTypesByID[object.ObjectID], object.ObjectType,
		)
	}
	for _, object := range objects {
		result.Counts[object.ObjectType]++
		var contract map[string]any
		if err := json.Unmarshal(object.Contract, &contract); err != nil {
			result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
				Code: "CONTRACT_JSON_INVALID", ObjectType: object.ObjectType,
				ObjectID: object.ObjectID, Message: "合同不是有效 JSON 对象",
			})
			continue
		}
		for _, field := range semanticContractRequiredFields[object.ObjectType] {
			if !semanticContractFieldPresent(contract[field]) {
				result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
					Code: "CONTRACT_FIELD_REQUIRED", ObjectType: object.ObjectType,
					ObjectID: object.ObjectID, Field: field,
					Message: "认证合同缺少必填字段",
				})
			}
		}
		result.Issues = append(result.Issues, validateSemanticAliases(object, contract)...)
		requireReference := func(field, expectedType string) {
			if !semanticReleaseReferenceExists(
				objectTypesByID, stringContractValue(contract[field]), expectedType,
			) {
				result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
					Code: "CONTRACT_REFERENCE_INVALID", ObjectType: object.ObjectType,
					ObjectID: object.ObjectID, Field: field,
					Message: "合同引用必须唯一指向同一发布包中的认证对象",
				})
			}
		}
		requireReferences := func(field, expectedType string) {
			values, valid := semanticContractStrings(contract[field])
			if !valid || len(values) == 0 {
				result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
					Code: "CONTRACT_REFERENCE_INVALID", ObjectType: object.ObjectType,
					ObjectID: object.ObjectID, Field: field,
					Message: "合同引用列表必须包含同一发布包中的认证对象",
				})
				return
			}
			for _, value := range values {
				if !semanticReleaseReferenceExists(objectTypesByID, value, expectedType) {
					result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
						Code: "CONTRACT_REFERENCE_INVALID", ObjectType: object.ObjectType,
						ObjectID: object.ObjectID, Field: field,
						Message: "合同引用必须唯一指向同一发布包中的认证对象",
					})
				}
			}
		}
		switch object.ObjectType {
		case "METRIC":
			requireReference("defaultTimeDimensionId", "TIME")
			requireReferences("sourceDatasetIds", "DATASET")
			requireReferences("permissionPolicyIds", "POLICY")
			requireReferences("qualityRuleIds", "QUALITY_RULE")
			if contract["groupableDimensionIds"] != nil {
				requireReferences("groupableDimensionIds", "DIMENSION")
			}
		case "DIMENSION_VALUE":
			requireReference("dimensionId", "DIMENSION")
			key := stringContractValue(contract["dimensionId"]) + "\x00" +
				strings.ToLower(stringContractValue(contract["canonicalCode"]))
			if previous := dimensionValueKeys[key]; key == "\x00" || previous != "" {
				result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
					Code:       "DIMENSION_VALUE_COMPOSITE_KEY_CONFLICT",
					ObjectType: object.ObjectType, ObjectID: object.ObjectID,
					Field:   "canonicalCode",
					Message: "维度值必须使用维度作用域内唯一的规范码",
				})
			} else {
				dimensionValueKeys[key] = object.ObjectID
			}
		case "QUALITY_RULE":
			requireReference("targetId", "")
		case "POLICY":
			requireReferences("accessibleObjectIds", "")
			roles, rolesValid := semanticContractStrings(contract["roles"])
			purposes, purposesValid := semanticContractStrings(contract["purpose"])
			effect := strings.ToUpper(stringContractValue(contract["effect"]))
			if !rolesValid || len(roles) == 0 || !purposesValid || len(purposes) == 0 ||
				(effect != "ALLOW" && effect != "DENY") {
				result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
					Code: "POLICY_CONTRACT_INVALID", ObjectType: object.ObjectType,
					ObjectID: object.ObjectID,
					Message:  "策略必须具有角色、用途和明确的 ALLOW/DENY 效果",
				})
			}
		}
		if object.ObjectType == "RELATION" {
			certified, _ := contract["certified"].(bool)
			cardinality, _ := contract["cardinality"].(string)
			fanout, _ := contract["fanoutPolicy"].(string)
			relationType, _ := contract["relationType"].(string)
			if !certified || cardinality == "unknown" ||
				!semanticGraphRelationTypeAllowed(relationType) ||
				strings.EqualFold(fanout, "UNSAFE") {
				result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
					Code: "RELATION_NOT_EXECUTION_SAFE", ObjectType: object.ObjectType,
					ObjectID: object.ObjectID,
					Message:  "关系必须认证、基数已知且 fanout 策略安全",
				})
			}
			for _, endpoint := range []struct {
				field, typeField string
			}{
				{"fromId", "fromType"}, {"toId", "toType"},
			} {
				if !semanticReleaseReferenceExists(
					objectTypesByID,
					stringContractValue(contract[endpoint.field]),
					stringContractValue(contract[endpoint.typeField]),
				) {
					result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
						Code: "RELATION_ENDPOINT_INVALID", ObjectType: object.ObjectType,
						ObjectID: object.ObjectID, Field: endpoint.field,
						Message: "关系端点必须唯一引用同一发布包内对象",
					})
				}
			}
		}
	}
	for _, objectType := range semanticReleaseRequiredTypes {
		if result.Counts[objectType] == 0 {
			result.Issues = append(result.Issues, SemanticReleaseValidationIssue{
				Code: "REQUIRED_OBJECT_TYPE_MISSING", ObjectType: objectType,
				Message: "生产语义发布缺少必需对象类型",
			})
		}
	}
	if len(result.Issues) > 0 {
		result.Status = "BLOCKED"
	}
	return result
}

func validateSemanticAliases(
	object SemanticReleaseObject,
	contract map[string]any,
) []SemanticReleaseValidationIssue {
	issues := []SemanticReleaseValidationIssue{}
	positive, negative := map[string]bool{}, map[string]bool{}
	for _, field := range []string{
		"aliases", "synonyms", "abbreviations", "shortNames", "positiveAliases",
		"negativeAliases", "hardNegativeExamples",
	} {
		if contract[field] == nil {
			continue
		}
		values, valid := semanticContractStrings(contract[field])
		if !valid || len(values) > 128 {
			issues = append(issues, SemanticReleaseValidationIssue{
				Code: "ALIAS_CONTRACT_INVALID", ObjectType: object.ObjectType,
				ObjectID: object.ObjectID, Field: field,
				Message: "别名合同必须是有界非空字符串列表",
			})
			continue
		}
		for _, value := range values {
			key := semanticAliasKey(value)
			if key == "" || len([]rune(value)) > 256 || containsControlRune(value) {
				issues = append(issues, SemanticReleaseValidationIssue{
					Code: "ALIAS_CONTRACT_INVALID", ObjectType: object.ObjectType,
					ObjectID: object.ObjectID, Field: field,
					Message: "别名为空、过长或包含控制字符",
				})
				continue
			}
			if field == "negativeAliases" || field == "hardNegativeExamples" {
				negative[key] = true
			} else {
				positive[key] = true
			}
		}
	}
	for key := range positive {
		if negative[key] {
			issues = append(issues, SemanticReleaseValidationIssue{
				Code: "ALIAS_POLARITY_CONFLICT", ObjectType: object.ObjectType,
				ObjectID: object.ObjectID, Field: "aliases",
				Message: "同一规范化别名不能同时是当前对象的正例和反例",
			})
		}
	}
	if locale, exists := contract["locale"]; exists {
		value, valid := locale.(string)
		if !valid || len(strings.TrimSpace(value)) < 2 || len(value) > 35 {
			issues = append(issues, SemanticReleaseValidationIssue{
				Code: "ALIAS_LOCALE_INVALID", ObjectType: object.ObjectType,
				ObjectID: object.ObjectID, Field: "locale", Message: "别名 locale 无效",
			})
		}
	}
	return issues
}

func semanticContractStrings(value any) ([]string, bool) {
	result := []string{}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) != "" {
			result = append(result, strings.TrimSpace(typed))
		}
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok || strings.TrimSpace(text) == "" {
				return nil, false
			}
			result = append(result, strings.TrimSpace(text))
		}
	default:
		return nil, false
	}
	return result, true
}

func semanticAliasKey(value string) string {
	return cases.Fold().String(strings.Join(strings.Fields(norm.NFKC.String(value)), " "))
}

func semanticReleaseReferenceExists(
	objectTypesByID map[string][]string,
	reference, expectedType string,
) bool {
	reference = strings.TrimSpace(reference)
	expectedType = strings.ToUpper(strings.TrimSpace(expectedType))
	if separator := strings.IndexByte(reference, ':'); separator > 0 {
		possibleType := strings.ToUpper(reference[:separator])
		if semanticReleaseObjectTypes[possibleType] {
			expectedType, reference = possibleType, reference[separator+1:]
		}
	}
	types := objectTypesByID[reference]
	if expectedType == "" {
		return len(types) == 1
	}
	matches := 0
	for _, objectType := range types {
		if objectType == expectedType {
			matches++
		}
	}
	return matches == 1
}

func stringContractValue(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}

func semanticGraphRelationTypeAllowed(value string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "-", "_"))) {
	case "contains", "measures", "depends_on", "sourced_from",
		"groupable_by", "belongs_to", "has_value", "joins_to",
		"synonym_of", "can_access", "derived_from", "guards", "uses":
		return true
	default:
		return false
	}
}

func hashSemanticRelease(objects []SemanticReleaseObject) (string, error) {
	manifest := make([]map[string]any, 0, len(objects))
	for _, object := range objects {
		manifest = append(manifest, map[string]any{
			"type": object.ObjectType, "id": object.ObjectID,
			"version": object.ObjectVersion, "hash": object.ContentHash,
			"domainId": object.DomainID, "ownerId": object.OwnerID,
			"validFrom": object.ValidFrom.UTC().Format(time.RFC3339Nano),
			"validTo":   semanticReleaseTime(object.ValidTo),
		})
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func semanticReleaseTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func semanticContractFieldPresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func validReleaseIdentity(value string, maximum int) bool {
	length := len([]rune(value))
	return length >= 1 && length <= maximum && value == strings.TrimSpace(value) &&
		!containsControlRune(value)
}

func containsControlRune(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func oneOfRelease(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
