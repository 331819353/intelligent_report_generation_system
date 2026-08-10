package askdatahttp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/understanding"
)

var ErrReleaseDriftRequired = errors.New("conversation release drift requires confirmation")

type ReleaseDescriptorView struct {
	ReleaseID       askdata.ID          `json:"releaseId"`
	ContentHash     askdata.ContentHash `json:"contentHash"`
	SemanticVersion string              `json:"semanticVersion"`
	Status          string              `json:"status"`
}

type ReleaseChangeView struct {
	ObjectType  string     `json:"objectType"`
	ObjectID    askdata.ID `json:"objectId"`
	Name        string     `json:"name"`
	FromVersion string     `json:"fromVersion,omitempty"`
	ToVersion   string     `json:"toVersion,omitempty"`
	ChangeKind  string     `json:"changeKind"`
	Summary     string     `json:"summary"`
}

type ReleaseDriftView struct {
	ConversationID askdata.ID            `json:"conversationId"`
	PinnedAt       *time.Time            `json:"pinnedAt,omitempty"`
	Previous       ReleaseDescriptorView `json:"previous"`
	Active         ReleaseDescriptorView `json:"active"`
	Changes        []ReleaseChangeView   `json:"changes"`
}

type ReleaseDriftRequiredError struct{ Drift ReleaseDriftView }

func (failure *ReleaseDriftRequiredError) Error() string {
	if failure == nil {
		return ErrReleaseDriftRequired.Error()
	}
	return fmt.Sprintf("%s: conversation %s", ErrReleaseDriftRequired, failure.Drift.ConversationID)
}

func (*ReleaseDriftRequiredError) Unwrap() error { return ErrReleaseDriftRequired }

type ConfirmReleaseDriftInput struct {
	ConversationID    askdata.ID
	PreviousReleaseID askdata.ID
	ActiveReleaseID   askdata.ID
}

type ReleasePinResult struct {
	ConversationID askdata.ID            `json:"conversationId"`
	Release        ReleaseDescriptorView `json:"release"`
	Replayed       bool                  `json:"replayed"`
}

func (service *PostgresService) resolveConversationRelease(
	ctx context.Context,
	identity RequestIdentity,
	conversationID askdata.ID,
	active askdata.ReleaseRef,
) (understanding.DriftAction, *ReleaseDriftView, error) {
	if !canonicalUUID(conversationID) || active.Validate() != nil {
		return "", nil, ErrInvalidRequest
	}
	var pinned understanding.ConversationRelease
	var pinnedAt *time.Time
	var previous, current ReleaseDescriptorView
	found := false
	runner := service.scopeRunner
	if runner == nil {
		return "", nil, ErrQuestionServiceFailure
	}
	err := runner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT release.id::text,release.content_hash,
			release.semantic_version,release.status
		FROM askdata.releases AS release
		WHERE release.tenant_id=$1 AND release.domain_id=$2 AND release.id=$3
		  AND release.content_hash=$4 AND release.status='ACTIVE'`,
			identity.TenantID, identity.DomainID, active.ReleaseID, active.ContentHash,
		).Scan(&current.ReleaseID, &current.ContentHash, &current.SemanticVersion, &current.Status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoActiveRelease
			}
			return err
		}
		var releaseID, contentHash, semanticVersion, status string
		var acknowledged bool
		err := tx.QueryRow(ctx, `SELECT pinned.id::text,pinned.content_hash,
			pinned.semantic_version,pinned.status,conversation.pinned_at,
			conversation.pin_drift_acknowledged
		FROM askdata.conversations AS conversation
		JOIN askdata.releases AS pinned
		  ON pinned.id=conversation.pinned_release_id
		 AND pinned.tenant_id=conversation.tenant_id
		 AND pinned.domain_id=conversation.domain_id
		WHERE conversation.tenant_id=$1 AND conversation.domain_id=$2
		  AND conversation.actor_id=$3 AND conversation.id=$4`,
			identity.TenantID, identity.DomainID, identity.ActorID, conversationID,
		).Scan(&releaseID, &contentHash, &semanticVersion, &status, &pinnedAt, &acknowledged)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		previous = ReleaseDescriptorView{
			ReleaseID: askdata.ID(releaseID), ContentHash: askdata.ContentHash(contentHash),
			SemanticVersion: semanticVersion, Status: status,
		}
		pinned = understanding.ConversationRelease{
			Pinned:      askdata.ReleaseRef{ReleaseID: previous.ReleaseID, ContentHash: previous.ContentHash},
			PinnedState: understanding.ReleasePinStatus(status), Acknowledged: acknowledged,
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	if !found {
		return understanding.DriftPinAfterBinding, nil, nil
	}
	_, action, err := understanding.ResolvePinnedRelease(pinned, active)
	if err != nil {
		return "", nil, err
	}
	if action == understanding.DriftForceRebind {
		if _, err := service.confirmReleaseDrift(ctx, identity, ConfirmReleaseDriftInput{
			ConversationID: conversationID, PreviousReleaseID: previous.ReleaseID,
			ActiveReleaseID: current.ReleaseID,
		}); err != nil {
			return "", nil, err
		}
		return action, nil, nil
	}
	if action != understanding.DriftConfirmRequired {
		return action, nil, nil
	}
	changes, err := service.releaseChanges(ctx, identity, previous.ReleaseID, current.ReleaseID)
	if err != nil {
		return "", nil, err
	}
	return action, &ReleaseDriftView{
		ConversationID: conversationID, PinnedAt: pinnedAt,
		Previous: previous, Active: current, Changes: changes,
	}, nil
}

func (service *PostgresService) releaseChanges(
	ctx context.Context,
	identity RequestIdentity,
	previousReleaseID, activeReleaseID askdata.ID,
) ([]ReleaseChangeView, error) {
	changes := []ReleaseChangeView{}
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH previous AS (
			SELECT object_type,object_id,content_hash,contract_json
			FROM askdata.release_objects
			WHERE tenant_id=$1 AND domain_id=$2 AND release_id=$3
			  AND object_type IN ('METRIC','DIMENSION')
		), active AS (
			SELECT object_type,object_id,content_hash,contract_json
			FROM askdata.release_objects
			WHERE tenant_id=$1 AND domain_id=$2 AND release_id=$4
			  AND object_type IN ('METRIC','DIMENSION')
		), changed AS (
			SELECT COALESCE(active.object_type,previous.object_type) AS object_type,
			  COALESCE(active.object_id,previous.object_id) AS object_id,
			  previous.content_hash AS previous_hash,active.content_hash AS active_hash,
			  previous.contract_json AS previous_contract,active.contract_json AS active_contract
			FROM previous FULL JOIN active USING(object_type,object_id)
			WHERE previous.content_hash IS DISTINCT FROM active.content_hash
		)
		SELECT changed.object_type,changed.object_id::text,
		  COALESCE(metric.name,changed.active_contract->>'name',changed.previous_contract->>'name','未命名对象'),
		  COALESCE(changed.previous_contract->>'versionNo',''),
		  COALESCE(changed.active_contract->>'versionNo',''),
		  CASE WHEN changed.previous_hash IS NULL THEN 'ADDED'
		       WHEN changed.active_hash IS NULL THEN 'REMOVED' ELSE 'UPDATED' END
		FROM changed
		LEFT JOIN askdata.metrics AS metric
		  ON metric.tenant_id=$1 AND metric.domain_id=$2
		 AND metric.id=changed.object_id AND changed.object_type='METRIC'
		ORDER BY changed.object_type,3,changed.object_id
		LIMIT 20`, identity.TenantID, identity.DomainID, previousReleaseID, activeReleaseID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var change ReleaseChangeView
			var fromVersion, toVersion string
			if err := rows.Scan(&change.ObjectType, &change.ObjectID, &change.Name, &fromVersion, &toVersion, &change.ChangeKind); err != nil {
				return err
			}
			if fromVersion != "" {
				change.FromVersion = "v" + fromVersion
			}
			if toVersion != "" {
				change.ToVersion = "v" + toVersion
			}
			if change.ObjectType == "METRIC" {
				change.Summary = "计算逻辑、默认筛选或聚合规则已更新"
			} else {
				change.Summary = "维度属性或成员规则已更新"
			}
			changes = append(changes, change)
		}
		return rows.Err()
	})
	return changes, err
}

func (service *PostgresService) ConfirmReleaseDrift(
	ctx context.Context,
	identity RequestIdentity,
	input ConfirmReleaseDriftInput,
) (ReleasePinResult, error) {
	if service == nil || identity.validate() != nil || !canonicalUUID(input.ConversationID) ||
		!canonicalUUID(input.PreviousReleaseID) || !canonicalUUID(input.ActiveReleaseID) {
		return ReleasePinResult{}, ErrInvalidRequest
	}
	return service.confirmReleaseDrift(ctx, identity, input)
}

func (service *PostgresService) confirmReleaseDrift(
	ctx context.Context,
	identity RequestIdentity,
	input ConfirmReleaseDriftInput,
) (ReleasePinResult, error) {
	result := ReleasePinResult{ConversationID: input.ConversationID}
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		var active ReleaseDescriptorView
		if err := tx.QueryRow(ctx, `SELECT id::text,content_hash,semantic_version,status
			FROM askdata.releases
			WHERE tenant_id=$1 AND domain_id=$2 AND id=$3 AND status='ACTIVE'
			FOR SHARE`, identity.TenantID, identity.DomainID, input.ActiveReleaseID,
		).Scan(&active.ReleaseID, &active.ContentHash, &active.SemanticVersion, &active.Status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoActiveRelease
			}
			return err
		}
		var pinnedID, pinnedStatus string
		var acknowledged bool
		if err := tx.QueryRow(ctx, `SELECT conversation.pinned_release_id::text,
			pinned.status,conversation.pin_drift_acknowledged
		FROM askdata.conversations AS conversation
		JOIN askdata.releases AS pinned
		  ON pinned.id=conversation.pinned_release_id AND pinned.tenant_id=conversation.tenant_id
		WHERE conversation.tenant_id=$1 AND conversation.domain_id=$2
		  AND conversation.actor_id=$3 AND conversation.id=$4
		FOR UPDATE OF conversation`, identity.TenantID, identity.DomainID,
			identity.ActorID, input.ConversationID,
		).Scan(&pinnedID, &pinnedStatus, &acknowledged); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return orchestrator.ErrRunNotFound
			}
			return err
		}
		if askdata.ID(pinnedID) == active.ReleaseID && acknowledged {
			result.Release, result.Replayed = active, true
			return nil
		}
		if askdata.ID(pinnedID) != input.PreviousReleaseID ||
			(pinnedStatus != "SUPERSEDED" && pinnedStatus != "RETAINED" && pinnedStatus != "RETIRED") {
			return ErrReleaseDriftRequired
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.conversations SET
			pinned_release_id=$1,pinned_at=clock_timestamp(),
			pin_drift_acknowledged=true,updated_at=clock_timestamp()
			WHERE tenant_id=$2 AND domain_id=$3 AND actor_id=$4 AND id=$5
			  AND pinned_release_id=$6`, active.ReleaseID, identity.TenantID, identity.DomainID,
			identity.ActorID, input.ConversationID, input.PreviousReleaseID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrReleaseDriftRequired
		}
		result.Release = active
		return nil
	})
	return result, err
}
