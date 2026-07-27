package mcpgateway

// Defines the static backend-resolver and transaction-owned target lifecycle
// boundaries used by the per-run gateway.

import (
	"context"
	"encoding/json"
	"fmt"

	"petris.dev/toby/internal/agent/protocol"
	agentserver "petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/mcpgateway/connector"
)

// ProgressReporter receives bounded MCP startup stages and attached
// subprocess output for the requesting acquisition.
type ProgressReporter interface {
	// Report records one MCP acquisition progress event.
	Report(protocol.AcquireProgress) error
}

// TargetClass selects one exact backend implementation.
type TargetClass string

const (
	// TargetBuiltin selects an in-process Toby MCP server.
	TargetBuiltin TargetClass = "builtin"
	// TargetLocalHTTP selects a local HTTP MCP server.
	TargetLocalHTTP TargetClass = "local-http"
	// TargetLocalStdio selects a local stdio MCP server.
	TargetLocalStdio TargetClass = "local-stdio"
	// TargetRemoteHTTP selects a remote HTTP MCP server.
	TargetRemoteHTTP TargetClass = "remote-http"
)

// TargetRequest carries either the built-in session snapshot or one validated
// configured target. Only the built-in class receives Session.
type TargetRequest struct {
	ResourceID protocol.ResourceID
	Caller     agentserver.HostActionCaller
	Name       string
	Session    json.RawMessage
	Spec       TargetSpec
}

var _ fmt.Stringer = TargetRequest{}

// String withholds the host-action authority, session snapshot, and target
// definition.
func (r TargetRequest) String() string {
	return fmt.Sprintf(
		"{ResourceID:<redacted> Caller:<redacted> Name:%q Session:<redacted> Spec:<redacted>}",
		r.Name,
	)
}

// BackendResolver validates and prepares one target class without starting a
// process or exposing a connector. Implementations own reusable registries and
// transports and permanently shut them down when Shutdown returns.
type BackendResolver interface {
	// Class returns the target class handled by the resolver.
	Class() TargetClass
	// Resolve validates and prepares one target.
	Resolve(context.Context, TargetRequest) (PreparedBackend, error)
	// Shutdown releases all resolver-owned resources.
	Shutdown(context.Context) error
}

// PreparedBackend is one validated target whose readiness work has not begun.
type PreparedBackend interface {
	// Acquire makes the prepared backend ready for connections.
	Acquire(context.Context, ProgressReporter) (AcquiredBackend, error)
}

// AcquiredBackend owns one ready target route. Target must remain stable until
// Revoke. Revoke synchronously and idempotently removes authority without
// waiting; Release performs bounded cleanup and honors its context.
type AcquiredBackend interface {
	// Target returns the connector target for the acquired backend.
	Target() connector.Target
	// Revoke removes the acquired backend's authority.
	Revoke()
	// Release performs bounded backend cleanup.
	Release(context.Context) error
}
