package registryimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestUploadServiceStoresImmutableContentAddressedFileAndCreatesBatch(t *testing.T) {
	payload := []byte("code,name\nmetric_a,Metric A\n")
	storage := &uploadFixtureStorage{}
	creator := &uploadFixtureCreator{}
	service := NewUploadService(storage, creator, "uploads")
	input := UploadInput{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
		AssetType: AssetMetric, Filename: "metrics.CSV", ContentType: "application/octet-stream",
		Size: int64(len(payload)), Body: bytes.NewReader(payload),
	}
	result, err := service.Upload(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	wantHash := hex.EncodeToString(digest[:])
	wantKey := "semantic-imports/" + input.TenantID + "/" + input.DomainID + "/metric/" + wantHash + ".csv"
	if storage.bucket != "uploads" || storage.key != wantKey || storage.contentType != "text/csv" || !bytes.Equal(storage.payload, payload) {
		t.Fatalf("stored = %q/%q %q %q", storage.bucket, storage.key, storage.contentType, storage.payload)
	}
	if creator.input.FileHash != wantHash || creator.input.FileObjectURI != "minio://uploads/"+wantKey ||
		creator.input.FileName != "metrics.CSV" || creator.input.CreatedBy != input.ActorID {
		t.Fatalf("create input = %#v", creator.input)
	}
	if !result.Created || result.ImportID == "" || result.State != StateUploaded {
		t.Fatalf("result = %#v", result)
	}
}

func TestUploadServiceRejectsUnsafeOrMismatchedFilesBeforeStorage(t *testing.T) {
	valid := UploadInput{
		TenantID: uuid.NewString(), DomainID: uuid.NewString(), ActorID: uuid.NewString(),
		AssetType: AssetMetric, Filename: "metrics.csv", Size: 1, Body: strings.NewReader("x"),
	}
	for _, mutate := range []func(*UploadInput){
		func(input *UploadInput) { input.Filename = "../metrics.csv" },
		func(input *UploadInput) { input.Filename = `..\\metrics.csv` },
		func(input *UploadInput) { input.Filename = "metrics.exe" },
		func(input *UploadInput) { input.Size = 2 },
		func(input *UploadInput) { input.AssetType = "metric" },
	} {
		input := valid
		input.Body = strings.NewReader("x")
		mutate(&input)
		storage := &uploadFixtureStorage{}
		_, err := NewUploadService(storage, &uploadFixtureCreator{}, "uploads").Upload(context.Background(), input)
		if !errors.Is(err, ErrImportUploadInvalid) || storage.key != "" {
			t.Errorf("input %#v error/storage = %v/%q", input, err, storage.key)
		}
	}
	tooLarge := valid
	tooLarge.Size = MaxImportUploadBytes + 1
	if _, err := NewUploadService(&uploadFixtureStorage{}, &uploadFixtureCreator{}, "uploads").Upload(context.Background(), tooLarge); !errors.Is(err, ErrImportUploadTooLarge) {
		t.Fatalf("too-large error = %v", err)
	}
}

type uploadFixtureStorage struct {
	bucket, key, contentType string
	payload                  []byte
	err                      error
}

func (storage *uploadFixtureStorage) Put(
	_ context.Context,
	bucket, key string,
	body io.Reader,
	_ int64,
	contentType string,
) error {
	storage.bucket, storage.key, storage.contentType = bucket, key, contentType
	storage.payload, _ = io.ReadAll(body)
	return storage.err
}

type uploadFixtureCreator struct {
	input CreateImportInput
	err   error
}

func (creator *uploadFixtureCreator) CreateImport(
	_ context.Context,
	input CreateImportInput,
) (SemanticImport, bool, error) {
	creator.input = input
	if creator.err != nil {
		return SemanticImport{}, false, creator.err
	}
	return SemanticImport{
		ID: uuid.NewString(), TenantID: input.TenantID, DomainID: input.DomainID,
		AssetType: input.AssetType, FileHash: input.FileHash, FileObjectURI: input.FileObjectURI,
		FileName: input.FileName, CreatedBy: input.CreatedBy, State: StateUploaded,
	}, true, nil
}
