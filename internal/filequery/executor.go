package filequery

import (
	"context"
	"sync"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

// Executor 从对象存储读取固定文件版本，并在进程内执行受限 DSL。
type Executor struct {
	manager VersionReader
	mu      sync.Mutex
	active  map[string]context.CancelFunc
}

// VersionReader 隔离固定文件版本读取，便于验证版本归属与取消行为。
type VersionReader interface {
	ReadVersionTables(context.Context, string, string, int64) (datasource.FileVersion, []datasource.FileTableData, error)
}

// NewExecutor 创建可取消的 Excel/CSV 查询执行器。
func NewExecutor(manager VersionReader) *Executor {
	return &Executor{manager: manager, active: map[string]context.CancelFunc{}}
}

// Execute 校验固定版本后执行过滤、关联、聚合、排序及行列权限。
func (e *Executor) Execute(ctx context.Context, source datasource.Source, queryID string, document dataset.Document, tables map[string]querycompiler.TableRef, fileVersionID string, parameters map[string]any, scope policy.UserScope, rowPolicies []policy.RowPolicy, columnPolicies []policy.ColumnPolicy, maxRows int) (datasource.QueryResult, error) {
	started := time.Now()
	queryContext, cancel := context.WithCancel(ctx)
	e.mu.Lock()
	if _, exists := e.active[queryID]; exists {
		e.mu.Unlock()
		cancel()
		return datasource.QueryResult{}, ErrQueryAlreadyActive
	}
	e.active[queryID] = cancel
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.active, queryID)
		e.mu.Unlock()
	}()
	version, fileTables, err := e.manager.ReadVersionTables(queryContext, source.TenantID, fileVersionID, source.RuntimeQuota.MaxExcelFileBytes)
	if err != nil {
		return datasource.QueryResult{}, err
	}
	if version.ID != source.FileAssetID {
		return datasource.QueryResult{}, ErrFileVersionMismatch
	}
	result, err := Evaluate(queryContext, Input{
		Document: document, Tables: tables, FileTables: fileTables, Parameters: parameters,
		Scope: scope, RowPolicies: rowPolicies, ColumnPolicies: columnPolicies, MaxRows: maxRows,
	})
	if err != nil {
		return datasource.QueryResult{}, err
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result, nil
}

// Cancel 中止当前进程内同一查询标识对应的文件计算。
func (e *Executor) Cancel(_ context.Context, queryID string) (bool, error) {
	e.mu.Lock()
	cancel, exists := e.active[queryID]
	e.mu.Unlock()
	if !exists {
		return false, nil
	}
	cancel()
	return true, nil
}
