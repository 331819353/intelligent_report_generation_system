package runtime

import (
	"encoding/json"
	"fmt"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/report"
)

// Error is a stable runtime failure safe to expose through the report API.
// Details intentionally never include component bindings or protected data.
type Error struct {
	code    string
	message string
	cause   error
}

func NewError(code, message string, cause error) *Error {
	return &Error{code: code, message: message, cause: cause}
}

func (err *Error) Error() string {
	if err == nil {
		return "report runtime failed"
	}
	if err.cause != nil {
		return fmt.Sprintf("%s: %v", err.message, err.cause)
	}
	return err.message
}

func (err *Error) Unwrap() error { return err.cause }
func (err *Error) Code() string  { return err.code }

type ComponentState string

const (
	StateLoading      ComponentState = "LOADING"
	StateReady        ComponentState = "READY"
	StateEmpty        ComponentState = "EMPTY"
	StatePartial      ComponentState = "PARTIAL"
	StateError        ComponentState = "ERROR"
	StateNoPermission ComponentState = "NO_PERMISSION"
	StateStale        ComponentState = "STALE"
	StateTimeout      ComponentState = "TIMEOUT"
)

func ValidComponentState(state ComponentState) bool {
	switch state {
	case StateLoading, StateReady, StateEmpty, StatePartial, StateError, StateNoPermission, StateStale, StateTimeout:
		return true
	default:
		return false
	}
}

type LoadedReport struct {
	ReportID       askdata.ID              `json:"reportId"`
	VersionID      askdata.ID              `json:"versionId"`
	VersionNo      int                     `json:"versionNo"`
	DefinitionHash string                  `json:"definitionHash"`
	Definition     report.ReportDefinition `json:"definition"`
}

type QueryRequest struct {
	ReportID              askdata.ID                `json:"reportId"`
	ReportVersionID       askdata.ID                `json:"reportVersionId"`
	BindingMode           report.BindingMode        `json:"bindingMode"`
	DatasetID             askdata.ID                `json:"datasetId,omitempty"`
	DatasetVersionID      askdata.ID                `json:"datasetVersionId,omitempty"`
	DataContextID         askdata.ID                `json:"dataContextId,omitempty"`
	SemanticReleaseID     askdata.ID                `json:"semanticReleaseId,omitempty"`
	SemanticContentHash   askdata.ContentHash       `json:"semanticContentHash,omitempty"`
	FixedQueryPlanHash    askdata.ContentHash       `json:"fixedQueryPlanHash,omitempty"`
	SourceQuestionRunID   askdata.ID                `json:"sourceQuestionRunId,omitempty"`
	CompilationArtifactID askdata.ID                `json:"compilationArtifactId,omitempty"`
	SemanticIR            json.RawMessage           `json:"semanticIr,omitempty"`
	ResolvedTimeSpec      json.RawMessage           `json:"resolvedTimeSpec,omitempty"`
	Dimensions            []report.FieldBinding     `json:"dimensions"`
	Measures              []report.FieldBinding     `json:"measures"`
	Filters               []ResolvedFilter          `json:"filters"`
	Parameters            []report.DefaultParameter `json:"parameters"`
	Limit                 int                       `json:"limit"`
	Timeout               time.Duration             `json:"-"`
	PolicyScopeHash       string                    `json:"policyScopeHash"`
	UncertifiedDefinition bool                      `json:"uncertifiedDefinition"`
	// Draft marks a query planned against the editable draft rather than an
	// immutable published version. Such a query carries no ReportVersionID, and
	// the executor only accepts that for bindings that are self-contained in the
	// definition.
	Draft bool `json:"draft,omitempty"`
}

type ComponentPlan struct {
	ComponentID askdata.ID    `json:"componentId"`
	PageID      askdata.ID    `json:"pageId"`
	BlockID     askdata.ID    `json:"blockId"`
	SlotID      askdata.ID    `json:"slotId"`
	Query       *QueryRequest `json:"query,omitempty"`
	// Blocked names the reason a bound component cannot execute against the
	// current target. It is distinct from a nil Query with no reason, which
	// means the component legitimately needs no data.
	Blocked string `json:"blocked,omitempty"`
}

type ExecutionPlan struct {
	Components []ComponentPlan `json:"components"`
}
