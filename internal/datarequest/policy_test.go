package datarequest

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestDeriveSensitivityUsesStrictMaximum(t *testing.T) {
	result, err := DeriveSensitivity([]SensitivityFact{
		{SourceID: "field-a", Sensitivity: SensitivityInternal},
		{SourceID: "field-b", Sensitivity: SensitivityRestricted},
		{SourceID: "dimension-a", Sensitivity: SensitivityConfidential},
	})
	if err != nil || result != SensitivityRestricted {
		t.Fatalf("result=%s err=%v", result, err)
	}
	for _, facts := range [][]SensitivityFact{nil, {{SourceID: "field", Sensitivity: "UNKNOWN"}}, {
		{SourceID: "field", Sensitivity: SensitivityInternal},
		{SourceID: "field", Sensitivity: SensitivityRestricted},
	}} {
		if _, err := DeriveSensitivity(facts); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("facts=%v err=%v", facts, err)
		}
	}
}

func TestValidateApprovalRequiresIndependentActiveCosigner(t *testing.T) {
	requester, approver, cosigner := uuid.NewString(), uuid.NewString(), uuid.NewString()
	base := ApprovalPolicyInput{
		Sensitivity: SensitivityRestricted, RequesterUserID: requester,
		ApproverUserID: approver, ActiveMemberIDs: []string{requester, approver, cosigner},
	}
	for _, value := range []string{"", requester, approver, uuid.NewString()} {
		base.SecurityCosignID = value
		if err := ValidateApproval(base); !errors.Is(err, ErrSecurityCosignRequired) {
			t.Fatalf("cosigner=%q err=%v", value, err)
		}
	}
	base.SecurityCosignID = cosigner
	if err := ValidateApproval(base); err != nil {
		t.Fatal(err)
	}
	base.Sensitivity, base.SecurityCosignID = SensitivityInternal, ""
	if err := ValidateApproval(base); err != nil {
		t.Fatalf("internal sensitivity required cosign: %v", err)
	}
	base.SecurityCosignID = uuid.NewString()
	if err := ValidateApproval(base); !errors.Is(err, ErrSecurityCosignRequired) {
		t.Fatalf("inactive optional cosigner accepted: %v", err)
	}
}
