package materialization

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"intelligent-report-generation-system/internal/platform/database"
)

type SnapshotQualityStatus string

const (
	SnapshotQualityOK   SnapshotQualityStatus = "OK"
	SnapshotQualityWarn SnapshotQualityStatus = "WARN"
	SnapshotQualityFail SnapshotQualityStatus = "FAIL"
)

// SnapshotStart contains only facts known before warehouse execution. The
// canonical schema hash has already been recomputed from normalized Dataset
// DSL by the resolver; snapshot_version is the immutable build-run identity.
type SnapshotStart struct {
	SchemaHash      string
	SnapshotVersion string
	Physical        PhysicalIdentifier
	RelationKind    string
}

type MaterializationSnapshot struct {
	ID                   string                `json:"id"`
	TenantID             string                `json:"tenantId"`
	MaterializationID    string                `json:"materializationId"`
	BuildRunID           string                `json:"buildRunId"`
	SchemaHash           string                `json:"schemaHash"`
	SnapshotVersion      string                `json:"snapshotVersion"`
	SnapshotHash         string                `json:"snapshotHash"`
	SnapshotStartedAt    time.Time             `json:"snapshotStartedAt"`
	SnapshotCompletedAt  *time.Time            `json:"snapshotCompletedAt,omitempty"`
	DataAvailableThrough *time.Time            `json:"dataAvailableThrough,omitempty"`
	RowCount             *int64                `json:"rowCount,omitempty"`
	SizeBytes            *int64                `json:"sizeBytes,omitempty"`
	QualityStatus        SnapshotQualityStatus `json:"qualityStatus"`
}

// MaterializationMeta is the control-plane freshness contract consumed by
// TIME-004 and QUERY-010. It intentionally contains no warehouse relation.
type MaterializationMeta struct {
	MaterializationID    string                `json:"materializationId"`
	SchemaHash           string                `json:"schemaHash"`
	SnapshotVersion      string                `json:"snapshotVersion"`
	SnapshotCompletedAt  time.Time             `json:"snapshotCompletedAt"`
	DataAvailableThrough *time.Time            `json:"dataAvailableThrough,omitempty"`
	RowCount             *int64                `json:"rowCount,omitempty"`
	QualityStatus        SnapshotQualityStatus `json:"qualityStatus"`
}

type SnapshotControlReader interface {
	GetLatestSnapshot(context.Context, string, string) (MaterializationMeta, error)
}

// SnapshotService makes the control-plane-only dependency explicit. Callers
// cannot accidentally pass or use a warehouse connection on freshness reads.
type SnapshotService struct{ reader SnapshotControlReader }

func NewSnapshotService(reader SnapshotControlReader) *SnapshotService {
	return &SnapshotService{reader: reader}
}

func (service *SnapshotService) GetLatestSnapshot(
	ctx context.Context,
	tenantID, materializationID string,
) (MaterializationMeta, error) {
	if service == nil || service.reader == nil {
		return MaterializationMeta{}, ErrInvalidRequest
	}
	return service.reader.GetLatestSnapshot(ctx, tenantID, materializationID)
}

func (store *PostgresStore) BeginSnapshot(
	ctx context.Context,
	claim Claim,
	start SnapshotStart,
) (snapshot MaterializationSnapshot, err error) {
	if store == nil || store.pool == nil || validateClaim(claim) != nil ||
		!hashPattern.MatchString(start.SchemaHash) ||
		strings.TrimSpace(start.SnapshotVersion) == "" ||
		len(start.SnapshotVersion) > 128 ||
		start.SnapshotVersion != strings.TrimSpace(start.SnapshotVersion) ||
		start.RelationKind != claim.Plan.Target.RelationKind {
		return MaterializationSnapshot{}, ErrInvalidRequest
	}
	if err := ValidatePhysicalIdentifier(
		start.Physical, claim.TenantID, claim.DatasetID, claim.ID, claim.Layer,
	); err != nil {
		return MaterializationSnapshot{}, err
	}
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(
			hashtextextended($1,0)
		)`, "dataset-publication:"+claim.TenantID+":"+claim.DatasetID); err != nil {
			return err
		}
		if err := assertLeaseTx(ctx, tx, claim); err != nil {
			return err
		}

		existing, lookupErr := loadSnapshotByBuildRunTx(ctx, tx, claim.ID)
		if lookupErr == nil {
			if existing.SchemaHash != start.SchemaHash ||
				existing.SnapshotVersion != start.SnapshotVersion ||
				existing.SnapshotCompletedAt != nil {
				return ErrConflict
			}
			snapshot = existing
			return nil
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return lookupErr
		}

		var materializationID string
		var activeSchemaHash, activeDatasetVersionID string
		activeErr := tx.QueryRow(ctx, `SELECT id::text,schema_hash,dataset_version_id::text
			FROM platform.dataset_materializations
			WHERE dataset_id=$1 AND status='ACTIVE'
			FOR SHARE`, claim.DatasetID).
			Scan(&materializationID, &activeSchemaHash, &activeDatasetVersionID)
		if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
			return activeErr
		}
		reuseStable := activeErr == nil && activeSchemaHash == start.SchemaHash &&
			activeDatasetVersionID == claim.DatasetVersionID
		if !reuseStable {
			var buildingExists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM platform.dataset_materializations
				WHERE dataset_id=$1 AND status='BUILDING'
			)`, claim.DatasetID).Scan(&buildingExists); err != nil {
				return err
			}
			if buildingExists {
				return ErrConflict
			}
			if err := tx.QueryRow(ctx, `INSERT INTO platform.dataset_materializations(
				tenant_id,dataset_id,dataset_version_id,build_run_id,layer,status,
				relation_kind,refresh_mode,physical_schema,physical_name,
				published_schema,published_name,schema_hash,snapshot_hash,
				watermark_json
			) VALUES(
				$1,$2,$3,$4,$5,'BUILDING',$6,$7,$8,$9,$10,$11,$12,$13,'{}'::jsonb
			) RETURNING id::text`,
				claim.TenantID, claim.DatasetID, claim.DatasetVersionID, claim.ID,
				claim.Layer, start.RelationKind, claim.Mode,
				start.Physical.Schema, start.Physical.Name,
				start.Physical.PublishedSchema, start.Physical.PublishedName,
				start.SchemaHash, claim.InputSnapshotHash,
			).Scan(&materializationID); err != nil {
				return err
			}
		}

		row := tx.QueryRow(ctx, `INSERT INTO platform.materialization_snapshots(
			tenant_id,materialization_id,build_run_id,schema_hash,snapshot_version,
			snapshot_hash,physical_schema,physical_name,snapshot_started_at,quality_status
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp(),'WARN')
		RETURNING id::text,tenant_id::text,materialization_id::text,
			build_run_id::text,schema_hash,snapshot_version,snapshot_hash,
			snapshot_started_at,snapshot_completed_at,data_available_through,
			row_count,size_bytes,quality_status`,
			claim.TenantID, materializationID, claim.ID, start.SchemaHash,
			start.SnapshotVersion, claim.InputSnapshotHash,
			start.Physical.Schema, start.Physical.Name)
		return scanSnapshot(row, &snapshot)
	})
	if err != nil {
		return MaterializationSnapshot{}, mapStoreError(err)
	}
	return snapshot, nil
}

func (store *PostgresStore) GetLatestSnapshot(
	ctx context.Context,
	tenantID, materializationID string,
) (meta MaterializationMeta, err error) {
	if store == nil || store.pool == nil ||
		!validUUID(tenantID) || !validUUID(materializationID) {
		return MaterializationMeta{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var completedAt pgtype.Timestamptz
		var availableThrough pgtype.Timestamptz
		var rowCount pgtype.Int8
		err := tx.QueryRow(ctx, `SELECT materialization_id::text,schema_hash,
			snapshot_version,snapshot_completed_at,data_available_through,
			row_count,quality_status
			FROM platform.materialization_snapshots
			WHERE materialization_id=$1 AND snapshot_completed_at IS NOT NULL
			ORDER BY snapshot_completed_at DESC,id DESC
			LIMIT 1`, materializationID).Scan(
			&meta.MaterializationID, &meta.SchemaHash, &meta.SnapshotVersion,
			&completedAt, &availableThrough, &rowCount, &meta.QualityStatus,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		meta.SnapshotCompletedAt = completedAt.Time.UTC()
		meta.DataAvailableThrough = timePointer(availableThrough)
		meta.RowCount = int64Pointer(rowCount)
		return nil
	})
	if err != nil {
		return MaterializationMeta{}, mapStoreError(err)
	}
	return meta, nil
}

func loadSnapshotByBuildRunTx(
	ctx context.Context,
	tx pgx.Tx,
	buildRunID string,
) (MaterializationSnapshot, error) {
	var snapshot MaterializationSnapshot
	row := tx.QueryRow(ctx, `SELECT id::text,tenant_id::text,
		materialization_id::text,build_run_id::text,schema_hash,
		snapshot_version,snapshot_hash,snapshot_started_at,
		snapshot_completed_at,data_available_through,row_count,size_bytes,quality_status
		FROM platform.materialization_snapshots
		WHERE build_run_id=$1`, buildRunID)
	if err := scanSnapshot(row, &snapshot); errors.Is(err, pgx.ErrNoRows) {
		return MaterializationSnapshot{}, ErrNotFound
	} else if err != nil {
		return MaterializationSnapshot{}, err
	}
	return snapshot, nil
}

func failSnapshotTx(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
) (string, error) {
	var materializationID string
	err := tx.QueryRow(ctx, `SELECT materialization_id::text
		FROM platform.materialization_snapshots
		WHERE build_run_id=$1 AND snapshot_completed_at IS NULL
		FOR UPDATE`, claim.ID).Scan(&materializationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.materialization_snapshots SET
		snapshot_completed_at=clock_timestamp(),quality_status='FAIL'
		WHERE build_run_id=$1 AND snapshot_completed_at IS NULL`, claim.ID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform.dataset_materializations SET
		status='FAILED'
		WHERE id=$1 AND status='BUILDING'`, materializationID); err != nil {
		return "", err
	}
	return materializationID, nil
}

func completeSnapshotTx(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	activation Activation,
	quality SnapshotQualityStatus,
) error {
	tag, err := tx.Exec(ctx, `UPDATE platform.materialization_snapshots SET
		snapshot_hash=$1,snapshot_completed_at=clock_timestamp(),
		data_available_through=$2,row_count=$3,size_bytes=$4,quality_status=$5
		WHERE build_run_id=$6 AND materialization_id=(
			SELECT id FROM platform.dataset_materializations
			WHERE build_run_id=$6
		) AND snapshot_completed_at IS NULL`,
		activation.SnapshotHash, activation.DataAvailableThrough,
		activation.RowCount, activation.SizeBytes, quality, claim.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrInvalidTransition
	}
	return nil
}

func snapshotQualityStatus(results []QualityResult) SnapshotQualityStatus {
	status := SnapshotQualityOK
	for _, result := range results {
		if result.Status != QualityFailed {
			continue
		}
		if result.Severity == QualityError {
			return SnapshotQualityFail
		}
		status = SnapshotQualityWarn
	}
	return status
}

func scanSnapshot(row pgx.Row, snapshot *MaterializationSnapshot) error {
	var completedAt, availableThrough pgtype.Timestamptz
	var rowCount, sizeBytes pgtype.Int8
	if err := row.Scan(
		&snapshot.ID, &snapshot.TenantID, &snapshot.MaterializationID,
		&snapshot.BuildRunID, &snapshot.SchemaHash, &snapshot.SnapshotVersion,
		&snapshot.SnapshotHash, &snapshot.SnapshotStartedAt, &completedAt,
		&availableThrough, &rowCount, &sizeBytes, &snapshot.QualityStatus,
	); err != nil {
		return err
	}
	snapshot.SnapshotStartedAt = snapshot.SnapshotStartedAt.UTC()
	snapshot.SnapshotCompletedAt = timePointer(completedAt)
	snapshot.DataAvailableThrough = timePointer(availableThrough)
	snapshot.RowCount = int64Pointer(rowCount)
	snapshot.SizeBytes = int64Pointer(sizeBytes)
	return nil
}
