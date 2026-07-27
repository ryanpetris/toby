// Package tobymcp is the agent-hosted MCP server Toby exposes to tools
// running inside a native sandbox. It composes host Git tools, a resources.read
// fallback, and sandbox-safe toby:// session and documentation resources.
package tobymcp

import (
	"context"
	"fmt"
	"strings"

	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/version"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Runner serves the complete sandbox MCP surface.
type Runner struct {
	tools     []Tool
	resources []Resource
	logger    *diagnostic.Logger
}

// NewRunner validates and combines MCP contributions.
func NewRunner(params RunnerParams) (*Runner, error) {
	seenTools := map[string]bool{}
	seenResources := map[string]bool{}
	var tools []Tool
	var resources []Resource
	for _, contributor := range params.Contributors {
		if contributor == nil {
			continue
		}
		for _, tool := range contributor.Tools() {
			if tool.Name == "" {
				return nil, fmt.Errorf("mcp tool must define a name")
			}
			if tool.Register == nil {
				return nil, fmt.Errorf("mcp tool %s must define a register function", tool.Name)
			}
			if seenTools[tool.Name] {
				return nil, fmt.Errorf("duplicate mcp tool: %s", tool.Name)
			}
			seenTools[tool.Name] = true
			tools = append(tools, tool)
		}
		for _, resource := range contributor.Resources() {
			if err := validateResource(resource); err != nil {
				return nil, err
			}
			if seenResources[resource.URI] {
				return nil, fmt.Errorf("duplicate mcp resource: %s", resource.URI)
			}
			seenResources[resource.URI] = true
			resources = append(resources, resource)
		}
	}
	var logger *diagnostic.Logger
	if params.Diagnostics != nil {
		logger = params.Diagnostics.Logger("agent.mcp")
	}

	return &Runner{
		tools:     tools,
		resources: resources,
		logger:    logger,
	}, nil
}

func (r *Runner) server(
	client GitClient,
	snapshot SessionSnapshot,
) *mcp.Server {
	session := &Session{
		Git:       client,
		Snapshot:  snapshot.Clone(),
		Resources: append([]Resource(nil), r.resources...),
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "toby", Version: version.String()}, &mcp.ServerOptions{
		Instructions: strings.TrimSuffix(serverInstructions, "\n"),
	})
	for _, tool := range r.tools {
		tool.Register(mcpServer, session)
	}
	for _, resource := range r.resources {
		resource.Register(mcpServer, session)
	}
	return mcpServer
}

// Serve connects a fresh MCP server to one transport and waits for the
// connection to end. Canceling ctx closes the connection before returning.
func (r *Runner) Serve(
	ctx context.Context,
	client GitClient,
	snapshot SessionSnapshot,
	transport mcp.Transport,
) error {
	if r == nil {
		return fmt.Errorf("serve Toby MCP session: runner is nil")
	}
	if ctx == nil {
		return fmt.Errorf("serve Toby MCP session: context is nil")
	}
	if transport == nil {
		return fmt.Errorf("serve Toby MCP session: transport is nil")
	}
	if err := snapshot.Validate(); err != nil {
		return fmt.Errorf("serve Toby MCP session: invalid snapshot: %w", err)
	}

	session, err := r.server(client, snapshot).Connect(ctx, transport, nil)
	if err != nil {
		return fmt.Errorf("connect Toby MCP session: %w", err)
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- session.Wait()
	}()

	select {
	case err := <-waitResult:
		if err != nil {
			return fmt.Errorf("serve Toby MCP session: %w", err)
		}
		return nil
	case <-ctx.Done():
		closeErr := session.Close()
		waitErr := <-waitResult
		r.logger.DebugError("close canceled Toby MCP session", closeErr)
		r.logger.DebugError("finish canceled Toby MCP session", waitErr)
		return ctx.Err()
	}
}
