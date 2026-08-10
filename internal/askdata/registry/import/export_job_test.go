package registryimport

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestExportWorkerGeneratesContentAddressedPinnedArtifact(t *testing.T) {
	tenantID, domainID, versionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	claim := ExportJobClaim{SemanticExportJob: SemanticExportJob{
		ID: uuid.NewString(), TenantID: tenantID, DomainID: domainID,
		AssetTypes:       []AssetType{AssetMetric},
		PinnedVersionIDs: map[AssetType][]string{AssetMetric: {versionID}},
		LeaseToken:       uuid.NewString(), State: ExportRunning,
	}}
	store := &exportWorkerStore{claim: &claim}
	catalog := &recordingExportCatalog{dataset: ExportDataset{Sheets: []ExportSheet{{
		AssetType: AssetMetric,
		Rows:      []map[string]string{completeExportRow(t, AssetMetric)},
	}}}}
	storage := &exportWorkerStorage{}
	worker := NewExportWorker(store, NewExportService(catalog), storage, "uploads")
	processed, err := worker.ProcessNext(context.Background(), tenantID, "worker-1", time.Minute)
	if err != nil || !processed {
		t.Fatalf("processed/err = %v/%v", processed, err)
	}
	if !catalog.selection.System || catalog.selection.DomainID != domainID ||
		catalog.selection.PinnedVersionIDs[AssetMetric][0] != versionID {
		t.Fatalf("selection = %#v", catalog.selection)
	}
	if store.completed == nil || store.failedCode != "" ||
		!strings.HasPrefix(storage.key, "semantic-exports/"+tenantID+"/"+domainID+"/"+claim.ID+"/") ||
		!strings.HasSuffix(storage.key, ".xlsx") || storage.contentType == "" || len(storage.body) == 0 {
		t.Fatalf("completion/storage = %#v %s/%s (%d)", store.completed, storage.bucket, storage.key, len(storage.body))
	}
	if store.objectURI != "s3://uploads/"+storage.key {
		t.Fatalf("object URI = %q", store.objectURI)
	}
}

func TestExportWorkerClosesPermanentContractFailure(t *testing.T) {
	claim := ExportJobClaim{SemanticExportJob: SemanticExportJob{
		ID: uuid.NewString(), TenantID: uuid.NewString(), DomainID: uuid.NewString(),
		AssetTypes:       []AssetType{AssetMetric},
		PinnedVersionIDs: map[AssetType][]string{AssetMetric: {uuid.NewString()}},
		LeaseToken:       uuid.NewString(), State: ExportRunning,
	}}
	store := &exportWorkerStore{claim: &claim}
	catalog := &recordingExportCatalog{dataset: ExportDataset{Sheets: []ExportSheet{{
		AssetType: AssetMetric, Rows: []map[string]string{{"code": "missing-columns"}},
	}}}}
	worker := NewExportWorker(store, NewExportService(catalog), &exportWorkerStorage{}, "uploads")
	processed, err := worker.ProcessNext(context.Background(), claim.TenantID, "worker-1", time.Minute)
	if err != nil || !processed {
		t.Fatalf("processed/err = %v/%v", processed, err)
	}
	if store.failedCode != "EXPORT_CONTRACT_INVALID" || store.retryable {
		t.Fatalf("failure = %q retryable=%v", store.failedCode, store.retryable)
	}
}

func TestSemanticExportJobEffectiveStateExpiresReadyArtifact(t *testing.T) {
	now := time.Now().UTC()
	job := SemanticExportJob{State: ExportReady, ExpiresAt: now.Add(-time.Second)}
	if got := job.EffectiveState(now); got != ExportExpired {
		t.Fatalf("effective state = %s", got)
	}
	job.State = ExportFailed
	if got := job.EffectiveState(now); got != ExportFailed {
		t.Fatalf("failed effective state = %s", got)
	}
}

func completeExportRow(t *testing.T, assetType AssetType) map[string]string {
	t.Helper()
	definition, err := TemplateDefinitionFor(assetType)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]string, len(definition.Columns))
	for _, column := range definition.Columns {
		result[column.Name] = ""
	}
	result["code"] = "metric_code"
	return result
}

type exportWorkerStore struct {
	claim                 *ExportJobClaim
	completed             *ExportArtifact
	objectURI, failedCode string
	retryable             bool
}

func (store *exportWorkerStore) ListTenantIDs(context.Context) ([]string, error) {
	if store.claim == nil {
		return nil, nil
	}
	return []string{store.claim.TenantID}, nil
}

func (store *exportWorkerStore) Claim(
	context.Context, string, string, time.Duration,
) (*ExportJobClaim, error) {
	claim := store.claim
	store.claim = nil
	return claim, nil
}

func (store *exportWorkerStore) Complete(
	_ context.Context,
	_ ExportJobClaim,
	_ string,
	artifact ExportArtifact,
	objectURI string,
) error {
	store.completed = &artifact
	store.objectURI = objectURI
	return nil
}

func (store *exportWorkerStore) Fail(
	_ context.Context,
	_ ExportJobClaim,
	_, code string,
	retryable bool,
) error {
	store.failedCode, store.retryable = code, retryable
	return nil
}

type recordingExportCatalog struct {
	dataset   ExportDataset
	selection ExportSelection
	err       error
}

func (catalog *recordingExportCatalog) CountExportRows(
	context.Context, ExportSelection,
) (int, error) {
	return 0, errors.New("not used")
}

func (catalog *recordingExportCatalog) LoadExportDataset(
	_ context.Context,
	selection ExportSelection,
) (ExportDataset, error) {
	catalog.selection = selection
	return catalog.dataset, catalog.err
}

type exportWorkerStorage struct {
	bucket, key, contentType string
	body                     []byte
	err                      error
}

func (storage *exportWorkerStorage) Put(
	_ context.Context,
	bucket, key string,
	body io.Reader,
	_ int64,
	contentType string,
) error {
	storage.bucket, storage.key, storage.contentType = bucket, key, contentType
	storage.body, _ = io.ReadAll(body)
	return storage.err
}
