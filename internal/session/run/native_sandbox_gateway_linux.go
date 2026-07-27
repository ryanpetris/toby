//go:build linux

package run

// Builds the shared sandbox gRPC endpoint from all launch resource openers.

import (
	"fmt"
	"path/filepath"

	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/sandbox/layout"
	"petris.dev/toby/internal/sandboxgateway"
	"petris.dev/toby/internal/uuid"
)

type nativeSandboxGateway struct {
	endpoint   *sandboxgateway.Endpoint
	capability *sandboxgateway.Capability
	logger     *diagnostic.Logger
}

func acquireNativeSandboxGateway(
	paths config.Paths,
	logger *diagnostic.Logger,
	groups ...map[string]sandboxgateway.Opener,
) (*nativeSandboxGateway, error) {
	openers := make(map[string]sandboxgateway.Opener)
	for _, group := range groups {
		for id, opener := range group {
			if _, duplicate := openers[id]; duplicate {
				return nil, fmt.Errorf(
					"client resource ID %q is duplicated",
					id,
				)
			}
			openers[id] = opener
		}
	}

	runtimePaths, err := paths.ResolveRuntime()
	if err != nil {
		return nil, err
	}
	socketID, err := uuid.NewV4()
	if err != nil {
		return nil, fmt.Errorf(
			"generate sandbox capability socket name: %w",
			err,
		)
	}
	endpoint, err := sandboxgateway.Listen(
		filepath.Join(
			runtimePaths.Root,
			"sandbox-"+socketID+".sock",
		),
		openers,
		sandboxgateway.Options{Logger: logger},
	)
	if err != nil {
		return nil, err
	}

	device, inode := endpoint.SocketGeneration()
	capability, err := sandboxgateway.OpenCapability(
		sandboxgateway.DescriptorConfig{
			HostSocket:       endpoint.Path(),
			HostSocketDevice: device,
			HostSocketInode:  inode,
			SandboxSocket:    layout.SandboxSocket(),
		},
	)
	if err != nil {
		logger.DebugError(
			"close sandbox gateway after capability setup failed",
			endpoint.Close(),
		)
		return nil, err
	}

	return &nativeSandboxGateway{
		endpoint:   endpoint,
		capability: capability,
		logger:     logger,
	}, nil
}

func (g *nativeSandboxGateway) Close() error {
	if g == nil {
		return nil
	}

	if g.endpoint != nil {
		g.logger.DebugError(
			"close sandbox gateway endpoint",
			g.endpoint.Close(),
		)
		g.endpoint = nil
	}
	if g.capability != nil {
		g.logger.DebugError(
			"close sandbox gateway capability",
			g.capability.Close(),
		)
		g.capability = nil
	}

	return nil
}
