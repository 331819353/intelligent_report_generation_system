package observability

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

type QuotaPostgresStore struct {
	pool *pgxpool.Pool
}

func NewQuotaPostgresStore(pool *pgxpool.Pool) (*QuotaPostgresStore, error) {
	if pool == nil {
		return nil, errors.New("quota database pool is required")
	}
	return &QuotaPostgresStore{pool: pool}, nil
}

func (store *QuotaPostgresStore) LoadSnapshots(ctx context.Context, request QuotaCheckRequest) ([]QuotaSnapshot, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("quota store is not initialized")
	}
	if err := validateQuotaRequest(request); err != nil {
		return nil, err
	}
	var snapshots []QuotaSnapshot
	err := database.WithTenantTx(ctx, store.pool, string(request.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT * FROM askdata.load_quota_usage_snapshots($1,$2,$3,$4)`,
			request.DomainID, request.ActorID, request.RunID, request.At.UTC())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var snapshot QuotaSnapshot
			var scope, period string
			var tokenLimit, runLimit, costLimit pgtype.Int8
			if err := rows.Scan(
				&scope, &snapshot.ScopeID, &period, &tokenLimit, &runLimit, &costLimit,
				&snapshot.Usage.LLMTokens, &snapshot.Usage.Runs, &snapshot.Usage.CostCents,
				&snapshot.ResetAt,
			); err != nil {
				return err
			}
			snapshot.Scope = QuotaScope(scope)
			snapshot.Period = QuotaPeriod(period)
			snapshot.Limits = QuotaLimits{
				LLMTokens: nullableInt64(tokenLimit), Runs: nullableInt64(runLimit), CostCents: nullableInt64(costLimit),
			}
			if err := validateQuotaSnapshot(request, snapshot); err != nil {
				return err
			}
			snapshots = append(snapshots, snapshot)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, mapQuotaDatabaseError(err)
	}
	return snapshots, nil
}

func (store *QuotaPostgresStore) Check(ctx context.Context, request QuotaCheckRequest) (QuotaDecision, error) {
	snapshots, err := store.LoadSnapshots(ctx, request)
	if err != nil {
		return QuotaDecision{}, err
	}
	return EvaluateQuota(request, snapshots)
}

func (store *QuotaPostgresStore) RecordCost(ctx context.Context, record CostRecord) (bool, error) {
	if store == nil || store.pool == nil {
		return false, errors.New("quota store is not initialized")
	}
	if err := record.Validate(); err != nil {
		return false, err
	}
	var inserted bool
	err := database.WithTenantTx(ctx, store.pool, string(record.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT askdata.record_cost_usage(
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11
		)`, record.ID, record.RunID, record.DomainID, record.ActorID,
			record.QuestionType, record.Provider, record.Model, record.PromptTokens,
			record.CompletionTokens, record.CostCents, record.QueryScanBytes).Scan(&inserted)
	})
	if err != nil {
		return false, mapQuotaDatabaseError(err)
	}
	return inserted, nil
}

func nullableInt64(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func mapQuotaDatabaseError(err error) error {
	message := err.Error()
	switch {
	case strings.Contains(message, "ASKDATA_QUOTA_SCOPE_INVALID"):
		return fmt.Errorf("quota scope is forbidden: %w", err)
	case strings.Contains(message, "ASKDATA_COST_USAGE_INVALID"):
		return fmt.Errorf("cost usage is invalid: %w", err)
	case strings.Contains(message, "ASKDATA_COST_IDEMPOTENCY_CONFLICT"):
		return fmt.Errorf("cost usage idempotency conflict: %w", err)
	default:
		return err
	}
}
