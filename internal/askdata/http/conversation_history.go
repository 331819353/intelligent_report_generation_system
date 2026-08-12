package askdatahttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
)

type ConversationSummary struct {
	ConversationID       askdata.ID         `json:"conversationId"`
	LatestRunID          askdata.ID         `json:"latestRunId"`
	Label                string             `json:"label"`
	State                orchestrator.State `json:"state"`
	Pinned               bool               `json:"pinned"`
	Archived             bool               `json:"archived"`
	Release              askdata.ReleaseRef `json:"release"`
	ReleaseDrifted       bool               `json:"releaseDrifted"`
	ClarificationPending bool               `json:"clarificationPending"`
	NarrativeDegraded    bool               `json:"narrativeDegraded"`
	RunCount             int                `json:"runCount"`
	RecordVersion        int64              `json:"recordVersion"`
	UpdatedAt            time.Time          `json:"updatedAt"`
}

type ConversationPage struct {
	Items      []ConversationSummary `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}
type ConversationDetail struct {
	Conversation  ConversationSummary `json:"conversation"`
	Runs          []RunView           `json:"runs"`
	NextRunCursor string              `json:"nextRunCursor,omitempty"`
}
type ConversationMutationInput struct {
	ExpectedVersion int64 `json:"expectedVersion"`
}

type ConversationRenameInput struct {
	ExpectedVersion int64  `json:"expectedVersion"`
	Label           string `json:"label"`
}

type ConversationHistoryBackend interface {
	ListConversations(context.Context, RequestIdentity, string, bool, int, string) (ConversationPage, error)
	GetConversation(context.Context, RequestIdentity, askdata.ID, int, string) (ConversationDetail, error)
	SetConversationPinned(context.Context, RequestIdentity, askdata.ID, ConversationMutationInput, bool) (ConversationSummary, error)
	SetConversationArchived(context.Context, RequestIdentity, askdata.ID, ConversationMutationInput, bool) (ConversationSummary, error)
	RenameConversation(context.Context, RequestIdentity, askdata.ID, ConversationRenameInput) (ConversationSummary, error)
}

type conversationCursor struct {
	Pinned    bool       `json:"pinned"`
	UpdatedAt time.Time  `json:"updatedAt"`
	ID        askdata.ID `json:"id"`
}

type conversationRunCursor struct {
	CreatedAt time.Time  `json:"createdAt"`
	ID        askdata.ID `json:"id"`
}

func encodeHistoryCursor(value any) string {
	raw, _ := json.Marshal(value)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeHistoryCursor(raw string, target any) error {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > 1024 || askdata.DecodeStrictJSON(decoded, target) != nil {
		return ErrInvalidRequest
	}
	return nil
}

func (service *PostgresService) ListConversations(ctx context.Context, identity RequestIdentity, search string, archived bool, limit int, cursor string) (ConversationPage, error) {
	if service == nil || service.pool == nil || identity.validate() != nil || limit < 1 || limit > 100 || len(search) > 256 || strings.TrimSpace(search) != search {
		return ConversationPage{}, ErrInvalidRequest
	}
	var decodedCursor *conversationCursor
	if cursor != "" {
		var parsed conversationCursor
		if decodeHistoryCursor(cursor, &parsed) != nil || parsed.UpdatedAt.IsZero() || !canonicalUUID(parsed.ID) {
			return ConversationPage{}, ErrInvalidRequest
		}
		decodedCursor = &parsed
	}
	var cursorPinned, cursorTime, cursorID any
	if decodedCursor != nil {
		cursorPinned, cursorTime, cursorID = decodedCursor.Pinned, decodedCursor.UpdatedAt, decodedCursor.ID
	}
	page := ConversationPage{Items: []ConversationSummary{}}
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH active AS (
		  SELECT id,content_hash FROM askdata.releases WHERE tenant_id=$1 AND domain_id=$2 AND status='ACTIVE'
		  ORDER BY activated_at DESC NULLS LAST,id DESC LIMIT 1
		), ranked AS (
		  SELECT run.*,row_number() OVER(PARTITION BY run.conversation_id ORDER BY run.created_at DESC,run.id DESC) AS position,
		    count(*) OVER(PARTITION BY run.conversation_id) AS run_count
		  FROM askdata.question_runs run WHERE run.tenant_id=$1 AND run.domain_id=$2 AND run.actor_id=$3
		), latest_label AS (
		  SELECT DISTINCT ON (run.conversation_id) run.conversation_id,
		    COALESCE(NULLIF(artifact.payload_json#>>'{artifact,layers,structured,headline,label}',''),
		      NULLIF(artifact.payload_json#>>'{artifact,layers,narrative,summary}',''),
		      NULLIF(artifact.payload_json#>>'{answer,layers,structured,headline,label}',''),
		      NULLIF(artifact.payload_json#>>'{answer,layers,narrative,summary}',''),'分析会话') AS label,
		    COALESCE((artifact.payload_json#>>'{artifact,verification,degraded}')::boolean,
		      (artifact.payload_json#>>'{answer,verification,degraded}')::boolean,false) AS degraded
		  FROM askdata.question_artifacts artifact JOIN askdata.question_runs run
		    ON run.tenant_id=artifact.tenant_id AND run.id=artifact.question_run_id
		  WHERE artifact.tenant_id=$1 AND artifact.domain_id=$2 AND artifact.actor_id=$3
		    AND artifact.artifact_type='ANSWER'
		  ORDER BY run.conversation_id,artifact.created_at DESC,artifact.artifact_index DESC
		)
		SELECT conversation.id::text,ranked.id::text,COALESCE(conversation.custom_label,latest_label.label,'分析会话'),ranked.current_state,
		  conversation.is_pinned,conversation.archived_at IS NOT NULL,ranked.release_id::text,ranked.release_content_hash,
		  COALESCE(active.id<>ranked.release_id OR active.content_hash<>ranked.release_content_hash,false),
		  ranked.current_state='CLARIFICATION_REQUIRED',COALESCE(latest_label.degraded,false),ranked.run_count,
		  conversation.record_version,conversation.updated_at
		FROM askdata.conversations conversation JOIN ranked ON ranked.conversation_id=conversation.id AND ranked.position=1
		LEFT JOIN latest_label ON latest_label.conversation_id=conversation.id LEFT JOIN active ON true
		WHERE conversation.tenant_id=$1 AND conversation.domain_id=$2 AND conversation.actor_id=$3
		  AND (($4 AND conversation.archived_at IS NOT NULL) OR (NOT $4 AND conversation.archived_at IS NULL))
		  AND ($5='' OR COALESCE(conversation.custom_label,latest_label.label,'分析会话') ILIKE '%'||replace(replace(replace($5,'\\','\\\\'),'%','\\%'),'_','\\_')||'%' ESCAPE '\\')
		  AND ($6::boolean IS NULL
		    OR (conversation.is_pinned=$6 AND (conversation.updated_at,conversation.id)<($7,$8::uuid))
		    OR ($6 AND NOT conversation.is_pinned))
		ORDER BY conversation.is_pinned DESC,conversation.updated_at DESC,conversation.id DESC LIMIT $9`, identity.TenantID, identity.DomainID, identity.ActorID, archived, search, cursorPinned, cursorTime, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ConversationSummary
			if err := rows.Scan(&item.ConversationID, &item.LatestRunID, &item.Label, &item.State, &item.Pinned, &item.Archived, &item.Release.ReleaseID, &item.Release.ContentHash, &item.ReleaseDrifted, &item.ClarificationPending, &item.NarrativeDegraded, &item.RunCount, &item.RecordVersion, &item.UpdatedAt); err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return ConversationPage{}, err
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.NextCursor = encodeHistoryCursor(conversationCursor{Pinned: last.Pinned, UpdatedAt: last.UpdatedAt, ID: last.ConversationID})
		page.Items = page.Items[:limit]
	}
	return page, nil
}

func (service *PostgresService) GetConversation(ctx context.Context, identity RequestIdentity, id askdata.ID, limit int, cursor string) (ConversationDetail, error) {
	if service == nil || service.pool == nil || identity.validate() != nil || !canonicalUUID(id) || limit < 1 || limit > 100 {
		return ConversationDetail{}, ErrInvalidRequest
	}
	var decodedCursor *conversationRunCursor
	if cursor != "" {
		var parsed conversationRunCursor
		if decodeHistoryCursor(cursor, &parsed) != nil || parsed.CreatedAt.IsZero() || !canonicalUUID(parsed.ID) {
			return ConversationDetail{}, ErrInvalidRequest
		}
		decodedCursor = &parsed
	}
	var cursorTime, cursorID any
	if decodedCursor != nil {
		cursorTime, cursorID = decodedCursor.CreatedAt, decodedCursor.ID
	}
	page, err := service.conversationByID(ctx, identity, id)
	if err != nil {
		return ConversationDetail{}, err
	}
	type runPosition struct {
		ID        askdata.ID
		CreatedAt time.Time
	}
	runs := []runPosition{}
	err = service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text,created_at FROM askdata.question_runs
			WHERE tenant_id=$1 AND domain_id=$2 AND actor_id=$3 AND conversation_id=$4
			  AND ($5::timestamptz IS NULL OR (created_at,id)<($5,$6::uuid))
			ORDER BY created_at DESC,id DESC LIMIT $7`, identity.TenantID, identity.DomainID, identity.ActorID, id, cursorTime, cursorID, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var run runPosition
			if e = rows.Scan(&run.ID, &run.CreatedAt); e != nil {
				return e
			}
			runs = append(runs, run)
		}
		return rows.Err()
	})
	if err != nil {
		return ConversationDetail{}, err
	}
	detail := ConversationDetail{Conversation: page, Runs: []RunView{}}
	if len(runs) > limit {
		last := runs[limit-1]
		detail.NextRunCursor = encodeHistoryCursor(conversationRunCursor{CreatedAt: last.CreatedAt, ID: last.ID})
		runs = runs[:limit]
	}
	for _, run := range runs {
		snapshot, e := service.GetQuestion(ctx, identity, run.ID)
		if e != nil {
			return ConversationDetail{}, e
		}
		detail.Runs = append(detail.Runs, newRunView(snapshot))
	}
	return detail, nil
}

func (service *PostgresService) SetConversationPinned(ctx context.Context, identity RequestIdentity, id askdata.ID, input ConversationMutationInput, pinned bool) (ConversationSummary, error) {
	return service.mutateConversation(ctx, identity, id, input, "PIN", pinned)
}
func (service *PostgresService) SetConversationArchived(ctx context.Context, identity RequestIdentity, id askdata.ID, input ConversationMutationInput, archived bool) (ConversationSummary, error) {
	return service.mutateConversation(ctx, identity, id, input, "ARCHIVE", archived)
}

func (service *PostgresService) RenameConversation(ctx context.Context, identity RequestIdentity, id askdata.ID, input ConversationRenameInput) (ConversationSummary, error) {
	label, err := normalizeConversationLabel(input.Label)
	if service == nil || service.pool == nil || identity.validate() != nil || !canonicalUUID(id) || input.ExpectedVersion < 1 || err != nil {
		return ConversationSummary{}, ErrInvalidRequest
	}
	err = service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		tag, updateErr := tx.Exec(ctx, `UPDATE askdata.conversations SET
			custom_label=$1,record_version=record_version+1,updated_at=clock_timestamp()
			WHERE tenant_id=$2 AND domain_id=$3 AND actor_id=$4 AND id=$5
			  AND record_version=$6 AND archived_at IS NULL`, label, identity.TenantID,
			identity.DomainID, identity.ActorID, id, input.ExpectedVersion)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return orchestrator.ErrVersionConflict
		}
		return nil
	})
	if err != nil {
		return ConversationSummary{}, err
	}
	return service.conversationByID(ctx, identity, id)
}

func normalizeConversationLabel(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if raw != label || label == "" || len([]rune(label)) > 120 {
		return "", ErrInvalidRequest
	}
	for _, character := range label {
		if character < 32 || character == 127 {
			return "", ErrInvalidRequest
		}
	}
	return label, nil
}
func (service *PostgresService) mutateConversation(ctx context.Context, identity RequestIdentity, id askdata.ID, input ConversationMutationInput, operation string, value bool) (ConversationSummary, error) {
	if service == nil || identity.validate() != nil || !canonicalUUID(id) || input.ExpectedVersion < 1 {
		return ConversationSummary{}, ErrInvalidRequest
	}
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		var tag pgconn.CommandTag
		var e error
		if operation == "PIN" {
			tag, e = tx.Exec(ctx, `UPDATE askdata.conversations SET is_pinned=$1,record_version=record_version+1,updated_at=clock_timestamp() WHERE tenant_id=$2 AND domain_id=$3 AND actor_id=$4 AND id=$5 AND record_version=$6 AND archived_at IS NULL`, value, identity.TenantID, identity.DomainID, identity.ActorID, id, input.ExpectedVersion)
		} else {
			tag, e = tx.Exec(ctx, `UPDATE askdata.conversations SET archived_at=CASE WHEN $1 THEN clock_timestamp() ELSE NULL END,is_pinned=CASE WHEN $1 THEN false ELSE is_pinned END,record_version=record_version+1,updated_at=clock_timestamp() WHERE tenant_id=$2 AND domain_id=$3 AND actor_id=$4 AND id=$5 AND record_version=$6`, value, identity.TenantID, identity.DomainID, identity.ActorID, id, input.ExpectedVersion)
		}
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return orchestrator.ErrVersionConflict
		}
		return nil
	})
	if err != nil {
		return ConversationSummary{}, err
	}
	return service.conversationByID(ctx, identity, id)
}
func (service *PostgresService) conversationByID(ctx context.Context, identity RequestIdentity, id askdata.ID) (ConversationSummary, error) {
	var result ConversationSummary
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH active AS(
			SELECT id,content_hash FROM askdata.releases
			WHERE tenant_id=$1 AND domain_id=$2 AND status='ACTIVE'
			ORDER BY activated_at DESC NULLS LAST,id DESC LIMIT 1
		),latest AS(
			SELECT * FROM askdata.question_runs
			WHERE tenant_id=$1 AND domain_id=$2 AND actor_id=$3 AND conversation_id=$4
			ORDER BY created_at DESC,id DESC LIMIT 1
		),label AS(
			SELECT COALESCE(NULLIF(artifact.payload_json#>>'{artifact,layers,structured,headline,label}',''),
			  NULLIF(artifact.payload_json#>>'{artifact,layers,narrative,summary}',''),
			  NULLIF(artifact.payload_json#>>'{answer,layers,structured,headline,label}',''),
			  NULLIF(artifact.payload_json#>>'{answer,layers,narrative,summary}',''),'分析会话') AS value,
			  COALESCE((artifact.payload_json#>>'{artifact,verification,degraded}')::boolean,
			    (artifact.payload_json#>>'{answer,verification,degraded}')::boolean,false) AS degraded
			FROM askdata.question_artifacts artifact JOIN askdata.question_runs run
			  ON run.tenant_id=artifact.tenant_id AND run.id=artifact.question_run_id
			WHERE artifact.tenant_id=$1 AND artifact.domain_id=$2 AND artifact.actor_id=$3
			  AND run.conversation_id=$4 AND artifact.artifact_type='ANSWER'
			ORDER BY artifact.created_at DESC,artifact.artifact_index DESC LIMIT 1
		)
		SELECT conversation.id::text,latest.id::text,COALESCE(conversation.custom_label,label.value,'分析会话'),latest.current_state,
		  conversation.is_pinned,conversation.archived_at IS NOT NULL,latest.release_id::text,latest.release_content_hash,
		  COALESCE(active.id<>latest.release_id OR active.content_hash<>latest.release_content_hash,false),
		  latest.current_state='CLARIFICATION_REQUIRED',COALESCE(label.degraded,false),
		  (SELECT count(*) FROM askdata.question_runs run_count WHERE run_count.tenant_id=$1
		    AND run_count.domain_id=$2 AND run_count.actor_id=$3 AND run_count.conversation_id=conversation.id),
		  conversation.record_version,conversation.updated_at
		FROM askdata.conversations conversation JOIN latest ON true LEFT JOIN label ON true LEFT JOIN active ON true
		WHERE conversation.tenant_id=$1 AND conversation.domain_id=$2 AND conversation.actor_id=$3 AND conversation.id=$4`,
			identity.TenantID, identity.DomainID, identity.ActorID, id).Scan(&result.ConversationID, &result.LatestRunID, &result.Label, &result.State, &result.Pinned, &result.Archived, &result.Release.ReleaseID, &result.Release.ContentHash, &result.ReleaseDrifted, &result.ClarificationPending, &result.NarrativeDegraded, &result.RunCount, &result.RecordVersion, &result.UpdatedAt)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ConversationSummary{}, orchestrator.ErrRunNotFound
	}
	if err != nil {
		return ConversationSummary{}, fmt.Errorf("load conversation history: %w", err)
	}
	return result, nil
}
