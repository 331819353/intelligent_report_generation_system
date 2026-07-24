package datasource

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
)

// ConnectionTestWorker 只执行数据库已经冻结的精确数据源版本。它不能自行选择
// config hash、证明时间或有效期，最终提交仍由数据库租约函数校验。
type ConnectionTestWorker struct {
	repository ConnectionTestWorkerRepository
	connectors map[Type]Connector
	timeout    time.Duration
}

func NewConnectionTestWorker(
	repository ConnectionTestWorkerRepository,
	timeout time.Duration,
	connectors ...Connector,
) *ConnectionTestWorker {
	registered := make(map[Type]Connector, len(connectors))
	for _, connector := range connectors {
		registered[connector.Type()] = connector
	}
	return &ConnectionTestWorker{
		repository: repository,
		connectors: registered,
		timeout:    timeout,
	}
}

func (w *ConnectionTestWorker) TenantIDs(ctx context.Context) ([]string, error) {
	return w.repository.ListConnectionTestTenantIDs(ctx)
}

func (w *ConnectionTestWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	claim, err := w.repository.ClaimConnectionTest(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	connector := w.connectors[claim.Source.Type]
	if connector == nil {
		_, failErr := w.repository.FailConnectionTest(
			ctx, claim.TenantID, claim.Job.ID, claim.LeaseToken,
			"CONNECTION_FAILED", false,
		)
		return true, failErr
	}

	testCtx, cancelTest := context.WithTimeout(ctx, w.timeout)
	heartbeatDone := make(chan error, 1)
	go w.heartbeat(testCtx, cancelTest, claim, lease, heartbeatDone)

	result, testErr := connector.Test(testCtx, claim.Source)
	testContextErr := testCtx.Err()
	cancelTest()
	heartbeatErr := <-heartbeatDone
	if heartbeatErr != nil {
		return true, heartbeatErr
	}
	if ctx.Err() != nil {
		return true, ctx.Err()
	}
	if testErr != nil || errors.Is(testContextErr, context.DeadlineExceeded) {
		code, retryable := safeConnectionTestFailure(testContextErr, claim.Source.Type)
		_, failErr := w.repository.FailConnectionTest(
			ctx, claim.TenantID, claim.Job.ID, claim.LeaseToken,
			code, retryable,
		)
		return true, failErr
	}
	if err := w.repository.CompleteConnectionTest(
		ctx, claim.TenantID, claim.Job.ID, claim.LeaseToken,
		safeServerVersion(result.ServerVersion), result.LatencyMS,
	); err != nil {
		return true, err
	}
	return true, nil
}

func (w *ConnectionTestWorker) heartbeat(
	ctx context.Context,
	cancelTest context.CancelFunc,
	claim *ConnectionTestClaim,
	lease time.Duration,
	done chan<- error,
) {
	interval := lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-timer.C:
			err := w.repository.HeartbeatConnectionTest(
				ctx, claim.TenantID, claim.Job.ID, claim.LeaseToken, lease,
			)
			if err != nil {
				cancelTest()
				done <- err
				return
			}
			timer.Reset(interval)
		}
	}
}

func safeConnectionTestFailure(testContextError error, sourceType Type) (string, bool) {
	if errors.Is(testContextError, context.DeadlineExceeded) {
		return "CONNECTION_TIMEOUT", true
	}
	if sourceType == TypeExcel {
		return "FILE_UNAVAILABLE", false
	}
	return "CONNECTION_FAILED", false
}

func safeServerVersion(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, value)
	lowerValue := strings.ToLower(value)
	for _, forbidden := range []string{
		"password", "passwd", "secret", "token=", "jdbc:", "://",
	} {
		if strings.Contains(lowerValue, forbidden) {
			return ""
		}
	}
	characters := []rune(strings.TrimSpace(value))
	if len(characters) > 256 {
		characters = characters[:256]
	}
	return string(characters)
}
