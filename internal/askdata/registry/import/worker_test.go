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
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCanTransitionCoversLegalAndIllegalEdges(t *testing.T) {
	legal := [][2]State{
		{StateUploaded, StateValidating},
		{StateValidating, StateValidating},
		{StateValidating, StateValidated},
		{StateValidating, StateFailed},
		{StateValidated, StatePartiallyCommitted},
		{StateValidated, StateCommitted},
		{StateValidated, StateWithdrawn},
		{StatePartiallyCommitted, StatePartiallyCommitted},
		{StatePartiallyCommitted, StateCommitted},
		{StatePartiallyCommitted, StateWithdrawn},
		{StateCommitted, StateWithdrawn},
	}
	for _, edge := range legal {
		if !CanTransition(edge[0], edge[1]) {
			t.Errorf("legal transition %s -> %s rejected", edge[0], edge[1])
		}
	}
	illegal := [][2]State{
		{StateUploaded, StateValidated},
		{StateUploaded, StateFailed},
		{StateValidating, StateCommitted},
		{StateValidated, StateFailed},
		{StatePartiallyCommitted, StateFailed},
		{StateCommitted, StateValidated},
		{StateWithdrawn, StateValidated},
		{StateFailed, StateValidating},
	}
	for _, edge := range illegal {
		if CanTransition(edge[0], edge[1]) {
			t.Errorf("illegal transition %s -> %s accepted", edge[0], edge[1])
		}
	}
}

func TestWorkerWritesTenThousandRowsInFiveHundredRowBatchesAndResumes(t *testing.T) {
	claim := validWorkerClaim()
	store := &memoryWorkerStore{claim: claim, rows: map[int]ValidatedRow{}}
	source := &generatedRowSource{total: 10_000, interruptAt: 1_501}
	worker := NewWorker(store, source, passThroughValidator{})

	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, "import-worker", DefaultValidationLease,
	)
	if !processed || !errors.Is(err, errSimulatedInterruption) {
		t.Fatalf("first ProcessNext() = %v, %v", processed, err)
	}
	if got := len(store.rows); got != 1_500 {
		t.Fatalf("persisted before interruption = %d, want 1500", got)
	}
	if store.completed {
		t.Fatal("interrupted import was completed")
	}

	processed, err = worker.ProcessNext(
		context.Background(), claim.TenantID, "import-worker", DefaultValidationLease,
	)
	if !processed || err != nil {
		t.Fatalf("resumed ProcessNext() = %v, %v", processed, err)
	}
	if got := len(store.rows); got != 10_000 {
		t.Fatalf("persisted after resume = %d, want 10000", got)
	}
	if !store.completed || store.failedReason != "" {
		t.Fatalf("terminal state completed=%v failure=%q", store.completed, store.failedReason)
	}
	if len(store.batchSizes) != 20 {
		t.Fatalf("batch count = %d, want 20 (%v)", len(store.batchSizes), store.batchSizes)
	}
	for index, size := range store.batchSizes {
		if size != MaxRowWriteBatch {
			t.Fatalf("batch %d size = %d, want %d", index, size, MaxRowWriteBatch)
		}
	}
	if source.starts[0] != 0 || source.starts[1] != 1_500 {
		t.Fatalf("resume offsets = %v", source.starts)
	}
}

func TestWorkerRecordsPermanentFailureWithoutLeakingCause(t *testing.T) {
	claim := validWorkerClaim()
	store := &memoryWorkerStore{claim: claim, rows: map[int]ValidatedRow{}}
	worker := NewWorker(store, &generatedRowSource{total: 1}, UnavailableValidator{})
	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, "import-worker", DefaultValidationLease,
	)
	if !processed || err != nil {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	if store.failedReason != "IMPORT_VALIDATOR_UNAVAILABLE" || store.completed {
		t.Fatalf("failure=%q completed=%v", store.failedReason, store.completed)
	}
}

func TestWorkerPreparesCompleteBatchAndStillResumesPersistedRows(t *testing.T) {
	claim := validWorkerClaim()
	store := &memoryWorkerStore{
		claim: claim,
		rows: map[int]ValidatedRow{1: {
			RowNo: 1, RawJSON: json.RawMessage(`{"code":"M-00001"}`),
			NormalizedJSON: json.RawMessage(`{"code":"M-00001"}`), State: RowValid,
		}},
	}
	source := &generatedRowSource{total: 3}
	validator := &recordingPreparedValidator{}
	worker := NewWorker(store, source, validator)
	processed, err := worker.ProcessNext(
		context.Background(), claim.TenantID, "import-worker", DefaultValidationLease,
	)
	if !processed || err != nil {
		t.Fatalf("ProcessNext() = %v, %v", processed, err)
	}
	if validator.preparedRows != 3 {
		t.Fatalf("prepared rows = %d, want 3", validator.preparedRows)
	}
	if fmt.Sprint(source.starts) != "[0]" {
		t.Fatalf("source offsets = %v, want full batch preparation", source.starts)
	}
	if len(store.rows) != 3 || fmt.Sprint(store.batchSizes) != "[2]" {
		t.Fatalf("rows/batches = %d/%v", len(store.rows), store.batchSizes)
	}
}

func TestCanonicalObjectRejectsTrailingJSON(t *testing.T) {
	if _, err := canonicalObject(json.RawMessage(`{"a":1}{"b":2}`)); !errors.Is(err, ErrInvalidImportRow) {
		t.Fatalf("trailing JSON error = %v", err)
	}
	payload, err := canonicalObject(json.RawMessage(` { "b":2, "a":1 } `))
	if err != nil || string(payload) != `{"a":1,"b":2}` {
		t.Fatalf("canonical object = %s, %v", payload, err)
	}
}

func TestParseImportObjectURI(t *testing.T) {
	bucket, key, err := parseImportObjectURI("minio://semantic-imports/tenant/file.xlsx")
	if err != nil || bucket != "semantic-imports" || key != "tenant/file.xlsx" {
		t.Fatalf("parsed URI = %q, %q, %v", bucket, key, err)
	}
	for _, value := range []string{
		"https://example.com/file.xlsx",
		"minio:///file.xlsx",
		"minio://bucket/../file.xlsx",
		"minio://user:secret@bucket/file.xlsx",
		"minio://bucket/file.xlsx?version=1",
	} {
		if _, _, err := parseImportObjectURI(value); err == nil {
			t.Errorf("unsafe URI %q accepted", value)
		}
	}
}

func TestFileRowSourceVerifiesHashAndKeepsStableResumeRowNumbers(t *testing.T) {
	data := []byte("code,name\nM1,Revenue\nM2,Profit\nM3,Margin\n")
	digest := sha256.Sum256(data)
	claim := validWorkerClaim()
	claim.FileName = "metrics.csv"
	claim.FileHash = hex.EncodeToString(digest[:])
	source := NewFileRowSource(memoryObjectStorage{body: data})
	type capturedRow struct {
		rowNo int
		raw   string
	}
	captured := []capturedRow{}
	err := source.ForEachRow(
		context.Background(), claim, 1,
		func(rowNo int, raw json.RawMessage) error {
			captured = append(captured, capturedRow{rowNo: rowNo, raw: string(raw)})
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []capturedRow{
		{rowNo: 2, raw: `{"code":"M2","name":"Profit"}`},
		{rowNo: 3, raw: `{"code":"M3","name":"Margin"}`},
	}
	if fmt.Sprint(captured) != fmt.Sprint(want) {
		t.Fatalf("captured rows = %#v, want %#v", captured, want)
	}
	claim.FileHash = strings.Repeat("0", 64)
	err = source.ForEachRow(context.Background(), claim, 0, func(int, json.RawMessage) error { return nil })
	var permanent *PermanentImportError
	if !errors.As(err, &permanent) || permanent.Code != "IMPORT_FILE_HASH_MISMATCH" {
		t.Fatalf("hash mismatch error = %v", err)
	}
}

var errSimulatedInterruption = errors.New("simulated worker interruption")

type generatedRowSource struct {
	total       int
	interruptAt int
	interrupted bool
	starts      []int
}

func (source *generatedRowSource) ForEachRow(
	ctx context.Context,
	_ Claim,
	after int,
	consume func(int, json.RawMessage) error,
) error {
	source.starts = append(source.starts, after)
	for rowNo := after + 1; rowNo <= source.total; rowNo++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if source.interruptAt == rowNo && !source.interrupted {
			source.interrupted = true
			return errSimulatedInterruption
		}
		if err := consume(rowNo, json.RawMessage(fmt.Sprintf(`{"code":"M-%05d"}`, rowNo))); err != nil {
			return err
		}
	}
	return nil
}

type passThroughValidator struct{}

func (passThroughValidator) ValidateRow(
	_ context.Context,
	_ Claim,
	rowNo int,
	raw json.RawMessage,
) (ValidatedRow, error) {
	return ValidatedRow{
		RowNo: rowNo, RawJSON: raw, NormalizedJSON: raw, State: RowValid,
	}, nil
}

type recordingPreparedValidator struct{ preparedRows int }

func (validator *recordingPreparedValidator) Prepare(
	_ context.Context,
	_ Claim,
	rows []RawImportRow,
) (RowValidator, error) {
	validator.preparedRows = len(rows)
	return passThroughValidator{}, nil
}

func (*recordingPreparedValidator) ValidateRow(
	context.Context,
	Claim,
	int,
	json.RawMessage,
) (ValidatedRow, error) {
	return ValidatedRow{}, ErrImportValidatorUnavailable
}

type memoryObjectStorage struct{ body []byte }

func (storage memoryObjectStorage) Get(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(storage.body)), nil
}

type memoryWorkerStore struct {
	claim        Claim
	rows         map[int]ValidatedRow
	batchSizes   []int
	completed    bool
	failedReason string
}

func (store *memoryWorkerStore) ListTenantIDs(context.Context) ([]string, error) {
	return []string{store.claim.TenantID}, nil
}

func (store *memoryWorkerStore) ClaimForValidation(
	context.Context,
	string,
	string,
	time.Duration,
) (*Claim, error) {
	claim := store.claim
	claim.Attempt++
	claim.LeaseToken = uuid.NewString()
	claim.ResumeAfterRow = 0
	for rowNo := range store.rows {
		if rowNo > claim.ResumeAfterRow {
			claim.ResumeAfterRow = rowNo
		}
	}
	store.claim = claim
	return &claim, nil
}

func (*memoryWorkerStore) Heartbeat(context.Context, Claim, string, time.Duration) error {
	return nil
}

func (store *memoryWorkerStore) UpsertRows(
	_ context.Context,
	_ Claim,
	_ string,
	rows []ValidatedRow,
) error {
	store.batchSizes = append(store.batchSizes, len(rows))
	for _, row := range rows {
		store.rows[row.RowNo] = row
	}
	return nil
}

func (store *memoryWorkerStore) CompleteValidation(context.Context, Claim, string) error {
	store.completed = true
	return nil
}

func (store *memoryWorkerStore) FailValidation(
	_ context.Context,
	_ Claim,
	_ string,
	reason string,
) error {
	store.failedReason = reason
	return nil
}

func validWorkerClaim() Claim {
	digest := sha256.Sum256([]byte("semantic-import-fixture"))
	return Claim{
		ImportID: uuid.NewString(), TenantID: uuid.NewString(), DomainID: uuid.NewString(),
		AssetType: AssetMetric, FileObjectURI: "minio://semantic-imports/file.xlsx",
		FileHash: hex.EncodeToString(digest[:]), FileName: "metric.xlsx",
		LeaseToken: uuid.NewString(), Attempt: 0,
	}
}

func TestValidationIssueBounds(t *testing.T) {
	valid := ValidationIssue{
		Column: "formula", Code: "IMPORT_FORMULA_CYCLE",
		Message: "formula contains a dependency cycle", Expected: "acyclic formula",
	}
	if !validIssue(valid) {
		t.Fatal("valid issue rejected")
	}
	valid.Message = strings.Repeat("x", 2049)
	if validIssue(valid) {
		t.Fatal("oversized issue accepted")
	}
}
