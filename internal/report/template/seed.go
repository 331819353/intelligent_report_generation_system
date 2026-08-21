package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
)

const BundledManifestCount = 55

// SeedBundledComponents hydrates the migration's platform component identities
// with the exact embedded manifests used by the compiler. Existing real
// manifests are hash-verified and never overwritten; only the explicit
// embedded-registry placeholder may be replaced once.
func SeedBundledComponents(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("component manifest seed database is required")
	}
	registry, err := NewDefaultRegistry()
	if err != nil {
		return err
	}
	manifests := registry.List()
	if len(manifests) != BundledManifestCount {
		return fmt.Errorf("bundled component manifest count is %d, want %d", len(manifests), BundledManifestCount)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT
		set_config('app.access_mode','SYSTEM',true),
		set_config('app.tenant_id','',true),
		set_config('app.user_id','',true),
		set_config('app.domain_id','',true)`); err != nil {
		return err
	}
	for _, manifest := range manifests {
		if err := seedBundledManifest(ctx, tx, manifest); err != nil {
			return fmt.Errorf("seed component manifest %s@%s: %w", manifest.Type, manifest.Version, err)
		}
	}
	return tx.Commit(ctx)
}

func seedBundledManifest(ctx context.Context, tx pgx.Tx, manifest Manifest) error {
	canonical, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	expectedHash := string(askdata.HashBytes(canonical))
	var templateID string
	if err := tx.QueryRow(ctx, `INSERT INTO platform.component_templates(tenant_id,type)
		VALUES(NULL,$1) ON CONFLICT(type) WHERE tenant_id IS NULL DO UPDATE SET type=EXCLUDED.type
		RETURNING id::text`, manifest.Type).Scan(&templateID); err != nil {
		return err
	}
	var storedRaw []byte
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT manifest_json,content_hash
		FROM platform.component_template_versions
		WHERE component_template_id=$1 AND version=$2 FOR UPDATE`, templateID, manifest.Version,
	).Scan(&storedRaw, &storedHash)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = tx.Exec(ctx, `INSERT INTO platform.component_template_versions(
			component_template_id,version,status,manifest_json,content_hash,migrator_id
		) VALUES($1,$2,'ACTIVE',$3,$4,$5)`, templateID, manifest.Version, canonical, expectedHash,
			nullableMigratorID(manifest))
		return err
	}
	if err != nil {
		return err
	}
	var placeholder struct {
		Seed string `json:"seed"`
	}
	if json.Unmarshal(storedRaw, &placeholder) == nil && placeholder.Seed == "embedded-registry" {
		command, err := tx.Exec(ctx, `UPDATE platform.component_template_versions SET
			manifest_json=$1,content_hash=$2,migrator_id=$3
			WHERE component_template_id=$4 AND version=$5 AND manifest_json->>'seed'='embedded-registry'`,
			canonical, expectedHash, nullableMigratorID(manifest), templateID, manifest.Version)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errors.New("component manifest placeholder changed concurrently")
		}
		return nil
	}
	storedManifest, err := DecodeManifest(storedRaw)
	if err != nil {
		return fmt.Errorf("stored manifest is invalid: %w", err)
	}
	storedCanonical, err := json.Marshal(storedManifest)
	if err != nil {
		return err
	}
	if storedManifest.Type != manifest.Type || storedManifest.Version != manifest.Version ||
		storedHash != expectedHash || askdata.HashBytes(storedCanonical) != askdata.ContentHash(expectedHash) {
		return errors.New("stored bundled manifest differs from the embedded registry")
	}
	return nil
}

func nullableMigratorID(manifest Manifest) any {
	if manifest.Migration == nil {
		return nil
	}
	return manifest.Migration.MigratorID
}
