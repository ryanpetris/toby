package sandboxgateway

// Validates opaque identifiers and immutable opener registries.

import (
	"fmt"
	"reflect"
	"strings"
)

func validateIdentifier(name string, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxIdentifierBytes {
		return fmt.Errorf(
			"%s exceeds %d bytes",
			name,
			maxIdentifierBytes,
		)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s contains NUL", name)
	}

	return nil
}

func cloneOpeners(source map[string]Opener) (map[string]Opener, error) {
	if len(source) == 0 {
		return nil, fmt.Errorf(
			"sandbox gateway resource registry is empty",
		)
	}

	result := make(map[string]Opener, len(source))
	for id, opener := range source {
		if err := validateIdentifier("client resource ID", id); err != nil {
			return nil, err
		}
		if isNil(opener) {
			return nil, fmt.Errorf(
				"sandbox gateway resource %q has no opener",
				id,
			)
		}
		result[id] = opener
	}

	return result, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}

	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan,
		reflect.Func,
		reflect.Interface,
		reflect.Map,
		reflect.Pointer,
		reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
