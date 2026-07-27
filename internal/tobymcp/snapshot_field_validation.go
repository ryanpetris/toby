package tobymcp

// Validates repeated text values and sandbox paths shared across snapshot
// collection validators.

import (
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validateUniqueSessionStrings(label string, values []string) error {
	if len(values) > maxSessionSnapshotItems {
		return fmt.Errorf(
			"%s count exceeds %d",
			label,
			maxSessionSnapshotItems,
		)
	}

	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if err := validateSessionText(
			fmt.Sprintf("%s %d", label, index),
			value,
		); err != nil {
			return err
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("%s %d duplicates %q", label, index, value)
		}
		seen[value] = struct{}{}
	}

	return nil
}

func validateSessionText(label, value string) error {
	return validateSessionString(label, value, maxSessionTextBytes)
}

func validateSessionString(label, value string, maximum int) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if len(value) > maximum {
		return fmt.Errorf(
			"%s exceeds %d bytes",
			label,
			maximum,
		)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%s has surrounding whitespace", label)
	}
	if strings.Contains(value, "://") {
		return fmt.Errorf("%s must not contain a URL", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}

	return nil
}

func validateSessionPath(label, value string) error {
	if err := validateSessionString(
		label,
		value,
		maxSessionPathBytes,
	); err != nil {
		return err
	}
	if !path.IsAbs(value) || path.Clean(value) != value {
		return fmt.Errorf("%s must be a clean absolute sandbox path", label)
	}

	return nil
}

func validateSessionAccess(label, value string) error {
	switch value {
	case "regular", "read_only", "dev":
		return nil
	default:
		return fmt.Errorf("%s is invalid: %q", label, value)
	}
}
