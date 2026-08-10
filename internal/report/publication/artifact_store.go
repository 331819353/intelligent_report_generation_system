package publication

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type ArtifactStore interface {
	PutTemporary(context.Context, string, []byte) error
	Promote(context.Context, string, string) error
	Delete(context.Context, string) error
	Read(context.Context, string) ([]byte, error)
}

type MinIOArtifactStore struct {
	client *minio.Client
	bucket string
}

func NewMinIOArtifactStore(client *minio.Client, bucket string) (*MinIOArtifactStore, error) {
	if client == nil || strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("MinIO client and bucket are required")
	}
	return &MinIOArtifactStore{client: client, bucket: bucket}, nil
}

func NewMinIOArtifactStoreWithCredentials(endpoint, accessKey, secretKey string, useSSL bool, bucket string) (*MinIOArtifactStore, error) {
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: useSSL})
	if err != nil {
		return nil, err
	}
	return NewMinIOArtifactStore(client, bucket)
}

func (store *MinIOArtifactStore) PutTemporary(ctx context.Context, key string, body []byte) error {
	_, err := store.client.PutObject(ctx, store.bucket, cleanObjectKey(key), bytes.NewReader(body), int64(len(body)), minio.PutObjectOptions{ContentType: "application/json"})
	return err
}

func (store *MinIOArtifactStore) Promote(ctx context.Context, temporaryKey, finalKey string) error {
	var err error
	temporaryKey, err = store.objectKey(temporaryKey)
	if err != nil {
		return err
	}
	finalKey, err = store.objectKey(finalKey)
	if err != nil {
		return err
	}
	if _, err := store.client.StatObject(ctx, store.bucket, finalKey, minio.StatObjectOptions{}); err == nil {
		removeErr := store.client.RemoveObject(ctx, store.bucket, temporaryKey, minio.RemoveObjectOptions{})
		if removeErr != nil {
			var response minio.ErrorResponse
			if !errors.As(removeErr, &response) || response.Code != "NoSuchKey" {
				return removeErr
			}
		}
		return nil
	}
	_, err = store.client.CopyObject(ctx, minio.CopyDestOptions{Bucket: store.bucket, Object: finalKey}, minio.CopySrcOptions{Bucket: store.bucket, Object: temporaryKey})
	if err != nil {
		return err
	}
	return store.client.RemoveObject(ctx, store.bucket, temporaryKey, minio.RemoveObjectOptions{})
}

func (store *MinIOArtifactStore) Delete(ctx context.Context, key string) error {
	return store.client.RemoveObject(ctx, store.bucket, cleanObjectKey(key), minio.RemoveObjectOptions{})
}

func (store *MinIOArtifactStore) Read(ctx context.Context, key string) ([]byte, error) {
	objectKey, err := store.objectKey(key)
	if err != nil {
		return nil, err
	}
	object, err := store.client.GetObject(ctx, store.bucket, objectKey, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()
	return io.ReadAll(io.LimitReader(object, 5*1024*1024+1))
}

func (store *MinIOArtifactStore) objectKey(value string) (string, error) {
	if strings.HasPrefix(value, "minio://") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host != store.bucket || strings.Trim(parsed.Path, "/") == "" {
			return "", errors.New("report artifact URI is invalid or addresses another bucket")
		}
		return cleanObjectKey(parsed.Path), nil
	}
	return cleanObjectKey(value), nil
}

func (store *MinIOArtifactStore) URI(key string) string {
	return "minio://" + store.bucket + "/" + cleanObjectKey(key)
}

func cleanObjectKey(key string) string {
	return strings.TrimLeft(strings.ReplaceAll(key, "..", "_"), "/")
}
