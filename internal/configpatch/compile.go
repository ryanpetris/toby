package configpatch

// Compiles Ensure and Remove intents into RFC 6902 operations against a live
// document.

import (
	"fmt"
	"reflect"
	"strconv"
)

func compileIntents(document map[string]any, patch Patch) ([]Operation, error) {
	current := cloneObject(document)
	compiled := make([]Operation, 0, len(patch.Remove)+len(patch.Ensure))
	for _, item := range patch.Remove {
		ops, err := compileRemove(current, item)
		if err != nil {
			return nil, err
		}
		current, err = applyOperations(current, ops)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, ops...)
	}
	for _, item := range patch.Ensure {
		ops, err := compileEnsure(current, item)
		if err != nil {
			return nil, err
		}
		current, err = applyOperations(current, ops)
		if err != nil {
			return nil, err
		}
		compiled = append(compiled, ops...)
	}
	return compiled, nil
}

func compileEnsure(document map[string]any, item Value) ([]Operation, error) {
	tokens, err := parsePointer(item.Path)
	if err != nil {
		return nil, err
	}

	var ops []Operation
	var current any = document
	for index, token := range tokens {
		pointer := formatPointer(tokens[:index+1])
		last := index == len(tokens)-1
		switch node := current.(type) {
		case map[string]any:
			child, exists := node[token]
			if !exists {
				if !last {
					ops = append(ops, Operation{
						Op:    "add",
						Path:  pointer,
						Value: map[string]any{},
					})
					current = map[string]any{}
					continue
				}
				ops = append(ops, Operation{
					Op:    "add",
					Path:  pointer,
					Value: newEnsureNode(item.Value),
				})
				return ops, nil
			}
			if last {
				more, err := ensureExisting(pointer, child, item.Value)
				if err != nil {
					return nil, err
				}
				return append(ops, more...), nil
			}
			current = child
		case []any:
			arrayIndex, indexErr := parseArrayIndex(token, len(node))
			if indexErr != nil {
				return nil, fmt.Errorf("ensure %s: %w", pointer, indexErr)
			}
			if last {
				more, err := ensureExisting(pointer, node[arrayIndex], item.Value)
				if err != nil {
					return nil, err
				}
				return append(ops, more...), nil
			}
			current = node[arrayIndex]
		default:
			return nil, fmt.Errorf("ensure %s: cannot descend into %s", pointer, valueKind(current))
		}
	}
	return ops, nil
}

func compileRemove(document map[string]any, item Value) ([]Operation, error) {
	tokens, err := parsePointer(item.Path)
	if err != nil {
		return nil, err
	}

	current, err := lookup(document, tokens)
	if err != nil {
		return nil, fmt.Errorf("remove %s: %w", item.Path, err)
	}
	if current == missingValue {
		return nil, nil
	}

	if array, ok := current.([]any); ok {
		ops := make([]Operation, 0)
		for index := len(array) - 1; index >= 0; index-- {
			if equalValue(array[index], item.Value) {
				ops = append(ops, Operation{
					Op:   "remove",
					Path: formatPointer(append(append([]string{}, tokens...), strconv.Itoa(index))),
				})
			}
		}
		return ops, nil
	}
	if equalValue(current, item.Value) {
		return []Operation{{Op: "remove", Path: item.Path}}, nil
	}
	return nil, nil
}

func ensureExisting(pointer string, current, want any) ([]Operation, error) {
	if array, ok := current.([]any); ok {
		for _, item := range array {
			if equalValue(item, want) {
				return nil, nil
			}
		}
		return []Operation{{
			Op:    "add",
			Path:  pointer + "/-",
			Value: want,
		}}, nil
	}
	if equalValue(current, want) {
		return nil, nil
	}
	return []Operation{{
		Op:    "replace",
		Path:  pointer,
		Value: want,
	}}, nil
}

func newEnsureNode(want any) any {
	if want == nil {
		return []any{nil}
	}
	switch reflect.ValueOf(want).Kind() {
	case reflect.Map, reflect.Slice, reflect.Array:
		return want
	default:
		return []any{want}
	}
}

type missing struct{}

var missingValue any = missing{}

func lookup(document map[string]any, tokens []string) (any, error) {
	var current any = document
	for index, token := range tokens {
		pointer := formatPointer(tokens[:index+1])
		switch node := current.(type) {
		case map[string]any:
			child, exists := node[token]
			if !exists {
				return missingValue, nil
			}
			current = child
		case []any:
			arrayIndex, err := parseArrayIndex(token, len(node))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", pointer, err)
			}
			current = node[arrayIndex]
		default:
			return nil, fmt.Errorf("cannot descend into %s", valueKind(current))
		}
	}
	return current, nil
}

func cloneObject(document map[string]any) map[string]any {
	cloned, ok := cloneJSONValue(document).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return cloned
}

func valueKind(value any) string {
	if value == nil {
		return "null"
	}
	return reflect.TypeOf(value).String()
}
