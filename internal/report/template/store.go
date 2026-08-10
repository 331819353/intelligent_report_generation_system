package template

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type TemplateIdentity struct {
	TenantID askdata.ID
	ActorID  askdata.ID
}

type ResolvedTemplate struct {
	ReportTemplateVersionID    askdata.ID      `json:"reportTemplateVersionId"`
	Version                    string          `json:"version"`
	ReportDefinition           json.RawMessage `json:"reportDefinition"`
	ReportContentHash          string          `json:"reportContentHash"`
	StructureTemplateVersionID askdata.ID      `json:"structureTemplateVersionId"`
	Structure                  json.RawMessage `json:"structure"`
	StructureContentHash       string          `json:"structureContentHash"`
	LayoutTemplateVersionID    askdata.ID      `json:"layoutTemplateVersionId"`
	Layout                     json.RawMessage `json:"layout"`
	LayoutContentHash          string          `json:"layoutContentHash"`
	ThemeVersionID             askdata.ID      `json:"themeVersionId"`
	Theme                      json.RawMessage `json:"theme"`
	ThemeContentHash           string          `json:"themeContentHash"`
	NarrativeTemplateVersionID askdata.ID      `json:"narrativeTemplateVersionId"`
	Narrative                  json.RawMessage `json:"narrative"`
	NarrativeContentHash       string          `json:"narrativeContentHash"`
}

type PostgresTemplateStore struct{ pool *pgxpool.Pool }

var ErrTemplateNotFound = errors.New("report template version not found")

func NewPostgresTemplateStore(pool *pgxpool.Pool) *PostgresTemplateStore {
	return &PostgresTemplateStore{pool: pool}
}

func (store *PostgresTemplateStore) ResolveTemplate(ctx context.Context, identity TemplateIdentity, reportTemplateID askdata.ID, version string) (ResolvedTemplate, error) {
	ctx, err := store.requestContext(ctx, identity, reportTemplateID)
	if err != nil {
		return ResolvedTemplate{}, err
	}
	if _, err := parseSemver(version); err != nil {
		return ResolvedTemplate{}, fmt.Errorf("invalid report template version: %w", err)
	}
	return store.resolve(ctx, identity, `report_version.report_template_id=$1 AND report_version.version=$2`, reportTemplateID, version)
}

// ResolveTemplateVersion resolves the exact immutable composition referenced
// by a Report Definition or a published report version.
func (store *PostgresTemplateStore) ResolveTemplateVersion(
	ctx context.Context, identity TemplateIdentity, reportTemplateVersionID askdata.ID,
) (ResolvedTemplate, error) {
	ctx, err := store.requestContext(ctx, identity, reportTemplateVersionID)
	if err != nil {
		return ResolvedTemplate{}, err
	}
	return store.resolve(ctx, identity, `report_version.id=$1`, reportTemplateVersionID)
}

func (store *PostgresTemplateStore) requestContext(
	ctx context.Context, identity TemplateIdentity, id askdata.ID,
) (context.Context, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("template store is not configured")
	}
	if identity.TenantID.Validate() != nil || identity.ActorID.Validate() != nil || id.Validate() != nil {
		return nil, errors.New("template store identity is invalid")
	}
	for _, value := range []askdata.ID{identity.TenantID, identity.ActorID, id} {
		if _, err := uuid.Parse(string(value)); err != nil {
			return nil, errors.New("template store IDs must be UUIDs")
		}
	}
	return database.WithAccessContext(ctx, string(identity.ActorID), ""), nil
}

func (store *PostgresTemplateStore) resolve(
	ctx context.Context, identity TemplateIdentity, predicate string, arguments ...any,
) (ResolvedTemplate, error) {
	var result ResolvedTemplate
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT report_version.id::text,report_version.version,
			report_version.definition_json,report_version.content_hash,
			structure.id::text,structure.definition_json,structure.content_hash,
			layout.id::text,layout.definition_json,layout.content_hash,
			theme.id::text,theme.definition_json,theme.content_hash,
			narrative.id::text,narrative.definition_json,narrative.content_hash
			FROM platform.report_template_versions AS report_version
			JOIN platform.report_structure_template_versions AS structure
			  ON structure.id=report_version.structure_template_version_id AND structure.tenant_id=report_version.tenant_id
			JOIN platform.report_layout_template_versions AS layout
			  ON layout.id=report_version.layout_template_version_id AND layout.tenant_id=report_version.tenant_id
			JOIN platform.report_theme_versions AS theme
			  ON theme.id=report_version.theme_version_id AND theme.tenant_id=report_version.tenant_id
			JOIN platform.report_narrative_template_versions AS narrative
			  ON narrative.id=report_version.narrative_template_version_id AND narrative.tenant_id=report_version.tenant_id
			WHERE `+predicate+`
			  AND report_version.status IN ('PUBLISHED','DEPRECATED','RETAINED')
			  AND structure.status IN ('PUBLISHED','DEPRECATED','RETAINED')
			  AND layout.status IN ('PUBLISHED','DEPRECATED','RETAINED')
			  AND theme.status IN ('PUBLISHED','DEPRECATED','RETAINED')
			  AND narrative.status IN ('PUBLISHED','DEPRECATED','RETAINED')`, arguments...).Scan(
			&result.ReportTemplateVersionID, &result.Version, &result.ReportDefinition, &result.ReportContentHash,
			&result.StructureTemplateVersionID, &result.Structure, &result.StructureContentHash,
			&result.LayoutTemplateVersionID, &result.Layout, &result.LayoutContentHash,
			&result.ThemeVersionID, &result.Theme, &result.ThemeContentHash,
			&result.NarrativeTemplateVersionID, &result.Narrative, &result.NarrativeContentHash,
		)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ResolvedTemplate{}, ErrTemplateNotFound
	}
	if err != nil {
		return ResolvedTemplate{}, err
	}
	for _, document := range []struct {
		name string
		raw  *json.RawMessage
		hash string
	}{
		{"report", &result.ReportDefinition, result.ReportContentHash},
		{"structure", &result.Structure, result.StructureContentHash},
		{"layout", &result.Layout, result.LayoutContentHash},
		{"theme", &result.Theme, result.ThemeContentHash},
		{"narrative", &result.Narrative, result.NarrativeContentHash},
	} {
		canonical, err := canonicalTemplateJSON(*document.raw)
		if err != nil {
			return ResolvedTemplate{}, fmt.Errorf("%s template JSON: %w", document.name, err)
		}
		if string(askdata.HashBytes(canonical)) != document.hash {
			return ResolvedTemplate{}, fmt.Errorf("%s template content hash mismatch", document.name)
		}
		*document.raw = canonical
	}
	return result, nil
}

func canonicalTemplateJSON(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("template document must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("template document must contain one JSON value")
	}
	return json.Marshal(value)
}
