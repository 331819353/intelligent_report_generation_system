// Package workitem builds an authorized cross-context inbox without copying
// source workflow truth. Only actor-specific read markers are persisted here.
package workitem

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
)

var (
	ErrInvalid  = errors.New("work item request is invalid")
	ErrNotFound = errors.New("work item was not found")
)

type Identity struct{ TenantID, DomainID, ActorID askdata.ID }
type Item struct {
	Kind                 string     `json:"kind"`
	Type                 string     `json:"type"`
	ObjectID             askdata.ID `json:"objectId"`
	Status               string     `json:"status"`
	RequesterUserID      askdata.ID `json:"requesterUserId,omitempty"`
	RequesterDisplayName string     `json:"requesterDisplayName,omitempty"`
	AssigneeDisplayName  string     `json:"assigneeDisplayName,omitempty"`
	Priority             string     `json:"priority"`
	SLADueAt             *time.Time `json:"slaDueAt,omitempty"`
	Overdue              bool       `json:"overdue"`
	DomainID             askdata.ID `json:"domainId"`
	Summary              string     `json:"summary"`
	SourceHref           string     `json:"sourceHref"`
	AllowedActions       []string   `json:"allowedActions"`
	Unread               bool       `json:"unread"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	Version              string     `json:"version"`
}

type Page struct {
	Items       []Item `json:"items"`
	NextCursor  string `json:"nextCursor,omitempty"`
	Total       int    `json:"total"`
	UnreadTotal int    `json:"unreadTotal"`
}

// ActionFieldConstraint describes the body contract for one source-owned
// command. It contains validation metadata only and never copies source facts.
type ActionFieldConstraint struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Required  bool     `json:"required"`
	Enum      []string `json:"enum,omitempty"`
	Min       *int64   `json:"min,omitempty"`
	MaxLength int      `json:"maxLength,omitempty"`
}

// ActionCommand points at the authoritative bounded-context endpoint. The
// inbox remains a read model and does not duplicate another module's state
// machine.
type ActionCommand struct {
	Action              string                  `json:"action"`
	Method              string                  `json:"method"`
	Href                string                  `json:"href"`
	IdempotencyRequired bool                    `json:"idempotencyRequired"`
	FixedValues         map[string]string       `json:"fixedValues,omitempty"`
	Fields              []ActionFieldConstraint `json:"fields,omitempty"`
}

type ActionContext struct {
	SourceObjectID    askdata.ID            `json:"sourceObjectId"`
	References        map[string]askdata.ID `json:"references,omitempty"`
	ExpectedVersion   string                `json:"expectedVersion"`
	ApprovalPurpose   string                `json:"approvalPurpose"`
	FixedVersionID    string                `json:"fixedVersionId,omitempty"`
	FixedHash         string                `json:"fixedHash,omitempty"`
	DifferenceSummary string                `json:"differenceSummary"`
	EvidenceSummary   string                `json:"evidenceSummary"`
	RiskSummary       string                `json:"riskSummary"`
	Commands          []ActionCommand       `json:"commands"`
}

type Detail struct {
	Item          Item          `json:"item"`
	ActionContext ActionContext `json:"actionContext"`
}

type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

const unifiedItemsSQL = `WITH permitted(code) AS (
  SELECT permission.code FROM platform.user_roles assignment
  JOIN platform.role_permissions link ON link.tenant_id=assignment.tenant_id AND link.role_id=assignment.role_id
  JOIN platform.permissions permission ON permission.tenant_id=link.tenant_id AND permission.id=link.permission_id
  JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
  WHERE assignment.tenant_id=$1 AND assignment.user_id=$3 AND role.status='ACTIVE' AND role.deleted_at IS NULL
), source AS (
  SELECT 'DOMAIN_ACCESS_APPROVAL'::text AS source_type,application.id,application.domain_id,
    application.status::text AS status,application.applicant_user_id AS requester_user_id,NULL::timestamptz AS sla_due_at,
    application.updated_at,extract(epoch from application.updated_at)::text AS source_version,
    '领域访问申请待审批'::text AS summary,'/governance/domain-applications/'||application.id::text AS href,
    ARRAY['APPROVE','REJECT']::text[] AS actions
  FROM platform.domain_access_applications application
  WHERE application.tenant_id=$1 AND application.domain_id=$2 AND application.status='PENDING'
    AND platform.user_is_domain_administrator(application.domain_id)
  UNION ALL
  SELECT 'DATA_SOURCE_PUBLICATION',request.id,source.domain_id,request.status,request.requester_user_id,NULL,
    request.updated_at,request.version::text,'数据源发布申请待审批','/governance/data-sources/'||request.data_source_id::text,
    ARRAY['APPROVE','REJECT']::text[]
  FROM platform.data_source_publication_requests request JOIN platform.data_sources source
    ON source.tenant_id=request.tenant_id AND source.id=request.data_source_id
  WHERE request.tenant_id=$1 AND source.domain_id=$2 AND request.status='PENDING'
    AND EXISTS(SELECT 1 FROM permitted WHERE code='data_source.publish')
  UNION ALL
  SELECT 'DATASET_PUBLICATION',request.id,dataset.domain_id,request.status,request.requester_user_id,NULL,
    request.updated_at,request.version::text,'数据集发布申请待审批','/governance/datasets/'||request.dataset_id::text,
    ARRAY['APPROVE','REJECT']::text[]
  FROM platform.dataset_publication_requests request JOIN platform.datasets dataset
    ON dataset.tenant_id=request.tenant_id AND dataset.id=request.dataset_id
  WHERE request.tenant_id=$1 AND dataset.domain_id=$2 AND request.status='PENDING'
    AND EXISTS(SELECT 1 FROM permitted WHERE code='dataset.publish')
  UNION ALL
  SELECT 'DATA_REQUEST',request.id,request.domain_id,request.state,request.requester_user_id,request.sla_due_at,
    request.updated_at,request.record_version::text,'取数申请待处理','/data-requests/'||request.id::text,
    CASE WHEN request.state='SUBMITTED' THEN ARRAY['APPROVE','REJECT']::text[]
      WHEN request.state='APPROVED' THEN ARRAY['START']::text[]
      WHEN request.state='IN_PROGRESS' THEN ARRAY['DELIVER']::text[] ELSE ARRAY[]::text[] END
  FROM platform.data_requests request
  WHERE request.tenant_id=$1 AND request.domain_id=$2 AND request.state IN ('SUBMITTED','APPROVED','IN_PROGRESS')
    AND (($3=ANY(request.approver_user_ids) AND request.state IN ('SUBMITTED','APPROVED'))
      OR (request.assignee_user_id=$3 AND request.state='IN_PROGRESS'))
  UNION ALL
  SELECT 'FEEDBACK_TICKET',ticket.id,ticket.domain_id,ticket.status,ticket.reporter_user_id,ticket.sla_due_at,
    ticket.updated_at,ticket.record_version::text,'反馈工单待处理','/operations/feedback/'||ticket.id::text,
    ARRAY['OPEN']::text[]
  FROM askdata.feedback_tickets ticket
  WHERE ticket.tenant_id=$1 AND ticket.domain_id=$2 AND ticket.owner_user_id=$3 AND ticket.status NOT IN ('REJECTED','CLOSED')
  UNION ALL
  SELECT notification.notification_type,notification.id,notification.domain_id,'UNRESOLVED',decision.created_by,NULL,
    notification.created_at,extract(epoch from notification.created_at)::text,notification.summary,
    '/decisions/'||notification.decision_id::text,
    CASE notification.notification_type WHEN 'DECISION_APPROVAL' THEN ARRAY['APPROVE','REJECT']::text[]
      WHEN 'ACTION_ASSIGNED' THEN CASE action.status
        WHEN 'TODO' THEN ARRAY['START']::text[]
        WHEN 'DOING' THEN ARRAY['BLOCK','COMPLETE']::text[]
        WHEN 'BLOCKED' THEN ARRAY['START']::text[]
        ELSE ARRAY[]::text[] END
      ELSE ARRAY['OPEN']::text[] END
  FROM decision.decision_notifications notification JOIN decision.decisions decision
    ON decision.tenant_id=notification.tenant_id AND decision.id=notification.decision_id
  LEFT JOIN decision.action_items action
    ON action.tenant_id=notification.tenant_id AND action.id=notification.action_id
  LEFT JOIN decision.decision_approvals approval
    ON approval.tenant_id=notification.tenant_id AND approval.decision_id=notification.decision_id
      AND approval.approver_user_id=notification.recipient_user_id
  LEFT JOIN decision.decision_approval_events approval_event
    ON approval_event.tenant_id=approval.tenant_id AND approval_event.approval_id=approval.id
  WHERE notification.tenant_id=$1 AND notification.domain_id=$2 AND notification.recipient_user_id=$3
    AND notification.resolved_at IS NULL
    AND (notification.notification_type<>'DECISION_APPROVAL'
      OR (decision.status='IN_REVIEW' AND approval.status='PENDING' AND approval_event.id IS NULL))
    AND (notification.notification_type<>'ACTION_ASSIGNED' OR action.status IN ('TODO','DOING','BLOCKED'))
    AND (notification.notification_type<>'ACTION_BLOCKED' OR action.status='BLOCKED')
    AND (notification.notification_type<>'ACTION_OVERDUE' OR action.status IN ('TODO','DOING','BLOCKED'))
    AND (notification.notification_type NOT IN ('DECISION_REVIEW_DUE','OUTCOME_REVIEW_DUE') OR decision.status='REVIEW_DUE')
  UNION ALL
  SELECT 'REPORT_EXPORT_FAILED',job.id,job.domain_id,job.state,job.requested_by,job.expires_at,
    job.updated_at,job.attempt::text,'报告导出任务失败','/reports/'||job.report_id::text,
    ARRAY['RETRY']::text[]
  FROM platform.report_export_jobs job WHERE job.tenant_id=$1 AND job.domain_id=$2 AND job.requested_by=$3 AND job.state='FAILED'
  UNION ALL
  SELECT 'REPORT_DELIVERY_READY',delivery.id,delivery.domain_id,delivery.state,delivery.recipient_user_id,NULL,
    delivery.updated_at,extract(epoch from delivery.updated_at)::text,'定时报表已生成',delivery.report_link,
    ARRAY['OPEN','MARK_READ']::text[]
  FROM platform.report_deliveries delivery WHERE delivery.tenant_id=$1 AND delivery.domain_id=$2
    AND delivery.recipient_user_id=$3 AND delivery.state='READY' AND delivery.read_at IS NULL
  UNION ALL
  SELECT 'REPORT_DELIVERY_FAILED',delivery.id,delivery.domain_id,delivery.state,schedule.created_by,delivery.next_attempt_at,
    delivery.updated_at,extract(epoch from delivery.updated_at)::text,'定时报表分发失败','/reports/'||delivery.report_id::text,
    CASE WHEN schedule.state='PAUSED' THEN ARRAY['OPEN','RESUME']::text[] ELSE ARRAY['OPEN']::text[] END
  FROM platform.report_deliveries delivery JOIN platform.report_schedules schedule
    ON schedule.tenant_id=delivery.tenant_id AND schedule.id=delivery.schedule_id
  WHERE delivery.tenant_id=$1 AND delivery.domain_id=$2 AND schedule.owner_user_id=$3
    AND delivery.state='FAILED' AND (delivery.attempt>=5 OR schedule.state='PAUSED')
  UNION ALL
  SELECT 'RUNTIME_CONFIG_APPROVAL',version.id,$2::uuid,version.state,version.created_by,NULL,
    version.updated_at,version.record_version::text,'运行配置变更待审批','/platform-management/runtime-config/versions/'||version.id::text,
    ARRAY['APPROVE','REJECT']::text[]
  FROM platform.runtime_config_versions version
  WHERE version.tenant_id=$1 AND version.state='IN_REVIEW' AND version.created_by<>$3
    AND platform.user_is_platform_administrator()
    AND (version.scope_type<>'DOMAIN' OR version.scope_id=$2::text)
), visible AS (
 SELECT source.*,receipt.source_version AS read_version FROM source
 LEFT JOIN platform.work_item_receipts receipt ON receipt.tenant_id=$1 AND receipt.domain_id=$2 AND receipt.actor_user_id=$3
  AND receipt.source_type=source.source_type AND receipt.source_id=source.id
)
SELECT visible.source_type,visible.id::text,visible.domain_id::text,visible.status,visible.requester_user_id::text,
  visible.sla_due_at,visible.updated_at,visible.source_version,visible.summary,visible.href,visible.actions,
  (visible.read_version IS NULL OR visible.read_version<>visible.source_version) AS unread,
  CASE WHEN visible.sla_due_at IS NOT NULL AND visible.sla_due_at<=$7 THEN 'HIGH'
    WHEN visible.source_type IN('DOMAIN_ACCESS_APPROVAL','DECISION_APPROVAL','RUNTIME_CONFIG_APPROVAL') THEN 'HIGH'
    WHEN visible.sla_due_at IS NOT NULL AND visible.sla_due_at<=$7+interval '7 days' THEN 'MEDIUM'
    WHEN visible.source_type IN('DATA_SOURCE_PUBLICATION','DATASET_PUBLICATION','REPORT_DELIVERY_FAILED') THEN 'MEDIUM'
    ELSE 'LOW' END AS priority,
  COALESCE(requester.display_name,'系统') AS requester_display_name,assignee.display_name AS assignee_display_name,
  count(*) OVER() AS total_count,
  count(*) FILTER(WHERE visible.read_version IS NULL OR visible.read_version<>visible.source_version) OVER() AS unread_count
FROM visible
LEFT JOIN platform.users requester ON requester.tenant_id=$1 AND requester.id=visible.requester_user_id
JOIN platform.users assignee ON assignee.tenant_id=$1 AND assignee.id=$3
WHERE ($4='' OR visible.source_type=$4) AND ($5::uuid IS NULL OR visible.id=$5)
  AND (NOT $6 OR visible.read_version IS NULL OR visible.read_version<>visible.source_version)
  AND ($10='' OR ($10='APPROVAL' AND visible.actions&&ARRAY['APPROVE','REJECT']::text[])
    OR ($10='TASK' AND NOT (visible.actions&&ARRAY['APPROVE','REJECT']::text[])))
ORDER BY (visible.sla_due_at IS NOT NULL AND visible.sla_due_at<=$7) DESC,
  COALESCE(visible.sla_due_at,'infinity'::timestamptz),visible.updated_at DESC,visible.id
LIMIT $8 OFFSET $9`

func (store *Store) List(ctx context.Context, identity Identity, onlyUnread bool, itemType string, limit int) ([]Item, error) {
	page, err := store.ListPage(ctx, identity, onlyUnread, itemType, limit, "")
	return page.Items, err
}

func (store *Store) ListPage(ctx context.Context, identity Identity, onlyUnread bool, itemType string, limit int, cursor string) (Page, error) {
	return store.ListPageKind(ctx, identity, onlyUnread, itemType, "", limit, cursor)
}

func (store *Store) ListPageKind(ctx context.Context, identity Identity, onlyUnread bool, itemType, kind string, limit int, cursor string) (Page, error) {
	offset := 0
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return Page{}, ErrInvalid
		}
		offset, err = strconv.Atoi(string(raw))
		if err != nil || offset < 0 || offset > 100000 {
			return Page{}, ErrInvalid
		}
	}
	if limit < 1 || limit > 200 {
		return Page{}, ErrInvalid
	}
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if kind != "" && kind != "APPROVAL" && kind != "TASK" {
		return Page{}, ErrInvalid
	}
	items, total, unread, err := store.query(ctx, identity, onlyUnread, itemType, kind, "", limit+1, offset)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: items, Total: total, UnreadTotal: unread}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + limit)))
	}
	return page, nil
}

func (store *Store) query(ctx context.Context, identity Identity, onlyUnread bool, itemType, kind string, id askdata.ID, limit, offset int) ([]Item, int, int, error) {
	if store == nil || store.pool == nil || identity.validate() != nil || limit < 1 || limit > 201 || offset < 0 || (id != "" && id.Validate() != nil) {
		return nil, 0, 0, ErrInvalid
	}
	items := []Item{}
	total, unreadTotal := 0, 0
	now := time.Now().UTC()
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var nullableID any
		if id != "" {
			nullableID = id
		}
		rows, err := tx.Query(ctx, unifiedItemsSQL, identity.TenantID, identity.DomainID, identity.ActorID, strings.ToUpper(itemType), nullableID, onlyUnread, now, limit, offset, kind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Item
			if err := rows.Scan(&item.Type, &item.ObjectID, &item.DomainID, &item.Status, &item.RequesterUserID, &item.SLADueAt, &item.UpdatedAt, &item.Version, &item.Summary, &item.SourceHref, &item.AllowedActions, &item.Unread, &item.Priority, &item.RequesterDisplayName, &item.AssigneeDisplayName, &total, &unreadTotal); err != nil {
				return err
			}
			item.Overdue = item.SLADueAt != nil && !item.SLADueAt.After(now)
			item.Kind = classifyKind(item.AllowedActions)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, unreadTotal, err
}

func (store *Store) Detail(ctx context.Context, identity Identity, itemType string, id askdata.ID) (Detail, error) {
	items, _, _, err := store.query(ctx, identity, false, strings.ToUpper(itemType), "", id, 2, 0)
	if err != nil {
		return Detail{}, err
	}
	if len(items) != 1 {
		return Detail{}, ErrNotFound
	}
	item := items[0]
	actionContext := ActionContext{
		SourceObjectID:    item.ObjectID,
		ExpectedVersion:   item.Version,
		ApprovalPurpose:   item.Summary,
		DifferenceSummary: "完整差异保留在来源模块的受权详情中",
		EvidenceSummary:   "工作箱未复制来源证据事实",
		RiskSummary:       "工作箱未复制来源风险事实",
		Commands:          []ActionCommand{},
	}
	err = database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return enrichActionContextTx(ctx, tx, identity, item, &actionContext)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	actionContext.Commands = sourceCommands(item, actionContext)
	return Detail{Item: item, ActionContext: actionContext}, nil
}

func enrichActionContextTx(ctx context.Context, tx pgx.Tx, identity Identity, item Item, target *ActionContext) error {
	setReference := func(name string, id askdata.ID) {
		if id == "" {
			return
		}
		if target.References == nil {
			target.References = map[string]askdata.ID{}
		}
		target.References[name] = id
	}
	switch item.Type {
	case "DATA_SOURCE_PUBLICATION":
		var sourceID, versionID askdata.ID
		if err := tx.QueryRow(ctx, `SELECT data_source_id::text,data_source_version_id::text,config_hash,version::text
			FROM platform.data_source_publication_requests WHERE tenant_id=$1 AND id=$2 AND status='PENDING'`,
			identity.TenantID, item.ObjectID).Scan(&sourceID, &versionID, &target.FixedHash, &target.ExpectedVersion); err != nil {
			return err
		}
		setReference("dataSourceId", sourceID)
		target.FixedVersionID = string(versionID)
		target.DifferenceSummary = "发布申请已固定数据源配置版本与配置哈希"
	case "DATASET_PUBLICATION":
		var datasetID, draftID askdata.ID
		if err := tx.QueryRow(ctx, `SELECT dataset_id::text,draft_version_id::text,expected_dsl_hash,version::text
			FROM platform.dataset_publication_requests WHERE tenant_id=$1 AND id=$2 AND status='PENDING'`,
			identity.TenantID, item.ObjectID).Scan(&datasetID, &draftID, &target.FixedHash, &target.ExpectedVersion); err != nil {
			return err
		}
		setReference("datasetId", datasetID)
		target.FixedVersionID = string(draftID)
		target.DifferenceSummary = "发布申请已固定草稿修订、DSL 哈希与执行计划哈希"
	case "DECISION_APPROVAL", "ACTION_ASSIGNED", "DECISION_REVIEW_DUE":
		var decisionID, actionID askdata.ID
		var decisionVersion, actionVersion int64
		var actionStatus string
		var evidenceCount, optionCount, riskCount int
		if err := tx.QueryRow(ctx, `SELECT notification.decision_id::text,COALESCE(notification.action_id::text,''),
			decision.record_version,COALESCE(action.record_version,0),COALESCE(action.status,''),
			(SELECT count(*) FROM decision.decision_evidence evidence WHERE evidence.tenant_id=decision.tenant_id AND evidence.decision_id=decision.id),
			(SELECT count(*) FROM decision.decision_options option_value WHERE option_value.tenant_id=decision.tenant_id AND option_value.decision_id=decision.id),
			jsonb_array_length(decision.risks_json)
			FROM decision.decision_notifications notification
			JOIN decision.decisions decision ON decision.tenant_id=notification.tenant_id AND decision.id=notification.decision_id
			LEFT JOIN decision.action_items action ON action.tenant_id=notification.tenant_id AND action.id=notification.action_id
			WHERE notification.tenant_id=$1 AND notification.domain_id=$2 AND notification.recipient_user_id=$3
			  AND notification.id=$4 AND notification.resolved_at IS NULL`, identity.TenantID, identity.DomainID, identity.ActorID, item.ObjectID).
			Scan(&decisionID, &actionID, &decisionVersion, &actionVersion, &actionStatus, &evidenceCount, &optionCount, &riskCount); err != nil {
			return err
		}
		setReference("decisionId", decisionID)
		setReference("actionId", actionID)
		target.FixedVersionID = strconv.FormatInt(decisionVersion, 10)
		target.ExpectedVersion = strconv.FormatInt(decisionVersion, 10)
		if item.Type == "ACTION_ASSIGNED" {
			target.ExpectedVersion = strconv.FormatInt(actionVersion, 10)
			target.FixedVersionID = target.ExpectedVersion
			target.DifferenceSummary = "行动项当前状态为 " + actionStatus
		} else {
			target.DifferenceSummary = fmt.Sprintf("决策包含 %d 个备选方案", optionCount)
		}
		target.EvidenceSummary = fmt.Sprintf("已固定 %d 条可验证证据引用", evidenceCount)
		target.RiskSummary = fmt.Sprintf("已声明 %d 项风险", riskCount)
	case "RUNTIME_CONFIG_APPROVAL":
		var versionNo int
		if err := tx.QueryRow(ctx, `SELECT version_no,config_hash,impact_summary,record_version::text
			FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2 AND state='IN_REVIEW'`, identity.TenantID, item.ObjectID).
			Scan(&versionNo, &target.FixedHash, &target.ApprovalPurpose, &target.ExpectedVersion); err != nil {
			return err
		}
		target.FixedVersionID = strconv.Itoa(versionNo)
		target.DifferenceSummary = "配置版本已完成 Schema、引用和安全校验"
		target.EvidenceSummary = "配置内容哈希已固定；工作箱不返回配置值"
		target.RiskSummary = "影响摘要由申请人提交，完整变更需在来源详情复核"
	case "REPORT_EXPORT_FAILED":
		var reportID, versionID askdata.ID
		var attempt int
		if err := tx.QueryRow(ctx, `SELECT report_id::text,report_version_id::text,attempt
			FROM platform.report_export_jobs WHERE tenant_id=$1 AND domain_id=$2 AND requested_by=$3 AND id=$4 AND state='FAILED'`,
			identity.TenantID, identity.DomainID, identity.ActorID, item.ObjectID).Scan(&reportID, &versionID, &attempt); err != nil {
			return err
		}
		setReference("reportId", reportID)
		target.FixedVersionID = string(versionID)
		target.DifferenceSummary = fmt.Sprintf("固定报告版本的导出已失败，共尝试 %d 次", attempt)
	case "REPORT_DELIVERY_READY", "REPORT_DELIVERY_FAILED":
		var scheduleID, reportID, versionID askdata.ID
		var scheduleVersion int64
		if err := tx.QueryRow(ctx, `SELECT delivery.schedule_id::text,delivery.report_id::text,delivery.report_version_id::text,schedule.record_version
			FROM platform.report_deliveries delivery JOIN platform.report_schedules schedule
			  ON schedule.tenant_id=delivery.tenant_id AND schedule.id=delivery.schedule_id
			WHERE delivery.tenant_id=$1 AND delivery.domain_id=$2 AND delivery.id=$3
			  AND (delivery.recipient_user_id=$4 OR schedule.owner_user_id=$4)`,
			identity.TenantID, identity.DomainID, item.ObjectID, identity.ActorID).Scan(&scheduleID, &reportID, &versionID, &scheduleVersion); err != nil {
			return err
		}
		setReference("scheduleId", scheduleID)
		setReference("reportId", reportID)
		target.FixedVersionID = string(versionID)
		if item.Type == "REPORT_DELIVERY_FAILED" {
			target.ExpectedVersion = strconv.FormatInt(scheduleVersion, 10)
		}
		target.DifferenceSummary = "分发任务固定报告版本且不包含结果数据副本"
	}
	return nil
}

func sourceCommands(item Item, context ActionContext) []ActionCommand {
	commands := make([]ActionCommand, 0, len(item.AllowedActions))
	versionField := func(name string) ActionFieldConstraint {
		minimum := int64(1)
		return ActionFieldConstraint{Name: name, Type: "integer", Required: true, Min: &minimum}
	}
	textField := func(name string, required bool, max int) ActionFieldConstraint {
		return ActionFieldConstraint{Name: name, Type: "string", Required: required, MaxLength: max}
	}
	for _, action := range item.AllowedActions {
		command := ActionCommand{Action: action, Method: http.MethodPost, IdempotencyRequired: true}
		switch item.Type {
		case "DOMAIN_ACCESS_APPROVAL":
			command.Href = "/api/v1/domain-applications/" + string(item.ObjectID) + "/decision"
			command.FixedValues = map[string]string{"decision": map[bool]string{true: "APPROVED", false: "REJECTED"}[action == "APPROVE"]}
			command.Fields = []ActionFieldConstraint{textField("comment", action == "REJECT", 2000)}
		case "DATA_SOURCE_PUBLICATION":
			sourceID := context.References["dataSourceId"]
			command.Href = "/api/v1/data-sources/" + string(sourceID) + "/publish-requests/" + string(item.ObjectID) + "/" + strings.ToLower(action)
			command.Fields = []ActionFieldConstraint{versionField("expectedVersion"), textField("reason", action == "REJECT", 1000)}
		case "DATASET_PUBLICATION":
			datasetID := context.References["datasetId"]
			command.Href = "/api/v1/datasets/" + string(datasetID) + "/publish-requests/" + string(item.ObjectID) + "/" + strings.ToLower(action)
			name := "note"
			if action == "REJECT" {
				name = "reason"
			}
			command.Fields = []ActionFieldConstraint{versionField("expectedVersion"), textField(name, action == "REJECT", 1000)}
		case "DATA_REQUEST":
			command.Href = "/api/v1/data-requests/" + string(item.ObjectID) + "/transition"
			target := map[string]string{"APPROVE": "APPROVED", "REJECT": "REJECTED", "START": "IN_PROGRESS"}[action]
			command.FixedValues = map[string]string{"toState": target}
			command.Fields = []ActionFieldConstraint{versionField("recordVersion"), textField("note", action == "REJECT", 2000)}
			if action == "START" {
				command.Fields = append(command.Fields, ActionFieldConstraint{Name: "assigneeUserId", Type: "uuid", Required: false})
			}
		case "DECISION_APPROVAL":
			command.Href = "/api/v1/decisions/" + string(context.References["decisionId"]) + "/approvals"
			command.FixedValues = map[string]string{"decision": action}
			command.Fields = []ActionFieldConstraint{versionField("expectedVersion"), textField("comment", action == "REJECT", 4096)}
		case "ACTION_ASSIGNED":
			command.Href = "/api/v1/decisions/" + string(context.References["decisionId"]) + "/actions/" + string(context.References["actionId"]) + "/transition"
			command.FixedValues = map[string]string{"target": map[string]string{"START": "DOING", "BLOCK": "BLOCKED", "COMPLETE": "DONE"}[action]}
			command.Fields = []ActionFieldConstraint{versionField("expectedVersion")}
			if action == "BLOCK" {
				command.Fields = append(command.Fields, textField("reason", true, 4096))
			}
			if action == "COMPLETE" {
				command.Fields = append(command.Fields, textField("completionEvidence", true, 2048))
			}
		case "RUNTIME_CONFIG_APPROVAL":
			command.Href = "/api/v1/runtime-config/versions/" + string(item.ObjectID) + "/" + strings.ToLower(action)
			command.Fields = []ActionFieldConstraint{versionField("expectedVersion")}
			if action == "REJECT" {
				command.Fields = append(command.Fields, textField("reason", true, 2000))
			}
		case "REPORT_EXPORT_FAILED":
			command.Href = "/api/v1/reports/" + string(context.References["reportId"]) + "/exports/" + string(item.ObjectID) + "/retry"
		case "REPORT_DELIVERY_READY":
			if action == "MARK_READ" {
				command.Href = "/api/v1/report-deliveries/" + string(item.ObjectID) + "/read"
			} else {
				command.Method, command.Href, command.IdempotencyRequired = http.MethodGet, item.SourceHref, false
			}
		case "REPORT_DELIVERY_FAILED":
			if action == "RESUME" {
				command.Href = "/api/v1/report-schedules/" + string(context.References["scheduleId"]) + "/resume"
				command.Fields = []ActionFieldConstraint{versionField("expectedVersion")}
			} else {
				command.Method, command.Href, command.IdempotencyRequired = http.MethodGet, item.SourceHref, false
			}
		default:
			if action == "OPEN" {
				command.Method, command.Href, command.IdempotencyRequired = http.MethodGet, item.SourceHref, false
			} else {
				continue
			}
		}
		commands = append(commands, command)
	}
	return commands
}

func (store *Store) MarkRead(ctx context.Context, identity Identity, itemType string, id askdata.ID, now time.Time) (Item, error) {
	items, _, _, err := store.query(ctx, identity, false, strings.ToUpper(itemType), "", id, 2, 0)
	if err != nil {
		return Item{}, err
	}
	if len(items) != 1 {
		return Item{}, ErrNotFound
	}
	item := items[0]
	err = database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.work_item_receipts(tenant_id,domain_id,actor_user_id,source_type,source_id,source_version,read_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$7) ON CONFLICT(tenant_id,domain_id,actor_user_id,source_type,source_id)
			DO UPDATE SET source_version=EXCLUDED.source_version,read_at=EXCLUDED.read_at,updated_at=EXCLUDED.updated_at`, identity.TenantID, identity.DomainID, identity.ActorID, item.Type, item.ObjectID, item.Version, now)
		return err
	})
	item.Unread = false
	return item, err
}
func classifyKind(actions []string) string {
	for _, action := range actions {
		if action == "APPROVE" || action == "REJECT" {
			return "APPROVAL"
		}
	}
	return "TASK"
}

func (identity Identity) validate() error {
	for _, id := range []askdata.ID{identity.TenantID, identity.DomainID, identity.ActorID} {
		if id.Validate() != nil {
			return ErrInvalid
		}
	}
	return nil
}

type Handler struct{ store *Store }

func NewHandler(authService *auth.Service, idempotency platformidempotency.Repository, store *Store) http.Handler {
	h := &Handler{store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/work-items", h.list)
	mux.HandleFunc("GET /api/v1/work-items/{type}/{id}", h.detail)
	mux.HandleFunc("POST /api/v1/work-items/{type}/{id}/read", h.markRead)
	governed := platformidempotency.Middleware(platformidempotency.MiddlewareOptions{Repository: idempotency, ResolveIdentity: func(ctx context.Context) (platformidempotency.Identity, error) {
		i, e := identityFromContext(ctx)
		return platformidempotency.Identity{TenantID: string(i.TenantID), ActorID: string(i.ActorID)}, e
	}, Requires: platformidempotency.RequiresGovernedWrite, WriteError: writeError, MaxRequestBytes: 1024}, mux)
	return auth.RequireAccessToken(authService, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		governed.ServeHTTP(w, r)
	}))
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	i, e := identityFromContext(r.Context())
	if e != nil {
		writeError(w, 403, "WORK_ITEM_FORBIDDEN", "work inbox is forbidden")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil {
			writeError(w, 400, "WORK_ITEM_INVALID", "limit is invalid")
			return
		}
	}
	unread := r.URL.Query().Get("unread") == "true"
	page, e := h.store.ListPageKind(r.Context(), i, unread, r.URL.Query().Get("type"), r.URL.Query().Get("kind"), limit, r.URL.Query().Get("cursor"))
	if e != nil {
		writeStoreError(w, e)
		return
	}
	writeJSON(w, 200, page)
}
func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	i, e := identityFromContext(r.Context())
	if e != nil {
		writeError(w, 403, "WORK_ITEM_FORBIDDEN", "work inbox is forbidden")
		return
	}
	value, e := h.store.Detail(r.Context(), i, r.PathValue("type"), askdata.ID(r.PathValue("id")))
	if e != nil {
		writeStoreError(w, e)
		return
	}
	writeJSON(w, http.StatusOK, value)
}
func (h *Handler) markRead(w http.ResponseWriter, r *http.Request) {
	i, e := identityFromContext(r.Context())
	if e != nil {
		writeError(w, 403, "WORK_ITEM_FORBIDDEN", "work inbox is forbidden")
		return
	}
	if decodeEmpty(r) != nil {
		writeError(w, 400, "WORK_ITEM_INVALID", "request body is invalid")
		return
	}
	item, e := h.store.MarkRead(r.Context(), i, r.PathValue("type"), askdata.ID(r.PathValue("id")), time.Now().UTC())
	if e != nil {
		writeStoreError(w, e)
		return
	}
	writeJSON(w, 200, item)
}
func identityFromContext(ctx context.Context) (Identity, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	access, aok := database.AccessContextFromContext(ctx)
	if !ok || !aok || claims.Subject != access.UserID || access.DomainID == "" {
		return Identity{}, ErrInvalid
	}
	i := Identity{askdata.ID(claims.TenantID), askdata.ID(access.DomainID), askdata.ID(claims.Subject)}
	return i, i.validate()
}
func decodeEmpty(r *http.Request) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	var body map[string]json.RawMessage
	d := json.NewDecoder(io.LimitReader(r.Body, 1025))
	if d.Decode(&body) != nil || len(body) != 0 {
		return ErrInvalid
	}
	var x any
	if !errors.Is(d.Decode(&x), io.EOF) {
		return ErrInvalid
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}
func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvalid) {
		writeError(w, 400, "WORK_ITEM_INVALID", err.Error())
		return
	}
	if errors.Is(err, ErrNotFound) {
		writeError(w, 404, "WORK_ITEM_NOT_FOUND", err.Error())
		return
	}
	writeError(w, 500, "WORK_ITEM_INTERNAL", "work inbox operation failed")
}
