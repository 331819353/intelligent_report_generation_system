package reportasset

import (
	"context"
	"errors"
	"testing"
	"time"
)

type noopGraphWriter struct{}

func (noopGraphWriter) Upsert(context.Context, ReportGraphProjection) error { return nil }
func (noopGraphWriter) Remove(context.Context, ReportGraphProjection) error { return nil }

// sourceGoneStore 模拟一条孤儿 outbox 行：它指向的报告版本已不存在。
type sourceGoneStore struct {
	ProjectionRuntimeStore
	claims     int
	finishedAs error
}

func (store *sourceGoneStore) ClaimExtraction(
	context.Context, string, time.Duration,
) (*ExtractionClaim, error) {
	store.claims++
	if store.claims > 1 {
		// 终结后不应再被领取；返回 nil 模拟 attempt<10 谓词把它排除在外。
		return nil, nil
	}
	return &ExtractionClaim{ID: "claim-1", TenantID: "tenant-1"}, nil
}

func (store *sourceGoneStore) Extract(context.Context, ExtractionClaim) error {
	return ErrAssetSourceGone
}

func (store *sourceGoneStore) FinishExtraction(
	_ context.Context, _ ExtractionClaim, runErr error,
) error {
	store.finishedAs = runErr
	return nil
}

func (store *sourceGoneStore) ClaimProjection(
	context.Context, string, time.Duration,
) (*AssetProjectionClaim, error) {
	return nil, nil
}

// 源版本已消失是永久性失败：Worker 必须把它终结掉，并且**不能**把它当作
// Worker 错误上报——否则每个轮询周期都会为同一条孤儿行打一条无法处置的错误日志，
// 把运维真正需要看到的失败淹没掉。
func TestVanishedSourceIsTerminalAndNotReportedAsAWorkerError(t *testing.T) {
	store := &sourceGoneStore{}
	worker, err := NewProjectionRuntimeWorker(store, noopGraphWriter{})
	if err != nil {
		t.Fatalf("NewProjectionRuntimeWorker() error = %v", err)
	}
	processed, runErr := worker.ProcessNext(context.Background(), "tenant-1", time.Minute)
	if !processed {
		t.Fatal("a claimed orphan row must count as processed work")
	}
	if runErr != nil {
		t.Fatalf("a handled permanent failure must not surface as a worker error: %v", runErr)
	}
	if !errors.Is(store.finishedAs, ErrAssetSourceGone) {
		t.Fatalf("the permanent reason must reach FinishExtraction, got %v", store.finishedAs)
	}
}

// 普通（瞬时）失败仍必须照常上报，终结逻辑不能吞掉真实错误。
func TestTransientExtractionFailuresStillSurface(t *testing.T) {
	store := &transientFailureStore{failure: errors.New("warehouse timeout")}
	worker, err := NewProjectionRuntimeWorker(store, noopGraphWriter{})
	if err != nil {
		t.Fatalf("NewProjectionRuntimeWorker() error = %v", err)
	}
	processed, runErr := worker.ProcessNext(context.Background(), "tenant-1", time.Minute)
	if !processed || runErr == nil {
		t.Fatalf("a transient failure must still be reported, processed=%v err=%v", processed, runErr)
	}
}

type transientFailureStore struct {
	ProjectionRuntimeStore
	failure error
}

func (store *transientFailureStore) ClaimExtraction(
	context.Context, string, time.Duration,
) (*ExtractionClaim, error) {
	return &ExtractionClaim{ID: "claim-1", TenantID: "tenant-1"}, nil
}

func (store *transientFailureStore) Extract(context.Context, ExtractionClaim) error {
	return store.failure
}

func (store *transientFailureStore) FinishExtraction(
	context.Context, ExtractionClaim, error,
) error {
	return nil
}
