package datasource

import (
	"context"
	"errors"
	"time"
)

var (
	ErrConnectionTestQueueUnavailable = errors.New("asynchronous connection test queue is unavailable")
	ErrConnectionTestNotFound         = errors.New("connection test job was not found")
	ErrConnectionTestPending          = errors.New("connection test is pending")
	ErrConnectionTestFailed           = errors.New("connection test failed")
	ErrConnectionTestLeaseLost        = errors.New("connection test worker lease was lost")
	ErrIdempotencyKeyInvalid          = errors.New("idempotency key must contain at most 256 bytes")
)

type ConnectionTestJobStatus string

const (
	ConnectionTestQueued    ConnectionTestJobStatus = "QUEUED"
	ConnectionTestRunning   ConnectionTestJobStatus = "RUNNING"
	ConnectionTestSucceeded ConnectionTestJobStatus = "SUCCEEDED"
	ConnectionTestFailed    ConnectionTestJobStatus = "FAILED"
	ConnectionTestCancelled ConnectionTestJobStatus = "CANCELLED"
)

// ConnectionTestJob 是 API 可见的安全任务快照。租约、worker 身份、配置摘要和
// 凭据均不会序列化到该对象。
type ConnectionTestJob struct {
	ID              string                  `json:"id"`
	DataSourceID    string                  `json:"dataSourceId"`
	ConfigVersionID string                  `json:"configVersionId"`
	Status          ConnectionTestJobStatus `json:"status"`
	Attempt         int                     `json:"attempt"`
	MaxAttempts     int                     `json:"maxAttempts"`
	ErrorCode       string                  `json:"errorCode,omitempty"`
	ErrorMessage    string                  `json:"errorMessage,omitempty"`
	ServerVersion   string                  `json:"serverVersion,omitempty"`
	LatencyMS       int64                   `json:"latencyMs,omitempty"`
	RequestedAt     time.Time               `json:"requestedAt"`
	StartedAt       *time.Time              `json:"startedAt,omitempty"`
	CompletedAt     *time.Time              `json:"completedAt,omitempty"`
	TestedAt        *time.Time              `json:"testedAt,omitempty"`
	ExpiresAt       *time.Time              `json:"expiresAt,omitempty"`
}

func (j ConnectionTestJob) Active() bool {
	return j.Status == ConnectionTestQueued || j.Status == ConnectionTestRunning
}

type ConnectionTestClaim struct {
	Job        ConnectionTestJob
	TenantID   string
	LeaseToken string
	Source     Source
}

// ConnectionTestJobRepository 是 API 控制面能力。实现只能通过数据库入队函数
// 创建任务，不能直接形成成功证明。
type ConnectionTestJobRepository interface {
	EnqueueConnectionTest(context.Context, string, string, string, string) (ConnectionTestJob, error)
	GetConnectionTest(context.Context, string, string, string) (ConnectionTestJob, error)
	LatestConnectionTest(context.Context, string, string, string, string) (*ConnectionTestJob, error)
}

// ConnectionTestWorkerRepository 是专用 worker 数据面能力。所有状态变化均由
// SECURITY DEFINER 函数校验租约并生成数据库时间。
type ConnectionTestWorkerRepository interface {
	ListConnectionTestTenantIDs(context.Context) ([]string, error)
	ClaimConnectionTest(context.Context, string, string, time.Duration) (*ConnectionTestClaim, error)
	HeartbeatConnectionTest(context.Context, string, string, string, time.Duration) error
	CompleteConnectionTest(context.Context, string, string, string, string, int64) error
	FailConnectionTest(context.Context, string, string, string, string, bool) (ConnectionTestJobStatus, error)
}
