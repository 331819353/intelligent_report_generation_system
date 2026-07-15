package queryruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
)

// Service 统一编排白名单解析、权限注入、受控执行、超时取消和审计。
type Service struct {
	datasets   DatasetStore
	sources    SourceStore
	policies   PolicyStore
	store      RuntimeStore
	connectors map[datasource.Type]QueryConnector
	files      FileExecutor
	federated  FederatedExecutor
	mu         sync.Mutex
	active     map[string]activeQuery
}

// SetFederatedExecutor 在进程启动装配阶段注册跨源执行器。
func (s *Service) SetFederatedExecutor(executor FederatedExecutor) { s.federated = executor }

// NewService 创建安全查询运行时；文件执行器是可选依赖，未注册时拒绝 Excel/CSV 预览。
func NewService(datasets DatasetStore, sources SourceStore, policies PolicyStore, store RuntimeStore, connectors map[datasource.Type]QueryConnector, fileExecutors ...FileExecutor) *Service {
	service := &Service{datasets: datasets, sources: sources, policies: policies, store: store, connectors: connectors, active: map[string]activeQuery{}}
	if len(fileExecutors) > 0 {
		service.files = fileExecutors[0]
	}
	return service
}

// Preview 执行当前草稿的小样本查询，响应和审计都不会返回或保存 SQL、参数明文或结果样本。
func (s *Service) Preview(ctx context.Context, tenantID, actorID, datasetID string, input dataset.PreviewInput) (dataset.PreviewResult, error) {
	if tenantID == "" || actorID == "" || datasetID == "" {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	record, err := s.datasets.Get(ctx, tenantID, datasetID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	document, err := dataset.DecodeAndNormalize(record.DSL)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrInvalidDocument
	}
	// DSL 中只有资产 ID；每次执行都从控制库重新解析物理白名单并加载当前策略，
	// 因而草稿不能缓存旧表名或旧权限来绕过撤权。
	resolved, err := s.store.Resolve(ctx, tenantID, document)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	quota, err := s.sources.Quota(ctx, tenantID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	sources, err := s.loadSources(ctx, tenantID, resolved, quota)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	scope, rowPolicies, columnPolicies, err := s.policies.Load(ctx, tenantID, actorID, "DATASET", datasetID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	maxRows := input.MaxRows
	if maxRows == 0 {
		maxRows = min(document.ExecutionPolicy.PreviewLimit, 100)
	}
	if maxRows < 1 || maxRows > document.ExecutionPolicy.PreviewLimit {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	queryID, err := queryIdentifier(input.QueryID)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}

	// 数据库和文件路径共用同一审计记录与生命周期；分支只负责生成各自的执行计划。
	baseRun := RunRecord{ID: queryID, TenantID: tenantID, DatasetID: datasetID, DatasetVersionID: record.DraftVersionID, ActorID: actorID, SourceID: resolved.SourceID}
	baseRun.Sources = resolvedRunSources(queryID, document.Dataset.Type, resolved)
	if document.Dataset.Type == "CROSS_SOURCE" {
		return s.previewFederated(ctx, sources, record, document, resolved, input.Parameters, scope, rowPolicies, columnPolicies, maxRows, baseRun)
	}
	source, ok := sources[resolved.SourceID]
	if !ok {
		return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
	}
	if resolved.SourceType == datasource.TypeExcel {
		return s.previewFile(ctx, source, record, document, resolved, input.Parameters, scope, rowPolicies, columnPolicies, maxRows, baseRun)
	}
	return s.previewDatabase(ctx, source, document, resolved, input.Parameters, scope, rowPolicies, columnPolicies, maxRows, baseRun)
}

func (s *Service) loadSources(ctx context.Context, tenantID string, resolved ResolvedPlan, quota datasource.Quota) (map[string]datasource.Source, error) {
	ids := map[string]datasource.Type{}
	for _, node := range resolved.Nodes {
		ids[node.SourceID] = node.SourceType
	}
	if len(ids) == 0 && resolved.SourceID != "" {
		ids[resolved.SourceID] = resolved.SourceType
	}
	result := make(map[string]datasource.Source, len(ids))
	for id, expectedType := range ids {
		source, err := s.sources.Get(ctx, tenantID, id)
		if err != nil {
			return nil, err
		}
		if source.Status != datasource.StatusActive || source.Type != expectedType {
			return nil, dataset.ErrPreviewUnsupported
		}
		source.RuntimeQuota = quota
		result[id] = source
	}
	return result, nil
}

func (s *Service) previewDatabase(ctx context.Context, source datasource.Source, document dataset.Document, resolved ResolvedPlan, parameters map[string]any, scope policy.UserScope, rowPolicies []policy.RowPolicy, columnPolicies []policy.ColumnPolicy, maxRows int, run RunRecord) (dataset.PreviewResult, error) {
	connector := s.connectors[resolved.SourceType]
	if connector == nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
	}
	dialect := querycompiler.MySQL
	if resolved.SourceType == datasource.TypeOracle {
		dialect = querycompiler.Oracle
	}
	compiled, err := querycompiler.Compile(querycompiler.Input{
		Document: document, Dialect: dialect, Tables: resolved.Tables, Parameters: parameters,
		Scope: scope, RowPolicies: rowPolicies, ColumnPolicies: columnPolicies, MaxRows: maxRows,
	})
	if err != nil {
		return dataset.PreviewResult{}, fmt.Errorf("%w: %v", dataset.ErrPreviewInvalid, err)
	}
	// 审计只保存计划和绑定摘要，不保存可还原的 SQL、参数明文或结果样本。
	parameterJSON, err := json.Marshal(compiled.Args)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	run.PlanHash, run.ParameterHash = hash([]byte(compiled.SQL)), hash(parameterJSON)
	return s.execute(ctx, document, run, compiled.MaxRows, connector, func(queryContext context.Context) (datasource.QueryResult, error) {
		return connector.Query(queryContext, source, run.ID, compiled.SQL, compiled.Args, compiled.MaxRows)
	})
}

func (s *Service) previewFile(ctx context.Context, source datasource.Source, record dataset.Record, document dataset.Document, resolved ResolvedPlan, parameters map[string]any, scope policy.UserScope, rowPolicies []policy.RowPolicy, columnPolicies []policy.ColumnPolicy, maxRows int, run RunRecord) (dataset.PreviewResult, error) {
	if s.files == nil || resolved.FileVersionID == "" {
		return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
	}
	normalized, err := querycompiler.NormalizeParameters(document.Parameters, parameters)
	if err != nil {
		return dataset.PreviewResult{}, fmt.Errorf("%w: %v", dataset.ErrPreviewInvalid, err)
	}
	// 文件查询没有 SQL 可哈希，因此计划摘要包含固定文件版本、物理白名单和当前
	// 权限规则；绑定摘要包含参数及策略属性值。两者共同标识一次可复现执行。
	planJSON, err := json.Marshal(struct {
		DatasetPlan string
		FileVersion string
		Tables      map[string]querycompiler.TableRef
		Rows        []policy.RowPolicy
		Columns     []policy.ColumnPolicy
	}{record.PlanHash, resolved.FileVersionID, resolved.Tables, rowPolicies, columnPolicies})
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	bindingJSON, err := json.Marshal(struct {
		Parameters map[string]any
		Scope      policy.UserScope
	}{normalized, scope})
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	run.PlanHash, run.ParameterHash = hash(planJSON), hash(bindingJSON)
	return s.execute(ctx, document, run, maxRows, s.files, func(queryContext context.Context) (datasource.QueryResult, error) {
		return s.files.Execute(queryContext, source, run.ID, document, resolved.Tables, resolved.FileVersionID, normalized, scope, rowPolicies, columnPolicies, maxRows)
	})
}

func (s *Service) previewFederated(ctx context.Context, sources map[string]datasource.Source, record dataset.Record, document dataset.Document, resolved ResolvedPlan, parameters map[string]any, scope policy.UserScope, rowPolicies []policy.RowPolicy, columnPolicies []policy.ColumnPolicy, maxRows int, run RunRecord) (dataset.PreviewResult, error) {
	if s.federated == nil || len(resolved.Nodes) < 2 {
		return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
	}
	normalized, err := querycompiler.NormalizeParameters(document.Parameters, parameters)
	if err != nil {
		return dataset.PreviewResult{}, fmt.Errorf("%w: %v", dataset.ErrPreviewInvalid, err)
	}
	planJSON, err := json.Marshal(struct {
		DatasetPlan string
		Nodes       map[string]ResolvedNode
		Rows        []policy.RowPolicy
		Columns     []policy.ColumnPolicy
	}{record.PlanHash, resolved.Nodes, rowPolicies, columnPolicies})
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	bindingJSON, err := json.Marshal(struct {
		Parameters map[string]any
		Scope      policy.UserScope
	}{normalized, scope})
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	run.PlanHash, run.ParameterHash = hash(planJSON), hash(bindingJSON)
	return s.execute(ctx, document, run, maxRows, s.federated, func(queryContext context.Context) (datasource.QueryResult, error) {
		return s.federated.Execute(queryContext, run.ID, document, resolved, sources, normalized, scope, rowPolicies, columnPolicies, maxRows)
	})
}

// execute 统一处理审计状态、25 秒预览上限、主动取消和 Connector 结果边界。
func (s *Service) execute(ctx context.Context, document dataset.Document, run RunRecord, maxRows int, canceller QueryCanceller, executeQuery func(context.Context) (datasource.QueryResult, error)) (dataset.PreviewResult, error) {
	timeout := min(time.Duration(document.ExecutionPolicy.TimeoutMS)*time.Millisecond, 25*time.Second)
	queryContext, cancel := context.WithTimeout(ctx, timeout)
	// 先登记本实例的取消句柄，再写 RUNNING 审计；一旦审计记录可被取消接口查到，
	// 即使底层 Connector 尚未登记 queryID，也能先终止本地上下文。失败路径由
	// defer 统一清理。
	s.mu.Lock()
	s.active[run.ID] = activeQuery{canceller: canceller, cancel: cancel}
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.active, run.ID)
		s.mu.Unlock()
	}()
	if err := s.store.Start(ctx, run); err != nil {
		if errors.Is(err, dataset.ErrQueryConflict) {
			return dataset.PreviewResult{}, dataset.ErrQueryConflict
		}
		return dataset.PreviewResult{}, err
	}
	started := time.Now()
	result, queryErr := executeQuery(queryContext)
	durationMS := time.Since(started).Milliseconds()
	if queryErr != nil {
		status, code := "FAILED", "QUERY_EXECUTION_FAILED"
		if errors.Is(queryContext.Err(), context.DeadlineExceeded) {
			status, code = "TIMEOUT", "QUERY_TIMEOUT"
		} else if errors.Is(queryContext.Err(), context.Canceled) {
			status, code = "CANCELLED", "QUERY_CANCELLED"
		}
		if status == "TIMEOUT" || status == "CANCELLED" {
			// context 只能停止本地等待；数据库驱动可能仍在执行，所以再以独立短
			// 上下文向远端发送取消，避免沿用已经失效的原请求上下文。
			cancelContext, cancelRequest := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = canceller.Cancel(cancelContext, run.ID)
			cancelRequest()
		}
		// 即使客户端断开也要尽力结束审计记录，防止 RUNNING 状态永久残留。
		_ = s.store.Finish(context.WithoutCancel(ctx), run.TenantID, run.ID, status, 0, durationMS, code, nil, result.SourceStats)
		if status == "TIMEOUT" {
			return dataset.PreviewResult{QueryID: run.ID}, dataset.ErrPreviewTimeout
		}
		return dataset.PreviewResult{QueryID: run.ID}, dataset.ErrPreviewFailed
	}
	// 不信任 Connector 返回的计数。结果行数不一致或突破上限时丢弃整个结果，
	// 不能把超限数据交给 HTTP 层后再截断。
	if result.RowCount != len(result.Rows) || result.RowCount > maxRows {
		_ = s.store.Finish(context.WithoutCancel(ctx), run.TenantID, run.ID, "FAILED", 0, durationMS, "INVALID_QUERY_RESULT", nil, result.SourceStats)
		return dataset.PreviewResult{QueryID: run.ID}, dataset.ErrPreviewFailed
	}
	if result.DurationMS > 0 {
		durationMS = result.DurationMS
	}
	if err := s.store.Finish(context.WithoutCancel(ctx), run.TenantID, run.ID, "SUCCEEDED", result.RowCount, durationMS, "", result.Warnings, result.SourceStats); err != nil {
		if errors.Is(err, dataset.ErrQueryNotFound) {
			return dataset.PreviewResult{QueryID: run.ID}, dataset.ErrPreviewFailed
		}
		return dataset.PreviewResult{QueryID: run.ID}, err
	}
	return dataset.PreviewResult{QueryID: run.ID, Columns: result.Columns, Rows: result.Rows, RowCount: result.RowCount, DurationMS: durationMS, Warnings: previewWarnings(result.Warnings)}, nil
}

func previewWarnings(warnings []datasource.QueryWarning) []dataset.PreviewWarning {
	if len(warnings) == 0 {
		return nil
	}
	result := make([]dataset.PreviewWarning, 0, len(warnings))
	for _, warning := range warnings {
		result = append(result, dataset.PreviewWarning{
			Code: warning.Code, Message: warning.Message, JoinID: warning.JoinID, EstimatedRows: warning.EstimatedRows,
		})
	}
	return result
}

// Cancel 只允许发起用户取消仍处于 RUNNING 状态的查询。
func (s *Service) Cancel(ctx context.Context, tenantID, actorID, datasetID, queryID string) error {
	if _, err := uuid.Parse(queryID); err != nil {
		return dataset.ErrQueryNotFound
	}
	sources, err := s.store.CancellableSources(ctx, tenantID, actorID, datasetID, queryID)
	if err != nil {
		return err
	}
	// 当前实例先取消上下文，覆盖文件执行器尚未登记查询的极短竞态窗口。
	localCancelled := false
	var localCanceller QueryCanceller
	s.mu.Lock()
	if active, ok := s.active[queryID]; ok {
		localCanceller = active.canceller
		active.cancel()
		localCancelled = true
	}
	s.mu.Unlock()
	cancelled := false
	if localCanceller != nil {
		cancelled, err = localCanceller.Cancel(ctx, queryID)
		if err != nil {
			return dataset.ErrPreviewFailed
		}
	} else {
		// 跨实例取消使用持久化子查询 ID；文件节点没有远端驱动，只能由原实例取消。
		for _, source := range sources {
			if source.SourceType == datasource.TypeExcel {
				continue
			}
			connector := s.connectors[source.SourceType]
			if connector == nil {
				continue
			}
			value, cancelErr := connector.Cancel(ctx, source.SubqueryID)
			if cancelErr != nil {
				return dataset.ErrPreviewFailed
			}
			cancelled = cancelled || value
		}
	}
	if !cancelled && !localCancelled {
		return dataset.ErrQueryNotFound
	}
	return s.store.Finish(ctx, tenantID, queryID, "CANCELLED", 0, 0, "QUERY_CANCELLED", nil, nil)
}

func resolvedRunSources(queryID, datasetType string, resolved ResolvedPlan) []RunSourceRecord {
	// 单源多表会被编译为一条 SQL，只使用主 queryID；取消时通过 query_runs
	// 的兼容来源即可定位，不能为多个节点重复登记同一个子查询 ID。
	if datasetType != "CROSS_SOURCE" {
		return nil
	}
	result := make([]RunSourceRecord, 0, len(resolved.Nodes))
	for _, node := range resolved.Nodes {
		result = append(result, RunSourceRecord{
			NodeID: node.NodeID, SourceID: node.SourceID, SourceType: node.SourceType, SubqueryID: FederatedSubqueryID(queryID, node.NodeID),
			SourceVersion: node.SourceVersion, FileVersionID: node.FileVersionID, Watermark: node.Watermark,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NodeID < result[j].NodeID })
	return result
}

func queryIdentifier(requested string) (string, error) {
	// 客户端可预生成 queryID 以便并发发起取消，但必须使用规范 UUID，避免同一
	// 标识的多种文本形式破坏 active map 和数据库唯一约束的一致性。
	if requested == "" {
		return uuid.NewString(), nil
	}
	parsed, err := uuid.Parse(requested)
	if err != nil || parsed.String() != requested {
		return "", errors.New("query ID must be a canonical UUID")
	}
	return requested, nil
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// FederatedSubqueryID 从主查询 UUID 和节点标识稳定派生远端子查询 UUID。
func FederatedSubqueryID(queryID, nodeID string) string {
	parent, err := uuid.Parse(queryID)
	if err != nil {
		return uuid.Nil.String()
	}
	return uuid.NewSHA1(parent, []byte(nodeID)).String()
}
