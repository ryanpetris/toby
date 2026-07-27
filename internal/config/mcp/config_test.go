package mcpconfig

// Exercises native MCP loading, strict schema rejection, and cross-field
// resource policy.

import (
	"strings"
	"testing"
	"time"

	"petris.dev/toby/internal/agent/resource"
	configfile "petris.dev/toby/internal/config/file"
	"petris.dev/toby/internal/sandbox/mount"
)

func TestResolveSubstitutionsDetachesAndValidates(t *testing.T) {
	t.Parallel()

	config := Config{
		Servers: map[string]Server{
			"remote": {
				Type:      ServerRemote,
				Transport: TransportHTTP,
				URL:       "https://example.invalid/{token}",
				Headers:   map[string]string{"Authorization": "Bearer {token}"},
			},
			"local": {
				Type:        ServerLocal,
				Transport:   TransportStdio,
				Image:       "ghcr.io/example/mcp:latest",
				Command:     []string{"/bin/server", "{token}"},
				Environment: map[string]string{"TOKEN": "{token}"},
				Scope:       resource.ScopeRun,
				Network:     resource.NetworkPrivate,
			},
		},
	}

	resolved, err := config.ResolveSubstitutions(func(value string) (string, error) {
		return strings.ReplaceAll(value, "{token}", "secret"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Servers["remote"].URL != "https://example.invalid/secret" ||
		resolved.Servers["remote"].Headers["Authorization"] != "Bearer secret" ||
		resolved.Servers["local"].Command[1] != "secret" ||
		resolved.Servers["local"].Environment["TOKEN"] != "secret" {
		t.Fatalf("resolved = %#v", resolved)
	}
	if config.Servers["local"].Environment["TOKEN"] != "{token}" {
		t.Fatal("ResolveSubstitutions mutated source config")
	}
}

func TestDecodeWithSubstitutionsValidatesOnlyResolvedURL(t *testing.T) {
	t.Parallel()

	config, err := DecodeWithSubstitutions(
		map[string]any{
			"remote": map[string]any{
				"type":      "remote",
				"transport": "http",
				"url":       "{endpoint}",
			},
		},
		func(value string) (string, error) {
			return strings.ReplaceAll(
				value,
				"{endpoint}",
				"https://example.invalid/mcp",
			), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := config.Servers["remote"].URL; got != "https://example.invalid/mcp" {
		t.Fatalf("resolved URL = %q", got)
	}
}

func TestNativeDecodeRejectsInvalidShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		block   string
		wantErr string
	}{
		{
			name:    "unknown server field",
			block:   mcpUnknownServerFieldFixture,
			wantErr: "unknown field \"unexpected\"",
		},
		{
			name:    "unknown endpoint field",
			block:   mcpUnknownEndpointFieldFixture,
			wantErr: "unknown field \"unexpected\"",
		},
		{
			name:    "string command",
			block:   mcpInvalidCommandTypeFixture,
			wantErr: "cannot unmarshal",
		},
		{
			name:    "empty local field on remote",
			block:   mcpRemoteCommandFixture,
			wantErr: "local-only field \"command\"",
		},
		{
			name:    "null endpoint on stdio",
			block:   mcpStdioEndpointFixture,
			wantErr: "must not define endpoint",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			block := decodeMCPFixture(t, test.block)
			_, err := DecodeWithSubstitutions(
				block,
				func(value string) (string, error) {
					return value, nil
				},
			)
			if err == nil ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf(
					"DecodeWithSubstitutions() error = %v, want containing %q",
					err,
					test.wantErr,
				)
			}
		})
	}
}

func TestNativeConfigRejectsAmbiguousDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  func() Config
		wantErr string
	}{
		{
			name: "reserved Toby server",
			config: func() Config {
				config := validConfig()
				config.Servers["toby"] = validRemoteServer()
				return config
			},
			wantErr: "reserved for Toby",
		},
		{
			name: "implicit type",
			config: func() Config {
				config := validConfig()
				server := config.Servers["remote"]
				server.Type = ""
				config.Servers["remote"] = server
				return config
			},
			wantErr: "unsupported type",
		},
		{
			name: "implicit transport",
			config: func() Config {
				config := validConfig()
				server := config.Servers["local"]
				server.Transport = ""
				config.Servers["local"] = server
				return config
			},
			wantErr: "unsupported transport",
		},
		{
			name: "remote stdio",
			config: func() Config {
				config := validConfig()
				server := config.Servers["remote"]
				server.Transport = TransportStdio
				config.Servers["remote"] = server
				return config
			},
			wantErr: "must use http",
		},
		{
			name: "remote local field",
			config: func() Config {
				config := validConfig()
				server := config.Servers["remote"]
				server.Image = "ghcr.io/example/mcp:latest"
				config.Servers["remote"] = server
				return config
			},
			wantErr: "local-only fields",
		},
		{
			name: "local missing image",
			config: func() Config {
				config := validConfig()
				server := config.Servers["local"]
				server.Image = ""
				config.Servers["local"] = server
				return config
			},
			wantErr: "image must be",
		},
		{
			name: "local missing scope",
			config: func() Config {
				config := validConfig()
				server := config.Servers["local"]
				server.Scope = ""
				config.Servers["local"] = server
				return config
			},
			wantErr: "scope is required",
		},
		{
			name: "local invalid network",
			config: func() Config {
				config := validConfig()
				server := config.Servers["local"]
				server.Network = "outbound"
				config.Servers["local"] = server
				return config
			},
			wantErr: "network has unsupported",
		},
		{
			name: "stdio endpoint",
			config: func() Config {
				config := validConfig()
				server := config.Servers["local"]
				server.Endpoint = &Endpoint{
					Kind: EndpointKind("tcp"),
					Path: "/mcp",
				}
				config.Servers["local"] = server
				return config
			},
			wantErr: "must not define an endpoint",
		},
		{
			name: "stdio idle timeout",
			config: func() Config {
				config := validConfig()
				server := config.Servers["local"]
				server.IdleTimeout = time.Minute
				config.Servers["local"] = server
				return config
			},
			wantErr: "must not define idleTimeout",
		},
		{
			name: "http missing endpoint",
			config: func() Config {
				config := validConfig()
				server := validLocalHTTPServer()
				server.Endpoint = nil
				config.Servers["local"] = server
				return config
			},
			wantErr: "requires an endpoint",
		},
		{
			name: "Unix socket outside runtime bind",
			config: func() Config {
				config := validConfig()
				server := validLocalHTTPServer()
				server.Endpoint.Socket = "/run/server/mcp.sock"
				config.Servers["local"] = server
				return config
			},
			wantErr: "directly beneath /run/toby",
		},
		{
			name: "endpoint request fragment",
			config: func() Config {
				config := validConfig()
				server := validLocalHTTPServer()
				server.Endpoint.Path = "/mcp#fragment"
				config.Servers["local"] = server
				return config
			},
			wantErr: "query or fragment",
		},
		{
			name: "controlled header",
			config: func() Config {
				config := validConfig()
				server := config.Servers["remote"]
				server.Headers = map[string]string{
					"MCP-Session-Id": "fixed",
				}
				config.Servers["remote"] = server
				return config
			},
			wantErr: "reserved",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := test.config().Validate()
			if err == nil ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf(
					"Validate() error = %v, want containing %q",
					err,
					test.wantErr,
				)
			}
		})
	}
}

func TestNativeConfigRejectsBridgeControlledHeaders(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"Accept",
		"Connection",
		"Content-Length",
		"Content-Type",
		"Host",
		"Keep-Alive",
		"Last-Event-ID",
		"MCP-Protocol-Version",
		"MCP-Session-ID",
		"Proxy-Authorization",
		"Proxy-Connection",
		"TE",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := validConfig()
			server := config.Servers["remote"]
			server.Headers = map[string]string{name: "value"}
			config.Servers["remote"] = server

			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), "reserved") {
				t.Fatalf(
					"Validate() header %q error = %v, want reserved-header rejection",
					name,
					err,
				)
			}
		})
	}
}

func TestNativeConfigRejectsUnsafeBridgeHeaderValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"control\x01byte",
		"delete\x7fbyte",
	} {
		config := validConfig()
		server := config.Servers["remote"]
		server.Headers = map[string]string{"Authorization": value}
		config.Servers["remote"] = server

		err := config.Validate()
		if err == nil || !strings.Contains(err.Error(), "invalid value") {
			t.Fatalf(
				"Validate() header value %q error = %v, want invalid-value rejection",
				value,
				err,
			)
		}
	}
}

func TestNativeConfigRejectsRuntimeControlledSidecarEnvironment(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"HOME", "TOBY_SANDBOX"} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := validConfig()
			server := config.Servers["local"]
			server.Environment = map[string]string{name: "override"}
			config.Servers["local"] = server

			err := config.Validate()
			if err == nil ||
				!strings.Contains(err.Error(), "runtime-controlled") {
				t.Fatalf(
					"Validate() environment %q error = %v, want runtime-controlled rejection",
					name,
					err,
				)
			}
		})
	}
}

func TestNativeConfigRejectsUnsafeMountPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mounts  []Mount
		wantErr string
	}{
		{
			name: "relative source",
			mounts: []Mount{{
				Source: "state",
				Target: "/state",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			}},
			wantErr: "clean absolute host path",
		},
		{
			name: "device access",
			mounts: []Mount{{
				Source: "/srv/state",
				Target: "/state",
				Access: mount.AccessDev,
				Scope:  resource.ScopeHome,
			}},
			wantErr: "unsupported access",
		},
		{
			name: "user scope",
			mounts: []Mount{{
				Source: "/srv/state",
				Target: "/state",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeUser,
			}},
			wantErr: "unsupported scope",
		},
		{
			name: "overlapping targets",
			mounts: []Mount{
				{
					Source: "/srv/state",
					Target: "/state",
					Access: mount.AccessRegular,
					Scope:  resource.ScopeHome,
				},
				{
					Source: "/srv/cache",
					Target: "/state/cache",
					Access: mount.AccessReadOnly,
					Scope:  resource.ScopeHome,
				},
			},
			wantErr: "overlaps",
		},
		{
			name: "reserved proc target",
			mounts: []Mount{{
				Source: "/srv/state",
				Target: "/proc/server",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			}},
			wantErr: "reserved path",
		},
		{
			name: "reserved dev target",
			mounts: []Mount{{
				Source: "/srv/state",
				Target: "/dev",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			}},
			wantErr: "reserved path",
		},
		{
			name: "reserved tmp target",
			mounts: []Mount{{
				Source: "/srv/state",
				Target: "/tmp/cache",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			}},
			wantErr: "reserved path",
		},
		{
			name: "reserved run target",
			mounts: []Mount{{
				Source: "/srv/state",
				Target: "/run/toby/state",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			}},
			wantErr: "reserved path",
		},
		{
			name: "root overlaps reserved targets",
			mounts: []Mount{{
				Source: "/srv/state",
				Target: "/",
				Access: mount.AccessRegular,
				Scope:  resource.ScopeHome,
			}},
			wantErr: "reserved path",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := validConfig()
			server := config.Servers["local"]
			server.Mounts = test.mounts
			config.Servers["local"] = server

			err := config.Validate()
			if err == nil ||
				!strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf(
					"Validate() error = %v, want containing %q",
					err,
					test.wantErr,
				)
			}
		})
	}
}

func validConfig() Config {
	return Config{
		Servers: map[string]Server{
			"local":  validLocalStdioServer(),
			"remote": validRemoteServer(),
		},
	}
}

func validRemoteServer() Server {
	return Server{
		Type:      ServerRemote,
		Transport: TransportHTTP,
		URL:       "https://example.invalid/mcp",
	}
}

func validLocalStdioServer() Server {
	return Server{
		Type:      ServerLocal,
		Transport: TransportStdio,
		Image:     "ghcr.io/example/mcp:latest",
		Command:   []string{"/bin/server"},
		Scope:     resource.ScopeRun,
		Network:   resource.NetworkPrivate,
	}
}

func validLocalHTTPServer() Server {
	return Server{
		Type:      ServerLocal,
		Transport: TransportHTTP,
		Image:     "ghcr.io/example/mcp:latest",
		Command:   []string{"/bin/server"},
		Endpoint: &Endpoint{
			Kind:   EndpointUnix,
			Socket: "/run/toby/mcp.sock",
			Path:   "/mcp",
		},
		Scope:   resource.ScopeUser,
		Network: resource.NetworkHost,
	}
}

func decodeMCPFixture(t *testing.T, block string) map[string]any {
	t.Helper()

	document := map[string]any{}
	if err := configfile.Decode(
		[]byte("resources:\n  mcps:\n"+block),
		configfile.FormatYAML,
		"fixture",
		&document,
	); err != nil {
		t.Fatal(err)
	}

	resources := document["resources"].(map[string]any)
	return resources["mcps"].(map[string]any)
}
