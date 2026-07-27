package resource

// Declares the complete resource specification consumed by Builder and the
// deliberately opaque Key it produces.

import (
	"fmt"
	"time"
)

// Kind identifies the reusable resource implementation.
type Kind string

const (
	// KindMCPStdio identifies a stdio MCP resource.
	KindMCPStdio Kind = "mcp-stdio"
	// KindMCPHTTP identifies an HTTP MCP resource.
	KindMCPHTTP Kind = "mcp-http"
	// KindCaddy identifies the shared Caddy proxy.
	KindCaddy Kind = "caddy"
)

// Transport identifies the resource's application transport.
type Transport string

const (
	// TransportStdio selects standard input and output.
	TransportStdio Transport = "stdio"
	// TransportHTTP selects HTTP.
	TransportHTTP Transport = "http"
)

// Network identifies the resource's network isolation mode.
type Network string

const (
	// NetworkHost uses the host network namespace.
	NetworkHost Network = "host"
	// NetworkPrivate uses a private network namespace.
	NetworkPrivate Network = "private"
)

// EndpointKind identifies how a resource exposes its listener.
type EndpointKind string

const (
	// EndpointNone means the resource has no listener.
	EndpointNone EndpointKind = "none"
	// EndpointTCP selects a TCP listener.
	EndpointTCP EndpointKind = "tcp"
	// EndpointUnix selects a Unix-domain listener.
	EndpointUnix EndpointKind = "unix"
)

// Endpoint describes a resource listener.
type Endpoint struct {
	Kind   EndpointKind
	Port   uint16
	Socket string
	Path   string
}

// Identity contains the process user and group.
type Identity struct {
	UID int
	GID int
}

// EnvironmentVariable is an exact process environment entry. Sensitive values
// are HMAC-fingerprinted before entering the canonical identity.
type EnvironmentVariable struct {
	Name      string
	Value     string
	Sensitive bool
}

// MountSourceIdentity names the exact pinned inode behind a configured mount
// path. It participates only in the opaque canonical resource key.
type MountSourceIdentity struct {
	Device   uint64
	Inode    uint64
	FileType uint32
}

// Mount describes one resource filesystem mount.
type Mount struct {
	Source         string
	SourceIdentity MountSourceIdentity
	Target         string
	Access         string
	Scope          Scope
}

// Spec is the complete input to reusable-resource identity. RequestedScope is
// the broadest sharing the caller permits. Builder narrows it according to
// mounts and the required RunAuthority declaration. ScopeIdentity is empty
// when the resulting effective scope is user and is the canonical home,
// project, or run identifier for narrower effective scopes.
type Spec struct {
	Kind            Kind
	Transport       Transport
	ManifestDigest  string
	RootFSDigest    string
	Argv            []string
	Workdir         string
	Identity        Identity
	Environment     []EnvironmentVariable
	Endpoint        Endpoint
	Mounts          []Mount
	Network         Network
	IdleTimeout     time.Duration
	BridgeVersion   string
	ProtocolVersion string
	RequestedScope  Scope
	RunAuthority    RunAuthority
	ScopeIdentity   string
}

// Key is an opaque canonical resource identity. It intentionally exposes no
// canonical document or secret fingerprints.
type Key struct {
	digest    [32]byte
	kind      Kind
	transport Transport
	scope     Scope
}

var _ fmt.Stringer = Key{}

// Digest returns the canonical resource digest.
func (k Key) Digest() string {
	return fmt.Sprintf("sha256:%x", k.digest)
}

// String returns the canonical textual representation.
func (k Key) String() string {
	return k.Digest()
}

// Summary is the safe status/log representation of a resource key.
type Summary struct {
	ID        string
	Kind      Kind
	Transport Transport
	Scope     Scope
}

// Summary returns the non-secret resource identity fields.
func (k Key) Summary() Summary {
	return Summary{
		ID:        k.Digest(),
		Kind:      k.kind,
		Transport: k.transport,
		Scope:     k.scope,
	}
}
