package datasource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Service struct {
	repo            Repository
	connectors      map[Type]Connector
	completer       TableCompleter
	jobs            MetadataJobRepository
	connectionTests ConnectionTestJobRepository
	now             func() time.Time
	testTTL         time.Duration
}

// SetTableCompleter 注入 LLM 元数据完善器，保持数据源领域对具体 AI 实现解耦。
func (s *Service) SetTableCompleter(completer TableCompleter) { s.completer = completer }

// SetMetadataJobRepository 注入持久化任务仓储，使 HTTP 只负责入队、worker 负责采样和完善。
func (s *Service) SetMetadataJobRepository(jobs MetadataJobRepository) { s.jobs = jobs }

// SetConnectionTestJobRepository 注入异步连接测试控制面。API 只能通过该仓储
// 入队和读取任务；成功证明由使用独立数据库身份的 worker 形成。
func (s *Service) SetConnectionTestJobRepository(tests ConnectionTestJobRepository) {
	s.connectionTests = tests
}

// NewService 按数据源类型注册连接器，使业务状态机与具体数据库驱动解耦。
func NewService(repo Repository, connectors ...Connector) *Service {
	m := map[Type]Connector{}
	for _, c := range connectors {
		m[c.Type()] = c
	}
	return &Service{repo: repo, connectors: m, now: time.Now, testTTL: 30 * time.Minute}
}

// Audit 将数据源操作审计转交给仓储层持久化。
func (s *Service) Audit(ctx context.Context, tenantID, actorID, action, resourceID string, detail any) error {
	return s.repo.Audit(ctx, tenantID, actorID, action, resourceID, detail)
}

// Create 在租户配额内创建草稿状态的数据源。
func (s *Service) Create(ctx context.Context, source Source) (Source, error) {
	if source.Visibility == "" {
		source.Visibility = VisibilityPrivate
	}
	if err := source.Validate(); err != nil {
		return Source{}, fmt.Errorf("%w: %v", ErrInvalidConfiguration, err)
	}
	quota, err := s.repo.Quota(ctx, source.TenantID)
	if err != nil {
		return Source{}, err
	}
	count, err := s.repo.Count(ctx, source.TenantID)
	if err != nil {
		return Source{}, err
	}
	if count >= quota.MaxDataSources {
		return Source{}, ErrQuotaExceeded
	}
	source.Status = StatusDraft
	return s.repo.Create(ctx, source)
}

// List 返回租户下未删除的数据源。
func (s *Service) List(ctx context.Context, tenantID string) ([]Source, error) {
	items, err := s.repo.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index], err = s.withPublicationReview(ctx, tenantID, items[index])
		if err != nil {
			return nil, err
		}
	}
	return items, nil
}

// Get 在租户边界内返回单个数据源，供详情和编辑基线读取。
func (s *Service) Get(ctx context.Context, tenantID, id string) (Source, error) {
	var source Source
	var err error
	if versioned, ok := s.repo.(VersionedRepository); ok {
		source, err = versioned.GetDraft(ctx, tenantID, id)
	} else {
		source, err = s.repo.Get(ctx, tenantID, id)
	}
	if err != nil {
		return Source{}, err
	}
	return s.withPublicationReview(ctx, tenantID, source)
}

// SampleTable 通过受控元数据采样接口读取少量真实数据，不接受调用方传入 SQL。
func (s *Service) SampleTable(ctx context.Context, tenantID, sourceID string, table MetadataTable, maxRows int) (SampleResult, error) {
	if maxRows < 1 || maxRows > 10 {
		return SampleResult{}, errors.New("sample row limit must be between 1 and 10")
	}
	source, connector, err := s.load(ctx, tenantID, sourceID)
	if err != nil {
		return SampleResult{}, err
	}
	sampler, ok := connector.(MetadataSampler)
	if !ok {
		return SampleResult{}, errors.New("connector does not support table sampling")
	}
	return sampler.Sample(ctx, source, table, maxRows)
}

// Update 仅允许修改非同步、非删除状态的数据源，并重置为待验证草稿。
func (s *Service) Update(ctx context.Context, source Source) (Source, error) {
	current, err := s.Get(ctx, source.TenantID, source.ID)
	if err != nil {
		return Source{}, err
	}
	if source.Version < 1 || source.Version != current.Version {
		return Source{}, ErrVersionConflict
	}
	if current.Status == StatusSyncing || current.Status == StatusDeleting || current.Status == StatusDeleted {
		return Source{}, fmt.Errorf("cannot update source in %s status", current.Status)
	}
	if current.ReviewStatus == ReviewPending {
		return Source{}, ErrReviewPending
	}
	// 版本化仓储会让旧发布配置继续服务，编辑草稿时不能提前关闭线上连接池。
	// 旧仓储仍维持原有“覆盖配置前关闭连接”的行为。
	if _, versioned := s.repo.(VersionedRepository); !versioned {
		if connector := s.connectors[current.Type]; connector != nil {
			if err := connector.Close(ctx, current); err != nil {
				return Source{}, fmt.Errorf("close current source connection: %w", err)
			}
		}
	}
	// 编辑表单不回显密码；未提交新密码时沿用当前内部引用，避免迫使浏览器获取秘密。
	if source.SecretRef == "" && (source.Type == TypeMySQL || source.Type == TypeOracle) {
		source.SecretRef = current.SecretRef
	}
	if source.OwnerID == "" {
		source.OwnerID = current.OwnerID
	}
	if source.Visibility == "" {
		source.Visibility = current.Visibility
	}
	source.CreatedBy = current.CreatedBy
	source.Status = StatusDraft
	if err := source.Validate(); err != nil {
		return Source{}, err
	}
	return s.repo.Update(ctx, source)
}

// Enable 将已禁用数据源恢复为可同步状态。
func (s *Service) Enable(ctx context.Context, tenantID, id string) error {
	if err := s.ensureReviewAllowsManagement(ctx, tenantID, id); err != nil {
		return err
	}
	source, err := s.repo.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if source.Status != StatusDisabled {
		return fmt.Errorf("cannot enable source in %s status", source.Status)
	}
	return s.repo.UpdateStatus(ctx, tenantID, id, StatusActive, "")
}

// Disable 暂停当前活跃数据源的同步能力。
func (s *Service) Disable(ctx context.Context, tenantID, id string) error {
	if err := s.ensureReviewAllowsManagement(ctx, tenantID, id); err != nil {
		return err
	}
	source, err := s.repo.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if source.Status != StatusActive {
		return fmt.Errorf("cannot disable source in %s status", source.Status)
	}
	return s.repo.UpdateStatus(ctx, tenantID, id, StatusDisabled, "")
}

// Test 调用对应连接器验证当前草稿，并把结果绑定到精确版本和配置摘要。
// 版本化仓储中的成功测试不会自动发布；旧仓储保留原状态流转以兼容测试替身和外部实现。
func (s *Service) Test(ctx context.Context, tenantID, id string) (TestResult, error) {
	source, connector, err := s.loadDraft(ctx, tenantID, id)
	if err != nil {
		return TestResult{}, err
	}
	startedAt := s.now().UTC()
	result, err := connector.Test(ctx, source)
	completedAt := s.now().UTC()
	versioned, supportsVersioning := s.repo.(VersionedRepository)
	if err != nil {
		if supportsVersioning {
			_, _ = versioned.RecordConnectionTest(ctx, tenantID, id, ConnectionTestRun{
				DataSourceID: id, ConfigVersion: source.ConfigVersionID, ConfigHash: source.ConfigHash,
				Status: ValidationFailed, ErrorMessage: "connection test failed",
				StartedAt: startedAt, CompletedAt: completedAt,
			})
		} else {
			// 旧仓储测试失败仍落为 ERROR。
			_ = s.repo.UpdateStatus(ctx, tenantID, id, StatusError, err.Error())
		}
		return TestResult{}, err
	}
	if supportsVersioning {
		expiresAt := completedAt.Add(s.testTTL)
		if _, err := versioned.RecordConnectionTest(ctx, tenantID, id, ConnectionTestRun{
			DataSourceID: id, ConfigVersion: source.ConfigVersionID, ConfigHash: source.ConfigHash,
			Status: ValidationPassed, ServerVersion: result.ServerVersion, LatencyMS: result.LatencyMS,
			StartedAt: startedAt, CompletedAt: completedAt, ExpiresAt: &expiresAt,
		}); err != nil {
			return TestResult{}, err
		}
		result.ConfigVersionID, result.ConfigHash = source.ConfigVersionID, source.ConfigHash
		result.TestedAt, result.ExpiresAt = &completedAt, &expiresAt
		return result, nil
	}
	if err := s.repo.UpdateStatus(ctx, tenantID, id, StatusActive, ""); err != nil {
		return TestResult{}, err
	}
	return result, nil
}

// QueueConnectionTest 把当前草稿的精确版本交给数据库冻结并入队。幂等键只保存
// SHA-256 摘要，避免把调用方可能携带业务信息的原值写入控制库。
func (s *Service) QueueConnectionTest(
	ctx context.Context,
	tenantID, actorID, id, idempotencyKey string,
) (ConnectionTestJob, error) {
	if s.connectionTests == nil {
		return ConnectionTestJob{}, ErrConnectionTestQueueUnavailable
	}
	if err := s.ensureReviewAllowsManagement(ctx, tenantID, id); err != nil {
		return ConnectionTestJob{}, err
	}
	if len(idempotencyKey) > 256 {
		return ConnectionTestJob{}, ErrIdempotencyKeyInvalid
	}
	idempotencyKeyHash := ""
	if idempotencyKey != "" {
		sum := sha256.Sum256([]byte(idempotencyKey))
		idempotencyKeyHash = hex.EncodeToString(sum[:])
	}
	return s.connectionTests.EnqueueConnectionTest(
		ctx, tenantID, id, actorID, idempotencyKeyHash,
	)
}

// GetConnectionTest 返回租户和数据源边界内的安全任务快照。
func (s *Service) GetConnectionTest(
	ctx context.Context,
	tenantID, sourceID, jobID string,
) (ConnectionTestJob, error) {
	if s.connectionTests == nil {
		return ConnectionTestJob{}, ErrConnectionTestQueueUnavailable
	}
	return s.connectionTests.GetConnectionTest(ctx, tenantID, sourceID, jobID)
}

// Publish 将当前草稿切换为运行时版本。只有同版本、同摘要且未过期的成功测试可发布。
func (s *Service) Publish(ctx context.Context, tenantID, actorID, id string) (Source, error) {
	versioned, ok := s.repo.(VersionedRepository)
	if !ok {
		return Source{}, ErrVersioningRequired
	}
	draft, err := versioned.GetDraft(ctx, tenantID, id)
	if err != nil {
		return Source{}, err
	}
	if draft.ConfigVersionID == "" || draft.ConfigHash == "" {
		return Source{}, ErrSourceVersionChanged
	}
	if draft.PublicationStatus == PublicationPublished && draft.PublishedVersionID == draft.ConfigVersionID {
		return draft, nil
	}
	now := s.now().UTC()
	if s.connectionTests != nil {
		latest, latestErr := s.connectionTests.LatestConnectionTest(
			ctx, tenantID, id, draft.ConfigVersionID, draft.ConfigHash,
		)
		if latestErr != nil {
			return Source{}, latestErr
		}
		if latest != nil {
			switch latest.Status {
			case ConnectionTestQueued, ConnectionTestRunning:
				return Source{}, ErrConnectionTestPending
			case ConnectionTestFailed:
				return Source{}, ErrConnectionTestFailed
			case ConnectionTestCancelled:
				return Source{}, ErrSourceVersionChanged
			}
		}
	}
	if draft.ValidationStatus != ValidationPassed {
		return Source{}, ErrTestRequired
	}
	if draft.TestExpiresAt == nil || !draft.TestExpiresAt.After(now) {
		return Source{}, ErrTestExpired
	}
	// 发布切换前关闭旧发布版本的连接池；失败时旧指针未变，后续请求可按旧快照重建。
	if draft.PublishedVersionID != "" && draft.PublishedVersionID != draft.ConfigVersionID {
		current, err := s.repo.Get(ctx, tenantID, id)
		if err != nil {
			return Source{}, err
		}
		if connector := s.connectors[current.Type]; connector != nil {
			if err := connector.Close(ctx, current); err != nil {
				return Source{}, fmt.Errorf("close published source connection: %w", err)
			}
		}
	}
	return versioned.Publish(ctx, tenantID, id, actorID, draft.ConfigVersionID, draft.ConfigHash, now)
}

// Sync 执行元数据采集、事务入库和状态回写的完整流程。
func (s *Service) Sync(ctx context.Context, tenantID, id string) (SyncResult, error) {
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return SyncResult{}, err
	}
	if source.Status != StatusActive {
		return SyncResult{}, fmt.Errorf("cannot sync source in %s status", source.Status)
	}
	if err := s.repo.UpdateStatus(ctx, tenantID, id, StatusSyncing, ""); err != nil {
		return SyncResult{}, err
	}
	result, err := connector.Sync(ctx, source)
	if err != nil {
		_ = s.repo.UpdateStatus(ctx, tenantID, id, StatusError, err.Error())
		return SyncResult{}, err
	}
	if err := s.repo.ApplyMetadata(ctx, source, result); err != nil {
		_ = s.repo.UpdateStatus(ctx, tenantID, id, StatusError, err.Error())
		return SyncResult{}, err
	}
	// 表明细只在服务内部用于入库，不随同步响应返回，避免大元数据快照占用接口带宽。
	result.Tables = nil
	return result, s.repo.UpdateStatus(ctx, tenantID, id, StatusActive, "")
}

// DiscoverTables 只读取源库技术元数据，不写入 PostgreSQL 资产表。
func (s *Service) DiscoverTables(ctx context.Context, tenantID, id string) (SyncResult, error) {
	if err := s.ensureReviewAllowsTableConfiguration(ctx, tenantID, id); err != nil {
		return SyncResult{}, err
	}
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return SyncResult{}, err
	}
	if source.Status != StatusActive {
		return SyncResult{}, fmt.Errorf("cannot discover source in %s status", source.Status)
	}
	return connector.Sync(ctx, source)
}

// InspectFileSource 在“新增数据表”阶段解析文件结构并固化当前版本的解析方案。
// 新建文件数据源只负责上传和登记，不提前读取 Sheet 或产生映射任务。
func (s *Service) InspectFileSource(ctx context.Context, tenantID, id string) (ExcelWorkbookInspection, error) {
	if err := s.ensureReviewAllowsTableConfiguration(ctx, tenantID, id); err != nil {
		return ExcelWorkbookInspection{}, err
	}
	// Sheet 解析属于运行面操作，必须固定到已发布配置。已发布数据源重新
	// 上传文件后，新草稿在上线前不能通过此接口提前解析或映射。
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return ExcelWorkbookInspection{}, err
	}
	if source.Status != StatusActive {
		return ExcelWorkbookInspection{}, fmt.Errorf("cannot inspect file source in %s status", source.Status)
	}
	inspector, ok := connector.(FileStructureInspector)
	if !ok {
		return ExcelWorkbookInspection{}, errors.New("data source does not support file structure inspection")
	}
	return inspector.Inspect(ctx, source)
}

// ImportTables 是兼容旧调用方的同步入口，只发送技术元数据，不读取业务样本。
// 需要样本时必须使用带逐任务授权的 QueueImportTablesWithSampleMode。
func (s *Service) ImportTables(ctx context.Context, tenantID, actorID, id string, selections []TableSelection) ([]ImportedTable, error) {
	if len(selections) == 0 || len(selections) > 100 {
		return nil, errors.New("between 1 and 100 tables must be selected")
	}
	return s.importTables(ctx, tenantID, actorID, id, selections)
}

// RefreshTables 是兼容旧调用方的同步入口，只发送技术元数据；单表失败不阻断后续表。
func (s *Service) RefreshTables(ctx context.Context, tenantID, actorID, id string) (TableRefreshResult, error) {
	result := TableRefreshResult{Items: []TableRefreshItem{}}
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return result, err
	}
	if source.Status != StatusActive {
		return result, fmt.Errorf("cannot refresh tables from source in %s status", source.Status)
	}
	if s.completer == nil {
		return result, errors.New("metadata AI completer is not configured")
	}
	selections, err := s.repo.ListActiveTableSelections(ctx, tenantID, id)
	if err != nil {
		return result, err
	}
	result.Total = len(selections)
	if len(selections) == 0 {
		result.Status = "SUCCEEDED"
		return result, nil
	}
	discovered, err := connector.Sync(ctx, source)
	if err != nil {
		return result, err
	}
	available := make(map[string]MetadataTable, len(discovered.Tables))
	for _, table := range discovered.Tables {
		available[metadataTableKey(table)] = table
	}
	for _, selection := range selections {
		item := TableRefreshItem{TableName: selection.TableName, Status: "FAILED", Stage: "DISCOVERY"}
		key := selection.CatalogName + "\x1f" + selection.SchemaName + "\x1f" + selection.TableName
		table, exists := available[key]
		if !exists {
			item.Code = "SOURCE_TABLE_NOT_FOUND"
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}
		metadata, err := selectedMetadataResult([]MetadataTable{table})
		if err != nil {
			item.Stage, item.Code, item.Cause = "PERSISTENCE", "METADATA_BUILD_FAILED", err
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}
		ids, err := s.repo.ApplySelectedMetadata(ctx, source, metadata)
		if err != nil {
			item.Stage, item.Code, item.Cause = "PERSISTENCE", "METADATA_UPDATE_FAILED", err
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}
		item.ID = ids[key]
		result.TechnicalUpdated++
		structureHash, _, err := metadataTableHash(table)
		if err != nil {
			item.Stage, item.Code, item.Cause = "LLM", "STRUCTURE_HASH_FAILED", err
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}
		if err := s.completer.CompleteTable(ctx, tenantID, actorID, item.ID, nil, true, nil, structureHash, "", "", 0); err != nil {
			item.Stage, item.Code, item.Cause = "LLM", "LLM_COMPLETION_FAILED", err
			result.Items = append(result.Items, item)
			result.Failed++
			continue
		}
		item.Status, item.Stage = "SUCCEEDED", "COMPLETE"
		result.Items = append(result.Items, item)
		result.Succeeded++
	}
	switch {
	case result.Failed == 0:
		result.Status = "SUCCEEDED"
	case result.Succeeded == 0:
		result.Status = "FAILED"
	default:
		result.Status = "PARTIAL"
	}
	return result, nil
}

func (s *Service) importTables(ctx context.Context, tenantID, actorID, id string, selections []TableSelection) ([]ImportedTable, error) {
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if source.Status != StatusActive {
		return nil, fmt.Errorf("cannot import tables from source in %s status", source.Status)
	}
	if s.completer == nil {
		return nil, errors.New("metadata AI completer is not configured")
	}
	discovered, err := connector.Sync(ctx, source)
	if err != nil {
		return nil, err
	}
	available := make(map[string]MetadataTable, len(discovered.Tables))
	for _, table := range discovered.Tables {
		available[metadataTableKey(table)] = table
	}
	selected := make([]MetadataTable, 0, len(selections))
	seen := map[string]bool{}
	for _, selection := range selections {
		key := selection.CatalogName + "\x1f" + selection.SchemaName + "\x1f" + selection.TableName
		table, exists := available[key]
		if !exists || seen[key] {
			return nil, errors.New("selected table is invalid or duplicated")
		}
		seen[key] = true
		selected = append(selected, table)
	}
	result, err := selectedMetadataResult(selected)
	if err != nil {
		return nil, err
	}
	ids, err := s.repo.ApplySelectedMetadata(ctx, source, result)
	if err != nil {
		return nil, err
	}
	imported := make([]ImportedTable, 0, len(selected))
	for _, table := range selected {
		tableID := ids[metadataTableKey(table)]
		structureHash, _, err := metadataTableHash(table)
		if err != nil {
			return nil, fmt.Errorf("hash metadata for table %s: %w", table.Name, err)
		}
		if err := s.completer.CompleteTable(ctx, tenantID, actorID, tableID, nil, true, nil, structureHash, "", "", 0); err != nil {
			return nil, fmt.Errorf("complete metadata for table %s: %w", table.Name, err)
		}
		imported = append(imported, ImportedTable{ID: tableID, Table: table})
	}
	return imported, nil
}

func selectedMetadataResult(tables []MetadataTable) (SyncResult, error) {
	payload, err := json.Marshal(tables)
	if err != nil {
		return SyncResult{}, err
	}
	hash := sha256.Sum256(payload)
	return SyncResult{Assets: len(tables), Watermark: time.Now().UTC().Format(time.RFC3339Nano), SnapshotHash: hex.EncodeToString(hash[:]), Tables: tables}, nil
}

func sampleRows(sample SampleResult) []map[string]any {
	rows := make([]map[string]any, 0, len(sample.Rows))
	for _, values := range sample.Rows {
		row := make(map[string]any, len(sample.Columns))
		for index, column := range sample.Columns {
			if index < len(values) {
				row[column] = values[index]
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// sampleRowsForColumns 在字段级增量中只保留本次 LLM 目标，避免未变化字段样本继续出境。
func sampleRowsForColumns(sample SampleResult, columns []string) []map[string]any {
	if columns == nil {
		return sampleRows(sample)
	}
	allowed := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		allowed[column] = struct{}{}
	}
	rows := make([]map[string]any, 0, len(sample.Rows))
	for _, values := range sample.Rows {
		row := make(map[string]any, len(allowed))
		for index, column := range sample.Columns {
			if _, exists := allowed[column]; !exists || index >= len(values) {
				continue
			}
			row[column] = values[index]
		}
		rows = append(rows, row)
	}
	return rows
}

// Delete 先关闭连接器资源，再将数据源软删除。
func (s *Service) Delete(ctx context.Context, tenantID, id string) error {
	if err := s.ensureReviewAllowsManagement(ctx, tenantID, id); err != nil {
		return err
	}
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if err := s.repo.UpdateStatus(ctx, tenantID, id, StatusDeleting, ""); err != nil {
		return err
	}
	if err := connector.Close(ctx, source); err != nil {
		_ = s.repo.UpdateStatus(ctx, tenantID, id, StatusError, err.Error())
		return err
	}
	return s.repo.UpdateStatus(ctx, tenantID, id, StatusDeleted, "")
}

func (s *Service) withPublicationReview(ctx context.Context, tenantID string, source Source) (Source, error) {
	source.ReviewStatus = ReviewNotSubmitted
	store, ok := s.repo.(interface {
		LatestPublicationRequest(context.Context, string, string) (*PublicationRequest, error)
	})
	if !ok {
		return source, nil
	}
	request, err := store.LatestPublicationRequest(ctx, tenantID, source.ID)
	if err != nil {
		return Source{}, err
	}
	return applyPublicationReview(source, request), nil
}

func (s *Service) ensureReviewAllowsManagement(ctx context.Context, tenantID, sourceID string) error {
	source, err := s.Get(ctx, tenantID, sourceID)
	if err != nil {
		return err
	}
	if source.ReviewStatus == ReviewPending {
		return ErrReviewPending
	}
	return nil
}

func (s *Service) ensureReviewAllowsTableConfiguration(ctx context.Context, tenantID, sourceID string) error {
	source, err := s.Get(ctx, tenantID, sourceID)
	if err != nil {
		return err
	}
	switch source.ReviewStatus {
	case ReviewPending:
		return ErrReviewPending
	case ReviewRejected:
		return ErrReviewRejected
	default:
		return nil
	}
}

// load 加载数据源、匹配连接器并注入租户运行配额。
func (s *Service) load(ctx context.Context, tenantID, id string) (Source, Connector, error) {
	source, err := s.repo.Get(ctx, tenantID, id)
	if err != nil {
		return Source{}, nil, err
	}
	connector := s.connectors[source.Type]
	if connector == nil {
		return Source{}, nil, errors.New("connector is not registered")
	}
	quota, err := s.repo.Quota(ctx, tenantID)
	if err != nil {
		return Source{}, nil, err
	}
	source.RuntimeQuota = quota
	return source, connector, nil
}

// loadDraft 仅用于连接测试等管理面操作；运行时 load 始终优先使用已发布快照。
func (s *Service) loadDraft(ctx context.Context, tenantID, id string) (Source, Connector, error) {
	source, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return Source{}, nil, err
	}
	connector := s.connectors[source.Type]
	if connector == nil {
		return Source{}, nil, errors.New("connector is not registered")
	}
	quota, err := s.repo.Quota(ctx, tenantID)
	if err != nil {
		return Source{}, nil, err
	}
	source.RuntimeQuota = quota
	return source, connector, nil
}
