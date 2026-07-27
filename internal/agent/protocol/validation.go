package protocol

// Validates bounded identifiers, tokens, and deferred JSON specifications.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

const (
	maxBinaryVersionBytes = 128
	maxIdentifierBytes    = 256
	maxSpecBytes          = MaxConfigurationBytes
	maxErrorMessageBytes  = 4 << 10
)

// MaxConfigurationBytes bounds one encoded resource-owned configuration
// document retained or rendered by agent services.
const MaxConfigurationBytes = 256 << 10

// ValidateConfigurationDocument validates one bounded JSON object.
func ValidateConfigurationDocument(data json.RawMessage) error {
	if len(data) > MaxConfigurationBytes {
		return fmt.Errorf(
			"%w: encoded object has %d bytes, limit is %d",
			ErrConfigurationTooLarge,
			len(data),
			MaxConfigurationBytes,
		)
	}

	return validateJSONObject("configuration document", data)
}

// ValidateHostActionPayload validates one bounded JSON request or response
// object carried on the agent session.
func ValidateHostActionPayload(data json.RawMessage) error {
	return validateJSONObject("host action payload", data)
}

func validateBinaryVersion(value string) error {
	if value == "" {
		return errors.New("binary version is required")
	}
	if len(value) > maxBinaryVersionBytes {
		return fmt.Errorf("binary version exceeds %d bytes", maxBinaryVersionBytes)
	}
	if !utf8.ValidString(value) {
		return errors.New("binary version is not valid UTF-8")
	}

	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("binary version contains a control character")
		}
	}

	return nil
}

// ValidateBinaryVersion validates one informational binary version.
func ValidateBinaryVersion(value string) error {
	return validateBinaryVersion(value)
}

func validateJSONObject(field string, data json.RawMessage) error {
	if len(data) == 0 {
		return fmt.Errorf("%s is required", field)
	}
	if len(data) > maxSpecBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, maxSpecBytes)
	}
	if err := rejectDuplicateFields(data); err != nil {
		return fmt.Errorf("%s is invalid JSON: %w", field, err)
	}

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return fmt.Errorf("%s must be a JSON object", field)
	}

	return nil
}
