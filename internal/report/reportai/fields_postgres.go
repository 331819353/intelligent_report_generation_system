package reportai

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/policy"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/store"
)

// FieldCatalog resolves the only field names that may cross the Report AI
// boundary. Implementations must evaluate the current actor's access rather
// than accepting an allowlist supplied by the browser.
type FieldCatalog interface {
	AllowedFields(context.Context, store.Identity, report.DataContext) ([]string, error)
}

type PostgresFieldCatalog struct {
	pool     *pgxpool.Pool
	policies *policy.PostgresStore
}

func NewPostgresFieldCatalog(pool *pgxpool.Pool) *PostgresFieldCatalog {
	return &PostgresFieldCatalog{pool: pool, policies: policy.NewPostgresStore(pool)}
}

func (catalog *PostgresFieldCatalog) AllowedFields(
	ctx context.Context,
	identity store.Identity,
	dataContext report.DataContext,
) ([]string, error) {
	if catalog == nil || catalog.pool == nil || catalog.policies == nil || ctx == nil ||
		identity.Validate() != nil || dataContext.ID.Validate() != nil ||
		dataContext.DatasetID.Validate() != nil || dataContext.DatasetVersionID.Validate() != nil {
		return nil, errors.New("report AI field catalog request is invalid")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	_, _, columnPolicies, err := catalog.policies.Load(
		ctx, string(identity.TenantID), string(identity.ActorID), "DATASET", string(dataContext.DatasetID),
	)
	if err != nil {
		return nil, err
	}
	denied := map[string]struct{}{}
	for _, columnPolicy := range columnPolicies {
		if columnPolicy.PolicyType == "DENY" {
			denied[strings.ToLower(columnPolicy.FieldCode)] = struct{}{}
		}
	}
	fields := []string{}
	err = database.WithTenantTx(ctx, catalog.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT field.field_code::text
			FROM platform.dataset_fields AS field
			JOIN platform.dataset_versions AS version
			  ON version.tenant_id=field.tenant_id AND version.id=field.dataset_version_id
			WHERE field.dataset_version_id=$1 AND version.dataset_id=$2
			  AND version.status IN ('PUBLISHED','STALE') AND field.visible
			ORDER BY field.ordinal_position,field.field_code`,
			dataContext.DatasetVersionID, dataContext.DatasetID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var field string
			if scanErr := rows.Scan(&field); scanErr != nil {
				return scanErr
			}
			if _, blocked := denied[strings.ToLower(field)]; !blocked {
				fields = append(fields, field)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(fields)
	fields = slices.Compact(fields)
	if fields == nil {
		fields = []string{}
	}
	return fields, nil
}

var _ FieldCatalog = (*PostgresFieldCatalog)(nil)
