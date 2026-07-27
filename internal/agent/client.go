package agent

// Lazily resolves runtime paths and constructs the launch-side client only
// when an agent operation is requested.

import (
	"context"
	"fmt"
	"sync"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/agent/protocol"
	"petris.dev/toby/internal/config"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/diagnostic/warning"
	"petris.dev/toby/internal/version"
)

// Client exposes per-user agent operations to CLI and launch orchestration.
type Client struct {
	paths    config.Paths
	warnings *warning.Service
	logger   *diagnostic.Logger

	mu      sync.Mutex
	service *agentclient.Service
}

// NewClient constructs a lazy agent client.
func NewClient(
	paths config.Paths,
	warnings *warning.Service,
	diagnostics *diagnostic.Service,
) *Client {
	return &Client{
		paths:    paths,
		warnings: warnings,
		logger:   diagnostics.Logger("agent.client"),
	}
}

// Status reads safe process state without autostarting a missing agent.
func (c *Client) Status(
	ctx context.Context,
) (protocol.ServiceStatusResponse, error) {
	service, err := c.clientService()
	if err != nil {
		return protocol.ServiceStatusResponse{}, err
	}
	return service.Status(ctx)
}

// OpenAgent connects to an existing agent without starting one.
func (c *Client) OpenAgent(
	ctx context.Context,
	handler agentclient.HostActionHandler,
) (*agentclient.AgentSession, error) {
	service, err := c.clientService()
	if err != nil {
		return nil, err
	}

	return service.OpenAgent(ctx, handler)
}

// Stop signals the agent without autostarting a missing instance.
func (c *Client) Stop(ctx context.Context) error {
	service, err := c.clientService()
	if err != nil {
		return err
	}
	return service.Stop(ctx)
}

// Connect opens one persistent agent session before resource
// configuration is processed.
func (c *Client) Connect(
	ctx context.Context,
	handler agentclient.HostActionHandler,
) (*agentclient.AgentSession, error) {
	service, err := c.clientService()
	if err != nil {
		return nil, err
	}

	return service.Connect(ctx, handler)
}

func (c *Client) clientService() (*agentclient.Service, error) {
	if c == nil {
		return nil, fmt.Errorf("agent client is nil")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.service != nil {
		return c.service, nil
	}

	runtimePaths, err := c.paths.ResolveRuntime()
	if err != nil {
		return nil, err
	}

	agentSocket, systemdManaged := preferredAgentSocket(
		runtimePaths.AgentSocket,
	)
	var launcher agentclient.Launcher
	if !systemdManaged {
		launcher, err = agentclient.NewCommandLauncher(c.logger)
		if err != nil {
			return nil, err
		}
	}
	service, err := agentclient.NewService(
		agentSocket,
		version.String(),
		launcher,
		agentclient.ServiceOptions{
			Warnings: c.warnings,
			Logger:   c.logger,
		},
	)
	if err != nil {
		return nil, err
	}
	c.service = service

	return c.service, nil
}
