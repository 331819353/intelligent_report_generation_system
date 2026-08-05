package toolhost

import (
	"errors"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

type recordingValidator struct {
	called bool
	err    error
}

func (validator *recordingValidator) ValidateArguments(ToolName, ToolArguments) error {
	validator.called = true
	return validator.err
}

func TestValidateCallFailsClosedAndUsesSecondaryValidator(t *testing.T) {
	release := askdata.ReleaseRef{ReleaseID: "release-1", ContentHash: askdata.HashBytes([]byte("release"))}
	arguments := NewArguments(release)
	mention := "销售额"
	limit := 10
	arguments.Mention = &mention
	arguments.ObjectTypes = []ObjectType{ObjectTypeMetric}
	arguments.DomainIDs = []askdata.ID{"sales"}
	arguments.Limit = &limit
	request := CallRequest{SchemaVersion: SchemaVersion, CallID: "call-1", Tool: ToolSearchSemanticObjects, Arguments: arguments}

	validator := &recordingValidator{}
	if err := ValidateCall(request, validator); err != nil {
		t.Fatalf("ValidateCall() error = %v", err)
	}
	if !validator.called {
		t.Fatal("secondary argument validator was not called")
	}

	request.Tool = "run_arbitrary_sql"
	validator.called = false
	if err := ValidateCall(request, validator); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("ValidateCall() error = %v, want unknown tool", err)
	}
	if validator.called {
		t.Fatal("validator must not run for an unknown tool")
	}
}

func TestDefaultArgumentValidatorRejectsCrossToolFields(t *testing.T) {
	release := askdata.ReleaseRef{ReleaseID: "release-1", ContentHash: askdata.HashBytes([]byte("release"))}
	arguments := NewArguments(release)
	planHash := askdata.HashBytes([]byte("plan"))
	maxRows := 100
	mention := "not allowed"
	arguments.PlanHash = &planHash
	arguments.MaxRows = &maxRows
	arguments.Mention = &mention
	err := arguments.ValidateFor(ToolExecuteQueryPlan)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("ValidateFor() error = %v, want forbidden field rejection", err)
	}
}

func TestValidateCallPropagatesSecondaryValidationFailure(t *testing.T) {
	request := CallRequest{SchemaVersion: SchemaVersion, CallID: "call-1", Tool: ToolValidateQueryPlan}
	validator := &recordingValidator{err: errors.New("schema mismatch")}
	err := ValidateCall(request, validator)
	if err == nil || !strings.Contains(err.Error(), "schema mismatch") {
		t.Fatalf("ValidateCall() error = %v, want secondary validation failure", err)
	}
}
