package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

// Store 定义数据集草稿、发布版本和幂等响应的租户内持久化边界。
type Store interface {
	Create(context.Context, string, string, CreateInput, Prepared) (Record, error)
	Get(context.Context, string, string) (Record, error)
	List(context.Context, string, int, int) ([]Summary, int, error)
	Update(context.Context, string, string, string, UpdateInput, Prepared) (Record, error)
	Disable(context.Context, string, string, string, LifecycleInput) (Record, error)
	Restore(context.Context, string, string, string, LifecycleInput) (Record, error)
	Delete(context.Context, string, string, string, LifecycleInput) error
	ReplayPublication(context.Context, string, string, string, string, string) (VersionRecord, bool, error)
	Publish(context.Context, string, string, string, PublishPlan) (VersionRecord, error)
	GetVersion(context.Context, string, string, string) (VersionRecord, error)
	ResolveVersionSourceRevision(context.Context, string, string, string) (RevisionRecord, error)
	ListVersions(context.Context, string, string, int, int) ([]VersionSummary, int, error)
	GetVersionUsage(context.Context, string, string, string) (VersionUsage, error)
	TransitionVersion(context.Context, string, string, string, string, VersionTransitionInput) (VersionRecord, error)
	GetRevision(context.Context, string, string, string) (RevisionRecord, error)
	ListRevisions(context.Context, string, string, int, int) ([]RevisionSummary, int, error)
	RollbackRevision(context.Context, string, string, string, RollbackRevisionInput, RevisionRecord, Prepared) (Record, error)
}

// PublicationValidator 使用确定的草稿快照执行发布前权限加载和最小查询试跑。
type PublicationValidator interface {
	ValidatePublication(context.Context, string, string, PublicationCandidate) (PreviewResult, error)
}

// Service 编排 DSL 校验、草稿持久化、发布试跑和不可变版本管理。
type Service struct {
	store     Store
	validator PublicationValidator
}

// NewService 创建数据集领域服务。
func NewService(store Store, validators ...PublicationValidator) *Service {
	service := &Service{store: store}
	if len(validators) > 0 {
		service.validator = validators[0]
	}
	return service
}

// SetPublicationValidator 在 API 依赖完成装配后注册查询运行时，避免领域层依赖具体执行器。
func (s *Service) SetPublicationValidator(validator PublicationValidator) { s.validator = validator }

// Validate 仅校验 DSL 并返回规范结构和逻辑计划，不产生持久化副作用。
func (s *Service) Validate(raw []byte) (Prepared, error) { return Prepare(raw) }

// Create 创建首个可编辑草稿，DSL 中的基本信息必须与外层请求一致。
func (s *Service) Create(ctx context.Context, tenantID, actorID string, input CreateInput) (Record, error) {
	if tenantID == "" || actorID == "" {
		return Record{}, errors.New("tenant and actor are required")
	}
	input.Code, input.Name = strings.TrimSpace(input.Code), strings.TrimSpace(input.Name)
	input.Description, input.Type = strings.TrimSpace(input.Description), strings.ToUpper(strings.TrimSpace(input.Type))
	input.Layer = Layer(strings.ToUpper(strings.TrimSpace(string(input.Layer))))
	prepared, err := Prepare(input.DSL)
	if err != nil {
		return Record{}, err
	}
	if input.Code == "" || input.Name == "" || input.Type == "" {
		return Record{}, fmt.Errorf("%w: code, name and type are required", ErrInvalidDocument)
	}
	if input.Code != prepared.Document.Dataset.Code || input.Name != prepared.Document.Dataset.Name || input.Type != prepared.Document.Dataset.Type {
		return Record{}, fmt.Errorf("%w: request metadata must match DSL dataset metadata", ErrInvalidDocument)
	}
	if input.Description != prepared.Document.Dataset.Description {
		return Record{}, fmt.Errorf("%w: description must match DSL dataset description", ErrInvalidDocument)
	}
	if input.Layer == "" {
		input.Layer = prepared.Document.Dataset.Layer
	} else if input.Layer != prepared.Document.Dataset.Layer {
		return Record{}, fmt.Errorf("%w: layer must match DSL dataset layer", ErrInvalidDocument)
	}
	return s.store.Create(ctx, tenantID, actorID, input, prepared)
}

// Get 加载租户内数据集及其当前规范化草稿。
func (s *Service) Get(ctx context.Context, tenantID, id string) (Record, error) {
	if tenantID == "" || strings.TrimSpace(id) == "" {
		return Record{}, ErrNotFound
	}
	return s.store.Get(ctx, tenantID, id)
}

// List 分页返回当前租户的数据集目录。
func (s *Service) List(ctx context.Context, tenantID string, limit, offset int) ([]Summary, int, error) {
	if tenantID == "" || limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, fmt.Errorf("%w: invalid dataset page", ErrInvalidDocument)
	}
	return s.store.List(ctx, tenantID, limit, offset)
}

// Update 更新草稿并以 expectedVersion 防止覆盖并发修改。
func (s *Service) Update(ctx context.Context, tenantID, actorID, id string, input UpdateInput) (Record, error) {
	if tenantID == "" || actorID == "" || strings.TrimSpace(id) == "" {
		return Record{}, ErrNotFound
	}
	if input.ExpectedVersion <= 0 {
		return Record{}, fmt.Errorf("%w: expectedVersion must be greater than zero", ErrInvalidDocument)
	}
	input.Name, input.Description = strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
	prepared, err := Prepare(input.DSL)
	if err != nil {
		return Record{}, err
	}
	if input.Name == "" || input.Name != prepared.Document.Dataset.Name {
		return Record{}, fmt.Errorf("%w: name must match DSL dataset name", ErrInvalidDocument)
	}
	if input.Description != prepared.Document.Dataset.Description {
		return Record{}, fmt.Errorf("%w: description must match DSL dataset description", ErrInvalidDocument)
	}
	current, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return Record{}, err
	}
	if current.Code != prepared.Document.Dataset.Code {
		return Record{}, fmt.Errorf("%w: dataset code cannot be changed through draft update", ErrInvalidDocument)
	}
	// 首次发布后，层级成为数据集身份的一部分。跨层改造必须新建数据集并
	// 显式建立血缘，否则稳定视图、上游合同和历史物化会在同一 ID 下冲突。
	if current.CurrentPublishedVersionID != "" {
		published, err := s.store.GetVersion(
			ctx,
			tenantID,
			id,
			current.CurrentPublishedVersionID,
		)
		if err != nil {
			return Record{}, err
		}
		if published.Layer != prepared.Document.Dataset.Layer {
			return Record{}, fmt.Errorf(
				"%w: published dataset layer is immutable; create a new dataset for cross-layer migration",
				ErrInvalidTransition,
			)
		}
	}
	// 数据集类型由当前草稿引用的数据源数量派生。设计器增删跨源节点时会在
	// SINGLE_SOURCE 与 CROSS_SOURCE 之间切换，不能把这个派生属性当作不可变编码。
	return s.store.Update(ctx, tenantID, actorID, id, input, prepared)
}

// Disable 把数据集切换为可恢复的目录级停用状态；草稿和发布快照均保持不变。
func (s *Service) Disable(ctx context.Context, tenantID, actorID, id string, input LifecycleInput) (Record, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || input.ExpectedVersion < 1 {
		return Record{}, ErrInvalidDocument
	}
	return s.store.Disable(ctx, tenantID, actorID, id, input)
}

// Restore 只允许把目录级 DISABLED 数据集恢复到停用前的稳定状态。发布版本
// 快照保持不可变；没有可靠停用快照的迁移前数据按仓储约定安全恢复为草稿。
func (s *Service) Restore(ctx context.Context, tenantID, actorID, id string, input LifecycleInput) (Record, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || input.ExpectedVersion < 1 {
		return Record{}, ErrInvalidDocument
	}
	return s.store.Restore(ctx, tenantID, actorID, id, input)
}

// Delete 软删除未被下游或运行中查询占用的数据集，历史审计和版本快照不做物理清除。
func (s *Service) Delete(ctx context.Context, tenantID, actorID, id string, input LifecycleInput) error {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || input.ExpectedVersion < 1 {
		return ErrInvalidDocument
	}
	return s.store.Delete(ctx, tenantID, actorID, id, input)
}

// Publish 将一个未变化的草稿修订试跑后复制为不可变发布版本。
func (s *Service) Publish(ctx context.Context, tenantID, actorID, id, idempotencyKey string, input PublishInput) (VersionRecord, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || !canonicalUUID(input.DraftVersionID) ||
		!validIdempotencyKey(idempotencyKey) || input.ExpectedVersion < 1 || input.ExpectedDraftRecordVersion < 1 || !validHash(input.ExpectedDSLHash) {
		return VersionRecord{}, ErrInvalidDocument
	}
	if input.ValidationParameters == nil {
		input.ValidationParameters = map[string]any{}
	}
	requestHash, err := publicationRequestHash(id, input)
	if err != nil {
		return VersionRecord{}, ErrInvalidDocument
	}
	// 幂等命中必须先于草稿版本检查，覆盖数据库已提交但客户端没有收到响应的重试。
	if replay, found, err := s.store.ReplayPublication(ctx, tenantID, actorID, id, idempotencyKey, requestHash); err != nil || found {
		return replay, err
	}
	plan, err := s.preparePublication(ctx, tenantID, actorID, id, idempotencyKey, input)
	if err != nil {
		return VersionRecord{}, err
	}
	record, err := s.store.Publish(ctx, tenantID, actorID, id, plan)
	if errors.Is(err, ErrPublishValidation) {
		return VersionRecord{}, publicationFailure("nodes", "PUBLISH_DEPENDENCY_CHANGED", "草稿依赖在发布前发生变化，请重新保存并试跑")
	}
	return record, err
}

// preparePublication performs all deterministic and query-runtime validation without writing a
// version. Approval uses the returned immutable plan in a transaction that also finalizes the
// human decision; legacy direct publication reuses the same validation boundary.
func (s *Service) preparePublication(
	ctx context.Context,
	tenantID, actorID, id, idempotencyKey string,
	input PublishInput,
) (PublishPlan, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(id) || !canonicalUUID(input.DraftVersionID) ||
		!validIdempotencyKey(idempotencyKey) || input.ExpectedVersion < 1 || input.ExpectedDraftRecordVersion < 1 || !validHash(input.ExpectedDSLHash) {
		return PublishPlan{}, ErrInvalidDocument
	}
	if input.ValidationParameters == nil {
		input.ValidationParameters = map[string]any{}
	}
	requestHash, err := publicationRequestHash(id, input)
	if err != nil {
		return PublishPlan{}, ErrInvalidDocument
	}
	if s.validator == nil {
		return PublishPlan{}, ErrPublishUnavailable
	}
	current, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return PublishPlan{}, err
	}
	if current.Status == "DISABLED" {
		return PublishPlan{}, ErrInvalidTransition
	}
	if current.Version != input.ExpectedVersion || current.DraftVersionID != input.DraftVersionID ||
		current.DraftRecordVersion != input.ExpectedDraftRecordVersion || current.DSLHash != input.ExpectedDSLHash {
		return PublishPlan{}, ErrConflict
	}
	prepared, err := Prepare(current.DSL)
	if err != nil {
		return PublishPlan{}, publicationFailure("dsl", "PUBLISH_DSL_INVALID", "当前草稿无法重新通过 DSL 校验")
	}
	if prepared.DSLHash != current.DSLHash || prepared.PlanHash != current.PlanHash {
		return PublishPlan{}, publicationFailure("dsl", "PUBLISH_DERIVATION_MISMATCH", "当前草稿与服务端派生摘要不一致")
	}
	issues := unconfirmedJoinIssues(prepared.Document)
	if len(issues) > 0 {
		return PublishPlan{}, &PublicationValidationError{Issues: issues}
	}
	result, err := s.validator.ValidatePublication(ctx, tenantID, actorID, PublicationCandidate{
		DatasetID: id, DraftVersionID: current.DraftVersionID, DraftRecordVersion: current.DraftRecordVersion,
		DSLHash: current.DSLHash, PlanHash: current.PlanHash, DSL: append(json.RawMessage(nil), current.DSL...),
		Parameters: input.ValidationParameters,
	})
	if err != nil {
		var validation *PublicationValidationError
		if errors.As(err, &validation) || errors.Is(err, ErrConflict) {
			return PublishPlan{}, err
		}
		return PublishPlan{}, publicationFailure("executionPolicy", "PUBLISH_QUERY_FAILED", "发布前查询试跑失败")
	}
	if warningIssues := publicationWarningIssues(prepared.Document, result.Warnings); len(warningIssues) > 0 {
		return PublishPlan{}, &PublicationValidationError{Issues: warningIssues}
	}
	return PublishPlan{
		IdempotencyKey: idempotencyKey, RequestHash: requestHash, ExpectedVersion: input.ExpectedVersion,
		DraftVersionID: input.DraftVersionID, ExpectedDraftRecordVersion: input.ExpectedDraftRecordVersion,
		ExpectedDSLHash: input.ExpectedDSLHash, Prepared: prepared,
	}, nil
}

// GetVersion 按父数据集和精确版本 ID 加载不可变快照，禁止回退到当前草稿或当前发布指针。
func (s *Service) GetVersion(ctx context.Context, tenantID, datasetID, versionID string) (VersionRecord, error) {
	if tenantID == "" || !canonicalUUID(datasetID) || !canonicalUUID(versionID) {
		return VersionRecord{}, ErrVersionNotFound
	}
	return s.store.GetVersion(ctx, tenantID, datasetID, versionID)
}

// ListVersions 返回发布版本目录，不混入可变草稿行。
func (s *Service) ListVersions(ctx context.Context, tenantID, datasetID string, limit, offset int) ([]VersionSummary, int, error) {
	if tenantID == "" || !canonicalUUID(datasetID) || limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidDocument
	}
	return s.store.ListVersions(ctx, tenantID, datasetID, limit, offset)
}

// GetVersionUsage 按精确版本汇总租户内引用和运行占用，不返回下游资源明细。
func (s *Service) GetVersionUsage(ctx context.Context, tenantID, datasetID, versionID string) (VersionUsage, error) {
	if tenantID == "" || !canonicalUUID(datasetID) || !canonicalUUID(versionID) {
		return VersionUsage{}, ErrVersionNotFound
	}
	return s.store.GetVersionUsage(ctx, tenantID, datasetID, versionID)
}

// TransitionVersion 执行发布版本的单向失效或废弃状态迁移。
func (s *Service) TransitionVersion(ctx context.Context, tenantID, actorID, datasetID, versionID string, input VersionTransitionInput) (VersionRecord, error) {
	input.ExpectedStatus = strings.ToUpper(strings.TrimSpace(input.ExpectedStatus))
	input.TargetStatus = strings.ToUpper(strings.TrimSpace(input.TargetStatus))
	validTransition := input.ExpectedStatus == "PUBLISHED" && (input.TargetStatus == "STALE" || input.TargetStatus == "DEPRECATED") ||
		input.ExpectedStatus == "STALE" && input.TargetStatus == "DEPRECATED"
	if tenantID == "" || actorID == "" || !canonicalUUID(datasetID) || !canonicalUUID(versionID) || input.ExpectedVersion < 1 || !validTransition {
		return VersionRecord{}, ErrInvalidTransition
	}
	return s.store.TransitionVersion(ctx, tenantID, actorID, datasetID, versionID, input)
}

// GetRevision 按父数据集和精确修订 ID 加载不可变草稿快照。
func (s *Service) GetRevision(ctx context.Context, tenantID, datasetID, revisionID string) (RevisionRecord, error) {
	if tenantID == "" || !canonicalUUID(datasetID) || !canonicalUUID(revisionID) {
		return RevisionRecord{}, ErrRevisionNotFound
	}
	return s.store.GetRevision(ctx, tenantID, datasetID, revisionID)
}

// ListRevisions 返回创建、保存和回滚产生的草稿历史，不混入发布或生命周期操作。
func (s *Service) ListRevisions(ctx context.Context, tenantID, datasetID string, limit, offset int) ([]RevisionSummary, int, error) {
	if tenantID == "" || !canonicalUUID(datasetID) || limit < 1 || limit > 200 || offset < 0 {
		return nil, 0, ErrInvalidDocument
	}
	return s.store.ListRevisions(ctx, tenantID, datasetID, limit, offset)
}

// RollbackRevision 把历史修订复制为新的当前草稿修订；历史快照和当前发布指针
// 均保持不变，后续仍需通过正常发布流程生成新的不可变发布版本。
func (s *Service) RollbackRevision(ctx context.Context, tenantID, actorID, datasetID, revisionID string, input RollbackRevisionInput) (Record, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(datasetID) || !canonicalUUID(revisionID) || input.ExpectedVersion < 1 {
		return Record{}, ErrInvalidDocument
	}
	revision, err := s.store.GetRevision(ctx, tenantID, datasetID, revisionID)
	if err != nil {
		return Record{}, err
	}
	prepared, err := prepareRollbackRevision(revision)
	if err != nil {
		return Record{}, err
	}
	return s.store.RollbackRevision(ctx, tenantID, actorID, datasetID, input, revision, prepared)
}

// RollbackVersion 仅在发布版本能够唯一映射到内容完全一致的源草稿修订时执行回滚。
// 遗留缺失或重复来源都必须失败关闭，禁止按哈希、序号或最近修订进行猜测。
func (s *Service) RollbackVersion(ctx context.Context, tenantID, actorID, datasetID, versionID string, input RollbackRevisionInput) (Record, error) {
	if tenantID == "" || actorID == "" || !canonicalUUID(datasetID) || !canonicalUUID(versionID) || input.ExpectedVersion < 1 {
		return Record{}, ErrInvalidDocument
	}
	revision, err := s.store.ResolveVersionSourceRevision(ctx, tenantID, datasetID, versionID)
	if err != nil {
		return Record{}, err
	}
	prepared, err := prepareRollbackRevision(revision)
	if err != nil {
		return Record{}, fmt.Errorf("%w: source revision integrity check failed", ErrVersionRollbackUnavailable)
	}
	return s.store.RollbackRevision(ctx, tenantID, actorID, datasetID, input, revision, prepared)
}

func prepareRollbackRevision(revision RevisionRecord) (Prepared, error) {
	prepared, err := Prepare(revision.DSL)
	if err != nil {
		return Prepared{}, err
	}
	if prepared.DSLHash != revision.DSLHash || prepared.PlanHash != revision.PlanHash ||
		prepared.Document.Dataset.Name != revision.Name || prepared.Document.Dataset.Description != revision.Description ||
		prepared.Document.Dataset.Type != revision.Type || prepared.Document.DSLVersion != revision.DSLVersion {
		return Prepared{}, fmt.Errorf("%w: stored revision derivation mismatch", ErrInvalidDocument)
	}
	return prepared, nil
}

func publicationRequestHash(datasetID string, input PublishInput) (string, error) {
	raw, err := json.Marshal(struct {
		DatasetID string `json:"datasetId"`
		PublishInput
	}{DatasetID: datasetID, PublishInput: input})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func unconfirmedJoinIssues(document Document) []PublicationIssue {
	issues := []PublicationIssue{}
	for index, join := range document.Joins {
		if !join.ManualConfirmed {
			issues = append(issues, PublicationIssue{Path: fmt.Sprintf("joins[%d].manualConfirmed", index), Code: "JOIN_CONFIRMATION_REQUIRED", Reason: "发布前必须确认 Join 字段和连接方式"})
		}
	}
	return issues
}

func publicationWarningIssues(document Document, warnings []PreviewWarning) []PublicationIssue {
	if len(warnings) == 0 {
		return nil
	}
	joinIndex := make(map[string]int, len(document.Joins))
	for index, join := range document.Joins {
		joinIndex[join.ID] = index
	}
	issues := make([]PublicationIssue, 0, len(warnings))
	for _, warning := range warnings {
		path := "joins"
		if index, exists := joinIndex[warning.JoinID]; exists {
			path = fmt.Sprintf("joins[%d]", index)
		}
		code := strings.TrimSpace(warning.Code)
		if code == "" {
			code = "JOIN_RISK_DETECTED"
		}
		issues = append(issues, PublicationIssue{Path: path, Code: code, Reason: "发布试跑检测到未解决的 Join 风险"})
	}
	return issues
}

func publicationFailure(path, code, reason string) error {
	return &PublicationValidationError{Issues: []PublicationIssue{{Path: path, Code: code, Reason: reason}}}
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}

func validHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validIdempotencyKey(value string) bool {
	if len(value) < 1 || len(value) > 128 || strings.TrimSpace(value) != value {
		return false
	}
	// 与数据库 CHECK 保持一致，避免先执行发布试跑后才因控制字符失败。
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
