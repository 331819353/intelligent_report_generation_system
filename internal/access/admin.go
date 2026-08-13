package access

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type UserDomain struct {
	ID         string `json:"id"`
	Code       string `json:"code"`
	Name       string `json:"name"`
	Default    bool   `json:"default"`
	MemberRole string `json:"memberRole"`
}
type UserSummary struct {
	ID                    string       `json:"id"`
	EmployeeNo            string       `json:"employeeNo"`
	Email                 string       `json:"email"`
	DisplayName           string       `json:"displayName"`
	Status                string       `json:"status"`
	PlatformAdministrator bool         `json:"platformAdministrator"`
	Domains               []UserDomain `json:"domains"`
	LastLoginAt           *string      `json:"lastLoginAt,omitempty"`
	CreatedAt             string       `json:"createdAt"`
}
type BusinessDomain struct {
	ID          string `json:"id"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Default     bool   `json:"default"`
	Version     int64  `json:"version"`
	CreatedAt   string `json:"createdAt"`
	// AccessSensitivity drives whether joining this domain needs an independent
	// security co-signature, using the same vocabulary as data request
	// sensitivity so the platform has one definition of "sensitive".
	AccessSensitivity string                `json:"accessSensitivity"`
	Administrators    []DomainAdministrator `json:"administrators"`
}
type DomainAdministrator struct {
	ID          string `json:"id"`
	EmployeeNo  string `json:"employeeNo"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
}
type ShareTarget struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}
type DomainCatalogItem struct {
	BusinessDomain
	AccessStatus string `json:"accessStatus"`
}
type DomainApplication struct {
	ID                   string `json:"id"`
	DomainID             string `json:"domainId"`
	DomainCode           string `json:"domainCode"`
	DomainName           string `json:"domainName"`
	ApplicantUserID      string `json:"applicantUserId"`
	ApplicantEmail       string `json:"applicantEmail"`
	ApplicantDisplayName string `json:"applicantDisplayName"`
	Status               string `json:"status"`
	Reason               string `json:"reason"`
	ReviewComment        string `json:"reviewComment"`
	ReviewedBy           string `json:"reviewedBy,omitempty"`
	ReviewedAt           string `json:"reviewedAt,omitempty"`
	CreatedAt            string `json:"createdAt"`
	// Dual-approval governance for sensitive domains. All of these are derived
	// server-side; the browser never decides whether a second seat is needed.
	DomainSensitivity    string                   `json:"domainSensitivity"`
	RequiresDualApproval bool                     `json:"requiresDualApproval"`
	SLADueAt             string                   `json:"slaDueAt,omitempty"`
	SLAStatus            string                   `json:"slaStatus"`
	EscalationLevel      int                      `json:"escalationLevel"`
	Approvals            []DomainAccessApproval   `json:"approvals"`
	ApprovedRoles        []string                 `json:"approvedRoles"`
	ActorApproval        string                   `json:"actorApproval,omitempty"`
	OpenSeats            []string                 `json:"openSeats"`
	Escalations          []DomainAccessEscalation `json:"escalations"`
}

// DomainAccessApproval is one immutable approval receipt. Comments are stored
// in the clear because a domain access decision has to stay readable to the
// applicant and to auditors, unlike the release approval reasons which are
// hashed.
type DomainAccessApproval struct {
	ReviewerID          string `json:"reviewerId"`
	ReviewerDisplayName string `json:"reviewerDisplayName"`
	ReviewRole          string `json:"reviewRole"`
	Decision            string `json:"decision"`
	Comment             string `json:"comment"`
	CreatedAt           string `json:"createdAt"`
}

type DomainAccessEscalation struct {
	Level       int    `json:"level"`
	EscalatedBy string `json:"escalatedBy"`
	Note        string `json:"note"`
	CreatedAt   string `json:"createdAt"`
}
type PlatformApproval struct {
	ID                   string  `json:"id"`
	Kind                 string  `json:"kind"`
	Version              int64   `json:"version"`
	ResourceID           string  `json:"resourceId"`
	ResourceName         string  `json:"resourceName"`
	DomainID             string  `json:"domainId"`
	DomainCode           string  `json:"domainCode"`
	DomainName           string  `json:"domainName"`
	RequesterUserID      string  `json:"requesterUserId"`
	RequesterEmail       string  `json:"requesterEmail"`
	RequesterDisplayName string  `json:"requesterDisplayName"`
	Status               string  `json:"status"`
	Note                 string  `json:"note"`
	ReviewerDisplayName  string  `json:"reviewerDisplayName,omitempty"`
	SubmittedAt          string  `json:"submittedAt"`
	ReviewedAt           *string `json:"reviewedAt,omitempty"`
	// Two-seat governance, populated for DOMAIN_ACCESS requests only. Other
	// approval kinds report a single empty seat set.
	RequiresDualApproval bool     `json:"requiresDualApproval"`
	ApprovedRoles        []string `json:"approvedRoles"`
	OpenSeats            []string `json:"openSeats"`
	SLADueAt             string   `json:"slaDueAt,omitempty"`
	SLAStatus            string   `json:"slaStatus"`
	EscalationLevel      int      `json:"escalationLevel"`
}
type PlatformAuditLog struct {
	ID               string `json:"id"`
	Action           string `json:"action"`
	ResourceType     string `json:"resourceType"`
	ResourceID       string `json:"resourceId"`
	Result           string `json:"result"`
	ActorDisplayName string `json:"actorDisplayName"`
	ActorEmail       string `json:"actorEmail"`
	RequestID        string `json:"requestId,omitempty"`
	OccurredAt       string `json:"occurredAt"`
}
type AdminStore struct{ pool *pgxpool.Pool }

var businessDomainCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

var (
	ErrDomainApplicationForbidden              = errors.New("仅目标领域管理员或平台管理员可以审批领域申请")
	ErrDomainApplicationPlatformReviewRequired = errors.New("超出领域管理员权限范围，需要平台管理员审批")
	ErrDomainApplicationSeatInvalid            = errors.New("审批席位无效：敏感领域需要业务 Owner 与安全复核两个不同席位")
	ErrDomainApplicationSeatTaken              = errors.New("该审批席位已由他人签署")
	ErrDomainApplicationSelfCosign             = errors.New("同一账号不得同时占据业务 Owner 与安全复核席位")
	ErrDomainApplicationSecurityReviewRequired = errors.New("安全复核席位需要平台管理员签署")
	ErrDomainApplicationEscalationInvalid      = errors.New("升级无效：申请未超期或升级级别已用尽")
)

// Domain access review seats. DOMAIN_OWNER carries the business decision and
// SECURITY the independent check; a sensitive domain needs both, filled by two
// different accounts.
const (
	domainReviewRoleOwner    = "DOMAIN_OWNER"
	domainReviewRoleSecurity = "SECURITY"
)

// domainAccessReviewSLA is the review window before escalation becomes
// available, matching the 24 hour SLA used by the semantic release approvals.
const domainAccessReviewSLA = 24 * time.Hour

// requiresDualDomainApproval mirrors datarequest.RequiresSecurityCosign so the
// platform has one definition of "sensitive enough to need a second pair of
// eyes" rather than two that can drift apart.
func requiresDualDomainApproval(sensitivity string) bool {
	return sensitivity == "CONFIDENTIAL" || sensitivity == "RESTRICTED"
}

func validDomainReviewRole(role string) bool {
	return role == domainReviewRoleOwner || role == domainReviewRoleSecurity
}

// domainApplicationSLAStatus reports the review window without the browser
// having to compute it from timestamps.
func domainApplicationSLAStatus(status string, dueAt *time.Time) string {
	if status != "PENDING" || dueAt == nil {
		return "NOT_APPLICABLE"
	}
	remaining := time.Until(*dueAt)
	switch {
	case remaining <= 0:
		return "OVERDUE"
	case remaining <= 4*time.Hour:
		return "DUE_SOON"
	default:
		return "ON_TRACK"
	}
}

func validateDomainApplicationReviewer(
	platformAdministrator, domainAdministrator bool,
) error {
	if !platformAdministrator && !domainAdministrator {
		return ErrDomainApplicationForbidden
	}
	return nil
}

// NewAdminStore 创建平台、领域和用户三级治理存储。
func NewAdminStore(pool *pgxpool.Pool) *AdminStore { return &AdminStore{pool: pool} }

// ListUsers 返回平台身份与领域归属；不暴露可自由组合的角色权限。
func (s *AdminStore) ListUsers(ctx context.Context, tenantID string) ([]UserSummary, error) {
	var users []UserSummary
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT
			  u.id,u.employee_no,u.email,u.display_name,u.status,u.last_login_at::text,u.created_at::text,
			  EXISTS(
			    SELECT 1
			    FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role
			      ON role.id=assignment.role_id
			     AND role.tenant_id=assignment.tenant_id
			    WHERE assignment.tenant_id=u.tenant_id
			      AND assignment.user_id=u.id
			      AND role.code::text='platform_admin'
			      AND role.status='ACTIVE'
			      AND role.deleted_at IS NULL
			  ),
			  COALESCE((
			    SELECT jsonb_agg(
			      jsonb_build_object(
			        'id',domain.id,'code',domain.code,'name',domain.name,
			        'default',domain.is_default,'memberRole',membership.member_role
			      )
			      ORDER BY domain.is_default DESC,domain.name
			    )
			    FROM platform.domain_memberships AS membership
			    JOIN platform.business_domains AS domain
			      ON domain.id=membership.domain_id
			     AND domain.tenant_id=membership.tenant_id
			    WHERE membership.tenant_id=u.tenant_id
			      AND membership.user_id=u.id
			      AND membership.status='ACTIVE'
			      AND domain.deleted_at IS NULL
			  ),'[]'::jsonb)
			FROM platform.users u
			WHERE u.deleted_at IS NULL
			ORDER BY u.created_at,u.email`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var user UserSummary
			var domainsJSON []byte
			if err := rows.Scan(
				&user.ID, &user.EmployeeNo, &user.Email, &user.DisplayName, &user.Status,
				&user.LastLoginAt, &user.CreatedAt, &user.PlatformAdministrator, &domainsJSON,
			); err != nil {
				return err
			}
			if err := json.Unmarshal(domainsJSON, &user.Domains); err != nil {
				return err
			}
			users = append(users, user)
		}
		return rows.Err()
	})
	return users, err
}

// ListShareTargets returns the active users and roles that can actually receive
// an internal share in the caller's selected domain. It exposes directory
// labels only; role permissions and users from other domains remain hidden.
func (s *AdminStore) ListShareTargets(
	ctx context.Context, tenantID, actorID, domainID string,
) (targets []ShareTarget, err error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(actorID) == "" || strings.TrimSpace(domainID) == "" {
		return nil, errors.New("share target scope is required")
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var allowed bool
		if err := tx.QueryRow(ctx, `SELECT platform.user_is_platform_administrator() OR EXISTS(
			SELECT 1 FROM platform.domain_memberships AS membership
			JOIN platform.business_domains AS domain
			  ON domain.id=membership.domain_id AND domain.tenant_id=membership.tenant_id
			WHERE membership.tenant_id=$1::uuid AND membership.domain_id=$2::uuid
			  AND membership.user_id=$3::uuid AND membership.status='ACTIVE'
			  AND domain.status='ACTIVE' AND domain.deleted_at IS NULL
		)`, tenantID, domainID, actorID).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return errors.New("active domain membership is required")
		}
		rows, queryErr := tx.Query(ctx, `
			SELECT id::text,target_type,target_name,target_detail FROM (
			  SELECT user_account.id,'USER'::text AS target_type,user_account.display_name AS target_name,
			    user_account.email::text AS target_detail,0 AS target_order
			  FROM platform.users AS user_account
			  JOIN platform.domain_memberships AS membership
			    ON membership.user_id=user_account.id AND membership.tenant_id=user_account.tenant_id
			  WHERE user_account.tenant_id=$1::uuid AND membership.domain_id=$2::uuid
			    AND membership.status='ACTIVE' AND user_account.status='ACTIVE'
			    AND user_account.deleted_at IS NULL
			  UNION ALL
			  SELECT role.id,'ROLE',role.name,COALESCE(NULLIF(role.description,''),role.code::text),1
			  FROM platform.roles AS role
			  WHERE role.tenant_id=$1::uuid AND role.status='ACTIVE' AND role.deleted_at IS NULL
			    AND EXISTS(
			      SELECT 1 FROM platform.user_roles AS assignment
			      JOIN platform.domain_memberships AS membership
			        ON membership.user_id=assignment.user_id AND membership.tenant_id=assignment.tenant_id
			      JOIN platform.users AS member
			        ON member.id=assignment.user_id AND member.tenant_id=assignment.tenant_id
			      WHERE assignment.role_id=role.id AND membership.domain_id=$2::uuid
			        AND membership.status='ACTIVE' AND member.status='ACTIVE' AND member.deleted_at IS NULL
			    )
			) AS directory ORDER BY target_order,target_name,id`, tenantID, domainID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item ShareTarget
			if err := rows.Scan(&item.ID, &item.Type, &item.Name, &item.Detail); err != nil {
				return err
			}
			targets = append(targets, item)
		}
		return rows.Err()
	})
	return targets, err
}

// ListPlatformApprovals provides one metadata-only queue for platform
// governance. Publication content remains inside its business domain; this
// view exposes only the request identity, status and responsible domain.
func (s *AdminStore) ListPlatformApprovals(
	ctx context.Context, tenantID, actorID string, limit int,
) (items []PlatformApproval, err error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("approval limit must be between 1 and 200")
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, checkErr := isPlatformAdministratorTx(ctx, tx, actorID)
		if checkErr != nil {
			return checkErr
		}
		if !allowed {
			return errors.New("platform administrator permission is required")
		}
		// 跨领域审批中心只读取治理元数据；完成平台管理员复核后在当前
		// 事务切换为系统读取模式，避免资产 RLS 把其他领域的队列静默过滤。
		if _, setErr := tx.Exec(ctx, "SELECT set_config('app.access_mode','SYSTEM',true)"); setErr != nil {
			return setErr
		}
		rows, queryErr := tx.Query(ctx, `SELECT
			approval_id,kind,approval_version,resource_id,resource_name,domain_id,domain_code,
			domain_name,requester_user_id,requester_email,requester_display_name,
			status,note,reviewer_display_name,submitted_at::text,reviewed_at::text,
			requires_dual_approval,approved_roles,sla_due_at,escalation_level
		FROM (
			SELECT application.id AS approval_id,'DOMAIN_ACCESS'::text AS kind,0::bigint AS approval_version,
				domain.id AS resource_id,domain.name AS resource_name,
				domain.id AS domain_id,domain.code::text AS domain_code,domain.name AS domain_name,
				applicant.id AS requester_user_id,applicant.email::text AS requester_email,
				applicant.display_name AS requester_display_name,application.status::text AS status,
				application.reason AS note,COALESCE(reviewer.display_name,'') AS reviewer_display_name,
				application.created_at AS submitted_at,application.reviewed_at,
				application.requires_dual_approval,
				COALESCE((
				  SELECT array_agg(approval.review_role ORDER BY approval.review_role)
				  FROM platform.domain_access_approvals AS approval
				  WHERE approval.application_id=application.id AND approval.decision='APPROVED'
				),ARRAY[]::text[]) AS approved_roles,
				application.sla_due_at,application.escalation_level
			FROM platform.domain_access_applications AS application
			JOIN platform.business_domains AS domain
			  ON domain.id=application.domain_id AND domain.tenant_id=application.tenant_id
			JOIN platform.users AS applicant
			  ON applicant.id=application.applicant_user_id AND applicant.tenant_id=application.tenant_id
			LEFT JOIN platform.users AS reviewer
			  ON reviewer.id=application.reviewed_by AND reviewer.tenant_id=application.tenant_id
			UNION ALL
			SELECT request.id,'DATA_SOURCE',request.version,source.id,source.name,
				domain.id,domain.code::text,domain.name,requester.id,requester.email::text,
				requester.display_name,request.status,request.request_note,
				COALESCE(reviewer.display_name,''),request.submitted_at,request.reviewed_at,
				false,ARRAY[]::text[],NULL::timestamptz,0
			FROM platform.data_source_publication_requests AS request
			JOIN platform.data_sources AS source
			  ON source.id=request.data_source_id AND source.tenant_id=request.tenant_id
			JOIN platform.business_domains AS domain
			  ON domain.id=source.domain_id AND domain.tenant_id=source.tenant_id
			JOIN platform.users AS requester
			  ON requester.id=request.requester_user_id AND requester.tenant_id=request.tenant_id
			LEFT JOIN platform.users AS reviewer
			  ON reviewer.id=request.reviewer_user_id AND reviewer.tenant_id=request.tenant_id
			UNION ALL
			SELECT request.id,'DATASET',request.version,dataset.id,dataset.name,
				domain.id,domain.code::text,domain.name,requester.id,requester.email::text,
				requester.display_name,request.status,request.request_note,
				COALESCE(reviewer.display_name,''),request.submitted_at,request.reviewed_at,
				false,ARRAY[]::text[],NULL::timestamptz,0
			FROM platform.dataset_publication_requests AS request
			JOIN platform.datasets AS dataset
			  ON dataset.id=request.dataset_id AND dataset.tenant_id=request.tenant_id
			JOIN platform.business_domains AS domain
			  ON domain.id=dataset.domain_id AND domain.tenant_id=dataset.tenant_id
			JOIN platform.users AS requester
			  ON requester.id=request.requester_user_id AND requester.tenant_id=request.tenant_id
			LEFT JOIN platform.users AS reviewer
			  ON reviewer.id=request.reviewer_user_id AND reviewer.tenant_id=request.tenant_id
		) AS approvals
		ORDER BY submitted_at DESC,approval_id DESC
		LIMIT $1`, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item PlatformApproval
			var slaDueAt *time.Time
			if scanErr := rows.Scan(
				&item.ID, &item.Kind, &item.Version, &item.ResourceID, &item.ResourceName,
				&item.DomainID, &item.DomainCode, &item.DomainName,
				&item.RequesterUserID, &item.RequesterEmail,
				&item.RequesterDisplayName, &item.Status, &item.Note,
				&item.ReviewerDisplayName, &item.SubmittedAt, &item.ReviewedAt,
				&item.RequiresDualApproval, &item.ApprovedRoles, &slaDueAt,
				&item.EscalationLevel,
			); scanErr != nil {
				return scanErr
			}
			if item.ApprovedRoles == nil {
				item.ApprovedRoles = []string{}
			}
			if slaDueAt != nil {
				item.SLADueAt = slaDueAt.UTC().Format(time.RFC3339Nano)
			}
			item.SLAStatus = domainApplicationSLAStatus(item.Status, slaDueAt)
			// The seats still open are derived here so the cross-domain queue can
			// show a platform administrator what a request is actually waiting on.
			item.OpenSeats = []string{}
			if item.Kind == "DOMAIN_ACCESS" && item.Status == "PENDING" {
				required := []string{domainReviewRoleOwner}
				if item.RequiresDualApproval {
					required = append(required, domainReviewRoleSecurity)
				}
				for _, seat := range required {
					if !containsString(item.ApprovedRoles, seat) {
						item.OpenSeats = append(item.OpenSeats, seat)
					}
				}
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// ListPlatformAuditLogs returns an immutable, metadata-only operational trail.
// Raw audit detail is deliberately omitted because it may contain resource
// diagnostics that belong inside a business domain.
func (s *AdminStore) ListPlatformAuditLogs(
	ctx context.Context, tenantID string, limit int,
) (items []PlatformAuditLog, err error) {
	if limit < 1 || limit > 200 {
		return nil, errors.New("audit log limit must be between 1 and 200")
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT
			log.id::text,log.action,log.resource_type,COALESCE(log.resource_id,''),
			log.result,COALESCE(actor.display_name,'系统'),COALESCE(actor.email::text,''),
			COALESCE(log.request_id,''),log.occurred_at::text
		FROM platform.audit_logs AS log
		LEFT JOIN platform.users AS actor
		  ON actor.id=log.actor_user_id AND actor.tenant_id=log.tenant_id
		ORDER BY log.occurred_at DESC,log.id DESC
		LIMIT $1`, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item PlatformAuditLog
			if scanErr := rows.Scan(
				&item.ID, &item.Action, &item.ResourceType, &item.ResourceID,
				&item.Result, &item.ActorDisplayName, &item.ActorEmail,
				&item.RequestID, &item.OccurredAt,
			); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// UpdateUserStatus controls the lifecycle of one registered account. Disabling
// an account revokes all live sessions and increments token_version so already
// issued access tokens stop working immediately.
func (s *AdminStore) UpdateUserStatus(
	ctx context.Context, tenantID, actorID, userID, status string,
) error {
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "ACTIVE" && status != "DISABLED" {
		return errors.New("user status must be ACTIVE or DISABLED")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !allowed {
			return errors.New("only a platform administrator can update user status")
		}
		var currentStatus string
		if err := tx.QueryRow(ctx, `SELECT status::text
			FROM platform.users
			WHERE id=$1::uuid AND deleted_at IS NULL
			FOR UPDATE`, userID).Scan(&currentStatus); err != nil {
			return err
		}
		if currentStatus == status {
			return nil
		}
		if status == "DISABLED" {
			if userID == actorID {
				return errors.New("platform administrator cannot disable the current account")
			}
			platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, userID)
			if err != nil {
				return err
			}
			if platformAdministrator {
				return errors.New("remove the platform administrator identity before disabling this user")
			}
			var domainAdministrator bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM platform.domain_memberships
				WHERE user_id=$1::uuid AND status='ACTIVE'
				  AND member_role='DOMAIN_ADMIN'
			)`, userID).Scan(&domainAdministrator); err != nil {
				return err
			}
			if domainAdministrator {
				return errors.New("replace the domain administrator identity before disabling this user")
			}
		}
		result, err := tx.Exec(ctx, `UPDATE platform.users
			SET status=$2::platform.user_status,
			    token_version=token_version+1,
			    version=version+1
			WHERE id=$1::uuid AND tenant_id=$3::uuid AND deleted_at IS NULL`,
			userID, status, tenantID,
		)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("user was not found")
		}
		var revokedSessions int64
		if status == "DISABLED" {
			result, err := tx.Exec(ctx, `UPDATE platform.auth_sessions
				SET revoked_at=now(),revoke_reason='USER_DISABLED',last_used_at=now()
				WHERE user_id=$1::uuid AND revoked_at IS NULL`, userID)
			if err != nil {
				return err
			}
			revokedSessions = result.RowsAffected()
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'UPDATE_USER_STATUS','USER',$3,jsonb_build_object(
			'previousStatus',$4::text,'status',$5::text,
			'revokedSessions',$6::bigint
		))`, tenantID, actorID, userID, currentStatus, status, revokedSessions)
		return err
	})
}

// ListDomains 返回当前用户能够进入的业务领域。平台管理员拥有全平台权限，
// 因此可进入租户内全部领域；其他用户仍只返回显式加入的领域。
func (s *AdminStore) ListDomains(
	ctx context.Context, tenantID, userID string,
) ([]BusinessDomain, error) {
	platformAdministrator, err := s.IsPlatformAdministrator(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	return s.listDomains(ctx, tenantID, userID, platformAdministrator)
}

// ListManagedDomains returns the full catalog only to platform administrators.
func (s *AdminStore) ListManagedDomains(
	ctx context.Context, tenantID, userID string,
) ([]BusinessDomain, error) {
	allowed, err := s.IsPlatformAdministrator(ctx, tenantID, userID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errors.New("platform administrator permission is required")
	}
	return s.listDomains(ctx, tenantID, userID, true)
}

func (s *AdminStore) listDomains(
	ctx context.Context, tenantID, userID string, includeAll bool,
) ([]BusinessDomain, error) {
	var domains []BusinessDomain
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT
			  domain.id,domain.code,domain.name,domain.description,domain.status,
			  domain.is_default,domain.version,domain.created_at::text,
			  domain.access_sensitivity,
			  COALESCE((
			    SELECT jsonb_agg(jsonb_build_object(
			      'id',administrator.id,'employeeNo',administrator.employee_no,
			      'email',administrator.email,
			      'displayName',administrator.display_name
			    ) ORDER BY administrator.display_name,administrator.email)
			    FROM platform.domain_memberships AS administrator_membership
			    JOIN platform.users AS administrator
			      ON administrator.id=administrator_membership.user_id
			     AND administrator.tenant_id=administrator_membership.tenant_id
			    WHERE administrator_membership.tenant_id=domain.tenant_id
			      AND administrator_membership.domain_id=domain.id
			      AND administrator_membership.status='ACTIVE'
			      AND administrator_membership.member_role='DOMAIN_ADMIN'
			      AND administrator.deleted_at IS NULL
			  ),'[]'::jsonb)
			FROM platform.business_domains AS domain
			WHERE (
			    $2::boolean
			    OR EXISTS(
			      SELECT 1 FROM platform.domain_memberships AS membership
			      WHERE membership.tenant_id=domain.tenant_id
			        AND membership.domain_id=domain.id
			        AND membership.user_id=$1::uuid
			        AND membership.status='ACTIVE'
			    )
			  )
			  AND domain.deleted_at IS NULL
			ORDER BY domain.is_default DESC,domain.status,domain.name`, userID, includeAll)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var domain BusinessDomain
			var administratorsJSON []byte
			if err := rows.Scan(
				&domain.ID, &domain.Code, &domain.Name, &domain.Description,
				&domain.Status, &domain.Default, &domain.Version, &domain.CreatedAt,
				&domain.AccessSensitivity, &administratorsJSON,
			); err != nil {
				return err
			}
			if err := json.Unmarshal(administratorsJSON, &domain.Administrators); err != nil {
				return err
			}
			domains = append(domains, domain)
		}
		return rows.Err()
	})
	return domains, err
}

// CreateDomain 创建可切换的租户业务领域并记录审计事件。
func (s *AdminStore) CreateDomain(
	ctx context.Context, tenantID, actorID, code, name, description string,
	administratorUserIDs []string,
) (BusinessDomain, error) {
	var domain BusinessDomain
	code = strings.ToLower(strings.TrimSpace(code))
	name = strings.TrimSpace(name)
	description = strings.TrimSpace(description)
	if !businessDomainCodePattern.MatchString(code) {
		return domain, errors.New("domain code must start with a letter and contain 2-32 lowercase letters, numbers, _ or -")
	}
	if name == "" {
		return domain, errors.New("domain name is required")
	}
	administratorUserIDs = uniqueStrings(administratorUserIDs)
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !platformAdministrator {
			return errors.New("only a platform administrator can create domains")
		}
		var administratorCount int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT user_account.id)
			FROM platform.users AS user_account
			WHERE user_account.id=ANY($1::uuid[])
			  AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role ON role.id=assignment.role_id
			    WHERE assignment.user_id=user_account.id
			      AND role.code::text='platform_admin'
			      AND role.status='ACTIVE' AND role.deleted_at IS NULL
			  )
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.domain_memberships AS membership
			    WHERE membership.tenant_id=user_account.tenant_id
			      AND membership.user_id=user_account.id
			      AND membership.status='ACTIVE'
			      AND membership.member_role='MEMBER'
			  )`,
			administratorUserIDs,
		).Scan(&administratorCount); err != nil {
			return err
		}
		if administratorCount != len(administratorUserIDs) {
			return errors.New("domain administrators must be active users without platform or ordinary-user identity")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.business_domains(
				tenant_id,code,name,description,created_by
			) VALUES($1,$2,$3,$4,$5)
			RETURNING id,code,name,description,status,is_default,version,created_at::text,
			  access_sensitivity`,
			tenantID, code, name, description, actorID,
		).Scan(
			&domain.ID, &domain.Code, &domain.Name, &domain.Description,
			&domain.Status, &domain.Default, &domain.Version, &domain.CreatedAt,
			&domain.AccessSensitivity,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
				tenant_id,domain_id,user_id,assigned_by,member_role,status
			)
			SELECT $1,$2,administrator_id,$3,'DOMAIN_ADMIN','ACTIVE'
			FROM unnest($4::uuid[]) AS administrator_id
			ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE
			SET member_role='DOMAIN_ADMIN',status='ACTIVE',assigned_by=EXCLUDED.assigned_by`,
			tenantID, domain.ID, actorID, administratorUserIDs,
		); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'CREATE','BUSINESS_DOMAIN',$3,jsonb_build_object(
				'code',$4::text,'name',$5::text,'administratorUserIds',$6::text[]
			))`, tenantID, actorID, domain.ID, code, name, administratorUserIDs)
		return err
	})
	if err == nil {
		domains, listErr := s.ListManagedDomains(ctx, tenantID, actorID)
		if listErr == nil {
			for _, item := range domains {
				if item.ID == domain.ID {
					return item, nil
				}
			}
		}
	}
	return domain, err
}

// AssignUserDomain grants an active user access to one active domain.
func (s *AdminStore) AssignUserDomain(
	ctx context.Context, tenantID, actorID, userID, domainID string,
) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !platformAdministrator {
			return errors.New("only a platform administrator can assign domain memberships directly")
		}
		result, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
				tenant_id,domain_id,user_id,assigned_by,status,member_role
			)
			SELECT $1,domain.id,user_account.id,$2,'ACTIVE','MEMBER'
			FROM platform.business_domains AS domain
			CROSS JOIN platform.users AS user_account
			WHERE domain.id=$3::uuid
			  AND domain.status='ACTIVE'
			  AND domain.deleted_at IS NULL
			  AND user_account.id=$4::uuid
			  AND user_account.status='ACTIVE'
			  AND user_account.deleted_at IS NULL
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role ON role.id=assignment.role_id
			    WHERE assignment.user_id=user_account.id
			      AND role.code::text='platform_admin'
			      AND role.status='ACTIVE' AND role.deleted_at IS NULL
			  )
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.domain_memberships AS membership
			    WHERE membership.tenant_id=user_account.tenant_id
			      AND membership.user_id=user_account.id
			      AND membership.status='ACTIVE'
			      AND membership.member_role='DOMAIN_ADMIN'
			  )
			ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE
			SET status='ACTIVE',member_role='MEMBER',assigned_by=EXCLUDED.assigned_by`,
			tenantID, actorID, domainID, userID,
		)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("active user or domain was not found")
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'ASSIGN_DOMAIN','USER',$3,jsonb_build_object(
				'domainId',$4::text
			))`, tenantID, actorID, userID, domainID)
		return err
	})
}

// RevokeUserDomain removes one ordinary membership. Domain administrators must
// first be replaced or demoted by a platform administrator.
func (s *AdminStore) RevokeUserDomain(
	ctx context.Context, tenantID, actorID, userID, domainID string,
) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !platformAdministrator {
			return errors.New("only a platform administrator can revoke domain memberships directly")
		}
		var memberRole string
		if err := tx.QueryRow(ctx, `SELECT member_role::text
			FROM platform.domain_memberships
			WHERE tenant_id=$1::uuid AND user_id=$2::uuid AND domain_id=$3::uuid`,
			tenantID, userID, domainID,
		).Scan(&memberRole); err != nil {
			return err
		}
		if memberRole == "DOMAIN_ADMIN" {
			return errors.New("domain administrator must be replaced before membership can be revoked")
		}
		result, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
			WHERE tenant_id=$1::uuid
			  AND user_id=$2::uuid
			  AND domain_id=$3::uuid`,
			tenantID, userID, domainID,
		)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errors.New("domain membership was not found")
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.auth_sessions
			SET business_domain_id=NULL,last_used_at=now()
			WHERE user_id=$1::uuid AND business_domain_id=$2::uuid
			  AND revoked_at IS NULL`, userID, domainID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'REVOKE_DOMAIN','USER',$3,jsonb_build_object(
				'domainId',$4::text
			))`, tenantID, actorID, userID, domainID)
		return err
	})
}

// UpdateDomainStatus 启用或停用非默认领域。
// UpdateDomainAccessSensitivity changes how strictly access to a domain is
// reviewed. Only a platform administrator may change it, and the change never
// touches applications already in flight: those pinned their requirement when
// they were submitted, so a downgrade cannot retroactively drop the security
// seat from a request that is waiting on it.
func (s *AdminStore) UpdateDomainAccessSensitivity(
	ctx context.Context, tenantID, actorID, id, sensitivity string,
) (BusinessDomain, error) {
	var domain BusinessDomain
	sensitivity = strings.ToUpper(strings.TrimSpace(sensitivity))
	switch sensitivity {
	case "PUBLIC", "INTERNAL", "CONFIDENTIAL", "RESTRICTED":
	default:
		return domain, errors.New("domain access sensitivity must be PUBLIC, INTERNAL, CONFIDENTIAL or RESTRICTED")
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !platformAdministrator {
			return errors.New("only a platform administrator can update domain access sensitivity")
		}
		if err := tx.QueryRow(ctx, `UPDATE platform.business_domains
			SET access_sensitivity=$2,version=version+1
			WHERE id=$1 AND tenant_id=$3 AND deleted_at IS NULL
			RETURNING id,code,name,description,status,is_default,version,created_at::text,
			  access_sensitivity`,
			id, sensitivity, tenantID,
		).Scan(
			&domain.ID, &domain.Code, &domain.Name, &domain.Description,
			&domain.Status, &domain.Default, &domain.Version, &domain.CreatedAt,
			&domain.AccessSensitivity,
		); err != nil {
			return err
		}
		domain.Administrators = []DomainAdministrator{}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'UPDATE_DOMAIN_ACCESS_SENSITIVITY','BUSINESS_DOMAIN',$3,
				jsonb_build_object('accessSensitivity',$4::text))`,
			tenantID, actorID, id, sensitivity,
		)
		return err
	})
	return domain, err
}

func (s *AdminStore) UpdateDomainStatus(
	ctx context.Context, tenantID, actorID, id, status string,
) (BusinessDomain, error) {
	var domain BusinessDomain
	status = strings.ToUpper(strings.TrimSpace(status))
	if status != "ACTIVE" && status != "DISABLED" {
		return domain, errors.New("domain status must be ACTIVE or DISABLED")
	}
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !platformAdministrator {
			return errors.New("only a platform administrator can update domain status")
		}
		var isDefault bool
		if err := tx.QueryRow(ctx, `SELECT is_default FROM platform.business_domains
			WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, id).Scan(&isDefault); err != nil {
			return err
		}
		if isDefault && status == "DISABLED" {
			return errors.New("default domain cannot be disabled")
		}
		if err := tx.QueryRow(ctx, `UPDATE platform.business_domains
			SET status=$2,version=version+1
			WHERE id=$1 AND tenant_id=$3
			RETURNING id,code,name,description,status,is_default,version,created_at::text,
			  access_sensitivity`,
			id, status, tenantID,
		).Scan(
			&domain.ID, &domain.Code, &domain.Name, &domain.Description,
			&domain.Status, &domain.Default, &domain.Version, &domain.CreatedAt,
			&domain.AccessSensitivity,
		); err != nil {
			return err
		}
		var unboundSessions int64
		if status == "DISABLED" {
			result, err := tx.Exec(ctx, `UPDATE platform.auth_sessions
				SET business_domain_id=NULL,last_used_at=now()
				WHERE tenant_id=$1
				  AND business_domain_id=$2
				  AND revoked_at IS NULL`,
				tenantID, domain.ID,
			)
			if err != nil {
				return err
			}
			unboundSessions = result.RowsAffected()
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'UPDATE_STATUS','BUSINESS_DOMAIN',$3,jsonb_build_object(
				'status',$4::text,'unboundSessions',$5::bigint
			))`, tenantID, actorID, domain.ID, status, unboundSessions)
		return err
	})
	return domain, err
}

// IsPlatformAdministrator reports whether the actor has the active system
// platform_admin role. Domain governance never infers this from broad RBAC
// permissions because only this role may create domains or appoint admins.
func (s *AdminStore) IsPlatformAdministrator(
	ctx context.Context, tenantID, userID string,
) (allowed bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err = isPlatformAdministratorTx(ctx, tx, userID)
		return err
	})
	return allowed, err
}

func isPlatformAdministratorTx(
	ctx context.Context, tx pgx.Tx, userID string,
) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.user_roles AS assignment
		JOIN platform.roles AS role
		  ON role.id=assignment.role_id
		 AND role.tenant_id=assignment.tenant_id
		WHERE assignment.user_id=$1::uuid
		  AND role.code::text='platform_admin'
		  AND role.status='ACTIVE'
		  AND role.deleted_at IS NULL
	)`, userID).Scan(&allowed)
	return allowed, err
}

// SetPlatformAdministrator changes only the fixed platform identity. The last
// platform administrator cannot be removed, so the tenant always retains a
// control-plane owner.
func (s *AdminStore) SetPlatformAdministrator(
	ctx context.Context, tenantID, actorID, userID string, enabled bool,
) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !allowed {
			return errors.New("only a platform administrator can change the platform identity")
		}
		var activeUser bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.users
			WHERE id=$1::uuid AND status='ACTIVE' AND deleted_at IS NULL
		)`, userID).Scan(&activeUser); err != nil {
			return err
		}
		if !activeUser {
			return errors.New("active user was not found")
		}
		if enabled {
			var domainAdministrator bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
				SELECT 1 FROM platform.domain_memberships
				WHERE user_id=$1::uuid AND status='ACTIVE'
				  AND member_role='DOMAIN_ADMIN'
			)`, userID).Scan(&domainAdministrator); err != nil {
				return err
			}
			if domainAdministrator {
				return errors.New("replace the user's domain administrator identity before appointing a platform administrator")
			}
			if _, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
				WHERE tenant_id=$1::uuid AND user_id=$2::uuid`, tenantID, userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE platform.auth_sessions
				SET business_domain_id=NULL,last_used_at=now()
				WHERE tenant_id=$1::uuid AND user_id=$2::uuid
				  AND business_domain_id IS NOT NULL AND revoked_at IS NULL`, tenantID, userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(
				tenant_id,user_id,role_id,assigned_by
			)
			SELECT $1,$2,role.id,$3
			FROM platform.roles AS role
			WHERE role.code::text='platform_admin'
			  AND role.status='ACTIVE' AND role.deleted_at IS NULL
			ON CONFLICT(tenant_id,user_id,role_id) DO NOTHING`,
				tenantID, userID, actorID,
			); err != nil {
				return err
			}
		} else {
			var assigned, lastAdministrator bool
			if err := tx.QueryRow(ctx, `SELECT
				EXISTS(
				  SELECT 1 FROM platform.user_roles AS assignment
				  JOIN platform.roles AS role ON role.id=assignment.role_id
				  WHERE assignment.user_id=$1::uuid
				    AND role.code::text='platform_admin'
				    AND role.status='ACTIVE' AND role.deleted_at IS NULL
				),
				(SELECT count(*) <= 1
				 FROM platform.user_roles AS assignment
				 JOIN platform.roles AS role ON role.id=assignment.role_id
				 WHERE role.code::text='platform_admin'
				   AND role.status='ACTIVE' AND role.deleted_at IS NULL)`,
				userID,
			).Scan(&assigned, &lastAdministrator); err != nil {
				return err
			}
			if assigned && lastAdministrator {
				return errors.New("tenant must retain at least one platform administrator")
			}
			if assigned {
				if _, err := tx.Exec(ctx, `DELETE FROM platform.user_roles AS assignment
					USING platform.roles AS role
					WHERE assignment.role_id=role.id
					  AND assignment.user_id=$1::uuid
					  AND role.code::text='platform_admin'`, userID); err != nil {
					return err
				}
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'SET_PLATFORM_ADMINISTRATOR','USER',$3,
			jsonb_build_object('enabled',$4::boolean))`, tenantID, actorID, userID, enabled)
		return err
	})
}

// IsDomainAdministrator verifies an explicit active administrator designation
// for one domain. Global roles are intentionally not treated as implicit data
// access to that domain.
func (s *AdminStore) IsDomainAdministrator(
	ctx context.Context, tenantID, userID, domainID string,
) (allowed bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.domain_memberships AS membership
			JOIN platform.business_domains AS domain
			  ON domain.id=membership.domain_id
			 AND domain.tenant_id=membership.tenant_id
			WHERE membership.user_id=$1::uuid
			  AND membership.domain_id=$2::uuid
			  AND membership.status='ACTIVE'
			  AND membership.member_role='DOMAIN_ADMIN'
			  AND domain.status='ACTIVE'
			  AND domain.deleted_at IS NULL
		)`, userID, domainID).Scan(&allowed)
	})
	return allowed, err
}

// ListDomainCatalog exposes only active domain metadata plus the caller's
// membership/application state. It never exposes resources from those domains.
func (s *AdminStore) ListDomainCatalog(
	ctx context.Context, tenantID, userID string,
) (items []DomainCatalogItem, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT
			domain.id,domain.code,domain.name,domain.description,domain.status,
			domain.is_default,domain.version,domain.created_at::text,
			CASE
			  WHEN EXISTS(
			    SELECT 1
			    FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role
			      ON role.tenant_id=assignment.tenant_id
			     AND role.id=assignment.role_id
			    WHERE assignment.tenant_id=domain.tenant_id
			      AND assignment.user_id=$1::uuid
			      AND role.code='platform_admin'
			      AND role.status='ACTIVE'
			      AND role.deleted_at IS NULL
			  ) THEN 'PLATFORM_ADMIN'
			  WHEN membership.user_id IS NOT NULL THEN membership.member_role::text
			  WHEN application.status IS NOT NULL THEN application.status::text
			  ELSE 'AVAILABLE'
			END,
			COALESCE((
			  SELECT jsonb_agg(jsonb_build_object(
			    'id',administrator.id,'employeeNo',administrator.employee_no,
			    'email',administrator.email,
			    'displayName',administrator.display_name
			  ) ORDER BY administrator.display_name,administrator.email)
			  FROM platform.domain_memberships AS administrator_membership
			  JOIN platform.users AS administrator
			    ON administrator.id=administrator_membership.user_id
			   AND administrator.tenant_id=administrator_membership.tenant_id
			  WHERE administrator_membership.tenant_id=domain.tenant_id
			    AND administrator_membership.domain_id=domain.id
			    AND administrator_membership.status='ACTIVE'
			    AND administrator_membership.member_role='DOMAIN_ADMIN'
			    AND administrator.deleted_at IS NULL
			),'[]'::jsonb)
		FROM platform.business_domains AS domain
		LEFT JOIN platform.domain_memberships AS membership
		  ON membership.tenant_id=domain.tenant_id
		 AND membership.domain_id=domain.id
		 AND membership.user_id=$1::uuid
		 AND membership.status='ACTIVE'
		LEFT JOIN LATERAL (
		  SELECT request.status
		  FROM platform.domain_access_applications AS request
		  WHERE request.tenant_id=domain.tenant_id
		    AND request.domain_id=domain.id
		    AND request.applicant_user_id=$1::uuid
		  ORDER BY request.created_at DESC
		  LIMIT 1
		) AS application ON true
		WHERE domain.status='ACTIVE' AND domain.deleted_at IS NULL
		ORDER BY domain.is_default DESC,domain.name`, userID)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DomainCatalogItem
			var administratorsJSON []byte
			item.Administrators = []DomainAdministrator{}
			if scanErr := rows.Scan(
				&item.ID, &item.Code, &item.Name, &item.Description, &item.Status,
				&item.Default, &item.Version, &item.CreatedAt, &item.AccessStatus,
				&administratorsJSON,
			); scanErr != nil {
				return scanErr
			}
			if unmarshalErr := json.Unmarshal(administratorsJSON, &item.Administrators); unmarshalErr != nil {
				return unmarshalErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// ApplyDomainAccess creates one pending self-service membership request.
func (s *AdminStore) ApplyDomainAccess(
	ctx context.Context, tenantID, applicantID, domainID, reason string,
) (DomainApplication, error) {
	reason = strings.TrimSpace(reason)
	if len([]byte(reason)) > 1000 {
		return DomainApplication{}, errors.New("application reason is too long")
	}
	var application DomainApplication
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var id string
		// requires_dual_approval and the review SLA are pinned from the domain at
		// submission time: reading them at decision time would let a sensitivity
		// downgrade retroactively weaken a request that is already in flight.
		err := tx.QueryRow(ctx, `INSERT INTO platform.domain_access_applications(
				tenant_id,domain_id,applicant_user_id,reason,
				requires_dual_approval,sla_due_at
			)
			SELECT $1,domain.id,$2,$3,
			  domain.access_sensitivity IN ('CONFIDENTIAL','RESTRICTED'),
			  now()+interval '24 hours'
			FROM platform.business_domains AS domain
			JOIN platform.users AS applicant
			  ON applicant.tenant_id=domain.tenant_id AND applicant.id=$2::uuid
			WHERE domain.id=$4::uuid
			  AND domain.status='ACTIVE' AND domain.deleted_at IS NULL
			  AND applicant.status='ACTIVE' AND applicant.deleted_at IS NULL
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role ON role.id=assignment.role_id
			    WHERE assignment.user_id=applicant.id
			      AND role.code::text='platform_admin'
			      AND role.status='ACTIVE' AND role.deleted_at IS NULL
			  )
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.domain_memberships AS membership
			    WHERE membership.tenant_id=domain.tenant_id
			      AND membership.domain_id=domain.id
			      AND membership.user_id=applicant.id
			      AND membership.status='ACTIVE'
			  )
			ON CONFLICT(tenant_id,domain_id,applicant_user_id)
			  WHERE status='PENDING'
			DO UPDATE SET reason=EXCLUDED.reason
			RETURNING id::text`, tenantID, applicantID, reason, domainID).Scan(&id)
		// A merged duplicate keeps its original seats and SLA; only the stated
		// purpose is refreshed.
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("active domain was not found or user is already a member")
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'APPLY_DOMAIN','DOMAIN_APPLICATION',$3,
				jsonb_build_object('domainId',$4::text))`,
			tenantID, applicantID, id, domainID,
		)
		return err
	})
	if err != nil {
		return application, err
	}
	items, err := s.ListMyDomainApplications(ctx, tenantID, applicantID)
	if err != nil {
		return application, err
	}
	for _, item := range items {
		if item.DomainID == domainID && item.Status == "PENDING" {
			return item, nil
		}
	}
	return application, errors.New("created domain application was not found")
}

// WithdrawDomainApplication lets only the applicant cancel a pending request.
// Rejected and cancelled requests remain immutable audit history; a later
// application creates a new row, while concurrent duplicate pending submits
// are merged by ApplyDomainAccess.
func (s *AdminStore) WithdrawDomainApplication(
	ctx context.Context, tenantID, applicantID, applicationID string,
) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var domainID string
		err := tx.QueryRow(ctx, `UPDATE platform.domain_access_applications
			SET status='CANCELLED'
			WHERE tenant_id=$1::uuid AND id=$2::uuid
			  AND applicant_user_id=$3::uuid AND status='PENDING'
			RETURNING domain_id::text`, tenantID, applicationID, applicantID).Scan(&domainID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("pending domain application was not found")
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'WITHDRAW_DOMAIN_APPLICATION','DOMAIN_APPLICATION',$3,
				jsonb_build_object('domainId',$4::text))`,
			tenantID, applicantID, applicationID, domainID,
		)
		return err
	})
}

// ListMyDomainApplications returns the caller's request history.
func (s *AdminStore) ListMyDomainApplications(
	ctx context.Context, tenantID, userID string,
) ([]DomainApplication, error) {
	return s.listDomainApplications(ctx, tenantID, userID, "", false)
}

// ListPendingDomainApplications returns one domain's administrator queue.
func (s *AdminStore) ListPendingDomainApplications(
	ctx context.Context, tenantID, actorID, domainID string,
) ([]DomainApplication, error) {
	allowed, err := s.IsDomainAdministrator(ctx, tenantID, actorID, domainID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errors.New("domain administrator permission is required")
	}
	return s.listDomainApplications(ctx, tenantID, actorID, domainID, true)
}

func (s *AdminStore) listDomainApplications(
	ctx context.Context, tenantID, userID, domainID string, pendingOnly bool,
) (items []DomainApplication, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT
			application.id::text,domain.id::text,domain.code::text,domain.name,
			applicant.id::text,applicant.email::text,applicant.display_name,
			application.status::text,application.reason,application.review_comment,
			COALESCE(application.reviewed_by::text,''),
			COALESCE(application.reviewed_at::text,''),application.created_at::text,
			domain.access_sensitivity,application.requires_dual_approval,
			application.sla_due_at,application.escalation_level,
			COALESCE((
			  SELECT jsonb_agg(jsonb_build_object(
			      'reviewerId',approval.reviewer_id::text,
			      'reviewerDisplayName',reviewer.display_name,
			      'reviewRole',approval.review_role,'decision',approval.decision,
			      'comment',approval.comment,'createdAt',approval.created_at::text
			    ) ORDER BY approval.created_at)
			  FROM platform.domain_access_approvals AS approval
			  JOIN platform.users AS reviewer
			    ON reviewer.id=approval.reviewer_id AND reviewer.tenant_id=approval.tenant_id
			  WHERE approval.application_id=application.id
			),'[]'::jsonb),
			COALESCE((
			  SELECT jsonb_agg(jsonb_build_object(
			      'level',escalation.level,'escalatedBy',escalation.escalated_by::text,
			      'note',escalation.note,'createdAt',escalation.created_at::text
			    ) ORDER BY escalation.level)
			  FROM platform.domain_access_escalations AS escalation
			  WHERE escalation.application_id=application.id
			),'[]'::jsonb)
		FROM platform.domain_access_applications AS application
		JOIN platform.business_domains AS domain
		  ON domain.id=application.domain_id AND domain.tenant_id=application.tenant_id
		JOIN platform.users AS applicant
		  ON applicant.id=application.applicant_user_id
		 AND applicant.tenant_id=application.tenant_id
		WHERE ($2::uuid IS NULL OR application.domain_id=$2::uuid)
		  AND (NOT $3::boolean OR application.status='PENDING')
		  AND ($2::uuid IS NOT NULL OR application.applicant_user_id=$1::uuid)
		ORDER BY application.created_at DESC`, userID, nullableUUID(domainID), pendingOnly)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item DomainApplication
			var slaDueAt *time.Time
			var approvalsJSON, escalationsJSON []byte
			if scanErr := rows.Scan(
				&item.ID, &item.DomainID, &item.DomainCode, &item.DomainName,
				&item.ApplicantUserID, &item.ApplicantEmail,
				&item.ApplicantDisplayName, &item.Status, &item.Reason,
				&item.ReviewComment, &item.ReviewedBy, &item.ReviewedAt,
				&item.CreatedAt, &item.DomainSensitivity, &item.RequiresDualApproval,
				&slaDueAt, &item.EscalationLevel, &approvalsJSON, &escalationsJSON,
			); scanErr != nil {
				return scanErr
			}
			item.Approvals = []DomainAccessApproval{}
			item.Escalations = []DomainAccessEscalation{}
			if err := json.Unmarshal(approvalsJSON, &item.Approvals); err != nil {
				return err
			}
			if err := json.Unmarshal(escalationsJSON, &item.Escalations); err != nil {
				return err
			}
			if slaDueAt != nil {
				item.SLADueAt = slaDueAt.UTC().Format(time.RFC3339Nano)
			}
			item.SLAStatus = domainApplicationSLAStatus(item.Status, slaDueAt)
			item.ApprovedRoles = []string{}
			item.OpenSeats = []string{}
			for _, approval := range item.Approvals {
				if approval.Decision == "APPROVED" {
					item.ApprovedRoles = append(item.ApprovedRoles, approval.ReviewRole)
				}
				if approval.ReviewerID == userID {
					item.ActorApproval = approval.Decision
				}
			}
			// The seats still to be filled are derived here so the queue can show
			// a reviewer what is actually being waited on.
			if item.Status == "PENDING" {
				required := []string{domainReviewRoleOwner}
				if item.RequiresDualApproval {
					required = append(required, domainReviewRoleSecurity)
				}
				for _, seat := range required {
					if !containsString(item.ApprovedRoles, seat) {
						item.OpenSeats = append(item.OpenSeats, seat)
					}
				}
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

// ReviewDomainApplication atomically decides a pending request and creates an
// ordinary active membership only on approval.
func (s *AdminStore) ReviewDomainApplication(
	ctx context.Context, tenantID, reviewerID, applicationID, decision, comment, reviewRole string,
) error {
	decision = strings.ToUpper(strings.TrimSpace(decision))
	comment = strings.TrimSpace(comment)
	reviewRole = strings.ToUpper(strings.TrimSpace(reviewRole))
	if decision != "APPROVED" && decision != "REJECTED" {
		return errors.New("decision must be APPROVED or REJECTED")
	}
	if len([]byte(comment)) > 1000 {
		return errors.New("review comment is too long")
	}
	if reviewRole == "" {
		reviewRole = domainReviewRoleOwner
	}
	if !validDomainReviewRole(reviewRole) {
		return ErrDomainApplicationSeatInvalid
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var domainID, applicantID, status string
		var requiresDualApproval bool
		if err := tx.QueryRow(ctx, `SELECT domain_id::text,applicant_user_id::text,status::text,
			requires_dual_approval
			FROM platform.domain_access_applications
			WHERE id=$1::uuid FOR UPDATE`, applicationID,
		).Scan(&domainID, &applicantID, &status, &requiresDualApproval); err != nil {
			return err
		}
		if status != "PENDING" {
			return errors.New("domain application has already been decided")
		}
		if !requiresDualApproval && reviewRole != domainReviewRoleOwner {
			// A standard domain has exactly one seat. Accepting a SECURITY
			// signature here would leave the request pending forever, because the
			// owner seat it is waiting for would still be empty.
			return ErrDomainApplicationSeatInvalid
		}
		if reviewerID == applicantID {
			return ErrDomainApplicationSelfCosign
		}
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, reviewerID)
		if err != nil {
			return err
		}
		var domainAdministrator bool
		if err := tx.QueryRow(ctx, `SELECT platform.user_is_domain_administrator($1::uuid)`,
			domainID,
		).Scan(&domainAdministrator); err != nil {
			return err
		}
		if err := validateDomainApplicationReviewer(
			platformAdministrator, domainAdministrator,
		); err != nil {
			return err
		}
		// The security seat is the independent check, so it cannot be filled by
		// the same administrators who run the domain day to day.
		if reviewRole == domainReviewRoleSecurity && !platformAdministrator {
			return ErrDomainApplicationSecurityReviewRequired
		}
		// Append the receipt first. The unique constraints on (application,
		// reviewer) and (application, role) are what actually enforce separation
		// of duties, so a concurrent second signature loses the race here rather
		// than silently completing the approval.
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_access_approvals(
				tenant_id,application_id,reviewer_id,review_role,decision,comment
			) VALUES($1,$2::uuid,$3::uuid,$4,$5,$6)`,
			tenantID, applicationID, reviewerID, reviewRole, decision, comment,
		); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) && postgresError.Code == "23505" {
				if strings.Contains(postgresError.ConstraintName, "one_seat_per_reviewer") {
					return ErrDomainApplicationSelfCosign
				}
				return ErrDomainApplicationSeatTaken
			}
			return err
		}
		// One rejection in either seat is decisive; approval needs every required
		// seat filled.
		decided := decision == "REJECTED"
		if decision == "APPROVED" {
			if !requiresDualApproval {
				decided = true
			} else {
				var approvedSeats int
				if err := tx.QueryRow(ctx, `SELECT count(DISTINCT review_role)
					FROM platform.domain_access_approvals
					WHERE tenant_id=$1::uuid AND application_id=$2::uuid AND decision='APPROVED'`,
					tenantID, applicationID).Scan(&approvedSeats); err != nil {
					return err
				}
				decided = approvedSeats == 2
			}
		}
		var applicantPlatformAdministrator, applicantDomainAdministrator, applicantTargetDomainAdministrator bool
		if decision == "APPROVED" {
			if err := tx.QueryRow(ctx, `SELECT
				EXISTS(
				  SELECT 1 FROM platform.user_roles AS assignment
				  JOIN platform.roles AS role ON role.id=assignment.role_id
				  WHERE assignment.user_id=$1::uuid
				    AND role.code::text='platform_admin'
				    AND role.status='ACTIVE' AND role.deleted_at IS NULL
				),
				EXISTS(
				  SELECT 1 FROM platform.domain_memberships
				  WHERE user_id=$1::uuid AND status='ACTIVE'
				    AND member_role='DOMAIN_ADMIN'
				),
				EXISTS(
				  SELECT 1 FROM platform.domain_memberships
				  WHERE user_id=$1::uuid AND domain_id=$2::uuid
				    AND status='ACTIVE' AND member_role='DOMAIN_ADMIN'
				)`, applicantID, domainID).Scan(
				&applicantPlatformAdministrator,
				&applicantDomainAdministrator,
				&applicantTargetDomainAdministrator,
			); err != nil {
				return err
			}
			if applicantDomainAdministrator && !applicantTargetDomainAdministrator && !platformAdministrator {
				return ErrDomainApplicationPlatformReviewRequired
			}
		}
		if !decided {
			// The first seat of a sensitive-domain approval only records its
			// receipt. The request stays PENDING and grants nothing until the
			// second seat signs.
			_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
					tenant_id,actor_user_id,action,resource_type,resource_id,detail
				) VALUES($1,$2,'COSIGN_DOMAIN_APPLICATION','DOMAIN_APPLICATION',$3,
					jsonb_build_object('domainId',$4::text,'applicantUserId',$5::text,
					'decision',$6::text,'reviewRole',$7::text))`,
				tenantID, reviewerID, applicationID, domainID, applicantID, decision, reviewRole,
			)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.domain_access_applications
			SET status=$2::platform.domain_application_status,review_comment=$3,
			    reviewed_by=$4::uuid,reviewed_at=now()
			WHERE id=$1::uuid`, applicationID, decision, comment, reviewerID); err != nil {
			return err
		}
		if decision == "APPROVED" && !applicantPlatformAdministrator {
			memberRole := "MEMBER"
			if applicantDomainAdministrator {
				memberRole = "DOMAIN_ADMIN"
			}
			if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
					tenant_id,domain_id,user_id,assigned_by,status,member_role
				) VALUES($1,$2,$3,$4,'ACTIVE',$5::platform.domain_member_role)
				ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE
				SET status='ACTIVE',member_role=EXCLUDED.member_role,assigned_by=EXCLUDED.assigned_by`,
				tenantID, domainID, applicantID, reviewerID, memberRole,
			); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'REVIEW_DOMAIN_APPLICATION','DOMAIN_APPLICATION',$3,
				jsonb_build_object('domainId',$4::text,'applicantUserId',$5::text,
				'decision',$6::text,'reviewRole',$7::text,
				'requiresDualApproval',$8::boolean))`,
			tenantID, reviewerID, applicationID, domainID, applicantID, decision,
			reviewRole, requiresDualApproval,
		)
		return err
	})
}

// EscalateDomainApplication records that a pending request has passed its review
// SLA and raises it one level, up to three.
//
// Escalation deliberately does not decide anything: an overdue sensitive-domain
// request must still collect both signatures, because a review that grants
// itself by timing out is not a review. The ledger exists so an unattended
// request becomes visible instead of sitting silently in a queue.
func (s *AdminStore) EscalateDomainApplication(
	ctx context.Context, tenantID, actorID, applicationID, note string,
) (int, error) {
	note = strings.TrimSpace(note)
	if len([]byte(note)) > 1000 {
		return 0, errors.New("escalation note is too long")
	}
	level := 0
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var domainID, status string
		var slaDueAt *time.Time
		var currentLevel int
		if err := tx.QueryRow(ctx, `SELECT domain_id::text,status::text,sla_due_at,escalation_level
			FROM platform.domain_access_applications
			WHERE tenant_id=$1::uuid AND id=$2::uuid FOR UPDATE`, tenantID, applicationID).
			Scan(&domainID, &status, &slaDueAt, &currentLevel); err != nil {
			return err
		}
		if status != "PENDING" || slaDueAt == nil || currentLevel >= 3 || time.Now().Before(*slaDueAt) {
			return ErrDomainApplicationEscalationInvalid
		}
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		var domainAdministrator bool
		if err := tx.QueryRow(ctx, `SELECT platform.user_is_domain_administrator($1::uuid)`,
			domainID).Scan(&domainAdministrator); err != nil {
			return err
		}
		if err := validateDomainApplicationReviewer(
			platformAdministrator, domainAdministrator,
		); err != nil {
			return err
		}
		level = currentLevel + 1
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_access_escalations(
				tenant_id,application_id,level,escalated_by,note
			) VALUES($1,$2::uuid,$3,$4::uuid,$5)`,
			tenantID, applicationID, level, actorID, note); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) && postgresError.Code == "23505" {
				return ErrDomainApplicationEscalationInvalid
			}
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.domain_access_applications
			SET escalation_level=$2 WHERE tenant_id=$1::uuid AND id=$3::uuid`,
			tenantID, level, applicationID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'ESCALATE_DOMAIN_APPLICATION','DOMAIN_APPLICATION',$3,
				jsonb_build_object('domainId',$4::text,'level',$5::int))`,
			tenantID, actorID, applicationID, domainID, level,
		)
		return err
	})
	return level, err
}

// ReplaceDomainAdministrators replaces the explicit administrator set. Removed
// administrators become unassigned unless they administer another domain; they
// are never silently converted into ordinary users.
func (s *AdminStore) ReplaceDomainAdministrators(
	ctx context.Context, tenantID, actorID, domainID string, userIDs []string,
) error {
	userIDs = uniqueStrings(userIDs)
	if len(userIDs) == 0 {
		return errors.New("domain must retain at least one administrator")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		platformAdministrator, err := isPlatformAdministratorTx(ctx, tx, actorID)
		if err != nil {
			return err
		}
		if !platformAdministrator {
			return errors.New("only a platform administrator can appoint domain administrators")
		}
		var validCount int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT user_account.id)
			FROM platform.users AS user_account
			WHERE user_account.id=ANY($1::uuid[])
			  AND user_account.status='ACTIVE' AND user_account.deleted_at IS NULL
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role ON role.id=assignment.role_id
			    WHERE assignment.user_id=user_account.id
			      AND role.code::text='platform_admin'
			      AND role.status='ACTIVE' AND role.deleted_at IS NULL
			  )
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.domain_memberships AS membership
			    WHERE membership.tenant_id=user_account.tenant_id
			      AND membership.user_id=user_account.id
			      AND membership.status='ACTIVE'
			      AND membership.member_role='MEMBER'
			      AND membership.domain_id<>$2::uuid
			  )`,
			userIDs, domainID,
		).Scan(&validCount); err != nil {
			return err
		}
		if validCount != len(userIDs) {
			return errors.New("domain administrators must be active users without platform identity or ordinary membership in another domain")
		}
		var domainExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.business_domains
			WHERE id=$1::uuid AND deleted_at IS NULL
		)`, domainID).Scan(&domainExists); err != nil {
			return err
		}
		if !domainExists {
			return errors.New("domain was not found")
		}
		if _, err := tx.Exec(ctx, `DELETE FROM platform.domain_memberships
			WHERE domain_id=$1::uuid AND member_role='DOMAIN_ADMIN'
			  AND NOT(user_id=ANY($2::uuid[]))`, domainID, userIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.auth_sessions AS session
			SET business_domain_id=NULL,last_used_at=now()
			WHERE session.tenant_id=$1::uuid
			  AND session.business_domain_id=$2::uuid
			  AND session.revoked_at IS NULL
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.domain_memberships AS membership
			    WHERE membership.tenant_id=session.tenant_id
			      AND membership.domain_id=$2::uuid
			      AND membership.user_id=session.user_id
			      AND membership.status='ACTIVE'
			  )
			  AND NOT EXISTS(
			    SELECT 1 FROM platform.user_roles AS assignment
			    JOIN platform.roles AS role ON role.id=assignment.role_id
			    WHERE assignment.tenant_id=session.tenant_id
			      AND assignment.user_id=session.user_id
			      AND role.code::text='platform_admin'
			      AND role.status='ACTIVE' AND role.deleted_at IS NULL
			  )`,
			tenantID, domainID,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.domain_memberships(
				tenant_id,domain_id,user_id,assigned_by,status,member_role
			)
			SELECT $1,$2,user_id,$3,'ACTIVE','DOMAIN_ADMIN'
			FROM unnest($4::uuid[]) AS user_id
			ON CONFLICT(tenant_id,domain_id,user_id) DO UPDATE
			SET status='ACTIVE',member_role='DOMAIN_ADMIN',assigned_by=EXCLUDED.assigned_by`,
			tenantID, domainID, actorID, userIDs,
		); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
				tenant_id,actor_user_id,action,resource_type,resource_id,detail
			) VALUES($1,$2,'REPLACE_ADMINISTRATORS','BUSINESS_DOMAIN',$3,
				jsonb_build_object('administratorUserIds',$4::text[]))`,
			tenantID, actorID, domainID, userIDs,
		)
		return err
	})
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
