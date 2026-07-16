package queryruntime

import (
	"context"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

// QueryConnector 是数据库 Connector 暴露给安全查询运行时的最小能力集合。
type QueryConnector interface {
	Query(context.Context, datasource.Source, string, string, []any, int) (datasource.QueryResult, error)
	Cancel(context.Context, string) (bool, error)
}

// QueryCanceller 抽象数据库远端取消与文件进程内取消，供统一生命周期使用。
type QueryCanceller interface {
	Cancel(context.Context, string) (bool, error)
}

// FileExecutor 使用固定文件版本执行受限 DSL，不允许读取可变的当前版本。
type FileExecutor interface {
	Execute(context.Context, datasource.Source, string, dataset.Document, map[string]querycompiler.TableRef, string, map[string]any, policy.UserScope, []policy.RowPolicy, []policy.ColumnPolicy, int) (datasource.QueryResult, error)
	Cancel(context.Context, string) (bool, error)
}

// FederatedExecutor 协调多个数据库或文件源，并在网关完成跨源计算。
type FederatedExecutor interface {
	Execute(context.Context, string, dataset.Document, ResolvedPlan, map[string]datasource.Source, map[string]any, policy.UserScope, []policy.RowPolicy, []policy.ColumnPolicy, int) (datasource.QueryResult, error)
	Cancel(context.Context, string) (bool, error)
}

// DatasetStore 读取当前草稿或精确发布版本定义；精确版本的依赖复核由物理解析事务完成。
type DatasetStore interface {
	Get(context.Context, string, string) (dataset.Record, error)
	GetVersion(context.Context, string, string, string) (dataset.VersionRecord, error)
}

// SourceStore 提供源连接配置及租户运行配额。
type SourceStore interface {
	Get(context.Context, string, string) (datasource.Source, error)
	Quota(context.Context, string) (datasource.Quota, error)
}

// PolicyStore 在每次执行前加载当前用户真正适用的行列策略。
type PolicyStore interface {
	Load(context.Context, string, string, string, string) (policy.UserScope, []policy.RowPolicy, []policy.ColumnPolicy, error)
	ValidateDefinitions(context.Context, string, string, string, []string) error
}

// ResolvedPlan 是控制库白名单解析后的单数据源物理计划。
type ResolvedPlan struct {
	SourceID      string
	SourceType    datasource.Type
	FileVersionID string
	Tables        map[string]querycompiler.TableRef
	Nodes         map[string]ResolvedNode
}

// ResolvedNode 固定一个 DSL 节点的可信物理源、版本和数据水位。
type ResolvedNode struct {
	NodeID, SourceID, FileVersionID, Watermark string
	SourceType                                 datasource.Type
	SourceVersion                              int64
	Table                                      querycompiler.TableRef
}

// RunSourceRecord 保存一次查询所触达的节点来源，不包含连接凭据。
type RunSourceRecord struct {
	NodeID, SourceID, SubqueryID, FileVersionID, Watermark string
	SourceType                                             datasource.Type
	SourceVersion                                          int64
}

// RunRecord 仅保存查询摘要，不含 SQL、参数明文或结果样本。
type RunRecord struct {
	ID, TenantID, DatasetID, DatasetVersionID, ActorID, SourceID string
	MetricID, MetricVersionID                                    string
	RunType                                                      string
	PlanHash, ParameterHash                                      string
	Sources                                                      []RunSourceRecord
}

// RuntimeStore 解析物理白名单并持久化可审计的查询生命周期。
type RuntimeStore interface {
	Resolve(context.Context, string, dataset.Document) (ResolvedPlan, error)
	ResolveVersion(context.Context, string, string, string, dataset.Document) (ResolvedPlan, error)
	Start(context.Context, RunRecord) error
	Finish(context.Context, string, string, string, int, int64, string, []datasource.QueryWarning, []datasource.QuerySourceStat) error
	CancellableSources(context.Context, string, string, string, string) ([]RunSourceRecord, error)
}

type activeQuery struct {
	canceller QueryCanceller
	cancel    context.CancelFunc
}
