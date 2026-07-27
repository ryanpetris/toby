package server

// Declares resource-session boundaries and bounded server configuration.

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/resourcelog"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/shutdown"
)

// HostActionCaller invokes one privileged host action on the live launch CLI.
// Calls fail closed once that lease connection is revoked.
type HostActionCaller interface {
	// Call invokes one privileged host action.
	Call(context.Context, json.RawMessage) (json.RawMessage, error)
}

// ResourceLease is one independently releasable resource acquisition. Both
// identifiers are opaque outside the agent.
type ResourceLease interface {
	// ResourceID returns the opaque resource identifier.
	ResourceID() protocol.ResourceID
	// LeaseID returns the opaque lease identifier.
	LeaseID() protocol.LeaseID
	// Release relinquishes the resource lease.
	Release(context.Context) error
}

// ResourceCoordinator acquires and opens one resource configuration without
// batching it with any other launch work.
type ResourceCoordinator interface {
	// AcquireResource resolves and leases one resource.
	AcquireResource(
		context.Context,
		protocol.ResourceAcquireRequest,
		HostActionCaller,
	) (ResourceLease, error)
	// OpenResource opens a lease-authorized stream.
	OpenResource(
		context.Context,
		protocol.ResourceKind,
		protocol.ResourceID,
		protocol.LeaseID,
	) (ResourceStream, error)
	// ResourceSnapshot returns current resource and lease counts.
	ResourceSnapshot() ResourceSnapshot
}

// ModelsCoordinator performs lease-authorized one-resource model operations.
type ModelsCoordinator interface {
	// ListModels discovers models through one authorized lease.
	ListModels(
		context.Context,
		protocol.LeaseID,
	) (map[string]any, error)
	// FlushModelsCache invalidates cached models for a lease.
	FlushModelsCache(protocol.LeaseID) error
}

// ResourceLister returns non-secret active resource entries.
type ResourceLister interface {
	// ResourceItems returns non-secret active resource summaries.
	ResourceItems() []ResourceItem
}

// ResourceItem is one non-secret active agent resource.
type ResourceItem struct {
	ID           protocol.ResourceID
	Kind         protocol.ResourceKind
	ActiveLeases uint64
}

// ResourceStream owns one accepted resource operation until it closes or its
// agent session is canceled.
type ResourceStream interface {
	// Close releases the resource stream.
	Close() error
}

// ByteResourceStream serves one bidirectional MCP or models byte stream.
type ByteResourceStream interface {
	ResourceStream
	// Serve relays a bidirectional byte stream.
	Serve(context.Context, net.Conn) error
}

// OCIResourceStream follows one agent-owned OCI operation.
type OCIResourceStream interface {
	ResourceStream
	// Follow emits ordered OCI events until the operation ends.
	Follow(context.Context, func(protocol.OCIEvent) error) error
}

// ResourceSnapshot is the non-secret resource activity exposed by agent
// status.
type ResourceSnapshot struct {
	ActiveResources uint64
	ActiveLeases    uint64
}

// Options bounds all work that may otherwise retain a client connection.
type Options struct {
	AcquireTimeout         time.Duration
	ReleaseTimeout         time.Duration
	StartupGrace           time.Duration
	IdleCheckInterval      time.Duration
	ClientShutdownGrace    time.Duration
	ClientShutdownMargin   time.Duration
	TransportShutdownGrace time.Duration
	MaxConcurrentRPCs      int
	ResourceLogs           *resourcelog.Service
	Logger                 *diagnostic.Logger
}

func (o Options) withDefaults() Options {
	if o.AcquireTimeout <= 0 {
		o.AcquireTimeout = 2 * time.Minute
	}
	if o.ReleaseTimeout <= 0 {
		o.ReleaseTimeout = shutdown.ClientResourceReleaseGrace
	}
	if o.StartupGrace <= 0 {
		o.StartupGrace = 15 * time.Second
	}
	if o.IdleCheckInterval <= 0 {
		o.IdleCheckInterval = 250 * time.Millisecond
	}
	if o.ClientShutdownGrace <= 0 {
		o.ClientShutdownGrace = shutdown.AgentClientGrace
	}
	if o.ClientShutdownMargin <= 0 {
		o.ClientShutdownMargin = shutdown.AgentClientMargin
	}
	if o.ClientShutdownMargin >= o.ClientShutdownGrace {
		o.ClientShutdownMargin = o.ClientShutdownGrace / 5
	}
	if o.TransportShutdownGrace <= 0 {
		o.TransportShutdownGrace = shutdown.AgentTransportGrace
	}
	if o.MaxConcurrentRPCs <= 0 {
		o.MaxConcurrentRPCs = 256
	}

	return o
}

// ServeOptions controls the lifetime policy for one agent invocation.
type ServeOptions struct {
	Persistent bool
}
