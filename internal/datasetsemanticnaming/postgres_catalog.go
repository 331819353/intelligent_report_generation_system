package datasetsemanticnaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresCatalog struct {
	pool *pgxpool.Pool
}

func NewPostgresCatalog(pool *pgxpool.Pool) *PostgresCatalog {
	return &PostgresCatalog{pool: pool}
}

func (catalog *PostgresCatalog) Load(
	ctx context.Context,
	tenantID string,
	document dataset.Document,
) (result Context, err error) {
	if catalog == nil || catalog.pool == nil || tenantID == "" {
		return Context{}, dataset.ErrSemanticNamingUnavailable
	}
	err = database.WithTenantTx(ctx, catalog.pool, tenantID, func(tx pgx.Tx) error {
		versionIDs := []string{}
		seen := map[string]bool{}
		for _, node := range document.Nodes {
			if node.Type != "DATASET" || node.DatasetVersionID == "" {
				continue
			}
			if !seen[node.DatasetVersionID] {
				seen[node.DatasetVersionID] = true
				versionIDs = append(versionIDs, node.DatasetVersionID)
			}
		}
		if len(versionIDs) == 0 || len(versionIDs) > MaxUpstreams {
			return fmt.Errorf("DWD/DWS/ADS naming requires governed dataset upstreams")
		}
		upstreams, err := loadUpstreams(ctx, tx, versionIDs)
		if err != nil {
			return err
		}
		if len(upstreams) != len(versionIDs) {
			return fmt.Errorf("one or more exact upstream versions are unavailable")
		}
		result.Upstreams = upstreams
		result.Taxonomy, err = loadTaxonomy(ctx, tx)
		return err
	})
	return result, err
}

func loadUpstreams(
	ctx context.Context,
	tx pgx.Tx,
	versionIDs []string,
) ([]UpstreamContext, error) {
	type snapshot struct {
		upstream UpstreamContext
		raw      json.RawMessage
	}
	rows, err := tx.Query(ctx, `SELECT
			dataset.id::text,version.id::text,dataset.code::text,dataset.name,
			dataset.description,COALESCE(version.dsl_json->'dataset'->>'domain',''),
			COALESCE(version.dsl_json->'dataset'->>'subject',''),version.layer,
			version.dsl_json
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS dataset
		  ON dataset.id=version.dataset_id
		 AND dataset.tenant_id=version.tenant_id
		WHERE version.id=ANY($1::uuid[])
		  AND version.status='PUBLISHED'
		  AND dataset.deleted_at IS NULL
		ORDER BY version.id
		FOR SHARE OF version,dataset`, versionIDs)
	if err != nil {
		return nil, err
	}
	snapshots := []snapshot{}
	for rows.Next() {
		var upstream UpstreamContext
		var raw json.RawMessage
		if err := rows.Scan(
			&upstream.DatasetID, &upstream.VersionID, &upstream.Code,
			&upstream.Name, &upstream.Description, &upstream.Domain,
			&upstream.Subject, &upstream.Layer, &raw,
		); err != nil {
			rows.Close()
			return nil, err
		}
		snapshots = append(snapshots, snapshot{upstream: upstream, raw: raw})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	upstreams := make([]UpstreamContext, 0, len(snapshots))
	for _, item := range snapshots {
		upstream := item.upstream
		raw := item.raw
		prepared, err := dataset.Prepare(raw)
		if err != nil {
			return nil, err
		}
		upstream.OutputGrain = prepared.Document.OutputGrain.Description
		for _, field := range prepared.Document.Fields {
			expression, err := json.Marshal(field.Expression)
			if err != nil {
				return nil, err
			}
			upstream.Fields = append(upstream.Fields, FieldContext{
				ID: field.ID, Code: field.Code, Name: field.Name,
				Description: field.Description, Role: field.Role,
				CanonicalType: field.CanonicalType, SemanticType: field.SemanticType,
				Aggregation: field.Aggregation, Expression: expression,
			})
		}
		upstream.Tags, err = loadApprovedTags(
			ctx, tx, upstream.DatasetID, upstream.VersionID,
		)
		if err != nil {
			return nil, err
		}
		upstreams = append(upstreams, upstream)
	}
	return upstreams, nil
}

func loadApprovedTags(
	ctx context.Context,
	tx pgx.Tx,
	datasetID, versionID string,
) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT tag.category||':'||tag.code::text||':'||tag.name
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
	items := []string{}
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func loadTaxonomy(ctx context.Context, tx pgx.Tx) ([]TaxonomyTag, error) {
	rows, err := tx.Query(ctx, `SELECT
			tag.id::text,tag.code::text,tag.name,tag.description,tag.category
		FROM platform.semantic_tags AS tag
		WHERE tag.status='ACTIVE'
		  AND tag.governance='CONTROLLED'
		  AND tag.category IN (
		    'BUSINESS_ENTITY','TABLE_FUNCTION',
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
			return nil, fmt.Errorf("controlled taxonomy exceeds safety limit")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(tags) == 0 {
		return nil, errors.New("controlled taxonomy is empty")
	}
	ids := make([]string, 0, len(tags))
	byID := make(map[string]int, len(tags))
	for index := range tags {
		ids = append(ids, tags[index].ID)
		byID[tags[index].ID] = index
	}
	aliasRows, err := tx.Query(ctx, `SELECT tag_id::text,alias::text
		FROM platform.semantic_tag_aliases
		WHERE tag_id=ANY($1::uuid[])
		ORDER BY tag_id,alias::text`, ids)
	if err != nil {
		return nil, err
	}
	defer aliasRows.Close()
	for aliasRows.Next() {
		var tagID, alias string
		if err := aliasRows.Scan(&tagID, &alias); err != nil {
			return nil, err
		}
		index, exists := byID[tagID]
		if exists {
			tags[index].Aliases = append(tags[index].Aliases, alias)
		}
	}
	if err := aliasRows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(tags, func(i, j int) bool {
		if tags[i].Category == tags[j].Category {
			return tags[i].Code < tags[j].Code
		}
		return tags[i].Category < tags[j].Category
	})
	return tags, nil
}
