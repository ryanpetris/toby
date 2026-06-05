package sessionservice

// Runtime-info sanitization: introspection resources never expose secrets, so
// every runtime-defined map is walked and any key naming a URL, header, command,
// argv, or environment value (and unsupported value kinds) is dropped.

import (
	"reflect"
	"strings"
)

func sanitizeRuntimeInfo(info map[string]any) map[string]any {
	if len(info) == 0 {
		return nil
	}
	clean := map[string]any{}
	for key, value := range info {
		if unsafeRuntimeInfoKey(key) {
			continue
		}
		if sanitized, ok := sanitizeRuntimeInfoValue(value); ok {
			clean[key] = sanitized
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func sanitizeRuntimeInfoValue(value any) (any, bool) {
	return sanitizeRuntimeInfoReflect(reflect.ValueOf(value))
}

func sanitizeRuntimeInfoReflect(value reflect.Value) (any, bool) {
	if !value.IsValid() {
		return nil, true
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, true
		}
		value = value.Elem()
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil, false
		}
		clean := map[string]any{}
		iter := value.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			if unsafeRuntimeInfoKey(key) {
				continue
			}
			if sanitized, ok := sanitizeRuntimeInfoReflect(iter.Value()); ok {
				clean[key] = sanitized
			}
		}
		return clean, len(clean) > 0
	case reflect.Slice, reflect.Array:
		clean := make([]any, 0, value.Len())
		for i := 0; i < value.Len(); i++ {
			if sanitized, ok := sanitizeRuntimeInfoReflect(value.Index(i)); ok {
				clean = append(clean, sanitized)
			}
		}
		return clean, len(clean) > 0
	case reflect.Bool:
		return value.Bool(), true
	case reflect.String:
		return value.String(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Float32, reflect.Float64:
		return value.Float(), true
	default:
		return nil, false
	}
}

func unsafeRuntimeInfoKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, unsafe := range []string{"url", "header", "headers", "command", "argv", "env", "environment"} {
		if key == unsafe || strings.Contains(key, unsafe) {
			return true
		}
	}
	return false
}
