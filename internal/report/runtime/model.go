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
}

type ComponentPlan struct {
	ComponentID askdata.ID    `json:"componentId"`
	PageID      askdata.ID    `json:"pageId"`
	BlockID     askdata.ID    `json:"blockId"`
	SlotID      askdata.ID    `json:"slotId"`
	Query       *QueryRequest `json:"query,omitempty"`
}

type ExecutionPlan struct {
	Components []ComponentPlan `json:"components"`
}

// PinExecutionVersion binds every query to the immutable report artifact that
// produced it. Plan construction deliberately cannot infer these IDs from the
// definition JSON, so the loader must apply them before a plan is returned or
// executed.
func PinExecutionVersion(plan *ExecutionPlan, loaded LoadedReport) error {
	if plan == nil || loaded.ReportID.Validate() != nil || loaded.VersionID.Validate() != nil {
		return fmt.Errorf("report execution version is invalid")
	}
	for index := range plan.Components {
		if plan.Components[index].Query == nil {
			continue
		}
		plan.Components[index].Query.ReportID = loaded.ReportID
		plan.Components[index].Query.ReportVersionID = loaded.VersionID
	}
	return nil
}
