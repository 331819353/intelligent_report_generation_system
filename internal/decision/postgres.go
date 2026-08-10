package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) ResolveApprovalPolicy(ctx context.Context, identity Identity, id string) (ApprovalPolicy, error) {
	if store == nil || store.pool == nil || identity.Validate() != nil || !validText(id, 1, 128) {
		return ApprovalPolicy{}, ErrInvalid
	}
	var result ApprovalPolicy
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var err error
		result, err = resolvePolicyTx(ctx, tx, identity, id)
		return err
	})
	return result, mapDBError(err)
}

func resolvePolicyTx(ctx context.Context, tx pgx.Tx, identity Identity, id string) (ApprovalPolicy, error) {
	result := ApprovalPolicy{ID: id}
	if err := tx.QueryRow(ctx, `SELECT required_approvals FROM decision.approval_policies
		WHERE tenant_id=$1 AND domain_id=$2 AND id=$3 AND status='ACTIVE'`, identity.TenantID, identity.DomainID, id).Scan(&result.RequiredApprovals); err != nil {
		return ApprovalPolicy{}, err
	}
	rows, err := tx.Query(ctx, `SELECT approver.approver_user_id::text
		FROM decision.approval_policy_approvers approver JOIN platform.users user_account
		  ON user_account.id=approver.approver_user_id AND user_account.tenant_id=approver.tenant_id
		JOIN platform.domain_memberships membership ON membership.tenant_id=approver.tenant_id
		 AND membership.domain_id=approver.domain_id AND membership.user_id=approver.approver_user_id
		WHERE approver.tenant_id=$1 AND approver.domain_id=$2 AND approver.policy_id=$3
		 AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL AND membership.status='ACTIVE'
		ORDER BY approver.sequence_no`, identity.TenantID, identity.DomainID, id)
	if err != nil {
		return ApprovalPolicy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var value askdata.ID
		if err := rows.Scan(&value); err != nil {
			return ApprovalPolicy{}, err
		}
		result.ApproverUserIDs = append(result.ApproverUserIDs, value)
	}
	if err := rows.Err(); err != nil {
		return ApprovalPolicy{}, err
	}
	if result.RequiredApprovals < 1 || result.RequiredApprovals > len(result.ApproverUserIDs) {
		return ApprovalPolicy{}, ErrPolicyUnavailable
	}
	return result, nil
}

func (store *PostgresStore) Create(ctx context.Context, identity Identity, input CreateInput, policy ApprovalPolicy, evidence []Evidence, now time.Time) (Aggregate, error) {
	if store == nil || store.pool == nil {
		return Aggregate{}, ErrInvalid
	}
	id := askdata.ID(uuid.NewString())
	var result Aggregate
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		inside, err := resolvePolicyTx(ctx, tx, identity, policy.ID)
		if err != nil {
			return err
		}
		if !samePolicy(inside, policy) {
			return ErrConflict
		}
		var ownerOK bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.users user_account
			JOIN platform.domain_memberships membership ON membership.tenant_id=user_account.tenant_id AND membership.user_id=user_account.id
			WHERE user_account.tenant_id=$1 AND user_account.id=$2 AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL
			 AND membership.domain_id=$3 AND membership.status='ACTIVE')`, identity.TenantID, input.OwnerUserID, identity.DomainID).Scan(&ownerOK); err != nil {
			return err
		}
		if !ownerOK {
			return ErrForbidden
		}
		risks, _ := json.Marshal(input.Risks)
		_, err = tx.Exec(ctx, `INSERT INTO decision.decisions(id,tenant_id,domain_id,owner_user_id,created_by,title,question,
			decision_text,expected_effect,risks_json,evidence_mode,approval_policy_id,required_approvals,status,review_at,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'DRAFT',$14,$15,$15)`, id, identity.TenantID, identity.DomainID,
			input.OwnerUserID, identity.ActorID, input.Title, input.Question, input.Decision, input.ExpectedEffect, risks, input.EvidenceMode, policy.ID, policy.RequiredApprovals, input.ReviewAt, now)
		if err != nil {
			return err
		}
		for _, option := range input.Options {
			_, err = tx.Exec(ctx, `INSERT INTO decision.decision_options(id,tenant_id,domain_id,decision_id,title,description,selected,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.NewString(), identity.TenantID, identity.DomainID, id, option.Title, option.Description, option.Selected, now)
			if err != nil {
				return err
			}
		}
		for _, item := range evidence {
			_, err = tx.Exec(ctx, `INSERT INTO decision.decision_evidence(id,tenant_id,domain_id,decision_id,source_type,source_id,source_hash,
			semantic_release_id,semantic_release_hash,data_snapshot_id,as_of,policy_scope_hash,summary,verified,created_by,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10::text,'')::uuid,$11,$12,$13,true,$14,$15)`, uuid.NewString(), identity.TenantID, identity.DomainID, id,
				item.SourceType, item.SourceID, item.SourceHash, item.SemanticReleaseID, item.SemanticReleaseHash, item.DataSnapshotID, item.AsOf, item.PolicyScopeHash, item.Summary, identity.ActorID, now)
			if err != nil {
				return err
			}
		}
		if err := appendDecisionEventTx(ctx, tx, identity, id, "CREATED", "", string(StatusDraft), "", 1, now); err != nil {
			return err
		}
		result, err = loadAggregateTx(ctx, tx, identity, id, true)
		return err
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) List(ctx context.Context, identity Identity, scope string, limit int, cursor string) ([]Decision, string, error) {
	items := []Decision{}
	next := ""
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var cursorTime *time.Time
		var cursorID string
		if cursor != "" {
			parts := strings.Split(cursor, "|")
			if len(parts) != 2 {
				return ErrInvalid
			}
			parsed, err := time.Parse(time.RFC3339Nano, parts[0])
			if err != nil || uuid.Validate(parts[1]) != nil {
				return ErrInvalid
			}
			cursorTime = &parsed
			cursorID = parts[1]
		}
		rows, err := tx.Query(ctx, `SELECT id::text,owner_user_id::text,created_by::text,title,question,decision_text,expected_effect,risks_json,status,evidence_mode,
			approval_policy_id,required_approvals,review_at,terminal_reason,record_version,created_at,updated_at
		FROM decision.decisions d WHERE tenant_id=$1 AND domain_id=$2
		 AND ($3='' OR ($3='MINE' AND (owner_user_id=$4 OR created_by=$4))
		  OR ($3='APPROVALS' AND EXISTS(SELECT 1 FROM decision.decision_approvals a WHERE a.tenant_id=d.tenant_id AND a.decision_id=d.id AND a.approver_user_id=$4 AND a.status='PENDING'
		    AND NOT EXISTS(SELECT 1 FROM decision.decision_approval_events ae WHERE ae.tenant_id=a.tenant_id AND ae.approval_id=a.id)))
		  OR ($3='ACTIONS' AND EXISTS(SELECT 1 FROM decision.action_items i WHERE i.tenant_id=d.tenant_id AND i.decision_id=d.id AND i.assignee_user_id=$4 AND i.status NOT IN ('DONE','CANCELED')))
		  OR ($3='REVIEWS' AND d.owner_user_id=$4 AND d.status='REVIEW_DUE'))
		 AND ($5::timestamptz IS NULL OR (updated_at,id)<($5::timestamptz,$6::uuid))
		ORDER BY updated_at DESC,id DESC LIMIT $7`, identity.TenantID, identity.DomainID, scope, identity.ActorID, cursorTime, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			value, err := scanDecision(rows)
			if err != nil {
				return err
			}
			items = append(items, value)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(items) > limit {
			last := items[limit-1]
			next = last.UpdatedAt.Format(time.RFC3339Nano) + "|" + string(last.ID)
			items = items[:limit]
		}
		return nil
	})
	return items, next, mapDBError(err)
}

func (store *PostgresStore) Get(ctx context.Context, identity Identity, id askdata.ID, events bool) (Aggregate, error) {
	var result Aggregate
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var err error
		result, err = loadAggregateTx(ctx, tx, identity, id, events)
		return err
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) Update(ctx context.Context, identity Identity, id askdata.ID, input UpdateInput, now time.Time) (Decision, error) {
	var result Decision
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		risks, _ := json.Marshal(input.Risks)
		row := tx.QueryRow(ctx, `UPDATE decision.decisions SET title=$1,question=$2,decision_text=$3,expected_effect=$4,risks_json=$5,review_at=$6,
			record_version=record_version+1,updated_at=$7 WHERE tenant_id=$8 AND domain_id=$9 AND id=$10 AND owner_user_id=$11 AND record_version=$12
			AND status IN ('DRAFT','REOPENED') RETURNING id::text,owner_user_id::text,created_by::text,title,question,decision_text,expected_effect,risks_json,status,evidence_mode,
			approval_policy_id,required_approvals,review_at,terminal_reason,record_version,created_at,updated_at`, input.Title, input.Question, input.Decision, input.ExpectedEffect, risks, input.ReviewAt, now,
			identity.TenantID, identity.DomainID, id, identity.ActorID, input.ExpectedVersion)
		var err error
		result, err = scanDecision(row)
		return err
	})
	return result, mapConcurrent(err)
}

func (store *PostgresStore) Submit(ctx context.Context, identity Identity, id askdata.ID, expected int64, policy ApprovalPolicy, now time.Time) (Aggregate, error) {
	var result Aggregate
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := lockDecisionTx(ctx, tx, identity, id)
		if err != nil {
			return err
		}
		if current.OwnerUserID != identity.ActorID {
			return ErrForbidden
		}
		if current.RecordVersion != expected {
			return ErrConflict
		}
		if !statusTransitionAllowed(current.Status, StatusInReview) {
			return ErrIllegalTransition
		}
		inside, err := resolvePolicyTx(ctx, tx, identity, policy.ID)
		if err != nil {
			return err
		}
		if !samePolicy(inside, policy) {
			return ErrConflict
		}
		for index, approver := range inside.ApproverUserIDs {
			if approver == identity.ActorID {
				return ErrPolicyUnavailable
			}
			_, err = tx.Exec(ctx, `INSERT INTO decision.decision_approvals(id,tenant_id,domain_id,decision_id,approver_user_id,sequence_no,status,created_at)
			VALUES($1,$2,$3,$4,$5,$6,'PENDING',$7)`, uuid.NewString(), identity.TenantID, identity.DomainID, id, approver, index+1, now)
			if err != nil {
				return err
			}
			if err = insertNotificationTx(ctx, tx, identity, id, "", approver, "DECISION_APPROVAL", string(id)+":approval:"+string(approver), "待审批决策", now); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE decision.decisions SET status='IN_REVIEW',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, identity.TenantID, id); err != nil {
			return err
		}
		if err = appendDecisionEventTx(ctx, tx, identity, id, "SUBMITTED", string(current.Status), string(StatusInReview), "", expected+1, now); err != nil {
			return err
		}
		result, err = loadAggregateTx(ctx, tx, identity, id, true)
		return err
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) DecideApproval(ctx context.Context, identity Identity, id askdata.ID, expected int64, approve bool, comment string, now time.Time) (Aggregate, error) {
	var result Aggregate
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := lockDecisionTx(ctx, tx, identity, id)
		if err != nil {
			return err
		}
		if current.RecordVersion != expected {
			return ErrConflict
		}
		if current.Status != StatusInReview {
			return ErrIllegalTransition
		}
		status := "APPROVED"
		if !approve {
			status = "REJECTED"
		}
		tag, err := tx.Exec(ctx, `INSERT INTO decision.decision_approval_events(
			id,tenant_id,domain_id,decision_id,approval_id,status,comment,actor_user_id,created_at)
			SELECT $1,approval.tenant_id,approval.domain_id,approval.decision_id,approval.id,$2,$3,$4,$5
			FROM decision.decision_approvals approval
			WHERE approval.tenant_id=$6 AND approval.domain_id=$7 AND approval.decision_id=$8
			  AND approval.approver_user_id=$4 AND approval.status='PENDING'
			  AND NOT EXISTS(SELECT 1 FROM decision.decision_approval_events event
			    WHERE event.tenant_id=approval.tenant_id AND event.approval_id=approval.id)`,
			uuid.NewString(), status, comment, identity.ActorID, now, identity.TenantID, identity.DomainID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrForbidden
		}
		target := StatusInReview
		if !approve {
			target = StatusRejected
		} else {
			var count int
			if err = tx.QueryRow(ctx, `SELECT count(*) FROM decision.decision_approvals approval
				LEFT JOIN decision.decision_approval_events event
				  ON event.tenant_id=approval.tenant_id AND event.approval_id=approval.id
				WHERE approval.tenant_id=$1 AND approval.decision_id=$2
				  AND COALESCE(event.status,approval.status)='APPROVED'`, identity.TenantID, id).Scan(&count); err != nil {
				return err
			}
			if count >= current.RequiredApprovals {
				target = StatusApproved
			}
		}
		if target != StatusInReview {
			if _, err = tx.Exec(ctx, `UPDATE decision.decisions SET status=$1,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4`, target, now, identity.TenantID, id); err != nil {
				return err
			}
			if err = appendDecisionEventTx(ctx, tx, identity, id, "APPROVAL_DECIDED", string(current.Status), string(target), comment, expected+1, now); err != nil {
				return err
			}
		}
		recipient := any(identity.ActorID)
		if target != StatusInReview {
			recipient = nil
		}
		if _, err = tx.Exec(ctx, `UPDATE decision.decision_notifications SET resolved_at=$1
			WHERE tenant_id=$2 AND decision_id=$3 AND notification_type='DECISION_APPROVAL'
			  AND ($4::uuid IS NULL OR recipient_user_id=$4)`, now, identity.TenantID, id, recipient); err != nil {
			return err
		}
		result, err = loadAggregateTx(ctx, tx, identity, id, true)
		return err
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) CreateAction(ctx context.Context, identity Identity, id askdata.ID, input CreateActionInput, now time.Time) (Action, error) {
	actionID := askdata.ID(uuid.NewString())
	var result Action
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := lockDecisionTx(ctx, tx, identity, id)
		if err != nil {
			return err
		}
		if current.OwnerUserID != identity.ActorID {
			return ErrForbidden
		}
		if current.Status != StatusApproved && current.Status != StatusInExecution && current.Status != StatusReopened {
			return ErrIllegalTransition
		}
		var assigned bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.users u JOIN platform.domain_memberships m ON m.tenant_id=u.tenant_id AND m.user_id=u.id
			WHERE u.tenant_id=$1 AND u.id=$2 AND u.status='ACTIVE' AND u.deleted_at IS NULL AND m.domain_id=$3 AND m.status='ACTIVE')`, identity.TenantID, input.AssigneeUserID, identity.DomainID).Scan(&assigned); err != nil {
			return err
		}
		if !assigned {
			return ErrForbidden
		}
		refs, _ := json.Marshal(input.DeliverableRefs)
		err = tx.QueryRow(ctx, `INSERT INTO decision.action_items(id,tenant_id,domain_id,decision_id,title,description,assignee_user_id,due_at,status,deliverable_refs,created_by,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,'TODO',$9,$10,$11,$11) RETURNING id::text,decision_id::text,title,description,assignee_user_id::text,due_at,status,block_reason,completion_evidence,deliverable_refs,record_version,created_at,updated_at`,
			actionID, identity.TenantID, identity.DomainID, id, input.Title, input.Description, input.AssigneeUserID, input.DueAt, refs, identity.ActorID, now).Scan(&result.ID, &result.DecisionID, &result.Title, &result.Description, &result.AssigneeUserID, &result.DueAt, &result.Status, &result.BlockReason, &result.CompletionEvidence, &refs, &result.RecordVersion, &result.CreatedAt, &result.UpdatedAt)
		if err != nil {
			return err
		}
		_ = json.Unmarshal(refs, &result.DeliverableRefs)
		result.SchemaVersion = SchemaVersion
		if current.Status == StatusApproved {
			if _, err = tx.Exec(ctx, `UPDATE decision.decisions SET status='IN_EXECUTION',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, identity.TenantID, id); err != nil {
				return err
			}
			if err = appendDecisionEventTx(ctx, tx, identity, id, "EXECUTION_STARTED", string(current.Status), string(StatusInExecution), "", current.RecordVersion+1, now); err != nil {
				return err
			}
		}
		if err = appendActionEventTx(ctx, tx, identity, id, actionID, "CREATED", "", string(ActionTODO), "", now); err != nil {
			return err
		}
		return insertNotificationTx(ctx, tx, identity, id, actionID, input.AssigneeUserID, "ACTION_ASSIGNED", string(actionID)+":assigned", "新行动项待处理", now)
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) TransitionAction(ctx context.Context, identity Identity, decisionID, actionID askdata.ID, input TransitionActionInput, now time.Time) (Action, error) {
	var result Action
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		decisionValue, err := lockDecisionTx(ctx, tx, identity, decisionID)
		if err != nil {
			return err
		}
		if decisionValue.Status != StatusInExecution && decisionValue.Status != StatusReviewDue && decisionValue.Status != StatusReopened {
			return ErrIllegalTransition
		}
		var current Action
		var refs []byte
		err = tx.QueryRow(ctx, `SELECT id::text,decision_id::text,title,description,assignee_user_id::text,due_at,status,block_reason,completion_evidence,deliverable_refs,record_version,created_at,updated_at
			FROM decision.action_items WHERE tenant_id=$1 AND domain_id=$2 AND decision_id=$3 AND id=$4 FOR UPDATE`, identity.TenantID, identity.DomainID, decisionID, actionID).Scan(&current.ID, &current.DecisionID, &current.Title, &current.Description, &current.AssigneeUserID, &current.DueAt, &current.Status, &current.BlockReason, &current.CompletionEvidence, &refs, &current.RecordVersion, &current.CreatedAt, &current.UpdatedAt)
		if err != nil {
			return err
		}
		if current.AssigneeUserID != identity.ActorID && decisionValue.OwnerUserID != identity.ActorID {
			return ErrForbidden
		}
		if current.RecordVersion != input.ExpectedVersion {
			return ErrConflict
		}
		if !actionTransitionAllowed(current.Status, input.Target) {
			return ErrIllegalTransition
		}
		if (current.Status == ActionDone || current.Status == ActionCanceled) && input.Target == ActionDoing && !validText(input.Reason, 1, 4096) {
			return ErrInvalid
		}
		blockReason := ""
		completion := ""
		if input.Target == ActionBlocked {
			blockReason = input.Reason
		}
		if input.Target == ActionDone {
			completion = input.CompletionEvidence
		}
		err = tx.QueryRow(ctx, `UPDATE decision.action_items SET status=$1,block_reason=$2,completion_evidence=$3,record_version=record_version+1,updated_at=$4
			WHERE tenant_id=$5 AND id=$6 AND record_version=$7 RETURNING id::text,decision_id::text,title,description,assignee_user_id::text,due_at,status,block_reason,completion_evidence,deliverable_refs,record_version,created_at,updated_at`, input.Target, blockReason, completion, now, identity.TenantID, actionID, input.ExpectedVersion).
			Scan(&result.ID, &result.DecisionID, &result.Title, &result.Description, &result.AssigneeUserID, &result.DueAt, &result.Status, &result.BlockReason, &result.CompletionEvidence, &refs, &result.RecordVersion, &result.CreatedAt, &result.UpdatedAt)
		if err != nil {
			return err
		}
		_ = json.Unmarshal(refs, &result.DeliverableRefs)
		result.SchemaVersion = SchemaVersion
		if err = appendActionEventTx(ctx, tx, identity, decisionID, actionID, "TRANSITIONED", string(current.Status), string(input.Target), input.Reason, now); err != nil {
			return err
		}
		if input.Target == ActionDone || input.Target == ActionCanceled {
			if _, err = tx.Exec(ctx, `UPDATE decision.decision_notifications SET resolved_at=$1
				WHERE tenant_id=$2 AND action_id=$3 AND notification_type IN ('ACTION_ASSIGNED','ACTION_BLOCKED','ACTION_OVERDUE') AND resolved_at IS NULL`, now, identity.TenantID, actionID); err != nil {
				return err
			}
		}
		if current.Status == ActionBlocked && input.Target != ActionBlocked {
			if _, err = tx.Exec(ctx, `UPDATE decision.decision_notifications SET resolved_at=$1
				WHERE tenant_id=$2 AND action_id=$3 AND notification_type='ACTION_BLOCKED' AND resolved_at IS NULL`, now, identity.TenantID, actionID); err != nil {
				return err
			}
		}
		if (current.Status == ActionDone || current.Status == ActionCanceled) && input.Target == ActionDoing {
			if _, err = tx.Exec(ctx, `UPDATE decision.decision_notifications
				SET summary='行动项已重新打开',created_at=$1,read_at=NULL,resolved_at=NULL
				WHERE tenant_id=$2 AND action_id=$3 AND notification_type='ACTION_ASSIGNED'`, now, identity.TenantID, actionID); err != nil {
				return err
			}
		}
		if input.Target == ActionBlocked {
			return insertNotificationTx(ctx, tx, identity, decisionID, actionID, decisionValue.OwnerUserID, "ACTION_BLOCKED", string(actionID)+":blocked:"+strconv.FormatInt(result.RecordVersion, 10), "行动项已阻塞", now)
		}
		return nil
	})
	return result, mapConcurrent(err)
}

func (store *PostgresStore) AddOutcomeMetric(ctx context.Context, identity Identity, decisionID askdata.ID, input AddMetricInput, now time.Time) (OutcomeMetric, error) {
	metricID := askdata.ID(uuid.NewString())
	var result OutcomeMetric
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := lockDecisionTx(ctx, tx, identity, decisionID)
		if err != nil {
			return err
		}
		if current.OwnerUserID != identity.ActorID {
			return ErrForbidden
		}
		if current.Status != StatusApproved && current.Status != StatusInExecution && current.Status != StatusReopened {
			return ErrIllegalTransition
		}
		var releaseOK bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM askdata.releases WHERE tenant_id=$1 AND domain_id=$2 AND id=$3 AND content_hash=$4 AND status IN ('ACTIVE','SUPERSEDED','RETAINED'))`, identity.TenantID, identity.DomainID, input.SemanticReleaseID, input.SemanticReleaseHash).Scan(&releaseOK); err != nil {
			return err
		}
		if !releaseOK {
			return ErrInvalid
		}
		var target, targetUpper any
		if input.TargetValue != nil {
			target = *input.TargetValue
		}
		if input.TargetUpperValue != nil {
			targetUpper = *input.TargetUpperValue
		}
		var semanticRaw []byte
		err = tx.QueryRow(ctx, `INSERT INTO decision.outcome_metrics(id,tenant_id,domain_id,decision_id,metric_version_id,semantic_ir_json,semantic_ir_hash,semantic_release_id,semantic_release_hash,
			baseline_value,target_direction,target_value,target_upper_value,review_at,attribution_note,created_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::numeric,$11,NULLIF($12::text,'')::numeric,NULLIF($13::text,'')::numeric,$14,$15,$16,$16)
			RETURNING id::text,decision_id::text,metric_version_id,semantic_ir_json,semantic_ir_hash,semantic_release_id::text,semantic_release_hash,baseline_value::text,target_direction,target_value::text,target_upper_value::text,review_at,attribution_note,
			current_value::text,COALESCE(current_result_hash,''),COALESCE(current_policy_scope_hash,''),current_as_of,drifted,refresh_status,record_version`, metricID, identity.TenantID, identity.DomainID, decisionID, input.MetricVersionID, input.SemanticIR, input.SemanticIRHash,
			input.SemanticReleaseID, input.SemanticReleaseHash, input.BaselineValue, input.TargetDirection, nullableString(target), nullableString(targetUpper), input.ReviewAt, input.AttributionNote, now).Scan(&result.ID, &result.DecisionID, &result.MetricVersionID, &semanticRaw, &result.SemanticIRHash, &result.SemanticReleaseID, &result.SemanticReleaseHash,
			&result.BaselineValue, &result.TargetDirection, &result.TargetValue, &result.TargetUpperValue, &result.ReviewAt, &result.AttributionNote, &result.CurrentValue, &result.CurrentResultHash, &result.CurrentPolicyScopeHash, &result.CurrentAsOf, &result.Drifted, &result.RefreshStatus, &result.RecordVersion)
		result.SemanticIR = semanticRaw
		return err
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) SaveOutcomeRefresh(ctx context.Context, identity Identity, decisionID, metricID askdata.ID, refresh OutcomeRefresh, now time.Time) (OutcomeMetric, error) {
	var result OutcomeMetric
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `UPDATE decision.outcome_metrics SET current_value=NULLIF($1,'')::numeric,current_result_hash=NULLIF($2,''),current_policy_scope_hash=NULLIF($3,''),current_as_of=$4,
			drifted=$5,refresh_status=$6,record_version=record_version+1,updated_at=$7 WHERE tenant_id=$8 AND domain_id=$9 AND decision_id=$10 AND id=$11
			RETURNING id::text,decision_id::text,metric_version_id,semantic_ir_json,semantic_ir_hash,semantic_release_id::text,semantic_release_hash,baseline_value::text,target_direction,target_value::text,target_upper_value::text,review_at,attribution_note,
			current_value::text,COALESCE(current_result_hash,''),COALESCE(current_policy_scope_hash,''),current_as_of,drifted,refresh_status,record_version`, refresh.Value, refresh.ResultHash, refresh.PolicyScopeHash, nullableTime(refresh.AsOf), refresh.Drifted, refresh.Status, now,
			identity.TenantID, identity.DomainID, decisionID, metricID).Scan(&result.ID, &result.DecisionID, &result.MetricVersionID, &raw, &result.SemanticIRHash, &result.SemanticReleaseID, &result.SemanticReleaseHash, &result.BaselineValue,
			&result.TargetDirection, &result.TargetValue, &result.TargetUpperValue, &result.ReviewAt, &result.AttributionNote, &result.CurrentValue, &result.CurrentResultHash, &result.CurrentPolicyScopeHash, &result.CurrentAsOf, &result.Drifted, &result.RefreshStatus, &result.RecordVersion)
		result.SemanticIR = raw
		return err
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) ConfirmOutcome(ctx context.Context, identity Identity, decisionID askdata.ID, input ConfirmOutcomeInput, now time.Time) (OutcomeReview, error) {
	var result OutcomeReview
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := lockDecisionTx(ctx, tx, identity, decisionID)
		if err != nil {
			return err
		}
		if current.OwnerUserID != identity.ActorID {
			return ErrForbidden
		}
		if current.Status != StatusReviewDue && current.Status != StatusInExecution && current.Status != StatusReopened {
			return ErrIllegalTransition
		}
		var ready bool
		if err = tx.QueryRow(ctx, `SELECT count(*)>0 AND bool_and(refresh_status IN ('SUCCEEDED','NO_DATA')) FROM decision.outcome_metrics WHERE tenant_id=$1 AND decision_id=$2`, identity.TenantID, decisionID).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			return ErrOutcomeBlocked
		}
		status := ReviewConfirmed
		if input.Conclusion == ConclusionInconclusive {
			status = ReviewInconclusive
		}
		err = tx.QueryRow(ctx, `INSERT INTO decision.outcome_reviews(id,tenant_id,domain_id,decision_id,status,conclusion,notes,generated_at,confirmed_by,confirmed_at,record_version,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$8,1,$8)
			ON CONFLICT(tenant_id,decision_id) DO UPDATE SET status=EXCLUDED.status,conclusion=EXCLUDED.conclusion,notes=EXCLUDED.notes,confirmed_by=EXCLUDED.confirmed_by,confirmed_at=EXCLUDED.confirmed_at,
				record_version=decision.outcome_reviews.record_version+1 WHERE decision.outcome_reviews.record_version=$10
			RETURNING id::text,decision_id::text,status,conclusion,notes,generated_at,confirmed_by::text,confirmed_at,record_version`, uuid.NewString(), identity.TenantID, identity.DomainID, decisionID, status, input.Conclusion, input.Notes, now, identity.ActorID, input.ExpectedVersion).
			Scan(&result.ID, &result.DecisionID, &result.Status, &result.Conclusion, &result.Notes, &result.GeneratedAt, &result.ConfirmedBy, &result.ConfirmedAt, &result.RecordVersion)
		result.SchemaVersion = SchemaVersion
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE decision.decision_notifications SET resolved_at=$1
			WHERE tenant_id=$2 AND decision_id=$3 AND notification_type IN ('DECISION_REVIEW_DUE','OUTCOME_REVIEW_DUE') AND resolved_at IS NULL`, now, identity.TenantID, decisionID)
		return err
	})
	return result, mapConcurrent(err)
}

func (store *PostgresStore) TransitionDecision(ctx context.Context, identity Identity, id askdata.ID, expected int64, target Status, reason string, now time.Time) (Aggregate, error) {
	var result Aggregate
	err := database.WithTenantTx(ctx, store.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		current, err := lockDecisionTx(ctx, tx, identity, id)
		if err != nil {
			return err
		}
		if current.OwnerUserID != identity.ActorID {
			return ErrForbidden
		}
		if current.RecordVersion != expected {
			return ErrConflict
		}
		if !statusTransitionAllowed(current.Status, target) {
			return ErrIllegalTransition
		}
		_, err = tx.Exec(ctx, `UPDATE decision.decisions SET status=$1,terminal_reason=$2,record_version=record_version+1,updated_at=$3 WHERE tenant_id=$4 AND id=$5`, target, reason, now, identity.TenantID, id)
		if err != nil {
			return err
		}
		if err = appendDecisionEventTx(ctx, tx, identity, id, "TRANSITIONED", string(current.Status), string(target), reason, expected+1, now); err != nil {
			return err
		}
		if target == StatusClosed || target == StatusCanceled {
			if _, err = tx.Exec(ctx, `UPDATE decision.decision_notifications SET resolved_at=$1
				WHERE tenant_id=$2 AND decision_id=$3 AND resolved_at IS NULL`, now, identity.TenantID, id); err != nil {
				return err
			}
		}
		result, err = loadAggregateTx(ctx, tx, identity, id, true)
		return err
	})
	return result, mapDBError(err)
}

func (store *PostgresStore) MarkReviewDue(ctx context.Context, now time.Time, limit int) (int, error) {
	return store.processTenants(ctx, func(tx pgx.Tx, tenantID string) (int, error) {
		rows, err := tx.Query(ctx, `SELECT id::text,domain_id::text,owner_user_id::text,record_version,status FROM decision.decisions
		WHERE tenant_id=$1 AND status IN ('IN_EXECUTION','REOPENED') AND review_at<=$2 ORDER BY review_at,id LIMIT $3 FOR UPDATE SKIP LOCKED`, tenantID, now, limit)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		type due struct {
			id, domain, owner askdata.ID
			version           int64
			status            Status
		}
		var values []due
		for rows.Next() {
			var value due
			if err := rows.Scan(&value.id, &value.domain, &value.owner, &value.version, &value.status); err != nil {
				return 0, err
			}
			values = append(values, value)
		}
		for _, value := range values {
			if _, err := tx.Exec(ctx, `UPDATE decision.decisions SET status='REVIEW_DUE',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenantID, value.id); err != nil {
				return 0, err
			}
			identity := Identity{TenantID: askdata.ID(tenantID), DomainID: value.domain, ActorID: value.owner}
			if err := appendDecisionEventTx(ctx, tx, identity, value.id, "REVIEW_DUE", string(value.status), string(StatusReviewDue), "", value.version+1, now); err != nil {
				return 0, err
			}
			if err := insertNotificationTx(ctx, tx, identity, value.id, "", value.owner, "DECISION_REVIEW_DUE", string(value.id)+":review-due", "决策结果待复盘", now); err != nil {
				return 0, err
			}
		}
		return len(values), nil
	})
}

func (store *PostgresStore) EscalateActions(ctx context.Context, now time.Time, limit int) (int, error) {
	return store.processTenants(ctx, func(tx pgx.Tx, tenantID string) (int, error) {
		rows, err := tx.Query(ctx, `SELECT action.id::text,action.decision_id::text,action.domain_id::text,decision.owner_user_id::text
		FROM decision.action_items action JOIN decision.decisions decision ON decision.id=action.decision_id AND decision.tenant_id=action.tenant_id
		WHERE action.tenant_id=$1 AND action.status NOT IN ('DONE','CANCELED') AND action.due_at<$2 ORDER BY action.due_at,action.id LIMIT $3 FOR UPDATE OF action SKIP LOCKED`, tenantID, now, limit)
		if err != nil {
			return 0, err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var actionID, decisionID, domainID, ownerID askdata.ID
			if err := rows.Scan(&actionID, &decisionID, &domainID, &ownerID); err != nil {
				return 0, err
			}
			identity := Identity{TenantID: askdata.ID(tenantID), DomainID: domainID, ActorID: ownerID}
			if err := insertNotificationTx(ctx, tx, identity, decisionID, actionID, ownerID, "ACTION_OVERDUE", string(actionID)+":overdue:"+now.Format("2006-01-02"), "行动项已超期", now); err != nil {
				return 0, err
			}
			count++
		}
		return count, rows.Err()
	})
}

func (store *PostgresStore) processTenants(ctx context.Context, fn func(pgx.Tx, string) (int, error)) (int, error) {
	if store == nil || store.pool == nil {
		return 0, ErrInvalid
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text FROM decision.list_work_tenants()`)
	if err != nil {
		return 0, err
	}
	var tenants []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		tenants = append(tenants, id)
	}
	rows.Close()
	total := 0
	for _, tenant := range tenants {
		err = database.WithTenantTx(ctx, store.pool, tenant, func(tx pgx.Tx) error { count, runErr := fn(tx, tenant); total += count; return runErr })
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func lockDecisionTx(ctx context.Context, tx pgx.Tx, identity Identity, id askdata.ID) (Decision, error) {
	row := tx.QueryRow(ctx, `SELECT id::text,owner_user_id::text,created_by::text,title,question,decision_text,expected_effect,risks_json,status,evidence_mode,
	approval_policy_id,required_approvals,review_at,terminal_reason,record_version,created_at,updated_at FROM decision.decisions WHERE tenant_id=$1 AND domain_id=$2 AND id=$3 FOR UPDATE`, identity.TenantID, identity.DomainID, id)
	return scanDecision(row)
}

type rowScanner interface{ Scan(...any) error }

func scanDecision(row rowScanner) (Decision, error) {
	var value Decision
	var risks []byte
	err := row.Scan(&value.ID, &value.OwnerUserID, &value.CreatedBy, &value.Title, &value.Question, &value.Decision, &value.ExpectedEffect, &risks, &value.Status, &value.EvidenceMode, &value.ApprovalPolicyID,
		&value.RequiredApprovals, &value.ReviewAt, &value.TerminalReason, &value.RecordVersion, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return Decision{}, err
	}
	value.SchemaVersion = SchemaVersion
	_ = json.Unmarshal(risks, &value.Risks)
	if value.Risks == nil {
		value.Risks = []string{}
	}
	return value, nil
}

func loadAggregateTx(ctx context.Context, tx pgx.Tx, identity Identity, id askdata.ID, events bool) (Aggregate, error) {
	value, err := scanDecision(tx.QueryRow(ctx, `SELECT id::text,owner_user_id::text,created_by::text,title,question,decision_text,expected_effect,risks_json,status,evidence_mode,
	approval_policy_id,required_approvals,review_at,terminal_reason,record_version,created_at,updated_at FROM decision.decisions WHERE tenant_id=$1 AND domain_id=$2 AND id=$3`, identity.TenantID, identity.DomainID, id))
	if err != nil {
		return Aggregate{}, err
	}
	result := Aggregate{Decision: value, Options: []Option{}, Evidence: []Evidence{}, Approvals: []Approval{}, Actions: []Action{}, Metrics: []OutcomeMetric{}}
	rows, err := tx.Query(ctx, `SELECT id::text,title,description,selected FROM decision.decision_options WHERE tenant_id=$1 AND decision_id=$2 ORDER BY created_at,id`, identity.TenantID, id)
	if err != nil {
		return Aggregate{}, err
	}
	for rows.Next() {
		var item Option
		if err := rows.Scan(&item.ID, &item.Title, &item.Description, &item.Selected); err != nil {
			rows.Close()
			return Aggregate{}, err
		}
		result.Options = append(result.Options, item)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT id::text,source_type,source_id::text,source_hash,semantic_release_id::text,semantic_release_hash,COALESCE(data_snapshot_id::text,''),as_of,policy_scope_hash,summary,verified,created_at
		FROM decision.decision_evidence WHERE tenant_id=$1 AND decision_id=$2 ORDER BY created_at,id`, identity.TenantID, id)
	if err != nil {
		return Aggregate{}, err
	}
	for rows.Next() {
		item := Evidence{SchemaVersion: SchemaVersion}
		if err := rows.Scan(&item.ID, &item.SourceType, &item.SourceID, &item.SourceHash, &item.SemanticReleaseID, &item.SemanticReleaseHash, &item.DataSnapshotID, &item.AsOf, &item.PolicyScopeHash, &item.Summary, &item.Verified, &item.CreatedAt); err != nil {
			rows.Close()
			return Aggregate{}, err
		}
		result.Evidence = append(result.Evidence, item)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT approval.id::text,approval.approver_user_id::text,approval.sequence_no,
		COALESCE(event.status,approval.status),COALESCE(event.comment,approval.comment),COALESCE(event.created_at,approval.decided_at)
		FROM decision.decision_approvals approval
		LEFT JOIN decision.decision_approval_events event
		  ON event.tenant_id=approval.tenant_id AND event.approval_id=approval.id
		WHERE approval.tenant_id=$1 AND approval.decision_id=$2 ORDER BY approval.sequence_no`, identity.TenantID, id)
	if err != nil {
		return Aggregate{}, err
	}
	for rows.Next() {
		var item Approval
		if err := rows.Scan(&item.ID, &item.ApproverUserID, &item.SequenceNo, &item.Status, &item.Comment, &item.DecidedAt); err != nil {
			rows.Close()
			return Aggregate{}, err
		}
		result.Approvals = append(result.Approvals, item)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT id::text,decision_id::text,title,description,assignee_user_id::text,due_at,status,block_reason,completion_evidence,deliverable_refs,record_version,created_at,updated_at FROM decision.action_items WHERE tenant_id=$1 AND decision_id=$2 ORDER BY created_at,id`, identity.TenantID, id)
	if err != nil {
		return Aggregate{}, err
	}
	for rows.Next() {
		item := Action{SchemaVersion: SchemaVersion}
		var refs []byte
		if err := rows.Scan(&item.ID, &item.DecisionID, &item.Title, &item.Description, &item.AssigneeUserID, &item.DueAt, &item.Status, &item.BlockReason, &item.CompletionEvidence, &refs, &item.RecordVersion, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			return Aggregate{}, err
		}
		_ = json.Unmarshal(refs, &item.DeliverableRefs)
		result.Actions = append(result.Actions, item)
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT id::text,decision_id::text,metric_version_id,semantic_ir_json,semantic_ir_hash,semantic_release_id::text,semantic_release_hash,baseline_value::text,target_direction,target_value::text,target_upper_value::text,review_at,attribution_note,
		current_value::text,COALESCE(current_result_hash,''),COALESCE(current_policy_scope_hash,''),current_as_of,drifted,refresh_status,record_version FROM decision.outcome_metrics WHERE tenant_id=$1 AND decision_id=$2 ORDER BY created_at,id`, identity.TenantID, id)
	if err != nil {
		return Aggregate{}, err
	}
	for rows.Next() {
		var item OutcomeMetric
		var raw []byte
		if err := rows.Scan(&item.ID, &item.DecisionID, &item.MetricVersionID, &raw, &item.SemanticIRHash, &item.SemanticReleaseID, &item.SemanticReleaseHash, &item.BaselineValue, &item.TargetDirection, &item.TargetValue, &item.TargetUpperValue, &item.ReviewAt, &item.AttributionNote, &item.CurrentValue, &item.CurrentResultHash, &item.CurrentPolicyScopeHash, &item.CurrentAsOf, &item.Drifted, &item.RefreshStatus, &item.RecordVersion); err != nil {
			rows.Close()
			return Aggregate{}, err
		}
		item.SemanticIR = raw
		result.Metrics = append(result.Metrics, item)
	}
	rows.Close()
	var review OutcomeReview
	err = tx.QueryRow(ctx, `SELECT id::text,decision_id::text,status,COALESCE(conclusion,''),notes,generated_at,COALESCE(confirmed_by::text,''),confirmed_at,record_version FROM decision.outcome_reviews WHERE tenant_id=$1 AND decision_id=$2`, identity.TenantID, id).
		Scan(&review.ID, &review.DecisionID, &review.Status, &review.Conclusion, &review.Notes, &review.GeneratedAt, &review.ConfirmedBy, &review.ConfirmedAt, &review.RecordVersion)
	if err == nil {
		review.SchemaVersion = SchemaVersion
		result.Review = &review
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Aggregate{}, err
	}
	if events {
		rows, err = tx.Query(ctx, `SELECT id::text,event_no,event_type,actor_user_id::text,from_status,to_status,reason,payload_json,record_version,created_at FROM decision.decision_events WHERE tenant_id=$1 AND decision_id=$2 ORDER BY event_no`, identity.TenantID, id)
		if err != nil {
			return Aggregate{}, err
		}
		for rows.Next() {
			var item Event
			var raw []byte
			if err := rows.Scan(&item.ID, &item.EventNo, &item.EventType, &item.ActorUserID, &item.FromStatus, &item.ToStatus, &item.Reason, &raw, &item.RecordVersion, &item.CreatedAt); err != nil {
				rows.Close()
				return Aggregate{}, err
			}
			_ = json.Unmarshal(raw, &item.Payload)
			result.Events = append(result.Events, item)
		}
		rows.Close()
	}
	return result, nil
}

func appendDecisionEventTx(ctx context.Context, tx pgx.Tx, identity Identity, id askdata.ID, eventType, from, to, reason string, version int64, now time.Time) error {
	var no int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(event_no),0)+1 FROM decision.decision_events WHERE tenant_id=$1 AND decision_id=$2`, identity.TenantID, id).Scan(&no); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO decision.decision_events(id,tenant_id,domain_id,decision_id,event_no,event_type,actor_user_id,from_status,to_status,reason,payload_json,record_version,created_at)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'{}',$11,$12)`, uuid.NewString(), identity.TenantID, identity.DomainID, id, no, eventType, identity.ActorID, from, to, reason, version, now)
	return err
}
func appendActionEventTx(ctx context.Context, tx pgx.Tx, identity Identity, decisionID, actionID askdata.ID, eventType, from, to, reason string, now time.Time) error {
	var no int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(event_no),0)+1 FROM decision.action_events WHERE tenant_id=$1 AND action_id=$2`, identity.TenantID, actionID).Scan(&no); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO decision.action_events(id,tenant_id,domain_id,decision_id,action_id,event_no,event_type,from_status,to_status,actor_user_id,reason,payload_json,created_at)
	VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'{}',$12)`, uuid.NewString(), identity.TenantID, identity.DomainID, decisionID, actionID, no, eventType, from, to, identity.ActorID, reason, now)
	return err
}
func insertNotificationTx(ctx context.Context, tx pgx.Tx, identity Identity, decisionID, actionID, recipient askdata.ID, kind, key, summary string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO decision.decision_notifications(id,tenant_id,domain_id,decision_id,action_id,recipient_user_id,notification_type,dedup_key,summary,created_at)
	VALUES($1,$2,$3,$4,NULLIF($5::text,'')::uuid,$6,$7,$8,$9,$10) ON CONFLICT(tenant_id,recipient_user_id,dedup_key) DO NOTHING`, uuid.NewString(), identity.TenantID, identity.DomainID, decisionID, actionID, recipient, kind, key, summary, now)
	return err
}

func samePolicy(left, right ApprovalPolicy) bool {
	if left.ID != right.ID || left.RequiredApprovals != right.RequiredApprovals || len(left.ApproverUserIDs) != len(right.ApproverUserIDs) {
		return false
	}
	for i := range left.ApproverUserIDs {
		if left.ApproverUserIDs[i] != right.ApproverUserIDs[i] {
			return false
		}
	}
	return true
}
func nullableString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}
func mapConcurrent(err error) error {
	mapped := mapDBError(err)
	if errors.Is(mapped, ErrNotFound) {
		return ErrConflict
	}
	return mapped
}
func mapDBError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505", "40001":
			return ErrConflict
		case "23503":
			return ErrForbidden
		case "23514", "22P02":
			return ErrInvalid
		}
	}
	return err
}
