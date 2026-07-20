package federation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/filequery"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/querycompiler"
	"intelligent-report-generation-system/internal/queryruntime"
)

const (
	maxSourceRows = 10000
)

var (
	ErrSourceRowLimit     = errors.New("federated source row limit exceeded")
	ErrUnsupportedJoin    = errors.New("federated preview currently supports equality joins only")
	ErrInvalidSourceShape = errors.New("federated source returned an invalid result shape")
)

type activeExecution struct {
	cancel     context.CancelFunc
	targets    map[string]queryruntime.QueryConnector
	cancelOnce sync.Once
	cancelErr  error
}

// Executor 并发读取受控源节点，并把规范数据交给统一 DSL 求值器完成跨源计算。
type Executor struct {
	connectors map[datasource.Type]queryruntime.QueryConnector
	files      filequery.VersionReader
	mu         sync.Mutex
	active     map[string]*activeExecution
}

// NewExecutor 创建实时联邦预览执行器。
func NewExecutor(connectors map[datasource.Type]queryruntime.QueryConnector, files filequery.VersionReader) *Executor {
	return &Executor{connectors: connectors, files: files, active: map[string]*activeExecution{}}
}

// Execute 对每个节点实施必要字段裁剪、过滤下推、源端限额和固定版本读取。
func (e *Executor) Execute(ctx context.Context, queryID string, document dataset.Document, plan queryruntime.ResolvedPlan, sources map[string]datasource.Source, parameters map[string]any, scope policy.UserScope, rowPolicies []policy.RowPolicy, columnPolicies []policy.ColumnPolicy, maxRows int) (datasource.QueryResult, error) {
	if document.Dataset.Type != "CROSS_SOURCE" || len(document.Nodes) < 2 || len(document.Nodes) > dataset.MaxNodes {
		return datasource.QueryResult{}, errors.New("invalid federated dataset node count")
	}
	for _, join := range document.Joins {
		for _, condition := range join.Conditions {
			if condition.Operator != "EQUALS" {
				return datasource.QueryResult{}, ErrUnsupportedJoin
			}
		}
	}
	executionDocument := pruneNodeProjections(document)
	preAggregation := planSourceAggregations(executionDocument, plan, columnPolicies)
	executionDocument = preAggregation.Document
	queryContext, cancel := context.WithCancel(ctx)
	targets := map[string]queryruntime.QueryConnector{}
	for _, node := range executionDocument.Nodes {
		resolved, ok := plan.Nodes[node.ID]
		if !ok {
			cancel()
			return datasource.QueryResult{}, errors.New("federated node is not in the resolved plan")
		}
		if resolved.SourceType != datasource.TypeExcel {
			connector := e.connectors[resolved.SourceType]
			if connector == nil {
				cancel()
				return datasource.QueryResult{}, errors.New("federated database connector is unavailable")
			}
			targets[queryruntime.FederatedSubqueryID(queryID, node.ID)] = connector
		}
	}
	e.mu.Lock()
	if _, exists := e.active[queryID]; exists {
		e.mu.Unlock()
		cancel()
		return datasource.QueryResult{}, errors.New("federated query ID is already active")
	}
	active := &activeExecution{cancel: cancel, targets: targets}
	e.active[queryID] = active
	e.mu.Unlock()
	defer func() {
		cancel()
		e.mu.Lock()
		delete(e.active, queryID)
		e.mu.Unlock()
	}()

	tables := make(map[string]filequery.NodeTableData, len(executionDocument.Nodes))
	stats := make([]datasource.QuerySourceStat, 0, len(executionDocument.Nodes))
	fileLoads := prepareFileLoads(executionDocument, plan)
	var tablesMu sync.Mutex
	var firstErr error
	var errorOnce sync.Once
	var wait sync.WaitGroup
	for _, node := range executionDocument.Nodes {
		node := node
		wait.Add(1)
		go func() {
			defer wait.Done()
			started := time.Now()
			table, err := e.loadNode(queryContext, queryID, executionDocument, node, plan, sources, parameters, fileLoads, sourceRowLimit(maxRows), preAggregation.Projections[node.ID])
			stat := datasource.QuerySourceStat{
				NodeID: node.ID, SubqueryID: queryruntime.FederatedSubqueryID(queryID, node.ID),
				DurationMS: time.Since(started).Milliseconds(), Status: sourceExecutionStatus(err),
			}
			if err == nil {
				stat.RowCount = len(table.Rows)
			}
			tablesMu.Lock()
			stats = append(stats, stat)
			if err == nil {
				tables[node.ID] = table
			}
			tablesMu.Unlock()
			if err != nil {
				errorOnce.Do(func() {
					firstErr = err
					cancel()
				})
			}
		}()
	}
	wait.Wait()
	sort.Slice(stats, func(i, j int) bool { return stats[i].NodeID < stats[j].NodeID })
	result := datasource.QueryResult{SourceStats: stats}
	if firstErr != nil {
		// 任一节点失败都会取消同批节点；HTTP 上下文结束只表示调用方不再等待，
		// 数据库驱动仍可能执行，因此必须在移除活动句柄前主动发送远端取消。
		cancelContext, cancelRemote := context.WithTimeout(context.Background(), 2*time.Second)
		_ = active.cancelTargets(cancelContext)
		cancelRemote()
		return result, firstErr
	}
	warnings, err := analyzeJoinRisks(queryContext, executionDocument, tables)
	if err != nil {
		return result, err
	}
	evaluated, err := filequery.Evaluate(queryContext, filequery.Input{
		Document: executionDocument, Tables: plan.Tables, NodeTables: tables, Parameters: parameters,
		Scope: scope, RowPolicies: rowPolicies, ColumnPolicies: columnPolicies, MaxRows: maxRows,
	})
	if err != nil {
		return result, err
	}
	evaluated.Warnings = warnings
	evaluated.SourceStats = stats
	return evaluated, nil
}

func sourceExecutionStatus(err error) string {
	if err == nil {
		return "SUCCEEDED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if errors.Is(err, context.Canceled) {
		return "CANCELLED"
	}
	return "FAILED"
}

// Cancel 同时终止本地联邦上下文和已登记的数据库子查询。
func (e *Executor) Cancel(ctx context.Context, queryID string) (bool, error) {
	e.mu.Lock()
	active, exists := e.active[queryID]
	e.mu.Unlock()
	if !exists {
		return false, nil
	}
	active.cancel()
	return true, active.cancelTargets(ctx)
}

func (a *activeExecution) cancelTargets(ctx context.Context) error {
	a.cancelOnce.Do(func() {
		for subqueryID, connector := range a.targets {
			if _, err := connector.Cancel(ctx, subqueryID); err != nil && a.cancelErr == nil {
				a.cancelErr = err
			}
		}
	})
	return a.cancelErr
}

type fileLoad struct {
	once    sync.Once
	version datasource.FileVersion
	tables  []datasource.FileTableData
	err     error
}

func prepareFileLoads(document dataset.Document, plan queryruntime.ResolvedPlan) map[string]*fileLoad {
	loads := map[string]*fileLoad{}
	for _, node := range document.Nodes {
		resolved := plan.Nodes[node.ID]
		if resolved.SourceType == datasource.TypeExcel {
			key := resolved.SourceID + ":" + resolved.FileVersionID
			if loads[key] == nil {
				loads[key] = &fileLoad{}
			}
		}
	}
	return loads
}

func (e *Executor) loadNode(ctx context.Context, queryID string, document dataset.Document, node dataset.Node, plan queryruntime.ResolvedPlan, sources map[string]datasource.Source, parameters map[string]any, fileLoads map[string]*fileLoad, rowLimit int, aggregateProjections map[string]querycompiler.ScanAggregateProjection) (filequery.NodeTableData, error) {
	resolved := plan.Nodes[node.ID]
	source, ok := sources[resolved.SourceID]
	if !ok || source.Type != resolved.SourceType {
		return filequery.NodeTableData{}, errors.New("federated source is unavailable")
	}
	if resolved.SourceType == datasource.TypeExcel {
		if e.files == nil || resolved.FileVersionID == "" {
			return filequery.NodeTableData{}, errors.New("federated file reader is unavailable")
		}
		key := resolved.SourceID + ":" + resolved.FileVersionID
		load := fileLoads[key]
		load.once.Do(func() {
			load.version, load.tables, load.err = e.files.ReadVersionTables(ctx, source.TenantID, resolved.FileVersionID, source.RuntimeQuota.MaxExcelFileBytes)
		})
		if load.err != nil {
			return filequery.NodeTableData{}, load.err
		}
		if load.version.ID != source.FileAssetID {
			return filequery.NodeTableData{}, filequery.ErrFileVersionMismatch
		}
		for _, table := range load.tables {
			if table.Name != resolved.Table.Name {
				continue
			}
			prepared, err := filequery.NodeTableFromFile(table, node.Projection)
			if err != nil {
				return filequery.NodeTableData{}, err
			}
			if len(prepared.Rows) > rowLimit {
				return filequery.NodeTableData{}, ErrSourceRowLimit
			}
			return prepared, nil
		}
		return filequery.NodeTableData{}, fmt.Errorf("worksheet %s is absent from the fixed file version", resolved.Table.Name)
	}
	dialect := querycompiler.MySQL
	if resolved.SourceType == datasource.TypeOracle {
		dialect = querycompiler.Oracle
	}
	compiled, err := querycompiler.CompileScan(querycompiler.ScanInput{
		Document: document, NodeID: node.ID, Dialect: dialect, Table: resolved.Table, Parameters: parameters, MaxRows: rowLimit + 1,
		AggregateProjections: aggregateProjections,
	})
	if err != nil {
		return filequery.NodeTableData{}, err
	}
	connector := e.connectors[resolved.SourceType]
	result, err := connector.Query(ctx, source, queryruntime.FederatedSubqueryID(queryID, node.ID), compiled.SQL, compiled.Args, compiled.MaxRows)
	if err != nil {
		slog.ErrorContext(ctx, "federated source query failed", "query_id", queryID, "node_id", node.ID, "source_id", source.ID, "error", err)
		return filequery.NodeTableData{}, err
	}
	if result.RowCount != len(result.Rows) || result.RowCount > rowLimit {
		slog.ErrorContext(ctx, "federated source row shape is invalid", "query_id", queryID, "node_id", node.ID, "reported_rows", result.RowCount, "actual_rows", len(result.Rows), "row_limit", rowLimit)
		return filequery.NodeTableData{}, ErrSourceRowLimit
	}
	if len(result.Columns) != len(node.Projection) {
		slog.ErrorContext(ctx, "federated source column count is invalid", "query_id", queryID, "node_id", node.ID, "actual_columns", result.Columns, "expected_columns", node.Projection)
		return filequery.NodeTableData{}, ErrInvalidSourceShape
	}
	for index, name := range node.Projection {
		if !strings.EqualFold(result.Columns[index], name) {
			slog.ErrorContext(ctx, "federated source column order is invalid", "query_id", queryID, "node_id", node.ID, "actual_columns", result.Columns, "expected_columns", node.Projection)
			return filequery.NodeTableData{}, ErrInvalidSourceShape
		}
	}
	rows := make([][]any, len(result.Rows))
	for rowIndex, row := range result.Rows {
		if len(row) != len(node.Projection) {
			return filequery.NodeTableData{}, ErrInvalidSourceShape
		}
		rows[rowIndex] = make([]any, len(row))
		for columnIndex, value := range row {
			column := node.Projection[columnIndex]
			canonicalType := resolved.Table.ColumnTypes[column]
			if aggregate, exists := aggregateProjections[column]; exists {
				canonicalType = resolved.Table.ColumnTypes[aggregate.SourceField]
				if aggregate.Function == "COUNT" {
					canonicalType = "INTEGER"
				}
			}
			normalized, err := normalizeValue(value, canonicalType)
			if err != nil {
				return filequery.NodeTableData{}, err
			}
			rows[rowIndex][columnIndex] = normalized
		}
	}
	return filequery.NodeTableData{Columns: append([]string(nil), node.Projection...), Rows: rows}, nil
}

func sourceRowLimit(maxRows int) int {
	return min(max(maxRows*50, 1000), maxSourceRows)
}

func normalizeValue(value any, canonicalType string) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch canonicalType {
	case "NUMBER", "INTEGER":
		switch number := value.(type) {
		case json.Number:
			parsed, err := number.Int64()
			if err == nil {
				return parsed, nil
			}
			// Oracle NUMBER 既可表示整数也可表示定点小数；元数据无法仅凭类型名
			// 判定精度时保留合法 json.Number，避免把 SUM(DECIMAL) 误判为整数。
			if _, decimalErr := number.Float64(); decimalErr != nil {
				return nil, errors.New("numeric source value is invalid")
			}
			return number, nil
		case float64:
			if math.Trunc(number) == number && number <= math.MaxInt64 && number >= math.MinInt64 {
				return int64(number), nil
			}
			return number, nil
		}
	case "DECIMAL":
		if number, ok := value.(json.Number); ok {
			if _, err := number.Float64(); err != nil {
				return nil, errors.New("decimal source value is invalid")
			}
			return number, nil
		}
	case "DATE", "DATETIME", "TIMESTAMP", "TIME", "TEXT", "STRING":
		if text, ok := value.(string); ok {
			return text, nil
		}
	case "BINARY":
		return nil, errors.New("binary columns cannot participate in federated preview")
	}
	return value, nil
}
