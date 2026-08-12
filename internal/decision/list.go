package decision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type ListQuery struct {
	Scope        string
	Search       string
	Status       Status
	EvidenceMode EvidenceMode
	DecisionType string
	ReviewFrom   *time.Time
	ReviewTo     *time.Time
	Owner        askdata.ID
	Sort         string
	Limit        int
	Cursor       string
}

type ScopeCounts struct {
	Mine      int `json:"mine"`
	Approvals int `json:"approvals"`
	Actions   int `json:"actions"`
	Reviews   int `json:"reviews"`
}

type DecisionListItem struct {
	Decision
	OwnerDisplayName      string     `json:"ownerDisplayName"`
	OwnerAvatarURL        string     `json:"ownerAvatarUrl,omitempty"`
	OwnerDepartment       string     `json:"ownerDepartment,omitempty"`
	EvidenceCount         int        `json:"evidenceCount"`
	VerifiedEvidenceCount int        `json:"verifiedEvidenceCount"`
	ActionTotal           int        `json:"actionTotal"`
	ActionDone            int        `json:"actionDone"`
	ActionBlocked         int        `json:"actionBlocked"`
	NextActionDueAt       *time.Time `json:"nextActionDueAt,omitempty"`
	AllowedActions        []string   `json:"allowedActions"`
}

type DecisionListPage struct {
	Items       []DecisionListItem `json:"items"`
	NextCursor  string             `json:"nextCursor,omitempty"`
	Total       int                `json:"total"`
	ScopeCounts ScopeCounts        `json:"scopeCounts"`
}

type ApprovalPolicySummary struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	RequiredApprovals int    `json:"requiredApprovals"`
	ApproverSummary   string `json:"approverSummary"`
}

type decisionListRepository interface {
	ListDetailed(context.Context, Identity, ListQuery) (DecisionListPage, error)
	ListApprovalPolicies(context.Context, Identity) ([]ApprovalPolicySummary, error)
}

type decisionListCursor struct {
	Sort string     `json:"sort"`
	At   time.Time  `json:"at"`
	ID   askdata.ID `json:"id"`
}

type decisionListRow struct {
	ID                    askdata.ID   `json:"id"`
	OwnerUserID           askdata.ID   `json:"owner_user_id"`
	CreatedBy             askdata.ID   `json:"created_by"`
	Title                 string       `json:"title"`
	Question              string       `json:"question"`
	DecisionText          string       `json:"decision_text"`
	ExpectedEffect        string       `json:"expected_effect"`
	Risks                 []string     `json:"risks_json"`
	Status                Status       `json:"status"`
	EvidenceMode          EvidenceMode `json:"evidence_mode"`
	ApprovalPolicyID      string       `json:"approval_policy_id"`
	RequiredApprovals     int          `json:"required_approvals"`
	ReviewAt              time.Time    `json:"review_at"`
	TerminalReason        string       `json:"terminal_reason"`
	RecordVersion         int64        `json:"record_version"`
	CreatedAt             time.Time    `json:"created_at"`
	UpdatedAt             time.Time    `json:"updated_at"`
	OwnerDisplayName      string       `json:"owner_display_name"`
	OwnerAvatarURL        string       `json:"owner_avatar_url"`
	OwnerDepartment       string       `json:"owner_department"`
	EvidenceCount         int          `json:"evidence_count"`
	VerifiedEvidenceCount int          `json:"verified_evidence_count"`
	ActionTotal           int          `json:"action_total"`
	ActionDone            int          `json:"action_done"`
	ActionBlocked         int          `json:"action_blocked"`
	NextActionDueAt       *time.Time   `json:"next_action_due_at"`
	Mine                  bool         `json:"is_mine"`
	PendingApproval       bool         `json:"pending_approval"`
	ActiveAction          bool         `json:"active_action"`
	ReviewDue             bool         `json:"review_due"`
}

func (service *Service) ListDetailed(ctx context.Context, identity Identity, query ListQuery) (DecisionListPage, error) {
	if identity.Validate() != nil {
		return DecisionListPage{}, ErrInvalid
	}
	query.Scope = strings.ToUpper(strings.TrimSpace(query.Scope))
	query.Search = strings.TrimSpace(query.Search)
	query.Sort = strings.ToUpper(strings.TrimSpace(query.Sort))
	query.DecisionType = strings.TrimSpace(query.DecisionType)
	if query.Sort == "" {
		query.Sort = "UPDATED_DESC"
	}
	if query.Limit < 1 || query.Limit > 200 || len(query.Search) > 256 || query.DecisionType != "" ||
		(query.Scope != "" && query.Scope != "MINE" && query.Scope != "APPROVALS" && query.Scope != "ACTIONS" && query.Scope != "REVIEWS") ||
		(query.Sort != "UPDATED_DESC" && query.Sort != "REVIEW_ASC" && query.Sort != "REVIEW_DESC") ||
		(query.Status != "" && !validDecisionStatus(query.Status)) ||
		(query.EvidenceMode != "" && query.EvidenceMode != EvidencePlatformVerified && query.EvidenceMode != EvidenceManual) ||
		(query.Owner != "" && !validUUID(query.Owner)) ||
		(query.ReviewFrom != nil && query.ReviewTo != nil && query.ReviewFrom.After(*query.ReviewTo)) {
		return DecisionListPage{}, ErrInvalid
	}
	repository, ok := service.repository.(decisionListRepository)
	if !ok {
		items, cursor, err := service.List(ctx, identity, query.Scope, query.Limit, query.Cursor)
		if err != nil {
			return DecisionListPage{}, err
		}
		page := DecisionListPage{Items: make([]DecisionListItem, 0, len(items)), NextCursor: cursor, Total: len(items)}
		for _, item := range items {
			page.Items = append(page.Items, DecisionListItem{Decision: item, AllowedActions: []string{"OPEN"}})
		}
		return page, nil
	}
	return repository.ListDetailed(ctx, identity, query)
}

func (service *Service) ListApprovalPolicies(ctx context.Context, identity Identity) ([]ApprovalPolicySummary, error) {
	if identity.Validate() != nil {
		return nil, ErrInvalid
	}
	repository, ok := service.repository.(decisionListRepository)
	if !ok {
		return nil, ErrPolicyUnavailable
	}
	return repository.ListApprovalPolicies(ctx, identity)
}

func validDecisionStatus(status Status) bool {
	switch status {
	case StatusDraft, StatusInReview, StatusApproved, StatusRejected, StatusInExecution, StatusReviewDue, StatusClosed, StatusReopened, StatusCanceled:
		return true
	default:
		return false
	}
}

func (store *PostgresStore) ListDetailed(ctx context.Context, identity Identity, query ListQuery) (DecisionListPage, error) {
	var cursor *decisionListCursor
	if query.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		var parsed decisionListCursor
		if err != nil || len(raw) > 1024 || askdata.DecodeStrictJSON(raw, &parsed) != nil || parsed.Sort != query.Sort || parsed.At.IsZero() || !validUUID(parsed.ID) {
			return DecisionListPage{}, ErrInvalid
		}
		cursor = &parsed
	}
	var cursorAt, cursorID any
	if cursor != nil {
		cursorAt, cursorID = cursor.At, cursor.ID
	}
	orderColumn, orderDirection, cursorOperator := "updated_at", "DESC", "<"
	if query.Sort == "REVIEW_ASC" {
		orderColumn, orderDirection, cursorOperator = "review_at", "ASC", ">"
	} else if query.Sort == "REVIEW_DESC" {
		orderColumn = "review_at"
	}
	page := DecisionListPage{Items: []DecisionListItem{}}
	var raw []byte
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		statement := fmt.Sprintf(`WITH visible AS (
		  SELECT decision_value.*,
		    owner.display_name AS owner_display_name,COALESCE(owner.attributes->>'avatarUrl','') AS owner_avatar_url,
		    COALESCE(owner.attributes->>'department','') AS owner_department,
		    (decision_value.owner_user_id=$3 OR decision_value.created_by=$3) AS is_mine,
		    EXISTS(SELECT 1 FROM decision.decision_approvals approval
		      WHERE approval.tenant_id=decision_value.tenant_id AND approval.decision_id=decision_value.id
		        AND approval.approver_user_id=$3 AND approval.status='PENDING'
		        AND NOT EXISTS(SELECT 1 FROM decision.decision_approval_events approval_event
		          WHERE approval_event.tenant_id=approval.tenant_id AND approval_event.approval_id=approval.id)) AS pending_approval,
		    EXISTS(SELECT 1 FROM decision.action_items action_value
		      WHERE action_value.tenant_id=decision_value.tenant_id AND action_value.decision_id=decision_value.id
		        AND action_value.assignee_user_id=$3 AND action_value.status NOT IN ('DONE','CANCELED')) AS active_action,
		    (decision_value.owner_user_id=$3 AND decision_value.status='REVIEW_DUE') AS review_due,
		    (SELECT count(*) FROM decision.decision_evidence evidence WHERE evidence.tenant_id=decision_value.tenant_id AND evidence.decision_id=decision_value.id) AS evidence_count,
		    (SELECT count(*) FROM decision.decision_evidence evidence WHERE evidence.tenant_id=decision_value.tenant_id AND evidence.decision_id=decision_value.id AND evidence.verified) AS verified_evidence_count,
		    (SELECT count(*) FROM decision.action_items action_value WHERE action_value.tenant_id=decision_value.tenant_id AND action_value.decision_id=decision_value.id) AS action_total,
		    (SELECT count(*) FROM decision.action_items action_value WHERE action_value.tenant_id=decision_value.tenant_id AND action_value.decision_id=decision_value.id AND action_value.status='DONE') AS action_done,
		    (SELECT count(*) FROM decision.action_items action_value WHERE action_value.tenant_id=decision_value.tenant_id AND action_value.decision_id=decision_value.id AND action_value.status='BLOCKED') AS action_blocked,
		    (SELECT min(action_value.due_at) FROM decision.action_items action_value WHERE action_value.tenant_id=decision_value.tenant_id AND action_value.decision_id=decision_value.id AND action_value.status NOT IN ('DONE','CANCELED')) AS next_action_due_at
		  FROM decision.decisions decision_value JOIN platform.users owner
		    ON owner.tenant_id=decision_value.tenant_id AND owner.id=decision_value.owner_user_id
		  WHERE decision_value.tenant_id=$1 AND decision_value.domain_id=$2
		), counts AS (
		  SELECT count(*) FILTER(WHERE is_mine) AS mine,count(*) FILTER(WHERE pending_approval) AS approvals,
		    count(*) FILTER(WHERE active_action) AS actions,count(*) FILTER(WHERE review_due) AS reviews FROM visible
		), filtered AS (
		  SELECT * FROM visible WHERE ($4='' OR ($4='MINE' AND is_mine) OR ($4='APPROVALS' AND pending_approval)
		    OR ($4='ACTIONS' AND active_action) OR ($4='REVIEWS' AND review_due))
		    AND ($5='' OR title ILIKE '%%'||replace(replace(replace($5,'\','\\'),'%%','\%%'),'_','\_')||'%%' ESCAPE '\'
		      OR question ILIKE '%%'||replace(replace(replace($5,'\','\\'),'%%','\%%'),'_','\_')||'%%' ESCAPE '\')
		    AND ($6='' OR status=$6) AND ($7='' OR evidence_mode=$7)
		    AND ($8::uuid IS NULL OR owner_user_id=$8) AND ($9::timestamptz IS NULL OR review_at>=$9)
		    AND ($10::timestamptz IS NULL OR review_at<=$10)
		), paged AS (
		  SELECT * FROM filtered WHERE ($11::timestamptz IS NULL OR (%s,id)%s($11,$12::uuid))
		  ORDER BY %s %s,id %s LIMIT $13
		)
		SELECT COALESCE((SELECT jsonb_agg(to_jsonb(ordered)) FROM (SELECT * FROM paged ORDER BY %s %s,id %s) ordered),'[]'::jsonb),
		  (SELECT count(*) FROM filtered),counts.mine,counts.approvals,counts.actions,counts.reviews FROM counts`,
			orderColumn, cursorOperator, orderColumn, orderDirection, orderDirection, orderColumn, orderDirection, orderDirection)
		var owner any
		if query.Owner != "" {
			owner = query.Owner
		}
		return tx.QueryRow(ctx, statement, identity.TenantID, identity.DomainID, identity.ActorID, query.Scope, query.Search,
			query.Status, query.EvidenceMode, owner, query.ReviewFrom, query.ReviewTo, cursorAt, cursorID, query.Limit+1).
			Scan(&raw, &page.Total, &page.ScopeCounts.Mine, &page.ScopeCounts.Approvals, &page.ScopeCounts.Actions, &page.ScopeCounts.Reviews)
	})
	if err != nil {
		return DecisionListPage{}, mapDBError(err)
	}
	var rows []decisionListRow
	if json.Unmarshal(raw, &rows) != nil {
		return DecisionListPage{}, fmt.Errorf("decode decision list projection: %w", ErrInvalid)
	}
	if len(rows) > query.Limit {
		last := rows[query.Limit-1]
		at := last.UpdatedAt
		if query.Sort == "REVIEW_ASC" || query.Sort == "REVIEW_DESC" {
			at = last.ReviewAt
		}
		cursorRaw, _ := json.Marshal(decisionListCursor{Sort: query.Sort, At: at, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(cursorRaw)
		rows = rows[:query.Limit]
	}
	for _, row := range rows {
		item := DecisionListItem{
			Decision: Decision{SchemaVersion: SchemaVersion, ID: row.ID, OwnerUserID: row.OwnerUserID, CreatedBy: row.CreatedBy,
				Title: row.Title, Question: row.Question, Decision: row.DecisionText, ExpectedEffect: row.ExpectedEffect,
				Risks: row.Risks, Status: row.Status, EvidenceMode: row.EvidenceMode, ApprovalPolicyID: row.ApprovalPolicyID,
				RequiredApprovals: row.RequiredApprovals, ReviewAt: row.ReviewAt, TerminalReason: row.TerminalReason,
				RecordVersion: row.RecordVersion, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt},
			OwnerDisplayName: row.OwnerDisplayName, OwnerAvatarURL: row.OwnerAvatarURL, OwnerDepartment: row.OwnerDepartment,
			EvidenceCount: row.EvidenceCount, VerifiedEvidenceCount: row.VerifiedEvidenceCount, ActionTotal: row.ActionTotal,
			ActionDone: row.ActionDone, ActionBlocked: row.ActionBlocked, NextActionDueAt: row.NextActionDueAt,
			AllowedActions: decisionAllowedActions(identity, row),
		}
		if item.Risks == nil {
			item.Risks = []string{}
		}
		page.Items = append(page.Items, item)
	}
	return page, nil
}

func decisionAllowedActions(identity Identity, row decisionListRow) []string {
	actions := []string{"OPEN"}
	if row.PendingApproval && row.Status == StatusInReview {
		actions = append(actions, "APPROVE", "REJECT")
	}
	if row.OwnerUserID != identity.ActorID {
		return actions
	}
	switch row.Status {
	case StatusDraft:
		actions = append(actions, "EDIT", "SUBMIT", "CANCEL")
	case StatusApproved:
		actions = append(actions, "CREATE_ACTION", "ADD_OUTCOME_METRIC", "CANCEL")
	case StatusInExecution:
		actions = append(actions, "CREATE_ACTION", "ADD_OUTCOME_METRIC", "REFRESH_OUTCOME", "CANCEL")
	case StatusReviewDue:
		actions = append(actions, "REFRESH_OUTCOME", "CONFIRM_OUTCOME", "CLOSE", "REOPEN", "CANCEL")
	case StatusReopened:
		actions = append(actions, "EDIT", "CREATE_ACTION", "ADD_OUTCOME_METRIC", "REFRESH_OUTCOME", "CANCEL")
	case StatusRejected, StatusClosed:
		actions = append(actions, "REOPEN")
	}
	return actions
}

func (store *PostgresStore) ListApprovalPolicies(ctx context.Context, identity Identity) ([]ApprovalPolicySummary, error) {
	items := []ApprovalPolicySummary{}
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT policy.id,policy.name,policy.required_approvals,
			count(approver.approver_user_id) FROM decision.approval_policies policy
			JOIN decision.approval_policy_approvers approver ON approver.tenant_id=policy.tenant_id
			  AND approver.domain_id=policy.domain_id AND approver.policy_id=policy.id
			JOIN platform.users user_account ON user_account.tenant_id=approver.tenant_id
			  AND user_account.id=approver.approver_user_id AND user_account.status='ACTIVE'
			  AND user_account.deleted_at IS NULL
			JOIN platform.domain_memberships membership ON membership.tenant_id=approver.tenant_id
			  AND membership.domain_id=approver.domain_id AND membership.user_id=approver.approver_user_id
			  AND membership.status='ACTIVE'
			WHERE policy.tenant_id=$1 AND policy.domain_id=$2 AND policy.status='ACTIVE'
			GROUP BY policy.id,policy.domain_id,policy.tenant_id,policy.name,policy.required_approvals
			HAVING count(approver.approver_user_id)>=policy.required_approvals ORDER BY policy.name,policy.id`, identity.TenantID, identity.DomainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ApprovalPolicySummary
			var count int
			if err = rows.Scan(&item.ID, &item.Name, &item.RequiredApprovals, &count); err != nil {
				return err
			}
			item.ApproverSummary = fmt.Sprintf("%d 位审批人，需 %d 人批准", count, item.RequiredApprovals)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, mapDBError(err)
}
