package datasource

import (
	"context"
	"io"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type MinIOStorage struct{ client *minio.Client }

// NewMinIOStorage 创建兼容 S3 的对象存储客户端。
func NewMinIOStorage(endpoint, accessKey, secretKey string, useSSL bool) (*MinIOStorage, error) {
	client, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: useSSL})
	if err != nil {
		return nil, err
	}
	return &MinIOStorage{client: client}, nil
}

// Put 按给定对象键上传文件内容。
func (s *MinIOStorage) Put(ctx context.Context, bucket, key string, body io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, bucket, key, body, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

// Get 获取对象流，并提前读取元数据以尽早暴露不存在错误。
func (s *MinIOStorage) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	object, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	if _, err := object.Stat(); err != nil {
		object.Close()
		return nil, err
	}
	return object, nil
}

// Delete 删除指定对象。
func (s *MinIOStorage) Delete(ctx context.Context, bucket, key string) error {
	return s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}
