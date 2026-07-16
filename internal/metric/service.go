package metric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/dataset"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	maxMetricDependencyVersions = 128
	maxMetricDependencyDepth    = 64
)

// Service 编排指标定义校验、精确数据集解析、试算和不可变发布。
type Service struct {
	store     Store
	previewer Previewer
	access    interface {
		Allowed(context.Context, access.Check) (bool, error)
	}
}

func NewService(store Store, previewers ...Previewer) *Service {
	service := &Service{store: store}
	if len(previewers) > 0 {
		service.previewer = previewers[0]
	}
	return service
}

func (s *Service) SetPreviewer(previewer Previewer) { s.previewer = previewer }

// SetPermissionChecker 接入统一权限服务，用于复核指标实际读取的精确数据集。
func (s *Service) SetPermissionChecker(checker interface {
	Allowed(context.Context, access.Check) (bool, error)
}) {
	s.access = checker
}

func (s *Service) Create(ctx context.Context, tenantID, actorID string, input CreateInput) (Record, error) {
	if tenantID == "" || actorID == "" {
		return Record{}, ErrInvalidDefinition
	}
	prepared, err := Prepare(input.Definition)
	if err != nil {
		return Record{}, err
	}
	if err := s.requireDatasetRead(ctx, tenantID, actorID, prepared.Definition.DatasetID); err != nil {
		return Record{}, err
	}
	if _, err := s.validatePrepared(ctx, tenantID, actorID, "", prepared); err != nil {
		return Record{}, err
	}
	return s.store.Create(ctx, tenantID, actorID, prepared)
}

func (s *Service) Get(ctx context.Context, tenantID, id string) (Record, error) {
	if tenantID == "" || !canonicalUUID(id) {
		return Record{}, ErrNotFound
	}
	return s.store.Get(ctx, tenantID, id)
}

func (s *Service) List(ctx context.Context, tenantID string, limit, offset int) ([]Summary, int, error) {
	if tenantID == "" || limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidDefinition
	}
	return s.store.List(ctx, tenantID, limit, offset)
}

func (s *Service) Update(ctx context.Context, tenantID, actorID, id string, input UpdateInput) (Record, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || input.ExpectedVersion < 1 || input.ExpectedDraftRecordVersion < 1 || !sha256Pattern.MatchString(input.ExpectedDefinitionHash) {
		return Record{}, ErrInvalidDefinition
	}
	current, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return Record{}, err
	}
	prepared, err := Prepare(input.Definition)
	if err != nil {
		return Record{}, err
	}
	if current.Code != prepared.Definition.Metric.Code || current.DatasetID != prepared.Definition.DatasetID {
		return Record{}, invalid("metric.code", "METRIC_IDENTITY_IMMUTABLE", "指标编码和所属数据集不能通过草稿更新修改")
	}
	if err := s.requireDatasetRead(ctx, tenantID, actorID, prepared.Definition.DatasetID); err != nil {
		return Record{}, err
	}
	if _, err := s.validatePrepared(ctx, tenantID, actorID, id, prepared); err != nil {
		return Record{}, err
	}
	return s.store.Update(ctx, tenantID, actorID, id, input, prepared)
}

// ValidateCurrent 对当前草稿重新执行全部数据集、字段、依赖和扇出校验。
func (s *Service) ValidateCurrent(ctx context.Context, tenantID, actorID, id string) (Prepared, error) {
	record, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return Prepared{}, err
	}
	if err := s.requireDatasetRead(ctx, tenantID, actorID, record.DatasetID); err != nil {
		return Prepared{}, err
	}
	prepared, err := Prepare(record.Definition)
	if err != nil {
		return Prepared{}, err
	}
	_, err = s.validatePrepared(ctx, tenantID, actorID, id, prepared)
	return prepared, err
}

func (s *Service) Preview(ctx context.Context, tenantID, actorID, id string, input PreviewInput) (dataset.PreviewResult, error) {
	if s.previewer == nil {
		return dataset.PreviewResult{}, ErrPreviewUnavailable
	}
	record, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	if err := s.requireDatasetRead(ctx, tenantID, actorID, record.DatasetID); err != nil {
		return dataset.PreviewResult{}, err
	}
	prepared, err := Prepare(record.Definition)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	validated, err := s.validatePrepared(ctx, tenantID, actorID, id, prepared)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	candidate, err := buildQueryCandidate(id, record.DraftVersionID, validated, input.DimensionFieldIDs)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	return s.previewer.PreviewMetric(ctx, tenantID, actorID, candidate, dataset.PreviewInput{
		QueryID: input.QueryID, Parameters: input.Parameters, MaxRows: input.MaxRows,
	}, false)
}

func (s *Service) PreviewVersion(ctx context.Context, tenantID, actorID, id, versionID string, input PreviewInput) (dataset.PreviewResult, error) {
	if s.previewer == nil {
		return dataset.PreviewResult{}, ErrPreviewUnavailable
	}
	version, err := s.GetVersion(ctx, tenantID, id, versionID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	if err := s.requireDatasetRead(ctx, tenantID, actorID, version.DatasetID); err != nil {
		return dataset.PreviewResult{}, err
	}
	if version.Status != "PUBLISHED" {
		return dataset.PreviewResult{}, ErrVersionUnavailable
	}
	prepared, err := Prepare(version.Definition)
	if err != nil {
		return dataset.PreviewResult{}, ErrVersionUnavailable
	}
	validated, err := s.validatePrepared(ctx, tenantID, actorID, id, prepared)
	if err != nil {
		// 发布版本的定义由服务端冻结；再次校验失败代表精确依赖已不可用，
		// 不能把它伪装成调用方可修改的定义错误。
		var validation *ValidationError
		if errors.As(err, &validation) {
			return dataset.PreviewResult{}, ErrVersionUnavailable
		}
		return dataset.PreviewResult{}, err
	}
	candidate, err := buildQueryCandidate(id, version.ID, validated, input.DimensionFieldIDs)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	return s.previewer.PreviewMetric(ctx, tenantID, actorID, candidate, dataset.PreviewInput{
		QueryID: input.QueryID, Parameters: input.Parameters, MaxRows: input.MaxRows,
	}, false)
}

func (s *Service) Publish(ctx context.Context, tenantID, actorID, id, idempotencyKey string, input PublishInput) (VersionRecord, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || !canonicalUUID(input.DraftVersionID) ||
		input.ExpectedVersion < 1 || input.ExpectedDraftRecordVersion < 1 || !sha256Pattern.MatchString(input.ExpectedDefinitionHash) || !validIdempotencyKey(idempotencyKey) {
		return VersionRecord{}, ErrInvalidDefinition
	}
	if input.ValidationParameters == nil {
		input.ValidationParameters = map[string]any{}
	}
	requestHash, err := publishRequestHash(id, input)
	if err != nil {
		return VersionRecord{}, ErrInvalidDefinition
	}
	// 先重放成功响应，覆盖事务已提交但客户端没有收到响应的网络重试。
	if replay, found, err := s.store.ReplayPublication(ctx, tenantID, actorID, id, idempotencyKey, requestHash); err != nil {
		return VersionRecord{}, err
	} else if found {
		if err := s.requireDatasetRead(ctx, tenantID, actorID, replay.DatasetID); err != nil {
			return VersionRecord{}, err
		}
		prepared, err := Prepare(replay.Definition)
		if err != nil || prepared.DefinitionHash != replay.DefinitionHash {
			return VersionRecord{}, ErrVersionUnavailable
		}
		// 幂等重放返回首次响应，但仍要按当前授权复核全部传递指标依赖；
		// 依赖后续变为 STALE/DEPRECATED 不改写首次响应本身。
		if _, err := s.loadDependencies(ctx, tenantID, actorID, id, prepared,
			map[string]int{}, map[string]Prepared{}, false); err != nil {
			return VersionRecord{}, err
		}
		return replay, nil
	}
	if s.previewer == nil {
		return VersionRecord{}, ErrPreviewUnavailable
	}
	current, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return VersionRecord{}, err
	}
	if err := s.requireDatasetRead(ctx, tenantID, actorID, current.DatasetID); err != nil {
		return VersionRecord{}, err
	}
	if current.Version != input.ExpectedVersion || current.DraftVersionID != input.DraftVersionID ||
		current.DraftRecordVersion != input.ExpectedDraftRecordVersion || current.DefinitionHash != input.ExpectedDefinitionHash {
		return VersionRecord{}, ErrConflict
	}
	prepared, err := Prepare(current.Definition)
	if err != nil || prepared.DefinitionHash != current.DefinitionHash {
		return VersionRecord{}, invalid("definition", "METRIC_DERIVATION_MISMATCH", "当前指标草稿与服务端摘要不一致")
	}
	validated, err := s.validatePrepared(ctx, tenantID, actorID, id, prepared)
	if err != nil {
		return VersionRecord{}, err
	}
	candidate, err := buildQueryCandidate(id, current.DraftVersionID, validated, nil)
	if err != nil {
		return VersionRecord{}, err
	}
	result, err := s.previewer.PreviewMetric(ctx, tenantID, actorID, candidate, dataset.PreviewInput{
		Parameters: input.ValidationParameters, MaxRows: 1,
	}, true)
	if err != nil {
		return VersionRecord{}, invalid("expression", "METRIC_PUBLISH_PREVIEW_FAILED", "指标发布前试算失败")
	}
	if validated.duplicateSensitive {
		for _, warning := range result.Warnings {
			if warning.Code == "JOIN_FANOUT_RISK" || warning.Code == "JOIN_CARDINALITY_MISMATCH" || warning.Code == "JOIN_MANY_TO_MANY" {
				return VersionRecord{}, invalid("expression", "METRIC_JOIN_FANOUT_DETECTED", "发布试算检测到可能重复累计指标的 Join 扇出")
			}
		}
	}
	return s.store.Publish(ctx, tenantID, actorID, id, PublishPlan{
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, ExpectedVersion: input.ExpectedVersion,
		DraftVersionID: input.DraftVersionID, ExpectedDraftRecordVersion: input.ExpectedDraftRecordVersion,
		ExpectedDefinitionHash: input.ExpectedDefinitionHash, Prepared: prepared,
	})
}

func (s *Service) GetVersion(ctx context.Context, tenantID, id, versionID string) (VersionRecord, error) {
	if tenantID == "" || !canonicalUUID(id) || !canonicalUUID(versionID) {
		return VersionRecord{}, ErrVersionNotFound
	}
	return s.store.GetVersion(ctx, tenantID, id, versionID)
}

func (s *Service) ListVersions(ctx context.Context, tenantID, id string, limit, offset int) ([]VersionSummary, int, error) {
	if tenantID == "" || !canonicalUUID(id) || limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidDefinition
	}
	return s.store.ListVersions(ctx, tenantID, id, limit, offset)
}

func (s *Service) GetVersionUsage(ctx context.Context, tenantID, id, versionID string) (VersionUsage, error) {
	if tenantID == "" || !canonicalUUID(id) || !canonicalUUID(versionID) {
		return VersionUsage{}, ErrVersionNotFound
	}
	return s.store.GetVersionUsage(ctx, tenantID, id, versionID)
}

func (s *Service) TransitionVersion(ctx context.Context, tenantID, actorID, id, versionID string, input VersionTransitionInput) (VersionRecord, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || !canonicalUUID(versionID) || input.ExpectedVersion < 1 || input.ExpectedStatus != "PUBLISHED" || input.TargetStatus != "DEPRECATED" {
		return VersionRecord{}, ErrInvalidTransition
	}
	return s.store.TransitionVersion(ctx, tenantID, actorID, id, versionID, input)
}

func (s *Service) Cancel(ctx context.Context, tenantID, actorID, id, queryID string) error {
	if s.previewer == nil {
		return ErrPreviewUnavailable
	}
	record, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	return s.previewer.Cancel(ctx, tenantID, actorID, record.DatasetID, queryID)
}

func (s *Service) validatePrepared(ctx context.Context, tenantID, actorID, metricID string, prepared Prepared) (validatedDefinition, error) {
	version, err := s.store.GetDatasetVersion(ctx, tenantID, prepared.Definition.DatasetID, prepared.Definition.DatasetVersionID)
	if err != nil || version.Status != "PUBLISHED" {
		return validatedDefinition{}, invalid("datasetVersionId", "METRIC_DATASET_VERSION_UNAVAILABLE", "指标必须固定租户内仍处于 PUBLISHED 的精确数据集版本")
	}
	document, err := dataset.DecodeAndNormalize(version.DSL)
	if err != nil || version.DatasetID != prepared.Definition.DatasetID {
		return validatedDefinition{}, invalid("datasetVersionId", "METRIC_DATASET_VERSION_INVALID", "数据集版本定义无法解析或不属于指定数据集")
	}
	if len(document.GroupBy) > 0 || len(document.Having) > 0 {
		return validatedDefinition{}, invalid("datasetVersionId", "METRIC_AGGREGATED_DATASET_UNSUPPORTED", "首阶段指标只能基于未预聚合的数据集版本")
	}
	fields := make(map[string]dataset.Field, len(document.Fields))
	for index, field := range document.Fields {
		if expressionContainsAggregate(field.Expression) {
			return validatedDefinition{}, invalid(fmt.Sprintf("datasetVersion.fields[%d]", index), "METRIC_AGGREGATED_DATASET_UNSUPPORTED", "首阶段指标不能再次聚合数据集中的聚合字段")
		}
		fields[field.ID] = field
	}
	dependencies, err := s.loadDependencies(ctx, tenantID, actorID, metricID, prepared,
		map[string]int{}, map[string]Prepared{}, true)
	if err != nil {
		return validatedDefinition{}, err
	}
	allDefinitions := append([]Prepared{prepared}, dependencyValues(dependencies)...)
	for _, candidate := range allDefinitions {
		if candidate.Definition.DatasetID != prepared.Definition.DatasetID || candidate.Definition.DatasetVersionID != prepared.Definition.DatasetVersionID {
			return validatedDefinition{}, invalid("expression.metricVersionId", "METRIC_REFERENCE_DATASET_MISMATCH", "所有指标依赖必须固定同一数据集版本")
		}
		if err := validateDefinitionFields(candidate.Definition, fields); err != nil {
			return validatedDefinition{}, err
		}
	}
	if err := validateDependencyDimensions(prepared.Definition, dependencies); err != nil {
		return validatedDefinition{}, err
	}
	expanded, err := expandMetricExpression(prepared.Definition, fields, dependencies, map[string]bool{})
	if err != nil {
		return validatedDefinition{}, err
	}
	duplicateSensitive := metricDuplicateSensitive(prepared.Definition, dependencies)
	if duplicateSensitive {
		nodes := map[string]bool{}
		expressionNodeIDs(expanded, nodes)
		if index, risky := firstMetricJoinFanoutRisk(document.Joins, nodes); risky {
			return validatedDefinition{}, invalid(fmt.Sprintf("datasetVersion.joins[%d]", index), "METRIC_JOIN_FANOUT_RISK", "指标引用 Join 的一侧会在声明粒度下重复累计")
		}
	}
	return validatedDefinition{
		prepared: prepared, datasetVersion: version, datasetDocument: document,
		dependencies: dependencies, duplicateSensitive: duplicateSensitive,
	}, nil
}

func (s *Service) loadDependencies(ctx context.Context, tenantID, actorID, rootMetricID string, root Prepared, state map[string]int, loaded map[string]Prepared, requirePublished bool) (map[string]Prepared, error) {
	var visit func(string) error
	visit = func(versionID string) error {
		if state[versionID] == 1 {
			return invalid("expression.metricVersionId", "METRIC_REFERENCE_CYCLE", "指标版本引用形成循环")
		}
		if state[versionID] == 2 {
			return nil
		}
		if len(state) >= maxMetricDependencyVersions {
			return invalid("expression.metricVersionId", "METRIC_DEPENDENCY_COMPLEXITY_EXCEEDED", "指标依赖图不能超过 128 个精确版本")
		}
		state[versionID] = 1
		version, err := s.store.GetVersionByID(ctx, tenantID, versionID)
		if err != nil || (requirePublished && version.Status != "PUBLISHED") ||
			(rootMetricID != "" && version.MetricID == rootMetricID) {
			return invalid("expression.metricVersionId", "METRIC_REFERENCE_UNAVAILABLE", "指标依赖不存在、已不可用或引用了自身历史版本")
		}
		if err := s.requireMetricRead(ctx, tenantID, actorID, version.MetricID); err != nil {
			return err
		}
		prepared, err := Prepare(version.Definition)
		if err != nil || prepared.DefinitionHash != version.DefinitionHash {
			return invalid("expression.metricVersionId", "METRIC_REFERENCE_INVALID", "指标依赖定义与可信摘要不一致")
		}
		loaded[versionID] = prepared
		for _, child := range prepared.DependencyVersionIDs {
			if err := visit(child); err != nil {
				return err
			}
		}
		state[versionID] = 2
		return nil
	}
	for _, versionID := range root.DependencyVersionIDs {
		if err := visit(versionID); err != nil {
			return nil, err
		}
	}
	// 加载完成后按 DAG 最长路径计算深度。不能在共享节点已访问时直接早退，
	// 否则“先浅后深”命中同一子树会漏算后续层级。
	if metricDependencyDepth(root, loaded) > maxMetricDependencyDepth {
		return nil, invalid("expression.metricVersionId", "METRIC_DEPENDENCY_COMPLEXITY_EXCEEDED", "指标依赖图不能超过 64 层")
	}
	return loaded, nil
}

func metricDependencyDepth(root Prepared, loaded map[string]Prepared) int {
	heights := make(map[string]int, len(loaded))
	var height func(string) int
	height = func(versionID string) int {
		if cached := heights[versionID]; cached > 0 {
			return cached
		}
		result := 1
		for _, child := range loaded[versionID].DependencyVersionIDs {
			if candidate := 1 + height(child); candidate > result {
				result = candidate
			}
		}
		heights[versionID] = result
		return result
	}
	result := 0
	for _, versionID := range root.DependencyVersionIDs {
		if candidate := height(versionID); candidate > result {
			result = candidate
		}
	}
	return result
}

func validateDefinitionFields(definition Definition, fields map[string]dataset.Field) error {
	var fieldIssue error
	visitMetricExpression(definition.Expression, func(expression Expression) {
		if fieldIssue != nil || expression.Type != "FIELD_REF" {
			return
		}
		field, exists := fields[expression.FieldID]
		if !exists || field.CanonicalType != "INTEGER" && field.CanonicalType != "DECIMAL" {
			fieldIssue = invalid("expression.fieldId", "METRIC_NUMERIC_FIELD_REQUIRED", "指标表达式只能引用当前数据集版本的数值字段")
		}
	})
	if fieldIssue != nil {
		return fieldIssue
	}
	for index, dimension := range definition.AllowedDimensions {
		field, exists := fields[dimension.FieldID]
		if !exists || field.Role != "DIMENSION" && field.Role != "TIME" && field.Role != "ATTRIBUTE" && field.Role != "IDENTIFIER" {
			return invalid(fmt.Sprintf("allowedDimensions[%d].fieldId", index), "METRIC_DIMENSION_FIELD_INVALID", "维度必须引用当前数据集版本的非度量字段")
		}
		for hierarchyIndex, hierarchyFieldID := range dimension.HierarchyFieldIDs {
			hierarchyField, exists := fields[hierarchyFieldID]
			if !exists || hierarchyField.Role == "MEASURE" {
				return invalid(fmt.Sprintf("allowedDimensions[%d].hierarchyFieldIds[%d]", index, hierarchyIndex), "METRIC_DIMENSION_HIERARCHY_INVALID", "层级只能引用当前数据集版本的非度量字段")
			}
		}
		if field.Code == definition.Metric.Code {
			return invalid("metric.code", "METRIC_CODE_DIMENSION_CONFLICT", "指标编码不能与可用维度字段编码重复")
		}
	}
	if definition.TimeFieldID != "" {
		field, exists := fields[definition.TimeFieldID]
		if !exists || field.Role != "TIME" || field.CanonicalType != "DATE" && field.CanonicalType != "DATETIME" {
			return invalid("timeFieldId", "METRIC_TIME_FIELD_INVALID", "时间字段必须引用 DATE 或 DATETIME 类型的 TIME 字段")
		}
	}
	return nil
}

func validateDependencyDimensions(root Definition, dependencies map[string]Prepared) error {
	rootDimensions := map[string]bool{}
	for _, dimension := range root.AllowedDimensions {
		rootDimensions[dimension.FieldID] = true
	}
	for versionID, dependency := range dependencies {
		if dependency.Definition.TimeFieldID != root.TimeFieldID || dependency.Definition.TimeGrain != root.TimeGrain {
			return invalid("timeGrain", "METRIC_REFERENCE_TIME_GRAIN_MISMATCH", fmt.Sprintf("依赖指标版本 %s 的时间字段或粒度与当前指标不一致", versionID))
		}
		allowed := map[string]bool{}
		for _, dimension := range dependency.Definition.AllowedDimensions {
			allowed[dimension.FieldID] = true
		}
		for fieldID := range rootDimensions {
			if !allowed[fieldID] {
				return invalid("allowedDimensions", "METRIC_REFERENCE_DIMENSION_MISMATCH", fmt.Sprintf("依赖指标版本 %s 不允许当前指标声明的全部维度", versionID))
			}
		}
	}
	return nil
}

func metricDuplicateSensitive(root Definition, dependencies map[string]Prepared) bool {
	if oneOf(root.Aggregation, "SUM", "AVG", "COUNT") {
		return true
	}
	for _, dependency := range dependencies {
		if oneOf(dependency.Definition.Aggregation, "SUM", "AVG", "COUNT") {
			return true
		}
	}
	return false
}

// firstMetricJoinFanoutRisk 沿 Join 图追踪指标来源，避免只检查相邻节点而漏掉多跳扇出。
func firstMetricJoinFanoutRisk(joins []dataset.Join, metricNodes map[string]bool) (int, bool) {
	for index, join := range joins {
		leftContainsMetric := joinSideContainsMetric(join.LeftNodeID, index, joins, metricNodes)
		rightContainsMetric := joinSideContainsMetric(join.RightNodeID, index, joins, metricNodes)
		risky := join.Cardinality == "MANY_TO_MANY" && (leftContainsMetric || rightContainsMetric) ||
			join.Cardinality == "ONE_TO_MANY" && leftContainsMetric ||
			join.Cardinality == "MANY_TO_ONE" && rightContainsMetric
		if risky {
			return index, true
		}
	}
	return 0, false
}

func joinSideContainsMetric(start string, excludedJoin int, joins []dataset.Join, metricNodes map[string]bool) bool {
	pending := []string{start}
	visited := map[string]bool{}
	for len(pending) > 0 {
		nodeID := pending[0]
		pending = pending[1:]
		if visited[nodeID] {
			continue
		}
		visited[nodeID] = true
		if metricNodes[nodeID] {
			return true
		}
		for index, join := range joins {
			if index == excludedJoin {
				continue
			}
			switch nodeID {
			case join.LeftNodeID:
				pending = append(pending, join.RightNodeID)
			case join.RightNodeID:
				pending = append(pending, join.LeftNodeID)
			}
		}
	}
	return false
}

func visitMetricExpression(expression Expression, visit func(Expression)) {
	visit(expression)
	for _, argument := range expression.Arguments {
		visitMetricExpression(argument, visit)
	}
}

func dependencyValues(values map[string]Prepared) []Prepared {
	result := make([]Prepared, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func publishRequestHash(metricID string, input PublishInput) (string, error) {
	payload := struct {
		MetricID                   string         `json:"metricId"`
		DraftVersionID             string         `json:"draftVersionId"`
		ExpectedVersion            int64          `json:"expectedVersion"`
		ExpectedDraftRecordVersion int64          `json:"expectedDraftRecordVersion"`
		ExpectedDefinitionHash     string         `json:"expectedDefinitionHash"`
		ValidationParameters       map[string]any `json:"validationParameters"`
	}{metricID, input.DraftVersionID, input.ExpectedVersion, input.ExpectedDraftRecordVersion, input.ExpectedDefinitionHash, input.ValidationParameters}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func (s *Service) requireDatasetRead(ctx context.Context, tenantID, actorID, datasetID string) error {
	return s.requireRead(ctx, tenantID, actorID, "DATASET", datasetID)
}

func (s *Service) requireMetricRead(ctx context.Context, tenantID, actorID, metricID string) error {
	return s.requireRead(ctx, tenantID, actorID, "METRIC", metricID)
}

func (s *Service) requireRead(ctx context.Context, tenantID, actorID, resourceType, objectID string) error {
	if s.access == nil {
		return nil
	}
	allowed, err := s.access.Allowed(ctx, access.Check{
		TenantID: tenantID, UserID: actorID, ResourceType: resourceType, Action: "READ", ObjectID: objectID,
	})
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
