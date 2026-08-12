package compiler

import (
	"errors"
	"fmt"
	"regexp"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/dataset"
)

// Row access policy compilation (SEM-CTX-001).
//
// A policy is translated exactly like a metric default filter - same AST, same
// translator, same parameter binding - with one addition: SUBJECT_ATTRIBUTE
// resolves to the reading actor's administered values.
//
// The whole point of the feature is the failure mode, so it is stated once,
// here, and enforced in one place:
//
//	a reader with no value for an attribute a policy references sees NO rows.
//
// Not "all rows", and not "an error". Denying is the only safe reading of "we
// do not know who this person is", and turning it into an error would let a
// missing grant take a working model offline for everyone.
var subjectAttributeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// errSubjectAttributeUnmatched signals that the reader holds no value for a
// referenced attribute. It never escapes compileRowAccessPredicate: it is
// converted into a predicate that selects nothing.
var errSubjectAttributeUnmatched = errors.New("subject attribute is not granted to the reading actor")

// falseExpression is a predicate no row satisfies, built from bound literals so
// it survives the query compiler unchanged.
func falseExpression() dataset.Expression {
	left := dataset.Expression{Type: "LITERAL", Value: float64(1)}
	right := dataset.Expression{Type: "LITERAL", Value: float64(0)}
	return dataset.Expression{Type: "EQUALS", Left: &left, Right: &right}
}

// compileRowAccessPolicies translates every policy pinned to a model into source
// filters for that model's node.
//
// Policies are ANDed, which is what makes them composable: adding a policy can
// only ever remove rows, so no combination of certified policies can widen
// access beyond what any one of them allows.
func compileRowAccessPolicies(
	policies []RowAccessPolicyContract,
	fields map[askdata.ID]FieldContract,
	subjectAttributes map[string][]string,
) ([]dataset.SourceFilter, error) {
	if len(policies) == 0 {
		return nil, nil
	}
	filters := make([]dataset.SourceFilter, 0, len(policies))
	for _, policy := range policies {
		expression, err := compileRowAccessPredicate(policy, fields, subjectAttributes)
		if err != nil {
			return nil, err
		}
		filters = append(filters, dataset.SourceFilter{Expression: &expression})
	}
	return filters, nil
}

func compileRowAccessPredicate(
	policy RowAccessPolicyContract,
	fields map[askdata.ID]FieldContract,
	subjectAttributes map[string][]string,
) (dataset.Expression, error) {
	predicate, err := decodeSemanticAST(policy.PredicateAST)
	if err != nil {
		return dataset.Expression{}, fmt.Errorf("row access policy %s: %w", policy.Code, err)
	}
	if !semanticBooleanType(predicate.Type) {
		return dataset.Expression{}, fmt.Errorf(
			"row access policy %s: predicate root must be boolean", policy.Code,
		)
	}
	translator := astTranslator{
		fields:            fields,
		referencedFields:  map[askdata.ID]struct{}{},
		subjectAttributes: subjectAttributes,
	}
	converted, err := translator.translate(predicate, astRowPolicy, 0)
	if errors.Is(err, errSubjectAttributeUnmatched) {
		// The reader holds no value for something this policy needs. Deny every
		// row rather than dropping the policy.
		return falseExpression(), nil
	}
	if err != nil {
		return dataset.Expression{}, fmt.Errorf("row access policy %s: %w", policy.Code, err)
	}
	if len(translator.referencedFields) == 0 {
		// A predicate that touches no model field cannot be scoping rows of that
		// model, whatever it claims to compare.
		return dataset.Expression{}, fmt.Errorf(
			"row access policy %s: predicate must reference a model field", policy.Code,
		)
	}
	return converted, nil
}

// joinedRowAccessFilters compiles the row access policies of every joined model,
// keyed by the DSL node the model was placed on.
//
// Joined models are handled separately from the anchor only because they live on
// different nodes; the rule is identical, and a joined model whose policies were
// skipped would turn any join into a permission bypass.
func joinedRowAccessFilters(
	resolution Resolution,
	fields map[askdata.ID]FieldContract,
) (map[string][]dataset.SourceFilter, error) {
	if len(resolution.JoinedModels) == 0 {
		return nil, nil
	}
	result := make(map[string][]dataset.SourceFilter, len(resolution.JoinedModels))
	for _, model := range resolution.JoinedModels {
		filters, err := compileRowAccessPolicies(
			model.RowAccessPolicies, fields, resolution.subjectAttributes,
		)
		if err != nil {
			return nil, err
		}
		if len(filters) > 0 {
			result[joinedNodeID(model.ModelVersionID)] = filters
		}
	}
	return result, nil
}
