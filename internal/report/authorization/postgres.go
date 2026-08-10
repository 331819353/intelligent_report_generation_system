package authorization

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/store"
)

var ErrForbidden = errors.New("report permission denied")

type PostgresAuthorizer struct{ pool *pgxpool.Pool }

func NewPostgresAuthorizer(pool *pgxpool.Pool) *PostgresAuthorizer {
	return &PostgresAuthorizer{pool: pool}
}

func (authorizer *PostgresAuthorizer) CheckReportView(ctx context.Context, identity store.Identity, reportID askdata.ID) error {
	return authorizer.check(ctx, identity, reportID, "VIEW")
}

func (authorizer *PostgresAuthorizer) CheckReportPublish(ctx context.Context, identity store.Identity, reportID askdata.ID) error {
	return authorizer.check(ctx, identity, reportID, "PUBLISH")
}

func (authorizer *PostgresAuthorizer) CheckReportEdit(ctx context.Context, identity store.Identity, reportID askdata.ID) error {
	return authorizer.check(ctx, identity, reportID, "EDIT")
}

func (authorizer *PostgresAuthorizer) check(ctx context.Context, identity store.Identity, reportID askdata.ID, action string) error {
	if authorizer == nil || authorizer.pool == nil || identity.Validate() != nil || reportID.Validate() != nil {
		return ErrForbidden
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var allowed bool
	err := database.WithTenantTx(ctx, authorizer.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT platform.report_v2_can_access($1,ARRAY[$2]::text[])`, reportID, action).Scan(&allowed)
	})
	if err != nil || !allowed {
		return errors.Join(err, ErrForbidden)
	}
	return nil
}
