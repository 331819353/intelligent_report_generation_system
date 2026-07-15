package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Open 建立 PostgreSQL 连接池，并通过探活确保连接可用。
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// WithTenantTx 在事务内设置租户上下文，确保 PostgreSQL RLS 对整个操作生效。
func WithTenantTx(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return errors.New("tenant ID is required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tenant transaction: %w", err)
	}
	// Commit 后 Rollback 是无操作；保留延迟回滚可覆盖所有提前返回路径。
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return fmt.Errorf("set tenant context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tenant transaction: %w", err)
	}
	return nil
}
