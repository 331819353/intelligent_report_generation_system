package datasource

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeConnectionTestWorkerRepository struct {
	claim          *ConnectionTestClaim
	completed      bool
	serverVersion  string
	latencyMS      int64
	failed         bool
	failureCode    string
	retryable      bool
	heartbeatError error
	completeError  error
}

func (r *fakeConnectionTestWorkerRepository) ListConnectionTestTenantIDs(context.Context) ([]string, error) {
	return []string{"tenant-1"}, nil
}

func (r *fakeConnectionTestWorkerRepository) ClaimConnectionTest(
	context.Context, string, string, time.Duration,
) (*ConnectionTestClaim, error) {
	claim := r.claim
	r.claim = nil
	return claim, nil
}

func (r *fakeConnectionTestWorkerRepository) HeartbeatConnectionTest(
	context.Context, string, string, string, time.Duration,
) error {
	return r.heartbeatError
}

func (r *fakeConnectionTestWorkerRepository) CompleteConnectionTest(
	_ context.Context,
	_, _, _, serverVersion string,
	latencyMS int64,
) error {
	if r.completeError != nil {
		return r.completeError
	}
	r.completed = true
	r.serverVersion = serverVersion
	r.latencyMS = latencyMS
	return nil
}

func (r *fakeConnectionTestWorkerRepository) FailConnectionTest(
	_ context.Context,
	_, _, _, errorCode string,
	retryable bool,
) (ConnectionTestJobStatus, error) {
	r.failed = true
	r.failureCode = errorCode
	r.retryable = retryable
	return ConnectionTestFailed, nil
}

type connectionTestConnector struct {
	kind   Type
	result TestResult
	err    error
	wait   bool
	seen   Source
}

func (c *connectionTestConnector) Type() Type { return c.kind }
func (c *connectionTestConnector) Test(ctx context.Context, source Source) (TestResult, error) {
	c.seen = source
	if c.wait {
		<-ctx.Done()
		return TestResult{}, ctx.Err()
	}
	return c.result, c.err
}
func (c *connectionTestConnector) Sync(context.Context, Source) (SyncResult, error) {
	return SyncResult{}, nil
}
func (c *connectionTestConnector) Close(context.Context, Source) error { return nil }

func connectionTestClaim() *ConnectionTestClaim {
	return &ConnectionTestClaim{
		Job: ConnectionTestJob{
			ID: "job-1", DataSourceID: "source-1",
			ConfigVersionID: "version-1", Status: ConnectionTestRunning,
		},
		TenantID: "tenant-1", LeaseToken: "lease-1",
		Source: Source{
			ID: "source-1", TenantID: "tenant-1", Type: TypeMySQL,
			ConfigVersionID: "version-1", ConfigHash: strings.Repeat("a", 64),
			SecretRef: "encrypted://never-returned",
		},
	}
}

func TestConnectionTestWorkerCompletesFrozenVersionAndSanitizesServerVersion(t *testing.T) {
	repository := &fakeConnectionTestWorkerRepository{claim: connectionTestClaim()}
	connector := &connectionTestConnector{
		kind: TypeMySQL,
		result: TestResult{
			ServerVersion: " MySQL 8.4\npassword=must-not-cross-control-boundary ",
			LatencyMS:     23,
		},
	}
	worker := NewConnectionTestWorker(repository, time.Second, connector)

	processed, err := worker.ProcessNext(
		context.Background(), "tenant-1", "worker-1", 30*time.Second,
	)
	if err != nil || !processed || !repository.completed {
		t.Fatalf("processed=%v completed=%v err=%v", processed, repository.completed, err)
	}
	if repository.serverVersion != "" {
		t.Fatalf("credential-like server banner was not redacted: %q", repository.serverVersion)
	}
	if connector.seen.ConfigVersionID != "version-1" ||
		connector.seen.SecretRef != "encrypted://never-returned" {
		t.Fatalf("connector did not receive frozen source: %#v", connector.seen)
	}
}

func TestConnectionTestWorkerPersistsOnlySafeFailureClassification(t *testing.T) {
	repository := &fakeConnectionTestWorkerRepository{claim: connectionTestClaim()}
	connector := &connectionTestConnector{
		kind: TypeMySQL,
		err:  errors.New("driver failed password=super-secret dsn=mysql://private"),
	}
	worker := NewConnectionTestWorker(repository, time.Second, connector)

	processed, err := worker.ProcessNext(
		context.Background(), "tenant-1", "worker-1", 30*time.Second,
	)
	if err != nil || !processed || !repository.failed {
		t.Fatalf("processed=%v failed=%v err=%v", processed, repository.failed, err)
	}
	if repository.failureCode != "CONNECTION_FAILED" || repository.retryable {
		t.Fatalf("failure code=%s retryable=%v", repository.failureCode, repository.retryable)
	}
	if strings.Contains(repository.failureCode, "secret") ||
		strings.Contains(repository.failureCode, "driver") {
		t.Fatal("raw connector error crossed the worker persistence boundary")
	}
}

func TestConnectionTestWorkerRetriesTimeoutWithSafeCode(t *testing.T) {
	repository := &fakeConnectionTestWorkerRepository{claim: connectionTestClaim()}
	connector := &connectionTestConnector{kind: TypeMySQL, wait: true}
	worker := NewConnectionTestWorker(repository, 10*time.Millisecond, connector)

	processed, err := worker.ProcessNext(
		context.Background(), "tenant-1", "worker-1", 30*time.Second,
	)
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if repository.failureCode != "CONNECTION_TIMEOUT" || !repository.retryable {
		t.Fatalf("failure code=%s retryable=%v", repository.failureCode, repository.retryable)
	}
}

func TestConnectionTestWorkerFailsClosedWhenCompletionLeaseIsLost(t *testing.T) {
	repository := &fakeConnectionTestWorkerRepository{
		claim:         connectionTestClaim(),
		completeError: ErrConnectionTestLeaseLost,
	}
	connector := &connectionTestConnector{
		kind: TypeMySQL, result: TestResult{ServerVersion: "8.4"},
	}
	worker := NewConnectionTestWorker(repository, time.Second, connector)

	processed, err := worker.ProcessNext(
		context.Background(), "tenant-1", "worker-1", 30*time.Second,
	)
	if !processed || !errors.Is(err, ErrConnectionTestLeaseLost) {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if repository.failed {
		t.Fatal("lost completion lease was overwritten with a failure")
	}
}
