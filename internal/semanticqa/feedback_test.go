package semanticqa

import (
	"strings"
	"testing"
)

func TestNormalizeQueryFeedbackInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		input     SubmitQueryFeedbackInput
		wantValid bool
		wantIssue string
	}{
		{
			name: "accurate feedback always clears issue type",
			input: SubmitQueryFeedbackInput{
				Rating: " accurate ", IssueType: "filter", Comment: " 口径正确 ",
			},
			wantValid: true,
			wantIssue: "NONE",
		},
		{
			name: "inaccurate feedback keeps normalized issue type",
			input: SubmitQueryFeedbackInput{
				Rating: "inaccurate", IssueType: " result_value ",
			},
			wantValid: true,
			wantIssue: "RESULT_VALUE",
		},
		{
			name:      "inaccurate feedback requires issue type",
			input:     SubmitQueryFeedbackInput{Rating: "INACCURATE"},
			wantValid: false,
			wantIssue: "",
		},
		{
			name: "unknown issue type is rejected",
			input: SubmitQueryFeedbackInput{
				Rating: "INACCURATE", IssueType: "MODEL_ERROR",
			},
			wantValid: false,
			wantIssue: "MODEL_ERROR",
		},
		{
			name: "oversized comments are rejected",
			input: SubmitQueryFeedbackInput{
				Rating: "INACCURATE", IssueType: "OTHER", Comment: strings.Repeat("字", 2001),
			},
			wantValid: false,
			wantIssue: "OTHER",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, valid := normalizeQueryFeedbackInput(test.input)
			if valid != test.wantValid {
				t.Fatalf("valid = %v, want %v", valid, test.wantValid)
			}
			if got.IssueType != test.wantIssue {
				t.Fatalf("issue type = %q, want %q", got.IssueType, test.wantIssue)
			}
		})
	}
}
