package semanticasset

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// BootstrapSemanticReleaseInput is deliberately explicit about time semantics.
// Legacy assets do not carry a complete calendar contract, so an operator must
// provide it instead of the migration silently guessing one.
type BootstrapSemanticReleaseInput struct {
	SemanticVersion      string `json:"semanticVersion"`
	DefaultTimezone      string `json:"defaultTimezone"`
	DefaultCalendar      string `json:"defaultCalendar"`
	CompletePeriodPolicy string `json:"completePeriodPolicy"`
	Notes                string `json:"notes,omitempty"`
}

type BootstrapSemanticReleaseIssue struct {
	Code       string `json:"code"`
	Severity   string `json:"severity"`
	ObjectType string `json:"objectType,omitempty"`
	ObjectID   string `json:"objectId,omitempty"`
	Message    string `json:"message"`
}

type BootstrapSemanticReleasePreview struct {
	Eligible       bool                            `json:"eligible"`
	SourceCounts   map[string]int                  `json:"sourceCounts"`
	CandidateCount int                             `json:"candidateCount"`
	Issues         []BootstrapSemanticReleaseIssue `json:"issues"`
	Candidate      *CreateSemanticReleaseInput     `json:"candidate,omitempty"`
}

type legacyReleaseSnapshot struct {
	Metrics         []legacyMetric
	Datasets        []legacyDataset
	Dimensions      []legacyDimension
	Compatibilities []legacyCompatibility
	Members         []legacyDimensionMember
	RoleCodes       []string
}

type legacyMetric struct {
	ID, VersionID, DatasetID, DatasetVersionID string
	Code, Name, Description, MetricType        string
	DomainID, OwnerID                          string
	VersionNo                                  int
	PublishedAt                                time.Time
	Definition                                 map[string]any
}

type legacyDataset struct {
	ID, VersionID, Code, Name, Description string
	DomainID, OwnerID                      string
	VersionNo                              int
	PublishedAt                            time.Time
	DSL                                    map[string]any
	PhysicalSchema, PhysicalName           string
}

type legacyDimension struct {
	ID, DatasetID, DatasetVersionID, FieldID string
	Code, Name, Description, DimensionType   string
	DefinitionHash, DomainID, OwnerID        string
	Sensitive, HighCardinality               bool
	MemberIndexPolicy                        string
	UpdatedAt                                time.Time
}

type legacyCompatibility struct {
	MetricVersionID, DimensionID string
	Cardinality, FanoutPolicy    string
}

type legacyDimensionMember struct {
	ID, DimensionID, MemberKey, CanonicalLabel string
	Aliases                                    []string
	ValidFrom                                  *time.Time
	ValidTo                                    *time.Time
}

type semanticReleaseBootstrapStore interface {
	LoadLegacyReleaseSnapshot(context.Context, string) (legacyReleaseSnapshot, error)
}

func (service *Service) PreviewBootstrapSemanticRelease(
	ctx context.Context,
	tenantID, actorID string,
	input BootstrapSemanticReleaseInput,
) (BootstrapSemanticReleasePreview, error) {
	store, ok := service.store.(semanticReleaseBootstrapStore)
	if !ok || !validActor(tenantID, actorID) ||
		!semanticVersionPattern.MatchString(strings.TrimSpace(input.SemanticVersion)) ||
		len([]rune(strings.TrimSpace(input.Notes))) > 4096 ||
		containsControlRune(input.Notes) ||
		!validBootstrapTimeContract(input) {
		return BootstrapSemanticReleasePreview{}, ErrInvalidRequest
	}
	snapshot, err := store.LoadLegacyReleaseSnapshot(ctx, tenantID)
	if err != nil {
		return BootstrapSemanticReleasePreview{}, err
	}
	return buildLegacySemanticReleaseCandidate(actorID, input, snapshot), nil
}

func (service *Service) BootstrapSemanticRelease(
	ctx context.Context,
	tenantID, actorID string,
	input BootstrapSemanticReleaseInput,
) (SemanticRelease, error) {
	preview, err := service.PreviewBootstrapSemanticRelease(ctx, tenantID, actorID, input)
	if err != nil {
		return SemanticRelease{}, err
	}
	if !preview.Eligible || preview.Candidate == nil {
		return SemanticRelease{}, fmt.Errorf("%w: LEGACY_BOOTSTRAP_BLOCKED", ErrInvalidRequest)
	}
	return service.CreateSemanticRelease(ctx, tenantID, actorID, *preview.Candidate)
}

func validBootstrapTimeContract(input BootstrapSemanticReleaseInput) bool {
	zone := strings.TrimSpace(input.DefaultTimezone)
	if zone == "" {
		return false
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return false
	}
	calendar := strings.ToUpper(strings.TrimSpace(input.DefaultCalendar))
	period := strings.ToUpper(strings.TrimSpace(input.CompletePeriodPolicy))
	return oneOfRelease(calendar, "GREGORIAN", "ISO_WEEK", "FISCAL") &&
		oneOfRelease(period, "EXCLUDE_INCOMPLETE", "INCLUDE_INCOMPLETE", "NOT_APPLICABLE")
}

func buildLegacySemanticReleaseCandidate(
	actorID string,
	input BootstrapSemanticReleaseInput,
	snapshot legacyReleaseSnapshot,
) BootstrapSemanticReleasePreview {
	preview := BootstrapSemanticReleasePreview{
		SourceCounts: map[string]int{
			"metrics": len(snapshot.Metrics), "datasets": len(snapshot.Datasets),
			"dimensions": len(snapshot.Dimensions), "verifiedCompatibilities": len(snapshot.Compatibilities),
			"dimensionValues": len(snapshot.Members), "queryRoles": len(snapshot.RoleCodes),
		},
		Issues: []BootstrapSemanticReleaseIssue{},
	}
	blockers := 0
	issue := func(severity, code, objectType, objectID, message string) {
		if severity == "BLOCKER" {
			blockers++
		}
		preview.Issues = append(preview.Issues, BootstrapSemanticReleaseIssue{
			Code: code, Severity: severity, ObjectType: objectType,
			ObjectID: objectID, Message: message,
		})
	}
	if len(snapshot.Metrics) == 0 {
		issue("BLOCKER", "NO_PUBLISHED_METRIC", "METRIC", "", "没有可迁移的当前已发布指标")
	}
	if len(snapshot.RoleCodes) == 0 {
		issue("BLOCKER", "NO_QUERY_ROLE", "POLICY", "", "没有同时拥有 METRIC.READ 与 DATASET.READ 的活动角色")
	}

	datasets := map[string]legacyDataset{}
	for _, item := range snapshot.Datasets {
		datasets[item.ID] = item
	}
	dimensions := map[string]legacyDimension{}
	for _, item := range snapshot.Dimensions {
		dimensions[item.ID] = item
	}
	compatible := map[string][]legacyCompatibility{}
	for _, item := range snapshot.Compatibilities {
		compatible[item.MetricVersionID] = append(compatible[item.MetricVersionID], item)
	}

	objects := []SemanticReleaseObjectInput{}
	addedDatasets, addedDimensions, addedTimes := map[string]bool{}, map[string]bool{}, map[string]bool{}
	metricIDs := map[string]bool{}
	validFrom := time.Now().UTC()
	add := func(objectType, objectID, objectVersion, domainID, ownerID, sensitivity string, from time.Time, to *time.Time, contract map[string]any) {
		encoded, err := json.Marshal(contract)
		if err != nil {
			issue("BLOCKER", "CONTRACT_ENCODING_FAILED", objectType, objectID, "无法编码迁移合同")
			return
		}
		objects = append(objects, SemanticReleaseObjectInput{
			ObjectType: objectType, ObjectID: objectID, ObjectVersion: objectVersion,
			DomainID: domainID, OwnerID: ownerID, Certification: "CERTIFIED",
			Sensitivity: sensitivity, ValidFrom: from.UTC(), ValidTo: to, Contract: encoded,
		})
	}
	addDataset := func(dataset legacyDataset) bool {
		if addedDatasets[dataset.ID] {
			return true
		}
		grain := dataset.DSL["outputGrain"]
		if !semanticContractFieldPresent(grain) || dataset.PhysicalSchema == "" || dataset.PhysicalName == "" {
			issue("BLOCKER", "DATASET_EXECUTION_CONTRACT_INCOMPLETE", "DATASET", dataset.ID,
				"当前发布数据集缺少输出粒度或活动物化，不能迁移为可执行合同")
			return false
		}
		add("DATASET", dataset.ID, dataset.VersionID, dataset.DomainID, dataset.OwnerID, "INTERNAL", dataset.PublishedAt, nil, map[string]any{
			"title": dataset.Name, "code": dataset.Code, "description": dataset.Description,
			"aliases": compactAliases(dataset.Name, dataset.Code), "grain": grain,
			"source":          map[string]any{"adapter": "GO_NATIVE_DATASET_V1", "schema": dataset.PhysicalSchema, "relation": dataset.PhysicalName},
			"freshness":       map[string]any{"requireActiveMaterialization": true},
			"nativeDatasetId": dataset.ID, "nativeDatasetVersionId": dataset.VersionID,
		})
		addedDatasets[dataset.ID] = true
		return true
	}
	addDimension := func(dimension legacyDimension) bool {
		if addedDimensions[dimension.ID] {
			return true
		}
		dataset, exists := datasets[dimension.DatasetID]
		if !exists || dataset.VersionID != dimension.DatasetVersionID || !addDataset(dataset) {
			issue("WARNING", "DIMENSION_SOURCE_NOT_CURRENT", "DIMENSION", dimension.ID,
				"维度没有固定到候选包中的当前发布数据集版本")
			return false
		}
		sensitivity := "INTERNAL"
		if dimension.Sensitive {
			sensitivity = "RESTRICTED"
		}
		add("DIMENSION", dimension.ID, dimension.DefinitionHash, dimension.DomainID, dimension.OwnerID, sensitivity, dimension.UpdatedAt, nil, map[string]any{
			"title": dimension.Name, "code": dimension.Code, "description": dimension.Description,
			"aliases": compactAliases(dimension.Name, dimension.Code), "valueKey": dimension.FieldID,
			"usages": []string{"FILTER", "GROUP_BY"}, "dimensionType": dimension.DimensionType,
			"sourceDatasetIds": []string{dimension.DatasetID}, "nativeDimensionId": dimension.ID,
			"nativeDimensionVersionId": dimension.DefinitionHash,
		})
		addedDimensions[dimension.ID] = true
		return true
	}
	addTime := func(metric legacyMetric, dataset legacyDataset) string {
		fieldID := stringMapValue(metric.Definition, "timeFieldId")
		timeID := "legacy_time__" + dataset.VersionID + "__" + fieldID
		title, completePolicy := "无时间维度", "NOT_APPLICABLE"
		if fieldID == "" {
			timeID = "legacy_time_none__" + dataset.VersionID
		} else {
			title = datasetFieldTitle(dataset.DSL, fieldID)
			completePolicy = strings.ToUpper(strings.TrimSpace(input.CompletePeriodPolicy))
		}
		if !addedTimes[timeID] {
			add("TIME", timeID, dataset.VersionID, metric.DomainID, metric.OwnerID, "INTERNAL", dataset.PublishedAt, nil, map[string]any{
				"title": title, "code": fieldID, "fieldId": fieldID,
				"timezone":             strings.TrimSpace(input.DefaultTimezone),
				"calendar":             strings.ToUpper(strings.TrimSpace(input.DefaultCalendar)),
				"completePeriodPolicy": completePolicy,
				"sourceDatasetIds":     []string{dataset.ID},
				"nativeDatasetId":      dataset.ID, "nativeDatasetVersionId": dataset.VersionID,
			})
			addedTimes[timeID] = true
		}
		return timeID
	}

	for _, metric := range snapshot.Metrics {
		dataset, exists := datasets[metric.DatasetID]
		if !exists || dataset.VersionID != metric.DatasetVersionID {
			issue("WARNING", "METRIC_SOURCE_NOT_CURRENT", "METRIC", metric.ID,
				"指标没有固定到候选包中的当前已发布且已物化数据集版本")
			continue
		}
		if !addDataset(dataset) {
			continue
		}
		formula := metric.Definition["expression"]
		if sourceCalculation, ok := metric.Definition["sourceCalculation"].(map[string]any); ok && semanticContractFieldPresent(sourceCalculation["formula"]) {
			formula = sourceCalculation["formula"]
		}
		grain := dataset.DSL["outputGrain"]
		if !semanticContractFieldPresent(formula) || !semanticContractFieldPresent(grain) {
			issue("WARNING", "METRIC_EXECUTION_CONTRACT_INCOMPLETE", "METRIC", metric.ID,
				"指标缺少已发布公式或输出粒度")
			continue
		}
		groupable := []string{}
		for _, relation := range compatible[metric.VersionID] {
			dimension, exists := dimensions[relation.DimensionID]
			if !exists || !addDimension(dimension) {
				continue
			}
			groupable = append(groupable, dimension.ID)
		}
		groupable = sortedUnique(groupable)
		timeID := addTime(metric, dataset)
		qualityID := "legacy_quality__" + metric.VersionID
		add("QUALITY_RULE", qualityID, metric.VersionID, metric.DomainID, metric.OwnerID, "INTERNAL", metric.PublishedAt, nil, map[string]any{
			"title": metric.Name + "执行前置校验", "targetId": metric.ID, "severity": "ERROR",
			"validator": map[string]any{"type": "NATIVE_RELEASE_AND_MATERIALIZATION", "definitionVersionId": metric.VersionID, "datasetVersionId": metric.DatasetVersionID},
		})
		metricContract := map[string]any{
			"title": metric.Name, "code": metric.Code, "description": metric.Description,
			"aliases": compactAliases(metric.Name, metric.Code), "formula": formula, "grain": grain,
			"defaultTimeDimensionId": timeID, "sourceDatasetIds": []string{metric.DatasetID},
			"permissionPolicyIds": []string{"legacy_query_read_policy"},
			"qualityRuleIds":      []string{qualityID}, "nativeMetricId": metric.ID,
			"nativeMetricVersionId": metric.VersionID, "nativeDatasetVersionId": metric.DatasetVersionID,
			"metricType": metric.MetricType,
		}
		if len(groupable) > 0 {
			metricContract["groupableDimensionIds"] = groupable
		}
		add("METRIC", metric.ID, metric.VersionID, metric.DomainID, metric.OwnerID, "INTERNAL", metric.PublishedAt, nil, metricContract)
		metricIDs[metric.ID] = true
		add("RELATION", "rel:metric-source:"+metric.VersionID, metric.VersionID, metric.DomainID, metric.OwnerID, "INTERNAL", metric.PublishedAt, nil, map[string]any{
			"title": metric.Name + "来源数据集", "relationType": "sourced_from",
			"fromId": metric.ID, "fromType": "METRIC", "toId": dataset.ID, "toType": "DATASET",
			"cardinality": "MANY_TO_ONE", "certified": true, "fanoutPolicy": "SAFE", "allowedForQuery": true,
		})
		for _, relation := range compatible[metric.VersionID] {
			if !addedDimensions[relation.DimensionID] {
				continue
			}
			cardinality := relation.Cardinality
			if cardinality == "" {
				cardinality = "MANY_TO_ONE"
			}
			fanout := relation.FanoutPolicy
			if fanout == "" {
				fanout = "SAFE"
			}
			add("RELATION", "rel:metric-dimension:"+metric.VersionID+":"+relation.DimensionID,
				metric.VersionID, metric.DomainID, metric.OwnerID, "INTERNAL", metric.PublishedAt, nil, map[string]any{
					"title": metric.Name + "可分组维度", "relationType": "groupable_by",
					"fromId": metric.ID, "fromType": "METRIC", "toId": relation.DimensionID, "toType": "DIMENSION",
					"cardinality": cardinality, "certified": true, "fanoutPolicy": fanout, "allowedForQuery": true,
				})
		}
	}

	for _, member := range snapshot.Members {
		dimension, exists := dimensions[member.DimensionID]
		if !exists || !addedDimensions[dimension.ID] || dimension.Sensitive ||
			dimension.MemberIndexPolicy == "NONE" {
			continue
		}
		from := dimension.UpdatedAt
		if member.ValidFrom != nil {
			from = member.ValidFrom.UTC()
		}
		aliases := compactAliases(append([]string{member.CanonicalLabel, member.MemberKey}, member.Aliases...)...)
		add("DIMENSION_VALUE", member.ID, dimension.DefinitionHash, dimension.DomainID, dimension.OwnerID, "INTERNAL", from, member.ValidTo, map[string]any{
			"title": member.CanonicalLabel, "dimensionId": dimension.ID,
			"canonicalCode": member.MemberKey, "aliases": aliases,
			"nativeDimensionValueId": member.ID,
		})
	}

	if len(metricIDs) > 0 && len(snapshot.RoleCodes) > 0 {
		accessibleMetricIDs := make([]string, 0, len(metricIDs))
		for metricID := range metricIDs {
			accessibleMetricIDs = append(accessibleMetricIDs, metricID)
		}
		add("POLICY", "legacy_query_read_policy", "v1", "", actorID, "INTERNAL", validFrom, nil, map[string]any{
			"title": "现有只读分析权限迁移策略", "roles": sortedUnique(snapshot.RoleCodes),
			"purpose": []string{"analytics"}, "effect": "ALLOW",
			"accessibleObjectIds": sortedUnique(accessibleMetricIDs),
		})
	}

	sort.Slice(objects, func(left, right int) bool {
		if objects[left].ObjectType != objects[right].ObjectType {
			return objects[left].ObjectType < objects[right].ObjectType
		}
		return objects[left].ObjectID < objects[right].ObjectID
	})
	candidate := CreateSemanticReleaseInput{
		SemanticVersion: strings.TrimSpace(input.SemanticVersion),
		Notes:           "由当前已发布原生资产显式迁移；来源版本保持不可变。 " + strings.TrimSpace(input.Notes),
		Objects:         objects,
	}
	preview.CandidateCount = len(objects)
	if blockers == 0 {
		normalizedObjects := make([]SemanticReleaseObject, 0, len(objects))
		for _, object := range objects {
			normalized, err := normalizeSemanticReleaseObject(object)
			if err != nil {
				issue("BLOCKER", "GENERATED_OBJECT_INVALID", object.ObjectType, object.ObjectID,
					"由原生资产生成的对象不满足发布输入边界")
				continue
			}
			normalizedObjects = append(normalizedObjects, normalized)
		}
		if blockers == 0 {
			validation := validateSemanticReleaseObjects(normalizedObjects)
			for _, validationIssue := range validation.Issues {
				issue("BLOCKER", validationIssue.Code, validationIssue.ObjectType,
					validationIssue.ObjectID, validationIssue.Message)
			}
		}
		if blockers == 0 {
			if _, err := normalizeSemanticRelease(candidate); err != nil {
				issue("BLOCKER", "GENERATED_RELEASE_INVALID", "RELEASE", "", err.Error())
			}
		}
	}
	preview.Eligible = blockers == 0
	if preview.Eligible {
		preview.Candidate = &candidate
	}
	return preview
}

func compactAliases(values ...string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := semanticAliasKey(value)
		if value != "" && key != "" && !seen[key] {
			seen[key] = true
			result = append(result, value)
		}
	}
	return result
}

func sortedUnique(values []string) []string {
	values = compactAliases(values...)
	sort.Strings(values)
	return values
}

func stringMapValue(value map[string]any, field string) string {
	result, _ := value[field].(string)
	return strings.TrimSpace(result)
}

func datasetFieldTitle(dsl map[string]any, fieldID string) string {
	fields, _ := dsl["fields"].([]any)
	for _, raw := range fields {
		field, _ := raw.(map[string]any)
		if stringMapValue(field, "id") == fieldID {
			if title := stringMapValue(field, "name"); title != "" {
				return title
			}
			if code := stringMapValue(field, "code"); code != "" {
				return code
			}
		}
	}
	return fieldID
}
