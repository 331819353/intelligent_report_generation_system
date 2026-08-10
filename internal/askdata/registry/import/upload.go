package registryimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const MaxImportUploadBytes = int64(50 << 20)

var (
	ErrImportUploadInvalid  = errors.New("semantic import upload is invalid")
	ErrImportUploadTooLarge = errors.New("semantic import upload exceeds the size limit")
)

type ImportUploadStorage interface {
	Put(context.Context, string, string, io.Reader, int64, string) error
}

type ImportBatchCreator interface {
	CreateImport(context.Context, CreateImportInput) (SemanticImport, bool, error)
}

type UploadInput struct {
	TenantID, DomainID, ActorID string
	AssetType                   AssetType
	Filename, ContentType       string
	Size                        int64
	Body                        io.Reader
}

type UploadResult struct {
	ImportID  string    `json:"importId"`
	AssetType AssetType `json:"assetType"`
	State     State     `json:"state"`
	Created   bool      `json:"created"`
}

type UploadService struct {
	storage ImportUploadStorage
	store   ImportBatchCreator
	bucket  string
}

func NewUploadService(
	storage ImportUploadStorage,
	store ImportBatchCreator,
	bucket string,
) *UploadService {
	return &UploadService{storage: storage, store: store, bucket: strings.TrimSpace(bucket)}
}

func (service *UploadService) Upload(
	ctx context.Context,
	input UploadInput,
) (UploadResult, error) {
	if service == nil || service.storage == nil || service.store == nil ||
		!boundedText(service.bucket, 255) || !canonicalUUID(input.TenantID) ||
		!canonicalUUID(input.DomainID) || !canonicalUUID(input.ActorID) ||
		!input.AssetType.Valid() || input.Body == nil || input.Size < 1 ||
		!validImportFilename(input.Filename) {
		return UploadResult{}, ErrImportUploadInvalid
	}
	if input.Size > MaxImportUploadBytes {
		return UploadResult{}, ErrImportUploadTooLarge
	}
	extension, contentType, ok := importFileContract(input.Filename)
	if !ok {
		return UploadResult{}, ErrImportUploadInvalid
	}
	payload, err := io.ReadAll(io.LimitReader(input.Body, MaxImportUploadBytes+1))
	if err != nil {
		return UploadResult{}, err
	}
	if int64(len(payload)) != input.Size {
		if int64(len(payload)) > MaxImportUploadBytes {
			return UploadResult{}, ErrImportUploadTooLarge
		}
		return UploadResult{}, ErrImportUploadInvalid
	}
	digest := sha256.Sum256(payload)
	fileHash := hex.EncodeToString(digest[:])
	key := fmt.Sprintf(
		"semantic-imports/%s/%s/%s/%s.%s",
		input.TenantID, input.DomainID, strings.ToLower(string(input.AssetType)), fileHash, extension,
	)
	if err := service.storage.Put(
		ctx, service.bucket, key, bytes.NewReader(payload), int64(len(payload)), contentType,
	); err != nil {
		return UploadResult{}, err
	}
	batch, created, err := service.store.CreateImport(ctx, CreateImportInput{
		TenantID: input.TenantID, DomainID: input.DomainID, AssetType: input.AssetType,
		FileObjectURI: "minio://" + service.bucket + "/" + key,
		FileHash:      fileHash, FileName: path.Base(input.Filename), CreatedBy: input.ActorID,
	})
	if err != nil {
		// The content-addressed object is intentionally retained. A concurrent
		// retry may already reference the same immutable key, so deleting it here
		// would turn a database failure into cross-request data loss.
		return UploadResult{}, err
	}
	return UploadResult{
		ImportID: batch.ID, AssetType: batch.AssetType, State: batch.State, Created: created,
	}, nil
}

func validImportFilename(value string) bool {
	value = strings.TrimSpace(value)
	return boundedText(value, 255) && !strings.ContainsAny(value, `/\\`) &&
		path.Base(value) == value && value != "." && value != ".."
}

func importFileContract(filename string) (string, string, bool) {
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(filename)), ".")
	switch extension {
	case "csv":
		return extension, "text/csv", true
	case "xls":
		return extension, "application/vnd.ms-excel", true
	case "xlsx":
		return extension, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", true
	default:
		return "", "", false
	}
}
