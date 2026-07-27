// Package mcpconfig defines the strict native configuration contract for
// agent-owned MCP backends.
package mcpconfig

import (
	"time"

	"petris.dev/toby/internal/agent/resource"
	"petris.dev/toby/internal/sandbox/mount"
)

const (
	// DefaultIdleTimeout is the inactivity period used for a shared local HTTP
	// backend when neither the block nor the server overrides it.
	DefaultIdleTimeout = 10 * time.Minute

	maxServers = 128
)

// ServerType identifies whether Toby launches a local OCI sidecar or connects
// to a remote HTTP service.
type ServerType string

const (
	// ServerLocal selects a locally launched MCP server.
	ServerLocal ServerType = "local"
	// ServerRemote selects a remote MCP server.
	ServerRemote ServerType = "remote"
)

// Transport identifies the upstream protocol surface. Applications still
// reach every native target through the run-scoped stdio connector.
type Transport string

const (
	// TransportStdio selects the MCP stdio transport.
	TransportStdio Transport = "stdio"
	// TransportHTTP selects the MCP streamable HTTP transport.
	TransportHTTP Transport = "http"
)

// EndpointKind identifies how a local HTTP sidecar exposes its listener.
type EndpointKind string

const (
	// EndpointUnix selects a Unix-domain sidecar endpoint.
	EndpointUnix EndpointKind = "unix"
)

// Config is one resolved native `resources.mcps` block.
type Config struct {
	Servers map[string]Server
}

// Server is one strict native MCP backend definition. Local HTTP entries carry
// an effective positive IdleTimeout after successful loading; remote and stdio
// entries carry zero.
type Server struct {
	Type        ServerType        `json:"type"`
	Transport   Transport         `json:"transport"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	Image       string            `json:"image,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Endpoint    *Endpoint         `json:"endpoint,omitempty"`
	Mounts      []Mount           `json:"mounts,omitempty"`
	Scope       resource.Scope    `json:"scope,omitempty"`
	Network     resource.Network  `json:"network,omitempty"`
	IdleTimeout time.Duration     `json:"idle_timeout,omitempty"`
}

// Endpoint describes one local HTTP listener and its MCP request path.
type Endpoint struct {
	Kind   EndpointKind `json:"kind"`
	Socket string       `json:"socket"`
	Path   string       `json:"path"`
}

// Mount is one host bind definition for a local sidecar. Scope declares the
// minimum sharing boundary introduced by the mounted data.
type Mount struct {
	Source string         `json:"source"`
	Target string         `json:"target"`
	Access mount.Access   `json:"access"`
	Scope  resource.Scope `json:"scope"`
}

// NormalizeServer applies per-resource defaults and validates one detached
// effective MCP configuration.
func NormalizeServer(server Server) (Server, error) {
	config := Config{
		Servers: map[string]Server{
			"resource": server.clone(),
		},
	}
	if server.Type == ServerLocal &&
		server.Transport == TransportHTTP &&
		server.IdleTimeout == 0 {
		server.IdleTimeout = DefaultIdleTimeout
		config.Servers["resource"] = server
	}
	if err := config.Validate(); err != nil {
		return Server{}, err
	}

	return config.Servers["resource"].clone(), nil
}

// Clone returns a detached configuration suitable for passing between
// process-level services without sharing secret-bearing maps or mutable slices.
func (c Config) Clone() Config {
	clone := Config{
		Servers: make(map[string]Server, len(c.Servers)),
	}
	for name, server := range c.Servers {
		clone.Servers[name] = server.clone()
	}

	return clone
}

// EffectiveScope applies mount narrowing to one local server's requested
// sharing scope.
func (s Server) EffectiveScope() (resource.Scope, error) {
	mounts := make([]resource.Mount, len(s.Mounts))
	for index, item := range s.Mounts {
		mounts[index] = resource.Mount{
			Source: item.Source,
			Target: item.Target,
			Access: string(item.Access),
			Scope:  item.Scope,
		}
	}

	return resource.EffectiveScope(
		s.Scope,
		mounts,
		resource.RunAuthorityAbsent,
	)
}

func (s Server) clone() Server {
	clone := s
	clone.Headers = cloneStringMap(s.Headers)
	clone.Command = append([]string(nil), s.Command...)
	clone.Environment = cloneStringMap(s.Environment)
	clone.Mounts = append([]Mount(nil), s.Mounts...)
	if s.Endpoint != nil {
		endpoint := *s.Endpoint
		clone.Endpoint = &endpoint
	}

	return clone
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	clone := make(map[string]string, len(values))
	for name, value := range values {
		clone[name] = value
	}

	return clone
}
