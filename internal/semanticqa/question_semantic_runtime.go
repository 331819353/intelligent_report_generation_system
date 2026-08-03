package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/semanticgraph"
)

type QuestionSemanticObject struct {
	ObjectType    string         `json:"objectType"`
	ObjectID      string         `json:"objectId"`
	ObjectVersion string         `json:"objectVersion"`
	DomainID      string         `json:"domainId,omitempty"`
	ContentHash   string         `json:"contentHash"`
	Contract      map[string]any `json:"contract"`
}

type QuestionSemanticSnapshot struct {
	TenantID        string                   `json:"tenantId"`
	ReleaseID       string                   `json:"releaseId"`
	SemanticVersion string                   `json:"semanticVersion"`
	ContentHash     string                   `json:"contentHash"`
	RoleCodes       []string                 `json:"roleCodes"`
	Purpose         string                   `json:"purpose"`
	EffectiveAt     time.Time                `json:"effectiveAt"`
	Objects         []QuestionSemanticObject `json:"objects"`
}

type QuestionGraphPlan struct {
	ID                string                       `json:"id"`
	SemanticVersion   string                       `json:"semanticVersion"`
	ContentHash       string                       `json:"contentHash"`
	NormalizerVersion string                       `json:"normalizerVersion"`
	BindingBundleHash string                       `json:"bindingBundleHash"`
	MetricVIDs        []string                     `json:"metricVids"`
	DimensionVIDs     []string                     `json:"dimensionVids"`
	TimeDimensionVIDs []string                     `json:"timeDimensionVids"`
	ValueBindings     []semanticgraph.ValueBinding `json:"valueBindings"`
	DatasetVIDs       []string                     `json:"datasetVids"`
	RoleVIDs          []string                     `json:"roleVids"`
	AuthorizedVIDs    []string                     `json:"authorizedVids"`
	JoinPaths         []semanticgraph.JoinPath     `json:"joinPaths"`
	EvidenceIDs       []string                     `json:"evidenceIds"`
	MaximumHops       int                          `json:"maximumHops"`
	FanoutRisk        string                       `json:"fanoutRisk"`
}

type questionSemanticScopeStore interface {
	LoadQuestionSemanticSnapshot(context.Context, string, string, time.Time) (QuestionSemanticSnapshot, error)
}

type questionSemanticCurrentStore interface {
	IsQuestionSemanticSnapshotCurrent(context.Context, string, string, string, string) (bool, error)
}

type questionSemanticFailure struct {
	Code    string
	Message string
	Clarify *QueryClarification
}

type questionArtifact struct {
	Type    string
	Hash    string
	Payload json.RawMessage
}

func (failure *questionSemanticFailure) Error() string {
	if failure == nil {
		return ""
	}
	return failure.Code + ": " + failure.Message
}

func (object QuestionSemanticObject) Code() string {
	if code := firstSemanticString(object.Contract, "code", "metricCode", "dimensionCode", "canonicalCode"); code != "" {
		return code
	}
	return object.ObjectID
}

func (object QuestionSemanticObject) Label() string {
	if label := firstSemanticString(object.Contract, "title", "name"); label != "" {
		return label
	}
	return object.Code()
}

func (object QuestionSemanticObject) Aliases() []string {
	items := []string{object.ObjectID, object.Code(), object.Label()}
	for _, field := range []string{"aliases", "synonyms", "abbreviations", "shortNames", "positiveAliases"} {
		items = append(items, semanticStringSlice(object.Contract[field])...)
	}
	if canonical := semanticString(object.Contract["canonicalCode"]); canonical != "" {
		items = append(items, canonical)
	}
	return uniqueStrings(items, 128)
}

func (object QuestionSemanticObject) VID(tenantID string) string {
	stableObjectID := object.ObjectID
	if object.ObjectType == "DIMENSION_VALUE" {
		stableObjectID = semanticString(object.Contract["dimensionId"]) + "::" + object.ObjectID
	}
	return semanticgraph.StableVID(
		tenantID, semanticGraphTag(object.ObjectType), stableObjectID, object.ObjectVersion,
	)
}

func semanticGraphTag(objectType string) string {
	switch objectType {
	case "DOMAIN":
		return "domain"
	case "ENTITY":
		return "entity"
	case "METRIC", "MEASURE":
		return "metric"
	case "DIMENSION", "TIME", "COHORT":
		return "dimension"
	case "DIMENSION_VALUE":
		return "dimension_value"
	case "SEMANTIC_MODEL", "DATASET":
		return "dataset"
	case "TABLE_COLUMN":
		return "table_column"
	case "BUSINESS_TERM", "PARSING_RULE":
		return "business_term"
	case "CERTIFIED_EXAMPLE":
		return "certified_example"
	case "POLICY":
		return "role"
	case "QUALITY_RULE":
		return "quality_rule"
	default:
		return ""
	}
}

func validateQuestionSemanticGraph(
	ctx context.Context,
	graph semanticgraph.Graph,
	snapshot QuestionSemanticSnapshot,
	understanding QuestionUnderstanding,
	plans []QueryPlan,
) (QuestionGraphPlan, error) {
	if graph == nil || snapshot.TenantID == "" || snapshot.ReleaseID == "" ||
		snapshot.SemanticVersion == "" || !validHash(snapshot.ContentHash) ||
		len(plans) == 0 || snapshot.EffectiveAt.IsZero() {
		return QuestionGraphPlan{}, semanticFailure(
			"ACTIVE_SEMANTIC_RELEASE_REQUIRED",
			"当前租户没有可用于问答的完整活动语义发布版本。",
		)
	}
	scope := semanticgraph.Scope{
		TenantID: snapshot.TenantID, SemanticVersion: snapshot.SemanticVersion,
		ContentHash: snapshot.ContentHash, Purpose: snapshot.Purpose,
		EffectiveAt: snapshot.EffectiveAt,
	}
	metricObjects, dimensionObjects := []QuestionSemanticObject{}, []QuestionSemanticObject{}
	valueObjects, datasetObjects := []QuestionSemanticObject{}, []QuestionSemanticObject{}
	metricVIDs, dimensionVIDs, timeDimensionVIDs := []string{}, []string{}, []string{}
	valueBindings, datasetVIDs := []semanticgraph.ValueBinding{}, []string{}
	metricDatasets := map[string][]QuestionSemanticObject{}
	dimensionDatasets := map[string][]QuestionSemanticObject{}

	for _, plan := range plans {
		metric, err := uniqueQuestionSemanticObject(snapshot, []string{"METRIC", "MEASURE"},
			plan.Conditions.MetricCode, plan.SelectedMetricID, plan.SelectedMetricVersionID)
		if err != nil {
			return QuestionGraphPlan{}, err
		}
		metricObjects = appendQuestionSemanticObject(metricObjects, metric)
		metricVIDs = append(metricVIDs, metric.VID(snapshot.TenantID))
		timeDimensionID := semanticString(metric.Contract["defaultTimeDimensionId"])
		if timeDimensionID == "" {
			return QuestionGraphPlan{}, semanticFailure(
				"TIME_DIMENSION_NOT_CERTIFIED", "指标没有固定活动语义版本中的默认时间维度。",
			)
		}
		timeDimension, timeErr := uniqueQuestionSemanticObject(
			snapshot, []string{"TIME"}, timeDimensionID, "", "",
		)
		if timeErr != nil {
			return QuestionGraphPlan{}, semanticFailure(
				"TIME_DIMENSION_NOT_CERTIFIED", "指标默认时间维度无法唯一映射到认证时间合同。",
			)
		}
		dimensionObjects = appendQuestionSemanticObject(dimensionObjects, timeDimension)
		timeDimensionVIDs = append(timeDimensionVIDs, timeDimension.VID(snapshot.TenantID))
		dimensionVIDs = append(dimensionVIDs, timeDimension.VID(snapshot.TenantID))
		sources, err := resolveQuestionDatasets(snapshot, metric, plan.SelectedDatasetVersionID)
		if err != nil {
			return QuestionGraphPlan{}, err
		}
		metricDatasets[metric.ObjectID] = sources
		for _, source := range sources {
			datasetObjects = appendQuestionSemanticObject(datasetObjects, source)
			datasetVIDs = append(datasetVIDs, source.VID(snapshot.TenantID))
		}
		for _, clause := range plan.Conditions.Dimensions {
			dimension, err := uniqueQuestionSemanticObject(snapshot,
				[]string{"DIMENSION", "TIME", "COHORT"}, clause.DimensionCode, clause.DimensionID, "")
			if err != nil {
				return QuestionGraphPlan{}, err
			}
			dimensionObjects = appendQuestionSemanticObject(dimensionObjects, dimension)
			dimensionVIDs = append(dimensionVIDs, dimension.VID(snapshot.TenantID))
			if sources, resolveErr := resolveOptionalQuestionDatasets(snapshot, dimension); resolveErr != nil {
				return QuestionGraphPlan{}, resolveErr
			} else if len(sources) > 0 {
				dimensionDatasets[dimension.ObjectID] = sources
				for _, source := range sources {
					datasetObjects = appendQuestionSemanticObject(datasetObjects, source)
					datasetVIDs = append(datasetVIDs, source.VID(snapshot.TenantID))
				}
			}
			memberKeys := append([]string(nil), clause.MemberKeys...)
			if clause.MemberKey != "" {
				memberKeys = append(memberKeys, clause.MemberKey)
			}
			for _, memberKey := range uniqueStrings(memberKeys, 256) {
				value, valueErr := uniqueQuestionSemanticValue(snapshot, dimension, memberKey)
				if valueErr != nil {
					return QuestionGraphPlan{}, valueErr
				}
				valueObjects = appendQuestionSemanticObject(valueObjects, value)
				valueBindings = append(valueBindings, semanticgraph.ValueBinding{
					DimensionVID: dimension.VID(snapshot.TenantID),
					ValueVID:     value.VID(snapshot.TenantID),
				})
			}
		}
	}
	metricVIDs, dimensionVIDs = uniqueStrings(metricVIDs, 32), uniqueStrings(dimensionVIDs, 64)
	timeDimensionVIDs = uniqueStrings(timeDimensionVIDs, 16)
	datasetVIDs = uniqueStrings(datasetVIDs, 64)
	valueBindings = uniqueQuestionValueBindings(valueBindings)

	roleObjects := applicableQuestionPolicies(snapshot)
	if len(roleObjects) == 0 {
		return QuestionGraphPlan{}, semanticFailure(
			"POLICY_DENIED", "活动语义版本没有与当前用户角色和用途匹配的允许策略。",
		)
	}
	roleVIDs := make([]string, 0, len(roleObjects))
	for _, object := range roleObjects {
		roleVIDs = append(roleVIDs, object.VID(snapshot.TenantID))
	}
	scope.RoleIDs = roleVIDs

	bundle := semanticgraph.Bundle{MetricVIDs: metricVIDs, DimensionVIDs: dimensionVIDs, Values: valueBindings}
	bundleValidation, bundleEvidence, err := graph.ValidateBundle(ctx, scope, bundle)
	if err != nil || !bundleValidation.Valid {
		if err == nil {
			err = semanticgraph.ErrNoCertifiedPath
		}
		return QuestionGraphPlan{}, graphSemanticFailure("SEMANTIC_BUNDLE_REJECTED", err)
	}
	evidenceIDs := questionMentionEvidenceIDs(understanding)
	evidenceIDs = append(evidenceIDs, bundleEvidence.EvidenceID)

	reachable := map[string]bool{}
	for _, metricVID := range metricVIDs {
		candidates, evidence, expandErr := graph.ExpandCandidates(ctx, scope, []string{metricVID})
		if expandErr != nil {
			return QuestionGraphPlan{}, graphSemanticFailure("GRAPH_CANDIDATE_EXTENSION_FAILED", expandErr)
		}
		evidenceIDs = append(evidenceIDs, evidence.EvidenceID)
		for _, candidate := range candidates {
			reachable[candidate.VID] = true
		}
	}
	for _, vid := range append(append([]string{}, dimensionVIDs...), datasetVIDs...) {
		if !reachable[vid] {
			return QuestionGraphPlan{}, semanticFailure(
				"NO_CERTIFIED_GRAPH_PATH", "已绑定对象不在指标的认证关系闭包内。",
			)
		}
	}

	authorizationCandidates := append(append(append([]string{}, metricVIDs...), dimensionVIDs...), datasetVIDs...)
	for _, binding := range valueBindings {
		authorizationCandidates = append(authorizationCandidates, binding.ValueVID)
	}
	authorizationCandidates = uniqueStrings(authorizationCandidates, 100)
	authorized, authorizationEvidence, err := graph.FilterAuthorized(ctx, scope, semanticgraph.AuthorizationRequest{
		RoleVIDs: roleVIDs, CandidateVIDs: authorizationCandidates,
	})
	if err != nil {
		return QuestionGraphPlan{}, graphSemanticFailure("POLICY_GRAPH_UNAVAILABLE", err)
	}
	if !sameStringSet(authorized, authorizationCandidates) {
		return QuestionGraphPlan{}, semanticFailure(
			"POLICY_DENIED", "至少一个指标、维度、值或数据集未通过语义图权限传播。",
		)
	}
	evidenceIDs = append(evidenceIDs, authorizationEvidence.EvidenceID)

	joinPaths := []semanticgraph.JoinPath{}
	for _, metric := range metricObjects {
		facts := metricDatasets[metric.ObjectID]
		for _, dimension := range dimensionObjects {
			for _, target := range dimensionDatasets[dimension.ObjectID] {
				if containsQuestionSemanticObject(facts, target.ObjectID) {
					continue
				}
				if len(facts) != 1 {
					return QuestionGraphPlan{}, semanticFailure(
						"SOURCE_DATASET_AMBIGUOUS", "指标包含多个事实来源，当前查询无法唯一确定认证 Join 起点。",
					)
				}
				paths, evidence, pathErr := graph.FindJoinPaths(ctx, scope, semanticgraph.JoinPathRequest{
					FactDatasetVID:      facts[0].VID(snapshot.TenantID),
					DimensionDatasetVID: target.VID(snapshot.TenantID), MaxHops: 4, Limit: 20,
				})
				if pathErr != nil || len(paths) == 0 {
					return QuestionGraphPlan{}, graphSemanticFailure("NO_CERTIFIED_GRAPH_PATH", pathErr)
				}
				joinPaths = append(joinPaths, paths[0])
				evidenceIDs = append(evidenceIDs, evidence.EvidenceID)
			}
		}
	}

	bundleHash, err := hashJSON(struct {
		Metrics    []string                     `json:"metrics"`
		Dimensions []string                     `json:"dimensions"`
		Values     []semanticgraph.ValueBinding `json:"values"`
	}{metricVIDs, dimensionVIDs, valueBindings})
	if err != nil {
		return QuestionGraphPlan{}, err
	}
	evidenceIDs = uniqueStrings(evidenceIDs, 256)
	plan := QuestionGraphPlan{
		SemanticVersion: snapshot.SemanticVersion, ContentHash: snapshot.ContentHash,
		NormalizerVersion: understanding.NormalizerVersion,
		BindingBundleHash: bundleHash, MetricVIDs: metricVIDs,
		DimensionVIDs: dimensionVIDs, TimeDimensionVIDs: timeDimensionVIDs,
		ValueBindings: valueBindings,
		DatasetVIDs:   datasetVIDs, RoleVIDs: uniqueStrings(roleVIDs, 32),
		AuthorizedVIDs: authorized, JoinPaths: joinPaths, EvidenceIDs: evidenceIDs,
		MaximumHops: 4, FanoutRisk: "NONE",
	}
	planHash, err := hashJSON(plan)
	if err != nil {
		return QuestionGraphPlan{}, err
	}
	plan.ID = "graphplan:" + planHash
	return plan, nil
}

func uniqueQuestionSemanticObject(
	snapshot QuestionSemanticSnapshot,
	types []string,
	code, nativeID, nativeVersionID string,
) (QuestionSemanticObject, error) {
	candidates := []QuestionSemanticObject{}
	for _, object := range snapshot.Objects {
		if !containsString(types, object.ObjectType) ||
			(!strings.EqualFold(object.ObjectID, code) && !strings.EqualFold(object.Code(), code)) {
			continue
		}
		if expected := semanticString(object.Contract["nativeMetricId"]); expected != "" && expected != nativeID {
			continue
		}
		if expected := semanticString(object.Contract["nativeMetricVersionId"]); expected != "" && expected != nativeVersionID {
			continue
		}
		candidates = append(candidates, object)
	}
	if len(candidates) != 1 {
		codeName := "SEMANTIC_OBJECT_NOT_CERTIFIED"
		message := "旧查询候选无法唯一映射到活动语义发布对象。"
		if len(candidates) > 1 {
			codeName, message = "SEMANTIC_OBJECT_AMBIGUOUS", "活动语义发布中存在会改变结果的同码对象。"
		}
		return QuestionSemanticObject{}, semanticFailure(codeName, message)
	}
	return candidates[0], nil
}

func resolveQuestionDatasets(
	snapshot QuestionSemanticSnapshot,
	owner QuestionSemanticObject,
	selectedNativeVersionID string,
) ([]QuestionSemanticObject, error) {
	items, err := resolveOptionalQuestionDatasets(snapshot, owner)
	if err != nil || len(items) == 0 {
		if err == nil {
			err = semanticFailure("SOURCE_DATASET_NOT_CERTIFIED", "指标没有认证来源数据集。")
		}
		return nil, err
	}
	if len(items) == 1 {
		return items, nil
	}
	selected := []QuestionSemanticObject{}
	for _, item := range items {
		if semanticString(item.Contract["nativeDatasetVersionId"]) == selectedNativeVersionID {
			selected = append(selected, item)
		}
	}
	if len(selected) != 1 {
		return nil, semanticFailure(
			"SOURCE_DATASET_AMBIGUOUS", "指标有多个认证来源，但没有与执行计划唯一绑定的数据集版本。",
		)
	}
	return selected, nil
}

func resolveOptionalQuestionDatasets(
	snapshot QuestionSemanticSnapshot,
	owner QuestionSemanticObject,
) ([]QuestionSemanticObject, error) {
	ids := semanticStringSlice(owner.Contract["sourceDatasetIds"])
	result := []QuestionSemanticObject{}
	for _, id := range ids {
		matches := []QuestionSemanticObject{}
		for _, object := range snapshot.Objects {
			if (object.ObjectType == "DATASET" || object.ObjectType == "SEMANTIC_MODEL") &&
				(strings.EqualFold(object.ObjectID, id) || strings.EqualFold(object.Code(), id)) {
				matches = append(matches, object)
			}
		}
		if len(matches) != 1 {
			return nil, semanticFailure(
				"SOURCE_DATASET_NOT_CERTIFIED", "语义对象引用的来源数据集不存在或不唯一。",
			)
		}
		result = appendQuestionSemanticObject(result, matches[0])
	}
	return result, nil
}

func uniqueQuestionSemanticValue(
	snapshot QuestionSemanticSnapshot,
	dimension QuestionSemanticObject,
	memberKey string,
) (QuestionSemanticObject, error) {
	candidates := []QuestionSemanticObject{}
	for _, object := range snapshot.Objects {
		if object.ObjectType != "DIMENSION_VALUE" ||
			!strings.EqualFold(semanticString(object.Contract["dimensionId"]), dimension.ObjectID) {
			continue
		}
		for _, alias := range object.Aliases() {
			if strings.EqualFold(alias, memberKey) {
				candidates = append(candidates, object)
				break
			}
		}
	}
	if len(candidates) != 1 {
		return QuestionSemanticObject{}, semanticFailure(
			"DIMENSION_VALUE_NOT_CERTIFIED", "筛选值无法唯一映射到当前维度的认证复合身份。",
		)
	}
	return candidates[0], nil
}

func applicableQuestionPolicies(snapshot QuestionSemanticSnapshot) []QuestionSemanticObject {
	roles := map[string]bool{}
	for _, role := range snapshot.RoleCodes {
		roles[strings.ToLower(strings.TrimSpace(role))] = true
	}
	result := []QuestionSemanticObject{}
	for _, object := range snapshot.Objects {
		if object.ObjectType != "POLICY" || !strings.EqualFold(semanticString(object.Contract["effect"]), "ALLOW") {
			continue
		}
		purposes := semanticStringSlice(object.Contract["purpose"])
		purposeMatched := len(purposes) == 0
		for _, purpose := range purposes {
			if purpose == "*" || strings.EqualFold(purpose, snapshot.Purpose) {
				purposeMatched = true
				break
			}
		}
		if !purposeMatched {
			continue
		}
		matched := false
		for _, role := range semanticStringSlice(object.Contract["roles"]) {
			if role == "*" || roles[strings.ToLower(role)] {
				matched = true
				break
			}
		}
		if matched {
			result = append(result, object)
		}
	}
	return result
}

func semanticFailure(code, message string) error {
	return &questionSemanticFailure{Code: code, Message: message}
}

func graphSemanticFailure(code string, cause error) error {
	message := "NebulaGraph 无法提供当前版本的认证关系证据。"
	if errors.Is(cause, semanticgraph.ErrNoCertifiedPath) {
		message = "当前绑定不存在可执行的认证语义图闭包。"
	}
	if cause == nil {
		cause = semanticgraph.ErrNoCertifiedPath
	}
	return fmt.Errorf("%w: %v", &questionSemanticFailure{Code: code, Message: message}, cause)
}

func firstSemanticString(contract map[string]any, fields ...string) string {
	for _, field := range fields {
		if value := semanticString(contract[field]); value != "" {
			return value
		}
	}
	return ""
}

func semanticString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func semanticStringSlice(value any) []string {
	result := []string{}
	switch items := value.(type) {
	case []any:
		for _, item := range items {
			if text := semanticString(item); text != "" {
				result = append(result, text)
			}
		}
	case []string:
		result = append(result, items...)
	case string:
		if strings.TrimSpace(items) != "" {
			result = append(result, strings.TrimSpace(items))
		}
	}
	return uniqueStrings(result, 256)
}

func appendQuestionSemanticObject(
	items []QuestionSemanticObject,
	object QuestionSemanticObject,
) []QuestionSemanticObject {
	for _, item := range items {
		if item.ObjectType == object.ObjectType && item.ObjectID == object.ObjectID &&
			item.ObjectVersion == object.ObjectVersion {
			return items
		}
	}
	return append(items, object)
}

func containsQuestionSemanticObject(items []QuestionSemanticObject, objectID string) bool {
	for _, item := range items {
		if item.ObjectID == objectID {
			return true
		}
	}
	return false
}

func uniqueQuestionValueBindings(items []semanticgraph.ValueBinding) []semanticgraph.ValueBinding {
	result := []semanticgraph.ValueBinding{}
	seen := map[string]bool{}
	for _, item := range items {
		key := item.DimensionVID + "\x00" + item.ValueVID
		if !seen[key] {
			seen[key] = true
			result = append(result, item)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].DimensionVID != result[right].DimensionVID {
			return result[left].DimensionVID < result[right].DimensionVID
		}
		return result[left].ValueVID < result[right].ValueVID
	})
	return result
}

func questionMentionEvidenceIDs(understanding QuestionUnderstanding) []string {
	result := []string{}
	for _, mention := range understanding.Mentions {
		if mention.EvidenceID != "" {
			result = append(result, mention.EvidenceID)
		}
	}
	for _, example := range understanding.CertifiedExamples {
		if example.EvidenceID != "" {
			result = append(result, example.EvidenceID)
		}
	}
	return uniqueStrings(result, 256)
}

func sameStringSet(left, right []string) bool {
	left, right = uniqueStrings(left, 256), uniqueStrings(right, 256)
	sort.Strings(left)
	sort.Strings(right)
	return len(left) == len(right) && strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func marshalQuestionArtifact(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func questionArtifacts(response QuestionResponse) []questionArtifact {
	artifacts := []questionArtifact{}
	appendArtifact := func(typeName string, value any) {
		payload := marshalQuestionArtifact(value)
		if len(payload) == 0 || string(payload) == "null" {
			return
		}
		hash, err := hashJSON(value)
		if err == nil && validHash(hash) {
			artifacts = append(artifacts, questionArtifact{
				Type: typeName, Hash: hash, Payload: payload,
			})
		}
	}
	if response.Understanding != nil {
		type safeMention struct {
			Type       string                     `json:"type"`
			StartByte  int                        `json:"startByte"`
			EndByte    int                        `json:"endByte"`
			Detector   string                     `json:"detector"`
			EvidenceID string                     `json:"evidenceId"`
			Candidates []QuestionMentionCandidate `json:"candidates"`
			Relation   string                     `json:"relation,omitempty"`
		}
		safe := struct {
			NormalizerVersion string                     `json:"normalizerVersion"`
			AlignmentMap      []QuestionAlignment        `json:"alignmentMap"`
			Mentions          []safeMention              `json:"mentions"`
			CertifiedExamples []QuestionCertifiedExample `json:"certifiedExamples"`
		}{
			NormalizerVersion: response.Understanding.NormalizerVersion,
			AlignmentMap:      response.Understanding.AlignmentMap, Mentions: []safeMention{},
			CertifiedExamples: response.Understanding.CertifiedExamples,
		}
		for _, mention := range response.Understanding.Mentions {
			safe.Mentions = append(safe.Mentions, safeMention{
				Type: mention.Type, StartByte: mention.StartByte, EndByte: mention.EndByte,
				Detector: mention.Detector, EvidenceID: mention.EvidenceID,
				Candidates: mention.Candidates, Relation: mention.Relation,
			})
		}
		appendArtifact("UNDERSTANDING", safe)
	}
	if response.GraphPlan != nil {
		appendArtifact("GRAPH_PLAN", response.GraphPlan)
	}
	if response.SemanticIR != nil {
		appendArtifact("SEMANTIC_IR", response.SemanticIR)
	}
	return artifacts
}

func questionSemanticFailureDetail(err error, fallbackCode string) (string, string) {
	failure := &questionSemanticFailure{}
	if errors.As(err, &failure) {
		return failure.Code, failure.Message
	}
	if errors.Is(err, ErrGraphNotReady) {
		return "SEMANTIC_GRAPH_NOT_READY", "活动语义发布尚未完成 NebulaGraph 投影，已拒绝降级执行。"
	}
	if fallbackCode == "" {
		fallbackCode = "SEMANTIC_VALIDATION_FAILED"
	}
	return fallbackCode, "当前问题无法通过活动语义发布和图关系校验。"
}

func mustQuestionHash(value any) string {
	hash, err := hashJSON(value)
	if err != nil {
		return ""
	}
	return hash
}

func questionSemanticReleaseID(snapshot *QuestionSemanticSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.ReleaseID
}

func questionSemanticVersion(snapshot *QuestionSemanticSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.SemanticVersion
}

func questionSemanticContentHash(snapshot *QuestionSemanticSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.ContentHash
}

func questionGraphPlanHash(plan *QuestionGraphPlan) string {
	if plan == nil {
		return ""
	}
	if value := strings.TrimPrefix(plan.ID, "graphplan:"); validHash(value) {
		return value
	}
	return mustQuestionHash(plan)
}

func questionBindingBundleHash(plan *QuestionGraphPlan, evidence AccuracyEvidence) string {
	if plan != nil && validHash(plan.BindingBundleHash) {
		return plan.BindingBundleHash
	}
	return mustQuestionHash(evidence.BindingEvidence)
}

func governedMentionClarification(
	understanding QuestionUnderstanding,
	confirmedMetricCodes []string,
) *QueryClarification {
	confirmed := map[string]bool{}
	for _, code := range confirmedMetricCodes {
		confirmed[strings.ToLower(strings.TrimSpace(code))] = true
	}
	candidatesByCode := map[string]QueryMetricCandidateTrace{}
	for _, mention := range understanding.Mentions {
		if mention.Type != "METRIC" || len(mention.Candidates) <= 1 {
			continue
		}
		confirmedCount := 0
		for _, candidate := range mention.Candidates {
			if confirmed[strings.ToLower(candidate.Code)] {
				confirmedCount++
			}
			candidatesByCode[candidate.Code] = QueryMetricCandidateTrace{
				Code: candidate.Code, Label: candidate.Label,
				MatchedTerm: mention.MentionText,
				MatchMethod: "GOVERNED_EXACT_ALIAS", Score: 1,
				Selected: confirmed[strings.ToLower(candidate.Code)],
				Source:   "SEMANTIC_RELEASE",
			}
		}
		if confirmedCount == 1 {
			continue
		}
		items := make([]QueryMetricCandidateTrace, 0, len(candidatesByCode))
		for _, candidate := range candidatesByCode {
			items = append(items, candidate)
		}
		sort.Slice(items, func(left, right int) bool { return items[left].Code < items[right].Code })
		return &QueryClarification{
			Type:             "METRIC",
			Message:          "该业务名称对应多个活动语义指标，请选择一个认证指标后继续。",
			MetricCandidates: items,
		}
	}
	return nil
}

func validateGovernedMetricAmbiguity(
	ctx context.Context,
	graph semanticgraph.Graph,
	snapshot QuestionSemanticSnapshot,
	understanding QuestionUnderstanding,
	plans []QueryPlan,
	confirmedMetricCodes []string,
) (*QueryClarification, error) {
	confirmed := map[string]bool{}
	for _, code := range confirmedMetricCodes {
		confirmed[strings.ToLower(strings.TrimSpace(code))] = true
	}
	for _, mention := range understanding.Mentions {
		if mention.Type != "METRIC" || len(mention.Candidates) <= 1 {
			continue
		}
		candidates := append([]QuestionMentionCandidate(nil), mention.Candidates...)
		confirmedCandidates := []QuestionMentionCandidate{}
		for _, candidate := range candidates {
			if confirmed[strings.ToLower(candidate.Code)] {
				confirmedCandidates = append(confirmedCandidates, candidate)
			}
		}
		if len(confirmedCandidates) == 1 {
			candidates = confirmedCandidates
		} else if len(confirmedCandidates) > 1 || len(candidates) > 3 {
			return metricClarificationForCandidates(mention, candidates, confirmed), nil
		}
		legal := []QuestionMentionCandidate{}
		for _, candidate := range candidates {
			valid, err := validateGovernedMetricCandidate(
				ctx, graph, snapshot, candidate, plans,
			)
			if err != nil {
				if errors.Is(err, semanticgraph.ErrNoCertifiedPath) {
					continue
				}
				return nil, graphSemanticFailure("GRAPH_BUNDLE_DISAMBIGUATION_FAILED", err)
			}
			if valid {
				legal = append(legal, candidate)
			}
		}
		if len(legal) == 0 {
			return nil, semanticFailure(
				"SEMANTIC_BUNDLE_REJECTED", "歧义指标候选均未形成有权限的认证执行闭包。",
			)
		}
		if len(legal) == 1 && questionPlansUseMetricCode(plans, legal[0].Code) {
			continue
		}
		return metricClarificationForCandidates(mention, legal, confirmed), nil
	}
	return nil, nil
}

func validateGovernedMetricCandidate(
	ctx context.Context,
	graph semanticgraph.Graph,
	snapshot QuestionSemanticSnapshot,
	candidate QuestionMentionCandidate,
	plans []QueryPlan,
) (bool, error) {
	var metric QuestionSemanticObject
	for _, object := range snapshot.Objects {
		if object.ObjectType == candidate.ObjectType && object.ObjectID == candidate.ObjectID &&
			object.ObjectVersion == candidate.ObjectVersion {
			metric = object
			break
		}
	}
	if metric.ObjectID == "" {
		return false, semanticgraph.ErrNoCertifiedPath
	}
	dimensionVIDs, datasetVIDs := []string{}, []string{}
	valueBindings := []semanticgraph.ValueBinding{}
	for _, plan := range plans {
		for _, clause := range plan.Conditions.Dimensions {
			dimension, err := uniqueQuestionSemanticObject(
				snapshot, []string{"DIMENSION", "TIME", "COHORT"},
				clause.DimensionCode, clause.DimensionID, "",
			)
			if err != nil {
				return false, semanticgraph.ErrNoCertifiedPath
			}
			dimensionVIDs = append(dimensionVIDs, dimension.VID(snapshot.TenantID))
			memberKeys := append([]string(nil), clause.MemberKeys...)
			if clause.MemberKey != "" {
				memberKeys = append(memberKeys, clause.MemberKey)
			}
			for _, memberKey := range uniqueStrings(memberKeys, 256) {
				value, valueErr := uniqueQuestionSemanticValue(snapshot, dimension, memberKey)
				if valueErr != nil {
					return false, semanticgraph.ErrNoCertifiedPath
				}
				valueBindings = append(valueBindings, semanticgraph.ValueBinding{
					DimensionVID: dimension.VID(snapshot.TenantID),
					ValueVID:     value.VID(snapshot.TenantID),
				})
			}
		}
	}
	timeID := semanticString(metric.Contract["defaultTimeDimensionId"])
	timeDimension, err := uniqueQuestionSemanticObject(snapshot, []string{"TIME"}, timeID, "", "")
	if err != nil {
		return false, semanticgraph.ErrNoCertifiedPath
	}
	dimensionVIDs = append(dimensionVIDs, timeDimension.VID(snapshot.TenantID))
	sources, err := resolveOptionalQuestionDatasets(snapshot, metric)
	if err != nil || len(sources) == 0 {
		return false, semanticgraph.ErrNoCertifiedPath
	}
	for _, source := range sources {
		datasetVIDs = append(datasetVIDs, source.VID(snapshot.TenantID))
	}
	dimensionVIDs = uniqueStrings(dimensionVIDs, 64)
	datasetVIDs = uniqueStrings(datasetVIDs, 64)
	valueBindings = uniqueQuestionValueBindings(valueBindings)
	roleObjects := applicableQuestionPolicies(snapshot)
	if len(roleObjects) == 0 {
		return false, semanticgraph.ErrNoCertifiedPath
	}
	roleVIDs := []string{}
	for _, role := range roleObjects {
		roleVIDs = append(roleVIDs, role.VID(snapshot.TenantID))
	}
	scope := semanticgraph.Scope{
		TenantID: snapshot.TenantID, SemanticVersion: snapshot.SemanticVersion,
		ContentHash: snapshot.ContentHash, RoleIDs: roleVIDs, Purpose: snapshot.Purpose,
		EffectiveAt: snapshot.EffectiveAt,
	}
	metricVID := metric.VID(snapshot.TenantID)
	validation, _, err := graph.ValidateBundle(ctx, scope, semanticgraph.Bundle{
		MetricVIDs: []string{metricVID}, DimensionVIDs: dimensionVIDs, Values: valueBindings,
	})
	if err != nil || !validation.Valid {
		if err == nil {
			err = semanticgraph.ErrNoCertifiedPath
		}
		return false, err
	}
	expanded, _, err := graph.ExpandCandidates(ctx, scope, []string{metricVID})
	if err != nil {
		return false, err
	}
	reachable := map[string]bool{}
	for _, item := range expanded {
		reachable[item.VID] = true
	}
	for _, vid := range append(append([]string{}, dimensionVIDs...), datasetVIDs...) {
		if !reachable[vid] {
			return false, semanticgraph.ErrNoCertifiedPath
		}
	}
	authorizationCandidates := append(append([]string{metricVID}, dimensionVIDs...), datasetVIDs...)
	for _, binding := range valueBindings {
		authorizationCandidates = append(authorizationCandidates, binding.ValueVID)
	}
	authorizationCandidates = uniqueStrings(authorizationCandidates, 100)
	authorized, _, err := graph.FilterAuthorized(ctx, scope, semanticgraph.AuthorizationRequest{
		RoleVIDs: roleVIDs, CandidateVIDs: authorizationCandidates,
	})
	if err != nil {
		return false, err
	}
	return sameStringSet(authorized, authorizationCandidates), nil
}

func metricClarificationForCandidates(
	mention QuestionMention,
	candidates []QuestionMentionCandidate,
	confirmed map[string]bool,
) *QueryClarification {
	items := make([]QueryMetricCandidateTrace, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, QueryMetricCandidateTrace{
			Code: candidate.Code, Label: candidate.Label, MatchedTerm: mention.MentionText,
			MatchMethod: "GOVERNED_EXACT_ALIAS_GRAPH_VALIDATED", Score: 1,
			Selected: confirmed[strings.ToLower(candidate.Code)], Source: "SEMANTIC_RELEASE",
		})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Code < items[right].Code })
	return &QueryClarification{
		Type:             "METRIC",
		Message:          "该业务名称仍对应多个会改变结果的认证执行 Bundle，请选择一个指标口径。",
		MetricCandidates: items,
	}
}

func questionPlansUseMetricCode(plans []QueryPlan, code string) bool {
	for _, plan := range plans {
		if strings.EqualFold(plan.Conditions.MetricCode, code) {
			return true
		}
	}
	return false
}

func (service *Service) questionSemanticSnapshotCurrent(
	ctx context.Context,
	tenantID, releaseID, semanticVersion, contentHash string,
) (bool, error) {
	store, ok := service.store.(questionSemanticCurrentStore)
	if service == nil || !ok || releaseID == "" || semanticVersion == "" || !validHash(contentHash) {
		return false, ErrGraphNotReady
	}
	return store.IsQuestionSemanticSnapshotCurrent(
		ctx, tenantID, releaseID, semanticVersion, contentHash,
	)
}
