package validator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
)

type ExplainSummary struct {
	RootNodeType      string              `json:"rootNodeType"`
	TotalCost         float64             `json:"totalCost"`
	RootPlanRows      int64               `json:"rootPlanRows"`
	PlanNodes         int                 `json:"planNodes"`
	MaxNodeRows       int64               `json:"maxNodeRows"`
	SequentialScans   int                 `json:"sequentialScans"`
	MaxSequentialRows int64               `json:"maxSequentialRows"`
	Joins             int                 `json:"joins"`
	MaxJoinRows       int64               `json:"maxJoinRows"`
	MaxJoinFanout     float64             `json:"maxJoinFanout"`
	SummaryHash       askdata.ContentHash `json:"summaryHash"`
}

func (summary ExplainSummary) Validate() error {
	if summary.RootNodeType == "" || len(summary.RootNodeType) > 128 ||
		!utf8.ValidString(summary.RootNodeType) || strings.ContainsAny(summary.RootNodeType, "\x00\r\n") ||
		math.IsNaN(summary.TotalCost) || math.IsInf(summary.TotalCost, 0) || summary.TotalCost < 0 ||
		summary.RootPlanRows < 0 || summary.PlanNodes < 1 || summary.PlanNodes > 4096 ||
		summary.MaxNodeRows < summary.RootPlanRows || summary.SequentialScans < 0 ||
		summary.MaxSequentialRows < 0 || summary.Joins < 0 || summary.MaxJoinRows < 0 ||
		math.IsNaN(summary.MaxJoinFanout) || math.IsInf(summary.MaxJoinFanout, 0) ||
		summary.MaxJoinFanout < 0 || summary.SummaryHash.Validate() != nil {
		return ErrPlanNotExecutable
	}
	expected, err := explainSummaryHash(summary)
	if err != nil || expected != summary.SummaryHash {
		return ErrPlanNotExecutable
	}
	return nil
}

type explainEnvelope struct {
	Plan json.RawMessage `json:"Plan"`
	JIT  json.RawMessage `json:"JIT,omitempty"`
}

type explainAccumulator struct {
	limits  Limits
	summary ExplainSummary
}

type explainNodeStats struct{ rows int64 }

func analyzeExplain(raw json.RawMessage, limits Limits, maxRows int) (ExplainSummary, error) {
	if limits.Validate() != nil || maxRows < 1 || maxRows > limits.MaxRows ||
		len(raw) < 2 || len(raw) > limits.MaxExplainBytes {
		return ExplainSummary{}, reject(CodeExplainInvalid)
	}
	var envelopes []explainEnvelope
	if err := askdata.DecodeStrictJSON(raw, &envelopes); err != nil || len(envelopes) != 1 || len(envelopes[0].Plan) == 0 {
		return ExplainSummary{}, reject(CodeExplainInvalid)
	}
	var root map[string]json.RawMessage
	if err := askdata.DecodeStrictJSON(envelopes[0].Plan, &root); err != nil {
		return ExplainSummary{}, reject(CodeExplainInvalid)
	}
	accumulator := explainAccumulator{limits: limits}
	rootStats, err := accumulator.visit(root, 0)
	if err != nil {
		return ExplainSummary{}, err
	}
	accumulator.summary.RootPlanRows = rootStats.rows
	if rootStats.rows > int64(maxRows) {
		return ExplainSummary{}, reject(CodeRootRowsExceeded)
	}
	accumulator.summary.SummaryHash, err = explainSummaryHash(accumulator.summary)
	if err != nil {
		return ExplainSummary{}, err
	}
	return accumulator.summary, accumulator.summary.Validate()
}

func (accumulator *explainAccumulator) visit(node map[string]json.RawMessage, depth int) (explainNodeStats, error) {
	if depth > accumulator.limits.MaxPlanDepth {
		return explainNodeStats{}, reject(CodePlanNodesExceeded)
	}
	accumulator.summary.PlanNodes++
	if accumulator.summary.PlanNodes > accumulator.limits.MaxPlanNodes {
		return explainNodeStats{}, reject(CodePlanNodesExceeded)
	}
	nodeType, err := explainString(node, "Node Type")
	if err != nil {
		return explainNodeStats{}, reject(CodeExplainInvalid)
	}
	rows, err := explainInt64(node, "Plan Rows")
	if err != nil {
		return explainNodeStats{}, reject(CodeExplainInvalid)
	}
	cost, err := explainFloat64(node, "Total Cost")
	if err != nil {
		return explainNodeStats{}, reject(CodeExplainInvalid)
	}
	if depth == 0 {
		accumulator.summary.RootNodeType = nodeType
		accumulator.summary.TotalCost = cost
		if cost > accumulator.limits.MaxTotalCost {
			return explainNodeStats{}, reject(CodePlanCostExceeded)
		}
	}
	if rows > accumulator.limits.MaxNodeRows {
		return explainNodeStats{}, reject(CodePlanNodeRowsExceeded)
	}
	if rows > accumulator.summary.MaxNodeRows {
		accumulator.summary.MaxNodeRows = rows
	}

	children := []json.RawMessage{}
	if rawChildren, exists := node["Plans"]; exists {
		if err := askdata.DecodeStrictJSON(rawChildren, &children); err != nil || len(children) > accumulator.limits.MaxPlanNodes {
			return explainNodeStats{}, reject(CodeExplainInvalid)
		}
	}
	childRows := make([]int64, 0, len(children))
	for _, rawChild := range children {
		var child map[string]json.RawMessage
		if err := askdata.DecodeStrictJSON(rawChild, &child); err != nil {
			return explainNodeStats{}, reject(CodeExplainInvalid)
		}
		stats, err := accumulator.visit(child, depth+1)
		if err != nil {
			return explainNodeStats{}, err
		}
		childRows = append(childRows, stats.rows)
	}
	if nodeType == "Seq Scan" {
		accumulator.summary.SequentialScans++
		if rows > accumulator.summary.MaxSequentialRows {
			accumulator.summary.MaxSequentialRows = rows
		}
		if rows > accumulator.limits.MaxSequentialScanRows {
			return explainNodeStats{}, reject(CodeSequentialScanExceeded)
		}
	}
	if isJoinNode(nodeType) {
		accumulator.summary.Joins++
		if rows > accumulator.summary.MaxJoinRows {
			accumulator.summary.MaxJoinRows = rows
		}
		if rows > accumulator.limits.MaxJoinRows {
			return explainNodeStats{}, reject(CodeJoinRowsExceeded)
		}
		if len(childRows) < 2 {
			return explainNodeStats{}, reject(CodeExplainInvalid)
		}
		denominator := childRows[0]
		for _, child := range childRows[1:] {
			if child > denominator {
				denominator = child
			}
		}
		fanout := float64(rows)
		if denominator > 0 {
			fanout /= float64(denominator)
		} else if rows == 0 {
			fanout = 0
		}
		if fanout > accumulator.summary.MaxJoinFanout {
			accumulator.summary.MaxJoinFanout = fanout
		}
		if fanout > accumulator.limits.MaxJoinFanout {
			return explainNodeStats{}, reject(CodeJoinFanoutExceeded)
		}
	}
	return explainNodeStats{rows: rows}, nil
}

func isJoinNode(nodeType string) bool {
	return nodeType == "Nested Loop" || strings.HasSuffix(nodeType, " Join")
}

func explainString(node map[string]json.RawMessage, key string) (string, error) {
	raw, exists := node[key]
	if !exists {
		return "", errors.New("missing string")
	}
	var value string
	if err := askdata.DecodeStrictJSON(raw, &value); err != nil || value == "" || len(value) > 128 ||
		!utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("invalid string")
	}
	return value, nil
}

func explainFloat64(node map[string]json.RawMessage, key string) (float64, error) {
	raw, exists := node[key]
	if !exists {
		return 0, errors.New("missing number")
	}
	var value float64
	if err := askdata.DecodeStrictJSON(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, errors.New("invalid number")
	}
	return value, nil
}

func explainInt64(node map[string]json.RawMessage, key string) (int64, error) {
	value, err := explainFloat64(node, key)
	if err != nil || math.Trunc(value) != value || value > math.MaxInt64 {
		return 0, errors.New("invalid integer")
	}
	return int64(value), nil
}

func explainSummaryHash(summary ExplainSummary) (askdata.ContentHash, error) {
	copy := summary
	copy.SummaryHash = ""
	payload, err := registry.CanonicalValue(copy)
	if err != nil {
		return "", fmt.Errorf("hash EXPLAIN summary: %w", err)
	}
	return askdata.HashBytes(payload), nil
}

// PostgresExplainer never executes the compiled SELECT. It starts a
// repeatable-read, read-only USER transaction, applies local timeouts/timezone
// and runs only the fixed EXPLAIN (FORMAT JSON) prefix.
type PostgresExplainer struct{ pool *pgxpool.Pool }

func NewPostgresExplainer(pool *pgxpool.Pool) *PostgresExplainer {
	return &PostgresExplainer{pool: pool}
}

func (explainer *PostgresExplainer) Explain(ctx context.Context, request ExplainRequest) (json.RawMessage, error) {
	if explainer == nil || explainer.pool == nil || request.Scope.Validate() != nil ||
		request.DomainID.Validate() != nil || !containsID(request.Scope.DomainIDs, request.DomainID) ||
		request.QueryPlanHash.Validate() != nil || request.CompiledPlanHash.Validate() != nil ||
		request.Source.DatasetVersionID.Validate() != nil || request.Source.MaterializationID.Validate() != nil ||
		request.StatementTimeoutMS < 100 || request.StatementTimeoutMS > 25000 ||
		request.LockTimeoutMS < 1 || request.LockTimeoutMS > request.StatementTimeoutMS ||
		request.MaxExplainBytes < 1024 || request.MaxExplainBytes > 16<<20 {
		return nil, ErrExplainUnavailable
	}
	if err := validateSQL(request.SQL, request.Source); err != nil {
		return nil, err
	}
	if _, err := time.LoadLocation(request.Timezone); err != nil {
		return nil, ErrExplainUnavailable
	}
	access, ok := database.AccessContextFromContext(ctx)
	if !ok || access.UserID != string(request.Scope.ActorID) || access.DomainID != string(request.DomainID) {
		return nil, ErrExplainUnavailable
	}
	tx, err := explainer.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, explainContextError(ctx)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.tenant_id',$1,true),
		set_config('app.access_mode','USER',true),
		set_config('app.user_id',$2,true),
		set_config('app.domain_id',$3,true),
		set_config('statement_timeout',$4,true),
		set_config('lock_timeout',$5,true),
		set_config('TimeZone',$6,true)`,
		request.Scope.TenantID, request.Scope.ActorID, request.DomainID,
		fmt.Sprintf("%dms", request.StatementTimeoutMS), fmt.Sprintf("%dms", request.LockTimeoutMS),
		request.Timezone); err != nil {
		return nil, explainContextError(ctx)
	}
	var raw json.RawMessage
	if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+request.SQL, request.Args...).Scan(&raw); err != nil {
		return nil, explainContextError(ctx)
	}
	if len(raw) < 2 || len(raw) > request.MaxExplainBytes {
		return nil, ErrExplainUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, explainContextError(ctx)
	}
	return append(json.RawMessage(nil), raw...), nil
}

func explainContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrExplainUnavailable
}
