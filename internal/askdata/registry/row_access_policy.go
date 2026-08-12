package registry

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"

	"intelligent-report-generation-system/internal/askdata"
)

// Governed row access policies (SEM-CTX-001).
//
// A policy is a boolean predicate over one semantic model's rows, written in
// the SAME semantic AST the compiler already translates for metric default
// filters, plus a single new leaf: SUBJECT_ATTRIBUTE, which stands for the
// reading actor's values for one administered business attribute.
//
//	{"type":"IN",
//	 "left":{"type":"FIELD_REF","fieldId":"region_code"},
//	 "right":{"type":"SUBJECT_ATTRIBUTE","attributeKey":"region_code"}}
//
// This package validates the SHAPE of the predicate and extracts the attribute
// keys it references. Translation into a query expression, and the fail-closed
// substitution of the reader's values, belong to the compiler - the semantic
// layer never evaluates a predicate itself.
var ErrRowAccessPolicyInvalid = errors.New("row access policy predicate is invalid")

// SubjectAttributeNode is the only addition to the semantic AST vocabulary.
const SubjectAttributeNode = "SUBJECT_ATTRIBUTE"

// maxRowAccessPredicateDepth bounds an authored predicate well below the
// compiler's own AST depth limit, so a policy can never be the reason a query
// fails to compile.
const maxRowAccessPredicateDepth = 24

var subjectAttributeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// rowAccessPredicate mirrors the compiler's semantic AST closely enough to
// validate structure without importing it: registry must not depend on the
// compiler, and the compiler already re-validates everything it translates.
type rowAccessPredicate struct {
	Type         string               `json:"type"`
	FieldID      string               `json:"fieldId,omitempty"`
	AttributeKey string               `json:"attributeKey,omitempty"`
	TargetType   string               `json:"targetType,omitempty"`
	Value        json.RawMessage      `json:"value,omitempty"`
	Argument     *rowAccessPredicate  `json:"argument,omitempty"`
	Arguments    []rowAccessPredicate `json:"arguments,omitempty"`
	Left         *rowAccessPredicate  `json:"left,omitempty"`
	Right        *rowAccessPredicate  `json:"right,omitempty"`
	Lower        *rowAccessPredicate  `json:"lower,omitempty"`
	Upper        *rowAccessPredicate  `json:"upper,omitempty"`
}

// booleanRowAccessNodes are the node types that may appear at the root: a row
// access policy has to be a predicate, not a value.
var booleanRowAccessNodes = map[string]bool{
	"AND": true, "OR": true, "NOT": true,
	"EQUALS": true, "NOT_EQUALS": true, "GT": true, "GTE": true, "LT": true, "LTE": true,
	"IN": true, "NOT_IN": true, "BETWEEN": true, "IS_NULL": true, "IS_NOT_NULL": true,
}

// ParseRowAccessPredicate validates a policy predicate and returns the sorted,
// deduplicated set of subject attribute keys it references.
//
// A predicate that references no subject attribute is rejected. Such a rule
// grants every reader the same rows, which is a metric default filter wearing
// the name of an access control - and naming it access control is exactly how
// it would come to be trusted as one.
func ParseRowAccessPredicate(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, ErrRowAccessPolicyInvalid
	}
	var node rowAccessPredicate
	if err := askdata.DecodeStrictJSON(raw, &node); err != nil {
		return nil, ErrRowAccessPolicyInvalid
	}
	if !booleanRowAccessNodes[strings.ToUpper(strings.TrimSpace(node.Type))] {
		return nil, ErrRowAccessPolicyInvalid
	}
	keys := map[string]struct{}{}
	if err := walkRowAccessPredicate(node, 0, keys); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, ErrRowAccessPolicyInvalid
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	if len(result) > 16 {
		return nil, ErrRowAccessPolicyInvalid
	}
	return result, nil
}

func walkRowAccessPredicate(node rowAccessPredicate, depth int, keys map[string]struct{}) error {
	if depth > maxRowAccessPredicateDepth {
		return ErrRowAccessPolicyInvalid
	}
	nodeType := strings.ToUpper(strings.TrimSpace(node.Type))
	switch nodeType {
	case SubjectAttributeNode:
		// A subject reference is a leaf and carries nothing but its key.
		if !subjectAttributeKeyPattern.MatchString(node.AttributeKey) ||
			node.FieldID != "" || node.Argument != nil || len(node.Arguments) > 0 ||
			node.Left != nil || node.Right != nil || node.Lower != nil || node.Upper != nil ||
			len(node.Value) > 0 {
			return ErrRowAccessPolicyInvalid
		}
		keys[node.AttributeKey] = struct{}{}
		return nil
	case "FIELD_REF":
		if strings.TrimSpace(node.FieldID) == "" {
			return ErrRowAccessPolicyInvalid
		}
		return nil
	case "LITERAL":
		if len(node.Value) == 0 {
			return ErrRowAccessPolicyInvalid
		}
		return nil
	case "TRUE", "FALSE":
		return nil
	case "AND", "OR", "ARRAY", "COALESCE":
		if len(node.Arguments) == 0 {
			return ErrRowAccessPolicyInvalid
		}
	case "NOT", "IS_NULL", "IS_NOT_NULL", "CAST":
		if node.Argument == nil {
			return ErrRowAccessPolicyInvalid
		}
	case "EQUALS", "NOT_EQUALS", "GT", "GTE", "LT", "LTE", "IN", "NOT_IN":
		if node.Left == nil || node.Right == nil {
			return ErrRowAccessPolicyInvalid
		}
	case "BETWEEN":
		if node.Argument == nil || node.Lower == nil || node.Upper == nil {
			return ErrRowAccessPolicyInvalid
		}
	default:
		// Arithmetic and other value-shaped nodes are intentionally absent: a
		// row access predicate compares governed fields to the reader's
		// attributes, and every extra node type is more surface to reason about
		// in a security control.
		return ErrRowAccessPolicyInvalid
	}
	for _, child := range []*rowAccessPredicate{node.Argument, node.Left, node.Right, node.Lower, node.Upper} {
		if child == nil {
			continue
		}
		if err := walkRowAccessPredicate(*child, depth+1, keys); err != nil {
			return err
		}
	}
	for _, child := range node.Arguments {
		if err := walkRowAccessPredicate(child, depth+1, keys); err != nil {
			return err
		}
	}
	return nil
}
