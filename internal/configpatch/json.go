package configpatch

// Converts documents and values through a JSON-compatible form that preserves
// integers.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
)

func decodeJSONValue(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing JSON after the first value")
	}
	return normalizeValue(value), nil
}

func decodeObject(data []byte) (map[string]any, error) {
	value, err := decodeJSONValue(data)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("document root must be an object")
	}
	return object, nil
}

func marshalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func cloneJSONValue(value any) any {
	if value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	cloned, err := decodeJSONValue(data)
	if err != nil {
		return value
	}
	return cloned
}

func equalValue(left, right any) bool {
	return reflect.DeepEqual(normalizeValue(left), normalizeValue(right))
}

func normalizeValue(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		number, err := typed.Float64()
		if err != nil {
			return typed.String()
		}
		return number
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return int64(typed)
	case uint8:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		return int64(typed)
	case float32:
		return float64(typed)
	case float64:
		return typed
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeValue(item)
		}
		return out
	default:
		reflected := reflect.ValueOf(value)
		if !reflected.IsValid() {
			return value
		}
		switch reflected.Kind() {
		case reflect.Slice, reflect.Array:
			out := make([]any, reflected.Len())
			for index := range reflected.Len() {
				out[index] = normalizeValue(reflected.Index(index).Interface())
			}
			return out
		case reflect.Map:
			if reflected.Type().Key().Kind() != reflect.String {
				return value
			}
			out := make(map[string]any, reflected.Len())
			iter := reflected.MapRange()
			for iter.Next() {
				out[iter.Key().String()] = normalizeValue(iter.Value().Interface())
			}
			return out
		default:
			return value
		}
	}
}
