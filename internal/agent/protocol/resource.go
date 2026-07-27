package protocol

// Defines the internal resource acquisition request.

import "encoding/json"

// ResourceAcquireRequest registers one raw resource configuration. The agent
// applies defaults and computes the authoritative stable identity.
type ResourceAcquireRequest struct {
	CorrelationID CorrelationID   `json:"-"`
	Kind          ResourceKind    `json:"kind"`
	Configuration json.RawMessage `json:"configuration"`
}
