package reportai

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
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

func (catalog *PostgresFieldCatalog) Candidates(
	ctx context.Context,
	identity store.Identity,
	limit int,
) ([]DataContextCandidate, error) {
	if catalog == nil || catalog.pool == nil || ctx == nil || identity.Validate() != nil || limit < 1 || limit > 30 {
		return nil, errors.New("report AI data context catalog request is invalid")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	discovered := []DataContextCandidate{}
	err := database.WithTenantTx(ctx, catalog.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT dataset.id::text,dataset.current_published_version_id::text,
			dataset.name,dataset.description
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS version
			  ON version.id=dataset.current_published_version_id
			 AND version.dataset_id=dataset.id AND version.tenant_id=dataset.tenant_id
			WHERE dataset.deleted_at IS NULL AND dataset.current_published_version_id IS NOT NULL
			  AND dataset.status IN ('PUBLISHED','STALE') AND version.status IN ('PUBLISHED','STALE')
			ORDER BY dataset.updated_at DESC,dataset.id LIMIT $1`, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var datasetID, versionID, name, description string
			if scanErr := rows.Scan(&datasetID, &versionID, &name, &description); scanErr != nil {
				return scanErr
			}
			contextID := askdata.ID(uuid.NewSHA1(uuid.NameSpaceOID, []byte("report-ai-context\x00"+datasetID+"\x00"+versionID)).String())
			dataContext := report.DataContext{
				ID: contextID, DatasetID: askdata.ID(datasetID), DatasetVersionID: askdata.ID(versionID),
				Alias: strings.TrimSpace(name), DefaultParameters: []report.DefaultParameter{},
				QueryPolicy: report.QueryPolicy{TimeoutMS: 10_000, MaxRows: 5_000, CacheTTLSeconds: 300},
			}
			discovered = append(discovered, DataContextCandidate{DataContext: dataContext, Name: name, Description: description})
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	result := make([]DataContextCandidate, 0, len(discovered))
	for _, candidate := range discovered {
		definitions, fieldsErr := catalog.allowedFieldDefinitions(ctx, identity, candidate.DataContext)
		if fieldsErr != nil {
			return nil, fieldsErr
		}
		if len(definitions) != 0 {
			candidate.FieldDefinitions = definitions
			candidate.Fields = make([]string, 0, len(definitions))
			for _, definition := range definitions {
				candidate.Fields = append(candidate.Fields, definition.Code)
			}
			result = append(result, candidate)
		}
	}
	return result, nil
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
	definitions, err := catalog.allowedFieldDefinitions(ctx, identity, dataContext)
	if err != nil {
		return nil, err
	}
	fields := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		fields = append(fields, definition.Code)
	}
	return fields, nil
}

func (catalog *PostgresFieldCatalog) allowedFieldDefinitions(
	ctx context.Context,
	identity store.Identity,
	dataContext report.DataContext,
) ([]FieldDefinition, error) {
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
	fields := []FieldDefinition{}
	err = database.WithTenantTx(ctx, catalog.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT field.field_code::text,field.field_name,
			field.canonical_type,field.semantic_type,field.field_role,field.aggregation
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
			var field FieldDefinition
			if scanErr := rows.Scan(&field.Code, &field.Name, &field.CanonicalType, &field.SemanticType, &field.Role, &field.Aggregation); scanErr != nil {
				return scanErr
			}
			if _, blocked := denied[strings.ToLower(field.Code)]; !blocked {
				fields = append(fields, field)
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(fields, func(left, right FieldDefinition) int { return strings.Compare(left.Code, right.Code) })
	fields = slices.CompactFunc(fields, func(left, right FieldDefinition) bool { return left.Code == right.Code })
	if fields == nil {
		fields = []FieldDefinition{}
	}
	return fields, nil
}

var _ FieldCatalog = (*PostgresFieldCatalog)(nil)
var _ DataContextCatalog = (*PostgresFieldCatalog)(nil)
