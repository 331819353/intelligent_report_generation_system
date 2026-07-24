package queryruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/metric"
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
	warehouse  WarehouseExecutor
	mu         sync.Mutex
	active     map[string]activeQuery
}

// SetFederatedExecutor 在进程启动装配阶段注册跨源执行器。
func (s *Service) SetFederatedExecutor(executor FederatedExecutor) { s.federated = executor }

// SetWarehouseExecutor registers the API role's read-only PostgreSQL execution
// path for governed warehouse_published views.
func (s *Service) SetWarehouseExecutor(executor WarehouseExecutor) { s.warehouse = executor }

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
	return s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		DatasetID: record.ID, VersionID: record.DraftVersionID, PlanHash: record.PlanHash, DSL: record.DSL,
	}, input, "PREVIEW")
}

// PreviewDraft 执行已有数据集的一份完整未保存候选 DSL。持久化草稿只作为乐观锁
// 和审计身份；候选经过规范化及策略复核后直接执行，不写入数据集或修订仓储。
func (s *Service) PreviewDraft(ctx context.Context, tenantID, actorID, datasetID string, input dataset.DraftPreviewInput) (dataset.DraftPreviewResult, error) {
	if tenantID == "" || actorID == "" || datasetID == "" || input.ExpectedVersion < 1 || input.MaxRows < 0 || input.MaxRows > 5 {
		return dataset.DraftPreviewResult{}, dataset.ErrPreviewInvalid
	}
	record, err := s.datasets.Get(ctx, tenantID, datasetID)
	if err != nil {
		return dataset.DraftPreviewResult{}, err
	}
	if record.Version != input.ExpectedVersion {
		return dataset.DraftPreviewResult{}, dataset.ErrConflict
	}
	prepared, err := dataset.Prepare(input.DSL)
	if err != nil {
		return dataset.DraftPreviewResult{}, err
	}
	if record.ID != datasetID || prepared.Document.Dataset.Code != record.Code || record.DraftVersionID == "" {
		return dataset.DraftPreviewResult{}, dataset.ErrInvalidDocument
	}
	fieldCodes := make([]string, 0, len(prepared.Document.Fields))
	for _, field := range prepared.Document.Fields {
		fieldCodes = append(fieldCodes, field.Code)
	}
	if err := s.policies.ValidateDefinitions(ctx, tenantID, "DATASET", record.ID, fieldCodes); err != nil {
		// 策略内部标识不应进入响应；候选不再满足数据集策略边界时直接失败关闭。
		return dataset.DraftPreviewResult{}, dataset.ErrPreviewInvalid
	}
	maxRows := input.MaxRows
	if maxRows == 0 {
		maxRows = min(prepared.Document.ExecutionPolicy.PreviewLimit, 5)
	}
	if maxRows < 1 || maxRows > 5 {
		return dataset.DraftPreviewResult{}, dataset.ErrPreviewInvalid
	}
	parameters := input.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	result, err := s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		DatasetID: record.ID, VersionID: record.DraftVersionID,
		PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
	}, dataset.PreviewInput{
		QueryID: input.QueryID, Parameters: parameters, MaxRows: maxRows,
	}, "PREVIEW")
	if err != nil {
		return dataset.DraftPreviewResult{}, err
	}
	return dataset.DraftPreviewResult{
		PreviewResult: result, DSLHash: prepared.DSLHash,
		PlanHash: prepared.PlanHash, BaseVersion: record.Version,
	}, nil
}

const candidatePolicyObjectID = "00000000-0000-0000-0000-000000000000"

// PreviewCandidate 执行尚未保存的数据集候选。候选没有 DATASET 对象身份，因此只
// 使用当前用户属性和资产访问权限，不继承任何已保存数据集的行列策略。
func (s *Service) PreviewCandidate(ctx context.Context, tenantID, actorID string, input dataset.CandidatePreviewInput) (dataset.CandidatePreviewResult, error) {
	if tenantID == "" || actorID == "" || input.MaxRows < 0 || input.MaxRows > 5 {
		return dataset.CandidatePreviewResult{}, dataset.ErrPreviewInvalid
	}
	prepared, err := dataset.Prepare(input.DSL)
	if err != nil {
		return dataset.CandidatePreviewResult{}, err
	}
	fieldCodes := make([]string, 0, len(prepared.Document.Fields))
	for _, field := range prepared.Document.Fields {
		fieldCodes = append(fieldCodes, field.Code)
	}
	if err := s.policies.ValidateDefinitions(ctx, tenantID, "DATASET", candidatePolicyObjectID, fieldCodes); err != nil {
		return dataset.CandidatePreviewResult{}, dataset.ErrPreviewInvalid
	}
	maxRows := input.MaxRows
	if maxRows == 0 {
		maxRows = min(prepared.Document.ExecutionPolicy.PreviewLimit, 5)
	}
	if maxRows < 1 || maxRows > 5 {
		return dataset.CandidatePreviewResult{}, dataset.ErrPreviewInvalid
	}
	parameters := input.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	result, err := s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		CandidateCode: prepared.Document.Dataset.Code, PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
	}, dataset.PreviewInput{QueryID: input.QueryID, Parameters: parameters, MaxRows: maxRows}, "COMPONENT_PREVIEW")
	if err != nil {
		return dataset.CandidatePreviewResult{}, err
	}
	return dataset.CandidatePreviewResult{PreviewResult: result, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash}, nil
}

// PreviewVersion 只执行 URL 中指定的发布版本，绝不回退到当前发布指针或可变草稿。
func (s *Service) PreviewVersion(ctx context.Context, tenantID, actorID, datasetID, versionID string, input dataset.PreviewInput) (dataset.PreviewResult, error) {
	if tenantID == "" || actorID == "" || datasetID == "" || versionID == "" {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	version, err := s.datasets.GetVersion(ctx, tenantID, datasetID, versionID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	if version.Status != "PUBLISHED" {
		return dataset.PreviewResult{}, dataset.ErrVersionUnavailable
	}
	return s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		DatasetID: datasetID, VersionID: version.ID, PlanHash: version.PlanHash, DSL: version.DSL, ExactVersion: true,
	}, input, "PREVIEW")
}

// PreviewRevision 使用调用者当前权限、数据策略和仍有效的资产映射执行一份不可变
// 草稿修订。修订不是发布依赖快照，因此物理资产必须经 Resolve 重新解析，不能借用
// ResolveVersion 绕开资产撤权。历史页面固定为小样本，客户端不能提高五行上限。
func (s *Service) PreviewRevision(ctx context.Context, tenantID, actorID, datasetID, revisionID string, input dataset.PreviewInput) (dataset.PreviewResult, error) {
	if tenantID == "" || actorID == "" || datasetID == "" || revisionID == "" || input.MaxRows < 0 || input.MaxRows > 5 {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	revision, err := s.datasets.GetRevision(ctx, tenantID, datasetID, revisionID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	if revision.ID != revisionID || revision.DatasetID != datasetID || revision.DraftVersionID == "" {
		return dataset.PreviewResult{}, dataset.ErrRevisionNotFound
	}
	prepared, err := dataset.Prepare(revision.DSL)
	if err != nil || prepared.DSLHash != revision.DSLHash || prepared.PlanHash != revision.PlanHash {
		return dataset.PreviewResult{}, dataset.ErrInvalidDocument
	}
	if input.MaxRows == 0 {
		input.MaxRows = min(prepared.Document.ExecutionPolicy.PreviewLimit, 5)
	}
	return s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		DatasetID: revision.DatasetID, VersionID: revision.DraftVersionID,
		PlanHash: revision.PlanHash, DSL: prepared.DSLJSON,
	}, input, "PREVIEW")
}

// PreviewMetric 执行指标服务端派生的计划，并复核它没有扩张精确数据集版本的来源边界。
func (s *Service) PreviewMetric(ctx context.Context, tenantID, actorID string, candidate metric.QueryCandidate, input dataset.PreviewInput, validation bool) (dataset.PreviewResult, error) {
	if tenantID == "" || actorID == "" || candidate.MetricID == "" || candidate.MetricVersionID == "" ||
		candidate.DatasetID == "" || candidate.DatasetVersionID == "" || candidate.PlanHash == "" {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	version, err := s.datasets.GetVersion(ctx, tenantID, candidate.DatasetID, candidate.DatasetVersionID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	if version.Status != "PUBLISHED" {
		return dataset.PreviewResult{}, dataset.ErrVersionUnavailable
	}
	original, err := dataset.DecodeAndNormalize(version.DSL)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrVersionUnavailable
	}
	derived, err := dataset.DecodeAndNormalize(candidate.DSL)
	if err != nil || !sameMetricSourceEnvelope(original, derived) {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	materializedRoot := false
	if version.Layer == dataset.LayerDWS {
		derived, err = materializedMetricDocument(original, derived, candidate.DatasetVersionID)
		if err != nil {
			return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
		}
		materializedRoot = true
	}
	derivedDSL, err := json.Marshal(derived)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	runType := "PREVIEW"
	if validation {
		runType = "VALIDATION"
	}
	return s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		DatasetID: candidate.DatasetID, VersionID: candidate.DatasetVersionID,
		MetricID: candidate.MetricID, MetricVersionID: candidate.MetricVersionID,
		PlanHash: candidate.PlanHash, DSL: derivedDSL, ExactVersion: true,
		MetricExecution: true, MaterializedRoot: materializedRoot,
	}, input, runType)
}

// sameMetricSourceEnvelope 禁止指标派生计划增加节点、字段投影、Join、过滤、参数或执行限额。
func sameMetricSourceEnvelope(original, derived dataset.Document) bool {
	return reflect.DeepEqual(original.Dataset, derived.Dataset) &&
		reflect.DeepEqual(original.Nodes, derived.Nodes) &&
		reflect.DeepEqual(original.Joins, derived.Joins) &&
		reflect.DeepEqual(original.Filters, derived.Filters) &&
		reflect.DeepEqual(original.Parameters, derived.Parameters) &&
		reflect.DeepEqual(original.ExecutionPolicy, derived.ExecutionPolicy)
}

// ValidatePublication 校验全部启用策略后执行一行试跑，结果样本只在进程内短暂存在并立即丢弃。
func (s *Service) ValidatePublication(ctx context.Context, tenantID, actorID string, candidate dataset.PublicationCandidate) (dataset.PreviewResult, error) {
	document, err := dataset.DecodeAndNormalize(candidate.DSL)
	if err != nil {
		return dataset.PreviewResult{}, &dataset.PublicationValidationError{Issues: []dataset.PublicationIssue{{Path: "dsl", Code: "PUBLISH_DSL_INVALID", Reason: "发布草稿无法解析"}}}
	}
	// 当前基数探测只对等值键有确定语义；其他操作符必须在进入查询前按 DSL 路径失败关闭。
	for joinIndex, join := range document.Joins {
		for conditionIndex, condition := range join.Conditions {
			if condition.Operator != "EQUALS" {
				return dataset.PreviewResult{}, &dataset.PublicationValidationError{Issues: []dataset.PublicationIssue{{
					Path: fmt.Sprintf("joins[%d].conditions[%d].operator", joinIndex, conditionIndex),
					Code: "JOIN_PROBE_OPERATOR_UNSUPPORTED", Reason: "当前发布基数探测只支持等值 Join",
				}}}
			}
		}
	}
	fieldCodes := make([]string, 0, len(document.Fields))
	for _, field := range document.Fields {
		fieldCodes = append(fieldCodes, field.Code)
	}
	if err := s.policies.ValidateDefinitions(ctx, tenantID, "DATASET", candidate.DatasetID, fieldCodes); err != nil {
		return dataset.PreviewResult{}, &dataset.PublicationValidationError{Issues: []dataset.PublicationIssue{{Path: "policies", Code: "PUBLISH_POLICY_INVALID", Reason: "启用的行列策略引用了无效字段或规则"}}}
	}
	return s.previewSnapshot(ctx, tenantID, actorID, runtimeSnapshot{
		DatasetID: candidate.DatasetID, VersionID: candidate.DraftVersionID, PlanHash: candidate.PlanHash, DSL: candidate.DSL,
	}, dataset.PreviewInput{Parameters: candidate.Parameters, MaxRows: 1}, "VALIDATION")
}

type runtimeSnapshot struct {
	DatasetID        string
	VersionID        string
	CandidateCode    string
	MetricID         string
	MetricVersionID  string
	PlanHash         string
	DSL              json.RawMessage
	ExactVersion     bool
	MetricExecution  bool
	MaterializedRoot bool
}

func (s *Service) previewSnapshot(ctx context.Context, tenantID, actorID string, snapshot runtimeSnapshot, input dataset.PreviewInput, runType string) (dataset.PreviewResult, error) {
	document, err := dataset.DecodeAndNormalize(snapshot.DSL)
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrInvalidDocument
	}
	// DSL 中只有资产 ID；每次执行都从控制库重新解析物理白名单并加载当前策略，
	// 因而草稿不能缓存旧表名或旧权限来绕过撤权。
	var resolved ResolvedPlan
	if snapshot.MaterializedRoot {
		resolved, err = s.store.ResolveMaterializedVersion(
			ctx, tenantID, snapshot.DatasetID, snapshot.VersionID, document,
		)
	} else if snapshot.ExactVersion {
		resolved, err = s.store.ResolveVersion(ctx, tenantID, snapshot.DatasetID, snapshot.VersionID, document)
	} else {
		resolved, err = s.store.Resolve(ctx, tenantID, document)
	}
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	if resolved.Engine == "" {
		// Compatibility for in-process resolvers compiled before the warehouse
		// path existed. Production PostgreSQL resolution always sets this
		// explicitly.
		resolved.Engine = ExecutionSource
	}
	policyObjectID := snapshot.DatasetID
	if policyObjectID == "" {
		policyObjectID = candidatePolicyObjectID
	}
	scope, rowPolicies, columnPolicies, err := s.policies.Load(ctx, tenantID, actorID, "DATASET", policyObjectID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	// 现有策略编译层位于聚合之后；指标若直接复用会导致行策略过晚生效，
	// 因此首阶段对带数据策略的数据集失败关闭。
	if snapshot.MetricExecution && (len(rowPolicies) > 0 || len(columnPolicies) > 0) {
		return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
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
	baseRun := RunRecord{
		ID: queryID, TenantID: tenantID, DatasetID: snapshot.DatasetID, DatasetVersionID: snapshot.VersionID,
		CandidateCode: snapshot.CandidateCode,
		MetricID:      snapshot.MetricID, MetricVersionID: snapshot.MetricVersionID,
		ActorID: actorID, SourceID: resolved.SourceID, ExecutionEngine: resolved.Engine,
		RunType: runType, Materializations: append([]ResolvedMaterialization(nil), resolved.Materializations...),
	}
	if resolved.Engine == ExecutionPostgreSQL {
		return s.previewWarehouse(
			ctx, tenantID, document, resolved, input.Parameters, scope,
			rowPolicies, columnPolicies, maxRows, baseRun,
		)
	}
	quota, err := s.sources.Quota(ctx, tenantID)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	sources, err := s.loadSources(ctx, tenantID, resolved, quota)
	if err != nil {
		return dataset.PreviewResult{}, err
	}
	baseRun.Sources = resolvedRunSources(queryID, document.Dataset.Type, resolved)
	if document.Dataset.Type == "CROSS_SOURCE" {
		return s.previewFederated(ctx, sources, snapshot.PlanHash, document, resolved, input.Parameters, scope, rowPolicies, columnPolicies, maxRows, baseRun)
	}
	source, ok := sources[resolved.SourceID]
	if !ok {
		return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
	}
	if resolved.SourceType == datasource.TypeExcel {
		return s.previewFile(ctx, source, snapshot.PlanHash, document, resolved, input.Parameters, scope, rowPolicies, columnPolicies, maxRows, baseRun)
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
	probes := []querycompiler.JoinProbeQuery{}
	if run.RunType == "VALIDATION" && len(document.Joins) > 0 {
		probes, err = querycompiler.CompileJoinProbes(querycompiler.JoinProbeInput{
			Document: document, Dialect: dialect, Tables: resolved.Tables, Parameters: parameters,
		})
		if err != nil {
			return dataset.PreviewResult{}, fmt.Errorf("%w: %v", dataset.ErrPreviewInvalid, err)
		}
	}
	// 审计只保存计划和绑定摘要，不保存可还原的 SQL、参数明文或结果样本。
	if len(probes) == 0 {
		parameterJSON, marshalErr := json.Marshal(compiled.Args)
		if marshalErr != nil {
			return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
		}
		run.PlanHash, run.ParameterHash = hash([]byte(compiled.SQL)), hash(parameterJSON)
	} else {
		parameterEnvelope := make([][]any, 0, len(probes)+1)
		planEnvelope := make([]string, 0, len(probes)+1)
		for _, probe := range probes {
			parameterEnvelope = append(parameterEnvelope, probe.Query.Args)
			planEnvelope = append(planEnvelope, probe.Query.SQL)
		}
		parameterEnvelope = append(parameterEnvelope, compiled.Args)
		planEnvelope = append(planEnvelope, compiled.SQL)
		parameterJSON, marshalErr := json.Marshal(parameterEnvelope)
		if marshalErr != nil {
			return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
		}
		planJSON, marshalErr := json.Marshal(planEnvelope)
		if marshalErr != nil {
			return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
		}
		run.PlanHash, run.ParameterHash = hash(planJSON), hash(parameterJSON)
	}
	return s.execute(ctx, document, run, compiled.MaxRows, connector, func(queryContext context.Context) (datasource.QueryResult, error) {
		warnings := []datasource.QueryWarning{}
		for _, probe := range probes {
			result, queryErr := connector.Query(queryContext, source, run.ID, probe.Query.SQL, probe.Query.Args, probe.Query.MaxRows)
			if queryErr != nil {
				return datasource.QueryResult{}, queryErr
			}
			stats, decodeErr := decodeJoinProbeResult(result)
			if decodeErr != nil {
				return datasource.QueryResult{}, decodeErr
			}
			probeWarnings, warningErr := joinProbeWarnings(document, probe.JoinID, stats)
			if warningErr != nil {
				return datasource.QueryResult{}, warningErr
			}
			warnings = append(warnings, probeWarnings...)
		}
		result, queryErr := connector.Query(queryContext, source, run.ID, compiled.SQL, compiled.Args, compiled.MaxRows)
		if queryErr != nil {
			// Connector 客户端不会回传远端响应正文，因此这里记录的错误只包含安全的
			// 连接阶段或 HTTP 状态信息，便于区分密钥缺失、网络失败和源端拒绝。
			slog.ErrorContext(queryContext, "dataset preview connector query failed", "query_id", run.ID, "dataset_id", run.DatasetID, "source_id", source.ID, "error", queryErr)
			return result, queryErr
		}
		// Connector 不是风险判定信任边界；数据库发布告警只能来自本地聚合探测。
		result.Warnings = warnings
		if len(probes) > 0 {
			// 发布审计记录整个探测和试跑生命周期，不能只采用最后一条 SQL 的耗时。
			result.DurationMS = 0
		}
		return result, nil
	})
}

func (s *Service) previewWarehouse(
	ctx context.Context,
	tenantID string,
	document dataset.Document,
	resolved ResolvedPlan,
	parameters map[string]any,
	scope policy.UserScope,
	rowPolicies []policy.RowPolicy,
	columnPolicies []policy.ColumnPolicy,
	maxRows int,
	run RunRecord,
) (dataset.PreviewResult, error) {
	if s.warehouse == nil || resolved.Engine != ExecutionPostgreSQL ||
		len(resolved.Materializations) == 0 || len(resolved.Tables) == 0 {
		return dataset.PreviewResult{}, dataset.ErrPreviewUnsupported
	}
	normalized, err := querycompiler.NormalizeParameters(document.Parameters, parameters)
	if err != nil {
		return dataset.PreviewResult{}, fmt.Errorf("%w: %v", dataset.ErrPreviewInvalid, err)
	}
	// Audit the structured plan and immutable materialization identities rather
	// than retaining generated SQL or parameter values.
	planJSON, err := json.Marshal(struct {
		Document         dataset.Document
		Tables           map[string]querycompiler.TableRef
		Materializations []ResolvedMaterialization
		Rows             []policy.RowPolicy
		Columns          []policy.ColumnPolicy
	}{
		Document: document, Tables: resolved.Tables,
		Materializations: resolved.Materializations,
		Rows:             rowPolicies, Columns: columnPolicies,
	})
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	bindingJSON, err := json.Marshal(struct {
		Parameters map[string]any
		Scope      policy.UserScope
	}{Parameters: normalized, Scope: scope})
	if err != nil {
		return dataset.PreviewResult{}, dataset.ErrPreviewInvalid
	}
	run.PlanHash, run.ParameterHash = hash(planJSON), hash(bindingJSON)
	return s.execute(
		ctx, document, run, maxRows, s.warehouse,
		func(queryContext context.Context) (datasource.QueryResult, error) {
			return s.warehouse.Execute(
				queryContext, tenantID, run.ID, document, resolved, normalized,
				scope, rowPolicies, columnPolicies, maxRows,
			)
		},
	)
}

type joinProbeStats struct {
	LeftDuplicateKeys, RightDuplicateKeys     int
	LeftMaxMultiplicity, RightMaxMultiplicity int
	FanoutKeys                                int
}

var joinProbeColumns = []string{
	"left_duplicate_keys", "right_duplicate_keys", "left_max_multiplicity", "right_max_multiplicity", "fanout_keys",
}

func decodeJoinProbeResult(result datasource.QueryResult) (joinProbeStats, error) {
	if result.RowCount != 1 || len(result.Rows) != 1 || len(result.Rows[0]) != len(joinProbeColumns) || len(result.Columns) != len(joinProbeColumns) {
		return joinProbeStats{}, errors.New("join probe returned an invalid result shape")
	}
	values := make([]int, len(joinProbeColumns))
	for index, expected := range joinProbeColumns {
		if !strings.EqualFold(result.Columns[index], expected) {
			return joinProbeStats{}, errors.New("join probe returned an unexpected column")
		}
		value, err := nonNegativeProbeInteger(result.Rows[0][index])
		if err != nil {
			return joinProbeStats{}, err
		}
		values[index] = value
	}
	stats := joinProbeStats{
		LeftDuplicateKeys: values[0], RightDuplicateKeys: values[1], LeftMaxMultiplicity: values[2],
		RightMaxMultiplicity: values[3], FanoutKeys: values[4],
	}
	if (stats.LeftDuplicateKeys > 0 && stats.LeftMaxMultiplicity < 2) ||
		(stats.RightDuplicateKeys > 0 && stats.RightMaxMultiplicity < 2) ||
		(stats.LeftDuplicateKeys == 0 && stats.LeftMaxMultiplicity > 1) ||
		(stats.RightDuplicateKeys == 0 && stats.RightMaxMultiplicity > 1) ||
		stats.FanoutKeys > stats.LeftDuplicateKeys || stats.FanoutKeys > stats.RightDuplicateKeys {
		return joinProbeStats{}, errors.New("join probe returned inconsistent statistics")
	}
	return stats, nil
}

func nonNegativeProbeInteger(value any) (int, error) {
	var parsed int64
	switch number := value.(type) {
	case json.Number:
		result, err := number.Int64()
		if err != nil {
			return 0, errors.New("join probe count is not an integer")
		}
		parsed = result
	case int:
		if number < 0 {
			return 0, errors.New("join probe count is negative")
		}
		return number, nil
	case int64:
		parsed = number
	case float64:
		// float64 只接受可精确表示的整数；真实 Connector 使用 json.Number，
		// 此分支主要兼容进程内测试替身，不能默许大整数精度丢失。
		if math.IsNaN(number) || math.IsInf(number, 0) || math.Trunc(number) != number || number < 0 || number > 1<<53-1 {
			return 0, errors.New("join probe count is not a supported integer")
		}
		parsed = int64(number)
	default:
		return 0, errors.New("join probe count has an unsupported type")
	}
	if parsed < 0 || uint64(parsed) > uint64(^uint(0)>>1) {
		return 0, errors.New("join probe count is outside the supported range")
	}
	return int(parsed), nil
}

func joinProbeWarnings(document dataset.Document, joinID string, stats joinProbeStats) ([]datasource.QueryWarning, error) {
	var target *dataset.Join
	for index := range document.Joins {
		if document.Joins[index].ID == joinID {
			target = &document.Joins[index]
			break
		}
	}
	if target == nil {
		return nil, errors.New("join probe does not match the dataset document")
	}
	warnings := []datasource.QueryWarning{}
	violatedSides := []string{}
	if (target.Cardinality == "ONE_TO_ONE" || target.Cardinality == "ONE_TO_MANY") && stats.LeftDuplicateKeys > 0 {
		violatedSides = append(violatedSides, "左侧")
	}
	if (target.Cardinality == "ONE_TO_ONE" || target.Cardinality == "MANY_TO_ONE") && stats.RightDuplicateKeys > 0 {
		violatedSides = append(violatedSides, "右侧")
	}
	if len(violatedSides) > 0 {
		warnings = append(warnings, datasource.QueryWarning{
			Code: "JOIN_CARDINALITY_MISMATCH", Message: "声明的 " + target.Cardinality + " 基数与实际数据不一致：" + strings.Join(violatedSides, "和") + " Join 键存在重复。", JoinID: target.ID,
		})
	}
	if target.Cardinality == "MANY_TO_MANY" {
		warnings = append(warnings, datasource.QueryWarning{
			Code: "JOIN_MANY_TO_MANY", Message: "多对多关联可能重复累计度量，请确认输出粒度或先聚合再关联。", JoinID: target.ID,
		})
	}
	if stats.FanoutKeys > 0 {
		warnings = append(warnings, datasource.QueryWarning{
			Code: "JOIN_FANOUT_RISK", Message: "检测到两侧重复 Join 键，关联结果可能发生扇出。", JoinID: target.ID,
		})
	}
	return warnings, nil
}

func (s *Service) previewFile(ctx context.Context, source datasource.Source, planHash string, document dataset.Document, resolved ResolvedPlan, parameters map[string]any, scope policy.UserScope, rowPolicies []policy.RowPolicy, columnPolicies []policy.ColumnPolicy, maxRows int, run RunRecord) (dataset.PreviewResult, error) {
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
	}{planHash, resolved.FileVersionID, resolved.Tables, rowPolicies, columnPolicies})
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

func (s *Service) previewFederated(ctx context.Context, sources map[string]datasource.Source, planHash string, document dataset.Document, resolved ResolvedPlan, parameters map[string]any, scope policy.UserScope, rowPolicies []policy.RowPolicy, columnPolicies []policy.ColumnPolicy, maxRows int, run RunRecord) (dataset.PreviewResult, error) {
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
	}{planHash, resolved.Nodes, rowPolicies, columnPolicies})
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
