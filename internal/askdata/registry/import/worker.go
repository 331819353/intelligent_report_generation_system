package registryimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata/registry"
	spikeexcel "intelligent-report-generation-system/internal/spike/excel"
)

const (
	DefaultValidationLease = 5 * time.Minute
	maxImportFileBytes     = int64(50 << 20)
)

var (
	ErrImportFileInvalid          = errors.New("semantic import file is invalid")
	ErrImportValidatorUnavailable = errors.New("semantic import validator is unavailable")
)

// WorkerStore is deliberately narrower than PostgresStore so parsing and
// batching can be exercised without a database. Asset-specific validation is
// injected by IMPORT-003.
type WorkerStore interface {
	ListTenantIDs(context.Context) ([]string, error)
	ClaimForValidation(context.Context, string, string, time.Duration) (*Claim, error)
	Heartbeat(context.Context, Claim, string, time.Duration) error
	UpsertRows(context.Context, Claim, string, []ValidatedRow) error
	CompleteValidation(context.Context, Claim, string) error
	FailValidation(context.Context, Claim, string, string) error
}

type RowSource interface {
	ForEachRow(context.Context, Claim, int, func(int, json.RawMessage) error) error
}

type RowValidator interface {
	ValidateRow(context.Context, Claim, int, json.RawMessage) (ValidatedRow, error)
}

// RawImportRow is the immutable, parser-normalized input to validation. A
// PreparedRowValidator receives the complete bounded file before row writes so
// cross-row rules such as formula cycles and hierarchy continuity can be
// evaluated without weakening the 500-row persistence boundary.
type RawImportRow struct {
	RowNo int
	Raw   json.RawMessage
}

type PreparedRowValidator interface {
	Prepare(context.Context, Claim, []RawImportRow) (RowValidator, error)
}

type Worker struct {
	store             WorkerStore
	source            RowSource
	validator         RowValidator
	heartbeatInterval func(time.Duration) time.Duration
}

func NewWorker(store WorkerStore, source RowSource, validator RowValidator) *Worker {
	return &Worker{
		store: store, source: source, validator: validator,
		heartbeatInterval: defaultImportHeartbeatInterval,
	}
}

func (worker *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, ErrInvalidImport
	}
	return worker.store.ListTenantIDs(ctx)
}

// ProcessNext claims and closes at most one import. Permanent file/contract
// failures are recorded as FAILED and count as processed; infrastructure or
// lease failures are returned and remain resumable after lease expiry.
func (worker *Worker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.source == nil || worker.validator == nil {
		return false, ErrInvalidImport
	}
	claim, err := worker.store.ClaimForValidation(ctx, tenantID, workerID, lease)
	if err != nil {
		return false, err
	}
	if claim == nil {
		return false, nil
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go worker.heartbeat(heartbeatCtx, cancelWork, *claim, workerID, lease, heartbeatDone)

	batch := make([]ValidatedRow, 0, MaxRowWriteBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := worker.store.UpsertRows(workCtx, *claim, workerID, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	validator := worker.validator
	var preparedRows []RawImportRow
	processErr := error(nil)
	if preparer, ok := worker.validator.(PreparedRowValidator); ok {
		processErr = worker.source.ForEachRow(
			workCtx, *claim, 0,
			func(rowNo int, raw json.RawMessage) error {
				preparedRows = append(preparedRows, RawImportRow{
					RowNo: rowNo, Raw: append(json.RawMessage(nil), raw...),
				})
				return nil
			},
		)
		if processErr == nil {
			validator, processErr = preparer.Prepare(workCtx, *claim, preparedRows)
			if validator == nil && processErr == nil {
				processErr = permanentImportError("IMPORT_VALIDATOR_CONTRACT", ErrInvalidImportRow)
			}
		}
	}
	consume := func(rowNo int, raw json.RawMessage) error {
		row, err := validator.ValidateRow(workCtx, *claim, rowNo, raw)
		if err != nil {
			return err
		}
		if row.RowNo != rowNo {
			return permanentImportError("IMPORT_VALIDATOR_CONTRACT", ErrInvalidImportRow)
		}
		batch = append(batch, row)
		if len(batch) == MaxRowWriteBatch {
			return flush()
		}
		return nil
	}
	if processErr == nil && preparedRows != nil {
		for _, input := range preparedRows {
			if input.RowNo <= claim.ResumeAfterRow {
				continue
			}
			if processErr = consume(input.RowNo, input.Raw); processErr != nil {
				break
			}
		}
	} else if processErr == nil {
		processErr = worker.source.ForEachRow(
			workCtx, *claim, claim.ResumeAfterRow, consume,
		)
	}
	if processErr == nil {
		processErr = flush()
	}
	stopHeartbeat()
	heartbeatErr := <-heartbeatDone
	cancelWork()
	if heartbeatErr != nil {
		return true, heartbeatErr
	}
	if processErr != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		var permanent *PermanentImportError
		if !errors.As(processErr, &permanent) {
			return true, processErr
		}
		if err := worker.store.FailValidation(ctx, *claim, workerID, permanent.Code); err != nil {
			return true, err
		}
		return true, nil
	}
	if err := worker.store.CompleteValidation(ctx, *claim, workerID); err != nil {
		return true, err
	}
	return true, nil
}

func (worker *Worker) heartbeat(
	ctx context.Context,
	cancelWork context.CancelFunc,
	claim Claim,
	workerID string,
	lease time.Duration,
	done chan<- error,
) {
	interval := worker.heartbeatInterval(lease)
	if interval <= 0 {
		interval = defaultImportHeartbeatInterval(lease)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-timer.C:
			if err := worker.store.Heartbeat(ctx, claim, workerID, lease); err != nil {
				cancelWork()
				done <- err
				return
			}
			timer.Reset(interval)
		}
	}
}

func defaultImportHeartbeatInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < time.Second {
		return time.Second
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval
}

// PermanentImportError marks a deterministic input or validator-contract
// failure that should close the batch instead of consuming retry attempts.
type PermanentImportError struct {
	Code  string
	Cause error
}

func (failure *PermanentImportError) Error() string {
	if failure == nil {
		return "semantic import failed"
	}
	return failure.Code
}

func (failure *PermanentImportError) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.Cause
}

func permanentImportError(code string, cause error) error {
	if !validCode(code) {
		code = "IMPORT_VALIDATION_FAILED"
	}
	return &PermanentImportError{Code: code, Cause: cause}
}

// UnavailableValidator keeps the foundation fail-closed until IMPORT-003
// registers the asset-specific four-layer validator. IMPORT-002 cannot create
// uploads without an API, so no user batch is consumed by this interim guard.
type UnavailableValidator struct{}

func (UnavailableValidator) ValidateRow(
	context.Context,
	Claim,
	int,
	json.RawMessage,
) (ValidatedRow, error) {
	return ValidatedRow{}, permanentImportError(
		"IMPORT_VALIDATOR_UNAVAILABLE",
		ErrImportValidatorUnavailable,
	)
}

type ImportObjectStorage interface {
	Get(context.Context, string, string) (io.ReadCloser, error)
}

// FileRowSource reads the immutable MinIO object, verifies its SHA-256, and
// converts the single-sheet CSV/XLS/XLSX template into canonical JSON rows.
// Row numbers exclude the header and remain stable across retries.
type FileRowSource struct {
	storage ImportObjectStorage
	limits  spikeexcel.Limits
}

func NewFileRowSource(storage ImportObjectStorage) *FileRowSource {
	limits := spikeexcel.DefaultLimits()
	limits.MaxFileBytes = maxImportFileBytes
	return &FileRowSource{storage: storage, limits: limits}
}

func (source *FileRowSource) ForEachRow(
	ctx context.Context,
	claim Claim,
	afterRow int,
	consume func(int, json.RawMessage) error,
) error {
	if source == nil || source.storage == nil || consume == nil || afterRow < 0 {
		return ErrInvalidImport
	}
	bucket, key, err := parseImportObjectURI(claim.FileObjectURI)
	if err != nil {
		return permanentImportError("IMPORT_FILE_URI_INVALID", err)
	}
	body, err := source.storage.Get(ctx, bucket, key)
	if err != nil {
		return err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, source.limits.MaxFileBytes+1))
	if err != nil {
		return err
	}
	if len(data) == 0 || int64(len(data)) > source.limits.MaxFileBytes {
		return permanentImportError("IMPORT_FILE_SIZE_INVALID", ErrImportFileInvalid)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != claim.FileHash {
		return permanentImportError("IMPORT_FILE_HASH_MISMATCH", ErrImportFileInvalid)
	}
	book, err := spikeexcel.Read(
		claim.FileName,
		bytes.NewReader(data),
		int64(len(data)),
		source.limits,
	)
	if err != nil {
		return permanentImportError("IMPORT_FILE_PARSE_FAILED", ErrImportFileInvalid)
	}
	if len(book.Sheets) < 1 {
		return permanentImportError("IMPORT_FILE_SHAPE_INVALID", ErrImportFileInvalid)
	}
	dataSheet := book.Sheets[0]
	if len(book.Sheets) > 1 {
		found := false
		for _, sheet := range book.Sheets {
			if sheet.Name == "Import" {
				dataSheet = sheet
				found = true
				break
			}
		}
		if !found {
			return permanentImportError("IMPORT_FILE_SHAPE_INVALID", ErrImportFileInvalid)
		}
	}
	if len(dataSheet.Rows) < 2 {
		return permanentImportError("IMPORT_FILE_SHAPE_INVALID", ErrImportFileInvalid)
	}
	headers, err := normalizeImportHeaders(dataSheet.Rows[0], source.limits.MaxColumns)
	if err != nil {
		return err
	}
	dataRowNo := 0
	for index, values := range dataSheet.Rows[1:] {
		if err := ctx.Err(); err != nil {
			return err
		}
		if index == 0 && len(values) > 0 && strings.HasPrefix(
			strings.TrimSpace(values[0]), TemplateInstructionPrefix,
		) {
			continue
		}
		dataRowNo++
		rowNo := dataRowNo
		if rowNo <= afterRow {
			continue
		}
		if len(values) > len(headers) {
			return permanentImportError("IMPORT_ROW_COLUMN_OVERFLOW", ErrImportFileInvalid)
		}
		row := make(map[string]any, len(headers))
		for column, header := range headers {
			value := ""
			if column < len(values) {
				value = strings.TrimSpace(values[column])
			}
			row[header] = value
		}
		raw, err := registry.CanonicalValue(row)
		if err != nil {
			return permanentImportError("IMPORT_ROW_JSON_INVALID", err)
		}
		if err := consume(rowNo, raw); err != nil {
			return err
		}
	}
	return nil
}

func parseImportObjectURI(raw string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "minio" && parsed.Scheme != "s3") ||
		parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", ErrImportFileInvalid
	}
	bucket := parsed.Host
	key := strings.TrimPrefix(parsed.EscapedPath(), "/")
	decodedKey, err := url.PathUnescape(key)
	if err != nil || decodedKey == "" || decodedKey != path.Clean(decodedKey) ||
		strings.HasPrefix(decodedKey, "../") || strings.Contains(decodedKey, "\x00") {
		return "", "", ErrImportFileInvalid
	}
	return bucket, decodedKey, nil
}

func normalizeImportHeaders(values []string, maxColumns int) ([]string, error) {
	if len(values) == 0 || len(values) > maxColumns {
		return nil, permanentImportError("IMPORT_HEADER_INVALID", ErrImportFileInvalid)
	}
	headers := make([]string, len(values))
	seen := map[string]struct{}{}
	for index, value := range values {
		value = strings.TrimSpace(value)
		if !boundedText(value, 128) {
			return nil, permanentImportError("IMPORT_HEADER_INVALID", ErrImportFileInvalid)
		}
		canonical := strings.ToLower(value)
		if _, exists := seen[canonical]; exists {
			return nil, permanentImportError("IMPORT_HEADER_DUPLICATE", ErrImportFileInvalid)
		}
		seen[canonical] = struct{}{}
		headers[index] = value
	}
	return headers, nil
}

func (claim Claim) String() string {
	return fmt.Sprintf("%s:%d", claim.ImportID, claim.Attempt)
}
