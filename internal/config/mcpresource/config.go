package mcpresource

// Applies typed configured and built-in MCP defaults before client
// registration or resource identity hashing.

import (
	"fmt"

	"petris.dev/toby/internal/agent/resource"
	mcpconfig "petris.dev/toby/internal/config/mcp"
	"petris.dev/toby/internal/tobymcp"
)

// Type selects the configured backend or Toby's launch-scoped built-in MCP.
type Type string

const (
	// TypeConfigured identifies a user-configured MCP resource.
	TypeConfigured Type = "configured"
	// TypeBuiltin identifies an MCP resource provided by Toby.
	TypeBuiltin Type = "builtin"
)

// ScopeIdentities supplies the exact launch identity for each shareable local
// MCP scope.
type ScopeIdentities struct {
	Home    string
	Project string
	Run     string
}

// BuiltinConfig is the launch-scoped, sandbox-safe built-in MCP definition.
type BuiltinConfig struct {
	RunIdentity string                  `json:"run_identity"`
	Session     tobymcp.SessionSnapshot `json:"session"`
}

// Config is one independently acquired MCP resource. Exactly one of Server or
// Builtin is populated after normalization.
type Config struct {
	Type          Type              `json:"type"`
	Server        *mcpconfig.Server `json:"server,omitempty"`
	ScopeIdentity string            `json:"scope_identity,omitempty"`
	Builtin       *BuiltinConfig    `json:"builtin,omitempty"`
}

// Configured constructs one client-side configured MCP resource with its
// effective sharing identity.
func Configured(
	server mcpconfig.Server,
	identities ScopeIdentities,
) (Config, error) {
	normalized, err := mcpconfig.NormalizeServer(server)
	if err != nil {
		return Config{}, err
	}

	scope := resource.ScopeUser
	if normalized.Type == mcpconfig.ServerLocal {
		scope, err = normalized.EffectiveScope()
		if err != nil {
			return Config{}, err
		}
	}
	identity, err := scopeIdentity(scope, identities)
	if err != nil {
		return Config{}, err
	}

	return Normalize(Config{
		Type:          TypeConfigured,
		Server:        &normalized,
		ScopeIdentity: identity,
	})
}

// Builtin constructs one client-side launch-scoped Toby MCP resource.
func Builtin(
	snapshot tobymcp.SessionSnapshot,
	runIdentity string,
) (Config, error) {
	return Normalize(Config{
		Type: TypeBuiltin,
		Builtin: &BuiltinConfig{
			RunIdentity: runIdentity,
			Session:     snapshot,
		},
	})
}

// Normalize applies MCP defaults and validates the selected resource shape.
func Normalize(input Config) (Config, error) {
	switch input.Type {
	case TypeConfigured:
		return normalizeConfigured(input)
	case TypeBuiltin:
		return normalizeBuiltin(input)
	default:
		return Config{}, fmt.Errorf(
			"MCP resource has unsupported type %q",
			input.Type,
		)
	}
}

func normalizeConfigured(input Config) (Config, error) {
	if input.Server == nil {
		return Config{}, fmt.Errorf(
			"configured MCP resource requires a server",
		)
	}
	if input.Builtin != nil {
		return Config{}, fmt.Errorf(
			"configured MCP resource must not contain a built-in definition",
		)
	}

	server, err := mcpconfig.NormalizeServer(*input.Server)
	if err != nil {
		return Config{}, err
	}
	result := Config{
		Type:          TypeConfigured,
		Server:        &server,
		ScopeIdentity: input.ScopeIdentity,
	}

	scope := resource.ScopeUser
	if server.Type == mcpconfig.ServerLocal {
		scope, err = server.EffectiveScope()
		if err != nil {
			return Config{}, err
		}
	}
	if scope == resource.ScopeUser {
		if result.ScopeIdentity != "" {
			return Config{}, fmt.Errorf(
				"user-scoped MCP resource must not have a scope identity",
			)
		}
		return result, nil
	}
	if result.ScopeIdentity == "" {
		return Config{}, fmt.Errorf(
			"%s-scoped MCP resource requires a scope identity",
			scope,
		)
	}

	return result, nil
}

func normalizeBuiltin(input Config) (Config, error) {
	if input.Server != nil || input.ScopeIdentity != "" {
		return Config{}, fmt.Errorf(
			"built-in MCP resource must not contain configured-server fields",
		)
	}
	if input.Builtin == nil {
		return Config{}, fmt.Errorf(
			"built-in MCP resource requires a definition",
		)
	}
	if input.Builtin.RunIdentity == "" {
		return Config{}, fmt.Errorf(
			"built-in MCP resource requires a run identity",
		)
	}
	if err := input.Builtin.Session.Validate(); err != nil {
		return Config{}, fmt.Errorf(
			"validate built-in MCP session: %w",
			err,
		)
	}

	return Config{
		Type: TypeBuiltin,
		Builtin: &BuiltinConfig{
			RunIdentity: input.Builtin.RunIdentity,
			Session:     input.Builtin.Session.Clone(),
		},
	}, nil
}

func scopeIdentity(
	scope resource.Scope,
	identities ScopeIdentities,
) (string, error) {
	switch scope {
	case resource.ScopeUser:
		return "", nil
	case resource.ScopeHome:
		if identities.Home != "" {
			return identities.Home, nil
		}
	case resource.ScopeProject:
		if identities.Project != "" {
			return identities.Project, nil
		}
	case resource.ScopeRun:
		if identities.Run != "" {
			return identities.Run, nil
		}
	default:
		return "", fmt.Errorf(
			"MCP resource has unsupported effective scope %q",
			scope,
		)
	}

	return "", fmt.Errorf(
		"%s-scoped MCP resource requires a scope identity",
		scope,
	)
}
