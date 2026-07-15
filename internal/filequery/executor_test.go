package filequery

import (
	"context"
	"errors"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/datasource"
)

type fakeVersionReader struct {
	started chan struct{}
	version datasource.FileVersion
	tables  []datasource.FileTableData
	err     error
}

func (f fakeVersionReader) ReadVersionTables(ctx context.Context, _, _ string, _ int64) (datasource.FileVersion, []datasource.FileTableData, error) {
	if f.started != nil {
		close(f.started)
		<-ctx.Done()
		return datasource.FileVersion{}, nil, ctx.Err()
	}
	return f.version, f.tables, f.err
}

func TestExecutorRejectsVersionFromAnotherFileAsset(t *testing.T) {
	executor := NewExecutor(fakeVersionReader{version: datasource.FileVersion{FileAsset: datasource.FileAsset{ID: "asset-other"}}})
	_, err := executor.Execute(context.Background(), datasource.Source{FileAssetID: "asset-expected", RuntimeQuota: datasource.Quota{MaxExcelFileBytes: 1024}}, "query-1", fileInput(t).Document, fileInput(t).Tables, "version-1", nil, fileInput(t).Scope, nil, nil, 10)
	if !errors.Is(err, ErrFileVersionMismatch) {
		t.Fatalf("version mismatch error=%v", err)
	}
}

func TestExecutorCanCancelAnActiveFileRead(t *testing.T) {
	started := make(chan struct{})
	executor := NewExecutor(fakeVersionReader{started: started})
	finished := make(chan error, 1)
	input := fileInput(t)
	go func() {
		_, err := executor.Execute(context.Background(), datasource.Source{FileAssetID: "asset-1", RuntimeQuota: datasource.Quota{MaxExcelFileBytes: 1024}}, "query-1", input.Document, input.Tables, "version-1", input.Parameters, input.Scope, nil, nil, 10)
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("文件读取未开始")
	}
	cancelled, err := executor.Cancel(context.Background(), "query-1")
	if err != nil || !cancelled {
		t.Fatalf("cancelled=%v err=%v", cancelled, err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("execute error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消后文件读取未结束")
	}
}
