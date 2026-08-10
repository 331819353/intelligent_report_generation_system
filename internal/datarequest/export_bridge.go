package datarequest

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrControlledExportInvalid = errors.New("controlled detail export is invalid")
var (
	ErrControlledExportNotReady = errors.New("controlled detail export is not ready")
	ErrControlledExportExpired  = errors.New("controlled detail export expired")
	ErrControlledExportLimit    = errors.New("controlled detail export download limit reached")
)

type ControlledExportState string

const (
	ControlledExportPending ControlledExportState = "PENDING"
	ControlledExportReady   ControlledExportState = "READY"
)

const (
	DefaultExportTTL       = 24 * time.Hour
	DefaultExportDownloads = 3
)

type ControlledExportCommand struct {
	TenantID      string
	DomainID      string
	ActorID       string
	DataRequestID string
	FieldRefs     []FieldRef
	Sensitivity   Sensitivity
	ExpiresAt     time.Time
	MaxDownloads  int
	RequestHash   askdata.ContentHash
}

type ControlledExportJob struct {
	JobID         string                `json:"jobId"`
	DataRequestID string                `json:"dataRequestId"`
	State         ControlledExportState `json:"state"`
	ExpiresAt     time.Time             `json:"expiresAt"`
	MaxDownloads  int                   `json:"maxDownloads"`
}

type ControlledDownloadGrant struct {
	JobID              string
	StorageKey         string
	ContentHash        askdata.ContentHash
	ExpiresAt          time.Time
	RemainingDownloads int
}

type ControlledExportQueue interface {
	EnqueueControlledExport(context.Context, ControlledExportCommand) (ControlledExportJob, error)
}

func ValidateControlledDownload(
	now time.Time, state ControlledExportState, expiresAt time.Time, downloadCount, maxDownloads int,
) error {
	if now.IsZero() || expiresAt.IsZero() || maxDownloads < 1 || downloadCount < 0 || downloadCount > maxDownloads {
		return ErrControlledExportInvalid
	}
	if !expiresAt.After(now.UTC()) {
		return ErrControlledExportExpired
	}
	if downloadCount >= maxDownloads {
		return ErrControlledExportLimit
	}
	if state != ControlledExportReady {
		return ErrControlledExportNotReady
	}
	return nil
}

type ExportBridge struct {
	queue ControlledExportQueue
	now   func() time.Time
}

func NewExportBridge(queue ControlledExportQueue) (*ExportBridge, error) {
	if queue == nil {
		return nil, ErrControlledExportInvalid
	}
	return &ExportBridge{queue: queue, now: time.Now}, nil
}

// Enqueue accepts only an approved/in-progress request and emits no result
// rows, object URL or SQL. Download expiry/count are imposed by the bridge,
// not accepted from an API caller.
func (bridge *ExportBridge) Enqueue(
	ctx context.Context, identity Identity, request Request,
) (ControlledExportJob, error) {
	if bridge == nil || bridge.queue == nil || ctx == nil || !identity.Valid() ||
		request.TenantID != identity.TenantID || request.DomainID != identity.DomainID ||
		uuid.Validate(request.ID) != nil ||
		(request.State != StateApproved && request.State != StateInProgress) ||
		len(request.RequiredFields) == 0 || sensitivityRank(request.SensitivityLevel) < 0 {
		return ControlledExportJob{}, ErrControlledExportInvalid
	}
	if RequiresSecurityCosign(request.SensitivityLevel) && request.SecurityCosignUserID == "" {
		return ControlledExportJob{}, ErrSecurityCosignRequired
	}
	if !slices.Contains(request.ApproverUserIDs, identity.ActorID) &&
		identity.ActorID != request.AssigneeUserID {
		return ControlledExportJob{}, ErrControlledExportInvalid
	}
	fields, err := normalizeFields(request.RequiredFields)
	if err != nil {
		return ControlledExportJob{}, ErrControlledExportInvalid
	}
	hash, err := controlledExportRequestHash(request.ID, fields, request.SensitivityLevel)
	if err != nil {
		return ControlledExportJob{}, err
	}
	now := bridge.now().UTC()
	command := ControlledExportCommand{
		TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: identity.ActorID,
		DataRequestID: request.ID, FieldRefs: fields, Sensitivity: request.SensitivityLevel,
		ExpiresAt: now.Add(DefaultExportTTL), MaxDownloads: DefaultExportDownloads,
		RequestHash: hash,
	}
	job, err := bridge.queue.EnqueueControlledExport(ctx, command)
	if err != nil {
		return ControlledExportJob{}, err
	}
	if uuid.Validate(job.JobID) != nil || job.DataRequestID != request.ID ||
		job.State != ControlledExportPending || !job.ExpiresAt.After(now) ||
		job.ExpiresAt.After(command.ExpiresAt) || job.MaxDownloads != command.MaxDownloads {
		return ControlledExportJob{}, ErrControlledExportInvalid
	}
	return job, nil
}
