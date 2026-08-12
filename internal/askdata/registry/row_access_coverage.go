package registry

import (
	"context"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/platform/database"
)

// RowAccessAttributeCoverage reports, for one subject attribute referenced by a
// certified policy, how many active domain members actually hold a value.
//
// This exists because the fail-closed rule is silent by design: a policy whose
// attribute nobody has been granted denies every row to everyone, and the model
// simply starts returning nothing. Surfacing coverage turns that from a mystery
// into a number somebody can act on before the policy is released.
type RowAccessAttributeCoverage struct {
	AttributeKey       string `json:"attributeKey"`
	PolicyCount        int    `json:"policyCount"`
	MemberCount        int    `json:"memberCount"`
	CoveredMemberCount int    `json:"coveredMemberCount"`
}

type RowAccessCoverageBackend interface {
	GetRowAccessCoverage(context.Context, AdminScope) ([]RowAccessAttributeCoverage, error)
}

func (store *PostgresStore) GetRowAccessCoverage(
	ctx context.Context, scope AdminScope,
) ([]RowAccessAttributeCoverage, error) {
	if store == nil || store.pool == nil {
		return nil, ErrRegistryInvalidRequest
	}
	if err := scope.Validate(ctx); err != nil {
		return nil, err
	}
	items := []RowAccessAttributeCoverage{}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT attribute_key,policy_count,member_count,covered_member_count
			FROM askdata.row_access_policy_coverage($1,$2)`, scope.TenantID, scope.DomainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item RowAccessAttributeCoverage
			if err := rows.Scan(&item.AttributeKey, &item.PolicyCount,
				&item.MemberCount, &item.CoveredMemberCount); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, normalizeAdminStoreError(err)
}

var _ RowAccessCoverageBackend = (*PostgresStore)(nil)
