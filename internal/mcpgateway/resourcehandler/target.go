package resourcehandler

// Converts normalized MCP resource configuration into backend requests.

import (
	"encoding/json"
	"fmt"

	"petris.dev/toby/internal/agent/resourcelease"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/config/mcpresource"
	"petris.dev/toby/internal/mcpgateway"
)

func targetRequest(
	configuration mcpresource.Config,
	request resourcelease.StreamRequest,
) (mcpgateway.TargetRequest, mcpgateway.TargetClass, error) {
	switch configuration.Type {
	case mcpresource.TypeBuiltin:
		if configuration.Builtin == nil {
			return mcpgateway.TargetRequest{}, "", fmt.Errorf(
				"built-in MCP definition is unavailable",
			)
		}
		session, err := json.Marshal(configuration.Builtin.Session)
		if err != nil {
			return mcpgateway.TargetRequest{}, "", fmt.Errorf(
				"encode built-in MCP session: %w",
				err,
			)
		}
		return mcpgateway.TargetRequest{
			ResourceID: request.Resource.ID,
			Caller:     request.Caller,
			Name:       mcpgateway.BuiltinTarget,
			Session:    session,
		}, mcpgateway.TargetBuiltin, nil
	case mcpresource.TypeConfigured:
		if configuration.Server == nil {
			return mcpgateway.TargetRequest{}, "", fmt.Errorf(
				"configured MCP server is unavailable",
			)
		}
		spec := targetSpec(*configuration.Server, configuration.ScopeIdentity)
		return mcpgateway.TargetRequest{
			ResourceID: request.Resource.ID,
			Caller:     request.Caller,
			Name:       "resource",
			Spec:       spec,
		}, targetClass(spec), nil
	default:
		return mcpgateway.TargetRequest{}, "", fmt.Errorf(
			"MCP resource has unsupported type %q",
			configuration.Type,
		)
	}
}

func targetClass(spec mcpgateway.TargetSpec) mcpgateway.TargetClass {
	if spec.Type == mcpgateway.TargetRemote {
		return mcpgateway.TargetRemoteHTTP
	}
	if spec.Transport == mcpgateway.TransportStdio {
		return mcpgateway.TargetLocalStdio
	}

	return mcpgateway.TargetLocalHTTP
}

func targetSpec(
	server mcpconfig.Server,
	scopeIdentity string,
) mcpgateway.TargetSpec {
	result := mcpgateway.TargetSpec{
		Type:          mcpgateway.TargetType(server.Type),
		Transport:     mcpgateway.Transport(server.Transport),
		URL:           server.URL,
		Headers:       cloneStringMap(server.Headers),
		Image:         server.Image,
		Command:       append([]string(nil), server.Command...),
		Environment:   cloneStringMap(server.Environment),
		Scope:         server.Scope,
		ScopeIdentity: scopeIdentity,
		Network:       server.Network,
		IdleTimeout: mcpgateway.Duration{
			Duration: server.IdleTimeout,
		},
	}
	if server.Endpoint != nil {
		result.Endpoint = &mcpgateway.Endpoint{
			Kind:   mcpgateway.EndpointKind(server.Endpoint.Kind),
			Socket: server.Endpoint.Socket,
			Path:   server.Endpoint.Path,
		}
	}
	if server.Mounts != nil {
		result.Mounts = make(
			[]mcpgateway.Mount,
			len(server.Mounts),
		)
		for index, item := range server.Mounts {
			result.Mounts[index] = mcpgateway.Mount{
				Source: item.Source,
				Target: item.Target,
				Access: item.Access,
				Scope:  item.Scope,
			}
		}
	}

	return result
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}

	return clone
}
