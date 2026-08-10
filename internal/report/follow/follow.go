// Package follow stores report personalization without changing report ACLs.
package follow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/auth"
	"intelligent-report-generation-system/internal/platform/database"
	platformidempotency "intelligent-report-generation-system/internal/platform/idempotency"
	reportauthorization "intelligent-report-generation-system/internal/report/authorization"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

var (
	ErrInvalid   = errors.New("report follow request is invalid")
	ErrForbidden = errors.New("report follow is forbidden")
	ErrNotFound  = errors.New("report follow was not found")
)

type Identity struct{ TenantID, DomainID, ActorID askdata.ID }
type Item struct {
	ReportID                  askdata.ID `json:"reportId"`
	Name                      string     `json:"name"`
	ReportType                string     `json:"reportType"`
	CurrentPublishedVersionID askdata.ID `json:"currentPublishedVersionId"`
	FollowedAt                time.Time  `json:"followedAt"`
	SourceHref                string     `json:"sourceHref"`
}
type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) List(ctx context.Context, i Identity, limit int, cursor string) (Page, error) {
	if s == nil || s.pool == nil || validateIdentity(i) != nil || limit < 1 || limit > 200 {
		return Page{}, ErrInvalid
	}
	var cursorTime *time.Time
	var cursorID any
	if cursor != "" {
		parts := strings.Split(cursor, "|")
		if len(parts) != 2 {
			return Page{}, ErrInvalid
		}
		parsed, e := time.Parse(time.RFC3339Nano, parts[0])
		if e != nil || askdata.ID(parts[1]).Validate() != nil {
			return Page{}, ErrInvalid
		}
		cursorTime = &parsed
		cursorID = parts[1]
	}
	result := Page{Items: []Item{}}
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT follow.report_id::text,report.name,report.report_type,report.current_published_version_id::text,follow.followed_at FROM platform.report_follows follow JOIN platform.reports report ON report.tenant_id=follow.tenant_id AND report.id=follow.report_id WHERE follow.tenant_id=$1 AND follow.domain_id=$2 AND follow.actor_user_id=$3 AND report.status='ACTIVE' AND report.current_published_version_id IS NOT NULL AND platform.report_v2_can_access(report.id,ARRAY['VIEW']::text[]) AND ($4::timestamptz IS NULL OR (follow.followed_at,follow.report_id)<($4,$5::uuid)) ORDER BY follow.followed_at DESC,follow.report_id DESC LIMIT $6`, i.TenantID, i.DomainID, i.ActorID, cursorTime, cursorID, limit+1)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var item Item
			if e = rows.Scan(&item.ReportID, &item.Name, &item.ReportType, &item.CurrentPublishedVersionID, &item.FollowedAt); e != nil {
				return e
			}
			item.SourceHref = "/reports/" + string(item.ReportID)
			result.Items = append(result.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return Page{}, err
	}
	if len(result.Items) > limit {
		last := result.Items[limit-1]
		result.NextCursor = last.FollowedAt.Format(time.RFC3339Nano) + "|" + string(last.ReportID)
		result.Items = result.Items[:limit]
	}
	return result, nil
}
func (s *Store) Follow(ctx context.Context, i Identity, reportID askdata.ID, now time.Time) error {
	if s == nil || s.pool == nil || validateIdentity(i) != nil || reportID.Validate() != nil || now.IsZero() {
		return ErrInvalid
	}
	err := database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO platform.report_follows(tenant_id,domain_id,report_id,actor_user_id,followed_at)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(tenant_id,domain_id,actor_user_id,report_id) DO NOTHING`, i.TenantID, i.DomainID, reportID, i.ActorID, now)
		return e
	})
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "42501" {
		return ErrForbidden
	}
	return err
}
func (s *Store) Unfollow(ctx context.Context, i Identity, reportID askdata.ID) error {
	if s == nil || s.pool == nil || validateIdentity(i) != nil || reportID.Validate() != nil {
		return ErrInvalid
	}
	return database.WithTenantTx(ctx, s.pool, string(i.TenantID), func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `DELETE FROM platform.report_follows WHERE tenant_id=$1 AND domain_id=$2 AND actor_user_id=$3 AND report_id=$4`, i.TenantID, i.DomainID, i.ActorID, reportID)
		return e
	})
}

type Service struct {
	store      *Store
	authorizer *reportauthorization.PostgresAuthorizer
	now        func() time.Time
}

func NewService(store *Store, authorizer *reportauthorization.PostgresAuthorizer) (*Service, error) {
	if store == nil || store.pool == nil || authorizer == nil {
		return nil, errors.New("report follow dependencies are incomplete")
	}
	return &Service{store: store, authorizer: authorizer, now: time.Now}, nil
}
func (s *Service) List(ctx context.Context, i Identity, limit int, cursor string) (Page, error) {
	return s.store.List(ctx, i, limit, cursor)
}
func (s *Service) Follow(ctx context.Context, i Identity, id askdata.ID) error {
	if validateIdentity(i) != nil || id.Validate() != nil {
		return ErrInvalid
	}
	if s.authorizer.CheckReportView(ctx, reportstore.Identity{TenantID: i.TenantID, DomainID: i.DomainID, ActorID: i.ActorID}, id) != nil {
		return ErrForbidden
	}
	return s.store.Follow(ctx, i, id, s.now().UTC())
}
func (s *Service) Unfollow(ctx context.Context, i Identity, id askdata.ID) error {
	if validateIdentity(i) != nil || id.Validate() != nil {
		return ErrInvalid
	}
	return s.store.Unfollow(ctx, i, id)
}

type Handler struct{ service *Service }

func NewHandler(authService *auth.Service, idempotency platformidempotency.Repository, service *Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/report-follows", h.list)
	mux.HandleFunc("POST /api/v1/reports/{id}/follow", h.follow)
	mux.HandleFunc("DELETE /api/v1/reports/{id}/follow", h.unfollow)
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
		writeServiceError(w, e)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, e = strconv.Atoi(raw)
		if e != nil {
			writeServiceError(w, ErrInvalid)
			return
		}
	}
	value, e := h.service.List(r.Context(), i, limit, r.URL.Query().Get("cursor"))
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, value)
}
func (h *Handler) follow(w http.ResponseWriter, r *http.Request)   { h.mutate(w, r, true) }
func (h *Handler) unfollow(w http.ResponseWriter, r *http.Request) { h.mutate(w, r, false) }
func (h *Handler) mutate(w http.ResponseWriter, r *http.Request, follow bool) {
	i, e := identityFromContext(r.Context())
	id := askdata.ID(r.PathValue("id"))
	if e != nil || id.Validate() != nil || decodeEmpty(r) != nil {
		writeServiceError(w, ErrInvalid)
		return
	}
	if follow {
		e = h.service.Follow(r.Context(), i, id)
	} else {
		e = h.service.Unfollow(r.Context(), i, id)
	}
	if e != nil {
		writeServiceError(w, e)
		return
	}
	writeJSON(w, 200, map[string]bool{"following": follow})
}
func identityFromContext(ctx context.Context) (Identity, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	access, aok := database.AccessContextFromContext(ctx)
	if !ok || !aok || claims.Subject != access.UserID {
		return Identity{}, ErrForbidden
	}
	i := Identity{askdata.ID(claims.TenantID), askdata.ID(access.DomainID), askdata.ID(claims.Subject)}
	return i, validateIdentity(i)
}
func validateIdentity(i Identity) error {
	if i.TenantID.Validate() != nil || i.DomainID.Validate() != nil || i.ActorID.Validate() != nil {
		return ErrInvalid
	}
	return nil
}
func decodeEmpty(r *http.Request) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	d := json.NewDecoder(io.LimitReader(r.Body, 1025))
	var body map[string]json.RawMessage
	if d.Decode(&body) != nil || len(body) != 0 {
		return ErrInvalid
	}
	var tail any
	if !errors.Is(d.Decode(&tail), io.EOF) {
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
func writeServiceError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, ErrInvalid):
		writeError(w, 400, "REPORT_FOLLOW_INVALID", e.Error())
	case errors.Is(e, ErrForbidden):
		writeError(w, 403, "REPORT_FOLLOW_FORBIDDEN", e.Error())
	case errors.Is(e, ErrNotFound):
		writeError(w, 404, "REPORT_FOLLOW_NOT_FOUND", e.Error())
	default:
		writeError(w, 500, "REPORT_FOLLOW_INTERNAL", "report follow operation failed")
	}
}
