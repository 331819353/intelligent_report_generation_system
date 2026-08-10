package datarequest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type controlledExportQueueFixture struct {
	command ControlledExportCommand
}

func TestControlledDownloadPolicyEnforcesExpiryAndCount(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	if err := ValidateControlledDownload(now, ControlledExportReady, now.Add(time.Hour), 2, 3); err != nil {
		t.Fatal(err)
	}
	if err := ValidateControlledDownload(now, ControlledExportReady, now, 0, 3); !errors.Is(err, ErrControlledExportExpired) {
		t.Fatalf("expiry err=%v", err)
	}
	if err := ValidateControlledDownload(now, ControlledExportReady, now.Add(time.Hour), 3, 3); !errors.Is(err, ErrControlledExportLimit) {
		t.Fatalf("limit err=%v", err)
	}
	if err := ValidateControlledDownload(now, ControlledExportPending, now.Add(time.Hour), 0, 3); !errors.Is(err, ErrControlledExportNotReady) {
		t.Fatalf("pending err=%v", err)
	}
}

func (queue *controlledExportQueueFixture) EnqueueControlledExport(
	_ context.Context, command ControlledExportCommand,
) (ControlledExportJob, error) {
	queue.command = command
	return ControlledExportJob{
		JobID: uuid.NewString(), DataRequestID: command.DataRequestID,
		State:     ControlledExportPending,
		ExpiresAt: command.ExpiresAt, MaxDownloads: command.MaxDownloads,
	}, nil
}

func TestExportBridgeImposesExpiryAndDownloadLimitWithoutRows(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	identity := testIdentity()
	queue := &controlledExportQueueFixture{}
	bridge, err := NewExportBridge(queue)
	if err != nil {
		t.Fatal(err)
	}
	bridge.now = func() time.Time { return now }
	request := Request{
		ID: uuid.NewString(), TenantID: identity.TenantID, DomainID: identity.DomainID,
		State: StateApproved, SensitivityLevel: SensitivityRestricted,
		SecurityCosignUserID: uuid.NewString(), RequiredFields: []FieldRef{{
			DatasetVersionID: uuid.NewString(), FieldID: "customer_id",
		}},
	}
	request.ApproverUserIDs = []string{identity.ActorID}
	job, err := bridge.Enqueue(context.Background(), identity, request)
	if err != nil || !job.ExpiresAt.Equal(now.Add(24*time.Hour)) || job.MaxDownloads != 3 ||
		queue.command.RequestHash == "" || queue.command.DataRequestID != request.ID {
		t.Fatalf("job=%#v command=%#v err=%v", job, queue.command, err)
	}
	request.SecurityCosignUserID = ""
	if _, err := bridge.Enqueue(context.Background(), identity, request); err != ErrSecurityCosignRequired {
		t.Fatalf("missing cosign err=%v", err)
	}
}
