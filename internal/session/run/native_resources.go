package run

// Registers launch resources through one persistent agent session and builds
// sandbox-safe configuration from client-owned capability endpoints.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	agentclient "petris.dev/toby/internal/agent/client"
	"petris.dev/toby/internal/config"
	appconfig "petris.dev/toby/internal/config/app"
	"petris.dev/toby/internal/config/mcpresource"
	"petris.dev/toby/internal/config/session"
	"petris.dev/toby/internal/diagnostic"
	"petris.dev/toby/internal/hostaction"
	"petris.dev/toby/internal/lifecycle"
	"petris.dev/toby/internal/sandboxgateway"
	"petris.dev/toby/internal/tobymcp"
)

type nativeHostActionHandler struct {
	mu     sync.RWMutex
	router *hostaction.Router
}

var _ agentclient.HostActionHandler = (*nativeHostActionHandler)(nil)

func (h *nativeHostActionHandler) SetRouter(router *hostaction.Router) {
	h.mu.Lock()
	h.router = router
	h.mu.Unlock()
}

func (h *nativeHostActionHandler) Handle(
	ctx context.Context,
	payload json.RawMessage,
) (json.RawMessage, error) {
	h.mu.RLock()
	router := h.router
	h.mu.RUnlock()
	if router == nil {
		return nil, fmt.Errorf(
			"host action router is not ready",
		)
	}

	request, err := hostaction.DecodeRequest(payload)
	if err != nil {
		response := hostaction.ResponseError(
			nil,
			hostaction.CodeInvalidRequest,
			err.Error(),
			nil,
		)
		return json.RawMessage(response), err
	}

	response, handleErr := router.Handle(ctx, request)
	return json.RawMessage(response), handleErr
}

type nativeResourceInput struct {
	RunID          string
	Paths          config.Paths
	Session        *agentclient.AgentSession
	Snapshot       tobymcp.SessionSnapshot
	Configuration  *appconfig.Service
	Resources      appconfig.ResourcesConfig
	Identities     mcpresource.ScopeIdentities
	Logger         *diagnostic.Logger
	CleanupContext func() context.Context
}

type nativeResources struct {
	Sandbox      *sandboxgateway.Capability
	Gateway      *nativeSandboxGateway
	MCPResources *nativeMCPResources
	Models       *nativeModelsResources
	Config       sessionconfig.Config
}

func acquireNativeResources(
	ctx context.Context,
	input nativeResourceInput,
) (result nativeResources, returnErr error) {
	if ctx == nil {
		return nativeResources{}, fmt.Errorf(
			"acquire native resources: context is nil",
		)
	}
	if input.Session == nil {
		return nativeResources{}, fmt.Errorf(
			"acquire native resources: agent session is required",
		)
	}
	if input.Configuration == nil {
		return nativeResources{}, fmt.Errorf(
			"acquire native resources: configuration is required",
		)
	}

	var modelsResources *nativeModelsResources
	var err error
	if len(input.Resources.Models) != 0 {
		modelsResources, err = acquireNativeModelsResources(
			ctx,
			input.Session,
			input.Configuration,
			input.Resources.Models,
			input.Logger,
			input.CleanupContext,
		)
		if err != nil {
			return nativeResources{}, err
		}
		defer func() {
			if returnErr != nil && modelsResources != nil {
				cleanupCtx := context.Background()
				if input.CleanupContext != nil {
					cleanupCtx = input.CleanupContext()
				}
				input.Logger.DebugError(
					"close models resources after acquisition failure",
					modelsResources.CloseContext(cleanupCtx),
				)
			}
		}()
	}

	var modelsEndpoints []sessionconfig.ModelsEndpoint
	if modelsResources != nil {
		modelsEndpoints = modelsResources.endpoints
	}
	snapshot := input.Snapshot.Clone()
	snapshot.Models = nativeSnapshotModels(modelsEndpoints)
	if err := snapshot.Validate(); err != nil {
		return nativeResources{}, fmt.Errorf(
			"update native session snapshot models: %w",
			err,
		)
	}

	mcpResources, err := acquireNativeMCPResources(
		ctx,
		input.Session,
		input.Resources.MCPs,
		input.Identities,
		snapshot,
		input.RunID,
		input.Logger,
		input.CleanupContext,
	)
	if err != nil {
		return nativeResources{}, err
	}
	defer func() {
		if returnErr != nil && mcpResources != nil {
			cleanupCtx := context.Background()
			if input.CleanupContext != nil {
				cleanupCtx = input.CleanupContext()
			}
			input.Logger.DebugError(
				"close MCP resources after acquisition failure",
				mcpResources.CloseContext(cleanupCtx),
			)
		}
	}()

	var modelOpeners map[string]sandboxgateway.Opener
	if modelsResources != nil {
		modelOpeners = modelsResources.openers
	}
	gateway, err := acquireNativeSandboxGateway(
		input.Paths,
		input.Logger,
		mcpResources.openers,
		modelOpeners,
	)
	if err != nil {
		return nativeResources{}, err
	}
	defer func() {
		if returnErr != nil && gateway != nil {
			input.Logger.DebugError(
				"close sandbox gateway after acquisition failure",
				gateway.Close(),
			)
		}
	}()

	config, err := nativeSessionConfig(
		input.Configuration,
		mcpResources.servers,
		modelsEndpoints,
		input.Snapshot.Projects,
	)
	if err != nil {
		return nativeResources{}, err
	}

	result = nativeResources{
		Sandbox:      gateway.capability,
		Gateway:      gateway,
		MCPResources: mcpResources,
		Models:       modelsResources,
		Config:       config,
	}
	mcpResources = nil
	modelsResources = nil
	gateway = nil

	return result, nil
}

func (r *nativeResources) CloseContext(ctx context.Context) error {
	if r == nil {
		return nil
	}

	var result error
	if r.Gateway != nil {
		result = errors.Join(result, r.Gateway.Close())
		r.Gateway = nil
		r.Sandbox = nil
	}
	if r.Models != nil {
		result = errors.Join(result, r.Models.CloseContext(ctx))
		r.Models = nil
	}
	if r.MCPResources != nil {
		result = errors.Join(result, r.MCPResources.CloseContext(ctx))
		r.MCPResources = nil
	}

	return result
}

func nativeSessionConfig(
	config *appconfig.Service,
	mcpServers []sessionconfig.MCPServer,
	modelsEndpoints []sessionconfig.ModelsEndpoint,
	projects []tobymcp.SessionProject,
) (sessionconfig.Config, error) {
	instructions, err := config.ResolveInstructions()
	if err != nil {
		return sessionconfig.Config{}, err
	}
	contents := make([][]byte, len(instructions))
	for index, instruction := range instructions {
		contents[index] = append([]byte(nil), instruction.Contents...)
	}
	contents = append(contents, lifecycle.SandboxInstructions())

	result := sessionconfig.Config{
		MCPServers: append(
			[]sessionconfig.MCPServer(nil),
			mcpServers...,
		),
		Models: append(
			[]sessionconfig.ModelsEndpoint(nil),
			modelsEndpoints...,
		),
		Projects:    nativeSessionProjectPaths(projects),
		Permissions: config.PermissionPaths(),
		Instructions: sessionconfig.Instructions{
			Contents: contents,
		},
	}
	for _, server := range result.MCPServers {
		if err := server.Validate(); err != nil {
			return sessionconfig.Config{}, err
		}
	}

	return result.Clone(), nil
}

func nativeSessionProjectPaths(
	projects []tobymcp.SessionProject,
) []string {
	paths := make([]string, len(projects))
	for index, project := range projects {
		paths[index] = project.SandboxPath
	}

	return paths
}
