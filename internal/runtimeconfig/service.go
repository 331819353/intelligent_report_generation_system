package runtimeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type AdminAuthorizer interface {
	IsPlatformAdministrator(context.Context, string, string) (bool, error)
}
type Service struct {
	pool   *pgxpool.Pool
	admins AdminAuthorizer
	now    func() time.Time
}

func NewService(pool *pgxpool.Pool, admins AdminAuthorizer) (*Service, error) {
	if pool == nil || admins == nil {
		return nil, errors.New("runtime config dependencies are incomplete")
	}
	return &Service{pool: pool, admins: admins, now: time.Now}, nil
}
func DeploymentParameters() []DeploymentParameter {
	// ChangeGuidance 会原样显示在运行配置中心的卡片上，因此和其余界面文案一样使用中文。
	definitions := []struct{ name, category, env, guidance string }{{"database.controlPlane", "DEPLOYMENT_PARAMETER", "DATABASE_URL", "通过部署系统修改，并重启受影响的服务"}, {"warehouse.database", "DEPLOYMENT_PARAMETER", "WAREHOUSE_DATABASE_URL", "通过部署系统修改，并重启 Worker"}, {"objectStorage.endpoint", "DEPLOYMENT_PARAMETER", "MINIO_ENDPOINT", "通过部署系统修改"}, {"auth.accessSigningSecret", "SECRET_REFERENCE", "AUTH_ACCESS_SECRET", "通过密钥管理系统轮换；平台内不会读取或展示明文"}, {"dataSource.encryptionKey", "SECRET_REFERENCE", "DATA_SOURCE_CREDENTIAL_KEY", "通过密钥管理系统轮换，并遵循既定的密钥轮换手册"}}
	result := make([]DeploymentParameter, 0, len(definitions))
	for _, definition := range definitions {
		_, configured := os.LookupEnv(definition.env)
		result = append(result, DeploymentParameter{Name: definition.name, Category: definition.category, Configured: configured, MutableOnline: false, ChangeGuidance: definition.guidance})
	}
	return result
}
func (s *Service) Create(ctx context.Context, tenant, actor string, input CreateInput) (Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return Version{}, e
	}
	input.ScopeType = upper(input.ScopeType)
	if ValidateScope(input.ScopeType, input.ScopeID) != nil || !safeImpact(input.ImpactSummary) {
		return Version{}, ErrInvalid
	}
	canonical, hash, compatibility, e := ValidateConfig(input.ScopeType, input.Config)
	if e != nil {
		return Version{}, e
	}
	input.Config = canonical
	var result Version
	now := s.clock()
	e = database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		var versionNo int
		if e := tx.QueryRow(ctx, `SELECT COALESCE(max(version_no),0)+1 FROM platform.runtime_config_versions WHERE tenant_id=$1 AND scope_type=$2 AND scope_id=$3`, tenant, input.ScopeType, input.ScopeID).Scan(&versionNo); e != nil {
			return e
		}
		if input.BaseVersionID != "" {
			var valid bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2 AND scope_type=$3 AND scope_id=$4 AND state IN('ACTIVE','SUPERSEDED'))`, tenant, input.BaseVersionID, input.ScopeType, input.ScopeID).Scan(&valid); e != nil {
				return e
			}
			if !valid {
				return ErrConflict
			}
		}
		return scanVersion(tx.QueryRow(ctx, `INSERT INTO platform.runtime_config_versions(id,tenant_id,scope_type,scope_id,version_no,base_version_id,config_json,config_hash,state,compatibility,impact_summary,created_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,NULLIF($6::text,'')::uuid,$7,$8,'DRAFT',$9,$10,$11,$12,$12) RETURNING id::text,scope_type,scope_id,version_no,COALESCE(base_version_id::text,''),config_json,config_hash,state,compatibility,impact_summary,created_by::text,COALESCE(approved_by::text,''),record_version,created_at,updated_at,submitted_at,approved_at,activated_at,COALESCE(rejected_by::text,''),rejected_at,rejection_reason`, uuid.NewString(), tenant, input.ScopeType, input.ScopeID, versionNo, input.BaseVersionID, input.Config, hash, compatibility, input.ImpactSummary, actor, now), &result)
	})
	return result, mapError(e)
}
func (s *Service) List(ctx context.Context, tenant, actor string, limit int) ([]Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return nil, e
	}
	if limit < 1 || limit > 200 {
		return nil, ErrInvalid
	}
	items := []Version{}
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT id::text,scope_type,scope_id,version_no,COALESCE(base_version_id::text,''),config_json,config_hash,state,compatibility,impact_summary,created_by::text,COALESCE(approved_by::text,''),record_version,created_at,updated_at,submitted_at,approved_at,activated_at,COALESCE(rejected_by::text,''),rejected_at,rejection_reason FROM platform.runtime_config_versions WHERE tenant_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2`, tenant, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v Version
			if e = scanVersion(rows, &v); e != nil {
				return e
			}
			items = append(items, v)
		}
		return rows.Err()
	})
	return items, mapError(e)
}
func (s *Service) Get(ctx context.Context, tenant, actor, id string) (Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return Version{}, e
	}
	return s.getSystem(ctx, tenant, id)
}
func (s *Service) getSystem(ctx context.Context, tenant, id string) (Version, error) {
	if !canonicalUUID(id) {
		return Version{}, ErrInvalid
	}
	var result Version
	result.Nodes = []RolloutNode{}
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		if e := scanVersion(tx.QueryRow(ctx, `SELECT id::text,scope_type,scope_id,version_no,COALESCE(base_version_id::text,''),config_json,config_hash,state,compatibility,impact_summary,created_by::text,COALESCE(approved_by::text,''),record_version,created_at,updated_at,submitted_at,approved_at,activated_at,COALESCE(rejected_by::text,''),rejected_at,rejection_reason FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2`, tenant, id), &result); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT id::text,consumer_type,ordinal,state,expected_hash,applied_hash,failure_code,attempt,applied_at FROM platform.runtime_config_rollout_nodes WHERE tenant_id=$1 AND version_id=$2 ORDER BY ordinal,id`, tenant, id)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var node RolloutNode
			if e = rows.Scan(&node.ID, &node.ConsumerType, &node.Ordinal, &node.State, &node.ExpectedHash, &node.AppliedHash, &node.FailureCode, &node.Attempt, &node.AppliedAt); e != nil {
				return e
			}
			result.Nodes = append(result.Nodes, node)
		}
		return rows.Err()
	})
	return result, mapError(e)
}
func (s *Service) Submit(ctx context.Context, tenant, actor, id string, input VersionInput) (Version, error) {
	return s.transition(ctx, tenant, actor, id, input, "DRAFT", "IN_REVIEW", false)
}
func (s *Service) Approve(ctx context.Context, tenant, actor, id string, input VersionInput) (Version, error) {
	return s.transition(ctx, tenant, actor, id, input, "IN_REVIEW", "APPROVED", true)
}
func (s *Service) Reject(ctx context.Context, tenant, actor, id string, input RejectInput) (Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return Version{}, e
	}
	if !canonicalUUID(id) || input.ExpectedVersion < 1 || !safeReason(input.Reason) {
		return Version{}, ErrInvalid
	}
	now := s.clock()
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='REJECTED',rejected_by=$1,rejected_at=$2,rejection_reason=$3,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$4 AND id=$5 AND state='IN_REVIEW' AND record_version=$6 AND created_by<>$1`, actor, now, input.Reason, tenant, id, input.ExpectedVersion)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `INSERT INTO platform.runtime_config_events(id,tenant_id,version_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,'REJECTED',$4,jsonb_build_object('reason',$5::text),$6)`, uuid.NewString(), tenant, id, actor, input.Reason, now)
		return e
	})
	if e != nil {
		return Version{}, mapError(e)
	}
	return s.getSystem(ctx, tenant, id)
}
func (s *Service) transition(ctx context.Context, tenant, actor, id string, input VersionInput, from, to string, approval bool) (Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return Version{}, e
	}
	if !canonicalUUID(id) || input.ExpectedVersion < 1 {
		return Version{}, ErrInvalid
	}
	now := s.clock()
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		query := `UPDATE platform.runtime_config_versions SET state=$1,record_version=record_version+1,updated_at=$2,submitted_at=CASE WHEN $1='IN_REVIEW' THEN $2 ELSE submitted_at END WHERE tenant_id=$3 AND id=$4 AND state=$5 AND record_version=$6`
		args := []any{to, now, tenant, id, from, input.ExpectedVersion}
		if approval {
			query = `UPDATE platform.runtime_config_versions SET state='APPROVED',approved_by=$1,approved_at=$2,record_version=record_version+1,updated_at=$2 WHERE tenant_id=$3 AND id=$4 AND state='IN_REVIEW' AND record_version=$5 AND created_by<>$1`
			args = []any{actor, now, tenant, id, input.ExpectedVersion}
		}
		tag, e := tx.Exec(ctx, query, args...)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `INSERT INTO platform.runtime_config_events(id,tenant_id,version_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,$4,$5,'{}',$6)`, uuid.NewString(), tenant, id, to, actor, now)
		return e
	})
	if e != nil {
		return Version{}, mapError(e)
	}
	return s.getSystem(ctx, tenant, id)
}
func (s *Service) Apply(ctx context.Context, tenant, actor, id string, input VersionInput) (Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return Version{}, e
	}
	if !canonicalUUID(id) || input.ExpectedVersion < 1 {
		return Version{}, ErrInvalid
	}
	now := s.clock()
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		var hash, scope, scopeID string
		var compatibility string
		if e := tx.QueryRow(ctx, `SELECT config_hash,scope_type,scope_id,compatibility FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2 AND state='APPROVED' AND record_version=$3 FOR UPDATE`, tenant, id, input.ExpectedVersion).Scan(&hash, &scope, &scopeID, &compatibility); e != nil {
			return ErrConflict
		}
		consumers := []string{"API", "ASKDATA_WORKER", "REPORT_WORKER"}
		if scope == "WORKER" {
			if scopeID != "API" && scopeID != "WORKER" && scopeID != "ASKDATA_WORKER" && scopeID != "REPORT_WORKER" {
				return ErrInvalid
			}
			consumers = []string{scopeID}
			if scopeID == "WORKER" {
				consumers = []string{"WORKER"}
			}
		}
		for index, consumer := range consumers {
			if _, e := tx.Exec(ctx, `INSERT INTO platform.runtime_config_rollout_nodes(id,tenant_id,version_id,ordinal,consumer_type,state,expected_hash,created_at,updated_at) VALUES($1,$2,$3,$4,$5,'PENDING',$6,$7,$7)`, uuid.NewString(), tenant, id, index+1, consumer, hash, now); e != nil {
				return e
			}
		}
		tag, e := tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='ROLLING_OUT',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3 AND state='APPROVED' AND record_version=$4`, now, tenant, id, input.ExpectedVersion)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, e = tx.Exec(ctx, `INSERT INTO platform.runtime_config_events(id,tenant_id,version_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,'ROLLOUT_STARTED',$4,jsonb_build_object('compatibility',$5::text),$6)`, uuid.NewString(), tenant, id, actor, compatibility, now)
		return e
	})
	if e != nil {
		return Version{}, mapError(e)
	}
	return s.getSystem(ctx, tenant, id)
}
func (s *Service) AcknowledgeRestart(ctx context.Context, tenant, actor, id, nodeID string) (Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return Version{}, e
	}
	if !canonicalUUID(id) || !canonicalUUID(nodeID) {
		return Version{}, ErrInvalid
	}
	now := s.clock()
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		tag, e := tx.Exec(ctx, `UPDATE platform.runtime_config_rollout_nodes SET state='APPLIED',applied_hash=expected_hash,applied_at=$1,updated_at=$1 WHERE tenant_id=$2 AND version_id=$3 AND id=$4 AND state='WAITING_RESTART'`, now, tenant, id, nodeID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return activateIfComplete(ctx, tx, tenant, id, now)
	})
	if e != nil {
		return Version{}, mapError(e)
	}
	return s.getSystem(ctx, tenant, id)
}
func (s *Service) Rollback(ctx context.Context, tenant, actor, id string, input VersionInput) (Version, error) {
	if e := s.authorize(ctx, tenant, actor); e != nil {
		return Version{}, e
	}
	if !canonicalUUID(id) || input.ExpectedVersion < 1 {
		return Version{}, ErrInvalid
	}
	now := s.clock()
	var target string
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		var scope, scopeID string
		if e := tx.QueryRow(ctx, `SELECT COALESCE(base_version_id::text,''),scope_type,scope_id FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2 AND state='ACTIVE' AND record_version=$3 FOR UPDATE`, tenant, id, input.ExpectedVersion).Scan(&target, &scope, &scopeID); e != nil {
			return ErrConflict
		}
		if target == "" {
			return ErrConflict
		}
		var valid bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.runtime_config_versions WHERE tenant_id=$1 AND id=$2 AND state IN('SUPERSEDED','ACTIVE'))`, tenant, target).Scan(&valid); e != nil || !valid {
			return ErrConflict
		}
		if _, e := tx.Exec(ctx, `INSERT INTO platform.runtime_config_effective(tenant_id,scope_type,scope_id,version_id,updated_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,scope_type,scope_id) DO UPDATE SET version_id=EXCLUDED.version_id,updated_at=EXCLUDED.updated_at`, tenant, scope, scopeID, target, now); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='ROLLED_BACK',record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenant, id); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE platform.runtime_config_versions SET state='ACTIVE',activated_at=$1,record_version=record_version+1,updated_at=$1 WHERE tenant_id=$2 AND id=$3`, now, tenant, target); e != nil {
			return e
		}
		_, execErr := tx.Exec(ctx, `INSERT INTO platform.runtime_config_events(id,tenant_id,version_id,event_type,actor_user_id,details_json,created_at) VALUES($1,$2,$3,'ROLLED_BACK',$4,jsonb_build_object('targetVersionId',$5::uuid),$6)`, uuid.NewString(), tenant, id, actor, target, now)
		return execErr
	})
	if e != nil {
		return Version{}, mapError(e)
	}
	return s.getSystem(ctx, tenant, target)
}
func (s *Service) authorize(ctx context.Context, tenant, actor string) error {
	if !canonicalUUID(tenant) || !canonicalUUID(actor) {
		return ErrInvalid
	}
	allowed, e := s.admins.IsPlatformAdministrator(ctx, tenant, actor)
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
func upper(v string) string { return strings.ToUpper(strings.TrimSpace(v)) }

type scanner interface{ Scan(...any) error }

func scanVersion(row scanner, v *Version) error {
	var raw []byte
	var base, approved, rejected string
	if e := row.Scan(&v.ID, &v.ScopeType, &v.ScopeID, &v.VersionNo, &base, &raw, &v.ConfigHash, &v.State, &v.Compatibility, &v.ImpactSummary, &v.CreatedBy, &approved, &v.RecordVersion, &v.CreatedAt, &v.UpdatedAt, &v.SubmittedAt, &v.ApprovedAt, &v.ActivatedAt, &rejected, &v.RejectedAt, &v.RejectionReason); e != nil {
		return e
	}
	v.BaseVersionID = askdata.ID(base)
	v.ApprovedBy = askdata.ID(approved)
	v.RejectedBy = askdata.ID(rejected)
	v.Config = raw
	return nil
}
func mapError(e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if errors.Is(e, ErrConflict) || errors.Is(e, ErrInvalid) {
		return e
	}
	var pgErr *pgconn.PgError
	if errors.As(e, &pgErr) {
		switch pgErr.Code {
		case "23505", "40001":
			return ErrConflict
		case "23503", "42501":
			return ErrForbidden
		case "23514", "22P02":
			return ErrInvalid
		}
	}
	return e
}
func canonicalUUID(v string) bool {
	parsed, e := uuid.Parse(v)
	return e == nil && parsed.String() == v
}

// Resolve returns a single effective value after re-validating its stored
// version. Domain scope takes precedence over tenant scope; worker scope is
// selected explicitly by callers.
func (s *Service) Resolve(ctx context.Context, tenant, scopeType, scopeID, key string) (any, bool, error) {
	if !canonicalUUID(tenant) || definitions[key].Key == "" {
		return nil, false, ErrInvalid
	}
	var raw []byte
	e := database.WithTenantTx(database.WithoutAccessContext(ctx), s.pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version.config_json FROM platform.runtime_config_effective effective JOIN platform.runtime_config_versions version ON version.tenant_id=effective.tenant_id AND version.id=effective.version_id WHERE effective.tenant_id=$1 AND effective.scope_type=$2 AND effective.scope_id=$3 AND version.state='ACTIVE'`, tenant, scopeType, scopeID).Scan(&raw)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if e != nil {
		return nil, false, e
	}
	canonical, _, _, e := ValidateConfig(scopeType, raw)
	if e != nil {
		return nil, false, e
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	if e = decoder.Decode(&object); e != nil {
		return nil, false, e
	}
	value, ok := object[key]
	return value, ok, nil
}
