//go:build linux

package run

// Acquires MCP resources and publishes their sandbox-visible configurations.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/clientresource"
	"petris.dev/toby/internal/agent/protocol"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/config/mcpresource"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/mcpgateway"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandboxgateway"
	"petris.dev/toby/internal/shutdown"
	"petris.dev/toby/internal/tobymcp"
)

const nativeMCPReleaseTimeout = shutdown.ClientResourceReleaseGrace

type nativeMCPResources struct {
	registry *clientresource.Registry
	openers  map[string]sandboxgateway.Opener
	servers  []sessionconfig.MCPServer
}

func acquireNativeMCPResources(
	ctx context.Context,
	session *agentclient.AgentSession,
	servers map[string]mcpconfig.Server,
	identities mcpresource.ScopeIdentities,
	snapshot tobymcp.SessionSnapshot,
	runIdentity string,
	logger *diagnostic.Logger,
	cleanupContext func() context.Context,
) (result *nativeMCPResources, returnErr error) {
	if ctx == nil {
		return nil, fmt.Errorf("acquire native MCP resources: context is nil")
	}
	if session == nil {
		return nil, fmt.Errorf(
			"acquire native MCP resources: agent session is required",
		)
	}

	registry, err := clientresource.NewRegistry(
		protocol.ResourceMCP,
		session,
		logger.With("resource_kind", protocol.ResourceMCP),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			logger.DebugError(
				"close MCP resource registry after acquisition failure",
				closeNativeMCPRegistry(registry, cleanupContext),
			)
		}
	}()

	type requestedResource struct {
		name          string
		configuration mcpresource.Config
	}
	builtin, err := mcpresource.Builtin(
		snapshot,
		runIdentity,
	)
	if err != nil {
		return nil, err
	}
	requested := []requestedResource{{
		name:          mcpgateway.BuiltinTarget,
		configuration: builtin,
	}}

	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		configuration, err := mcpresource.Configured(
			servers[name],
			identities,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"resolve MCP resource %q: %w",
				name,
				err,
			)
		}
		requested = append(requested, requestedResource{
			name:          name,
			configuration: configuration,
		})
	}

	openers := make(
		map[string]sandboxgateway.Opener,
		len(requested),
	)
	sessionServers := make(
		[]sessionconfig.MCPServer,
		0,
		len(requested),
	)
	for _, item := range requested {
		clientID, err := registry.Acquire(ctx, item.configuration)
		if err != nil {
			return nil, fmt.Errorf(
				"acquire MCP resource %q: %w",
				item.name,
				err,
			)
		}

		target := string(clientID)
		sessionServers = append(
			sessionServers,
			sessionconfig.MCPServer{
				Name:      item.name,
				Transport: sessionconfig.MCPTransportStdio,
				Command:   layout.SandboxBinary(),
				Args: []string{
					"resource",
					"connect",
					"--",
					target,
				},
			},
		)
		openers[target] = sandboxgateway.OpenFunc(
			func(
				openContext context.Context,
			) (io.ReadWriteCloser, error) {
				return registry.Open(openContext, clientID)
			},
		)
	}
	sort.Slice(sessionServers, func(i, j int) bool {
		return sessionServers[i].Name < sessionServers[j].Name
	})

	return &nativeMCPResources{
		registry: registry,
		openers:  openers,
		servers:  sessionServers,
	}, nil
}

func (r *nativeMCPResources) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}

	var result error
	r.openers = nil
	if r.registry != nil {
		result = errors.Join(result, r.registry.Close(ctx))
		r.registry = nil
	}

	return result
}

func closeNativeMCPRegistry(
	registry *clientresource.Registry,
	cleanupContext func() context.Context,
) error {
	if registry == nil {
		return nil
	}
	if cleanupContext != nil {
		return registry.Close(cleanupContext())
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		nativeMCPReleaseTimeout,
	)
	defer cancel()

	return registry.Close(ctx)
}
