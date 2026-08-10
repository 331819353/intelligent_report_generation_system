package publication

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report/store"
)

const (
	DefaultExportTTL   = 24 * time.Hour
	DefaultExportLease = 10 * time.Minute
	MaxExportAttempts  = 5
	MaxExportBytes     = 1 << 30
)

type ExportFormat string

const (
	ExportPDF  ExportFormat = "PDF"
	ExportPNG  ExportFormat = "PNG"
	ExportCSV  ExportFormat = "CSV"
	ExportXLSX ExportFormat = "XLSX"
)

type ExportState string

const (
	ExportPending ExportState = "PENDING"
	ExportRunning ExportState = "RUNNING"
	ExportReady   ExportState = "READY"
	ExportFailed  ExportState = "FAILED"
	ExportExpired ExportState = "EXPIRED"
)

type ExportJob struct {
	ID, TenantID, DomainID, ReportID, ReportVersionID, RequestedBy askdata.ID
	Format                                                         ExportFormat
	PageIDs                                                        []askdata.ID
	FilterSummary                                                  map[string]any
	AsOf                                                           time.Time
	Timezone                                                       string
	State                                                          ExportState
	Attempt                                                        int
	NextAttemptAt                                                  time.Time
	LeaseOwner, LeaseToken                                         string
	LeaseExpiresAt                                                 *time.Time
	ObjectURI, ContentHash, FailureCode                            string
	ArtifactBytes                                                  int64
	ExpiresAt, CreatedAt, UpdatedAt                                time.Time
	StartedAt, CompletedAt                                         *time.Time
}

func (job ExportJob) EffectiveState(now time.Time) ExportState {
	if !now.Before(job.ExpiresAt) && job.State != ExportFailed {
		return ExportExpired
	}
	return job.State
}

type CreateExportInput struct {
	ID, ReportID, ReportVersionID askdata.ID
	Format                        ExportFormat
	PageIDs                       []askdata.ID
	FilterSummary                 map[string]any
	AsOf                          time.Time
	Timezone                      string
	ExpiresAt                     time.Time
}

type ExportClaim struct{ ExportJob }

type ExportJobStore struct{ pool *pgxpool.Pool }

func NewExportJobStore(pool *pgxpool.Pool) *ExportJobStore { return &ExportJobStore{pool: pool} }

func (s *ExportJobStore) Create(ctx context.Context, identity store.Identity, input CreateExportInput) (ExportJob, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || input.ReportID.Validate() != nil ||
		input.ReportVersionID.Validate() != nil || !validExportFormat(input.Format) || len(input.PageIDs) > 100 {
		return ExportJob{}, errors.New("invalid report export request")
	}
	if input.ID == "" {
		input.ID = askdata.ID(uuid.NewString())
	}
	if input.ID.Validate() != nil {
		return ExportJob{}, errors.New("invalid report export ID")
	}
	pageIDs := make([]string, len(input.PageIDs))
	seen := map[askdata.ID]struct{}{}
	for index, id := range input.PageIDs {
		if id.Validate() != nil {
			return ExportJob{}, errors.New("invalid export page ID")
		}
		if _, exists := seen[id]; exists {
			return ExportJob{}, errors.New("duplicate export page ID")
		}
		seen[id] = struct{}{}
		pageIDs[index] = string(id)
	}
	if input.FilterSummary == nil {
		input.FilterSummary = map[string]any{}
	}
	filterJSON, err := json.Marshal(input.FilterSummary)
	if err != nil || len(filterJSON) > 64<<10 {
		return ExportJob{}, errors.New("invalid export filter summary")
	}
	input.Timezone = strings.TrimSpace(input.Timezone)
	if input.AsOf.IsZero() || input.Timezone == "" {
		return ExportJob{}, errors.New("export asOf and timezone are required")
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return ExportJob{}, errors.New("export timezone must be an IANA timezone")
	}
	now := time.Now().UTC()
	if input.ExpiresAt.IsZero() {
		input.ExpiresAt = now.Add(DefaultExportTTL)
	}
	if !input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(7*24*time.Hour)) {
		return ExportJob{}, errors.New("invalid export expiry")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var result ExportJob
	err = database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO platform.report_export_jobs(
			id,tenant_id,domain_id,report_id,report_version_id,requested_by,format,page_ids,
			filter_summary_json,as_of,timezone,expires_at
		) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12
		WHERE EXISTS(SELECT 1 FROM platform.report_versions
			WHERE id=$5 AND report_id=$4 AND artifact_state='READY')
		RETURNING `+exportJobColumns, input.ID, identity.TenantID, identity.DomainID, input.ReportID,
			input.ReportVersionID, identity.ActorID, input.Format, pageIDs, filterJSON,
			input.AsOf.UTC(), input.Timezone, input.ExpiresAt.UTC())
		return scanExportJob(row, &result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ExportJob{}, store.ErrNotFound
	}
	return result, err
}

func (s *ExportJobStore) Get(ctx context.Context, identity store.Identity, id askdata.ID) (ExportJob, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || id.Validate() != nil {
		return ExportJob{}, errors.New("invalid report export lookup")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var result ExportJob
	err := database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return scanExportJob(tx.QueryRow(ctx, `SELECT `+exportJobColumns+`
			FROM platform.report_export_jobs WHERE id=$1`, id), &result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ExportJob{}, store.ErrNotFound
	}
	if err == nil {
		result.State = result.EffectiveState(time.Now().UTC())
	}
	return result, err
}

func (s *ExportJobStore) Retry(ctx context.Context, identity store.Identity, id askdata.ID) (ExportJob, error) {
	if s == nil || s.pool == nil || identity.Validate() != nil || id.Validate() != nil {
		return ExportJob{}, errors.New("invalid report export retry")
	}
	ctx = database.WithAccessContext(ctx, string(identity.ActorID), string(identity.DomainID))
	var result ExportJob
	err := database.WithTenantTx(ctx, s.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		return scanExportJob(tx.QueryRow(ctx, `UPDATE platform.report_export_jobs SET
			state='PENDING',attempt=0,next_attempt_at=now(),failure_code='',completed_at=NULL
			WHERE id=$1 AND state='FAILED' AND expires_at>now()
			RETURNING `+exportJobColumns, id), &result)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ExportJob{}, errors.New("report export is not retryable")
	}
	return result, err
}

func (s *ExportJobStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("report export store is unavailable")
	}
	rows, err := s.pool.Query(ctx, `SELECT tenant_id::text FROM platform.list_report_export_tenants()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (s *ExportJobStore) Claim(ctx context.Context, tenantID, workerID string, lease time.Duration) (*ExportClaim, error) {
	if s == nil || s.pool == nil || askdata.ID(tenantID).Validate() != nil || strings.TrimSpace(workerID) == "" ||
		len(workerID) > 128 || lease < 30*time.Second || lease > 30*time.Minute {
		return nil, errors.New("invalid report export claim")
	}
	var result ExportJob
	found := false
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.report_export_jobs SET state='EXPIRED',
			failure_code='EXPORT_EXPIRED',completed_at=now(),lease_owner='',lease_token=NULL,lease_expires_at=NULL
			WHERE state IN ('PENDING','RUNNING') AND expires_at<=now()`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.report_export_jobs SET state='FAILED',
			failure_code='EXPORT_ATTEMPTS_EXHAUSTED',completed_at=now(),lease_owner='',lease_token=NULL,lease_expires_at=NULL
			WHERE state='RUNNING' AND lease_expires_at<=now() AND attempt>=5`); err != nil {
			return err
		}
		leaseToken := uuid.NewString()
		row := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT id FROM platform.report_export_jobs
			WHERE expires_at>now() AND attempt<5 AND (
			  (state='PENDING' AND next_attempt_at<=now()) OR
			  (state='RUNNING' AND lease_expires_at<=now())
			) ORDER BY next_attempt_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
		) UPDATE platform.report_export_jobs job SET state='RUNNING',attempt=job.attempt+1,
			lease_owner=$1,lease_token=$2,lease_expires_at=now()+$3::bigint*interval '1 second',
			started_at=COALESCE(job.started_at,now()),failure_code='',completed_at=NULL
			FROM candidate WHERE job.id=candidate.id RETURNING `+prefixedExportJobColumns("job"),
			workerID, leaseToken, int64(lease/time.Second))
		if err := scanExportJob(row, &result); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		found = true
		return nil
	})
	if err != nil || !found {
		return nil, err
	}
	return &ExportClaim{result}, nil
}

func (s *ExportJobStore) Complete(ctx context.Context, claim ExportClaim, workerID string, artifact ExportArtifact, objectURI string) error {
	if err := artifact.Validate(claim.Format); err != nil || objectURI == "" {
		return errors.New("invalid report export completion")
	}
	return database.WithTenantTx(ctx, s.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.report_export_jobs SET state='READY',
			object_uri=$1,content_hash=$2,artifact_bytes=$3,completed_at=now(),
			lease_owner='',lease_token=NULL,lease_expires_at=NULL,failure_code=''
			WHERE id=$4 AND state='RUNNING' AND lease_owner=$5 AND lease_token=$6`, objectURI,
			artifact.ContentHash, len(artifact.Bytes), claim.ID, workerID, claim.LeaseToken)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, errors.New("report export lease lost"))
		}
		return nil
	})
}

func (s *ExportJobStore) Fail(ctx context.Context, claim ExportClaim, workerID, code string, retryable bool) error {
	if strings.TrimSpace(code) == "" || len(code) > 128 {
		return errors.New("invalid report export failure")
	}
	return database.WithTenantTx(ctx, s.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		state := ExportFailed
		var completed any = time.Now().UTC()
		nextAttempt := time.Now().UTC()
		if retryable && claim.Attempt < MaxExportAttempts && time.Now().UTC().Before(claim.ExpiresAt) {
			state, completed = ExportPending, nil
			nextAttempt = time.Now().UTC().Add(time.Duration(min(300, 1<<min(claim.Attempt, 8))) * time.Second)
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.report_export_jobs SET state=$1,
			failure_code=$2,next_attempt_at=$3,completed_at=$4,
			lease_owner='',lease_token=NULL,lease_expires_at=NULL
			WHERE id=$5 AND state='RUNNING' AND lease_owner=$6 AND lease_token=$7`,
			state, code, nextAttempt, completed, claim.ID, workerID, claim.LeaseToken)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, errors.New("report export lease lost"))
		}
		return nil
	})
}

const exportJobColumns = `id::text,tenant_id::text,domain_id::text,report_id::text,report_version_id::text,
	requested_by::text,format,page_ids,filter_summary_json,as_of,timezone,state,attempt,
	next_attempt_at,lease_owner,COALESCE(lease_token::text,''),lease_expires_at,
	object_uri,content_hash,artifact_bytes,failure_code,expires_at,created_at,started_at,
	completed_at,updated_at`

func prefixedExportJobColumns(alias string) string {
	parts := strings.Split(exportJobColumns, ",")
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "(") {
			parts[index] = strings.ReplaceAll(part, "lease_token", alias+".lease_token")
		} else {
			parts[index] = alias + "." + part
		}
	}
	return strings.Join(parts, ",")
}

type exportScanner interface{ Scan(...any) error }

func scanExportJob(row exportScanner, target *ExportJob) error {
	var pageIDs []string
	var filters []byte
	if err := row.Scan(&target.ID, &target.TenantID, &target.DomainID, &target.ReportID, &target.ReportVersionID,
		&target.RequestedBy, &target.Format, &pageIDs, &filters, &target.AsOf, &target.Timezone,
		&target.State, &target.Attempt, &target.NextAttemptAt, &target.LeaseOwner, &target.LeaseToken,
		&target.LeaseExpiresAt, &target.ObjectURI, &target.ContentHash, &target.ArtifactBytes,
		&target.FailureCode, &target.ExpiresAt, &target.CreatedAt, &target.StartedAt,
		&target.CompletedAt, &target.UpdatedAt); err != nil {
		return err
	}
	target.PageIDs = make([]askdata.ID, len(pageIDs))
	for index, id := range pageIDs {
		target.PageIDs[index] = askdata.ID(id)
	}
	if err := json.Unmarshal(filters, &target.FilterSummary); err != nil {
		return err
	}
	return nil
}

func validExportFormat(value ExportFormat) bool {
	return value == ExportPDF || value == ExportPNG || value == ExportCSV || value == ExportXLSX
}

type ExportFooter struct {
	ReportVersion int            `json:"reportVersion"`
	AsOf          string         `json:"asOf"`
	Filters       map[string]any `json:"filters"`
	ExportedAt    string         `json:"exportedAt"`
	ExportedBy    askdata.ID     `json:"exportedBy"`
}

type ExportArtifact struct {
	Bytes       []byte
	ContentType string
	Extension   string
	ContentHash string
	Footer      ExportFooter
}

func (artifact *ExportArtifact) Seal() {
	sum := sha256.Sum256(artifact.Bytes)
	artifact.ContentHash = hex.EncodeToString(sum[:])
}

func (artifact ExportArtifact) Validate(format ExportFormat) error {
	if len(artifact.Bytes) == 0 || len(artifact.Bytes) > MaxExportBytes || artifact.ContentType == "" ||
		artifact.Extension != strings.ToLower(string(format)) || artifact.Footer.ReportVersion < 1 ||
		artifact.Footer.AsOf == "" || artifact.Footer.ExportedAt == "" || artifact.Footer.ExportedBy.Validate() != nil {
		return errors.New("invalid report export artifact")
	}
	sum := sha256.Sum256(artifact.Bytes)
	if artifact.ContentHash != hex.EncodeToString(sum[:]) {
		return errors.New("report export content hash mismatch")
	}
	return nil
}

type ExportGenerator interface {
	Generate(context.Context, ExportClaim, ExportFooter) (ExportArtifact, error)
}

type ExportObjectStorage interface {
	Put(context.Context, string, string, io.Reader, int64, string) error
}

type ExportWorker struct {
	store    *ExportJobStore
	versions interface {
		GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error)
	}
	generator ExportGenerator
	storage   ExportObjectStorage
	bucket    string
	now       func() time.Time
}

func NewExportWorker(jobStore *ExportJobStore, versions interface {
	GetVersion(context.Context, store.Identity, askdata.ID, *int) (store.Version, error)
}, generator ExportGenerator, storage ExportObjectStorage, bucket string) (*ExportWorker, error) {
	if jobStore == nil || versions == nil || generator == nil || storage == nil || strings.TrimSpace(bucket) == "" {
		return nil, errors.New("report export worker is not configured")
	}
	return &ExportWorker{store: jobStore, versions: versions, generator: generator, storage: storage, bucket: bucket, now: time.Now}, nil
}

func (w *ExportWorker) TenantIDs(ctx context.Context) ([]string, error) {
	return w.store.ListTenantIDs(ctx)
}

func (w *ExportWorker) ProcessNext(ctx context.Context, tenantID, workerID string, lease time.Duration) (bool, error) {
	claim, err := w.store.Claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	identity := store.Identity{TenantID: claim.TenantID, ActorID: claim.RequestedBy, DomainID: claim.DomainID}
	versionNo := 0
	// Resolve by exact immutable ID first; the repository's numeric API is used
	// only after deriving the version number from the claimed row.
	err = database.WithTenantTx(ctx, w.store.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT version_no FROM platform.report_versions WHERE id=$1 AND report_id=$2`,
			claim.ReportVersionID, claim.ReportID).Scan(&versionNo)
	})
	if err != nil {
		return true, w.store.Fail(ctx, *claim, workerID, "EXPORT_VERSION_UNAVAILABLE", false)
	}
	version, err := w.versions.GetVersion(database.WithAccessContext(ctx, string(claim.RequestedBy), string(claim.DomainID)), identity, claim.ReportID, &versionNo)
	if err != nil || version.ID != claim.ReportVersionID || version.ArtifactState != "READY" {
		return true, w.store.Fail(ctx, *claim, workerID, "EXPORT_VERSION_UNAVAILABLE", false)
	}
	location, locationErr := time.LoadLocation(claim.Timezone)
	if locationErr != nil {
		return true, w.store.Fail(ctx, *claim, workerID, "EXPORT_TIMEZONE_INVALID", false)
	}
	footer := ExportFooter{ReportVersion: version.VersionNo, AsOf: claim.AsOf.In(location).Format(time.RFC3339),
		Filters: claim.FilterSummary, ExportedAt: w.now().UTC().Format(time.RFC3339), ExportedBy: claim.RequestedBy}
	artifact, err := w.generator.Generate(ctx, *claim, footer)
	if err != nil {
		return true, w.store.Fail(ctx, *claim, workerID, "EXPORT_GENERATION_FAILED", true)
	}
	if err := artifact.Validate(claim.Format); err != nil {
		return true, w.store.Fail(ctx, *claim, workerID, "EXPORT_ARTIFACT_INVALID", false)
	}
	key := path.Join("report-exports", tenantID, string(claim.ReportID), string(claim.ID), artifact.ContentHash+"."+artifact.Extension)
	if err := w.storage.Put(ctx, w.bucket, key, bytes.NewReader(artifact.Bytes), int64(len(artifact.Bytes)), artifact.ContentType); err != nil {
		return true, w.store.Fail(ctx, *claim, workerID, "EXPORT_STORAGE_FAILED", true)
	}
	return true, w.store.Complete(ctx, *claim, workerID, artifact, "s3://"+w.bucket+"/"+key)
}

type ExportRows struct {
	Columns []string
	Rows    [][]any
}

type ExportResultSource interface {
	Rows(context.Context, ExportClaim) (ExportRows, error)
}

type TabularExportGenerator struct{ Source ExportResultSource }

func (g TabularExportGenerator) Generate(ctx context.Context, claim ExportClaim, footer ExportFooter) (ExportArtifact, error) {
	if g.Source == nil || (claim.Format != ExportCSV && claim.Format != ExportXLSX) {
		return ExportArtifact{}, errors.New("tabular report export is unavailable")
	}
	data, err := g.Source.Rows(ctx, claim)
	if err != nil || len(data.Columns) == 0 {
		return ExportArtifact{}, errors.Join(err, errors.New("report export result is empty"))
	}
	var artifact ExportArtifact
	if claim.Format == ExportCSV {
		var output bytes.Buffer
		writer := csv.NewWriter(&output)
		_ = writer.Write(data.Columns)
		for _, row := range data.Rows {
			values := make([]string, len(row))
			for index, value := range row {
				values[index] = fmt.Sprint(value)
			}
			if err := writer.Write(values); err != nil {
				return ExportArtifact{}, err
			}
		}
		filterJSON, _ := json.Marshal(footer.Filters)
		_ = writer.Write([]string{"# reportVersion", strconv.Itoa(footer.ReportVersion)})
		_ = writer.Write([]string{"# asOf", footer.AsOf})
		_ = writer.Write([]string{"# filters", string(filterJSON)})
		_ = writer.Write([]string{"# exportedAt", footer.ExportedAt})
		_ = writer.Write([]string{"# exportedBy", string(footer.ExportedBy)})
		writer.Flush()
		if err := writer.Error(); err != nil {
			return ExportArtifact{}, err
		}
		artifact = ExportArtifact{Bytes: output.Bytes(), ContentType: "text/csv; charset=utf-8", Extension: "csv", Footer: footer}
	} else {
		book := excelize.NewFile()
		defer book.Close()
		sheet := "Report"
		book.SetSheetName("Sheet1", sheet)
		for col, value := range data.Columns {
			cell, _ := excelize.CoordinatesToCellName(col+1, 1)
			_ = book.SetCellValue(sheet, cell, value)
		}
		for rowIndex, row := range data.Rows {
			for col, value := range row {
				cell, _ := excelize.CoordinatesToCellName(col+1, rowIndex+2)
				_ = book.SetCellValue(sheet, cell, value)
			}
		}
		footerRow := len(data.Rows) + 4
		filterJSON, _ := json.Marshal(footer.Filters)
		for index, item := range [][2]any{{"reportVersion", footer.ReportVersion}, {"asOf", footer.AsOf}, {"filters", string(filterJSON)}, {"exportedAt", footer.ExportedAt}, {"exportedBy", footer.ExportedBy}} {
			_ = book.SetCellValue(sheet, fmt.Sprintf("A%d", footerRow+index), item[0])
			_ = book.SetCellValue(sheet, fmt.Sprintf("B%d", footerRow+index), item[1])
		}
		buffer, err := book.WriteToBuffer()
		if err != nil {
			return ExportArtifact{}, err
		}
		artifact = ExportArtifact{Bytes: buffer.Bytes(), ContentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Extension: "xlsx", Footer: footer}
	}
	artifact.Seal()
	return artifact, nil
}

type HTTPDocumentExportGenerator struct {
	Endpoint *url.URL
	Token    string
	Client   *http.Client
}

func NewHTTPDocumentExportGenerator(endpoint, token string, client *http.Client) (*HTTPDocumentExportGenerator, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("report export renderer URL is invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	return &HTTPDocumentExportGenerator{Endpoint: parsed, Token: strings.TrimSpace(token), Client: client}, nil
}

func (g *HTTPDocumentExportGenerator) Generate(ctx context.Context, claim ExportClaim, footer ExportFooter) (ExportArtifact, error) {
	if g == nil || g.Endpoint == nil || !validExportFormat(claim.Format) {
		return ExportArtifact{}, errors.New("document report export is unavailable")
	}
	payload, _ := json.Marshal(map[string]any{"reportId": claim.ReportID, "reportVersionId": claim.ReportVersionID,
		"tenantId": claim.TenantID, "domainId": claim.DomainID,
		"requestedBy": claim.RequestedBy, "format": claim.Format, "pageIds": claim.PageIDs,
		"filters": claim.FilterSummary, "asOf": claim.AsOf.Format(time.RFC3339Nano), "timezone": claim.Timezone,
		"disableLazyLoading": true, "footer": footer})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.Endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return ExportArtifact{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if g.Token != "" {
		request.Header.Set("Authorization", "Bearer "+g.Token)
	}
	response, err := g.Client.Do(request)
	if err != nil {
		return ExportArtifact{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ExportArtifact{}, errors.New("report renderer rejected export")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 100<<20+1))
	if err != nil || len(body) == 0 || len(body) > 100<<20 {
		return ExportArtifact{}, errors.New("report renderer response is invalid")
	}
	contentType := map[ExportFormat]string{ExportPDF: "application/pdf", ExportPNG: "image/png",
		ExportCSV: "text/csv; charset=utf-8", ExportXLSX: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"}[claim.Format]
	artifact := ExportArtifact{Bytes: body, ContentType: contentType, Extension: strings.ToLower(string(claim.Format)), Footer: footer}
	artifact.Seal()
	return artifact, nil
}

type CompositeExportGenerator struct{ Document, Tabular ExportGenerator }

func (g CompositeExportGenerator) Generate(ctx context.Context, claim ExportClaim, footer ExportFooter) (ExportArtifact, error) {
	if claim.Format == ExportPDF || claim.Format == ExportPNG {
		if g.Document == nil {
			return ExportArtifact{}, errors.New("document export generator is unavailable")
		}
		return g.Document.Generate(ctx, claim, footer)
	}
	if g.Tabular == nil {
		return ExportArtifact{}, errors.New("tabular export generator is unavailable")
	}
	return g.Tabular.Generate(ctx, claim, footer)
}
