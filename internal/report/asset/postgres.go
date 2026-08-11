package asset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/store"
)

type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (repository *PostgresRepository) context(ctx context.Context, identity store.Identity, ids ...askdata.ID) (context.Context, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || identity.DomainID.Validate() != nil {
		return nil, errors.New("report asset repository is unavailable")
	}
	for _, id := range ids {
		if id.Validate() != nil {
			return nil, errors.New("report asset ID is invalid")
		}
		if parsed, err := uuid.Parse(string(id)); err != nil || parsed.String() != string(id) {
			return nil, errors.New("report asset IDs must be canonical UUIDs")
		}
	}
	return database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID)), nil
}

func (repository *PostgresRepository) List(ctx context.Context, identity store.Identity, query ListQuery) (Page, error) {
	var err error
	ctx, err = repository.context(ctx, identity)
	if err != nil {
		return Page{}, err
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Limit < 1 || query.Limit > 200 || query.Scope != "" && query.Scope != "all" && query.Scope != "mine" && query.Scope != "shared" {
		return Page{}, &Error{StableCode: "REPORT_ASSET_QUERY_INVALID", Message: "报告资产筛选条件无效"}
	}
	cursor, err := decodeCursor(query.Cursor)
	if err != nil {
		return Page{}, &Error{StableCode: "REPORT_ASSET_CURSOR_INVALID", Message: "报告资产游标无效", Err: err}
	}
	args := []any{identity.DomainID, identity.ActorID}
	where := []string{"report.domain_id=$1"}
	add := func(value any) string { args = append(args, value); return fmt.Sprintf("$%d", len(args)) }
	if query.Scope == "mine" {
		where = append(where, "report.owner_user_id=$2")
	} else if query.Scope == "shared" {
		where = append(where, "report.owner_user_id<>$2")
	}
	if query.OwnerID != "" {
		if query.OwnerID.Validate() != nil {
			return Page{}, &Error{StableCode: "REPORT_ASSET_QUERY_INVALID", Message: "Owner 筛选无效"}
		}
		where = append(where, "report.owner_user_id="+add(query.OwnerID))
	}
	if query.ReportType != "" {
		if query.ReportType != "REPORT" && query.ReportType != "DASHBOARD" {
			return Page{}, &Error{StableCode: "REPORT_ASSET_QUERY_INVALID", Message: "报告类型筛选无效"}
		}
		where = append(where, "report.report_type="+add(query.ReportType))
	}
	if search := strings.TrimSpace(query.Search); search != "" {
		if len(search) > 200 {
			return Page{}, &Error{StableCode: "REPORT_ASSET_QUERY_INVALID", Message: "搜索词过长"}
		}
		escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(search))
		where = append(where, "(lower(report.name) LIKE "+add("%"+escaped+"%")+" ESCAPE '\\' OR lower(report.code) LIKE "+fmt.Sprintf("$%d", len(args))+" ESCAPE '\\')")
	}
	if !cursor.UpdatedAt.IsZero() {
		where = append(where, "(report.updated_at,report.id)<("+add(cursor.UpdatedAt)+","+add(cursor.ID)+")")
	}
	lifecycleFilter := ""
	if query.Lifecycle != "" {
		switch query.Lifecycle {
		case LifecycleDraftOnly, LifecyclePublished, LifecycleChanged, LifecycleOffline:
			lifecycleFilter = " WHERE projected.lifecycle=" + add(query.Lifecycle)
		default:
			return Page{}, &Error{StableCode: "REPORT_ASSET_QUERY_INVALID", Message: "生命周期筛选无效"}
		}
	}
	limitArg := add(query.Limit + 1)
	sql := `WITH projected AS (
		SELECT report.id::text,report.code,report.name,report.report_type,
			report.owner_user_id::text,owner.display_name,
			CASE WHEN report.status='ARCHIVED' THEN 'OFFLINE'
				 WHEN report.current_published_version_id IS NULL THEN 'DRAFT_ONLY'
				 WHEN draft.definition_hash=version.definition_hash THEN 'PUBLISHED'
				 ELSE 'CHANGED' END AS lifecycle,
			version.version_no,draft.revision_no,
			GREATEST(draft.revision_no-COALESCE(version.source_revision_no,0),0),
			COALESCE(permission_summary.visible_count,1),COALESCE(permission_summary.editable_count,1),
			report.updated_at,
			platform.report_v2_row_can_access(report.id,report.domain_id,report.owner_user_id,ARRAY['VIEW']) AS can_view,
			platform.report_v2_row_can_access(report.id,report.domain_id,report.owner_user_id,ARRAY['EDIT']) AS can_edit,
			platform.report_v2_row_can_access(report.id,report.domain_id,report.owner_user_id,ARRAY['PUBLISH']) AS can_publish,
			platform.report_v2_row_can_access(report.id,report.domain_id,report.owner_user_id,ARRAY['EXPORT']) AS can_export,
			platform.report_v2_row_can_access(report.id,report.domain_id,report.owner_user_id,ARRAY['SHARE']) AS can_share,
			platform.report_v2_row_can_access(report.id,report.domain_id,report.owner_user_id,ARRAY['AI_EDIT']) AS can_ai_edit,
			(report.owner_user_id=$2 OR platform.user_is_asset_administrator()
			 OR platform.user_is_domain_administrator(report.domain_id)) AS can_manage
		FROM platform.reports report
		JOIN platform.users owner ON owner.id=report.owner_user_id AND owner.tenant_id=report.tenant_id
		JOIN platform.report_drafts draft ON draft.report_id=report.id AND draft.tenant_id=report.tenant_id
		LEFT JOIN platform.report_versions version ON version.id=report.current_published_version_id
		LEFT JOIN LATERAL (
			SELECT count(DISTINCT subject_id) FILTER(WHERE action='VIEW')+1 AS visible_count,
			       count(DISTINCT subject_id) FILTER(WHERE action='EDIT')+1 AS editable_count
			FROM platform.object_permissions permission
			WHERE permission.tenant_id=report.tenant_id AND permission.object_type='REPORT'
			  AND permission.object_id=report.id
		) permission_summary ON true
		WHERE ` + strings.Join(where, " AND ") + `
	)
	SELECT * FROM projected` + lifecycleFilter + `
	ORDER BY updated_at DESC,id DESC LIMIT ` + limitArg
	page := Page{Items: []Asset{}}
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Asset
			var versionNo *int
			var canView, canEdit, canPublish, canExport, canShare, canAIEdit, canManage bool
			if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.ReportType,
				&item.OwnerUserID, &item.OwnerName, &item.Lifecycle, &versionNo,
				&item.DraftRevisionNo, &item.UnpublishedChanges, &item.VisibleCount,
				&item.EditableCount, &item.UpdatedAt, &canView, &canEdit, &canPublish,
				&canExport, &canShare, &canAIEdit, &canManage); err != nil {
				return err
			}
			item.CurrentVersionNo = versionNo
			item.Shared = item.OwnerUserID != identity.ActorID
			item.AllowedActions = allowedActions(item.Lifecycle, canView, canEdit, canPublish, canExport, canShare, canAIEdit, canManage)
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return Page{}, err
	}
	if len(page.Items) > query.Limit {
		last := page.Items[query.Limit-1]
		page.NextCursor = encodeCursor(last.UpdatedAt, last.ID)
		page.Items = page.Items[:query.Limit]
	}
	return page, nil
}

func (repository *PostgresRepository) ListPermissions(ctx context.Context, identity store.Identity, reportID askdata.ID) ([]PermissionGrant, error) {
	var err error
	ctx, err = repository.context(ctx, identity, reportID)
	if err != nil {
		return nil, err
	}
	result := []PermissionGrant{}
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		if err := requirePermissionManager(ctx, tx, identity, reportID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT permission.id::text,permission.subject_type::text,
			permission.subject_id::text,CASE permission.subject_type::text WHEN 'USER'
			THEN target_user.display_name ELSE target_role.name END,permission.action,
			COALESCE(permission.granted_by::text,''),permission.created_at
			FROM platform.object_permissions permission
			LEFT JOIN platform.users target_user ON permission.subject_type::text='USER'
			 AND target_user.id=permission.subject_id AND target_user.tenant_id=permission.tenant_id
			LEFT JOIN platform.roles target_role ON permission.subject_type::text='ROLE'
			 AND target_role.id=permission.subject_id AND target_role.tenant_id=permission.tenant_id
			WHERE permission.object_type='REPORT' AND permission.object_id=$1
			ORDER BY permission.created_at,permission.id`, reportID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PermissionGrant
			if err := rows.Scan(&item.ID, &item.SubjectType, &item.SubjectID, &item.SubjectName, &item.Action, &item.GrantedBy, &item.CreatedAt); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func (repository *PostgresRepository) PublicationImpact(ctx context.Context, identity store.Identity, reportID askdata.ID) (PublicationImpact, error) {
	var err error
	ctx, err = repository.context(ctx, identity, reportID)
	if err != nil {
		return PublicationImpact{}, err
	}
	result := PublicationImpact{}
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
			COALESCE(permission_summary.visible_count,1),
			COALESCE(permission_summary.editable_count,1),
			COALESCE(share_summary.active_count,0),
			COALESCE(subscription_summary.active_count,0),
			COALESCE(version_summary.current_version_no,0),
			COALESCE(version_summary.current_version_no,0)+1
		FROM platform.reports report
		LEFT JOIN LATERAL (
			SELECT count(DISTINCT subject_id) FILTER(WHERE action='VIEW')+1 AS visible_count,
			       count(DISTINCT subject_id) FILTER(WHERE action='EDIT')+1 AS editable_count
			FROM platform.object_permissions permission
			WHERE permission.tenant_id=report.tenant_id AND permission.object_type='REPORT'
			  AND permission.object_id=report.id
		) permission_summary ON true
		LEFT JOIN LATERAL (
			SELECT count(*) AS active_count FROM platform.report_shares share
			WHERE share.tenant_id=report.tenant_id AND share.report_id=report.id
			  AND share.revoked_at IS NULL AND share.expired_at IS NULL AND share.expires_at>now()
		) share_summary ON true
		LEFT JOIN LATERAL (
			SELECT count(*) AS active_count
			FROM platform.report_subscriptions subscription
			JOIN platform.report_schedules schedule ON schedule.tenant_id=subscription.tenant_id
			 AND schedule.id=subscription.schedule_id
			WHERE schedule.report_id=report.id AND schedule.state='ACTIVE' AND subscription.state='ACTIVE'
		) subscription_summary ON true
		LEFT JOIN LATERAL (
			SELECT COALESCE(max(version_no),0) AS current_version_no
			FROM platform.report_versions version WHERE version.report_id=report.id
		) version_summary ON true
		WHERE report.id=$1 AND report.domain_id=$2
		  AND platform.report_v2_can_access(report.id,ARRAY['PUBLISH']::text[])`, reportID, identity.DomainID).
			Scan(&result.VisibleCount, &result.EditableCount, &result.ActiveShareCount,
				&result.SubscriptionCount, &result.CurrentVersionNo, &result.TargetVersionNo)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PublicationImpact{}, ErrNotFound
	}
	return result, err
}

func (repository *PostgresRepository) RecordPublishReview(
	ctx context.Context, identity store.Identity, reportID, reviewRunID, versionID askdata.ID,
	versionNo int, comment string, acknowledgedIssueCodes []string,
) error {
	var err error
	ctx, err = repository.context(ctx, identity, reportID, reviewRunID, versionID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]any{
		"reviewRunId": reviewRunID, "versionId": versionID, "versionNo": versionNo,
		"humanComment": strings.TrimSpace(comment), "acknowledgedIssueCodes": acknowledgedIssueCodes,
	})
	if err != nil || len(payload) > 32<<10 {
		return errors.New("report publication review receipt is invalid")
	}
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		_, execErr := tx.Exec(ctx, `INSERT INTO platform.report_asset_events(
			tenant_id,report_id,event_type,actor_user_id,payload_json
		) SELECT $1,$2,'PUBLISH_REVIEWED',$3,$4
		WHERE EXISTS(SELECT 1 FROM platform.report_versions WHERE id=$5 AND report_id=$2)`,
			identity.TenantID, reportID, identity.ActorID, payload, versionID)
		return execErr
	})
}

func (repository *PostgresRepository) Grant(ctx context.Context, identity store.Identity, reportID askdata.ID, input GrantInput) (PermissionGrant, bool, error) {
	var err error
	ctx, err = repository.context(ctx, identity, reportID, input.SubjectID)
	if err != nil || validateGrant(input) != nil {
		return PermissionGrant{}, false, &Error{StableCode: "REPORT_PERMISSION_INVALID", Message: "报告授权参数无效", Err: errors.Join(err, validateGrant(input))}
	}
	var result PermissionGrant
	created := false
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		if err := requirePermissionManager(ctx, tx, identity, reportID); err != nil {
			return err
		}
		id := askdata.ID(uuid.NewString())
		row := tx.QueryRow(ctx, `INSERT INTO platform.object_permissions(
			id,tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by
		) VALUES($1,$2,$3,$4,'REPORT',$5,$6,$7)
		ON CONFLICT(tenant_id,subject_type,subject_id,object_type,object_id,action) DO NOTHING
		RETURNING id::text,subject_type::text,subject_id::text,action,COALESCE(granted_by::text,''),created_at`,
			id, identity.TenantID, input.SubjectType, input.SubjectID, reportID, input.Action, identity.ActorID)
		if err := row.Scan(&result.ID, &result.SubjectType, &result.SubjectID, &result.Action, &result.GrantedBy, &result.CreatedAt); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err := tx.QueryRow(ctx, `SELECT id::text,subject_type::text,subject_id::text,action,
				COALESCE(granted_by::text,''),created_at FROM platform.object_permissions
				WHERE tenant_id=$1 AND subject_type=$2 AND subject_id=$3 AND object_type='REPORT'
				 AND object_id=$4 AND action=$5`, identity.TenantID, input.SubjectType, input.SubjectID, reportID, input.Action).
				Scan(&result.ID, &result.SubjectType, &result.SubjectID, &result.Action, &result.GrantedBy, &result.CreatedAt); err != nil {
				return err
			}
		} else {
			created = true
			if _, err := tx.Exec(ctx, `INSERT INTO platform.report_asset_events(
				tenant_id,report_id,event_type,actor_user_id,subject_type,subject_id,action,payload_json
			) VALUES($1,$2,'PERMISSION_GRANTED',$3,$4,$5,$6,'{}'::jsonb)`, identity.TenantID, reportID, identity.ActorID, input.SubjectType, input.SubjectID, input.Action); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx, `SELECT CASE $1 WHEN 'USER' THEN (SELECT display_name FROM platform.users WHERE id=$2)
			ELSE (SELECT name FROM platform.roles WHERE id=$2) END`, input.SubjectType, input.SubjectID).Scan(&result.SubjectName)
	})
	return result, created, err
}

func (repository *PostgresRepository) Revoke(ctx context.Context, identity store.Identity, reportID, grantID askdata.ID) (PermissionGrant, error) {
	var err error
	ctx, err = repository.context(ctx, identity, reportID, grantID)
	if err != nil {
		return PermissionGrant{}, err
	}
	var result PermissionGrant
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		if err := requirePermissionManager(ctx, tx, identity, reportID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `DELETE FROM platform.object_permissions WHERE id=$1 AND object_type='REPORT' AND object_id=$2
			RETURNING id::text,subject_type::text,subject_id::text,action,COALESCE(granted_by::text,''),created_at`, grantID, reportID).
			Scan(&result.ID, &result.SubjectType, &result.SubjectID, &result.Action, &result.GrantedBy, &result.CreatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.report_asset_events(
			tenant_id,report_id,event_type,actor_user_id,subject_type,subject_id,action,payload_json
		) VALUES($1,$2,'PERMISSION_REVOKED',$3,$4,$5,$6,jsonb_build_object('grantId',$7::text))`, identity.TenantID, reportID, identity.ActorID, result.SubjectType, result.SubjectID, result.Action, result.ID)
		return err
	})
	return result, err
}

func (repository *PostgresRepository) Transition(ctx context.Context, identity store.Identity, reportID askdata.ID, fromStatus, toStatus, reason, eventType string, expectedVersionID askdata.ID) error {
	var err error
	ctx, err = repository.context(ctx, identity, reportID)
	if err != nil || validateReason(reason) != nil {
		return &Error{StableCode: "REPORT_ASSET_REASON_INVALID", Message: "上下架原因无效", Err: errors.Join(err, validateReason(reason))}
	}
	return database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var status string
		var currentVersion askdata.ID
		var canPublish bool
		if err := tx.QueryRow(ctx, `SELECT report.status,COALESCE(report.current_published_version_id::text,''),
			platform.report_v2_row_can_access(report.id,report.domain_id,report.owner_user_id,ARRAY['PUBLISH'])
			FROM platform.reports report WHERE report.id=$1 AND report.domain_id=$2 FOR UPDATE`, reportID, identity.DomainID).
			Scan(&status, &currentVersion, &canPublish); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if !canPublish {
			return &Error{StableCode: "REPORT_ASSET_FORBIDDEN", Message: "无权执行报告上下架"}
		}
		if status != fromStatus {
			return &Error{StableCode: "REPORT_ASSET_STATE_CONFLICT", Message: "报告状态已发生变化"}
		}
		if expectedVersionID != "" && currentVersion != expectedVersionID {
			return &Error{StableCode: "REPORT_ASSET_STATE_CONFLICT", Message: "报告发布版本已发生变化"}
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.report_asset_reason',$1,true)`, reason); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.reports SET status=$1 WHERE id=$2`, toStatus, reportID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.report_asset_events(
			tenant_id,report_id,event_type,actor_user_id,reason,previous_status,new_status,payload_json
		) VALUES($1,$2,$3,$4,$5,$6,$7,'{}'::jsonb)`, identity.TenantID, reportID, eventType, identity.ActorID, reason, fromStatus, toStatus)
		return err
	})
}

func (repository *PostgresRepository) VersionForRestore(ctx context.Context, identity store.Identity, reportID askdata.ID) (store.Version, error) {
	var err error
	ctx, err = repository.context(ctx, identity, reportID)
	if err != nil {
		return store.Version{}, err
	}
	var result store.Version
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT version.id::text,version.report_id::text,version.version_no,version.source_revision_no,
			version.definition_json,version.definition_hash,version.schema_version,version.object_uri,
			version.published_by::text,version.published_at,version.rollback_of_version_no,
			COALESCE(version.rollback_reason,''),version.stale_insights_acknowledged,version.artifact_state,
			version.artifact_attempt,version.artifact_next_attempt_at
			FROM platform.reports report JOIN platform.report_versions version
			 ON version.id=report.current_published_version_id AND version.report_id=report.id
			WHERE report.id=$1 AND report.status='ARCHIVED'`, reportID).Scan(&result.ID, &result.ReportID, &result.VersionNo, &result.SourceRevisionNo, &raw, &result.DefinitionHash, &result.SchemaVersion, &result.ObjectURI, &result.PublishedBy, &result.PublishedAt, &result.RollbackOfVersionNo, &result.RollbackReason, &result.StaleInsightsAcknowledged, &result.ArtifactState, &result.ArtifactAttempt, &result.ArtifactNextAttemptAt)
		if err != nil {
			return err
		}
		result.DefinitionRaw = append([]byte(nil), raw...)
		return json.Unmarshal(raw, &result.Definition)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.Version{}, ErrNotFound
	}
	return result, err
}

func (repository *PostgresRepository) ListEvents(ctx context.Context, identity store.Identity, reportID askdata.ID, limit int) ([]Event, error) {
	var err error
	ctx, err = repository.context(ctx, identity, reportID)
	if err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, &Error{StableCode: "REPORT_ASSET_QUERY_INVALID", Message: "事件条数无效"}
	}
	result := []Event{}
	err = database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT event.id::text,event.event_type,COALESCE(event.actor_user_id::text,''),
			COALESCE(actor.display_name,''),COALESCE(event.subject_type::text,''),COALESCE(event.subject_id::text,''),
			COALESCE(event.action,''),COALESCE(event.reason,''),COALESCE(event.previous_status,''),
			COALESCE(event.new_status,''),event.payload_json,event.created_at
			FROM platform.report_asset_events event LEFT JOIN platform.users actor
			 ON actor.id=event.actor_user_id AND actor.tenant_id=event.tenant_id
			WHERE event.report_id=$1 ORDER BY event.created_at DESC,event.id DESC LIMIT $2`, reportID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Event
			if err := rows.Scan(&item.ID, &item.EventType, &item.ActorUserID, &item.ActorName, &item.SubjectType, &item.SubjectID, &item.Action, &item.Reason, &item.PreviousStatus, &item.NewStatus, &item.Payload, &item.CreatedAt); err != nil {
				return err
			}
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}

func requirePermissionManager(ctx context.Context, tx pgx.Tx, identity store.Identity, reportID askdata.ID) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT report.owner_user_id=$2 OR platform.user_is_asset_administrator()
		OR platform.user_is_domain_administrator(report.domain_id)
		FROM platform.reports report WHERE report.id=$1 AND report.domain_id=$3`, reportID, identity.ActorID, identity.DomainID).Scan(&allowed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !allowed {
		return &Error{StableCode: "REPORT_PERMISSION_FORBIDDEN", Message: "仅报告 Owner 或管理员可管理权限"}
	}
	return nil
}
