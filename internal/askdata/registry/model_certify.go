package registry

// ValidateSemanticModelCertification is the deterministic certification gate
// used before repository writes. The database repeats the same check so direct
// SQL cannot bypass it.
func ValidateSemanticModelCertification(model SemanticModel, contract *TimeContractVersion) error {
	if model.TimeContractVersionID == "" || contract == nil {
		return ValidationErrors{Issues: []ValidationIssue{{Code: TimeContractMissing, Path: "timeContractVersionId", Message: "semantic model certification requires a time contract version"}}}
	}
	if contract.ID != model.TimeContractVersionID || contract.TenantID != model.TenantID ||
		contract.DomainID != model.DomainID || contract.Status != VersionStatusCertified {
		return ValidationErrors{Issues: []ValidationIssue{{Code: validationCodeInvalidDependency, Path: "timeContractVersionId", Message: "time contract must be CERTIFIED and belong to the same tenant and domain"}}}
	}
	return nil
}
