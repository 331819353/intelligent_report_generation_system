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
	repo       Repository
	connectors map[Type]Connector
	completer  TableCompleter
	jobs       MetadataJobRepository
}

// SetTableCompleter 注入 LLM 元数据完善器，保持数据源领域对具体 AI 实现解耦。
func (s *Service) SetTableCompleter(completer TableCompleter) { s.completer = completer }

// SetMetadataJobRepository 注入持久化任务仓储，使 HTTP 只负责入队、worker 负责采样和完善。
func (s *Service) SetMetadataJobRepository(jobs MetadataJobRepository) { s.jobs = jobs }

// NewService 按数据源类型注册连接器，使业务状态机与具体数据库驱动解耦。
func NewService(repo Repository, connectors ...Connector) *Service {
	m := map[Type]Connector{}
	for _, c := range connectors {
		m[c.Type()] = c
	}
	return &Service{repo: repo, connectors: m}
}

// Audit 将数据源操作审计转交给仓储层持久化。
func (s *Service) Audit(ctx context.Context, tenantID, actorID, action, resourceID string, detail any) error {
	return s.repo.Audit(ctx, tenantID, actorID, action, resourceID, detail)
}

// Create 在租户配额内创建草稿状态的数据源。
func (s *Service) Create(ctx context.Context, source Source) (Source, error) {
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
	return s.repo.List(ctx, tenantID)
}

// Get 在租户边界内返回单个数据源，供详情和编辑基线读取。
func (s *Service) Get(ctx context.Context, tenantID, id string) (Source, error) {
	return s.repo.Get(ctx, tenantID, id)
}

// SampleTable 通过受控元数据采样接口读取少量真实数据，不接受调用方传入 SQL。
func (s *Service) SampleTable(ctx context.Context, tenantID, sourceID string, table MetadataTable, maxRows int) (SampleResult, error) {
	if maxRows < 1 || maxRows > 5 {
		return SampleResult{}, errors.New("sample row limit must be between 1 and 5")
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
	current, err := s.repo.Get(ctx, source.TenantID, source.ID)
	if err != nil {
		return Source{}, err
	}
	if current.Status == StatusSyncing || current.Status == StatusDeleting || current.Status == StatusDeleted {
		return Source{}, fmt.Errorf("cannot update source in %s status", current.Status)
	}
	// 更新前释放旧配置对应的连接池；即使后续写入失败，下一次查询也会按当前配置安全重建。
	if connector := s.connectors[current.Type]; connector != nil {
		if err := connector.Close(ctx, current); err != nil {
			return Source{}, fmt.Errorf("close current source connection: %w", err)
		}
	}
	// 编辑表单不回显密码；未提交新密码时沿用当前内部引用，避免迫使浏览器获取秘密。
	if source.SecretRef == "" && (source.Type == TypeMySQL || source.Type == TypeOracle) {
		source.SecretRef = current.SecretRef
	}
	source.Status = StatusDraft
	if err := source.Validate(); err != nil {
		return Source{}, err
	}
	return s.repo.Update(ctx, source)
}

// Enable 将已禁用数据源恢复为可同步状态。
func (s *Service) Enable(ctx context.Context, tenantID, id string) error {
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
	source, err := s.repo.Get(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if source.Status != StatusActive {
		return fmt.Errorf("cannot disable source in %s status", source.Status)
	}
	return s.repo.UpdateStatus(ctx, tenantID, id, StatusDisabled, "")
}

// Test 调用对应连接器验证连通性，并据结果更新数据源状态。
func (s *Service) Test(ctx context.Context, tenantID, id string) (TestResult, error) {
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return TestResult{}, err
	}
	result, err := connector.Test(ctx, source)
	if err != nil {
		// 测试失败必须落为 ERROR，避免未经验证的数据源进入同步流程。
		_ = s.repo.UpdateStatus(ctx, tenantID, id, StatusError, err.Error())
		return TestResult{}, err
	}
	if err := s.repo.UpdateStatus(ctx, tenantID, id, StatusActive, ""); err != nil {
		return TestResult{}, err
	}
	return result, nil
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
	source, connector, err := s.load(ctx, tenantID, id)
	if err != nil {
		return SyncResult{}, err
	}
	if source.Status != StatusActive {
		return SyncResult{}, fmt.Errorf("cannot discover source in %s status", source.Status)
	}
	return connector.Sync(ctx, source)
}

// ImportTables 对用户选中的源表采样三行，经 LLM 完善后形成 PostgreSQL 表资产。
func (s *Service) ImportTables(ctx context.Context, tenantID, actorID, id string, selections []TableSelection) ([]ImportedTable, error) {
	if len(selections) == 0 || len(selections) > 100 {
		return nil, errors.New("between 1 and 100 tables must be selected")
	}
	return s.importTables(ctx, tenantID, actorID, id, selections)
}

// RefreshTables 按已纳管范围逐表执行采样、技术结构更新和 LLM 完善，单表失败不阻断后续表。
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
	sampler, ok := connector.(MetadataSampler)
	if !ok {
		return result, errors.New("connector does not support metadata sampling")
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
		sample, err := sampler.Sample(ctx, source, table, 3)
		if err != nil {
			item.Stage, item.Code, item.Cause = "SAMPLE", "SAMPLE_FAILED", err
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
		if err := s.completer.CompleteTable(ctx, tenantID, actorID, item.ID, sampleRows(sample), true, nil, structureHash, "", "", 0); err != nil {
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
	sampler, ok := connector.(MetadataSampler)
	if !ok {
		return nil, errors.New("connector does not support metadata sampling")
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
		sample, err := sampler.Sample(ctx, source, table, 3)
		if err != nil {
			return nil, fmt.Errorf("sample table %s: %w", table.Name, err)
		}
		rows := sampleRows(sample)
		tableID := ids[metadataTableKey(table)]
		structureHash, _, err := metadataTableHash(table)
		if err != nil {
			return nil, fmt.Errorf("hash metadata for table %s: %w", table.Name, err)
		}
		if err := s.completer.CompleteTable(ctx, tenantID, actorID, tableID, rows, true, nil, structureHash, "", "", 0); err != nil {
			return nil, fmt.Errorf("complete metadata for table %s: %w", table.Name, err)
		}
		imported = append(imported, ImportedTable{ID: tableID, Table: table, Samples: rows})
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
