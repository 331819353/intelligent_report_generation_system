package metadataai

import (
	"errors"
	"testing"
)

func validCompletion() (CompletionInput, CompletionOutput) {
	input := CompletionInput{
		SchemaVersion: SchemaVersion,
		TargetTable:   true,
		Table:         Target{ID: "table-1"},
		Columns:       []Target{{ID: "column-1"}, {ID: "column-2"}},
	}
	output := CompletionOutput{
		SchemaVersion: SchemaVersion,
		Table: &SuggestionValue{
			TargetID: "table-1", BusinessName: "订单", BusinessDescription: "客户订单事实表",
			Tags: []string{"领域:运营", "作用:事实表"}, SensitivityLevel: "INTERNAL", Confidence: 0.91,
		},
		Columns: []SuggestionValue{
			{TargetID: "column-1", BusinessName: "订单编号", BusinessDescription: "订单唯一标识", Tags: []string{"作用:主数据"}, SensitivityLevel: "INTERNAL", SemanticType: "IDENTIFIER", Confidence: 0.98},
			{TargetID: "column-2", BusinessName: "订单金额", BusinessDescription: "订单含税金额", Tags: []string{"主题:经营分析"}, SensitivityLevel: "CONFIDENTIAL", SemanticType: "AMOUNT", Confidence: 0.88},
		},
	}
	return input, output
}

func TestValidateOutputAcceptsExactStructuredResult(t *testing.T) {
	input, output := validCompletion()
	if err := ValidateOutput(input, output); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOutputAcceptsOnlyRequestedIncrementalTargets(t *testing.T) {
	input, output := validCompletion()
	input.TargetTable = false
	input.Columns = input.Columns[:1]
	output.Table = nil
	output.Columns = output.Columns[:1]
	if err := ValidateOutput(input, output); err != nil {
		t.Fatal(err)
	}

	_, unexpectedTable := validCompletion()
	unexpectedTable.Columns = unexpectedTable.Columns[:1]
	if err := ValidateOutput(input, unexpectedTable); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unexpected table error = %v", err)
	}
}

func TestValidateOutputAcceptsTableOnlyCompletion(t *testing.T) {
	input, output := validCompletion()
	input.Columns = []Target{}
	output.Columns = []SuggestionValue{}
	if err := ValidateOutput(input, output); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOutputRejectsHallucinatedAndMissingTargets(t *testing.T) {
	input, output := validCompletion()
	output.Columns[0].TargetID = "invented-column"
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unknown target error = %v", err)
	}
	_, output = validCompletion()
	output.Columns = output.Columns[:1]
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing target error = %v", err)
	}
}

func TestValidateOutputRejectsUnsafeOrOutOfTaxonomyValues(t *testing.T) {
	input, output := validCompletion()
	output.Table.BusinessDescription = "<script>alert(1)</script>"
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unsafe text error = %v", err)
	}
	_, output = validCompletion()
	output.Columns[0].Tags = []string{"虚构:标签"}
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("tag taxonomy error = %v", err)
	}
	_, output = validCompletion()
	output.Table.Tags = []string{"领域:运营", "领域:运营"}
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("duplicate tag error = %v", err)
	}
}

func TestValidateOutputRejectsMissingRequiredCollectionsAndConfidence(t *testing.T) {
	input, output := validCompletion()
	output.Table.Tags = nil
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing tags error = %v", err)
	}
	_, output = validCompletion()
	output.Columns[0].Confidence = 0
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing confidence error = %v", err)
	}
	_, output = validCompletion()
	output.Columns = nil
	if err := ValidateOutput(input, output); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("missing columns error = %v", err)
	}
}

func TestNormalizeOutputTrimsAndDeduplicatesProviderTags(t *testing.T) {
	_, output := validCompletion()
	output.Table.Tags = []string{" 领域:运营 ", "领域:运营", "作用:事实表"}

	normalized := normalizeOutput(output)
	if got := normalized.Table.Tags; len(got) != 2 || got[0] != "领域:运营" || got[1] != "作用:事实表" {
		t.Fatalf("normalized tags=%#v", got)
	}
}

func TestSuggestionDispositionProtectsLockedAndChangedAssets(t *testing.T) {
	tests := []struct {
		name                   string
		locked                 bool
		current, expected      int64
		confidence, threshold  float64
		wantStatus, wantReason string
	}{
		{"locked", true, 1, 1, 0.99, 0.8, "PENDING", "MANUAL_LOCKED"},
		{"changed", false, 2, 1, 0.99, 0.8, "PENDING", "VERSION_CHANGED"},
		{"low", false, 1, 1, 0.79, 0.8, "PENDING", "LOW_CONFIDENCE"},
		{"high", false, 1, 1, 0.8, 0.8, "APPLIED", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, reason := suggestionDisposition(test.locked, test.current, test.expected, test.confidence, test.threshold)
			if status != test.wantStatus || reason != test.wantReason {
				t.Fatalf("got %s/%s, want %s/%s", status, reason, test.wantStatus, test.wantReason)
			}
		})
	}
}

func TestSuggestionDispositionRejectsIncompatibleSemanticType(t *testing.T) {
	target := Target{Kind: "COLUMN", CanonicalType: "STRING", BusinessVersion: 3}
	value := SuggestionValue{SemanticType: "PERCENTAGE", Confidence: 0.99}
	status, reason := suggestionDispositionForTarget(target, value, false, 3, 0.8)
	if status != "PENDING" || reason != "SEMANTIC_TYPE_INCOMPATIBLE" {
		t.Fatalf("status=%q reason=%q", status, reason)
	}
}
