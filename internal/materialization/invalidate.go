package materialization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const SnapshotCompletionChannel = "materialization_snapshot_completed"

var ErrInvalidSnapshotCompletion = errors.New("materialization snapshot completion is invalid")

type SnapshotCompletion struct {
	TenantID          string                `json:"tenantId"`
	MaterializationID string                `json:"materializationId"`
	SnapshotVersion   string                `json:"snapshotVersion"`
	QualityStatus     SnapshotQualityStatus `json:"qualityStatus"`
}

func (completion SnapshotCompletion) Validate() error {
	if !canonicalUUID(completion.TenantID) || !canonicalUUID(completion.MaterializationID) ||
		!canonicalUUID(completion.SnapshotVersion) ||
		(completion.QualityStatus != SnapshotQualityOK &&
			completion.QualityStatus != SnapshotQualityWarn &&
			completion.QualityStatus != SnapshotQualityFail) {
		return ErrInvalidSnapshotCompletion
	}
	return nil
}

type SnapshotCacheInvalidator interface {
	InvalidateBySnapshot(tenantID, materializationID string) int
}

// SnapshotInvalidationProjector consumes the transactional NOTIFY emitted by
// migration 000230. Notification loss is harmless because snapshot versions
// remain part of every cache key; this projector only makes eviction immediate.
type SnapshotInvalidationProjector struct {
	pool        *pgxpool.Pool
	invalidator SnapshotCacheInvalidator
}

func NewSnapshotInvalidationProjector(
	pool *pgxpool.Pool,
	invalidator SnapshotCacheInvalidator,
) (*SnapshotInvalidationProjector, error) {
	if pool == nil || invalidator == nil {
		return nil, ErrInvalidSnapshotCompletion
	}
	return &SnapshotInvalidationProjector{pool: pool, invalidator: invalidator}, nil
}

// Project validates one completion payload before touching the reverse index.
func (projector *SnapshotInvalidationProjector) Project(payload string) (int, error) {
	if projector == nil || projector.invalidator == nil {
		return 0, ErrInvalidSnapshotCompletion
	}
	var completion SnapshotCompletion
	if err := json.Unmarshal([]byte(payload), &completion); err != nil || completion.Validate() != nil {
		return 0, ErrInvalidSnapshotCompletion
	}
	return projector.invalidator.InvalidateBySnapshot(
		completion.TenantID, completion.MaterializationID,
	), nil
}

// Run owns one PostgreSQL session because LISTEN state is connection-local.
// The caller should restart it after an infrastructure error.
func (projector *SnapshotInvalidationProjector) Run(ctx context.Context) error {
	if projector == nil || projector.pool == nil || projector.invalidator == nil {
		return ErrInvalidSnapshotCompletion
	}
	connection, err := projector.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, "LISTEN "+SnapshotCompletionChannel); err != nil {
		return err
	}
	defer func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		_, _ = connection.Exec(cleanupContext, "UNLISTEN "+SnapshotCompletionChannel)
	}()
	for {
		notification, err := connection.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if notification.Channel != SnapshotCompletionChannel {
			continue
		}
		if _, err := projector.Project(notification.Payload); err != nil {
			// NOTIFY is an unauthenticated hint inside PostgreSQL. Ignore a
			// malformed payload rather than allowing it to stop future valid
			// invalidations; snapshot-version keys still preserve correctness.
			continue
		}
	}
}

func canonicalUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == value
}
