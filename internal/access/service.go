package access

import "context"

type Check struct {
	TenantID, UserID, ResourceType, Action, ObjectID string
}

type Store interface {
	Allowed(context.Context, Check) (bool, error)
}

type Service struct{ store Store }

// NewService 创建统一权限判定服务。
func NewService(store Store) *Service { return &Service{store: store} }

// Allowed 校验权限请求字段后委托存储层执行租户内判定。
func (s *Service) Allowed(ctx context.Context, check Check) (bool, error) {
	if check.TenantID == "" || check.UserID == "" || check.ResourceType == "" || check.Action == "" {
		return false, nil
	}
	return s.store.Allowed(ctx, check)
}
