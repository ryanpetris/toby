package resourcelease

// Defines the resource-specific resolution boundary and safe registry counts.

import (
	"context"
	"encoding/json"

	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/agent/server"
	"petris.dev/toby/internal/resourcehash"
)

// Resolver applies one resource kind's defaults, validates its effective
// configuration, and computes its stable resource identity.
type Resolver interface {
	// Kind returns the supported resource kind.
	Kind() protocol.ResourceKind
	// Resolve validates configuration and computes agent identity.
	Resolve(context.Context, json.RawMessage) (Resolved, error)
}

// ResourceOpener activates and opens raw streams for one resource kind.
type ResourceOpener interface {
	// Kind returns the supported resource kind.
	Kind() protocol.ResourceKind
	// Open activates and opens one resource stream.
	Open(context.Context, StreamRequest) (server.ResourceStream, error)
}

// RuntimeLifecycle observes lease demand and agent shutdown so a
// resource-specific runtime can implement warm-idle retention.
type RuntimeLifecycle interface {
	ResourceOpener
	// LeaseAcquired records a newly active lease.
	LeaseAcquired(Resolved)
	// LeaseReleased records a released lease.
	LeaseReleased(Resolved)
	// Shutdown stops all retained runtime resources.
	Shutdown(context.Context) error
}

// RuntimeLister reports agent resources whose opener-owned runtime remains
// active independently of lease registration. The registry uses these opaque
// identities only for non-secret status and resource-list snapshots.
type RuntimeLister interface {
	// RuntimeResourceIDs returns active resource identifiers.
	RuntimeResourceIDs() []protocol.ResourceID
}

// ModelsOperator performs one-resource model discovery and cache invalidation
// behind the same lease authority as raw model streams.
type ModelsOperator interface {
	// ListModels discovers models through one leased resource.
	ListModels(context.Context, StreamRequest) (map[string]any, error)
	// FlushModelsCache invalidates cached models for one resource.
	FlushModelsCache(Resolved)
}

// StreamRequest carries agent-private configuration and the live launch
// authority belonging to the lease that opened the stream.
type StreamRequest struct {
	Resource Resolved
	Caller   server.HostActionCaller
}

// Resolved is one effective resource configuration and its stable identity.
// Configuration remains agent-private and must never be logged or returned.
type Resolved struct {
	ID            protocol.ResourceID
	Digest        resourcehash.Digest
	Kind          protocol.ResourceKind
	Configuration any
}

// Snapshot contains only non-secret registry counts.
type Snapshot struct {
	ActiveResources uint64
	ActiveLeases    uint64
}
