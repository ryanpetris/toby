package protocol

// Defines transport-independent discovered model metadata.

import (
	"encoding/json"
	"fmt"
)

// ModelsListItemResponse carries one model and its tool-facing metadata.
type ModelsListItemResponse struct {
	CorrelationID CorrelationID
	OperationID   OperationID
	Sequence      uint64
	ModelID       string
	Model         json.RawMessage
}

// ValidateModelID validates one bounded tool-facing model identity.
func ValidateModelID(id string) error {
	if id == "" || len(id) > maxIdentifierBytes {
		return fmt.Errorf("model ID is invalid")
	}

	return nil
}
