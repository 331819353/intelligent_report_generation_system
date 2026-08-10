package asset

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
)

type Lifecycle string

const (
	LifecycleDraftOnly Lifecycle = "DRAFT_ONLY"
	LifecyclePublished Lifecycle = "PUBLISHED"
	LifecycleChanged   Lifecycle = "CHANGED"
	LifecycleOffline   Lifecycle = "OFFLINE"
)

type Action string

const (
	ActionView        Action = "VIEW"
	ActionEdit        Action = "EDIT"
	ActionPublish     Action = "PUBLISH"
	ActionVersions    Action = "VERSIONS"
	ActionPermissions Action = "PERMISSIONS"
	ActionArchive     Action = "ARCHIVE"
	ActionRestore     Action = "RESTORE"
	ActionExport      Action = "EXPORT"
	ActionShare       Action = "SHARE"
	ActionAIEdit      Action = "AI_EDIT"
)

var permissionActions = map[Action]struct{}{
	ActionView: {}, ActionEdit: {}, ActionPublish: {}, ActionExport: {}, ActionShare: {}, ActionAIEdit: {},
}

type ListQuery struct {
	Scope      string
	Lifecycle  Lifecycle
	OwnerID    askdata.ID
	ReportType reportmodel.ReportType
	Search     string
	Cursor     string
	Limit      int
}

type Asset struct {
	ID                 askdata.ID             `json:"id"`
	Code               string                 `json:"code"`
	Name               string                 `json:"name"`
	ReportType         reportmodel.ReportType `json:"reportType"`
	OwnerUserID        askdata.ID             `json:"ownerUserId"`
	OwnerName          string                 `json:"ownerName"`
	Lifecycle          Lifecycle              `json:"lifecycle"`
	CurrentVersionNo   *int                   `json:"currentVersionNo,omitempty"`
	DraftRevisionNo    int64                  `json:"draftRevisionNo"`
	UnpublishedChanges int64                  `json:"unpublishedChanges"`
	VisibleCount       int                    `json:"visibleCount"`
	EditableCount      int                    `json:"editableCount"`
	Shared             bool                   `json:"shared"`
	UpdatedAt          time.Time              `json:"updatedAt"`
	AllowedActions     []Action               `json:"allowedActions"`
}

type Page struct {
	Items      []Asset `json:"items"`
	NextCursor string  `json:"nextCursor,omitempty"`
}

type PermissionGrant struct {
	ID          askdata.ID `json:"id"`
	SubjectType string     `json:"subjectType"`
	SubjectID   askdata.ID `json:"subjectId"`
	SubjectName string     `json:"subjectName"`
	Action      Action     `json:"action"`
	GrantedBy   askdata.ID `json:"grantedBy,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type GrantInput struct {
	SubjectType string     `json:"subjectType"`
	SubjectID   askdata.ID `json:"subjectId"`
	Action      Action     `json:"action"`
}

type Event struct {
	ID             askdata.ID      `json:"id"`
	EventType      string          `json:"eventType"`
	ActorUserID    askdata.ID      `json:"actorUserId,omitempty"`
	ActorName      string          `json:"actorName,omitempty"`
	SubjectType    string          `json:"subjectType,omitempty"`
	SubjectID      askdata.ID      `json:"subjectId,omitempty"`
	Action         string          `json:"action,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	PreviousStatus string          `json:"previousStatus,omitempty"`
	NewStatus      string          `json:"newStatus,omitempty"`
	Payload        json.RawMessage `json:"payload"`
	CreatedAt      time.Time       `json:"createdAt"`
}

type Error struct {
	StableCode string
	Message    string
	Issues     compiler.ValidationIssues
	Err        error
}

func (value *Error) Error() string {
	if value.Message != "" {
		return value.Message
	}
	if value.Err != nil {
		return value.Err.Error()
	}
	return value.StableCode
}
func (value *Error) Unwrap() error { return value.Err }
func (value *Error) Code() string  { return value.StableCode }

var ErrNotFound = errors.New("report asset not found")

type cursorValue struct {
	UpdatedAt time.Time  `json:"updatedAt"`
	ID        askdata.ID `json:"id"`
}

func encodeCursor(updatedAt time.Time, id askdata.ID) string {
	raw, _ := json.Marshal(cursorValue{UpdatedAt: updatedAt.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(value string) (cursorValue, error) {
	if value == "" {
		return cursorValue{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return cursorValue{}, errors.New("cursor is invalid")
	}
	var result cursorValue
	if json.Unmarshal(raw, &result) != nil || result.ID.Validate() != nil || result.UpdatedAt.IsZero() {
		return cursorValue{}, errors.New("cursor is invalid")
	}
	return result, nil
}

func validateGrant(input GrantInput) error {
	if input.SubjectType != "USER" && input.SubjectType != "ROLE" {
		return errors.New("subjectType must be USER or ROLE")
	}
	if input.SubjectID.Validate() != nil {
		return errors.New("subjectId is invalid")
	}
	if _, exists := permissionActions[input.Action]; !exists {
		return errors.New("report permission action is invalid")
	}
	return nil
}

func validateReason(reason string) error {
	if reason != strings.TrimSpace(reason) || len(reason) < 1 || len(reason) > 1000 {
		return errors.New("reason must contain 1..1000 trimmed characters")
	}
	for _, character := range reason {
		if character < 32 || character == 127 {
			return errors.New("reason cannot contain control characters")
		}
	}
	return nil
}

func allowedActions(lifecycle Lifecycle, canView, canEdit, canPublish, canExport, canShare, canAIEdit, canManage bool) []Action {
	result := []Action{}
	if canView && lifecycle != LifecycleOffline {
		result = append(result, ActionView)
	}
	if canEdit && lifecycle != LifecycleOffline {
		result = append(result, ActionEdit)
	}
	if canPublish && lifecycle != LifecycleOffline {
		result = append(result, ActionPublish)
	}
	if canView || canEdit || canPublish {
		result = append(result, ActionVersions)
	}
	if canManage {
		result = append(result, ActionPermissions)
	}
	if canPublish {
		if lifecycle == LifecycleOffline {
			result = append(result, ActionRestore)
		} else {
			result = append(result, ActionArchive)
		}
	}
	if canExport && lifecycle != LifecycleOffline {
		result = append(result, ActionExport)
	}
	if canShare && lifecycle != LifecycleOffline {
		result = append(result, ActionShare)
	}
	if canAIEdit && lifecycle != LifecycleOffline {
		result = append(result, ActionAIEdit)
	}
	return result
}
