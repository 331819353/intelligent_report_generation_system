package dataset

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Store 定义数据集草稿的租户内持久化边界。
type Store interface {
	Create(context.Context, string, string, CreateInput, Prepared) (Record, error)
	Get(context.Context, string, string) (Record, error)
	List(context.Context, string, int, int) ([]Summary, int, error)
	Update(context.Context, string, string, string, UpdateInput, Prepared) (Record, error)
}

// Service 编排 DSL 校验、规范化、逻辑计划生成和草稿持久化。
type Service struct{ store Store }

// NewService 创建数据集领域服务。
func NewService(store Store) *Service { return &Service{store: store} }

// Validate 仅校验 DSL 并返回规范结构和逻辑计划，不产生持久化副作用。
func (s *Service) Validate(raw []byte) (Prepared, error) { return Prepare(raw) }

// Create 创建首个可编辑草稿，DSL 中的基本信息必须与外层请求一致。
func (s *Service) Create(ctx context.Context, tenantID, actorID string, input CreateInput) (Record, error) {
	if tenantID == "" || actorID == "" {
		return Record{}, errors.New("tenant and actor are required")
	}
	input.Code, input.Name = strings.TrimSpace(input.Code), strings.TrimSpace(input.Name)
	input.Description, input.Type = strings.TrimSpace(input.Description), strings.ToUpper(strings.TrimSpace(input.Type))
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
	if current.Code != prepared.Document.Dataset.Code || current.Type != prepared.Document.Dataset.Type {
		return Record{}, fmt.Errorf("%w: dataset code and type cannot be changed through draft update", ErrInvalidDocument)
	}
	return s.store.Update(ctx, tenantID, actorID, id, input, prepared)
}
