package semanticqa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

var allowedPatchRoots = map[string]bool{
	"dataset": true, "nodes": true, "joins": true, "preAggregations": true,
	"factContract": true, "analysisContract": true, "fields": true,
	"filters": true, "groupBy": true, "having": true, "sorts": true,
	"parameters": true, "outputGrain": true, "executionPolicy": true,
	"designer": true,
}

var orderedPatchRoots = []string{
	"dataset", "nodes", "joins", "preAggregations", "factContract",
	"analysisContract", "fields", "filters", "groupBy", "having", "sorts",
	"parameters", "outputGrain", "executionPolicy", "designer",
}

func componentDiff(
	baseline json.RawMessage,
	candidate json.RawMessage,
) ([]ChangeOperation, error) {
	decode := func(raw json.RawMessage) (map[string]json.RawMessage, error) {
		var value map[string]json.RawMessage
		decoder := json.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(&value); err != nil || value == nil {
			return nil, ErrInvalidRequest
		}
		return value, nil
	}
	base, err := decode(baseline)
	if err != nil {
		return nil, err
	}
	next, err := decode(candidate)
	if err != nil {
		return nil, err
	}
	if version := string(next["dslVersion"]); version != `"1.0"` {
		return nil, ErrInvalidRequest
	}
	operations := make([]ChangeOperation, 0, len(orderedPatchRoots))
	for _, root := range orderedPatchRoots {
		before, beforeExists := base[root]
		after, afterExists := next[root]
		if beforeExists && afterExists && bytes.Equal(before, after) {
			continue
		}
		operation := ChangeOperation{Path: "/" + root}
		switch {
		case !beforeExists && afterExists:
			operation.Operation = "ADD"
			operation.Value = append(json.RawMessage(nil), after...)
		case beforeExists && !afterExists:
			operation.Operation = "REMOVE"
		case beforeExists && afterExists:
			operation.Operation = "REPLACE"
			operation.Value = append(json.RawMessage(nil), after...)
		default:
			continue
		}
		operations = append(operations, operation)
	}
	if len(operations) > 256 {
		return nil, ErrUnsafeChange
	}
	return operations, nil
}

func applyChangeOperations(
	baseline json.RawMessage,
	operations []ChangeOperation,
) (json.RawMessage, error) {
	if len(operations) < 1 || len(operations) > 256 {
		return nil, fmt.Errorf("%w: operation count", ErrUnsafeChange)
	}
	var document any
	decoder := json.NewDecoder(bytes.NewReader(baseline))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: invalid baseline", ErrUnsafeChange)
	}
	if document == nil {
		document = map[string]any{"dslVersion": "1.0"}
	}
	seen := map[string]bool{}
	for _, operation := range operations {
		tokens, err := patchTokens(operation.Path)
		if err != nil || !allowedPatchRoots[tokens[0]] {
			return nil, fmt.Errorf("%w: invalid path %q", ErrUnsafeChange, operation.Path)
		}
		if seen[operation.Path] {
			return nil, fmt.Errorf("%w: duplicate path %q", ErrUnsafeChange, operation.Path)
		}
		seen[operation.Path] = true
		op := strings.ToUpper(strings.TrimSpace(operation.Operation))
		if op != "ADD" && op != "REPLACE" && op != "REMOVE" {
			return nil, fmt.Errorf("%w: invalid operation", ErrUnsafeChange)
		}
		var value any
		if op != "REMOVE" {
			if len(operation.Value) == 0 || len(operation.Value) > 256<<10 {
				return nil, fmt.Errorf("%w: invalid operation value", ErrUnsafeChange)
			}
			valueDecoder := json.NewDecoder(bytes.NewReader(operation.Value))
			valueDecoder.UseNumber()
			if err := valueDecoder.Decode(&value); err != nil {
				return nil, fmt.Errorf("%w: invalid operation value", ErrUnsafeChange)
			}
		} else if len(operation.Value) != 0 && string(operation.Value) != "null" {
			return nil, fmt.Errorf("%w: remove cannot carry a value", ErrUnsafeChange)
		}
		document, err = applyPatchAt(document, tokens, op, value)
		if err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func patchTokens(path string) ([]string, error) {
	if path == "" || path == "/" || len(path) > 512 || !strings.HasPrefix(path, "/") {
		return nil, ErrUnsafeChange
	}
	raw := strings.Split(path[1:], "/")
	tokens := make([]string, len(raw))
	for index, token := range raw {
		var decoded strings.Builder
		for offset := 0; offset < len(token); offset++ {
			if token[offset] != '~' {
				decoded.WriteByte(token[offset])
				continue
			}
			if offset+1 >= len(token) {
				return nil, ErrUnsafeChange
			}
			offset++
			switch token[offset] {
			case '0':
				decoded.WriteByte('~')
			case '1':
				decoded.WriteByte('/')
			default:
				return nil, ErrUnsafeChange
			}
		}
		tokens[index] = decoded.String()
		if tokens[index] == "" {
			return nil, ErrUnsafeChange
		}
	}
	return tokens, nil
}

func applyPatchAt(current any, tokens []string, operation string, value any) (any, error) {
	if len(tokens) == 0 {
		return nil, ErrUnsafeChange
	}
	token := tokens[0]
	if len(tokens) == 1 {
		switch target := current.(type) {
		case map[string]any:
			_, exists := target[token]
			switch operation {
			case "ADD":
				if exists {
					return nil, fmt.Errorf("%w: add target exists", ErrUnsafeChange)
				}
				target[token] = value
			case "REPLACE":
				if !exists {
					return nil, fmt.Errorf("%w: replace target is missing", ErrUnsafeChange)
				}
				target[token] = value
			case "REMOVE":
				if !exists {
					return nil, fmt.Errorf("%w: remove target is missing", ErrUnsafeChange)
				}
				delete(target, token)
			}
			return target, nil
		case []any:
			index, appendValue, err := patchArrayIndex(token, len(target), operation == "ADD")
			if err != nil {
				return nil, err
			}
			switch operation {
			case "ADD":
				if appendValue {
					return append(target, value), nil
				}
				target = append(target, nil)
				copy(target[index+1:], target[index:])
				target[index] = value
			case "REPLACE":
				target[index] = value
			case "REMOVE":
				target = append(target[:index], target[index+1:]...)
			}
			return target, nil
		default:
			return nil, fmt.Errorf("%w: path parent is not a container", ErrUnsafeChange)
		}
	}

	switch target := current.(type) {
	case map[string]any:
		child, exists := target[token]
		if !exists {
			return nil, fmt.Errorf("%w: path parent is missing", ErrUnsafeChange)
		}
		updated, err := applyPatchAt(child, tokens[1:], operation, value)
		if err != nil {
			return nil, err
		}
		target[token] = updated
		return target, nil
	case []any:
		index, _, err := patchArrayIndex(token, len(target), false)
		if err != nil {
			return nil, err
		}
		updated, err := applyPatchAt(target[index], tokens[1:], operation, value)
		if err != nil {
			return nil, err
		}
		target[index] = updated
		return target, nil
	default:
		return nil, fmt.Errorf("%w: path parent is not a container", ErrUnsafeChange)
	}
}

func patchArrayIndex(token string, length int, allowEnd bool) (int, bool, error) {
	if token == "-" {
		if allowEnd {
			return length, true, nil
		}
		return 0, false, fmt.Errorf("%w: append marker is not readable", ErrUnsafeChange)
	}
	if len(token) > 1 && token[0] == '0' {
		return 0, false, fmt.Errorf("%w: array index is not canonical", ErrUnsafeChange)
	}
	index, err := strconv.Atoi(token)
	if err != nil || index < 0 || index > length || (!allowEnd && index == length) {
		return 0, false, fmt.Errorf("%w: array index is out of range", ErrUnsafeChange)
	}
	return index, allowEnd && index == length, nil
}
