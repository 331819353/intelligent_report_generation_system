package materialization

import (
	"context"
	"errors"
	"testing"
)

type snapshotReaderStub struct {
	meta  MaterializationMeta
	err   error
	calls int
}

func (stub *snapshotReaderStub) GetLatestSnapshot(
	context.Context,
	string,
	string,
) (MaterializationMeta, error) {
	stub.calls++
	return stub.meta, stub.err
}

func TestSnapshotServiceUsesOnlyControlPlaneReader(t *testing.T) {
	reader := &snapshotReaderStub{meta: MaterializationMeta{
		MaterializationID: "materialization-id",
		SnapshotVersion:   "snapshot-10",
		QualityStatus:     SnapshotQualityOK,
	}}
	service := NewSnapshotService(reader)
	meta, err := service.GetLatestSnapshot(
		context.Background(), "tenant-id", "materialization-id",
	)
	if err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 || meta.SnapshotVersion != "snapshot-10" {
		t.Fatalf("control reader calls=%d meta=%+v", reader.calls, meta)
	}
}

func TestSnapshotServiceRejectsMissingControlReader(t *testing.T) {
	_, err := NewSnapshotService(nil).GetLatestSnapshot(
		context.Background(), "tenant-id", "materialization-id",
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v", err)
	}
}
