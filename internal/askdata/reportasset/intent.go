package reportasset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/operation"
)

const IntentRetention = 7 * 24 * time.Hour

var (
	ErrInvalidIntent         = errors.New("add-to-report intent is invalid")
	ErrIntentConflict        = errors.New("add-to-report idempotency key was reused with different content")
	ErrIntentExpired         = errors.New("add-to-report intent expired")
	ErrIntentState           = errors.New("add-to-report intent state does not allow this operation")
	ErrQuestionNotExportable = errors.New("question result is not a full answered result")
)

type IntentState string

const (
	IntentPendingConfirmation IntentState = "PENDING_CONFIRMATION"
	IntentPending             IntentState = "PENDING"
	IntentApplied             IntentState = "APPLIED"
	IntentRejected            IntentState = "REJECTED"
	IntentExpired             IntentState = "EXPIRED"
)

type IntentIdentity struct {
	TenantID, ActorID, DomainID askdata.ID
}

type BuildIntentRequest struct {
	QuestionRunID, ReportID, TargetPageID, TargetSectionID askdata.ID
	RunVersion, BaseRevision                               int64
	IdempotencyKeyHash                                     askdata.ContentHash
	OutcomeFull                                            bool
	SemanticQueryRef                                       report.SemanticQueryRef
	Chart                                                  answer.ChartRecommendation
	Title                                                  string
	BlockY                                                 int
}

type Intent struct {
	ID, TenantID, ActorID, QuestionRunID, ReportID askdata.ID
	Bundle                                         operation.Bundle
	PreviewHash                                    askdata.ContentHash
	State                                          IntentState
	AppliedRevisionNo                              *int64
	RejectionCode, RejectionDetail                 string
	CreatedAt, ExpiresAt                           time.Time
	Replayed                                       bool
}

type IntentRepository interface {
	CreatePreview(context.Context, IntentIdentity, BuildIntentRequest, operation.Bundle, askdata.ContentHash, time.Time) (Intent, error)
	Confirm(context.Context, IntentIdentity, askdata.ID, askdata.ContentHash, time.Time) (Intent, error)
	Get(context.Context, IntentIdentity, askdata.ID) (Intent, error)
}

type IntentService struct {
	repository IntentRepository
	now        func() time.Time
}

func NewIntentService(repository IntentRepository) *IntentService {
	return &IntentService{repository: repository, now: time.Now}
}

func (service *IntentService) CreatePreview(ctx context.Context, identity IntentIdentity, request BuildIntentRequest) (Intent, error) {
	if service == nil || service.repository == nil || validateIntentIdentity(identity) != nil ||
		!request.OutcomeFull || request.RunVersion < 1 || request.BaseRevision < 0 ||
		request.IdempotencyKeyHash.Validate() != nil {
		return Intent{}, ErrQuestionNotExportable
	}
	bundle, err := BuildOperationBundle(request)
	if err != nil {
		return Intent{}, err
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		return Intent{}, err
	}
	now := service.now().UTC()
	return service.repository.CreatePreview(ctx, identity, request, bundle, askdata.HashBytes(raw), now)
}

func (service *IntentService) Confirm(ctx context.Context, identity IntentIdentity, intentID askdata.ID, previewHash askdata.ContentHash) (Intent, error) {
	if service == nil || service.repository == nil || validateIntentIdentity(identity) != nil ||
		intentID.Validate() != nil || previewHash.Validate() != nil {
		return Intent{}, ErrInvalidIntent
	}
	return service.repository.Confirm(ctx, identity, intentID, previewHash, service.now().UTC())
}

func BuildOperationBundle(request BuildIntentRequest) (operation.Bundle, error) {
	if request.QuestionRunID.Validate() != nil || request.ReportID.Validate() != nil ||
		request.TargetPageID.Validate() != nil || request.TargetSectionID.Validate() != nil ||
		request.SemanticQueryRef.SourceQuestionRunID == nil ||
		*request.SemanticQueryRef.SourceQuestionRunID != request.QuestionRunID ||
		request.SemanticQueryRef.DatasetVersionID == nil ||
		(request.SemanticQueryRef.SemanticIR.TimeRange != nil && request.SemanticQueryRef.ResolvedTimeSpec == nil) ||
		len(request.SemanticQueryRef.EvidenceRefs) == 0 ||
		request.SemanticQueryRef.ChartRuleVersion != answer.ChartRuleVersion ||
		request.Chart.RuleVersion != answer.ChartRuleVersion || request.Chart.ComponentType == "" ||
		request.Chart.ComponentVersion == "" || request.BlockY < 0 {
		return operation.Bundle{}, ErrInvalidIntent
	}
	seed := string(request.QuestionRunID) + "\x00" + string(request.ReportID) + "\x00" + string(request.IdempotencyKeyHash)
	stableID := func(kind string) askdata.ID {
		return askdata.ID(uuid.NewSHA1(uuid.NameSpaceURL, []byte("askdata-report/"+kind+"/"+seed)).String())
	}
	componentID, blockID, zoneID, slotID := stableID("component"), stableID("block"), stableID("zone"), stableID("slot")
	component := report.Component{
		ID:          componentID,
		TemplateRef: report.ComponentTemplateReference{Type: request.Chart.ComponentType, Version: request.Chart.ComponentVersion},
		DataBinding: &report.DataBinding{BindingMode: report.BindingSemanticIR, SemanticQueryRef: &request.SemanticQueryRef},
		Options:     report.ComponentOptions{Title: strings.TrimSpace(request.Title)},
	}
	blockType := report.BlockChart
	if request.Chart.ComponentType == "data-table" {
		blockType = report.BlockTable
	} else if request.Chart.ComponentType == "metric-card" {
		blockType = report.BlockKPIGroup
	}
	block := report.Block{
		ID: blockID, Type: blockType,
		Layout: report.BlockLayout{
			Desktop: report.DesktopBlockLayout{X: 0, Y: request.BlockY, W: 24, H: 6},
			Mobile:  report.MobileBlockLayout{Order: request.BlockY + 1, Visible: true, HeightMode: report.MobileHeightAuto, SlotMode: report.MobileSlotStack},
		},
		Zones: []report.Zone{{
			Order: 1,
			ID:    zoneID, Type: report.ZoneContent,
			Layout: report.ZoneLayout{HeightMode: report.ZoneHeightAuto, MinHeight: 240, Columns: 24, Rows: 6, Overflow: report.OverflowExpand},
			Slots:  []report.Slot{{ID: slotID, Grid: report.SlotGrid{X: 0, Y: 0, W: 24, H: 6}, ComponentID: componentID}},
		}},
	}
	bundle := operation.Bundle{
		SchemaVersion: operation.SchemaVersion, ReportID: request.ReportID,
		BaseRevision: request.BaseRevision, Source: operation.SourceSystem,
		Operations: []operation.Operation{
			{Op: operation.ComponentCreate, TargetID: componentID, Payload: &operation.ComponentCreatePayload{Component: component}},
			{Op: operation.BlockCreate, TargetID: request.TargetSectionID, Payload: &operation.BlockCreatePayload{Block: block}},
		},
	}
	if err := bundle.Validate(); err != nil {
		return operation.Bundle{}, fmt.Errorf("%w: %v", ErrInvalidIntent, err)
	}
	return bundle, nil
}

func validateIntentIdentity(identity IntentIdentity) error {
	for _, id := range []askdata.ID{identity.TenantID, identity.ActorID, identity.DomainID} {
		parsed, err := uuid.Parse(string(id))
		if err != nil || parsed.String() != string(id) {
			return ErrInvalidIntent
		}
	}
	return nil
}
