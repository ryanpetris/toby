package protocol

// Opaque collision-resistant identities for concurrent progress operations.

import "crypto/rand"

// OperationID identifies one startup operation within an acquisition.
type OperationID string

// NewOperationID returns a collision-resistant identifier suitable for one
// progress operation.
func NewOperationID() OperationID {
	return OperationID(rand.Text())
}
