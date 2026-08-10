package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

var (
	ErrRegistryNotFound        = errors.New("semantic registry object was not found")
	ErrRegistryVersionConflict = errors.New("semantic registry object version conflict")
	ErrRegistryConflict        = errors.New("semantic registry object conflicts with an existing object")
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) CreateMetric(ctx context.Context, metric Metric) (Metric, error) {
	if store == nil || store.pool == nil {
		return Metric{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if metric.ID == "" {
		metric.ID = uuid.NewString()
	}
	if metric.Status == "" {
		metric.Status = "DRAFT"
	}
	if metric.Version == 0 {
		metric.Version = 1
	}
	if err := metric.Validate(); err != nil {
		return Metric{}, err
	}
	err := database.WithTenantTx(ctx, store.pool, metric.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO askdata.metrics(
			id,tenant_id,domain_id,code,name,description,status,owner_id,version
		) VALUES($1,$2,$3,$4,$5,$6,'DRAFT',$7,1)
		RETURNING created_at,updated_at,version`,
			metric.ID, metric.TenantID, metric.DomainID, metric.Code,
			metric.Name, metric.Description, metric.OwnerID,
		).Scan(&metric.CreatedAt, &metric.UpdatedAt, &metric.Version)
	})
	if registryUniqueConflict(err) {
		return Metric{}, ErrRegistryConflict
	}
	return metric, err
}

func (store *PostgresStore) UpdateMetric(ctx context.Context, metric Metric) (Metric, error) {
	if store == nil || store.pool == nil {
		return Metric{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := metric.Validate(); err != nil {
		return Metric{}, err
	}
	if metric.Status != "DRAFT" {
		return Metric{}, ErrRegistryVersionConflict
	}
	expectedVersion := metric.Version
	err := database.WithTenantTx(ctx, store.pool, metric.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE askdata.metrics SET
			code=$1,name=$2,description=$3,owner_id=$4,version=version+1
		WHERE id=$5 AND domain_id=$6 AND status='DRAFT' AND version=$7`,
			metric.Code, metric.Name, metric.Description, metric.OwnerID,
			metric.ID, metric.DomainID, expectedVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrRegistryVersionConflict
		}
		return scanMetric(tx.QueryRow(ctx, metricSelect+` WHERE id=$1 AND domain_id=$2`, metric.ID, metric.DomainID), &metric)
	})
	if registryUniqueConflict(err) {
		return Metric{}, ErrRegistryConflict
	}
	return metric, err
}

func (store *PostgresStore) GetMetric(ctx context.Context, tenantID, domainID, metricID string) (Metric, error) {
	if store == nil || store.pool == nil {
		return Metric{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	var metric Metric
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return scanMetric(tx.QueryRow(ctx, metricSelect+` WHERE id=$1 AND domain_id=$2`, metricID, domainID), &metric)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Metric{}, ErrRegistryNotFound
	}
	return metric, err
}

func (store *PostgresStore) ListMetrics(ctx context.Context, tenantID, domainID, cursor string, limit int) (MetricPage, error) {
	if store == nil || store.pool == nil {
		return MetricPage{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		return MetricPage{}, errors.New("tenant ID must be a UUID")
	}
	if _, err := uuid.Parse(domainID); err != nil {
		return MetricPage{}, errors.New("domain ID must be a UUID")
	}
	if limit < 1 || limit > 200 {
		return MetricPage{}, errors.New("metric page limit must be between 1 and 200")
	}
	position, err := decodeMetricCursor(cursor)
	if err != nil {
		return MetricPage{}, err
	}
	var cursorID any
	if position.ID != "" {
		cursorID = position.ID
	}
	page := MetricPage{Items: []Metric{}}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, metricSelect+`
			WHERE domain_id=$1
			  AND ($2::timestamptz IS NULL OR (updated_at,id)<($2,$3::uuid))
			ORDER BY updated_at DESC,id DESC LIMIT $4`,
			domainID, position.UpdatedAt, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var metric Metric
			if err := scanMetric(rows, &metric); err != nil {
				return err
			}
			page.Items = append(page.Items, metric)
		}
		return rows.Err()
	})
	if err != nil {
		return MetricPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.NextCursor, err = encodeMetricCursor(metricCursor{UpdatedAt: &last.UpdatedAt, ID: last.ID})
		if err != nil {
			return MetricPage{}, err
		}
		page.Items = page.Items[:limit]
	}
	return page, nil
}

func (store *PostgresStore) CreateMetricVersion(ctx context.Context, metric MetricVersion) (MetricVersion, error) {
	if store == nil || store.pool == nil {
		return MetricVersion{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if metric.ID == "" {
		metric.ID = uuid.NewString()
	}
	if metric.ObjectID == "" {
		metric.ObjectID = metric.MetricID
	}
	if metric.Status == "" {
		metric.Status = VersionStatusDraft
	}
	if len(metric.DefaultFiltersAST) == 0 {
		metric.DefaultFiltersAST = json.RawMessage(`{"type":"TRUE"}`)
	}
	if metric.TimeGrain == "" {
		metric.TimeGrain = "NONE"
	}
	if metric.NullPolicy == "" {
		metric.NullPolicy = "PRESERVE"
	}
	applyAdditivityDefaultsToMetric(&metric)
	if metric.ObjectID != metric.MetricID {
		return MetricVersion{}, ValidationErrors{Issues: []ValidationIssue{{Code: validationCodeInvalidDependency, Path: "objectId", Message: "must equal metricId"}}}
	}
	if metric.Status != VersionStatusDraft {
		return MetricVersion{}, errors.New("repository creates metric versions as DRAFT only")
	}
	if err := metric.Validate(); err != nil {
		return MetricVersion{}, err
	}
	dependencies := append([]string(nil), metric.MeasureVersionIDs...)
	sort.Strings(dependencies)
	metric.MeasureVersionIDs = dependencies
	err := database.WithTenantTx(ctx, store.pool, metric.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_versions(
			id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,
			formula_ast,default_filters_ast,unit,currency,time_grain,additivity,
			semi_additive_time_aggregation,aggregation_restriction,non_additive_dimensions,
			zero_denominator_policy,display_precision,additivity_suggestion,
			additivity_confirmed_by,additivity_confirmed_at,null_policy,
			incomplete_period_policy_override,status,content_hash,owner_id
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::text,$11,NULLIF($12,'')::text,
			NULLIF($13,'')::text,NULLIF($14,'')::text,$15,$16,$17,NULLIF($18,'')::text,
			NULLIF($19,'')::uuid,$20,$21,NULLIF($22,'')::text,'DRAFT',$23,$24)`,
			metric.ID, metric.TenantID, metric.DomainID, metric.MetricID, metric.VersionNo,
			metric.SemanticModelVersionID, metric.FormulaAST, metric.DefaultFiltersAST,
			metric.Unit, metric.Currency, metric.TimeGrain, metric.Additivity,
			metric.SemiAdditiveTimeAggregation, metric.AggregationRestriction,
			metric.NonAdditiveDimensions, metric.ZeroDenominatorPolicy, metric.DisplayPrecision,
			metric.AdditivitySuggestion, metric.AdditivityConfirmedBy, metric.AdditivityConfirmedAt,
			metric.NullPolicy, emptyStringToNil(string(metric.IncompletePeriodPolicyOverride)), metric.ContentHash,
			metric.OwnerID); err != nil {
			return err
		}
		for index, measureID := range dependencies {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_version_measures(
				tenant_id,domain_id,metric_version_id,measure_version_id,ordinal
			) VALUES($1,$2,$3,$4,$5)`,
				metric.TenantID, metric.DomainID, metric.ID, measureID, index+1); err != nil {
				return err
			}
		}
		return nil
	})
	if registryUniqueConflict(err) {
		return MetricVersion{}, ErrRegistryConflict
	}
	return metric, err
}

func (store *PostgresStore) GetMetricVersion(ctx context.Context, tenantID, domainID, versionID string) (MetricVersion, error) {
	if store == nil || store.pool == nil {
		return MetricVersion{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	var metric MetricVersion
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			version.id::text,version.tenant_id::text,version.domain_id::text,
			version.metric_id::text,version.version_no,version.status,version.content_hash,
			version.owner_id::text,version.created_at,version.updated_at,
			version.semantic_model_version_id::text,version.formula_ast,
			version.default_filters_ast,version.unit,COALESCE(version.currency,''),version.time_grain,
			COALESCE(version.additivity,''),COALESCE(version.semi_additive_time_aggregation,''),
			COALESCE(version.aggregation_restriction,''),version.non_additive_dimensions,
			version.zero_denominator_policy,version.display_precision,
			COALESCE(version.additivity_suggestion,''),COALESCE(version.additivity_confirmed_by::text,''),
			version.additivity_confirmed_at,version.null_policy,
			COALESCE(version.incomplete_period_policy_override,''),
			COALESCE(array_agg(link.measure_version_id::text ORDER BY link.ordinal)
				FILTER(WHERE link.measure_version_id IS NOT NULL),'{}'::text[])
		FROM askdata.metric_versions AS version
		LEFT JOIN askdata.metric_version_measures AS link
		  ON link.metric_version_id=version.id AND link.tenant_id=version.tenant_id
		WHERE version.id=$1 AND version.domain_id=$2
		GROUP BY version.id`, versionID, domainID).Scan(
			&metric.ID, &metric.TenantID, &metric.DomainID, &metric.MetricID,
			&metric.VersionNo, &metric.Status, &metric.ContentHash, &metric.OwnerID,
			&metric.CreatedAt, &metric.UpdatedAt, &metric.SemanticModelVersionID,
			&metric.FormulaAST, &metric.DefaultFiltersAST, &metric.Unit, &metric.Currency,
			&metric.TimeGrain, &metric.Additivity, &metric.SemiAdditiveTimeAggregation,
			&metric.AggregationRestriction, &metric.NonAdditiveDimensions,
			&metric.ZeroDenominatorPolicy, &metric.DisplayPrecision,
			&metric.AdditivitySuggestion, &metric.AdditivityConfirmedBy, &metric.AdditivityConfirmedAt,
			&metric.NullPolicy,
			&metric.IncompletePeriodPolicyOverride,
			&metric.MeasureVersionIDs,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return MetricVersion{}, ErrRegistryNotFound
	}
	metric.ObjectID = metric.MetricID
	return metric, err
}

func (store *PostgresStore) CreateReleaseDraft(ctx context.Context, release SemanticRelease, manifest ReleaseManifest) (SemanticRelease, error) {
	if store == nil || store.pool == nil {
		return SemanticRelease{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if release.ContentHash != manifest.ContentHash || len(manifest.Objects) == 0 {
		return SemanticRelease{}, errors.New("release manifest hash is missing or inconsistent")
	}
	key, err := ReleaseIdempotencyKey(release.TenantID, release.DomainID, release.SemanticVersion, release.ContentHash)
	if err != nil {
		return SemanticRelease{}, err
	}
	if release.ID == "" {
		release.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String()
	}
	if release.CreatedBy == "" || release.UpdatedBy == "" {
		return SemanticRelease{}, errors.New("release creator and updater are required")
	}
	release.Status = "DRAFT"
	err = database.WithTenantTx(ctx, store.pool, release.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
			id,tenant_id,domain_id,semantic_version,content_hash,status,
			object_count,created_by,updated_by
		) VALUES($1,$2,$3,$4,$5,'DRAFT',$6,$7,$8)
		ON CONFLICT(tenant_id,domain_id,semantic_version) DO NOTHING`,
			release.ID, release.TenantID, release.DomainID, release.SemanticVersion,
			release.ContentHash, len(manifest.Objects), release.CreatedBy, release.UpdatedBy)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var existingHash, existingManifestHash string
			var existingManifestCount int
			if err := tx.QueryRow(ctx, `SELECT id::text,content_hash,status,object_count,version,
				created_by::text,updated_by::text,created_at,updated_at,
				askdata.release_manifest_hash(releases.id),
				(SELECT count(*) FROM askdata.release_objects WHERE release_id=releases.id)
			FROM askdata.releases AS releases
			WHERE domain_id=$1 AND semantic_version=$2`, release.DomainID, release.SemanticVersion).Scan(
				&release.ID, &existingHash, &release.Status, &release.ObjectCount,
				&release.Version, &release.CreatedBy, &release.UpdatedBy,
				&release.CreatedAt, &release.UpdatedAt, &existingManifestHash,
				&existingManifestCount); err != nil {
				return err
			}
			if existingHash != string(release.ContentHash) ||
				existingManifestHash != string(manifest.ContentHash) ||
				existingManifestCount != len(manifest.Objects) {
				return ErrRegistryConflict
			}
			return nil
		}
		for _, object := range manifest.Objects {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
				tenant_id,domain_id,release_id,object_type,object_id,
				object_version_id,content_hash,sensitivity,contract_json
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				release.TenantID, release.DomainID, release.ID, object.Type,
				object.ObjectID, object.ObjectVersionID, object.ContentHash,
				object.Sensitivity, object.Contract); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT status,object_count,version,created_at,updated_at
			FROM askdata.releases WHERE id=$1`, release.ID).Scan(
			&release.Status, &release.ObjectCount, &release.Version,
			&release.CreatedAt, &release.UpdatedAt)
	})
	return release, err
}

const metricSelect = `SELECT id::text,tenant_id::text,domain_id::text,code::text,name,
	description,status,owner_id::text,version,created_at,updated_at FROM askdata.metrics`

type rowScanner interface{ Scan(...any) error }

func scanMetric(row rowScanner, metric *Metric) error {
	return row.Scan(&metric.ID, &metric.TenantID, &metric.DomainID, &metric.Code,
		&metric.Name, &metric.Description, &metric.Status, &metric.OwnerID,
		&metric.Version, &metric.CreatedAt, &metric.UpdatedAt)
}

type metricCursor struct {
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	ID        string     `json:"id,omitempty"`
}

func encodeMetricCursor(cursor metricCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeMetricCursor(encoded string) (metricCursor, error) {
	if encoded == "" {
		return metricCursor{}, nil
	}
	if len(encoded) > 1024 {
		return metricCursor{}, errors.New("metric cursor is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return metricCursor{}, errors.New("metric cursor is invalid")
	}
	var cursor metricCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.UpdatedAt == nil {
		return metricCursor{}, errors.New("metric cursor is invalid")
	}
	parsed, err := uuid.Parse(cursor.ID)
	if err != nil || parsed.String() != strings.ToLower(cursor.ID) {
		return metricCursor{}, errors.New("metric cursor is invalid")
	}
	cursor.UpdatedAt = timePointer(cursor.UpdatedAt.UTC())
	return cursor, nil
}

func timePointer(value time.Time) *time.Time { return &value }

func emptyStringToNil(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func registryUniqueConflict(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "23505"
}

func RegistryErrorCode(err error) string {
	var additivity *AdditivityError
	if errors.As(err, &additivity) {
		return additivity.Code
	}
	switch {
	case errors.Is(err, ErrRegistryNotFound):
		return "REG_NOT_FOUND"
	case errors.Is(err, ErrRegistryVersionConflict):
		return "REG_VERSION_CONFLICT"
	case errors.Is(err, ErrRegistryConflict):
		return "REG_CONFLICT"
	case errors.Is(err, ErrRegistryPermissionDenied):
		return "REG_PERMISSION_DENIED"
	case errors.Is(err, ErrRegistryIdempotencyConflict):
		return "REG_IDEMPOTENCY_CONFLICT"
	case errors.Is(err, ErrRegistryDraftInUse):
		return "REG_DRAFT_IN_USE"
	case errors.Is(err, ErrRegistryInvalidRequest):
		return "REG_INVALID_REQUEST"
	default:
		var validation ValidationErrors
		if errors.As(err, &validation) {
			return "REG_VALIDATION_FAILED"
		}
		return "REG_INTERNAL"
	}
}

func (store *PostgresStore) String() string {
	if store == nil || store.pool == nil {
		return "PostgresStore(unconfigured)"
	}
	return fmt.Sprintf("PostgresStore(%p)", store.pool)
}
