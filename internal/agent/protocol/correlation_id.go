package protocol

// Defines opaque request-correlation identities generated as UUID version 4.

import (
	"errors"
	"fmt"
	"strings"

	"petris.dev/toby/internal/uuid"
)

// CorrelationID identifies one request within an agent session. Clients create
// UUIDs; the agent treats the value as an opaque bounded string and echoes it
// unchanged on every associated response.
type CorrelationID string

// NewCorrelationID returns a fresh client-generated UUID correlation value.
func NewCorrelationID() (CorrelationID, error) {
	value, err := uuid.NewV4()
	if err != nil {
		return "", err
	}

	return CorrelationID(value), nil
}

func validateCorrelationID(id CorrelationID) error {
	if id == "" {
		return errors.New("correlation ID is required")
	}
	if len(id) > maxIdentifierBytes {
		return fmt.Errorf(
			"correlation ID exceeds %d bytes",
			maxIdentifierBytes,
		)
	}
	if strings.ContainsRune(string(id), '\x00') {
		return errors.New("correlation ID contains NUL")
	}

	return nil
}

// ValidateCorrelationID validates one client-supplied opaque correlation
// identity without interpreting its UUID representation.
func ValidateCorrelationID(id CorrelationID) error {
	return validateCorrelationID(id)
}
