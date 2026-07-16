package report

import (
	"context"
	"encoding/json"
	"errors"

	"intelligent-report-generation-system/internal/reportjson"
)

const (
	MaxChanges           = 100
	MaxPatchOperations   = 100
	MaxPatchBytes        = 256 << 10
	MaxPatchPathLength   = 2048
	MaxPatchPathSegments = 64
	MaxDefinitionBytes   = 2 << 20
	MaxRequestBytes      = 3 << 20
	MaxEditorRows        = 1000
)

var (
	ErrNotFound            = errors.New("report not found")
	ErrAlreadyExists       = errors.New("report code already exists")
	ErrConflict            = errors.New("report draft version conflict")
	ErrIdempotencyConflict = errors.New("report idempotency key conflict")
	ErrInvalidRequest      = errors.New("report request is invalid")
	ErrInvalidPatch        = errors.New("report patch is invalid")
	ErrPatchMismatch       = errors.New("report patch result does not match definition")
	ErrIdentityInvalid     = errors.New("report identity is invalid")
	ErrEditLocked          = errors.New("report edit is locked")
	ErrResourceOccupied    = errors.New("report resource is occupied")
	ErrForbidden           = errors.New("report operation is forbidden")
)

var allowedOperationTypes = map[string]bool{
	"BLOCK_MOVE":              true,
	"BLOCK_RESIZE":            true,
	"BLOCK_CREATE":            true,
	"BLOCK_CLEAR":             true,
	"BLOCK_DELETE":            true,
	"BLOCK_STICKY_UPDATE":     true,
	"COMPONENT_MOVE":          true,
	"COMPONENT_RESIZE":        true,
	"COMPONENT_CREATE":        true,
	"COMPONENT_COPY":          true,
	"COMPONENT_DELETE":        true,
	"COMPONENT_STICKY_UPDATE": true,
	"LEGACY_DRAFT_RECOVERY":   true,
	"UNDO":                    true,
	"REDO":                    true,
}

// EditorState 保存仅供设计器恢复的状态，不进入正式报告 JSON 或其内容哈希。
type EditorState struct {
	MinimumRowsByPage map[string]int `json:"minimumRowsByPage"`
}

// PatchOperation 是 T0409 支持的受限 RFC 6902 操作。
type PatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// ChangeTarget 记录一次语义操作指向的稳定实体，避免数组下标成为审计身份。
type ChangeTarget struct {
	PageID                string `json:"pageId,omitempty"`
	BlockID               string `json:"blockId,omitempty"`
	ComponentID           string `json:"componentId,omitempty"`
	SourceComponentID     string `json:"sourceComponentId,omitempty"`
	CreatedComponentID    string `json:"createdComponentId,omitempty"`
	ReferencedOperationID string `json:"referencedOperationId,omitempty"`
}

// DraftChange 把一次用户手势及其一个或多个原子 Patch 绑定为一条不可变修订。
type DraftChange struct {
	ClientOperationID string           `json:"clientOperationId"`
	OperationType     string           `json:"operationType"`
	Source            string           `json:"source"`
	Target            ChangeTarget     `json:"target"`
	Patch             []PatchOperation `json:"patch"`
}

type CreateInput struct {
	Definition  json.RawMessage `json:"definition"`
	EditorState EditorState     `json:"editorState"`
}

type UpdateInput struct {
	ExpectedRevision int64           `json:"expectedRevision"`
	Definition       json.RawMessage `json:"definition"`
	EditorState      EditorState     `json:"editorState"`
	Changes          []DraftChange   `json:"changes"`
}

// DraftRecord 是设计器加载和保存后返回的服务端草稿信封。
type DraftRecord struct {
	ID             string            `json:"id"`
	Code           string            `json:"code"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Type           string            `json:"type"`
	Status         string            `json:"status"`
	Revision       int64             `json:"revision"`
	DefinitionHash string            `json:"definitionHash"`
	Definition     json.RawMessage   `json:"definition"`
	EditorState    EditorState       `json:"editorState"`
	CreatedAt      string            `json:"createdAt"`
	UpdatedAt      string            `json:"updatedAt"`
	Capabilities   DraftCapabilities `json:"capabilities"`
}

// DraftCapabilities 只用于前端收口可用操作，真正授权仍由每次写事务重新校验。
type DraftCapabilities struct {
	Edit bool `json:"edit"`
}

// RevisionRecord 不保存客户端时间或操作者，相关字段均由服务端事务生成。
type RevisionRecord struct {
	ID                string          `json:"id"`
	BaseRevision      int64           `json:"baseRevision"`
	Revision          int64           `json:"revision"`
	ClientOperationID string          `json:"clientOperationId,omitempty"`
	OperationType     string          `json:"operationType"`
	Source            string          `json:"source"`
	Target            ChangeTarget    `json:"target"`
	Patch             json.RawMessage `json:"patch"`
	PatchHash         string          `json:"patchHash"`
	BeforeHash        string          `json:"beforeHash"`
	AfterHash         string          `json:"afterHash"`
	ActorUserID       string          `json:"actorUserId,omitempty"`
	CreatedAt         string          `json:"createdAt"`
}

// ComponentIndex 与 DependencyIndex 都能由最终规范文档重建。
type ComponentIndex struct {
	PageID, BlockID, ComponentID, ComponentType string
}

type DependencyIndex struct {
	Type, ID, Path string
}

type PreparedChange struct {
	ClientOperationID     string
	OperationType         string
	Source                string
	ReferencedOperationID string
	TargetJSON            json.RawMessage
	PatchJSON             json.RawMessage
	PatchHash             string
	BeforeHash            string
	After                 reportjson.Prepared
}

type CreatePlan struct {
	ID             string
	IdempotencyKey string
	RequestHash    string
	Prepared       reportjson.Prepared
	EditorState    EditorState
	Components     []ComponentIndex
	Dependencies   []DependencyIndex
}

type UpdatePlan struct {
	ExpectedRevision int64
	IdempotencyKey   string
	RequestHash      string
	Final            reportjson.Prepared
	EditorState      EditorState
	Changes          []PreparedChange
	AffectedBlockIDs []string
	Components       []ComponentIndex
	Dependencies     []DependencyIndex
}

// ConflictError 携带客户端重新加载所需的当前草稿指纹。
type ConflictError struct {
	Revision int64
	Hash     string
}

func (e *ConflictError) Error() string { return ErrConflict.Error() }
func (e *ConflictError) Unwrap() error { return ErrConflict }

// LockedError 返回稳定报告路径，便于设计器定位被服务端拒绝的修改。
type LockedError struct{ Paths []string }

func (e *LockedError) Error() string { return ErrEditLocked.Error() }
func (e *LockedError) Unwrap() error { return ErrEditLocked }

type OccupiedError struct {
	ReportOccupied bool
	BlockIDs       []string
}

func (e *OccupiedError) Error() string { return ErrResourceOccupied.Error() }
func (e *OccupiedError) Unwrap() error { return ErrResourceOccupied }

// Store 是报告草稿的租户事务持久化边界。
type Store interface {
	Replay(context.Context, string, string, string, string, string, string) (DraftRecord, bool, error)
	Create(context.Context, string, string, CreatePlan) (DraftRecord, error)
	Get(context.Context, string, string, string, string) (DraftRecord, error)
	Update(context.Context, string, string, string, UpdatePlan) (DraftRecord, error)
	ListRevisions(context.Context, string, string, string, int, int) ([]RevisionRecord, int, error)
}
