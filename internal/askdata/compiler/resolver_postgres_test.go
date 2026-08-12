package compiler

import (
	"testing"

	"intelligent-report-generation-system/internal/dataset"
)

func TestSemanticFieldContractUsesPublishedCodeAsLogicalIdentity(t *testing.T) {
	field := dataset.Field{
		ID: "field_stat_date", Code: "stat_date", Role: "TIME",
		CanonicalType: "DATE", SemanticType: "DATE",
	}
	contract, err := semanticFieldContract(field)
	if err != nil {
		t.Fatalf("semanticFieldContract() error = %v", err)
	}
	if contract.FieldID != "stat_date" || contract.Code != "stat_date" {
		t.Fatalf("logical field identity = %q/%q, want published code", contract.FieldID, contract.Code)
	}
	if contract.ContractHash.Validate() != nil {
		t.Fatal("field contract hash is invalid")
	}
}
