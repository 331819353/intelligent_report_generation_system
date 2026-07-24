package datasettagsuggestion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (store *PostgresStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidRequest
	}
	rows, err := store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (store *PostgresStore) ClaimNext(
	ctx context.Context,
	tenantID string,
	workerID string,
	lease time.Duration,
) (claim *Claim, err error) {
	if store == nil || store.pool == nil || strings.TrimSpace(tenantID) == "" ||
		!validWorkerID(workerID) || lease < time.Second || lease > time.Hour {
		return nil, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.dataset_tag_suggestion_jobs
			SET status='FAILED',error_code='LEASE_EXPIRED',
				error_message='worker lease expired after maximum attempts',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts`); err != nil {
			return err
		}
		item := Claim{TenantID: tenantID}
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT id
			FROM platform.dataset_tag_suggestion_jobs
			WHERE attempt<max_attempts
			  AND (
			    (status='PENDING' AND next_attempt_at<=now())
			    OR (status='RUNNING' AND lease_expires_at<=now())
			  )
			ORDER BY created_at,id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE platform.dataset_tag_suggestion_jobs AS job
		SET status='RUNNING',attempt=attempt+1,error_code='',error_message='',
			lease_owner=$1,lease_token=public.gen_random_uuid(),
			lease_expires_at=now()+($2*interval '1 second'),
			started_at=COALESCE(started_at,now()),completed_at=NULL,updated_at=now()
		FROM candidate
		WHERE job.id=candidate.id
		RETURNING job.id::text,job.dataset_id::text,job.dataset_version_id::text,
			job.schema_hash,job.layer,job.prompt_version,
			COALESCE(job.requested_by::text,''),job.lease_token::text,
			job.attempt,job.max_attempts`,
			workerID, int64(lease/time.Second),
		).Scan(
			&item.ID, &item.DatasetID, &item.DatasetVersionID,
			&item.SchemaHash, &item.Layer, &item.PromptVersion,
			&item.ActorID, &item.LeaseToken, &item.Attempt, &item.MaxAttempts,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		claim = &item
		return nil
	})
	return claim, err
}

const heartbeatJobSQL = `UPDATE platform.dataset_tag_suggestion_jobs
	SET lease_expires_at=GREATEST(
			lease_expires_at,
			clock_timestamp()+($1*interval '1 millisecond')
		),
		updated_at=now()
	WHERE id=$2::uuid AND status='RUNNING' AND lease_owner=$3
	  AND lease_token=$4::uuid AND attempt=$5
	  AND dataset_version_id=$6::uuid
	  AND lease_expires_at>clock_timestamp()`

// Heartbeat extends only the exact claim generation identified by the owner,
// random token, attempt and immutable dataset version. It must never revive an
// expired or reclaimed job.
func (store *PostgresStore) Heartbeat(
	ctx context.Context,
	claim Claim,
	workerID string,
	lease time.Duration,
) error {
	if store == nil || store.pool == nil || !validClaim(claim) ||
		!validWorkerID(workerID) || lease < time.Second || lease > time.Hour {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(
			ctx, heartbeatJobSQL, lease.Milliseconds(), claim.ID, workerID,
			claim.LeaseToken, claim.Attempt, claim.DatasetVersionID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

type sourceVersionRef struct {
	DataSourceID        string `json:"dataSourceId"`
	DataSourceVersionID string `json:"dataSourceVersionId"`
}

type rootSnapshot struct {
	Code                  string
	Name                  string
	Description           string
	DatasetType           string
	Layer                 string
	SchemaHash            string
	DSL                   json.RawMessage
	SourceVersionSnapshot json.RawMessage
}

func (store *PostgresStore) LoadInput(
	ctx context.Context,
	claim Claim,
	workerID string,
) (input Input, err error) {
	if store == nil || store.pool == nil || !validClaim(claim) || !validWorkerID(workerID) {
		return Input{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var loadErr error
		input, loadErr = store.loadInputTx(ctx, tx, claim, workerID, false)
		return loadErr
	})
	return input, err
}

func (store *PostgresStore) loadInputTx(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	workerID string,
	jobAlreadyLocked bool,
) (Input, error) {
	root, err := loadRoot(ctx, tx, claim, workerID, jobAlreadyLocked)
	if err != nil {
		return Input{}, err
	}
	if claim.ActorID == "" {
		return Input{}, ErrSubjectChanged
	}
	var actorActive bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.users
		WHERE id=$1::uuid AND tenant_id=platform.current_tenant_id()
		  AND status='ACTIVE' AND deleted_at IS NULL
	)`, claim.ActorID).Scan(&actorActive); err != nil {
		return Input{}, err
	}
	if !actorActive {
		return Input{}, ErrSubjectChanged
	}

	prepared, err := dataset.Prepare(root.DSL)
	if err != nil || prepared.DSLHash != root.SchemaHash ||
		prepared.DSLHash != claim.SchemaHash ||
		string(prepared.Document.Dataset.Layer) != claim.Layer {
		return Input{}, ErrSubjectChanged
	}
	fields, err := datasetFields(prepared.Document)
	if err != nil {
		return Input{}, err
	}
	input := Input{
		Dataset: DatasetContext{
			Code: root.Code, Name: root.Name, Description: root.Description,
			Layer: root.Layer, Type: root.DatasetType,
			VersionID: claim.DatasetVersionID, SchemaHash: root.SchemaHash,
		},
		Fields: fields,
		DAG:    dagContext(prepared.Document),
	}

	tables, upstreams, err := loadAndValidateDependencies(
		ctx, tx, claim, root.SourceVersionSnapshot, prepared.Document,
	)
	if err != nil {
		return Input{}, err
	}
	input.SourceTables = tables
	input.Upstreams = upstreams
	if input.Taxonomy, err = loadTaxonomy(ctx, tx); err != nil {
		return Input{}, err
	}
	if exceedsInputLimits(input) {
		return Input{}, ErrInputLimit
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Input{}, err
	}
	if len(raw) > MaxInputBytes {
		return Input{}, ErrInputLimit
	}
	return input, nil
}

func loadRoot(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	workerID string,
	jobAlreadyLocked bool,
) (root rootSnapshot, err error) {
	lock := "FOR SHARE OF job,version,dataset"
	if jobAlreadyLocked {
		lock = "FOR SHARE OF version,dataset"
	}
	query := `SELECT
			dataset.code::text,dataset.name,dataset.description,dataset.dataset_type,
			version.layer,version.schema_hash,version.dsl_json,
			job.source_version_snapshot
		FROM platform.dataset_tag_suggestion_jobs AS job
		JOIN platform.dataset_versions AS version
		  ON version.id=job.dataset_version_id
		 AND version.dataset_id=job.dataset_id
		 AND version.tenant_id=job.tenant_id
		JOIN platform.datasets AS dataset
		  ON dataset.id=version.dataset_id
		 AND dataset.tenant_id=version.tenant_id
		WHERE job.id=$1::uuid
		  AND job.dataset_id=$2::uuid
		  AND job.dataset_version_id=$3::uuid
		  AND job.schema_hash=$4
		  AND job.layer=$5
		  AND job.prompt_version=$6
		  AND job.status='RUNNING'
		  AND job.lease_owner=$7
		  AND job.lease_token=$8::uuid
		  AND job.lease_expires_at>now()
		  AND job.source_version_snapshot_hash=
		    encode(public.digest(job.source_version_snapshot::text,'sha256'),'hex')
		  AND version.status='PUBLISHED'
		  AND version.schema_hash=job.schema_hash
		  AND version.layer=job.layer
		  AND dataset.current_published_version_id=version.id
		  AND dataset.status='PUBLISHED'
		  AND dataset.deleted_at IS NULL
		` + lock
	err = tx.QueryRow(
		ctx, query,
		claim.ID, claim.DatasetID, claim.DatasetVersionID, claim.SchemaHash,
		claim.Layer, claim.PromptVersion, workerID, claim.LeaseToken,
	).Scan(
		&root.Code, &root.Name, &root.Description, &root.DatasetType,
		&root.Layer, &root.SchemaHash, &root.DSL, &root.SourceVersionSnapshot,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		var leaseOwned bool
		leaseErr := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.dataset_tag_suggestion_jobs
			WHERE id=$1::uuid AND status='RUNNING' AND lease_owner=$2
			  AND lease_token=$3::uuid AND lease_expires_at>now()
		)`, claim.ID, workerID, claim.LeaseToken).Scan(&leaseOwned)
		if leaseErr != nil {
			return rootSnapshot{}, leaseErr
		}
		if !leaseOwned {
			return rootSnapshot{}, ErrLeaseLost
		}
		return rootSnapshot{}, ErrSubjectChanged
	}
	return root, err
}

func loadAndValidateDependencies(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	sourceSnapshot json.RawMessage,
	document dataset.Document,
) ([]SourceTableContext, []UpstreamContext, error) {
	var tableExpected, fileExpected, datasetExpected int
	if err := tx.QueryRow(ctx, `SELECT
		count(*) FILTER(WHERE source_type='TABLE')::int,
		count(*) FILTER(WHERE source_type='FILE_VERSION')::int,
		count(*) FILTER(WHERE source_type='DATASET_VERSION')::int
		FROM platform.dataset_dependencies
		WHERE dataset_version_id=$1::uuid`,
		claim.DatasetVersionID,
	).Scan(&tableExpected, &fileExpected, &datasetExpected); err != nil {
		return nil, nil, err
	}
	if tableExpected+fileExpected+datasetExpected == 0 ||
		(claim.Layer == "ODS" && (tableExpected == 0 || datasetExpected != 0)) ||
		(claim.Layer != "ODS" && (tableExpected != 0 || fileExpected != 0 || datasetExpected == 0)) {
		return nil, nil, ErrSubjectChanged
	}

	tables, refs, err := loadSourceTables(ctx, tx, claim.DatasetVersionID, document)
	if err != nil {
		return nil, nil, err
	}
	if len(tables) != tableExpected || !sameSourceVersionSnapshot(sourceSnapshot, refs) {
		return nil, nil, ErrSubjectChanged
	}
	fileCount, err := validateFileDependencies(ctx, tx, claim.DatasetVersionID)
	if err != nil {
		return nil, nil, err
	}
	if fileCount != fileExpected {
		return nil, nil, ErrSubjectChanged
	}
	upstreams, err := loadUpstreams(ctx, tx, claim.DatasetVersionID, claim.Layer)
	if err != nil {
		return nil, nil, err
	}
	if len(upstreams) != datasetExpected {
		return nil, nil, ErrSubjectChanged
	}
	return tables, upstreams, nil
}

func loadSourceTables(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
	document dataset.Document,
) ([]SourceTableContext, []sourceVersionRef, error) {
	rows, err := tx.Query(ctx, `SELECT
		dependency.source_id,dependency.source_version,dependency.source_hash,
		source_table.metadata_version,source_table.structure_hash,
		source_table.catalog_name,source_table.schema_name,source_table.table_name,
		source_table.table_type,source_table.source_comment,
		source_table.business_name,source_table.business_description,source_table.tags,
		source_table.primary_key_columns,
		source.id::text,source.source_type::text,source.current_published_version_id::text
		FROM platform.dataset_dependencies AS dependency
		JOIN platform.metadata_tables AS source_table
		  ON source_table.id::text=dependency.source_id
		 AND source_table.tenant_id=dependency.tenant_id
		 AND source_table.asset_status='ACTIVE'
		 AND source_table.management_status='ENABLED'
		JOIN platform.data_sources AS source
		  ON source.id=source_table.data_source_id
		 AND source.tenant_id=source_table.tenant_id
		 AND source.status='ACTIVE'
		 AND source.publication_status='PUBLISHED'
		 AND source.current_published_version_id IS NOT NULL
		JOIN platform.data_source_versions AS source_version
		  ON source_version.id=source.current_published_version_id
		 AND source_version.data_source_id=source.id
		 AND source_version.tenant_id=source.tenant_id
		WHERE dependency.dataset_version_id=$1::uuid
		  AND dependency.source_type='TABLE'
		ORDER BY dependency.source_id
		FOR SHARE OF dependency,source_table,source,source_version`, versionID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	tables := []SourceTableContext{}
	projections := [][]string{}
	refsBySource := map[string]string{}
	for rows.Next() {
		var table SourceTableContext
		var dependencyVersion, metadataVersion int64
		var dependencyHash, structureHash string
		var primaryKeysRaw json.RawMessage
		var dataSourceID, dataSourceVersionID string
		if err := rows.Scan(
			&table.ID, &dependencyVersion, &dependencyHash,
			&metadataVersion, &structureHash,
			&table.CatalogName, &table.SchemaName, &table.TableName,
			&table.TableType, &table.SourceComment,
			&table.BusinessName, &table.BusinessDescription, &table.Tags,
			&primaryKeysRaw,
			&dataSourceID, &table.DataSourceType, &dataSourceVersionID,
		); err != nil {
			return nil, nil, err
		}
		if dependencyVersion != metadataVersion || dependencyHash != structureHash ||
			dataSourceVersionID == "" {
			return nil, nil, ErrSubjectChanged
		}
		if err := json.Unmarshal(primaryKeysRaw, &table.PrimaryKeys); err != nil {
			return nil, nil, ErrSubjectChanged
		}
		tables = append(tables, table)
		projections = append(projections, projectedColumns(document, table.ID))
		refsBySource[dataSourceID] = dataSourceVersionID
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()
	for index := range tables {
		columns, err := loadSourceColumns(
			ctx, tx, tables[index].ID, projections[index],
		)
		if err != nil {
			return nil, nil, err
		}
		if len(columns) != len(projections[index]) {
			return nil, nil, ErrSubjectChanged
		}
		tables[index].Columns = columns
	}
	refs := make([]sourceVersionRef, 0, len(refsBySource))
	for sourceID, sourceVersionID := range refsBySource {
		refs = append(refs, sourceVersionRef{
			DataSourceID: sourceID, DataSourceVersionID: sourceVersionID,
		})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].DataSourceID < refs[j].DataSourceID
	})
	return tables, refs, nil
}

func loadSourceColumns(
	ctx context.Context,
	tx pgx.Tx,
	tableID string,
	projection []string,
) ([]SourceColumnContext, error) {
	if len(projection) == 0 {
		return nil, ErrSubjectChanged
	}
	rows, err := tx.Query(ctx, `SELECT
		column_name,native_type,canonical_type,source_comment,
		business_name,business_description,semantic_type,tags,
		is_primary_key,is_foreign_key,is_unique,nullable
		FROM platform.metadata_columns
		WHERE table_id=$1::uuid
		  AND asset_status='ACTIVE'
		  AND column_name=ANY($2::text[])
		ORDER BY ordinal_position,id
		FOR SHARE`, tableID, projection)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := []SourceColumnContext{}
	for rows.Next() {
		var column SourceColumnContext
		if err := rows.Scan(
			&column.Name, &column.NativeType, &column.CanonicalType,
			&column.SourceComment, &column.BusinessName,
			&column.BusinessDescription, &column.SemanticType, &column.Tags,
			&column.PrimaryKey, &column.ForeignKey, &column.Unique, &column.Nullable,
		); err != nil {
			return nil, err
		}
		columns = append(columns, column)
	}
	return columns, rows.Err()
}

func validateFileDependencies(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
) (int, error) {
	rows, err := tx.Query(ctx, `SELECT
		dependency.source_version,dependency.source_hash,
		file_version.version,file_version.sha256
		FROM platform.dataset_dependencies AS dependency
		JOIN platform.file_asset_versions AS file_version
		  ON file_version.id::text=dependency.source_id
		 AND file_version.tenant_id=dependency.tenant_id
		WHERE dependency.dataset_version_id=$1::uuid
		  AND dependency.source_type='FILE_VERSION'
		ORDER BY dependency.source_id
		FOR SHARE OF dependency,file_version`, versionID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var frozenVersion, currentVersion int64
		var frozenHash, currentHash string
		if err := rows.Scan(
			&frozenVersion, &frozenHash, &currentVersion, &currentHash,
		); err != nil {
			return 0, err
		}
		if frozenVersion != currentVersion || frozenHash != currentHash {
			return 0, ErrSubjectChanged
		}
		count++
	}
	return count, rows.Err()
}

func loadUpstreams(
	ctx context.Context,
	tx pgx.Tx,
	versionID string,
	targetLayer string,
) ([]UpstreamContext, error) {
	rows, err := tx.Query(ctx, `SELECT
		dependency.source_id,dependency.source_version,dependency.source_hash,
		dependency.source_plan_hash,
		version.dataset_id::text,version.version_no,version.schema_hash,
		version.plan_hash,version.layer,version.dsl_json,
		dataset.code::text,dataset.name,dataset.description
		FROM platform.dataset_dependencies AS dependency
		JOIN platform.dataset_versions AS version
		  ON version.id::text=dependency.source_id
		 AND version.tenant_id=dependency.tenant_id
		 AND version.status='PUBLISHED'
		JOIN platform.datasets AS dataset
		  ON dataset.id=version.dataset_id
		 AND dataset.tenant_id=version.tenant_id
		 AND dataset.current_published_version_id=version.id
		 AND dataset.status='PUBLISHED'
		 AND dataset.deleted_at IS NULL
		WHERE dependency.dataset_version_id=$1::uuid
		  AND dependency.source_type='DATASET_VERSION'
		ORDER BY dependency.source_id
		FOR SHARE OF dependency,version,dataset`, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	upstreams := []UpstreamContext{}
	totalFields := 0
	for rows.Next() {
		var upstream UpstreamContext
		var frozenVersion int64
		var frozenHash, frozenPlanHash, currentPlanHash string
		var currentVersion int64
		var raw json.RawMessage
		if err := rows.Scan(
			&upstream.VersionID, &frozenVersion, &frozenHash, &frozenPlanHash,
			&upstream.DatasetID, &currentVersion, &upstream.SchemaHash,
			&currentPlanHash, &upstream.Layer, &raw,
			&upstream.Code, &upstream.Name, &upstream.Description,
		); err != nil {
			return nil, err
		}
		expectedLayer := "ODS"
		if targetLayer == "DWS" {
			expectedLayer = "DWD"
		}
		if frozenVersion != currentVersion || frozenHash != upstream.SchemaHash ||
			frozenPlanHash != currentPlanHash || upstream.Layer != expectedLayer {
			return nil, ErrSubjectChanged
		}
		prepared, err := dataset.Prepare(raw)
		if err != nil || prepared.DSLHash != upstream.SchemaHash {
			return nil, ErrSubjectChanged
		}
		upstream.OutputGrain = prepared.Document.OutputGrain.Description
		upstream.Fields, err = datasetFields(prepared.Document)
		if err != nil {
			return nil, err
		}
		totalFields += len(upstream.Fields)
		if totalFields > MaxUpstreamFields {
			return nil, ErrInputLimit
		}
		upstreams = append(upstreams, upstream)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for index := range upstreams {
		upstreams[index].ApprovedTags, err = loadApprovedDatasetTags(
			ctx, tx, upstreams[index].DatasetID, upstreams[index].VersionID,
		)
		if err != nil {
			return nil, err
		}
	}
	return upstreams, nil
}

func loadApprovedDatasetTags(
	ctx context.Context,
	tx pgx.Tx,
	datasetID string,
	versionID string,
) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT
		tag.category,tag.code::text,tag.name
		FROM platform.asset_tag_bindings AS binding
		JOIN platform.semantic_tags AS tag
		  ON tag.id=binding.tag_id
		 AND tag.tenant_id=binding.tenant_id
		 AND tag.status='ACTIVE'
		WHERE binding.asset_type='DATASET_VERSION'
		  AND binding.dataset_id=$1::uuid
		  AND binding.dataset_version_id=$2::uuid
		  AND binding.status='APPROVED'
		ORDER BY tag.category,tag.code::text,tag.id
		FOR SHARE OF binding,tag`, datasetID, versionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var category, code, name string
		if err := rows.Scan(&category, &code, &name); err != nil {
			return nil, err
		}
		tags = append(tags, category+":"+code+":"+name)
	}
	return tags, rows.Err()
}

func loadTaxonomy(ctx context.Context, tx pgx.Tx) ([]TaxonomyTag, error) {
	rows, err := tx.Query(ctx, `SELECT
		tag.id::text,tag.code::text,tag.name,tag.description,tag.category
		FROM platform.semantic_tags AS tag
		WHERE tag.status='ACTIVE'
		  AND tag.governance='CONTROLLED'
		  AND tag.category IN (
		    'BUSINESS_DOMAIN','BUSINESS_ENTITY','TABLE_FUNCTION',
		    'USAGE_SCOPE','DATA_GRAIN','JOIN_ROLE'
		  )
		ORDER BY tag.category,tag.code::text,tag.id
		FOR SHARE OF tag`)
	if err != nil {
		return nil, err
	}
	tags := []TaxonomyTag{}
	for rows.Next() {
		var tag TaxonomyTag
		if err := rows.Scan(
			&tag.ID, &tag.Code, &tag.Name, &tag.Description, &tag.Category,
		); err != nil {
			rows.Close()
			return nil, err
		}
		tags = append(tags, tag)
		if len(tags) > MaxTaxonomyTags {
			rows.Close()
			return nil, ErrInputLimit
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(tags) == 0 {
		return tags, nil
	}
	byID := make(map[string]int, len(tags))
	ids := make([]string, 0, len(tags))
	for index := range tags {
		byID[tags[index].ID] = index
		ids = append(ids, tags[index].ID)
	}
	aliasRows, err := tx.Query(ctx, `SELECT tag_id::text,alias::text
		FROM platform.semantic_tag_aliases
		WHERE tag_id=ANY($1::uuid[])
		ORDER BY tag_id,alias::text`, ids)
	if err != nil {
		return nil, err
	}
	defer aliasRows.Close()
	aliasCount := 0
	for aliasRows.Next() {
		var tagID, alias string
		if err := aliasRows.Scan(&tagID, &alias); err != nil {
			return nil, err
		}
		index, exists := byID[tagID]
		if !exists {
			return nil, ErrSubjectChanged
		}
		aliasCount++
		if aliasCount > MaxTaxonomyAliases {
			return nil, ErrInputLimit
		}
		tags[index].Aliases = append(tags[index].Aliases, alias)
	}
	return tags, aliasRows.Err()
}

func sameSourceVersionSnapshot(raw json.RawMessage, actual []sourceVersionRef) bool {
	var expected []sourceVersionRef
	if len(raw) == 0 || json.Unmarshal(raw, &expected) != nil {
		return false
	}
	sort.Slice(expected, func(i, j int) bool {
		return expected[i].DataSourceID < expected[j].DataSourceID
	})
	expectedRaw, err := json.Marshal(expected)
	if err != nil {
		return false
	}
	actualRaw, err := json.Marshal(actual)
	return err == nil && bytes.Equal(expectedRaw, actualRaw)
}

func exceedsInputLimits(input Input) bool {
	if len(input.Fields) > MaxDatasetFields ||
		len(input.SourceTables) > MaxSourceTables ||
		len(input.Upstreams) > MaxUpstreams ||
		len(input.Taxonomy) > MaxTaxonomyTags {
		return true
	}
	sourceColumns := 0
	upstreamFields := 0
	aliases := 0
	for _, table := range input.SourceTables {
		sourceColumns += len(table.Columns)
	}
	for _, upstream := range input.Upstreams {
		upstreamFields += len(upstream.Fields)
	}
	for _, tag := range input.Taxonomy {
		aliases += len(tag.Aliases)
	}
	return sourceColumns > MaxSourceColumns ||
		upstreamFields > MaxUpstreamFields ||
		aliases > MaxTaxonomyAliases
}

func (store *PostgresStore) Complete(
	ctx context.Context,
	claim Claim,
	workerID string,
	completion Completion,
) error {
	if store == nil || store.pool == nil || !validClaim(claim) ||
		!validWorkerID(workerID) || !validCompletion(completion) {
		return ErrInvalidRequest
	}
	subjectChanged := false
	err := database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		// Migration 071 gives every direct semantic-governance write statement
		// the same tenant gate before PostgreSQL takes target row locks. Acquire
		// it before even locking this job so completion can never form the
		// inverse binding-row -> governance-gate order with a direct statement.
		if _, err := tx.Exec(ctx, semanticGovernanceWriteGateSQL); err != nil {
			return err
		}
		if err := lockJob(ctx, tx, claim, workerID); err != nil {
			return err
		}
		currentInput, err := store.loadInputTx(ctx, tx, claim, workerID, true)
		if errors.Is(err, ErrSubjectChanged) || errors.Is(err, ErrInputLimit) {
			if finishErr := skipTx(ctx, tx, claim, workerID, "SUBJECT_CHANGED"); finishErr != nil {
				return finishErr
			}
			subjectChanged = true
			return nil
		}
		if err != nil {
			return err
		}
		inputRaw, err := json.Marshal(currentInput)
		if err != nil {
			return err
		}
		if inputDigest(inputRaw) != completion.InputHash {
			if err := skipTx(ctx, tx, claim, workerID, "SUBJECT_CHANGED"); err != nil {
				return err
			}
			subjectChanged = true
			return nil
		}
		var aiRequestValid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.ai_requests
			WHERE id=$1::uuid
			  AND purpose='DATASET_TAG_SUGGESTION'
			  AND resource_type='DATASET_VERSION'
			  AND resource_id=$2
			  AND status='SUCCEEDED'
		)`, completion.AIRequestID, claim.DatasetVersionID).Scan(&aiRequestValid); err != nil {
			return err
		}
		if !aiRequestValid {
			return ErrInvalidRequest
		}
		taxonomy := make(map[string]TaxonomyTag, len(currentInput.Taxonomy))
		for _, tag := range currentInput.Taxonomy {
			taxonomy[tag.ID] = tag
		}
		bindingCount := 0
		for _, suggestion := range completion.Suggestions {
			tag, exists := taxonomy[suggestion.TagID]
			if !exists || suggestion.TagCode != tag.Code ||
				suggestion.TagName != tag.Name || suggestion.Category != tag.Category {
				if err := skipTx(ctx, tx, claim, workerID, "SUBJECT_CHANGED"); err != nil {
					return err
				}
				subjectChanged = true
				return nil
			}
		}
		for index, suggestion := range completion.Suggestions {
			bindingID, resolution, err := ensureSuggestedBinding(
				ctx, tx, claim, suggestion,
			)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_tag_suggestion_items(
				tenant_id,job_id,lease_token,ordinal_position,tag_id,tag_code,tag_name,
				category,confidence,rationale,resolution,binding_id
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11
			)`,
				claim.ID, claim.LeaseToken, index+1, suggestion.TagID, suggestion.TagCode,
				suggestion.TagName, suggestion.Category, suggestion.Confidence,
				suggestion.Rationale, resolution, bindingID,
			); err != nil {
				return err
			}
			bindingCount++
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_tag_suggestion_jobs
			SET status='SUCCEEDED',ai_request_id=$1::uuid,
				input_hash=$2,output_hash=$3,
				suggestion_count=$4,binding_count=$5,
				error_code='',error_message='',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE id=$6::uuid AND status='RUNNING' AND lease_owner=$7
			  AND lease_token=$8::uuid AND lease_expires_at>now()`,
			completion.AIRequestID, completion.InputHash, completion.OutputHash,
			len(completion.Suggestions), bindingCount, claim.ID, workerID,
			claim.LeaseToken,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES(
			platform.current_tenant_id(),NULLIF($1,'')::uuid,
			'DATASET_TAG_SUGGESTION_COMPLETE','DATASET_VERSION',$2,
			jsonb_build_object(
			  'jobId',$3::text,'schemaHash',$4::text,'suggestionCount',$5::int
			)
		)`, claim.ActorID, claim.DatasetVersionID, claim.ID,
			claim.SchemaHash, len(completion.Suggestions))
		return err
	})
	if err != nil {
		return err
	}
	if subjectChanged {
		return ErrSubjectChanged
	}
	return nil
}

const semanticGovernanceWriteGateSQL = `SELECT pg_advisory_xact_lock(
	hashtextextended(
		'semantic-governance-write:'||platform.current_tenant_id()::text,
		0
	)
)`

func ensureSuggestedBinding(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	suggestion Suggestion,
) (bindingID string, resolution string, err error) {
	var status string
	err = tx.QueryRow(ctx, `SELECT id::text,status
		FROM platform.asset_tag_bindings
		WHERE tag_id=$1::uuid
		  AND asset_type='DATASET_VERSION'
		  AND dataset_id=$2::uuid
		  AND dataset_version_id=$3::uuid
		FOR UPDATE`,
		suggestion.TagID, claim.DatasetID, claim.DatasetVersionID,
	).Scan(&bindingID, &status)
	if err == nil {
		return bindingID, existingResolution(status), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	evidence, err := json.Marshal(map[string]any{
		"jobId":                   claim.ID,
		"promptVersion":           PromptVersion,
		"schemaHash":              claim.SchemaHash,
		"category":                suggestion.Category,
		"rationale":               suggestion.Rationale,
		"containsBusinessSamples": false,
	})
	if err != nil {
		return "", "", err
	}
	err = tx.QueryRow(ctx, `INSERT INTO platform.asset_tag_bindings(
			tenant_id,tag_id,asset_type,dataset_id,dataset_version_id,
			origin,status,confidence,evidence_json,assigned_by
		) VALUES(
			platform.current_tenant_id(),$1,'DATASET_VERSION',$2,$3,
			'LLM','SUGGESTED',$4,$5,NULLIF($6,'')::uuid
		)
		ON CONFLICT DO NOTHING
		RETURNING id::text`,
		suggestion.TagID, claim.DatasetID, claim.DatasetVersionID,
		suggestion.Confidence, evidence, claim.ActorID,
	).Scan(&bindingID)
	if err == nil {
		return bindingID, "CREATED_SUGGESTION", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	err = tx.QueryRow(ctx, `SELECT id::text,status
		FROM platform.asset_tag_bindings
		WHERE tag_id=$1::uuid
		  AND asset_type='DATASET_VERSION'
		  AND dataset_id=$2::uuid
		  AND dataset_version_id=$3::uuid
		FOR UPDATE`,
		suggestion.TagID, claim.DatasetID, claim.DatasetVersionID,
	).Scan(&bindingID, &status)
	if err != nil {
		return "", "", err
	}
	return bindingID, existingResolution(status), nil
}

func existingResolution(status string) string {
	switch status {
	case "APPROVED":
		return "EXISTING_APPROVED"
	case "REJECTED":
		return "EXISTING_REJECTED"
	default:
		return "EXISTING_SUGGESTION"
	}
}

func validCompletion(completion Completion) bool {
	if len(completion.Suggestions) > MaxSuggestions ||
		!sha256Pattern.MatchString(completion.InputHash) ||
		!sha256Pattern.MatchString(completion.OutputHash) ||
		strings.TrimSpace(completion.AIRequestID) == "" {
		return false
	}
	seen := map[string]bool{}
	for _, suggestion := range completion.Suggestions {
		if suggestion.TagID == "" || seen[suggestion.TagID] ||
			suggestion.TagCode == "" || suggestion.TagName == "" ||
			!suggestedCategory(suggestion.Category) ||
			math.IsNaN(suggestion.Confidence) ||
			math.IsInf(suggestion.Confidence, 0) ||
			suggestion.Confidence < 0 || suggestion.Confidence > 1 ||
			utf8.RuneCountInString(suggestion.Rationale) > MaxRationaleRunes ||
			hasControl(suggestion.Rationale) {
			return false
		}
		seen[suggestion.TagID] = true
	}
	canonical, err := canonicalOutput(completion.Suggestions)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(canonical)
	return completion.OutputHash == hex.EncodeToString(sum[:])
}

func lockJob(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	workerID string,
) error {
	var owned bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.dataset_tag_suggestion_jobs
		WHERE id=$1::uuid AND status='RUNNING' AND lease_owner=$2
		  AND lease_token=$3::uuid AND lease_expires_at>now()
		FOR UPDATE
	)`, claim.ID, workerID, claim.LeaseToken).Scan(&owned); err != nil {
		return err
	}
	if !owned {
		return ErrLeaseLost
	}
	return nil
}

func (store *PostgresStore) Skip(
	ctx context.Context,
	claim Claim,
	workerID string,
	code string,
) error {
	if store == nil || store.pool == nil || !validClaim(claim) ||
		!validWorkerID(workerID) || !validErrorCode(code) {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		return skipTx(ctx, tx, claim, workerID, code)
	})
}

func skipTx(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	workerID string,
	code string,
) error {
	tag, err := tx.Exec(ctx, `UPDATE platform.dataset_tag_suggestion_jobs
		SET status='SKIPPED',error_code=$1,error_message='',
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,
			completed_at=now(),updated_at=now()
		WHERE id=$2::uuid AND status='RUNNING' AND lease_owner=$3
		  AND lease_token=$4::uuid AND lease_expires_at>now()`,
		code, claim.ID, workerID, claim.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (store *PostgresStore) Fail(
	ctx context.Context,
	claim Claim,
	workerID string,
	code string,
	retryable bool,
) error {
	if store == nil || store.pool == nil || !validClaim(claim) ||
		!validWorkerID(workerID) || !validErrorCode(code) {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var attempt, maxAttempts int
		err := tx.QueryRow(ctx, `SELECT attempt,max_attempts
			FROM platform.dataset_tag_suggestion_jobs
			WHERE id=$1::uuid AND status='RUNNING' AND lease_owner=$2
			  AND lease_token=$3::uuid AND lease_expires_at>now()
			FOR UPDATE`,
			claim.ID, workerID, claim.LeaseToken,
		).Scan(&attempt, &maxAttempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
		if retryable && attempt < maxAttempts {
			delaySeconds := int64(1 << min(attempt, 6))
			tag, err := tx.Exec(ctx, `UPDATE platform.dataset_tag_suggestion_jobs
				SET status='PENDING',error_code=$1,error_message='',
					next_attempt_at=now()+($2*interval '1 second'),
					lease_owner='',lease_token=NULL,lease_expires_at=NULL,
					completed_at=NULL,updated_at=now()
				WHERE id=$3::uuid AND status='RUNNING' AND lease_owner=$4
				  AND lease_token=$5::uuid`,
				code, delaySeconds, claim.ID, workerID, claim.LeaseToken)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrLeaseLost
			}
			return nil
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.dataset_tag_suggestion_jobs
			SET status='FAILED',error_code=$1,error_message='',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE id=$2::uuid AND status='RUNNING' AND lease_owner=$3
			  AND lease_token=$4::uuid`,
			code, claim.ID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func validErrorCode(value string) bool {
	return len(value) >= 2 && len(value) <= 128 &&
		strings.TrimSpace(value) == value &&
		stableErrorCode(value)
}

func stableErrorCode(value string) bool {
	for index, character := range value {
		if index == 0 {
			if character < 'A' || character > 'Z' {
				return false
			}
			continue
		}
		if (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

var _ Store = (*PostgresStore)(nil)
