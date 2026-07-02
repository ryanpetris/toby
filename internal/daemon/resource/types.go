package resource

// Value types for MCP sidecar backends: the transport/status enums, the per-launch
// Defaults, the resolved SidecarSpec, and the spec/image resolution that turns a
// configured MCP server into a sidecar spec. Moved here (from the old per-launch
// mcpproxy) because the daemon-root registry owns the container mechanics; the
// per-project mcpproxy layer only registers backends on its proxy.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"petris.dev/toby/internal/config/app"
	sandboxruntime "petris.dev/toby/sandbox/runtime"
	"petris.dev/toby/tools"
)

type TransportType string

const (
	TransportStdio TransportType = appconfig.MCPTransportStdio
	TransportHTTP  TransportType = appconfig.MCPTransportHTTP
)

type Status string

const (
	StatusRegistered Status = "registered"
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusExited     Status = "exited"
	StatusFailed     Status = "failed"
	StatusStopped    Status = "stopped"
)

// Defaults are the config-derived sidecar defaults, resolved once per bring-up.
type Defaults struct {
	// Image is the configured `mcp.image` default sidecar image, used for any MCP
	// server that does not specify its own image.
	Image string
	// Build is the configured `mcp.build`; when set it is built once and the
	// resulting image becomes the default sidecar image.
	Build tools.Build
	// ContainerImage is the main sandbox image, used as the fallback default when
	// neither mcp.image nor mcp.build is configured.
	ContainerImage string
	Debug          bool
}

// resolveDefaultImage computes the effective default sidecar image once: the built
// `mcp.build` image when set, then `mcp.image`, then the main sandbox image, then the
// built-in default. Building shells out to the docker CLI.
func resolveDefaultImage(ctx context.Context, defaults Defaults) (string, error) {
	if defaults.Build.IsSet() {
		image, code, err := sandboxruntime.BuildImage(ctx, defaults.Build, strings.TrimSpace(defaults.Image), !defaults.Debug)
		if err != nil {
			return "", err
		}
		if code != 0 {
			return "", fmt.Errorf("mcp image build failed (exit %d)", code)
		}
		return image, nil
	}
	if image := strings.TrimSpace(defaults.Image); image != "" {
		return image, nil
	}
	if image := strings.TrimSpace(defaults.ContainerImage); image != "" {
		return image, nil
	}
	return sandboxruntime.DefaultImage, nil
}

type SidecarSpec struct {
	Name      string
	Transport TransportType
	Command   []string
	Env       map[string]string
	Image     string
	HTTPPort  int
	HTTPPath  string
	Debug     bool
}

// StatusSnapshot is a sanitized view of a backend's runtime state.
type StatusSnapshot struct {
	Name        string
	Status      Status
	Transport   TransportType
	PID         int
	ExitCode    int
	LastError   string
	UpdatedAt   time.Time
	RuntimeInfo map[string]any
}

type ProcessResult struct {
	ExitCode int
	Err      error
}

func sidecarSpec(name string, server appconfig.MCPServer, defaults Defaults) (SidecarSpec, error) {
	command, err := server.CommandParts()
	if err != nil {
		return SidecarSpec{}, fmt.Errorf("mcp.%s: %w", name, err)
	}
	env, err := server.Environment()
	if err != nil {
		return SidecarSpec{}, fmt.Errorf("mcp.%s: %w", name, err)
	}
	transport := server.Transport()
	spec := SidecarSpec{
		Name:      name,
		Transport: TransportType(transport),
		Command:   command,
		Env:       env,
		Image:     sidecarImage(server, defaults),
		HTTPPort:  server.Port(),
		HTTPPath:  server.Path(),
		Debug:     defaults.Debug,
	}
	if spec.Transport == TransportHTTP && spec.HTTPPort <= 0 {
		return SidecarSpec{}, fmt.Errorf("mcp.%s.port is required for http transport", name)
	}
	return spec, nil
}

// sidecarImage picks the sidecar image: the server's own override, otherwise the
// pre-resolved effective default (defaults.Image, set once in Acquire).
func sidecarImage(server appconfig.MCPServer, defaults Defaults) string {
	if image := server.Image(); image != "" {
		return image
	}
	return defaults.Image
}
