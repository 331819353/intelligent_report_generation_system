package datasource

import (
	"context"
	"errors"
	"fmt"
)

type Service struct {
	repo       Repository
	connectors map[Type]Connector
}

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
		return Source{}, err
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
		return Source{}, errors.New("tenant data source quota exceeded")
	}
	source.Status = StatusDraft
	return s.repo.Create(ctx, source)
}

// List 返回租户下未删除的数据源。
func (s *Service) List(ctx context.Context, tenantID string) ([]Source, error) {
	return s.repo.List(ctx, tenantID)
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
