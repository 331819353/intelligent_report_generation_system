package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type contextKey struct{}

// New 生成符合 UUID v4 位布局的请求标识。
func New() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(value[:])
}

// WithContext 将请求标识写入上下文，供日志和错误响应关联使用。
func WithContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

// FromContext 从上下文读取请求标识；不存在时返回空字符串。
func FromContext(ctx context.Context) string {
	value, _ := ctx.Value(contextKey{}).(string)
	return value
}
