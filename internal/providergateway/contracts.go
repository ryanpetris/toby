package providergateway

// Declares the models gateway's Caddy-generation and model-discovery
// boundaries so orchestration remains independently testable.

import (
	"context"
	"net"

	"petris.dev/toby/internal/agent/protocol"
)

// ProgressReporter receives bounded provider startup stages and attached
// subprocess output for the requesting acquisition.
type ProgressReporter interface {
	// Report records one models resource acquisition event.
	Report(protocol.AcquireProgress) error
}

// Connector retains one Caddy generation while a relay connection is active.
type Connector interface {
	// Done closes when the retained Caddy generation exits.
	Done() <-chan struct{}
	// Close releases the generation reference.
	Close()
}

// Generation is one ready Caddy process generation retained by the gateway.
// Load applies a complete native-JSON configuration. DialData connects through
// the retained runtime-directory capability, and OpenConnector retains the
// generation for that relay connection.
type Generation interface {
	// Generation returns the monotonically increasing generation number.
	Generation() uint64
	// DialData opens a Caddy data-plane connection.
	DialData(context.Context) (*net.UnixConn, error)
	// Load applies a Caddy configuration document.
	Load(context.Context, []byte) error
	// OpenConnector retains the generation for one relay.
	OpenConnector() (Connector, error)
	// Done closes when the generation exits.
	Done() <-chan struct{}
	// Err returns the generation's terminal error.
	Err() error
	// Release relinquishes the generation lease.
	Release()
}

// Pool supplies one shared Caddy generation and owns its restart/backoff
// registry.
type Pool interface {
	// Acquire starts or reuses a Caddy generation.
	Acquire(context.Context, ProgressReporter) (Generation, error)
	// Shutdown stops all pool-owned generations.
	Shutdown(context.Context) error
}

// ModelDiscoverer resolves models through a sandbox-safe capability route.
type ModelDiscoverer interface {
	// Discover retrieves model identifiers through a capability route.
	Discover(
		context.Context,
		ProviderDescriptor,
	) (map[string]any, error)
}

// GatewayFactory lazily constructs process-wide models gateway facilities
// only when the first provider request is acquired.
type GatewayFactory func(context.Context, ProgressReporter) (*Gateway, error)
