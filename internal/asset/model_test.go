package asset

import "testing"

func TestBusinessMetadataValidation(t *testing.T) {
	valid := BusinessMetadata{SensitivityLevel: "INTERNAL", Visibility: "PRIVATE", ExpectedVersion: 1, Tags: []string{"core"}}
	if err := valid.Validate(false); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.ExpectedVersion = 0
	if err := invalid.Validate(false); err == nil {
		t.Fatal("expected version validation error")
	}
	invalid = valid
	invalid.SensitivityLevel = "SECRET"
	if err := invalid.Validate(false); err == nil {
		t.Fatal("expected sensitivity validation error")
	}
	invalid = valid
	invalid.Visibility = "PUBLIC"
	if err := invalid.Validate(false); err == nil {
		t.Fatal("expected visibility validation error")
	}
}

func TestColumnBusinessMetadataRejectsUnknownSemanticType(t *testing.T) {
	metadata := BusinessMetadata{SensitivityLevel: "INTERNAL", SemanticType: "RAW_SQL", ExpectedVersion: 1}
	if err := metadata.Validate(true); err == nil {
		t.Fatal("unknown semantic type was accepted")
	}
}
