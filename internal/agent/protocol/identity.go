package protocol

// Defines strongly typed opaque identities and closed resource/stream kinds.

import (
	"fmt"
	"strings"
)

// SessionID identifies one agent session.
type SessionID string

// ClientResourceID identifies one launch-local resource registration.
type ClientResourceID string

// ResourceID is an opaque agent resource identifier.
type ResourceID string

// LeaseID is an opaque resource lease identifier.
type LeaseID string

// ResourceKind classifies agent-managed resources.
type ResourceKind string

const (
	// ResourceOCI identifies an OCI image resource.
	ResourceOCI ResourceKind = "resource.oci"
	// ResourceMCP identifies an MCP resource.
	ResourceMCP ResourceKind = "resource.mcp"
	// ResourceModels identifies a models API resource.
	ResourceModels ResourceKind = "resource.models"
)

// Validate verifies that the resource kind is supported.
func (k ResourceKind) Validate() error {
	switch k {
	case ResourceOCI, ResourceMCP, ResourceModels:
		return nil
	default:
		return fmt.Errorf("unknown resource kind %q", k)
	}
}

// TransportCapability identifies one transport adapter supported by an agent.
type TransportCapability string

const (
	// TransportUnixSocket selects a Unix-domain byte-stream capability.
	TransportUnixSocket TransportCapability = "unix"
)

func validateOpaqueID(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxIdentifierBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maxIdentifierBytes)
	}
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s contains NUL", name)
	}

	return nil
}

// ValidateSessionID validates one opaque agent session identity.
func ValidateSessionID(id SessionID) error {
	return validateOpaqueID("session ID", string(id))
}

// ValidateResourceID validates one opaque resource identity.
func ValidateResourceID(id ResourceID) error {
	return validateOpaqueID("resource ID", string(id))
}

// ValidateLeaseID validates one opaque resource lease identity.
func ValidateLeaseID(id LeaseID) error {
	return validateOpaqueID("lease ID", string(id))
}

// ValidateOperationID validates one opaque streamed-operation identity.
func ValidateOperationID(id OperationID) error {
	return validateOpaqueID("operation ID", string(id))
}
