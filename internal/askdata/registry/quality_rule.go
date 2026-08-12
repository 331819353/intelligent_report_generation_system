package registry

import (
	"errors"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/materialization"
)

// Governed data quality rules (SEM-QUALITY-001).
//
// The contract for askdata.quality_rules existed at every layer except the two
// that matter: rule_ast had no defined language, and nothing anywhere could
// execute one. Filling that gap by inventing an expression language would have
// meant building a second evaluator alongside the one the platform already has,
// and would have let Owners author rules that no component runs - which
// get_data_quality_status would then report as "rule present, not passed",
// reading as a data quality failure when nothing had actually been checked.
//
// So a semantic quality rule is a BINDING, not an expression. It names a check
// the materialization pipeline already performs on the warehouse output backing
// a semantic model, and the semantic layer only reads that outcome:
//
//	what it expresses  a check from the executing catalog, at a scope, on a target
//	who executes it    the materialization worker, never the semantic layer
//	when               during the dataset build run that produces a materialization
//	against which      the ACTIVE materialization pinned by the semantic model
//
// The validator refuses any binding whose rule code is not actually executed,
// so a rule that nobody can run cannot be created in the first place.
const QualityRuleBindingType = "DATASET_QUALITY_BINDING"

// QualityRuleBindingVersion is carried in the document so a future binding
// shape can be introduced without silently reinterpreting stored rules.
const QualityRuleBindingVersion = 1

var ErrQualityRuleBindingInvalid = errors.New("data quality rule binding is invalid")

// QualityRuleBinding is the decoded rule_ast document.
type QualityRuleBinding struct {
	Type            string `json:"type"`
	Version         int    `json:"version"`
	DatasetRuleCode string `json:"datasetRuleCode"`
	Scope           string `json:"scope"`
	FieldID         string `json:"fieldId,omitempty"`
	// MaxAgeHours optionally bounds how old a measurement may be before the
	// rule reports STALE. Zero means any measurement from the pinned
	// materialization counts, however old the build is.
	MaxAgeHours int `json:"maxAgeHours,omitempty"`
}

// DecodeQualityRuleBinding parses and validates a rule_ast document.
func DecodeQualityRuleBinding(raw []byte) (QualityRuleBinding, error) {
	var binding QualityRuleBinding
	if len(raw) == 0 {
		return QualityRuleBinding{}, ErrQualityRuleBindingInvalid
	}
	if err := askdata.DecodeStrictJSON(raw, &binding); err != nil {
		return QualityRuleBinding{}, ErrQualityRuleBindingInvalid
	}
	binding.Type = strings.ToUpper(strings.TrimSpace(binding.Type))
	binding.DatasetRuleCode = strings.ToUpper(strings.TrimSpace(binding.DatasetRuleCode))
	binding.Scope = strings.ToUpper(strings.TrimSpace(binding.Scope))
	binding.FieldID = strings.TrimSpace(binding.FieldID)
	if binding.Type != QualityRuleBindingType || binding.Version != QualityRuleBindingVersion {
		return QualityRuleBinding{}, ErrQualityRuleBindingInvalid
	}
	// The binding must name a check that is genuinely executed at this scope,
	// otherwise the rule could never produce an outcome.
	if !materialization.QualityRuleExecutable(binding.DatasetRuleCode, binding.Scope) {
		return QualityRuleBinding{}, ErrQualityRuleBindingInvalid
	}
	if (binding.Scope == "FIELD") != (binding.FieldID != "") {
		return QualityRuleBinding{}, ErrQualityRuleBindingInvalid
	}
	if len(binding.FieldID) > 128 || binding.MaxAgeHours < 0 || binding.MaxAgeHours > 8760 {
		return QualityRuleBinding{}, ErrQualityRuleBindingInvalid
	}
	return binding, nil
}
