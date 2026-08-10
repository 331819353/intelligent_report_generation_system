// Package userlifecycle coordinates cross-context owner transfer before a
// user is disabled. Adapters are the mandatory SPI for every bounded context;
// source actor/audit columns are never rewritten.
package userlifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

var (
	ErrInvalid   = errors.New("user lifecycle request is invalid")
	ErrForbidden = errors.New("platform administrator is required")
	ErrBlocked   = errors.New("user has responsibilities that must be resolved in the source module")
	ErrConflict  = errors.New("user responsibilities changed concurrently")
	ErrNotFound  = errors.New("user lifecycle batch was not found")
)

type AdminAuthorizer interface {
	IsPlatformAdministrator(context.Context, string, string) (bool, error)
}
type Disposition string

const (
	Transfer  Disposition = "TRANSFER"
	AutoClose Disposition = "AUTO_CLOSE"
	ReadOnly  Disposition = "READ_ONLY"
	Block     Disposition = "BLOCK"
)

type Item struct {
	Category       string      `json:"category"`
	DomainID       string      `json:"domainId"`
	ObjectID       string      `json:"objectId"`
	Disposition    Disposition `json:"disposition"`
	ReceiverUserID string      `json:"receiverUserId,omitempty"`
	SourceVersion  string      `json:"sourceVersion"`
	ExecutedAt     *time.Time  `json:"executedAt,omitempty"`
}
type Mapping struct {
	Category       string `json:"category"`
	DomainID       string `json:"domainId"`
	ReceiverUserID string `json:"receiverUserId"`
}
type Preview struct {
	TargetUserID string         `json:"targetUserId"`
	Items        []Item         `json:"items"`
	Counts       map[string]int `json:"counts"`
	CanDisable   bool           `json:"canDisable"`
}
type Batch struct {
	ID            string     `json:"id"`
	TargetUserID  string     `json:"targetUserId"`
	Status        string     `json:"status"`
	PlanHash      string     `json:"planHash"`
	FailureCode   string     `json:"failureCode,omitempty"`
	RecordVersion int64      `json:"recordVersion"`
	Items         []Item     `json:"items"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	CompletedAt   *time.Time `json:"completedAt,omitempty"`
}

type OwnerTransferAdapter interface {
	Category() string
	Preview(context.Context, pgx.Tx, string, string) ([]Item, error)
	Apply(context.Context, pgx.Tx, string, string, Item, time.Time) error
}
type SQLAdapter struct {
	category            string
	disposition         Disposition
	selectSQL, applySQL string
}

func (a SQLAdapter) Category() string { return a.category }
func (a SQLAdapter) Preview(ctx context.Context, tx pgx.Tx, tenantID, userID string) ([]Item, error) {
	rows, e := tx.Query(ctx, a.selectSQL, tenantID, userID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		item := Item{Category: a.category, Disposition: a.disposition}
		if e = rows.Scan(&item.DomainID, &item.ObjectID, &item.SourceVersion); e != nil {
			return nil, e
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (a SQLAdapter) Apply(ctx context.Context, tx pgx.Tx, tenantID, userID string, item Item, now time.Time) error {
	if a.disposition == ReadOnly {
		return nil
	}
	if a.disposition == Block {
		return ErrBlocked
	}
	tag, e := tx.Exec(ctx, a.applySQL, item.ReceiverUserID, now, tenantID, item.DomainID, item.ObjectID, userID, item.SourceVersion)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

type Service struct {
	pool     *pgxpool.Pool
	admins   AdminAuthorizer
	adapters map[string]OwnerTransferAdapter
	ordered  []OwnerTransferAdapter
	now      func() time.Time
}

func NewService(pool *pgxpool.Pool, admins AdminAuthorizer, configured ...OwnerTransferAdapter) (*Service, error) {
	if pool == nil || admins == nil {
		return nil, errors.New("user lifecycle dependencies are incomplete")
	}
	adapters := configured
	if len(adapters) == 0 {
		adapters = DefaultPostgresAdapters()
	}
	result := &Service{pool: pool, admins: admins, adapters: map[string]OwnerTransferAdapter{}, now: time.Now}
	for _, adapter := range adapters {
		if adapter == nil || adapter.Category() == "" {
			return nil, ErrInvalid
		}
		if _, exists := result.adapters[adapter.Category()]; exists {
			return nil, ErrInvalid
		}
		result.adapters[adapter.Category()] = adapter
		result.ordered = append(result.ordered, adapter)
	}
	sort.Slice(result.ordered, func(i, j int) bool { return result.ordered[i].Category() < result.ordered[j].Category() })
	return result, nil
}

func DefaultPostgresAdapters() []OwnerTransferAdapter {
	adapters := []OwnerTransferAdapter{
		SQLAdapter{"DATA_SOURCE", Transfer, `SELECT domain_id::text,id::text,version::text FROM platform.data_sources WHERE tenant_id=$1 AND owner_user_id=$2 AND status<>'DELETED' ORDER BY domain_id,id`, `UPDATE platform.data_sources SET owner_user_id=$1,version=version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND version::text=$7`},
		SQLAdapter{"DATASET", Transfer, `SELECT domain_id::text,id::text,version::text FROM platform.datasets WHERE tenant_id=$1 AND owner_user_id=$2 AND deleted_at IS NULL ORDER BY domain_id,id`, `UPDATE platform.datasets SET owner_user_id=$1,version=version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND version::text=$7`},
		SQLAdapter{"SEMANTIC_DOMAIN", Transfer, `SELECT id::text,id::text,version::text FROM askdata.domains WHERE tenant_id=$1 AND owner_id=$2 AND status<>'DEPRECATED' ORDER BY id`, `UPDATE askdata.domains SET owner_id=$1,version=version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND id=$5 AND owner_id=$6 AND version::text=$7`},
		SQLAdapter{"SAVED_QUESTION", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.saved_questions WHERE tenant_id=$1 AND owner_user_id=$2 AND status='ACTIVE' ORDER BY domain_id,id`, `UPDATE askdata.saved_questions SET owner_user_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"REPORT", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM platform.reports WHERE tenant_id=$1 AND owner_user_id=$2 ORDER BY domain_id,id`, `UPDATE platform.reports SET owner_user_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"REPORT_SCHEDULE", Transfer, `SELECT domain_id::text,id::text,record_version::text FROM platform.report_schedules WHERE tenant_id=$1 AND owner_user_id=$2 AND state<>'DISABLED' ORDER BY domain_id,id`, `UPDATE platform.report_schedules SET owner_user_id=$1,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND record_version::text=$7`},
		SQLAdapter{"FEEDBACK_TICKET", Transfer, `SELECT domain_id::text,id::text,record_version::text FROM askdata.feedback_tickets WHERE tenant_id=$1 AND owner_user_id=$2 AND status NOT IN('REJECTED','CLOSED') ORDER BY domain_id,id`, `UPDATE askdata.feedback_tickets SET owner_user_id=$1,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND record_version::text=$7`},
		SQLAdapter{"DATA_REQUEST_ASSIGNMENT", Transfer, `SELECT domain_id::text,id::text,record_version::text FROM platform.data_requests WHERE tenant_id=$1 AND assignee_user_id=$2 AND state IN('IN_PROGRESS','DELIVERED') ORDER BY domain_id,id`, `UPDATE platform.data_requests SET assignee_user_id=$1,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND assignee_user_id=$6 AND record_version::text=$7`},
		SQLAdapter{"DECISION", Transfer, `SELECT domain_id::text,id::text,record_version::text FROM decision.decisions WHERE tenant_id=$1 AND owner_user_id=$2 AND status NOT IN('CLOSED','CANCELED') ORDER BY domain_id,id`, `UPDATE decision.decisions SET owner_user_id=$1,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND record_version::text=$7`},
		SQLAdapter{"DECISION_ACTION", Transfer, `SELECT domain_id::text,id::text,record_version::text FROM decision.action_items WHERE tenant_id=$1 AND assignee_user_id=$2 AND status NOT IN('DONE','CANCELED') ORDER BY domain_id,id`, `UPDATE decision.action_items SET assignee_user_id=$1,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND assignee_user_id=$6 AND record_version::text=$7`},
		SQLAdapter{"KPI_BUNDLE", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.kpi_bundles WHERE tenant_id=$1 AND owner_user_id=$2 ORDER BY domain_id,id`, `UPDATE askdata.kpi_bundles SET owner_user_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"TIME_CONTRACT", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.time_contracts WHERE tenant_id=$1 AND owner_user_id=$2 ORDER BY domain_id,id`, `UPDATE askdata.time_contracts SET owner_user_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_user_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"SEMANTIC_METRIC", Transfer, `SELECT domain_id::text,id::text,version::text FROM askdata.metrics WHERE tenant_id=$1 AND owner_id=$2 ORDER BY domain_id,id`, `UPDATE askdata.metrics SET owner_id=$1,version=version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_id=$6 AND version::text=$7`},
		SQLAdapter{"SEMANTIC_DIMENSION", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.dimensions WHERE tenant_id=$1 AND owner_id=$2 ORDER BY domain_id,id`, `UPDATE askdata.dimensions SET owner_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"SEMANTIC_RELATIONSHIP", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.relationships WHERE tenant_id=$1 AND owner_id=$2 ORDER BY domain_id,id`, `UPDATE askdata.relationships SET owner_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"BUSINESS_TERM_VERSION", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.business_term_versions WHERE tenant_id=$1 AND owner_id=$2 ORDER BY domain_id,id`, `UPDATE askdata.business_term_versions SET owner_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"CERTIFIED_EXAMPLE_VERSION", Transfer, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.certified_example_versions WHERE tenant_id=$1 AND owner_id=$2 ORDER BY domain_id,id`, `UPDATE askdata.certified_example_versions SET owner_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"RELEASE_REFERENCE", Transfer, `SELECT release.domain_id::text,reference.id::text,reference.owner_id::text FROM askdata.release_references reference JOIN askdata.releases release ON release.tenant_id=reference.tenant_id AND release.id=reference.release_id WHERE reference.tenant_id=$1 AND reference.owner_id=$2 AND reference.released_at IS NULL ORDER BY release.domain_id,reference.id`, `UPDATE askdata.release_references reference SET owner_id=$1 FROM askdata.releases release WHERE reference.tenant_id=$3 AND reference.id=$5 AND reference.owner_id=$6 AND reference.owner_id::text=$7 AND release.tenant_id=reference.tenant_id AND release.id=reference.release_id AND release.domain_id=$4 AND $2::timestamptz IS NOT NULL`},
		SQLAdapter{"REPORT_SUBSCRIPTION", AutoClose, `SELECT domain_id::text,id::text,record_version::text FROM platform.report_subscriptions WHERE tenant_id=$1 AND recipient_user_id=$2 AND state='ACTIVE' ORDER BY domain_id,id`, `UPDATE platform.report_subscriptions SET state='REVOKED',record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND recipient_user_id=$6 AND record_version::text=$7`},
		SQLAdapter{"REPORT_DELIVERY", AutoClose, `SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM platform.report_deliveries WHERE tenant_id=$1 AND recipient_user_id=$2 AND state IN('PENDING','RUNNING','FAILED') ORDER BY domain_id,id`, `UPDATE platform.report_deliveries SET state='SKIPPED',report_link='',failure_code='RECIPIENT_DISABLED',access_checked_at=$2,lease_token=NULL,lease_expires_at=NULL,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND recipient_user_id=$6 AND extract(epoch from updated_at)::text=$7`},
		SQLAdapter{"CONVERSATION_HISTORY", ReadOnly, `SELECT domain_id::text,id::text,record_version::text FROM askdata.conversations WHERE tenant_id=$1 AND actor_id=$2 ORDER BY domain_id,id`, `SELECT 1 WHERE false`},
		SQLAdapter{"DATA_REQUEST_APPROVAL", Block, `SELECT domain_id::text,id::text,record_version::text FROM platform.data_requests WHERE tenant_id=$1 AND $2=ANY(approver_user_ids) AND state='SUBMITTED' ORDER BY domain_id,id`, `SELECT 1 WHERE false`},
		SQLAdapter{"DECISION_APPROVAL", Block, `SELECT approval.domain_id::text,approval.id::text,extract(epoch from approval.created_at)::text FROM decision.decision_approvals approval WHERE approval.tenant_id=$1 AND approval.approver_user_id=$2 AND approval.status='PENDING' AND NOT EXISTS(SELECT 1 FROM decision.decision_approval_events event WHERE event.tenant_id=approval.tenant_id AND event.approval_id=approval.id) ORDER BY approval.domain_id,approval.id`, `SELECT 1 WHERE false`},
		SQLAdapter{"RUNTIME_CONFIG_DRAFT", Block, `SELECT CASE WHEN scope_type='DOMAIN' THEN scope_id ELSE '' END,id::text,record_version::text FROM platform.runtime_config_versions WHERE tenant_id=$1 AND created_by=$2 AND state='DRAFT' ORDER BY scope_type,scope_id,id`, `SELECT 1 WHERE false`},
		SQLAdapter{"DOMAIN_ADMIN", Block, `SELECT membership.domain_id::text,membership.domain_id::text,extract(epoch from membership.updated_at)::text FROM platform.domain_memberships membership WHERE membership.tenant_id=$1 AND membership.user_id=$2 AND membership.status='ACTIVE' AND membership.member_role='DOMAIN_ADMIN' AND NOT EXISTS(SELECT 1 FROM platform.domain_memberships other JOIN platform.users other_user ON other_user.tenant_id=other.tenant_id AND other_user.id=other.user_id WHERE other.tenant_id=membership.tenant_id AND other.domain_id=membership.domain_id AND other.user_id<>membership.user_id AND other.status='ACTIVE' AND other.member_role='DOMAIN_ADMIN' AND other_user.status='ACTIVE' AND other_user.deleted_at IS NULL) ORDER BY membership.domain_id`, `SELECT 1 WHERE false`},
		SQLAdapter{"PLATFORM_ADMIN", Block, `SELECT ''::text,role.id::text,extract(epoch from assignment.assigned_at)::text FROM platform.user_roles assignment JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id WHERE assignment.tenant_id=$1 AND assignment.user_id=$2 AND role.code::text='platform_admin' AND role.status='ACTIVE' AND role.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM platform.user_roles other_assignment JOIN platform.roles other_role ON other_role.tenant_id=other_assignment.tenant_id AND other_role.id=other_assignment.role_id JOIN platform.users other_user ON other_user.tenant_id=other_assignment.tenant_id AND other_user.id=other_assignment.user_id WHERE other_assignment.tenant_id=assignment.tenant_id AND other_assignment.user_id<>assignment.user_id AND other_role.code::text='platform_admin' AND other_role.status='ACTIVE' AND other_role.deleted_at IS NULL AND other_user.status='ACTIVE' AND other_user.deleted_at IS NULL) ORDER BY role.id`, `SELECT 1 WHERE false`},
	}
	for _, value := range []struct{ category, table string }{
		{"SEMANTIC_ENTITY", "entities"}, {"SEMANTIC_MODEL", "semantic_models"},
		{"SEMANTIC_MEASURE", "measures"}, {"SEMANTIC_HIERARCHY", "hierarchies"},
		{"SEMANTIC_QUALITY_RULE", "quality_rules"}, {"METRIC_VERSION", "metric_versions"},
		{"METRIC_DIMENSION_VERSION", "metric_dimension_versions"},
		{"EVALUATION_CASE_VERSION", "evaluation_case_versions"},
		{"KPI_BUNDLE_VERSION", "kpi_bundle_versions"},
	} {
		adapters = append(adapters, versionedOwnerAdapter(value.category, value.table))
	}
	for _, value := range []struct{ category, table string }{
		{"REPORT_TEMPLATE", "report_templates"}, {"REPORT_STRUCTURE_TEMPLATE", "report_structure_templates"},
		{"REPORT_LAYOUT_TEMPLATE", "report_layout_templates"}, {"REPORT_THEME", "report_themes"},
		{"REPORT_NARRATIVE_TEMPLATE", "report_narrative_templates"},
	} {
		adapters = append(adapters, globalOwnerAdapter(value.category, value.table))
	}
	return adapters
}

func versionedOwnerAdapter(category, table string) OwnerTransferAdapter {
	return SQLAdapter{category: category, disposition: Transfer,
		selectSQL: fmt.Sprintf(`SELECT domain_id::text,id::text,extract(epoch from updated_at)::text FROM askdata.%s WHERE tenant_id=$1 AND owner_id=$2 AND status<>'DEPRECATED' ORDER BY domain_id,id`, table),
		applySQL:  fmt.Sprintf(`UPDATE askdata.%s SET owner_id=$1,updated_at=$2 WHERE tenant_id=$3 AND domain_id=$4 AND id=$5 AND owner_id=$6 AND extract(epoch from updated_at)::text=$7`, table)}
}

func globalOwnerAdapter(category, table string) OwnerTransferAdapter {
	return SQLAdapter{category: category, disposition: Transfer,
		selectSQL: fmt.Sprintf(`SELECT ''::text,id::text,owner_user_id::text FROM platform.%s WHERE tenant_id=$1 AND owner_user_id=$2 ORDER BY id`, table),
		applySQL:  fmt.Sprintf(`UPDATE platform.%s SET owner_user_id=$1 WHERE tenant_id=$3 AND id=$5 AND owner_user_id=$6 AND owner_user_id::text=$7 AND $2::timestamptz IS NOT NULL AND $4::text=''`, table)}
}

func (s *Service) Preview(ctx context.Context, tenantID, actorID, targetID string) (Preview, error) {
	if !canonical(tenantID) || !canonical(actorID) || !canonical(targetID) || actorID == targetID {
		return Preview{}, ErrInvalid
	}
	if e := s.authorize(ctx, tenantID, actorID); e != nil {
		return Preview{}, e
	}
	items, e := s.previewSystem(ctx, tenantID, targetID)
	if e != nil {
		return Preview{}, e
	}
	result := Preview{TargetUserID: targetID, Items: items, Counts: map[string]int{}, CanDisable: true}
	for _, item := range items {
		result.Counts[item.Category]++
		if item.Disposition == Block {
			result.CanDisable = false
		}
	}
	return result, nil
}
func (s *Service) PlanAndExecute(ctx context.Context, tenantID, actorID, targetID string, mappings []Mapping) (Batch, error) {
	preview, e := s.Preview(ctx, tenantID, actorID, targetID)
	if e != nil {
		return Batch{}, e
	}
	if !preview.CanDisable {
		return Batch{}, ErrBlocked
	}
	mapped := map[string]string{}
	for _, mapping := range mappings {
		if (mapping.DomainID != "" && !canonical(mapping.DomainID)) || !canonical(mapping.ReceiverUserID) || mapping.ReceiverUserID == targetID || s.adapters[mapping.Category] == nil {
			return Batch{}, ErrInvalid
		}
		key := mapping.Category + "|" + mapping.DomainID
		if mapped[key] != "" {
			return Batch{}, ErrInvalid
		}
		mapped[key] = mapping.ReceiverUserID
	}
	for index := range preview.Items {
		if preview.Items[index].Disposition == Transfer {
			receiver := mapped[preview.Items[index].Category+"|"+preview.Items[index].DomainID]
			if receiver == "" {
				return Batch{}, ErrInvalid
			}
			preview.Items[index].ReceiverUserID = receiver
		}
	}
	raw, _ := json.Marshal(preview.Items)
	digest := sha256.Sum256(raw)
	planHash := hex.EncodeToString(digest[:])
	batchID := uuid.NewString()
	now := s.clock()
	system := database.WithoutAccessContext(ctx)
	e = database.WithTenantTx(system, s.pool, tenantID, func(tx pgx.Tx) error {
		var active bool
		if e := tx.QueryRow(ctx, `SELECT status='ACTIVE' FROM platform.users WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, targetID).Scan(&active); e != nil || !active {
			if e != nil {
				return e
			}
			return ErrConflict
		}
		for _, item := range preview.Items {
			if item.Disposition == Transfer {
				var valid bool
				if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.users user_account WHERE user_account.tenant_id=$1 AND user_account.id=$2 AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL AND ($3='' OR EXISTS(SELECT 1 FROM platform.domain_memberships membership WHERE membership.tenant_id=user_account.tenant_id AND membership.user_id=user_account.id AND membership.domain_id=$3::uuid AND membership.status='ACTIVE')))`, tenantID, item.ReceiverUserID, item.DomainID).Scan(&valid); e != nil {
					return e
				}
				if !valid {
					return ErrForbidden
				}
			}
		}
		if _, e := tx.Exec(ctx, `INSERT INTO platform.user_lifecycle_batches(id,tenant_id,target_user_id,requested_by,status,plan_hash,created_at,updated_at) VALUES($1,$2,$3,$4,'PLANNED',$5,$6,$6)`, batchID, tenantID, targetID, actorID, planHash, now); e != nil {
			return e
		}
		for _, item := range preview.Items {
			if _, e := tx.Exec(ctx, `INSERT INTO platform.user_lifecycle_batch_items(id,tenant_id,batch_id,domain_id,category,object_id,disposition,receiver_user_id,source_version) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7,NULLIF($8,'')::uuid,$9)`, uuid.NewString(), tenantID, batchID, item.DomainID, item.Category, item.ObjectID, item.Disposition, item.ReceiverUserID, item.SourceVersion); e != nil {
				return e
			}
		}
		_, e := tx.Exec(ctx, `INSERT INTO platform.user_lifecycle_events(id,tenant_id,batch_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,'PLANNED',$4,jsonb_build_object('itemCount',$5::integer),$6)`, uuid.NewString(), tenantID, batchID, actorID, len(preview.Items), now)
		return e
	})
	if e != nil {
		return Batch{}, e
	}
	if e = s.execute(ctx, tenantID, actorID, batchID, 0); e != nil {
		_ = s.fail(ctx, tenantID, actorID, batchID, "TRANSFER_APPLY_FAILED")
		return s.Get(ctx, tenantID, actorID, batchID)
	}
	return s.Get(ctx, tenantID, actorID, batchID)
}

func (s *Service) execute(ctx context.Context, tenantID, actorID, batchID string, expectedVersion int64) error {
	system := database.WithoutAccessContext(ctx)
	now := s.clock()
	return database.WithTenantTx(system, s.pool, tenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `SELECT set_config('app.owner_transfer_mode','on',true)`); e != nil {
			return e
		}
		var target, status string
		var recordVersion int64
		if e := tx.QueryRow(ctx, `SELECT target_user_id::text,status,record_version FROM platform.user_lifecycle_batches WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, batchID).Scan(&target, &status, &recordVersion); e != nil {
			return e
		}
		if (status != "PLANNED" && status != "TRANSFER_FAILED") || (expectedVersion > 0 && recordVersion != expectedVersion) {
			return ErrConflict
		}
		if _, e := tx.Exec(ctx, `UPDATE platform.user_lifecycle_batches SET status='EXECUTING',failure_code='',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenantID, batchID); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT COALESCE(domain_id::text,''),category,object_id::text,disposition,COALESCE(receiver_user_id::text,''),source_version FROM platform.user_lifecycle_batch_items WHERE tenant_id=$1 AND batch_id=$2 ORDER BY category,domain_id NULLS FIRST,object_id FOR UPDATE`, tenantID, batchID)
		if e != nil {
			return e
		}
		items := []Item{}
		for rows.Next() {
			var item Item
			if e = rows.Scan(&item.DomainID, &item.Category, &item.ObjectID, &item.Disposition, &item.ReceiverUserID, &item.SourceVersion); e != nil {
				rows.Close()
				return e
			}
			items = append(items, item)
		}
		rows.Close()
		for _, item := range items {
			adapter := s.adapters[item.Category]
			if adapter == nil {
				return ErrConflict
			}
			if e = adapter.Apply(ctx, tx, tenantID, target, item, now); e != nil {
				return fmt.Errorf("apply %s: %w", item.Category, e)
			}
			if _, e = tx.Exec(ctx, `UPDATE platform.user_lifecycle_batch_items SET executed_at=$1 WHERE tenant_id=$2 AND batch_id=$3 AND category=$4 AND object_id=$5`, now, tenantID, batchID, item.Category, item.ObjectID); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE decision.decision_notifications notification SET resolved_at=$3 FROM platform.user_lifecycle_batch_items item WHERE item.tenant_id=$1 AND item.batch_id=$2 AND item.category IN('DECISION','DECISION_ACTION') AND notification.tenant_id=item.tenant_id AND notification.resolved_at IS NULL AND ((item.category='DECISION_ACTION' AND item.object_id=notification.action_id) OR (item.category='DECISION' AND item.object_id=notification.decision_id AND notification.action_id IS NULL)) AND EXISTS(SELECT 1 FROM decision.decision_notifications duplicate WHERE duplicate.tenant_id=notification.tenant_id AND duplicate.recipient_user_id=item.receiver_user_id AND duplicate.dedup_key=notification.dedup_key)`, tenantID, batchID, now); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE decision.decision_notifications notification SET recipient_user_id=item.receiver_user_id FROM platform.user_lifecycle_batch_items item WHERE item.tenant_id=$1 AND item.batch_id=$2 AND item.category IN('DECISION','DECISION_ACTION') AND notification.tenant_id=item.tenant_id AND notification.resolved_at IS NULL AND ((item.category='DECISION_ACTION' AND item.object_id=notification.action_id) OR (item.category='DECISION' AND item.object_id=notification.decision_id AND notification.action_id IS NULL))`, tenantID, batchID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE platform.auth_sessions SET revoked_at=$1,revoke_reason='USER_DISABLED',last_used_at=$1 WHERE tenant_id=$2 AND user_id=$3 AND revoked_at IS NULL`, now, tenantID, target); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE platform.users SET status='DISABLED',token_version=token_version+1,version=version+1 WHERE tenant_id=$1 AND id=$2 AND status='ACTIVE'`, tenantID, target); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE platform.user_lifecycle_batches SET status='COMPLETED',record_version=record_version+1,updated_at=$1,completed_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenantID, batchID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO platform.user_lifecycle_events(id,tenant_id,batch_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,'COMPLETED',$4,jsonb_build_object('targetUserId',$5::uuid),$6)`, uuid.NewString(), tenantID, batchID, actorID, target, now); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'DISABLE_USER_WITH_OWNER_TRANSFER','USER',$3,jsonb_build_object('batchId',$4::uuid))`, tenantID, actorID, target, batchID)
		return e
	})
}
func (s *Service) Retry(ctx context.Context, tenantID, actorID, batchID string, expectedVersion int64) (Batch, error) {
	if !canonical(batchID) || expectedVersion < 1 {
		return Batch{}, ErrInvalid
	}
	if e := s.authorize(ctx, tenantID, actorID); e != nil {
		return Batch{}, e
	}
	current, e := s.Get(ctx, tenantID, actorID, batchID)
	if e != nil {
		return Batch{}, e
	}
	if current.Status != "TRANSFER_FAILED" || current.RecordVersion != expectedVersion {
		return Batch{}, ErrConflict
	}
	if e = s.execute(ctx, tenantID, actorID, batchID, expectedVersion); e != nil {
		_ = s.fail(ctx, tenantID, actorID, batchID, "TRANSFER_RETRY_FAILED")
	}
	return s.Get(ctx, tenantID, actorID, batchID)
}
func (s *Service) fail(ctx context.Context, tenantID, actorID, batchID, code string) error {
	return database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenantID, func(tx pgx.Tx) error {
		now := s.clock()
		if _, e := tx.Exec(ctx, `UPDATE platform.user_lifecycle_batches SET status='TRANSFER_FAILED',failure_code=$1,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND status<>'COMPLETED'`, code, now, tenantID, batchID); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO platform.user_lifecycle_events(id,tenant_id,batch_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,'TRANSFER_FAILED',$4,jsonb_build_object('failureCode',$5::text),$6)`, uuid.NewString(), tenantID, batchID, actorID, code, now)
		return e
	})
}
func (s *Service) Get(ctx context.Context, tenantID, actorID, batchID string) (Batch, error) {
	if !canonical(batchID) {
		return Batch{}, ErrInvalid
	}
	if e := s.authorize(ctx, tenantID, actorID); e != nil {
		return Batch{}, e
	}
	var result Batch
	result.Items = []Item{}
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenantID, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT id::text,target_user_id::text,status,plan_hash,failure_code,record_version,created_at,updated_at,completed_at FROM platform.user_lifecycle_batches WHERE tenant_id=$1 AND id=$2`, tenantID, batchID).Scan(&result.ID, &result.TargetUserID, &result.Status, &result.PlanHash, &result.FailureCode, &result.RecordVersion, &result.CreatedAt, &result.UpdatedAt, &result.CompletedAt); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT COALESCE(domain_id::text,''),category,object_id::text,disposition,COALESCE(receiver_user_id::text,''),source_version,executed_at FROM platform.user_lifecycle_batch_items WHERE tenant_id=$1 AND batch_id=$2 ORDER BY category,domain_id NULLS FIRST,object_id`, tenantID, batchID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var item Item
			if e = rows.Scan(&item.DomainID, &item.Category, &item.ObjectID, &item.Disposition, &item.ReceiverUserID, &item.SourceVersion, &item.ExecutedAt); e != nil {
				return e
			}
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return Batch{}, ErrNotFound
	}
	return result, e
}
func (s *Service) previewSystem(ctx context.Context, tenantID, targetID string) ([]Item, error) {
	items := []Item{}
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenantID, func(tx pgx.Tx) error {
		var active bool
		if e := tx.QueryRow(ctx, `SELECT status='ACTIVE' FROM platform.users WHERE tenant_id=$1 AND id=$2`, tenantID, targetID).Scan(&active); e != nil {
			return e
		}
		if !active {
			return ErrConflict
		}
		for _, adapter := range s.ordered {
			values, e := adapter.Preview(ctx, tx, tenantID, targetID)
			if e != nil {
				return fmt.Errorf("preview %s: %w", adapter.Category(), e)
			}
			items = append(items, values...)
		}
		return nil
	})
	return items, e
}
func (s *Service) authorize(ctx context.Context, tenantID, actorID string) error {
	if !canonical(tenantID) || !canonical(actorID) {
		return ErrInvalid
	}
	allowed, e := s.admins.IsPlatformAdministrator(ctx, tenantID, actorID)
	if e != nil {
		return e
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
func (s *Service) clock() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}
func canonical(value string) bool {
	parsed, e := uuid.Parse(strings.TrimSpace(value))
	return e == nil && parsed.String() == value
}
