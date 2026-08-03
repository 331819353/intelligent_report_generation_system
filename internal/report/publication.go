package report

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/reportjson"
)

const (
	MaxPublishedVersionsPage = 200
	MaxPublishCommentBytes   = 1000
	MaxPublishedComponents   = 200
)

// ValidatePublication 对指定服务端草稿修订执行结构、安全和精确依赖校验。
func (s *Service) ValidatePublication(ctx context.Context, tenantID, actorID, id string, input ValidateInput) (ValidationResult, error) {
	if !validPublicationIdentity(tenantID, actorID, id) || input.Revision < 1 {
		return ValidationResult{}, ErrInvalidRequest
	}
	record, err := s.store.Get(ctx, tenantID, actorID, id, "UPDATE")
	if err != nil {
		return ValidationResult{}, err
	}
	if record.Revision != input.Revision {
		return ValidationResult{}, &ConflictError{Revision: record.Revision, Hash: record.DefinitionHash}
	}
	prepared, err := reportjson.Prepare(record.Definition)
	if err != nil {
		return ValidationResult{}, err
	}
	issues := validatePublishableDocument(prepared.Document, prepared.JSON)
	_, dependencies := deriveIndexes(prepared.Document)
	_, referenceIssues, err := s.store.ResolvePublicationDependencies(ctx, tenantID, actorID, id, dependencies)
	if err != nil {
		return ValidationResult{}, err
	}
	issues = append(issues, referenceIssues...)
	sortValidationIssues(issues)
	return ValidationResult{Valid: len(issues) == 0, Issues: issues}, nil
}

// Publish 固定当前草稿修订及其指标版本，生成只读 JSON 制品并原子切换运行指针。
func (s *Service) Publish(ctx context.Context, tenantID, actorID, id, idempotencyKey string, input PublishInput) (PublishedVersion, error) {
	if !validPublicationIdentity(tenantID, actorID, id) || !validIdempotencyKey(idempotencyKey) || input.Revision < 1 || len(input.Comment) > MaxPublishCommentBytes {
		return PublishedVersion{}, ErrInvalidRequest
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		return PublishedVersion{}, err
	}
	if replay, found, err := s.store.ReplayPublication(ctx, tenantID, actorID, id, "PUBLISH", idempotencyKey, requestHash); err != nil || found {
		return replay, err
	}
	record, err := s.store.Get(ctx, tenantID, actorID, id, "PUBLISH")
	if err != nil {
		return PublishedVersion{}, err
	}
	if record.Revision != input.Revision {
		return PublishedVersion{}, &ConflictError{Revision: record.Revision, Hash: record.DefinitionHash}
	}
	draft, err := reportjson.Prepare(record.Definition)
	if err != nil {
		return PublishedVersion{}, err
	}
	issues := validatePublishableDocument(draft.Document, draft.JSON)
	components, dependencies := deriveIndexes(draft.Document)
	snapshots, referenceIssues, err := s.store.ResolvePublicationDependencies(ctx, tenantID, actorID, id, dependencies)
	if err != nil {
		return PublishedVersion{}, err
	}
	issues = append(issues, referenceIssues...)
	if len(issues) > 0 {
		sortValidationIssues(issues)
		return PublishedVersion{}, &PublicationValidationError{Issues: issues}
	}
	published, err := buildPublishedArtifact(draft, record.Revision, record.DefinitionHash, snapshots)
	if err != nil {
		return PublishedVersion{}, err
	}
	objectURI, err := s.persistPublishedArtifact(ctx, tenantID, id, published)
	if err != nil {
		return PublishedVersion{}, err
	}
	return s.store.Publish(ctx, tenantID, actorID, id, PublishPlan{
		ExpectedRevision: input.Revision,
		IdempotencyKey:   idempotencyKey,
		RequestHash:      requestHash,
		Comment:          strings.TrimSpace(input.Comment),
		ObjectURI:        objectURI,
		Prepared:         published,
		Components:       components,
		Dependencies:     snapshots,
	})
}

func (s *Service) ListVersions(ctx context.Context, tenantID, actorID, id string, limit, offset int) ([]PublishedVersion, int, error) {
	if !validPublicationIdentity(tenantID, actorID, id) || limit < 1 || limit > MaxPublishedVersionsPage || offset < 0 {
		return nil, 0, ErrInvalidRequest
	}
	return s.store.ListVersions(ctx, tenantID, actorID, id, limit, offset)
}

func (s *Service) GetVersionArtifact(ctx context.Context, tenantID, actorID, id string, version int) (VersionArtifact, error) {
	if !validPublicationIdentity(tenantID, actorID, id) || version < 1 {
		return VersionArtifact{}, ErrVersionNotFound
	}
	artifact, err := s.store.GetVersionArtifact(ctx, tenantID, actorID, id, version)
	if err != nil {
		return VersionArtifact{}, err
	}
	if artifact.Version.ObjectURI != "" && s.artifacts != nil {
		definition, readErr := s.readPublishedArtifact(ctx, artifact.Version.ObjectURI, artifact.Version.SizeBytes)
		if readErr == nil {
			artifact.Definition = definition
		} else if errors.Is(readErr, ErrArtifactCorrupt) {
			return VersionArtifact{}, ErrArtifactCorrupt
		}
	}
	if int64(len(artifact.Definition)) != artifact.Version.SizeBytes {
		return VersionArtifact{}, ErrArtifactCorrupt
	}
	prepared, err := reportjson.Prepare(artifact.Definition)
	if err != nil || prepared.Hash != artifact.Version.SHA256 || prepared.Document.Report.Status != "PUBLISHED" {
		return VersionArtifact{}, ErrArtifactCorrupt
	}
	return artifact, nil
}

func (s *Service) persistPublishedArtifact(ctx context.Context, tenantID, reportID string, artifact reportjson.Prepared) (string, error) {
	if s.artifacts == nil || s.artifactBucket == "" {
		return "", nil
	}
	key := fmt.Sprintf("tenants/%s/reports/%s/artifacts/%s.json", tenantID, reportID, artifact.Hash)
	if err := s.artifacts.Put(ctx, s.artifactBucket, key, bytes.NewReader(artifact.JSON), int64(len(artifact.JSON)), "application/json"); err != nil {
		return "", fmt.Errorf("persist report artifact: %w", err)
	}
	return "s3://" + s.artifactBucket + "/" + key, nil
}

func (s *Service) readPublishedArtifact(ctx context.Context, objectURI string, expectedSize int64) (json.RawMessage, error) {
	prefix := "s3://" + s.artifactBucket + "/"
	if !strings.HasPrefix(objectURI, prefix) {
		return nil, ErrArtifactCorrupt
	}
	reader, err := s.artifacts.Get(ctx, s.artifactBucket, strings.TrimPrefix(objectURI, prefix))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, expectedSize+1))
	if err != nil || int64(len(payload)) != expectedSize {
		return nil, ErrArtifactCorrupt
	}
	return payload, nil
}

func (s *Service) GetManifest(ctx context.Context, tenantID, actorID, id string, version int) (ReportManifest, error) {
	artifact, err := s.GetVersionArtifact(ctx, tenantID, actorID, id, version)
	if err != nil {
		return ReportManifest{}, err
	}
	return ReportManifest{
		ReportID: id, Version: version, SchemaVersion: artifact.Version.SchemaVersion,
		DefinitionURL: fmt.Sprintf("/api/v1/reports/%s/versions/%d/definition", id, version),
		SHA256:        artifact.Version.SHA256, SizeBytes: artifact.Version.SizeBytes, PublishedAt: artifact.Version.PublishedAt,
	}, nil
}

func (s *Service) Rollback(ctx context.Context, tenantID, actorID, id string, version int, idempotencyKey string, input RollbackInput) (PublishedVersion, error) {
	if !validPublicationIdentity(tenantID, actorID, id) || version < 1 || !validIdempotencyKey(idempotencyKey) || len(input.Comment) > MaxPublishCommentBytes {
		return PublishedVersion{}, ErrInvalidRequest
	}
	requestHash, err := hashRequest(struct {
		Version int    `json:"version"`
		Comment string `json:"comment,omitempty"`
	}{version, input.Comment})
	if err != nil {
		return PublishedVersion{}, err
	}
	return s.store.Rollback(ctx, tenantID, actorID, id, version, idempotencyKey, requestHash, strings.TrimSpace(input.Comment))
}

func validPublicationIdentity(tenantID, actorID, reportID string) bool {
	return tenantID != "" && actorID != "" && uuid.Validate(reportID) == nil
}

func buildPublishedArtifact(draft reportjson.Prepared, revision int64, sourceHash string, snapshots []DependencySnapshot) (reportjson.Prepared, error) {
	payload, err := json.Marshal(draft.Document)
	if err != nil {
		return reportjson.Prepared{}, err
	}
	var document reportjson.Document
	if err := json.Unmarshal(payload, &document); err != nil {
		return reportjson.Prepared{}, err
	}
	document.Report.Status = "PUBLISHED"
	if document.Extensions == nil {
		document.Extensions = map[string]any{}
	}
	metricVersions := map[string]string{}
	datasetVersions := map[string]string{}
	for _, dependency := range snapshots {
		switch dependency.Type {
		case "METRIC_VERSION":
			metricVersions[dependency.ID] = dependency.VersionID
		case "DATASET_VERSION":
			datasetVersions[dependency.ID] = dependency.VersionID
		}
	}
	document.Extensions["publication"] = map[string]any{
		"sourceRevision":  revision,
		"sourceHash":      sourceHash,
		"metricVersions":  metricVersions,
		"datasetVersions": datasetVersions,
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return reportjson.Prepared{}, err
	}
	return reportjson.Prepare(encoded)
}

func validatePublishableDocument(document reportjson.Document, raw json.RawMessage) []ValidationIssue {
	issues := []ValidationIssue{}
	if document.IsCardDSL() {
		if len(document.Cards) > MaxPublishedComponents {
			issues = append(issues, ValidationIssue{Level: "error", Code: "REPORT_LIMIT_EXCEEDED", Path: "cards", Message: fmt.Sprintf("发布报告的卡片数不能超过 %d", MaxPublishedComponents)})
		}
		for cardIndex, card := range document.Cards {
			issues = append(issues, validatePublishableCard(cardIndex, card)...)
		}
		var root any
		if json.Unmarshal(raw, &root) == nil {
			walkPublicationSecurity(root, "$", &issues)
		}
		return issues
	}
	componentCount := 0
	for pageIndex, page := range document.Pages {
		for blockIndex, block := range page.Blocks {
			componentCount += len(block.Components)
			for componentIndex, component := range block.Components {
				if component.Type == "CHART" || component.Type == "KPI" || component.Type == "TABLE" || component.Type == "CONCLUSION" {
					if len(component.Binding) == 0 {
						issues = append(issues, ValidationIssue{Level: "error", Code: "REPORT_SEMANTIC_INVALID", Path: fmt.Sprintf("pages[%d].blocks[%d].components[%d].binding", pageIndex, blockIndex, componentIndex), Message: "数据卡片必须完成数据绑定", ComponentID: component.ID})
					}
				}
			}
		}
	}
	if componentCount > MaxPublishedComponents {
		issues = append(issues, ValidationIssue{Level: "error", Code: "REPORT_LIMIT_EXCEEDED", Path: "pages", Message: fmt.Sprintf("发布报告的组件数不能超过 %d", MaxPublishedComponents)})
	}
	for index, requirement := range document.DataRequirements {
		path := fmt.Sprintf("dataRequirements[%d]", index)
		if requirement.ResolutionStatus != "RESOLVED" {
			issues = append(issues, ValidationIssue{Level: "error", Code: "REPORT_SEMANTIC_INVALID", Path: path + ".resolutionStatus", Message: "发布前必须解析数据需求"})
		}
		if len(requirement.RequiredDimensions) > 0 && requirement.ResolvedDatasetVersionID == "" {
			issues = append(issues, ValidationIssue{Level: "error", Code: "DATASET_VERSION_NOT_FOUND", Path: path + ".resolvedDatasetVersionId", Message: "维度需求必须固定到数据集版本"})
		}
		if len(requirement.ResolvedMetricIDs) < len(requirement.RequiredMetrics) {
			issues = append(issues, ValidationIssue{Level: "error", Code: "METRIC_VERSION_NOT_FOUND", Path: path + ".resolvedMetricIds", Message: "必需指标尚未全部解析"})
		}
	}
	var root any
	if json.Unmarshal(raw, &root) == nil {
		walkPublicationSecurity(root, "$", &issues)
	}
	return issues
}

func validatePublishableCard(index int, card reportjson.Card) []ValidationIssue {
	path := fmt.Sprintf("cards[%d].binding", index)
	issue := func(suffix, message string) ValidationIssue {
		return ValidationIssue{Level: "error", Code: "REPORT_SEMANTIC_INVALID", Path: path + suffix, Message: message, ComponentID: card.ID}
	}
	metrics, dimensions := len(card.Binding.Metrics), len(card.Binding.Dimensions)
	issues := []ValidationIssue{}
	if card.Type != "TITLE" && card.Binding.SemanticModelID == "" {
		issues = append(issues, issue(".semanticModelId", "数据卡片必须绑定语义模型"))
	}
	switch card.Type {
	case "TITLE":
		if metrics+dimensions > 0 {
			issues = append(issues, issue("", "标题卡不能绑定指标或维度"))
		}
	case "CONCLUSION":
		if metrics != 1 {
			issues = append(issues, issue(".metrics", "结论卡必须绑定一个主指标"))
		}
	case "CHART":
		if metrics < 1 {
			issues = append(issues, issue(".metrics", "图形卡至少绑定一个指标"))
		}
	case "COMPARISON":
		if metrics < 1 || metrics > 2 {
			issues = append(issues, issue(".metrics", "对比卡需要一个当前指标和可选基线指标"))
		}
	case "RANKING":
		if metrics < 1 || dimensions < 1 || len(card.Binding.Sort) == 0 || card.Binding.Limit < 1 || card.Binding.Limit > 100 {
			issues = append(issues, issue("", "排序卡必须配置指标、维度、排序和 1～100 的 TopN"))
		}
	case "TABLE":
		if metrics+dimensions < 1 {
			issues = append(issues, issue("", "表格卡至少绑定一个字段"))
		}
	}
	return issues
}

var forbiddenPublicationKeys = map[string]bool{
	"tenantid": true, "sql": true, "rawsql": true, "mdx": true,
	"connectionstring": true, "password": true, "secret": true,
	"javascript": true, "script": true, "eval": true,
}

func walkPublicationSecurity(value any, path string, issues *[]ValidationIssue) {
	switch current := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
			nextPath := path + "." + key
			if forbiddenPublicationKeys[normalized] {
				*issues = append(*issues, ValidationIssue{Level: "error", Code: "REPORT_SECURITY_INVALID", Path: nextPath, Message: "发布 JSON 禁止包含租户覆盖、原始查询、密钥或可执行脚本"})
			}
			walkPublicationSecurity(current[key], nextPath, issues)
		}
	case []any:
		for index, item := range current {
			walkPublicationSecurity(item, fmt.Sprintf("%s[%d]", path, index), issues)
		}
	case string:
		lower := strings.ToLower(strings.TrimSpace(current))
		if strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "data:text/html") || strings.HasPrefix(lower, "file:") {
			*issues = append(*issues, ValidationIssue{Level: "error", Code: "REPORT_SECURITY_INVALID", Path: path, Message: "发布 JSON 包含不安全的资源协议"})
		}
	}
}

func sortValidationIssues(issues []ValidationIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		left := issues[i].Path + "\x00" + issues[i].Code + "\x00" + issues[i].DependencyID
		right := issues[j].Path + "\x00" + issues[j].Code + "\x00" + issues[j].DependencyID
		return left < right
	})
}
