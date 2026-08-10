package insight

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/store"
)

type EvidenceRecord struct {
	ID          askdata.ID          `json:"id"`
	ReportID    askdata.ID          `json:"reportId"`
	ComponentID askdata.ID          `json:"componentId"`
	Hash        askdata.ContentHash `json:"evidenceHash"`
	Bundle      EvidenceBundle      `json:"evidence"`
	CreatedAt   time.Time           `json:"createdAt"`
}

type ArtifactRecord struct {
	ID          askdata.ID      `json:"id"`
	ReportID    askdata.ID      `json:"reportId"`
	ComponentID askdata.ID      `json:"componentId"`
	EvidenceID  askdata.ID      `json:"evidenceId"`
	Artifact    InsightArtifact `json:"artifact"`
	CreatedAt   time.Time       `json:"createdAt"`
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (s *PostgresStore) SaveEvidence(ctx context.Context, identity store.Identity, reportID, componentID askdata.ID, bundle EvidenceBundle) (EvidenceRecord, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || reportID.Validate() != nil || componentID.Validate() != nil {
		return EvidenceRecord{}, errors.New("invalid report evidence")
	}
	bundle = bundle.Normalize()
	if err := bundle.Validate(); err != nil {
		return EvidenceRecord{}, err
	}
	hash, err := bundle.Hash()
	if err != nil {
		return EvidenceRecord{}, err
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return EvidenceRecord{}, err
	}
	result := EvidenceRecord{ID: askdata.ID(uuid.NewString()), ReportID: reportID, ComponentID: componentID, Hash: hash, Bundle: bundle}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	err = database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO platform.report_evidence_artifacts(
			id,tenant_id,report_id,component_id,evidence_json,evidence_hash
		) SELECT $1,$2,$3,$4,$5,$6 WHERE EXISTS(
			SELECT 1 FROM platform.report_draft_component_indexes WHERE report_id=$3 AND component_id=$4
		) ON CONFLICT(report_id,component_id,evidence_hash) DO UPDATE SET evidence_hash=EXCLUDED.evidence_hash
		RETURNING id::text,created_at`, result.ID, identity.TenantID, reportID, componentID, raw, hash).Scan(&result.ID, &result.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return EvidenceRecord{}, store.ErrNotFound
	}
	return result, err
}

func (s *PostgresStore) AppendArtifact(ctx context.Context, identity store.Identity, reportID, componentID, evidenceID askdata.ID, artifact InsightArtifact) (ArtifactRecord, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || reportID.Validate() != nil || componentID.Validate() != nil || evidenceID.Validate() != nil {
		return ArtifactRecord{}, errors.New("invalid report insight")
	}
	artifact = artifact.Normalize()
	if err := artifact.Validate(); err != nil {
		return ArtifactRecord{}, err
	}
	raw, err := json.Marshal(artifact)
	if err != nil {
		return ArtifactRecord{}, err
	}
	result := ArtifactRecord{
		ID: askdata.ID(uuid.NewString()), ReportID: reportID, ComponentID: componentID,
		EvidenceID: evidenceID, Artifact: artifact,
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	err = database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var evidenceRaw []byte
		var evidenceHash string
		if scanErr := tx.QueryRow(ctx, `SELECT evidence_json,evidence_hash FROM platform.report_evidence_artifacts
			WHERE id=$1 AND report_id=$2 AND component_id=$3`, evidenceID, reportID, componentID).Scan(&evidenceRaw, &evidenceHash); scanErr != nil {
			return scanErr
		}
		bundle, decodeErr := DecodeEvidenceBundle(evidenceRaw)
		if decodeErr != nil {
			return decodeErr
		}
		if string(artifact.EvidenceHash) != evidenceHash || artifact.ValidateAgainst(bundle) != nil {
			return errors.New("insight evidence provenance mismatch")
		}
		if artifact.Status == InsightCurrent {
			if _, updateErr := tx.Exec(ctx, `UPDATE platform.report_insight_artifacts
				SET status='STALE',artifact_json=jsonb_set(
					artifact_json,'{status}',to_jsonb('STALE'::text),false
				)
				WHERE report_id=$1 AND component_id=$2 AND status='CURRENT'`, reportID, componentID); updateErr != nil {
				return updateErr
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO platform.report_insight_artifacts(
			id,tenant_id,report_id,component_id,evidence_id,evidence_hash,artifact_json,status,
			human_edited,human_edited_by,human_edited_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::uuid,$11)
		RETURNING created_at`, result.ID, identity.TenantID, reportID, componentID, evidenceID,
			artifact.EvidenceHash, raw, artifact.Status, artifact.HumanEdited,
			optionalID(artifact.HumanEditedBy), optionalTime(artifact.HumanEditedAt)).Scan(&result.CreatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactRecord{}, store.ErrNotFound
	}
	return result, err
}

func (s *PostgresStore) EditCurrent(ctx context.Context, identity store.Identity, reportID, componentID askdata.ID, content InsightContent, editedAt time.Time) (ArtifactRecord, error) {
	current, evidenceID, _, err := s.loadCurrent(ctx, identity, reportID, componentID)
	if err != nil {
		return ArtifactRecord{}, err
	}
	edited, err := ApplyHumanEdit(current, identity.ActorID, editedAt, content)
	if err != nil {
		return ArtifactRecord{}, err
	}
	edited.ID = askdata.ID(uuid.NewString())
	return s.AppendArtifact(ctx, identity, reportID, componentID, evidenceID, edited)
}

func (s *PostgresStore) GetCurrent(ctx context.Context, identity store.Identity, reportID, componentID askdata.ID) (ArtifactRecord, error) {
	artifact, evidenceID, recordID, err := s.loadCurrent(ctx, identity, reportID, componentID)
	if err != nil {
		return ArtifactRecord{}, err
	}
	return ArtifactRecord{
		ID: recordID, ReportID: reportID, ComponentID: componentID,
		EvidenceID: evidenceID, Artifact: artifact,
	}, nil
}

func (s *PostgresStore) loadCurrent(ctx context.Context, identity store.Identity, reportID, componentID askdata.ID) (InsightArtifact, askdata.ID, askdata.ID, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || reportID.Validate() != nil || componentID.Validate() != nil {
		return InsightArtifact{}, "", "", errors.New("invalid report insight lookup")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var artifact InsightArtifact
	var evidenceID, recordID askdata.ID
	err := database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var raw []byte
		if scanErr := tx.QueryRow(ctx, `SELECT id::text,evidence_id::text,artifact_json
			FROM platform.report_insight_artifacts
			WHERE report_id=$1 AND component_id=$2 AND status='CURRENT'`, reportID, componentID,
		).Scan(&recordID, &evidenceID, &raw); scanErr != nil {
			return scanErr
		}
		var decodeErr error
		artifact, decodeErr = DecodeInsightArtifact(raw)
		return decodeErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return InsightArtifact{}, "", "", store.ErrNotFound
	}
	return artifact, evidenceID, recordID, err
}

func optionalID(value *askdata.ID) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func optionalTime(value *string) any {
	if value == nil {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return nil
	}
	return parsed
}
