package localhttp

// Defines the shared-process registry boundary and generation-bound connector
// lease used by local HTTP MCP sessions.

import (
	"context"
	"fmt"
	"net/http"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/mcpgateway/httpbridge"
)

// Pool canonicalizes definitions and owns reusable ready process generations.
// Prepare must not start a process; Preparation.Acquire performs readiness
// and obtains one service lease.
type Pool interface {
	// Prepare validates and canonicalizes a local HTTP definition.
	Prepare(
		context.Context,
		Definition,
		mcpgateway.ProgressReporter,
	) (Preparation, error)
	// Shutdown stops all pool-owned services.
	Shutdown(context.Context) error
}

// Preparation is one canonical local HTTP definition awaiting readiness.
type Preparation interface {
	// Acquire starts or reuses the canonical service.
	Acquire(context.Context) (ServiceLease, error)
	// Close releases retained preparation capabilities.
	Close() error
}

// ServiceLease is one run's reference to a ready shared generation. Revoke
// synchronously rejects and closes this run's connector leases without stopping
// a generation used by another run.
type ServiceLease interface {
	// OpenConnector retains one service generation for a logical session.
	OpenConnector(context.Context) (ServiceConnector, error)
	// Revoke rejects and closes connector leases for this run.
	Revoke()
	// Release relinquishes the service lease.
	Release(context.Context) error
}

// ServiceConnector pins one exact process generation for one logical MCP
// session and supplies its host-only HTTP endpoint.
type ServiceConnector interface {
	// Upstream returns the pinned service endpoint.
	Upstream() (httpbridge.Upstream, error)
	// Done closes when the pinned generation exits.
	Done() <-chan struct{}
	// Err returns the pinned generation's terminal error.
	Err() error
	// Close releases the connector's generation reference.
	Close()
}

// Definition is the complete secret-bearing reusable process identity. String
// redacts every field so diagnostics cannot expose its configuration.
type Definition struct {
	Image         string
	Command       []string
	Environment   map[string]string
	Endpoint      mcpgateway.Endpoint
	Mounts        []mcpgateway.Mount
	Scope         resource.Scope
	ScopeIdentity string
	Network       resource.Network
	IdleTimeout   mcpgateway.Duration
}

var _ fmt.Stringer = Definition{}

// String returns a fully redacted representation.
func (Definition) String() string {
	return "{Image:<redacted> Command:<redacted> Environment:<redacted> Endpoint:<redacted> Mounts:<redacted> Scope:<redacted> Network:<redacted> IdleTimeout:<redacted>}"
}

func (d Definition) clone() Definition {
	clone := d
	clone.Command = append([]string(nil), d.Command...)
	clone.Mounts = append([]mcpgateway.Mount(nil), d.Mounts...)
	clone.Environment = make(map[string]string, len(d.Environment))
	for name, value := range d.Environment {
		clone.Environment[name] = value
	}

	return clone
}

func definitionFromSpec(spec mcpgateway.TargetSpec) Definition {
	environment := make(map[string]string, len(spec.Environment))
	for name, value := range spec.Environment {
		environment[name] = value
	}

	endpoint := mcpgateway.Endpoint{}
	if spec.Endpoint != nil {
		endpoint = *spec.Endpoint
	}
	return Definition{
		Image:         spec.Image,
		Command:       append([]string(nil), spec.Command...),
		Environment:   environment,
		Endpoint:      endpoint,
		Mounts:        append([]mcpgateway.Mount(nil), spec.Mounts...),
		Scope:         spec.Scope,
		ScopeIdentity: spec.ScopeIdentity,
		Network:       spec.Network,
		IdleTimeout:   spec.IdleTimeout,
	}
}

func cloneUpstream(upstream httpbridge.Upstream) httpbridge.Upstream {
	headers := make(http.Header, len(upstream.Headers))
	for name, values := range upstream.Headers {
		headers[name] = append([]string(nil), values...)
	}

	return httpbridge.Upstream{
		Endpoint:   upstream.Endpoint,
		Headers:    headers,
		HTTPClient: upstream.HTTPClient,
	}
}
