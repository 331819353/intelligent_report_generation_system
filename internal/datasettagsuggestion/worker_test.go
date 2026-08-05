package datasettagsuggestion

import (
	"errors"
	"testing"
)

func TestClassifyGenerationErrorRetriesInvalidOutput(t *testing.T) {
	code, retryable := classifyGenerationError(ErrInvalidOutput)
	if code != "AI_INVALID_OUTPUT" || !retryable {
		t.Fatalf("classification = %q, %t", code, retryable)
	}
}

func TestClassifyGenerationErrorDoesNotRetryInvalidRequest(t *testing.T) {
	code, retryable := classifyGenerationError(ErrInvalidRequest)
	if code != "AI_INVALID_OUTPUT" || retryable {
		t.Fatalf("classification = %q, %t", code, retryable)
	}
	if errors.Is(ErrInvalidRequest, ErrInvalidOutput) {
		t.Fatal("invalid request must remain distinct from invalid output")
	}
}
