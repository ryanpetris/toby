package mcpgateway

// Declares the strict agent-side MCP gateway request and target definitions.

import (
	"encoding/json"
	"fmt"
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/sandbox/mount"
)

const (
	// ResourceKind labels MCP startup progress emitted by backend components.
	ResourceKind = "resource.mcp"

	// BuiltinTarget is reserved for Toby's per-run built-in MCP server.
	BuiltinTarget = "toby"
)

// TargetType identifies whether an MCP backend is supplied by Toby, a local
// OCI sidecar, or a remote service.
type TargetType string

const (
	// TargetLocal identifies a locally launched MCP server.
	TargetLocal TargetType = "local"
	// TargetRemote identifies a remote MCP server.
	TargetRemote TargetType = "remote"
)

// Transport identifies the upstream protocol surface. Every sandbox-facing
// descriptor remains stdio through `tobys resource connect`.
type Transport string

const (
	// TransportStdio selects the MCP stdio transport.
	TransportStdio Transport = "stdio"
	// TransportHTTP selects the MCP streamable HTTP transport.
	TransportHTTP Transport = "http"
)

// EndpointKind identifies how one local HTTP process exposes its listener
// inside the agent-owned sidecar.
type EndpointKind string

// EndpointUnix selects a Unix-domain local HTTP endpoint.
const EndpointUnix EndpointKind = "unix"

// TargetSpec is one fully resolved backend definition. Environment and Headers
// may contain secrets and therefore must never be logged or returned to the
// sandbox.
type TargetSpec struct {
	Type          TargetType        `json:"type"`
	Transport     Transport         `json:"transport"`
	URL           string            `json:"url,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	Image         string            `json:"image,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Environment   map[string]string `json:"environment,omitempty"`
	Endpoint      *Endpoint         `json:"endpoint,omitempty"`
	Mounts        []Mount           `json:"mounts,omitempty"`
	Scope         resource.Scope    `json:"scope,omitempty"`
	ScopeIdentity string            `json:"scopeIdentity,omitempty"`
	Network       resource.Network  `json:"network,omitempty"`
	IdleTimeout   Duration          `json:"idleTimeout,omitempty"`
}

var _ fmt.Stringer = TargetSpec{}

// String exposes only routing metadata and withholds every backend detail.
func (s TargetSpec) String() string {
	return fmt.Sprintf(
		"{Type:%q Transport:%q Backend:<redacted>}",
		s.Type,
		s.Transport,
	)
}

// Endpoint describes one local HTTP listener and its MCP request path.
type Endpoint struct {
	Kind   EndpointKind `json:"kind"`
	Socket string       `json:"socket,omitempty"`
	Path   string       `json:"path"`
}

// Mount is one explicitly resolved sidecar bind. Its scope narrows resource
// sharing in the agent registry.
type Mount struct {
	Source string         `json:"source"`
	Target string         `json:"target"`
	Access mount.Access   `json:"access"`
	Scope  resource.Scope `json:"scope"`
}

var _ fmt.Stringer = Mount{}

// String withholds both host and sandbox paths.
func (m Mount) String() string {
	return fmt.Sprintf(
		"{Source:<redacted> Target:<redacted> Access:%q Scope:%q}",
		m.Access,
		m.Scope,
	)
}

// Duration retains a human-readable duration on the protected wire while
// exposing a parsed value to lifecycle code.
type Duration struct {
	time.Duration
}

var (
	_ json.Marshaler   = Duration{}
	_ json.Unmarshaler = (*Duration)(nil)
)

// MarshalJSON renders a duration as the same string accepted by configuration.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

// UnmarshalJSON accepts a non-empty Go duration string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	d.Duration = parsed

	return nil
}
