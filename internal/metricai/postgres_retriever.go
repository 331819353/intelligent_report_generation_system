package metricai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
	"intelligent-report-generation-system/internal/platform/database"
)

const (
	postgresMetricMatchLimit = 24
	// Fetch one extra row so the host can fail closed instead of silently claiming that every
	// authorized ordinary dataset was inspected after truncating the catalog.
	postgresDatasetMatchLimit = maxDatasets + 1
	// Atomic facts are internal authoring evidence, never metric-center assets. Fetching one extra
	// row preserves the same fail-closed bounded-context guarantee used for datasets.
	postgresAtomicFactLimit = maxAtomicFacts + 1
)

// retrievalRows and retrievalQueryer keep the Postgres implementation unit-testable without
// weakening the production constructor, which always runs inside a tenant-scoped RLS transaction.
type retrievalRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

type retrievalQueryer interface {
	Query(context.Context, string, ...any) (retrievalRows, error)
	ValidateDatasetVersion(context.Context, string, string) error
}

type tenantRetrievalRunner func(context.Context, string, func(retrievalQueryer) error) error

type pgxRetrievalQueryer struct{ tx pgx.Tx }

func (q pgxRetrievalQueryer) Query(ctx context.Context, sql string, arguments ...any) (retrievalRows, error) {
	return q.tx.Query(ctx, sql, arguments...)
}

func (q pgxRetrievalQueryer) ValidateDatasetVersion(ctx context.Context, datasetID, versionID string) error {
	return dataset.ValidateVersionDependenciesInTx(ctx, q.tx, datasetID, versionID)
}

// PostgresRetriever returns readable published snapshots plus current drafts for which the actor
// has both effective READ and MANAGE. It never queries source rows, samples, secrets, credentials,
// or connector metadata.
type PostgresRetriever struct{ runTenant tenantRetrievalRunner }

func NewPostgresRetriever(pool *pgxpool.Pool) *PostgresRetriever {
	return &PostgresRetriever{runTenant: func(ctx context.Context, tenantID string, work func(retrievalQueryer) error) error {
		if pool == nil {
			return errors.New("metric AI Postgres retriever is not configured")
		}
		return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			return work(pgxRetrievalQueryer{tx: tx})
		})
	}}
}

type datasetSnapshot struct {
	ID, VersionID     string
	VersionNo         int
	SearchName        string
	SearchDescription string
	Status            string
	Manageable        bool
	Mapped            bool
	DSLHash, PlanHash string
	DSL               json.RawMessage
}

type metricSnapshot struct {
	ID, VersionID, Status, DefinitionHash string
	Definition                            json.RawMessage
	Dataset                               datasetSnapshot
}

type atomicFactSnapshot struct {
	DatasetID, DatasetVersionID string
	SourceFieldIDs              []string
	Name, Description, Caliber  string
	Aggregation, Period         string
	Dimensions, Tags            []string
	Confidence                  float64
}

// Retrieve builds a minimal semantic snapshot. Dataset DSL is parsed only inside the trusted
// process; the returned context contains logical dataset/field/metric metadata and exact hashes,
// never the DSL's physical nodes, filter literals, parameters, or source identifiers.
func (r *PostgresRetriever) Retrieve(ctx context.Context, tenantID, actorID string, raw AuthoringRequest) (RetrievalContext, error) {
	if r == nil || r.runTenant == nil {
		return RetrievalContext{}, errors.New("metric AI Postgres retriever is not configured")
	}
	if !canonicalUUID(strings.TrimSpace(tenantID)) || !canonicalUUID(strings.TrimSpace(actorID)) {
		return RetrievalContext{}, fmt.Errorf("%w: tenant and actor must be canonical UUIDs", ErrInvalidRetrievalContext)
	}
	request, err := normalizeRequest(raw)
	if err != nil {
		return RetrievalContext{}, err
	}
	searchText := request.Requirement

	metricRows := []metricSnapshot{}
	datasetRows := []datasetSnapshot{}
	draftDatasetRows := []datasetSnapshot{}
	atomicFactRows := []atomicFactSnapshot{}
	err = r.runTenant(ctx, tenantID, func(queryer retrievalQueryer) error {
		var queryErr error
		metricRows, queryErr = queryAuthorizedMetrics(ctx, queryer, tenantID, actorID, searchText, searchText)
		if queryErr != nil {
			return queryErr
		}
		datasetRows, queryErr = queryAuthorizedDatasets(ctx, queryer, tenantID, actorID, searchText, searchText)
		if queryErr != nil {
			return queryErr
		}
		draftDatasetRows, queryErr = queryAuthorizedDraftDatasets(ctx, queryer, tenantID, actorID, searchText, searchText)
		if queryErr != nil {
			return queryErr
		}
		atomicFactRows, queryErr = queryAuthorizedAtomicFacts(ctx, queryer, tenantID, actorID, searchText, searchText)
		if queryErr != nil {
			return queryErr
		}
		// Dependency validation is a publication contract. A current DRAFT is instead
		// validated locally from its canonical DSL and exact hashes below.
		metricRows, datasetRows, queryErr = retainUsableDatasetVersions(ctx, queryer, metricRows, datasetRows)
		return queryErr
	})
	if err != nil {
		return RetrievalContext{}, err
	}
	return buildRetrievalContext(metricRows, datasetRows, draftDatasetRows, atomicFactRows)
}

func retainUsableDatasetVersions(ctx context.Context, queryer retrievalQueryer, metricRows []metricSnapshot, datasetRows []datasetSnapshot) ([]metricSnapshot, []datasetSnapshot, error) {
	valid := map[string]bool{}
	checked := map[string]bool{}
	check := func(snapshot datasetSnapshot) error {
		key := datasetKey(snapshot.ID, snapshot.VersionID)
		if checked[key] {
			return nil
		}
		checked[key] = true
		err := queryer.ValidateDatasetVersion(ctx, snapshot.ID, snapshot.VersionID)
		if errors.Is(err, dataset.ErrVersionNotFound) || errors.Is(err, dataset.ErrVersionUnavailable) {
			return nil
		}
		if err != nil {
			return err
		}
		valid[key] = true
		return nil
	}
	for _, item := range metricRows {
		if err := check(item.Dataset); err != nil {
			return nil, nil, err
		}
	}
	for _, item := range datasetRows {
		if err := check(item); err != nil {
			return nil, nil, err
		}
	}
	filteredMetrics := make([]metricSnapshot, 0, len(metricRows))
	for _, item := range metricRows {
		if valid[datasetKey(item.Dataset.ID, item.Dataset.VersionID)] {
			filteredMetrics = append(filteredMetrics, item)
		}
	}
	filteredDatasets := make([]datasetSnapshot, 0, len(datasetRows))
	for _, item := range datasetRows {
		if valid[datasetKey(item.ID, item.VersionID)] {
			filteredDatasets = append(filteredDatasets, item)
		}
	}
	return filteredMetrics, filteredDatasets, nil
}

func queryAuthorizedDatasets(ctx context.Context, queryer retrievalQueryer, tenantID, actorID, name, searchText string) ([]datasetSnapshot, error) {
	return queryDatasetSnapshots(ctx, queryer, authorizedDatasetsSQL, tenantID, actorID, name, searchText)
}

func queryAuthorizedDraftDatasets(ctx context.Context, queryer retrievalQueryer, tenantID, actorID, name, searchText string) ([]datasetSnapshot, error) {
	return queryDatasetSnapshots(ctx, queryer, authorizedDraftDatasetsSQL, tenantID, actorID, name, searchText)
}

func queryDatasetSnapshots(ctx context.Context, queryer retrievalQueryer, query, tenantID, actorID, name, searchText string) ([]datasetSnapshot, error) {
	rows, err := queryer.Query(ctx, query, tenantID, actorID, name, searchText, postgresDatasetMatchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []datasetSnapshot{}
	for rows.Next() {
		var item datasetSnapshot
		if err := rows.Scan(
			&item.ID, &item.VersionID, &item.VersionNo, &item.SearchName, &item.SearchDescription,
			&item.Status, &item.Manageable, &item.Mapped, &item.DSLHash, &item.PlanHash, &item.DSL,
		); err != nil {
			return nil, err
		}
		item.DSL = append(json.RawMessage(nil), item.DSL...)
		result = append(result, item)
	}
	return result, rows.Err()
}

func queryAuthorizedMetrics(ctx context.Context, queryer retrievalQueryer, tenantID, actorID, name, searchText string) ([]metricSnapshot, error) {
	rows, err := queryer.Query(ctx, authorizedMetricsSQL, tenantID, actorID, name, searchText, postgresMetricMatchLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []metricSnapshot{}
	for rows.Next() {
		var item metricSnapshot
		if err := rows.Scan(
			&item.ID, &item.VersionID, &item.Status, &item.DefinitionHash, &item.Definition,
			&item.Dataset.ID, &item.Dataset.VersionID, &item.Dataset.VersionNo,
			&item.Dataset.SearchName, &item.Dataset.SearchDescription, &item.Dataset.Status,
			&item.Dataset.Manageable, &item.Dataset.Mapped, &item.Dataset.DSLHash, &item.Dataset.PlanHash, &item.Dataset.DSL,
		); err != nil {
			return nil, err
		}
		item.Definition = append(json.RawMessage(nil), item.Definition...)
		item.Dataset.DSL = append(json.RawMessage(nil), item.Dataset.DSL...)
		result = append(result, item)
	}
	return result, rows.Err()
}

func queryAuthorizedAtomicFacts(ctx context.Context, queryer retrievalQueryer, tenantID, actorID, name, searchText string) ([]atomicFactSnapshot, error) {
	rows, err := queryer.Query(ctx, authorizedAtomicFactsSQL, tenantID, actorID, name, searchText, postgresAtomicFactLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []atomicFactSnapshot{}
	for rows.Next() {
		var item atomicFactSnapshot
		if err := rows.Scan(
			&item.DatasetID, &item.DatasetVersionID, &item.SourceFieldIDs,
			&item.Name, &item.Description, &item.Caliber, &item.Aggregation,
			&item.Dimensions, &item.Period, &item.Tags, &item.Confidence,
		); err != nil {
			return nil, err
		}
		item.SourceFieldIDs = append([]string(nil), item.SourceFieldIDs...)
		item.Dimensions = append([]string(nil), item.Dimensions...)
		item.Tags = append([]string(nil), item.Tags...)
		result = append(result, item)
	}
	return result, rows.Err()
}

func buildRetrievalContext(metricRows []metricSnapshot, datasetRows, draftDatasetRows []datasetSnapshot, atomicFactGroups ...[]atomicFactSnapshot) (RetrievalContext, error) {
	publishedOrder := []string{}
	draftOrder := []string{}
	mappedOrder := []string{}
	byPair := map[string]datasetSnapshot{}
	categoryByPair := map[string]string{}
	addDataset := func(value datasetSnapshot, category string) error {
		if category == "draft" && (value.Status != "DRAFT" || !value.Manageable || value.Mapped) {
			return fmt.Errorf("%w: modifiable dataset snapshot is not a manageable draft", ErrInvalidRetrievalContext)
		}
		if category != "draft" && value.Status != "PUBLISHED" {
			return fmt.Errorf("%w: published dataset snapshot has invalid status", ErrInvalidRetrievalContext)
		}
		if category == "mapped" && !value.Mapped || category == "published" && value.Mapped {
			return fmt.Errorf("%w: dataset snapshot is in the wrong authoring tier", ErrInvalidRetrievalContext)
		}
		key := datasetKey(value.ID, value.VersionID)
		if existing, found := byPair[key]; found {
			if categoryByPair[key] != category || existing.VersionNo != value.VersionNo || existing.Status != value.Status ||
				existing.Manageable != value.Manageable || existing.Mapped != value.Mapped ||
				existing.DSLHash != value.DSLHash || existing.PlanHash != value.PlanHash ||
				!bytes.Equal(existing.DSL, value.DSL) {
				return fmt.Errorf("%w: inconsistent duplicate dataset snapshot", ErrInvalidRetrievalContext)
			}
			return nil
		}
		if len(publishedOrder)+len(draftOrder)+len(mappedOrder) >= maxDatasets {
			return fmt.Errorf("%w: authorized datasets exceed complete-search budget", ErrInvalidRetrievalContext)
		}
		byPair[key] = value
		categoryByPair[key] = category
		switch category {
		case "draft":
			draftOrder = append(draftOrder, key)
		case "mapped":
			mappedOrder = append(mappedOrder, key)
		default:
			publishedOrder = append(publishedOrder, key)
		}
		return nil
	}
	// Metric matches are added first so an exact reusable metric cannot lose its dataset merely
	// because the tenant has more than the bounded number of recently edited datasets.
	for _, item := range metricRows {
		category := "published"
		if item.Dataset.Mapped {
			category = "mapped"
		}
		if err := addDataset(item.Dataset, category); err != nil {
			return RetrievalContext{}, err
		}
	}
	for _, item := range datasetRows {
		if item.Mapped {
			continue
		}
		if err := addDataset(item, "published"); err != nil {
			return RetrievalContext{}, err
		}
	}
	for _, item := range draftDatasetRows {
		if item.Mapped {
			continue
		}
		if err := addDataset(item, "draft"); err != nil {
			return RetrievalContext{}, err
		}
	}
	for _, item := range datasetRows {
		if !item.Mapped {
			continue
		}
		if err := addDataset(item, "mapped"); err != nil {
			return RetrievalContext{}, err
		}
	}

	result := RetrievalContext{
		Datasets: []AuthorizedDataset{}, Fields: []AuthorizedField{}, ExistingMetrics: []AuthorizedMetric{},
		ModifiableDraftDatasets: []AuthorizedDataset{}, ModifiableDraftFields: []AuthorizedField{},
		MappedDatasets: []AuthorizedDataset{}, MappedFields: []AuthorizedField{}, AtomicFacts: []AuthorizedAtomicFact{},
	}
	appendDataset := func(key, category string) error {
		snapshot := byPair[key]
		prepared, err := dataset.Prepare(snapshot.DSL)
		if err != nil || prepared.DSLHash != snapshot.DSLHash || prepared.PlanHash != snapshot.PlanHash {
			kind := "published"
			if category == "draft" {
				kind = "draft"
			} else if category == "mapped" {
				kind = "mapped"
			}
			return fmt.Errorf("%w: %s dataset snapshot failed hash validation", ErrInvalidRetrievalContext, kind)
		}
		document := prepared.Document
		visibleFieldCount := 0
		for _, field := range document.Fields {
			if field.Visible == nil || *field.Visible {
				visibleFieldCount++
			}
		}
		if len(result.Fields)+len(result.ModifiableDraftFields)+len(result.MappedFields)+visibleFieldCount > maxFields {
			return fmt.Errorf("%w: authorized dataset fields exceed bounded context", ErrInvalidRetrievalContext)
		}
		datasetValue := AuthorizedDataset{
			ID: snapshot.ID, VersionID: snapshot.VersionID, VersionNo: snapshot.VersionNo,
			Name: document.Dataset.Name, Description: document.Dataset.Description, Status: snapshot.Status,
			DSLHash: snapshot.DSLHash, Aggregated: datasetDocumentAggregated(document), Mapped: snapshot.Mapped, Manageable: snapshot.Manageable,
		}
		switch category {
		case "draft":
			result.ModifiableDraftDatasets = append(result.ModifiableDraftDatasets, datasetValue)
		case "mapped":
			result.MappedDatasets = append(result.MappedDatasets, datasetValue)
		default:
			result.Datasets = append(result.Datasets, datasetValue)
		}
		for _, field := range document.Fields {
			if field.Visible != nil && !*field.Visible {
				continue
			}
			fieldValue := AuthorizedField{
				DatasetID: snapshot.ID, DatasetVersionID: snapshot.VersionID,
				ID: field.ID, Code: field.Code, Name: field.Name, Description: field.Description,
				CanonicalType: field.CanonicalType, Role: field.Role, SemanticType: field.SemanticType,
			}
			switch category {
			case "draft":
				result.ModifiableDraftFields = append(result.ModifiableDraftFields, fieldValue)
			case "mapped":
				result.MappedFields = append(result.MappedFields, fieldValue)
			default:
				result.Fields = append(result.Fields, fieldValue)
			}
		}
		return nil
	}
	for _, key := range publishedOrder {
		if err := appendDataset(key, "published"); err != nil {
			return RetrievalContext{}, err
		}
	}
	for _, key := range draftOrder {
		if err := appendDataset(key, "draft"); err != nil {
			return RetrievalContext{}, err
		}
	}
	for _, key := range mappedOrder {
		if err := appendDataset(key, "mapped"); err != nil {
			return RetrievalContext{}, err
		}
	}

	authorizedFields := make(map[string]bool, len(result.Fields)+len(result.MappedFields))
	for _, field := range result.Fields {
		authorizedFields[fieldKey(field.DatasetID, field.DatasetVersionID, field.ID)] = true
	}
	for _, field := range result.MappedFields {
		authorizedFields[fieldKey(field.DatasetID, field.DatasetVersionID, field.ID)] = true
	}
	if len(atomicFactGroups) > 1 || len(atomicFactGroups) == 1 && len(atomicFactGroups[0]) > maxAtomicFacts {
		return RetrievalContext{}, fmt.Errorf("%w: authorized atomic facts exceed bounded context", ErrInvalidRetrievalContext)
	}
	if len(atomicFactGroups) == 1 {
		for _, snapshot := range atomicFactGroups[0] {
			pair := datasetKey(snapshot.DatasetID, snapshot.DatasetVersionID)
			if _, found := byPair[pair]; !found || categoryByPair[pair] == "draft" {
				continue
			}
			authorized := len(snapshot.SourceFieldIDs) > 0
			for _, fieldID := range snapshot.SourceFieldIDs {
				if !authorizedFields[fieldKey(snapshot.DatasetID, snapshot.DatasetVersionID, fieldID)] {
					authorized = false
					break
				}
			}
			if !authorized {
				continue
			}
			result.AtomicFacts = append(result.AtomicFacts, AuthorizedAtomicFact{
				DatasetID: snapshot.DatasetID, DatasetVersionID: snapshot.DatasetVersionID,
				SourceFieldIDs: append([]string(nil), snapshot.SourceFieldIDs...),
				Name:           snapshot.Name, Description: snapshot.Description, Caliber: snapshot.Caliber,
				Aggregation: snapshot.Aggregation, Dimensions: append([]string(nil), snapshot.Dimensions...),
				Period: snapshot.Period, Tags: append([]string(nil), snapshot.Tags...), Confidence: snapshot.Confidence,
			})
		}
	}
	seenMetrics := map[string]bool{}
	for _, snapshot := range metricRows {
		if len(result.ExistingMetrics) >= maxExistingMetrics {
			break
		}
		if _, found := byPair[datasetKey(snapshot.Dataset.ID, snapshot.Dataset.VersionID)]; !found || seenMetrics[snapshot.VersionID] {
			continue
		}
		prepared, err := metric.Prepare(snapshot.Definition)
		if err != nil || prepared.DefinitionHash != snapshot.DefinitionHash ||
			prepared.Definition.Metric.Type != "ATOMIC" || len(prepared.DependencyVersionIDs) != 0 ||
			prepared.Definition.DatasetID != snapshot.Dataset.ID || prepared.Definition.DatasetVersionID != snapshot.Dataset.VersionID {
			return RetrievalContext{}, fmt.Errorf("%w: published metric snapshot failed definition validation", ErrInvalidRetrievalContext)
		}
		definition := prepared.Definition
		if !definitionUsesOnlyAuthorizedFields(definition, authorizedFields) {
			continue
		}
		result.ExistingMetrics = append(result.ExistingMetrics, AuthorizedMetric{
			ID: snapshot.ID, VersionID: snapshot.VersionID,
			Code: definition.Metric.Code, Name: definition.Metric.Name, Description: definition.Metric.Description,
			Status: snapshot.Status, DatasetID: definition.DatasetID, DatasetVersionID: definition.DatasetVersionID,
			DefinitionHash: prepared.DefinitionHash, Definition: definition,
		})
		seenMetrics[snapshot.VersionID] = true
	}
	return result, nil
}

func definitionUsesOnlyAuthorizedFields(definition metric.Definition, authorized map[string]bool) bool {
	references := map[string]bool{}
	collectExpressionFields(definition.Expression, references)
	for _, dimension := range definition.AllowedDimensions {
		references[dimension.FieldID] = true
		for _, fieldID := range dimension.HierarchyFieldIDs {
			references[fieldID] = true
		}
	}
	if definition.TimeFieldID != "" {
		references[definition.TimeFieldID] = true
	}
	for _, fieldID := range definition.NonAdditiveDimensionFieldIDs {
		references[fieldID] = true
	}
	for fieldID := range references {
		if !authorized[fieldKey(definition.DatasetID, definition.DatasetVersionID, fieldID)] {
			return false
		}
	}
	return true
}

func datasetDocumentAggregated(document dataset.Document) bool {
	if len(document.GroupBy) > 0 || len(document.Having) > 0 || len(document.PreAggregations) > 0 {
		return true
	}
	for _, field := range document.Fields {
		if datasetExpressionAggregated(field.Expression) {
			return true
		}
	}
	return false
}

func datasetExpressionAggregated(expression dataset.Expression) bool {
	if expression.Type == "AGGREGATE" {
		return true
	}
	for _, child := range []*dataset.Expression{expression.Argument, expression.Left, expression.Right, expression.Lower, expression.Upper, expression.Else} {
		if child != nil && datasetExpressionAggregated(*child) {
			return true
		}
	}
	for _, child := range expression.Arguments {
		if datasetExpressionAggregated(child) {
			return true
		}
	}
	for _, branch := range expression.Whens {
		if datasetExpressionAggregated(branch.When) || datasetExpressionAggregated(branch.Then) {
			return true
		}
	}
	return false
}

// These queries reproduce access.PostgresStore's effective permission semantics inside the same
// tenant/RLS transaction: active-role global grants or USER/active-ROLE object grants. SQL
// parameters contain only identity and authoring intent; no user text is interpolated into SQL.
const authorizationCTE = `WITH subject_roles AS MATERIALIZED (
  SELECT ur.role_id FROM platform.user_roles AS ur
  WHERE ur.tenant_id=$1 AND ur.user_id=$2
), actor_roles AS MATERIALIZED (
  SELECT subject.role_id
  FROM subject_roles AS subject
  JOIN platform.roles AS actor_role
    ON actor_role.tenant_id=$1 AND actor_role.id=subject.role_id
   AND actor_role.status='ACTIVE' AND actor_role.deleted_at IS NULL
), global_dataset_read AS MATERIALIZED (
  SELECT EXISTS(
    SELECT 1
    FROM platform.role_permissions AS rp
    JOIN platform.permissions AS permission
      ON permission.tenant_id=rp.tenant_id AND permission.id=rp.permission_id
    WHERE rp.tenant_id=$1
      AND rp.role_id IN (SELECT role_id FROM actor_roles)
      AND permission.resource_type='DATASET' AND permission.action='READ'
  ) AS allowed
), global_metric_read AS MATERIALIZED (
  SELECT EXISTS(
    SELECT 1
    FROM platform.role_permissions AS rp
    JOIN platform.permissions AS permission
      ON permission.tenant_id=rp.tenant_id AND permission.id=rp.permission_id
    WHERE rp.tenant_id=$1
      AND rp.role_id IN (SELECT role_id FROM actor_roles)
      AND permission.resource_type='METRIC' AND permission.action='READ'
  ) AS allowed
), global_dataset_manage AS MATERIALIZED (
  SELECT EXISTS(
    SELECT 1
    FROM platform.role_permissions AS rp
    JOIN platform.permissions AS permission
      ON permission.tenant_id=rp.tenant_id AND permission.id=rp.permission_id
    WHERE rp.tenant_id=$1
      AND rp.role_id IN (SELECT role_id FROM actor_roles)
      AND permission.resource_type='DATASET' AND permission.action='MANAGE'
  ) AS allowed
) `

const datasetReadPredicate = `(
  (SELECT allowed FROM global_dataset_read)
  OR EXISTS(
    SELECT 1 FROM platform.object_permissions AS object_grant
    WHERE object_grant.tenant_id=$1 AND object_grant.object_type='DATASET'
      AND object_grant.object_id=d.id AND object_grant.action='READ'
      AND (
        object_grant.subject_type='USER' AND object_grant.subject_id=$2
        OR object_grant.subject_type='ROLE' AND object_grant.subject_id IN (SELECT role_id FROM actor_roles)
      )
  )
)`

const metricReadPredicate = `(
  (SELECT allowed FROM global_metric_read)
  OR EXISTS(
    SELECT 1 FROM platform.object_permissions AS object_grant
    WHERE object_grant.tenant_id=$1 AND object_grant.object_type='METRIC'
      AND object_grant.object_id=m.id AND object_grant.action='READ'
      AND (
        object_grant.subject_type='USER' AND object_grant.subject_id=$2
        OR object_grant.subject_type='ROLE' AND object_grant.subject_id IN (SELECT role_id FROM actor_roles)
      )
  )
)`

const datasetManageExpression = `(
  (SELECT allowed FROM global_dataset_manage)
  OR EXISTS(
    SELECT 1 FROM platform.object_permissions AS manage_grant
    WHERE manage_grant.tenant_id=$1 AND manage_grant.object_type='DATASET'
      AND manage_grant.object_id=d.id AND manage_grant.action='MANAGE'
      AND (
        manage_grant.subject_type='USER' AND manage_grant.subject_id=$2
        OR manage_grant.subject_type='ROLE' AND manage_grant.subject_id IN (SELECT role_id FROM actor_roles)
      )
  )
)`

const noApplicableDataPolicyPredicate = `(
  NOT EXISTS(
    SELECT 1 FROM platform.data_row_policies AS row_policy
    WHERE row_policy.tenant_id=$1 AND row_policy.object_type='DATASET'
      AND row_policy.object_id=d.id AND row_policy.enabled
      AND (
        cardinality(row_policy.applicable_user_ids)=0 AND cardinality(row_policy.applicable_role_ids)=0
        OR $2=ANY(row_policy.applicable_user_ids)
        OR row_policy.applicable_role_ids && ARRAY(SELECT role_id FROM subject_roles)
      )
  )
  AND NOT EXISTS(
    SELECT 1 FROM platform.data_column_policies AS column_policy
    WHERE column_policy.tenant_id=$1 AND column_policy.object_type='DATASET'
      AND column_policy.object_id=d.id AND column_policy.enabled
      AND (
        cardinality(column_policy.applicable_user_ids)=0 AND cardinality(column_policy.applicable_role_ids)=0
        OR $2=ANY(column_policy.applicable_user_ids)
        OR column_policy.applicable_role_ids && ARRAY(SELECT role_id FROM subject_roles)
      )
  )
)`

const authorizedDatasetsSQL = authorizationCTE + `
SELECT d.id::text,version.id::text,version.version_no,d.name,d.description,version.status,
       ` + datasetManageExpression + ` AS manageable,
       (d.origin_table_id IS NOT NULL) AS mapped,
       version.schema_hash,version.plan_hash,version.dsl_json
FROM platform.datasets AS d
JOIN platform.dataset_versions AS version
  ON version.tenant_id=d.tenant_id AND version.dataset_id=d.id
 AND version.id=d.current_published_version_id
WHERE d.tenant_id=$1 AND d.deleted_at IS NULL AND d.status='PUBLISHED' AND version.status='PUBLISHED'
  AND ` + datasetReadPredicate + `
  AND ` + noApplicableDataPolicyPredicate + `
ORDER BY CASE WHEN d.origin_table_id IS NULL THEN 0 ELSE 1 END,
  CASE
  WHEN lower(d.name)=lower($3) OR lower(d.code::text)=lower($3) THEN 0
  WHEN strpos(lower($4),lower(d.name))>0 OR strpos(lower($4),lower(d.code::text))>0 THEN 1
  WHEN strpos(lower(d.name),lower($3))>0
    OR (d.description<>'' AND strpos(lower(d.description),lower($3))>0) THEN 2
  ELSE 3 END,
  d.updated_at DESC,d.id
LIMIT $5`

// Drafts are a separate capability surface: they must be the exact current draft of a live
// dataset and the actor must have both effective READ and MANAGE. The query reads only the
// canonical logical DSL envelope; it never joins source tables, samples, or connector secrets.
const authorizedDraftDatasetsSQL = authorizationCTE + `
SELECT d.id::text,version.id::text,version.version_no,d.name,d.description,version.status,
       ` + datasetManageExpression + ` AS manageable,
       (d.origin_table_id IS NOT NULL) AS mapped,
       version.schema_hash,version.plan_hash,version.dsl_json
FROM platform.datasets AS d
JOIN platform.dataset_versions AS version
  ON version.tenant_id=d.tenant_id AND version.dataset_id=d.id
 AND version.id=d.current_draft_version_id
WHERE d.tenant_id=$1 AND d.deleted_at IS NULL AND d.status<>'DISABLED' AND version.status='DRAFT'
	  AND d.origin_table_id IS NULL
  AND ` + datasetReadPredicate + `
  AND ` + datasetManageExpression + `
  AND ` + noApplicableDataPolicyPredicate + `
ORDER BY CASE
  WHEN lower(d.name)=lower($3) OR lower(d.code::text)=lower($3) THEN 0
  WHEN strpos(lower($4),lower(d.name))>0 OR strpos(lower($4),lower(d.code::text))>0 THEN 1
  WHEN strpos(lower(d.name),lower($3))>0
    OR (d.description<>'' AND strpos(lower(d.description),lower($3))>0) THEN 2
  ELSE 3 END,
  d.updated_at DESC,d.id
LIMIT $5`

const authorizedMetricsSQL = authorizationCTE + `
SELECT m.id::text,metric_version.id::text,metric_version.status,
       metric_version.definition_hash,metric_version.definition_json,
       d.id::text,dataset_version.id::text,dataset_version.version_no,d.name,d.description,
       dataset_version.status,` + datasetManageExpression + ` AS manageable,
       (d.origin_table_id IS NOT NULL) AS mapped,
       dataset_version.schema_hash,dataset_version.plan_hash,dataset_version.dsl_json
FROM platform.metrics AS m
JOIN platform.metric_versions AS metric_version
  ON metric_version.tenant_id=m.tenant_id AND metric_version.metric_id=m.id
 AND metric_version.id=m.current_published_version_id
JOIN platform.datasets AS d
  ON d.tenant_id=m.tenant_id AND d.id=m.dataset_id AND d.deleted_at IS NULL
JOIN platform.dataset_versions AS dataset_version
  ON dataset_version.tenant_id=d.tenant_id AND dataset_version.dataset_id=d.id
 AND dataset_version.id=d.current_published_version_id
 AND metric_version.dataset_id=d.id
 AND metric_version.dataset_version_id=dataset_version.id
WHERE m.tenant_id=$1 AND m.deleted_at IS NULL AND m.status='PUBLISHED' AND m.metric_type='ATOMIC'
  AND d.status='PUBLISHED'
  AND metric_version.status='PUBLISHED' AND dataset_version.status='PUBLISHED'
  AND ` + datasetReadPredicate + `
  AND ` + metricReadPredicate + `
  AND ` + noApplicableDataPolicyPredicate + `
ORDER BY CASE
  WHEN lower(m.name)=lower($3) OR lower(m.code::text)=lower($3) THEN 0
  WHEN strpos(lower($4),lower(m.name))>0 OR strpos(lower($4),lower(m.code::text))>0 THEN 1
  WHEN strpos(lower(m.name),lower($3))>0
    OR (m.description<>'' AND strpos(lower(m.description),lower($3))>0) THEN 2
  ELSE 3 END,
  m.updated_at DESC,m.id
LIMIT $5`

// Atomic facts are derived from immutable dataset DAGs and semantic enrichment. They are exposed
// only to the metric-authoring prompt, constrained by the same dataset authorization and policy
// fences as fields, and must still belong to the dataset's current published version.
const authorizedAtomicFactsSQL = authorizationCTE + `
SELECT candidate.dataset_id::text,candidate.dataset_version_id::text,candidate.source_field_ids,
       semantic.name,semantic.description,semantic.caliber,
       COALESCE(candidate.proposed_definition->>'aggregation',''),
       semantic.dimensions,semantic.period,semantic.tags,candidate.confidence::float8
FROM platform.metric_candidates AS candidate
JOIN platform.metric_semantic_documents AS semantic
  ON semantic.tenant_id=candidate.tenant_id AND semantic.subject_type='CANDIDATE'
 AND semantic.candidate_id=candidate.id
JOIN platform.datasets AS d
  ON d.tenant_id=candidate.tenant_id AND d.id=candidate.dataset_id
 AND d.deleted_at IS NULL AND d.current_published_version_id=candidate.dataset_version_id
JOIN platform.dataset_versions AS version
  ON version.tenant_id=d.tenant_id AND version.dataset_id=d.id
 AND version.id=candidate.dataset_version_id
WHERE candidate.tenant_id=$1 AND candidate.status IN ('READY','NEEDS_REVIEW')
  AND d.status='PUBLISHED' AND version.status='PUBLISHED'
  AND ` + datasetReadPredicate + `
  AND ` + noApplicableDataPolicyPredicate + `
ORDER BY CASE
  WHEN lower(semantic.name)=lower($3) THEN 0
  WHEN strpos(lower($4),lower(semantic.name))>0 OR strpos(lower($4),lower(d.name))>0 THEN 1
  WHEN strpos(lower(semantic.name),lower($3))>0
    OR (semantic.description<>'' AND strpos(lower(semantic.description),lower($3))>0) THEN 2
  ELSE 3 END,
  candidate.confidence DESC,candidate.id
LIMIT $5`

var _ Retriever = (*PostgresRetriever)(nil)
